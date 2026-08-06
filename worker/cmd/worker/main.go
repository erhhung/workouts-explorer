package main

import (
	"context"
	"errors"
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
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	db, err := database.Open(workerCtx, cfg.DatabaseURL, "workouts-worker")
	if err != nil {
		return err
	}
	defer db.Close()
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           workerapp.NewHandler(db, logger),
		ReadHeaderTimeout: config.ReadHeaderTimeout(),
		ReadTimeout:       config.ReadTimeout(),
		WriteTimeout:      config.WriteTimeout(),
		IdleTimeout:       config.IdleTimeout(),
	}
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- workerapp.NewRunner(db, logger, keys, cfg.LocalSourceRoots).Run(workerCtx)
	}()
	serverErr := httpserver.Run(workerCtx, logger, server, config.ShutdownTimeout())
	stopWorker()
	var workerErr error
	select {
	case workerErr = <-workerDone:
	case <-time.After(config.ShutdownTimeout()):
		return errors.New("worker shutdown timed out")
	}
	if serverErr != nil {
		return serverErr
	}
	return workerErr
}
