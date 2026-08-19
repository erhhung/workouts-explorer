package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/erhhung/workouts-explorer/internal/osm/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const migrationLockID int64 = 824665992842164

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	databaseURL := os.Getenv("OSM_MIGRATION_DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("OSM_MIGRATION_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to OSM migration database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire OSM migration lock: %w", err)
	}
	defer func() { _, _ = db.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID) }()
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("apply OSM migrations: %w", err)
	}
	return nil
}
