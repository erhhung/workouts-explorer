package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("osm-identity-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	region := flags.String("region", getenv("OSM_REGION_ID"), "provider region ID")
	localities := flags.String("localities", "Cupertino,Mountain View,Sunnyvale,Saratoga", "comma-separated locality names")
	fullRegion := flags.Bool("full-region", false, "evaluate the complete active region")
	keepSchema := flags.Bool("keep-schema", false, "retain the scratch schema for inspection")
	maximumSegments := flags.Int64("max-segments", envInt64(getenv("OSM_IDENTITY_EVAL_MAX_SEGMENTS"), 750000), "maximum selected physical segments")
	withoutProjection := flags.Bool("without-application-projection", false, "skip the workouts database projection")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	osmURL, root := getenv("OSM_MIGRATION_DATABASE_URL"), getenv("OSM_PIPELINE_ROOT")
	if osmURL == "" || root == "" || *region == "" || *maximumSegments < 1 {
		return fmt.Errorf("OSM_MIGRATION_DATABASE_URL, OSM_PIPELINE_ROOT, region, and a positive segment limit are required")
	}

	osmPool, err := database.OpenWithMaxConns(ctx, osmURL, "osm-identity-eval", 2)
	if err != nil {
		return err
	}
	defer osmPool.Close()
	var applicationPool interface{ Close() }
	config := osm.IdentityEvaluationConfig{
		OSM: osmPool, OSMDatabaseURL: osmURL, PipelineRoot: root, RegionID: *region,
		Localities: splitLocalities(*localities), FullRegion: *fullRegion, MaximumSegments: *maximumSegments,
		KeepScratchSchema: *keepSchema, Log: stderr,
	}
	if !*withoutProjection {
		applicationURL := getenv("MIGRATION_DATABASE_URL")
		if applicationURL == "" {
			return fmt.Errorf("MIGRATION_DATABASE_URL is required unless application projection is disabled")
		}
		pool, err := database.OpenWithMaxConns(ctx, applicationURL, "osm-identity-eval-application", 2)
		if err != nil {
			return err
		}
		applicationPool, config.Application = pool, pool
		defer applicationPool.Close()
	}

	report, err := osm.EvaluateIdentities(ctx, config)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write identity evaluation report")
	}
	for _, regression := range report.Regressions {
		if !regression.Passed {
			return errors.New("identity evaluation regressions failed")
		}
	}
	for _, check := range report.ContextChecks {
		if !check.Passed {
			return errors.New("identity evaluation context checks failed")
		}
	}
	if report.Identity.RequiredEdgeSplits != 0 {
		return errors.New("identity evaluation retained required edge splits")
	}
	return nil
}

func splitLocalities(value string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func envInt64(value string, fallback int64) int64 {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
