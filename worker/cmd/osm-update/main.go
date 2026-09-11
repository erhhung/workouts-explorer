package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

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
	databaseURL := os.Getenv("OSM_MIGRATION_DATABASE_URL")
	regionID := os.Getenv("OSM_REGION_ID")
	scratch := os.Getenv("OSM_UPDATE_SCRATCH")
	pipelineRoot := os.Getenv("OSM_PIPELINE_ROOT")
	maximumBytes, err := strconv.ParseInt(os.Getenv("OSM_MAX_DOWNLOAD_BYTES"), 10, 64)
	if databaseURL == "" || regionID == "" || scratch == "" || pipelineRoot == "" || err != nil || maximumBytes < 1 {
		return fmt.Errorf("OSM_MIGRATION_DATABASE_URL, OSM_REGION_ID, positive OSM_MAX_DOWNLOAD_BYTES, OSM_UPDATE_SCRATCH, and OSM_PIPELINE_ROOT are required")
	}
	pool, err := database.Open(ctx, databaseURL, "workouts-osm-update")
	if err != nil {
		return err
	}
	defer pool.Close()
	updater := osm.Updater{
		Store:      osm.PostgreSQLUpdateStore{Pool: pool},
		Pipeline:   osm.CommandPipeline{DatabaseURL: databaseURL, Root: pipelineRoot, Log: os.Stdout},
		HTTPClient: &http.Client{Timeout: 2 * time.Hour}, Download: osm.DownloadRegion,
		MaximumBytes: maximumBytes, ScratchRoot: scratch,
	}
	generation, err := updater.Run(ctx, regionID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "promoted OSM region %s generation %d\n", generation.RegionID, generation.ID)
	return nil
}
