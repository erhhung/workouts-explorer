package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
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
	if getenv("OSM_CONTROL_JOB_ID") != "" {
		return runManaged(ctx, getenv, stdout, pool)
	}
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
		if getenv("OSM_RESUME_GENERATION_ID") != "" || getenv("OSM_RESUME_BUILD_ID") != "" {
			return fmt.Errorf("a resume processing attempt ID is only valid with the resume command")
		}
		return runUpdate(ctx, getenv, stdout, pool, store, 0)
	case "resume":
		flags := flag.NewFlagSet("resume", flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(valueOrDefault(getenv("OSM_RESUME_BUILD_ID"), getenv("OSM_RESUME_GENERATION_ID")))
		if parseErr != nil {
			return parseErr
		}
		generationID := buildIDFlag(flags, defaultGeneration)
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *generationID < 1 {
			return fmt.Errorf("resume requires --build")
		}
		return runUpdate(ctx, getenv, stdout, pool, store, *generationID)
	case "status":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(valueOrDefault(getenv("OSM_BUILD_ID"), getenv("OSM_GENERATION_ID")))
		if parseErr != nil {
			return parseErr
		}
		generationID := buildIDFlag(flags, defaultGeneration)
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
		defaultGeneration, parseErr := parseOptionalID(valueOrDefault(getenv("OSM_BUILD_ID"), getenv("OSM_GENERATION_ID")))
		if parseErr != nil {
			return parseErr
		}
		generationID := buildIDFlag(flags, defaultGeneration)
		approvedBy := flags.String("approved-by", getenv("OSM_OPERATOR"), "operator identity")
		note := flags.String("note", getenv("OSM_OPERATOR_NOTE"), "optional clearance note")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *generationID < 1 {
			return fmt.Errorf("unblock requires --build")
		}
		unlock, err := store.LockGeneration(ctx, *generationID)
		if err != nil {
			return err
		}
		defer unlock()
		if err := store.Unblock(ctx, *generationID, *approvedBy, *note); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "unblocked OSM processing attempt %d; resume will run a fresh preflight\n", *generationID)
		return nil
	case "abort":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		flags.SetOutput(stderr)
		defaultGeneration, parseErr := parseOptionalID(valueOrDefault(getenv("OSM_BUILD_ID"), getenv("OSM_GENERATION_ID")))
		if parseErr != nil {
			return parseErr
		}
		generationID := buildIDFlag(flags, defaultGeneration)
		reason := flags.String("reason", getenv("OSM_ABORT_REASON"), "abort reason")
		if err := flags.Parse(arguments); err != nil {
			return err
		}
		if *generationID < 1 || getenv("OSM_REGION_ID") == "" {
			return fmt.Errorf("abort requires --build and OSM_REGION_ID")
		}
		unlock, err := store.LockGeneration(ctx, *generationID)
		if err != nil {
			return err
		}
		defer unlock()
		if err := store.Abort(ctx, *generationID, getenv("OSM_REGION_ID"), *reason); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "aborted OSM processing attempt %d\n", *generationID)
		return nil
	default:
		return fmt.Errorf("unknown osm-update command %q", command)
	}
}

func runUpdate(ctx context.Context, getenv func(string) string, stdout io.Writer, pool storagePool, store osm.PostgreSQLUpdateStore, resumeGenerationID int64) error {
	return runControlledUpdate(ctx, getenv, stdout, pool, store, resumeGenerationID, nil)
}

func runControlledUpdate(ctx context.Context, getenv func(string) string, stdout io.Writer, pool storagePool, store osm.PostgreSQLUpdateStore, resumeGenerationID int64, control *managedControl) error {
	regionID, scratch, pipelineRoot := getenv("OSM_REGION_ID"), getenv("OSM_UPDATE_SCRATCH"), getenv("OSM_PIPELINE_ROOT")
	maximumBytes, err := strconv.ParseInt(getenv("OSM_MAX_DOWNLOAD_BYTES"), 10, 64)
	metrics, metricsErr := osm.StorageMetricsFromEnv(getenv)
	maximumSampleAge, ageErr := durationOrDefault(getenv("OSM_PROMETHEUS_MAX_SAMPLE_AGE"), osm.DefaultMaximumAge)
	storageSettleTimeout, settleErr := durationOrDefault(getenv("OSM_STORAGE_SETTLE_TIMEOUT"), 90*time.Second)
	monitorStorage, monitorErr := strconv.ParseBool(valueOrDefault(getenv("OSM_MONITOR_DATABASE_STORAGE"), "true"))
	if control != nil && control.monitorDatabaseStorage != nil {
		monitorStorage = *control.monitorDatabaseStorage
		monitorErr = nil
	}
	monitorStorage = monitorStorage && metrics.Enabled
	logLocation, locationErr := time.LoadLocation(valueOrDefault(getenv("OSM_LOG_TIMEZONE"), "America/Los_Angeles"))
	if regionID == "" || scratch == "" || pipelineRoot == "" || err != nil || maximumBytes < 1 || metricsErr != nil || (monitorStorage && (ageErr != nil || maximumSampleAge <= 0 || settleErr != nil || storageSettleTimeout < 0)) || monitorErr != nil || locationErr != nil {
		return fmt.Errorf("OSM region, download, scratch, pipeline and enabled storage metrics configuration must be valid")
	}
	databaseURL := getenv("OSM_MIGRATION_DATABASE_URL")
	updater := osm.Updater{
		Store: store, Pipeline: osm.CommandPipeline{DatabaseURL: databaseURL, Root: pipelineRoot, Log: stdout},
		HTTPClient: &http.Client{Timeout: 2 * time.Hour}, Download: osm.DownloadRegion,
		MaximumBytes: maximumBytes, ScratchRoot: scratch, ResumeGenerationID: resumeGenerationID,
		Log: stdout, LogLocation: logLocation,
		StorageSettleTimeout:  storageSettleTimeout,
		SkipStorageMonitoring: !monitorStorage,
		StoragePreflight: osm.PrometheusStoragePreflight{
			DB: pool, HTTPClient: &http.Client{Timeout: 15 * time.Second}, PrometheusURL: metrics.PrometheusURL,
			AvailableMetric: metrics.AvailableMetric, CapacityMetric: metrics.CapacityMetric, SelectorLabels: metrics.SelectorLabels, MaximumAge: maximumSampleAge,
		},
	}
	if control != nil {
		updater.Progress = control
		updater.DiscardOnCancel = func() bool { return control.intent.Load() == "cancel" }
	}
	generation, err := updater.Run(ctx, regionID)
	if err != nil {
		return err
	}
	if number, readErr := store.PromotedGenerationNumber(ctx, generation.ID, generation.RegionID); readErr == nil {
		_, _ = fmt.Fprintf(stdout, "promoted OSM region %s generation %d; processing attempt %d\n", generation.RegionID, number, generation.ID)
	} else {
		_, _ = fmt.Fprintf(stdout, "completed promoted OSM processing attempt %d for %s\n", generation.ID, generation.RegionID)
	}
	return nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func buildIDFlag(flags *flag.FlagSet, value int64) *int64 {
	id := value
	flags.Int64Var(&id, "build", value, "internal processing attempt ID")
	flags.Int64Var(&id, "generation", value, "deprecated alias for --build (internal processing attempt ID)")
	return &id
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
		return 0, fmt.Errorf("invalid processing attempt ID %q", value)
	}
	return id, nil
}
