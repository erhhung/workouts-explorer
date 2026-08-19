package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.OSMDatabaseURL, "workouts-osm-catalog")
	if err != nil {
		return err
	}
	defer pool.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	regions, err := osm.FetchGeofabrikCatalog(ctx, client)
	if err != nil {
		return err
	}
	if err := osm.SyncCatalog(ctx, pool, regions, cfg.OSM.Regions); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "synchronized %d OSM regions\n", len(regions))
	return nil
}
