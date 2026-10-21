package migrations

import (
	stdstrings "strings"
	"testing"
)

func TestOwnerAwareInventoryContract(t *testing.T) {
	source, err := Files.ReadFile("025_owner_aware_source_inventory.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := stdstrings.Split(string(source), "-- +goose Down")[0]
	for _, required := range []string{
		"ADD COLUMN ownership_blocked_epoch bigint CHECK (ownership_blocked_epoch >= 0)",
		"WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)",
		"account.workout_ownership_epoch + 1", "WHERE account.id = NEW.account_id",
		"schema_version = 25, minimum_runtime_version = 25",
		"ALTER FUNCTION app.read_ingest_candidate_actions (uuid, text, uuid, jsonb) OWNER TO workouts_security_owner",
		"GRANT EXECUTE ON FUNCTION app.read_ingest_candidate_actions (uuid, text, uuid, jsonb) TO workouts_worker",
	} {
		if !stdstrings.Contains(up, required) {
			t.Errorf("ownership inventory lacks %q", required)
		}
	}
	recorder := functionBody(t, up, "CREATE OR REPLACE FUNCTION app.record_successful_source_object (")
	for _, required := range []string{
		"capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()",
		"capability.account_id = app.current_account_id()", "capability.ownership_epoch",
		"capability.job_id = target_job_id AND capability.worker_id = claiming_worker",
		"capability.lease_token = current_lease_token", "captured_epoch IS NULL THEN RETURN false",
		"child.status = 'running' AND child.cancel_requested_at IS NULL",
		"child.worker_id = claiming_worker AND child.lease_token = current_lease_token",
		"event.kind = 'skipped_live_owner'", "event.job_id = target_job_id",
		"file.job_id = target_job_id AND file.relative_name = object_name",
		"blocked_epoch := captured_epoch", "ownership_blocked_epoch = EXCLUDED.ownership_blocked_epoch",
		"IF first_seen THEN", "successful_checksum = EXCLUDED.successful_checksum",
	} {
		if !stdstrings.Contains(recorder, required) {
			t.Errorf("recorder lacks %q", required)
		}
	}
	for _, forbidden := range []string{"FROM app.accounts", "object_ownership_blocked", "DELETE FROM app.source_objects", "lease_expires_at", "clock_timestamp()"} {
		if stdstrings.Contains(recorder, forbidden) {
			t.Errorf("recorder must retain bytes and derive blockers/epoch internally: %q", forbidden)
		}
	}
	helper := functionBody(t, up, "CREATE FUNCTION app.read_ingest_candidate_actions (")
	for _, required := range []string{
		"app.fence_ingest_job(job, worker, lease, 26)", "account.state = 'active'",
		"WHEN frozen THEN COALESCE(stored.action, 'process')",
		"WHEN ingest_mode IS DISTINCT FROM 'incremental' THEN 'process'",
		"inventory.successful_checksum IS NOT NULL", "inventory.observed_identity = COALESCE(candidate.observed_identity",
		"inventory.ownership_blocked_epoch IS NULL OR inventory.ownership_blocked_epoch = current_epoch",
		"inventory.account_id = target_account AND inventory.source_id = target_source",
		"file.state IN ('succeeded','failed')", "jsonb_array_length(candidates) > 10000",
		"count(DISTINCT value->>'name')", "item->>'observed_identity' !~ '^v2:rclone:[0-9a-f]{64}$'",
	} {
		if !stdstrings.Contains(helper, required) {
			t.Errorf("candidate helper lacks %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE app.source_files", "UPDATE app.job_file_candidates", "GRANT SELECT"} {
		if stdstrings.Contains(up, forbidden) {
			t.Errorf("migration must preserve terminal checkpoints and private manifests: %q", forbidden)
		}
	}
}
