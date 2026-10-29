package migrations_test

import (
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

var ingestPhaseMessages = [][2]string{
	{"resuming", "Resuming incomplete sync job."},
	{"preparing", "Preparing to read workout files."},
	{"discovering", "Looking for workout files that need to be processed."},
	{"importing", "Parsing workout files and importing new and changed workouts."},
	{"analyzing_imported_routes", "Analyzing map data for imported routes."},
	{"analyzing_existing_routes", "Analyzing map data for existing routes."},
	{"updating_time_zones", "Determining and updating workout time zones."},
	{"cleanup", "Cleaning up temporary files."},
}

func TestIngestPhaseProgressMigrationContract(t *testing.T) {
	raw, err := migrations.Files.ReadFile("029_ingest_status_reporting.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("expected Up and forward-only Down")
	}
	up, down := parts[0], parts[1]
	_, phase, found := strings.Cut(up, "-- Reliable phase snapshots")
	if !found {
		t.Fatal("missing phase reporting section")
	}
	phase, _, _ = strings.Cut(phase, "-- BEGIN PRECISE NEAR-IDLE COVERAGE RECONCILIATION")
	for _, phase := range ingestPhaseMessages {
		if !strings.Contains(up, "('"+phase[0]+"', '"+phase[1]+"')") {
			t.Errorf("missing exact approved phase/message %q", phase)
		}
	}
	for _, required := range []string{
		"CREATE FUNCTION app.record_ingest_phase(target_job_id uuid, claiming_worker text, current_lease_token uuid,",
		"phase text, phase_current bigint, phase_total bigint) RETURNS boolean",
		"SECURITY DEFINER SET search_path = pg_catalog, app", "OWNER TO workouts_security_owner",
		"GRANT CREATE ON SCHEMA app TO workouts_security_owner", "REVOKE CREATE ON SCHEMA app FROM workouts_security_owner",
		"FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration",
		"GRANT EXECUTE ON FUNCTION app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint) TO workouts_worker",
		"app.fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 30)",
		"schema_version >= 29 AND minimum_runtime_version <= 30",
		"IF NOT FOUND THEN RETURN false", "ERRCODE = '22023'", "ERRCODE = '55000'",
		"phase_current < 0 OR phase_total < 0", "phase_total < phase_current",
		"'ingest-phase-' || replace(phase, '_', '-')",
		"jsonb_strip_nulls(jsonb_build_object('current', phase_current, 'total', phase_total, 'attempt', job_attempt))",
		"app.valid_job_safe_fields(event_fields) IS NOT TRUE", "ORDER BY id DESC LIMIT 1",
		"previous_event.fields->>'attempt' = job_attempt::text", "previous_event.fields = event_fields THEN RETURN true",
		"phase_current < (previous_event.fields->>'current')::bigint",
		"CREATE INDEX job_events_ingest_phase_latest_idx ON app.job_events(job_id, id DESC)",
		"WHERE code LIKE 'ingest-phase-%'", "schema_version = 29, minimum_runtime_version = 29",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("missing phase contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"CREATE TABLE", "ALTER TABLE", "CREATE OR REPLACE", "CREATE TRIGGER", "GRANT SELECT", "GRANT INSERT",
		"UPDATE app.jobs", "UPDATE app.job_progress", "UPDATE app.sources", "migration_baseline =",
		"app.record_job_event(", "count(*)", "count( * )", "ORDER BY created_at", "LIMIT 1000",
	} {
		if strings.Contains(phase, forbidden) {
			t.Errorf("non-additive or lossy phase contract %q", forbidden)
		}
	}
	if strings.Count(phase, "CREATE FUNCTION") != 1 || strings.Count(phase, "CREATE INDEX") != 1 || strings.Count(phase, "INSERT INTO") != 1 {
		t.Fatal("phase reporting must add only one function, one index, and append journal rows")
	}
	for _, required := range []string{
		"ingest status reporting is forward-only", "ERRCODE = '55000'", "restore the pre-migration backup",
	} {
		if !strings.Contains(down, required) {
			t.Errorf("missing Down contract %q", required)
		}
	}
	if strings.Contains(down, "DELETE FROM") || strings.Contains(down, "migration_baseline =") || strings.Contains(down, "DROP ") || strings.Contains(down, "UPDATE ") {
		t.Fatal("Down must preserve durable journal history and baseline27")
	}
}
