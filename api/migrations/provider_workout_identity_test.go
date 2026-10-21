package migrations

import (
	stdstrings "strings"
	"testing"
)

func TestProviderWorkoutIdentityContract(t *testing.T) {
	raw, err := Files.ReadFile("024_provider_workout_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := stdstrings.Split(string(raw), "-- +goose Down")[0]
	for _, required := range []string{
		"schema_version = 24, minimum_runtime_version = 24",
		"ADD COLUMN workout_ownership_epoch bigint NOT NULL DEFAULT 0",
		"ADD COLUMN ownership_epoch bigint", "ADD COLUMN runtime_version integer",
		"CREATE UNIQUE INDEX workouts_account_provider_identity_idx",
		"ON app.workouts(account_id, provider_namespace, provider_id) WHERE provider_id IS NOT NULL",
		"CREATE INDEX workout_deletion_targets_provider_namespace_idx",
		"FOREIGN KEY(workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE RESTRICT",
		"'adopted', 'skipped_live_owner'", "FOR NO KEY UPDATE NOWAIT",
		"ALTER TABLE app.workout_adoption_capabilities FORCE ROW LEVEL SECURITY",
		"REFERENCES app.ingest_write_capabilities(backend_pid, transaction_id) ON DELETE CASCADE",
		"pg_get_functiondef('app.require_ingest_write_capability()'::regprocedure)",
		"capability.runtime_version >= 26", "app.valid_ingest_ownership_epoch(capability.account_id, capability.ownership_epoch, capability.runtime_version)",
		"versioned ingest fence requires runtime 26 or newer",
	} {
		if !stdstrings.Contains(up, required) {
			t.Errorf("identity migration lacks %q", required)
		}
	}
	fence := functionBody(t, up, "CREATE FUNCTION app.fence_ingest_job(")
	previous := -1
	for _, required := range []string{
		"pg_try_advisory_xact_lock", "FOR KEY SHARE NOWAIT",
		"ORDER BY source.id FOR UPDATE NOWAIT", "FROM app.jobs parent", "FOR UPDATE NOWAIT",
		"FROM app.jobs job WHERE job.id = fence_ingest_job.job_id", "FROM app.account_data_generations",
		"INSERT INTO app.ingest_write_capabilities",
	} {
		index := stdstrings.Index(fence[previous+1:], required)
		if index < 0 {
			t.Fatalf("fence lock order lacks %q after position %d", required, previous)
		}
		previous += index + 1
	}
	existing := stdstrings.Index(fence, "SELECT capability.* INTO existing_capability")
	fresh := stdstrings.Index(fence, "SELECT job.account_id, snapshot.source_id")
	grant := stdstrings.Index(fence, "INSERT INTO app.ingest_write_capabilities")
	expiry := stdstrings.LastIndex(fence, "job.lease_expires_at >= clock_timestamp()")
	if existing < 0 || fresh <= existing || stdstrings.Contains(fence[existing:fresh], "clock_timestamp()") {
		t.Fatal("an existing transaction capability must be validated before fresh-grant clock checks")
	}
	if expiry <= fresh || expiry >= grant {
		t.Fatal("a fresh grant must recheck wall-clock expiry before inserting a capability")
	}
	resolver := functionBody(t, up, "CREATE FUNCTION app.resolve_ingest_workout_identity(")
	for _, required := range []string{"app.workout_deletion_suppressed", "FOR UPDATE NOWAIT", "candidate_count > 1",
		"workout identity-conflict", "'skipped_live_owner'", "app.workout_adoption_capabilities"} {
		if !stdstrings.Contains(resolver, required) {
			t.Errorf("resolver lacks %q", required)
		}
	}
	if stdstrings.Contains(resolver, "lease_expires_at") || stdstrings.Contains(resolver, "clock_timestamp()") {
		t.Fatal("resolver must not expire an already-granted transaction capability by wall clock")
	}
	for _, forbidden := range []string{"app.workout_coverage", "app.coverage_", "app.enqueue_coverage", "FOR UPDATE;", "pg_advisory_xact_lock("} {
		if stdstrings.Contains(resolver, forbidden) || stdstrings.Contains(fence, forbidden) {
			t.Errorf("identity file transaction introduces %q", forbidden)
		}
	}
	for _, forbidden := range []string{"DELETE FROM app.workouts", "DELETE FROM app.workout_import_events", "UPDATE app.workout_import_events"} {
		if stdstrings.Contains(up, forbidden) {
			t.Errorf("migration rewrites legacy identity/history: %q", forbidden)
		}
	}
	for _, forbidden := range []string{"DROP CONSTRAINT schema_metadata_minimum_runtime_version_check", "UPDATE app.ingest_write_capabilities capability SET ownership_epoch"} {
		if stdstrings.Contains(up, forbidden) {
			t.Errorf("migration changes metadata invariant or promotes a captured epoch: %q", forbidden)
		}
	}
	identity := functionBody(t, up, "CREATE OR REPLACE FUNCTION app.enforce_ingest_identity()")
	if stdstrings.Contains(identity, "bump_workout_ownership_epoch") {
		t.Fatal("adoption must not advance the source-deletion epoch")
	}
}
