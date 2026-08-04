package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	apiapp "github.com/erhhung/workouts-explorer/api"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/httpserver"
	"github.com/erhhung/workouts-explorer/internal/logging"
	"github.com/erhhung/workouts-explorer/internal/telemetry"
)

func main() {
	logger := logging.New("workouts-api")
	if err := run(context.Background(), logger); err != nil {
		logger.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}
	shutdownTelemetry, err := telemetry.Setup(ctx, "workouts-api", cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()

	db, err := database.Open(ctx, cfg.DatabaseURL, "workouts-api")
	if err != nil {
		return err
	}
	defer db.Close()
	handler, err := apiapp.NewHandlerContext(ctx, cfg, db, logger)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout(),
		ReadTimeout:       config.ReadTimeout(),
		WriteTimeout:      config.WriteTimeout(),
		IdleTimeout:       config.IdleTimeout(),
	}
	return httpserver.Run(ctx, logger, server, config.ShutdownTimeout())
}
