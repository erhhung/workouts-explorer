package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/erhhung/workouts-explorer/internal/osm/timezone"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	options := timezone.Options{
		ArchivePath: os.Getenv("TIMEZONE_BOUNDARY_ARCHIVE"),
		Release:     os.Getenv("TIMEZONE_BOUNDARY_RELEASE"),
		SourceURL:   os.Getenv("TIMEZONE_BOUNDARY_SOURCE_URL"),
		SHA256:      os.Getenv("TIMEZONE_BOUNDARY_SHA256"),
	}
	if err := options.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(options.ArchivePath); errors.Is(err, os.ErrNotExist) {
		if err := timezone.DownloadArchive(ctx, options.SourceURL, options.ArchivePath); err != nil {
			return err
		}
		defer os.Remove(options.ArchivePath)
	} else if err != nil {
		return fmt.Errorf("inspect timezone boundary archive: %w", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to OSM import database: %w", err)
	}
	defer pool.Close()
	result, err := timezone.Import(ctx, pool, options)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "promoted timezone dataset %d with %d boundaries\n", result.DatasetID, result.FeatureCount)
	return err
}
