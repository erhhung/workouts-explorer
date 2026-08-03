package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/httpserver"
	"github.com/erhhung/workouts-explorer/internal/logging"
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
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	shutdownTelemetry, err := telemetry.Setup(ctx, "workouts-worker", cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	db, err := database.Open(ctx, cfg.DatabaseURL, "workouts-worker")
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
	return httpserver.Run(ctx, logger, server, config.ShutdownTimeout())
}
