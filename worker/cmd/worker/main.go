package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/httpserver"
	"github.com/erhhung/workouts-explorer/internal/logging"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/erhhung/workouts-explorer/internal/telemetry"
	workerapp "github.com/erhhung/workouts-explorer/worker"
)

func main() {
	logger := logging.New("workouts-worker")
	if err := run(context.Background(), logger); err != nil {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

type supervisedComponent struct {
	name string
	run  func(context.Context) error
}

type componentResult struct {
	name string
	err  error
}

func supervise(ctx context.Context, shutdownTimeout time.Duration, components ...supervisedComponent) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan componentResult, len(components))
	for _, component := range components {
		component := component
		go func() { results <- componentResult{name: component.name, err: component.run(runCtx)} }()
	}

	remaining := len(components)
	var resultErr error
	select {
	case result := <-results:
		remaining--
		if ctx.Err() == nil {
			if result.err == nil {
				resultErr = fmt.Errorf("%s stopped unexpectedly", result.name)
			} else {
				resultErr = fmt.Errorf("%s stopped: %w", result.name, result.err)
			}
		}
	case <-ctx.Done():
	}
	cancel()

	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case <-results:
			remaining--
		case <-timer.C:
			return errors.New("worker shutdown timed out")
		}
	}
	return resultErr
}

func run(ctx context.Context, logger *slog.Logger) error {
	workerCtx, stopWorker := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopWorker()
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	keys, err := sourcecrypto.LoadKeyring(cfg.SourceKeyringFile)
	if err != nil {
		return errors.New("configure source encryption")
	}
	shutdownTelemetry, err := telemetry.Setup(workerCtx, "workouts-worker", cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	if err := workerapp.ScavengeStaging(cfg.StagingRoot); err != nil {
		return errors.New("prepare worker staging")
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	db, err := database.Open(workerCtx, cfg.DatabaseURL, "workouts-worker")
	if err != nil {
		return err
	}
	defer db.Close()
	osmDB, err := database.Open(workerCtx, cfg.OSMDatabaseURL, "workouts-worker-osm")
	if err != nil {
		return err
	}
	defer osmDB.Close()
	if err := workerapp.ConfigureFileSlotLimits(workerCtx, db, cfg.AccountConcurrency, cfg.GlobalConcurrency); err != nil {
		return err
	}
	if err := workerapp.ConfigureAutoSyncPolicy(workerCtx, db, cfg.AutoSyncInterval, cfg.AutoSyncStaleDays); err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           workerapp.NewHandler(db, osmDB, logger),
		ReadHeaderTimeout: config.ReadHeaderTimeout(),
		ReadTimeout:       config.ReadTimeout(),
		WriteTimeout:      config.WriteTimeout(),
		IdleTimeout:       config.IdleTimeout(),
	}
	runner := workerapp.NewRunnerWithOptions(db, logger, keys, cfg.LocalSourceRoots, workerapp.RunnerOptions{
		FileConcurrency: cfg.FileConcurrency,
	})
	scheduler := workerapp.NewScheduler(db, logger, keys, cfg.LocalSourceRoots, workerapp.SchedulerOptions{
		PollInterval: cfg.AutoSyncPollInterval,
		Lease:        cfg.SchedulerLease,
	})
	return supervise(workerCtx, config.ShutdownTimeout(),
		supervisedComponent{name: "health server", run: func(ctx context.Context) error {
			return httpserver.Run(ctx, logger, server, config.ShutdownTimeout())
		}},
		supervisedComponent{name: "job runner", run: func(ctx context.Context) error {
			return runner.Run(ctx)
		}},
		supervisedComponent{name: "sync scheduler", run: func(ctx context.Context) error {
			return scheduler.Run(ctx)
		}},
	)
}
