// Package migrationfixture exposes semantic checkpoints from active SQL to tests.
package migrationfixture

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
)

const (
	CanonicalFile     = "027_coverage_recovery_baseline.sql"
	RouteCounts       = "route-counts"
	AutoRetries       = "auto-retries"
	RuntimeCapability = "runtime-capability"
	DiagnosticLimits  = "diagnostic-limits"
	RetryPresentation = "retry-presentation"
	CanonicalMetadata = "canonical-metadata"
)

var phases = []string{RouteCounts, AutoRetries, RuntimeCapability, DiagnosticLimits, RetryPresentation, CanonicalMetadata}

// PhaseSQL returns the exact static SQL between a semantic phase's unique markers.
func PhaseSQL(active fs.FS, phase string) ([]byte, error) {
	source, err := fs.ReadFile(active, CanonicalFile)
	if err != nil {
		return nil, err
	}
	begin, end := "-- coverage-contract:"+phase+":begin\n", "-- coverage-contract:"+phase+":end\n"
	text := string(source)
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		return nil, fmt.Errorf("canonical coverage: missing or duplicate phase %q", phase)
	}
	_, rest, _ := strings.Cut(text, begin)
	body, _, found := strings.Cut(rest, end)
	if !found || strings.Contains(body, "+goose") {
		return nil, fmt.Errorf("canonical coverage: invalid phase %q", phase)
	}
	return []byte(body), nil
}

// ApplySQL keeps injected late-failure checks in the same transaction as real DDL.
func ApplySQL(ctx context.Context, db *sql.DB, source string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, source); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyPhase advances a feature checkpoint without touching schema metadata or Goose.
func ApplyPhase(ctx context.Context, db *sql.DB, active fs.FS, phase string) error {
	source, err := PhaseSQL(active, phase)
	if err != nil {
		return err
	}
	return ApplySQL(ctx, db, string(source))
}

// FinishCanonical27 applies phases after the named checkpoint and records the
// genuine canonical version atomically. The normal active provider can then apply 028.
func FinishCanonical27(ctx context.Context, db *sql.DB, active fs.FS, after string) error {
	start := 0
	if after != "" {
		start = -1
		for index, phase := range phases {
			if phase == after {
				start = index + 1
			}
		}
		if start < 0 || start == len(phases) {
			return fmt.Errorf("canonical coverage: invalid feature checkpoint %q", after)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prefix bool
	if err := tx.QueryRowContext(ctx, `SELECT schema_version=26 AND minimum_runtime_version=26
		AND (to_jsonb(metadata)->>'migration_baseline') IS NULL
		AND (SELECT count(*)=27 AND count(DISTINCT version_id)=27 AND min(version_id)=0
		AND max(version_id)=26 AND bool_and(is_applied) FROM public.goose_db_version)
		FROM app.schema_metadata metadata WHERE singleton`).Scan(&prefix); err != nil {
		return err
	}
	if !prefix {
		return fmt.Errorf("canonical coverage: checkpoints require the unchanged schema26 prefix")
	}
	for _, phase := range phases[start:] {
		source, err := PhaseSQL(active, phase)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(source)); err != nil {
			return fmt.Errorf("canonical coverage %s: %w", phase, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(27,true)`); err != nil {
		return err
	}
	return tx.Commit()
}
