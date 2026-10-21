package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/httpserver"
	"github.com/erhhung/workouts-explorer/internal/logging"
	"github.com/erhhung/workouts-explorer/internal/telemetry"
	workerapp "github.com/erhhung/workouts-explorer/worker"
)

func main() {
	logger := logging.New("workouts-coverage-worker")
	if err := run(context.Background(), logger); err != nil {
		logger.Error("coverage worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := config.LoadCoverageWorker()
	if err != nil {
		return err
	}
	shutdownTelemetry, err := telemetry.Setup(ctx, "workouts-coverage-worker", cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	// Each route can persist while its heartbeat runs. Three extra app/control
	// connections serve reconciliation, automatic retries and health. Discovery uses at most two
	// private probes, plus N private route connections outside the OSM pool.
	// Local OSM maximum: N routes + (N+2) control + 2 probes = 2*N+4.
	n := int32(cfg.JobConcurrency)
	db, err := database.OpenWithMaxConns(ctx, cfg.DatabaseURL, "workouts-coverage-worker", 2*n+3)
	if err != nil {
		return err
	}
	defer db.Close()
	osmDB, err := database.OpenOSMReader(ctx, cfg.OSMDatabaseURL, "workouts-coverage-worker-osm", n+2, cfg.UseReadReplicasOnly)
	if err != nil {
		return err
	}
	defer osmDB.Close()
	reader, err := readerplacement.New(osmDB, cfg.UseReadReplicasOnly, cfg.JobConcurrency)
	if err != nil {
		return err
	}
	defer reader.Close()
	runner := workerapp.NewCoverageRunnerWithOptions(db, osmDB, logger, workerapp.CoverageRunnerOptions{
		PollInterval: cfg.PollInterval, AdmissionInterval: cfg.AdmissionInterval, LeaseDuration: cfg.LeaseDuration,
		HeartbeatInterval: cfg.HeartbeatInterval, RouteTimeout: cfg.RouteTimeout,
		UseReadReplicasOnly: cfg.UseReadReplicasOnly,
		JobConcurrency:      cfg.JobConcurrency, Reader: reader,
		AutoRetryCount: &cfg.AutoRetryCount,
	})
	reconciler := workerapp.NewCoverageReconciler(db, osmDB, logger, workerapp.CoverageReconcilerOptions{
		PollInterval: cfg.ReconciliationPollInterval, ScanInterval: cfg.ReconciliationScanInterval,
		LeaseDuration: cfg.LeaseDuration, PageSize: cfg.ReconciliationPageSize,
		MinimumTraversalMeters: cfg.MinimumTraversalMeters,
		OSMAutoAddRegions:      cfg.OSMAutoAddRegions,
		UseReadReplicasOnly:    cfg.UseReadReplicasOnly,
	})
	server := &http.Server{Addr: cfg.ListenAddress, Handler: workerapp.NewHandler(db, osmDB, logger),
		ReadHeaderTimeout: config.ReadHeaderTimeout(), ReadTimeout: config.ReadTimeout(),
		WriteTimeout: config.WriteTimeout(), IdleTimeout: config.IdleTimeout()}
	return supervise(ctx, runner.Run, reconciler.Run, func(ctx context.Context) error {
		err := httpserver.Run(ctx, logger, server, config.ShutdownTimeout())
		// Shutdown's timeout bounds graceful HTTP draining. Force-close remaining
		// sockets before returning to the supervisor and closing database pools.
		if ctx.Err() != nil {
			err = errors.Join(err, server.Close())
		}
		return err
	})
}

func supervise(ctx context.Context, components ...func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	results := make(chan error, len(components))
	for _, component := range components {
		go func() {
			err := component(ctx)
			if ctx.Err() == nil {
				if err == nil {
					err = errors.New("coverage worker component stopped unexpectedly")
				}
				cancel(err)
			}
			results <- err
		}()
	}
	var result error
	for range components {
		result = errors.Join(result, <-results)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		result = errors.Join(cause, result)
	}
	return result
}
