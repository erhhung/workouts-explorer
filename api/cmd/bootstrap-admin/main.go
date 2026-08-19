package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"

	apiapp "github.com/erhhung/workouts-explorer/api"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/logging"
)

func main() {
	logger := logging.New("workouts-bootstrap-admin")
	if err := run(context.Background()); err != nil {
		logger.Error("bootstrap administrator failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	logger.Info("bootstrap administrator is present")
}

func run(ctx context.Context) error {
	username := flag.String("username", "", "administrator username")
	email := flag.String("email", "", "administrator email")
	passwordFile := flag.String("password-file", "", "path to a private password file")
	passwordMinimum := flag.Int("password-minimum", 0, "minimum password length in Unicode scalar values")
	rotateExistingPassword := flag.Bool("rotate-existing-password", false, "rotate the matching existing administrator password")
	flag.Parse()
	if flag.NArg() != 0 || *username == "" || *email == "" || *passwordFile == "" || *passwordMinimum == 0 {
		return errors.New("--username, --email, --password-file, and --password-minimum are required")
	}
	databaseURL := os.Getenv("BOOTSTRAP_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("BOOTSTRAP_DATABASE_URL is required")
	}
	db, err := database.Open(ctx, databaseURL, "workouts-bootstrap-admin")
	if err != nil {
		return errors.New("database is unavailable")
	}
	defer db.Close()
	return apiapp.BootstrapAdmin(ctx, db, apiapp.BootstrapAdminOptions{Username: *username, Email: *email, PasswordFile: *passwordFile, PasswordMin: *passwordMinimum, RotateExistingPassword: *rotateExistingPassword})
}
