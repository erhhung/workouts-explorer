package migrations

import "testing"

func TestIngestCoveragePipelineMigrationContract(t *testing.T) {
	source, err := Files.ReadFile("028_ingest_coverage_pipeline.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"CREATE TABLE app.coverage_batch_policy", "batch_size BETWEEN 1 AND 1000", "GREATEST(25,",
		"CHECK (parent_batch_size >= 1)", "CREATE TABLE app.ingest_coverage_handoffs", "CREATE TABLE app.adopted_coverage_handoff_receipts",
		"CREATE TRIGGER workout_import_events_coverage_handoff AFTER INSERT ON app.workout_import_events",
		"NEW.kind IN ('created','updated','adopted')", "handoff.origin = 'normal'", "CREATE FUNCTION app.configure_coverage_batch_size(runtime_version integer, size integer)",
		"CREATE OR REPLACE FUNCTION app.enqueue_coverage_update(", "needed := jsonb_array_length(additions)",
		"job.retry_of_job_id IS NULL", "context.parent_batch_size = capacity", "child.parent_job_id = job.id",
		"app.coverage_generation_vectors_equal(context.target_generations,generations)",
		"CREATE FUNCTION app.read_ingest_coverage_batch(", "CREATE FUNCTION app.apply_ingest_coverage_batch(",
		"CREATE FUNCTION app.defer_ingest_coverage_batch(", "CREATE FUNCTION app.read_pending_ingest_coverage_workouts(",
		"capability.runtime_version >= 28", "record_adopted_coverage_receipt", "FOR UPDATE NOWAIT",
		"minimum_runtime_version = 28", "schema_version = 28", "forward-only",
	} {
		if !sqlStrings.Contains(string(source), required) {
			t.Errorf("pipeline migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"parent_batch_size BETWEEN 1 AND 1000", "parent_batch_size <= 1000", "NO TRANSACTION", "ALTER TABLE app.matcher_slot_limits"} {
		if sqlStrings.Contains(string(source), forbidden) {
			t.Errorf("pipeline migration contains %q", forbidden)
		}
	}
}
