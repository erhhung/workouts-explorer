package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/erhhung/workouts-explorer/internal/coverage/evaluator"
	"github.com/jackc/pgx/v5/pgxpool"
)

type commandConfig struct {
	applicationDatabaseURL string
	osmDatabaseURL         string
	accountIdentity        string
	evaluation             evaluator.Config
}

func main() {
	if err := run(context.Background(), os.Stdout, os.Stderr, os.Getenv); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, stdout, stderr io.Writer, getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return err
	}
	appDB, err := pgxpool.New(ctx, cfg.applicationDatabaseURL)
	if err != nil {
		return writeStartupFailure(stdout, stderr, cfg.evaluation, "application_database")
	}
	defer appDB.Close()
	osmDB, err := pgxpool.New(ctx, cfg.osmDatabaseURL)
	if err != nil {
		return writeStartupFailure(stdout, stderr, cfg.evaluation, "osm_database")
	}
	defer osmDB.Close()

	runner := evaluator.Runner{
		Source:    evaluator.PostgreSQLSource{Pool: appDB},
		Snapshots: evaluator.OSMSnapshotFactory{Pool: osmDB},
		Windows:   evaluator.Matcher{},
	}
	report, runErr := runner.Run(ctx, cfg.accountIdentity, cfg.evaluation)
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		_, _ = fmt.Fprintln(stderr, "coverage evaluation failed: report_output")
		return errors.New("report_output")
	}
	if runErr != nil {
		category := "evaluation"
		var safe *evaluator.SafeError
		if errors.As(runErr, &safe) {
			category = safe.Category
		}
		_, _ = fmt.Fprintf(stderr, "coverage evaluation failed: %s\n", category)
		return errors.New(category)
	}
	return nil
}

func writeStartupFailure(stdout, stderr io.Writer, cfg evaluator.Config, category string) error {
	report := evaluator.NewReport(cfg)
	report.Errors[category]++
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		category = "report_output"
	}
	_, _ = fmt.Fprintf(stderr, "coverage evaluation failed: %s\n", category)
	return errors.New(category)
}

func loadConfig(getenv func(string) string) (commandConfig, error) {
	cfg := commandConfig{
		applicationDatabaseURL: getenv("MIGRATION_DATABASE_URL"),
		osmDatabaseURL:         getenv("OSM_DATABASE_URL"),
		accountIdentity:        getenv("COVERAGE_EVALUATION_ACCOUNT"),
		evaluation:             evaluator.Config{Limit: 20, MinimumTraversalMeters: 5},
	}
	if cfg.applicationDatabaseURL == "" || cfg.osmDatabaseURL == "" || cfg.accountIdentity == "" {
		return commandConfig{}, errors.New("MIGRATION_DATABASE_URL, OSM_DATABASE_URL, and COVERAGE_EVALUATION_ACCOUNT are required")
	}
	if raw := getenv("COVERAGE_EVALUATION_LIMIT"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			return commandConfig{}, errors.New("COVERAGE_EVALUATION_LIMIT must be between 1 and 500")
		}
		cfg.evaluation.Limit = value
	}
	if raw := getenv("COVERAGE_MIN_TRAVERSAL_METERS"); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.1 || value > 100 {
			return commandConfig{}, errors.New("COVERAGE_MIN_TRAVERSAL_METERS must be between 0.1 and 100")
		}
		cfg.evaluation.MinimumTraversalMeters = value
	}
	return cfg, nil
}
