package migrations_test

import (
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestCoverageOriginCleanupContract(t *testing.T) {
	raw, err := migrations.Files.ReadFile("028_ingest_coverage_pipeline.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"context.route_input_revision=$3", "context.route_input_sha256=$4",
		"app.coverage_generation_vectors_equal(context.target_generations,$5)",
		"canonical_existing := FOUND", "IF canonical_existing THEN",
		"CREATE FUNCTION app.ingest_coverage_cleanup_due(", "CREATE FUNCTION app.cleanup_ingest_coverage_origins(",
		"handoff.event_id=retired.event_id", "handoff.source_id=retired.source_id",
		"handoff.source_file_id=retired.source_file_id", "handoff.ingest_job_id=retired.ingest_job_id",
		"handoff.content_sha256=retired.content_sha256", "handoff.ordinal=retired.ordinal",
		"request.event_id=receipt.event_id", "source.account_id=workout.account_id AND source.deleted_at IS NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing origin/CAS contract %q", required)
		}
	}
	core := strings.Split(strings.Split(sql, "CREATE FUNCTION app.enqueue_coverage_update_core(")[1], "$function$;")[0]
	if !strings.Contains(core, "app.ingest_coverage_existing_target") {
		t.Fatal("ordinary enqueue must share canonical target classification")
	}
	apply := strings.Split(strings.Split(sql, "CREATE FUNCTION app.apply_ingest_coverage_batch(")[1], "$function$;")[0]
	if strings.Index(apply, "canonical_existing := FOUND") > strings.Index(apply, "INSERT INTO app.workout_coverage_states") {
		t.Fatal("capture cancellation before initialization/readiness writes")
	}
	cleanup := strings.Split(strings.Split(sql, "CREATE FUNCTION app.cleanup_ingest_coverage_origins(")[1], "$function$;")[0]
	if strings.Contains(cleanup, "record_adopted_coverage_receipt") || strings.Contains(cleanup, "ack_ingest_coverage_prepared") {
		t.Fatal("orphan cleanup must not use a workout-wide acknowledgement")
	}
}
