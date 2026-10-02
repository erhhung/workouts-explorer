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
	db, err := database.OpenWithMaxConns(ctx, cfg.DatabaseURL, "workouts-coverage-worker", 4)
	if err != nil {
		return err
	}
	defer db.Close()
	osmDB, err := database.OpenWithMaxConns(ctx, cfg.OSMDatabaseURL, "workouts-coverage-worker-osm", 3)
	if err != nil {
		return err
	}
	defer osmDB.Close()
	runner := workerapp.NewCoverageRunnerWithOptions(db, osmDB, logger, workerapp.CoverageRunnerOptions{
		PollInterval: cfg.PollInterval, AdmissionInterval: cfg.AdmissionInterval, LeaseDuration: cfg.LeaseDuration,
		HeartbeatInterval: cfg.HeartbeatInterval, RouteTimeout: cfg.RouteTimeout,
	})
	reconciler := workerapp.NewCoverageReconciler(db, osmDB, logger, workerapp.CoverageReconcilerOptions{
		PollInterval: cfg.ReconciliationPollInterval, ScanInterval: cfg.ReconciliationScanInterval,
		LeaseDuration: cfg.LeaseDuration, PageSize: cfg.ReconciliationPageSize,
		MinimumTraversalMeters: cfg.MinimumTraversalMeters,
		OSMAutoAddRegions:      cfg.OSMAutoAddRegions,
	})
	server := &http.Server{Addr: cfg.ListenAddress, Handler: workerapp.NewHandler(db, osmDB, logger),
		ReadHeaderTimeout: config.ReadHeaderTimeout(), ReadTimeout: config.ReadTimeout(),
		WriteTimeout: config.WriteTimeout(), IdleTimeout: config.IdleTimeout()}
	errorsCh := make(chan error, 3)
	go func() { errorsCh <- runner.Run(ctx) }()
	go func() { errorsCh <- reconciler.Run(ctx) }()
	go func() { errorsCh <- httpserver.Run(ctx, logger, server, config.ShutdownTimeout()) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errorsCh:
		if err == nil {
			return errors.New("coverage worker component stopped unexpectedly")
		}
		return err
	}
}
