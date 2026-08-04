package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	apiapp "github.com/erhhung/workouts-explorer/api"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/logging"
)

func main() {
	logger := logging.New("workouts-provision-roles")
	if err := run(context.Background()); err != nil {
		logger.Error("role provisioning failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("required database roles are present")
}

func run(ctx context.Context) error {
	databaseURL := os.Getenv("ROLE_PROVISIONING_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("ROLE_PROVISIONING_DATABASE_URL is required")
	}
	db, err := database.Open(ctx, databaseURL, "workouts-provision-roles")
	if err != nil {
		return errors.New("role provisioning database is unavailable")
	}
	defer db.Close()
	return apiapp.ProvisionRoles(ctx, db)
}
