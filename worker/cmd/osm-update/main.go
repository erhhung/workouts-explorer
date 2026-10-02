package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, getenv func(string) string, stdout, stderr io.Writer) error {
	databaseURL := getenv("OSM_MIGRATION_DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("OSM_MIGRATION_DATABASE_URL is required")
	}
	pool, err := database.Open(ctx, databaseURL, "workouts-osm-update")
	if err != nil {
		return err
	}
	defer pool.Close()
	store := osm.PostgreSQLUpdateStore{Pool: pool}
	command := "start"
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		command, arguments = arguments[0], arguments[1:]
	} else if configured := strings.TrimSpace(getenv("OSM_UPDATE_ACTION")); configured != "" {
		command = configured
	}
	switch command {
	case "start":
		flags := flag.NewFlagSet("start", flag.ContinueOnError)
		flags.SetOutput(stderr)
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if getenv("OSM_RESUME_GENERATION_ID") != "" {
			return fmt.Errorf("OSM_RESUME_GENERATION_ID is only valid with the resume command")
		}
		return runUpdate(ctx, getenv, stdout, pool, store, 0)
	case "resume":
		flags := flag.NewFlagSet("resume", flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(getenv("OSM_RESUME_GENERATION_ID"))
		if parseErr != nil {
			return parseErr
		}
		generationID := flags.Int64("generation", defaultGeneration, "generation to resume")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *generationID < 1 {
			return fmt.Errorf("resume requires --generation")
		}
		return runUpdate(ctx, getenv, stdout, pool, store, *generationID)
	case "status":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(getenv("OSM_GENERATION_ID"))
		if parseErr != nil {
			return parseErr
		}
		generationID := flags.Int64("generation", defaultGeneration, "generation ID")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		status, err := store.Status(ctx, *generationID, getenv("OSM_REGION_ID"))
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	case "unblock":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(getenv("OSM_GENERATION_ID"))
		if parseErr != nil {
			return parseErr
		}
		generationID := flags.Int64("generation", defaultGeneration, "generation ID")
		approvedBy := flags.String("approved-by", getenv("OSM_OPERATOR"), "operator identity")
		note := flags.String("note", getenv("OSM_OPERATOR_NOTE"), "optional clearance note")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *generationID < 1 {
			return fmt.Errorf("unblock requires --generation")
		}
		unlock, err := store.LockGeneration(ctx, *generationID)
		if err != nil {
			return err
		}
		defer unlock()
		if err := store.Unblock(ctx, *generationID, *approvedBy, *note); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "unblocked OSM generation %d; resume will run a fresh preflight\n", *generationID)
		return nil
	case "abort":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(getenv("OSM_GENERATION_ID"))
		if parseErr != nil {
			return parseErr
		}
		generationID := flags.Int64("generation", defaultGeneration, "generation ID")
		reason := flags.String("reason", getenv("OSM_ABORT_REASON"), "abort reason")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *generationID < 1 || getenv("OSM_REGION_ID") == "" {
			return fmt.Errorf("abort requires --generation and OSM_REGION_ID")
		}
		unlock, err := store.LockGeneration(ctx, *generationID)
		if err != nil {
			return err
		}
		defer unlock()
		if err := store.Abort(ctx, *generationID, getenv("OSM_REGION_ID"), *reason); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "aborted OSM generation %d\n", *generationID)
		return nil
	default:
		return fmt.Errorf("unknown osm-update command %q", command)
	}
}

func runUpdate(ctx context.Context, getenv func(string) string, stdout io.Writer, pool storagePool, store osm.PostgreSQLUpdateStore, resumeGenerationID int64) error {
	regionID, scratch, pipelineRoot := getenv("OSM_REGION_ID"), getenv("OSM_UPDATE_SCRATCH"), getenv("OSM_PIPELINE_ROOT")
	maximumBytes, err := strconv.ParseInt(getenv("OSM_MAX_DOWNLOAD_BYTES"), 10, 64)
	prometheusURL := valueOrDefault(getenv("OSM_PROMETHEUS_URL"), osm.DefaultPrometheusURL)
	pvcTemplate := valueOrDefault(getenv("OSM_PROMETHEUS_PVC_TEMPLATE"), osm.DefaultPVCTemplate)
	maximumSampleAge, ageErr := durationOrDefault(getenv("OSM_PROMETHEUS_MAX_SAMPLE_AGE"), osm.DefaultMaximumAge)
	logLocation, locationErr := time.LoadLocation(valueOrDefault(getenv("OSM_LOG_TIMEZONE"), "America/Los_Angeles"))
	if regionID == "" || scratch == "" || pipelineRoot == "" || err != nil || maximumBytes < 1 || ageErr != nil || maximumSampleAge <= 0 || locationErr != nil {
		return fmt.Errorf("OSM region, download, scratch, pipeline, and Prometheus storage-preflight configuration are required")
	}
	databaseURL := getenv("OSM_MIGRATION_DATABASE_URL")
	updater := osm.Updater{
		Store: store, Pipeline: osm.CommandPipeline{DatabaseURL: databaseURL, Root: pipelineRoot, Log: stdout},
		HTTPClient: &http.Client{Timeout: 2 * time.Hour}, Download: osm.DownloadRegion,
		MaximumBytes: maximumBytes, ScratchRoot: scratch, ResumeGenerationID: resumeGenerationID,
		Log: stdout, LogLocation: logLocation,
		StoragePreflight: osm.PrometheusStoragePreflight{
			DB: pool, HTTPClient: &http.Client{Timeout: 15 * time.Second}, PrometheusURL: prometheusURL,
			PVCTemplate: pvcTemplate, MaximumAge: maximumSampleAge,
		},
	}
	generation, err := updater.Run(ctx, regionID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "promoted OSM region %s generation %d\n", generation.RegionID, generation.ID)
	return nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func durationOrDefault(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

type storagePool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func parseOptionalID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("invalid generation ID %q", value)
	}
	return id, nil
}
