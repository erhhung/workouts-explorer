package migrations_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestEligibleCohortCanonical28Contract(t *testing.T) {
	files, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil || len(files) != 28 || files[len(files)-1] != "028_ingest_coverage_pipeline.sql" {
		t.Fatalf("canonical migration history must end at 028: %v %v", files, err)
	}
	raw, err := migrations.Files.ReadFile("028_ingest_coverage_pipeline.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE TABLE app.ingest_coverage_builders", "CREATE TABLE app.ingest_coverage_prepared",
		"UNIQUE(account_id,root_job_id)", "CHECK (batch_size BETWEEN 1 AND 1000)",
		"CREATE FUNCTION app.read_ingest_coverage_prepared(", "CREATE FUNCTION app.seal_ingest_coverage_cohort(",
		"CREATE FUNCTION app.record_ingest_coverage_repair(", "context.sealed_ingest_root_id IS NULL",
		"prior_parent.sealed_ingest_root_id", "ORDER BY parent.id,child.id FOR UPDATE OF child NOWAIT",
		"targets<builder.batch_size", "app.ingest_coverage_root_terminal(target_account_id,builder.root_job_id)",
		"app.ingest_coverage_snapshot_digest(target_account_id,prepared.workout_id)",
		"UPDATE app.schema_metadata SET schema_version = 28,minimum_runtime_version = 28 WHERE singleton;",
		"AFTER UPDATE OF processing_finished_at ON app.workout_coverage_states",
		"NEW.processing_finished_at IS DISTINCT FROM OLD.processing_finished_at",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing eligible-cohort contract: %s", required)
		}
	}
	if strings.Count(sql, "DEFERRABLE INITIALLY DEFERRED FOR EACH ROW") != 4 {
		t.Fatal("all four generation triggers must be deferred in canonical 028")
	}
	prepare := strings.Split(strings.Split(sql, "CREATE FUNCTION app.apply_ingest_coverage_batch(")[1], "$function$;")[0]
	if strings.Contains(prepare, "app.enqueue_") || strings.Contains(prepare, "INSERT INTO app.jobs") {
		t.Fatal("preparation must not create a placeholder or coalesce physical work")
	}
	intake := strings.Split(strings.Split(sql, "CREATE FUNCTION app.read_ingest_coverage_batch(")[1], "$function$;")[0]
	if strings.Contains(intake, "count(*)") || !strings.Contains(intake, "NOT EXISTS (SELECT 1 FROM app.ingest_coverage_prepared") {
		t.Fatal("intake must progress independently of candidate count and skip prepared rows")
	}
	prepared := strings.Split(strings.Split(sql, "CREATE TABLE app.ingest_coverage_prepared (")[1], ");")[0]
	for _, forbidden := range []string{"coordinates", "latitude", "longitude", "geometry", "telemetry"} {
		if strings.Contains(prepared, forbidden) {
			t.Errorf("prepared storage must be metadata-only: %s", forbidden)
		}
	}
}
