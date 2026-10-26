package migrations_test

import (
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

const reprocessingSafeFileColumns = "id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,checksum_sha256,state,failure_code,failure_summary,processing_started_at,processed_at,created_at,updated_at"

func reprocessingSQL(t *testing.T, name string) string {
	t.Helper()
	body, err := migrations.Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestIngestReprocessingMigrationContract(t *testing.T) {
	sql := reprocessingSQL(t, "031_ingest_reprocessing.sql")
	for _, required := range []string{
		"schema_version = 31, minimum_runtime_version = 31",
		"CREATE OR REPLACE FUNCTION app.valid_ingest_child_parameters(value jsonb)",
		"jsonb_typeof(value->'reprocessAllFiles') IS DISTINCT FROM 'boolean'",
		"value ? 'legacySchema6' AND (value ? 'reprocessAllFiles'",
		"ADD COLUMN observed_identity text", "ADD COLUMN ownership_blocked_epoch bigint CHECK (ownership_blocked_epoch >= 0)",
		"CREATE FUNCTION app.ingest_retry_ancestor_jobs(job uuid, target_account uuid, target_source uuid) RETURNS uuid[]",
		"cursor_id = ANY(visited) OR cardinality(visited) >= 100",
		"node.parent_job_id IS DISTINCT FROM expected_parent", "node.parent_retry IS NOT NULL",
		"node_policy IS DISTINCT FROM policy", "RETURN visited[2:cardinality(visited)]",
		"LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id = child.id AND snapshot.account_id = target_account",
		"(cardinality(visited) = 0 AND node.snapshot_job IS NULL)",
		"(node.snapshot_job IS NOT NULL AND (node.snapshot_source IS DISTINCT FROM target_source OR",
		"CREATE FUNCTION app.stamp_ingest_source_file_receipt() RETURNS trigger",
		"capability.runtime_version >= 31", "capability.source_id = NEW.source_id AND capability.job_id = NEW.job_id",
		"child.worker_id = capability.worker_id AND child.lease_token = capability.lease_token",
		"stored.action = 'process' AND stored.observed_size = NEW.size_bytes",
		"extract(epoch FROM NEW.modified_at)", "stored.modified_ns / 1000",
		"NEW.ownership_blocked_epoch := captured_epoch", "NEW.observed_identity := COALESCE(candidate.observed_identity",
		"event.source_file_id = NEW.id AND event.kind = 'skipped_live_owner'",
		"file.observed_identity IS NULL AND inventory.successful_job_id = file.job_id",
		"inventory.successful_checksum = file.checksum_sha256",
		"GRANT EXECUTE ON FUNCTION app.read_ingest_candidate_actions(uuid,text,uuid,jsonb) TO workouts_worker",
		"ALTER FUNCTION app.stamp_ingest_source_file_receipt() OWNER TO workouts_security_owner",
		"ALTER FUNCTION app.ingest_retry_ancestor_jobs(uuid,uuid,uuid) OWNER TO workouts_security_owner",
		"FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration",
		"ingest reprocessing is forward-only",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing reprocessing contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"ingestContractVersion", "migration_baseline =", "DISABLE TRIGGER", "NO FORCE ROW LEVEL SECURITY",
		"UPDATE app.source_files", "UPDATE app.source_objects", "UPDATE app.jobs", "UPDATE app.job_progress",
		"CREATE OR REPLACE FUNCTION app.fence_ingest_job", "CREATE TABLE", "CREATE INDEX",
		"CREATE OR REPLACE FUNCTION app.read_owned_job_files", "GRANT SELECT ON app.source_files",
		"TO workouts_api;", "TO workouts_worker,", "SECURITY INVOKER",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("unexpected reprocessing scope expansion %q", forbidden)
		}
	}
	if strings.Count(sql, "ancestors := app.ingest_retry_ancestor_jobs(") != 1 {
		t.Fatal("lineage must be built once per discovery")
	}
	// Compare the complete shipped validation block, not just selected fields.
	between := func(body, start, end string) string {
		t.Helper()
		_, body, ok := strings.Cut(body, start)
		if !ok {
			t.Fatalf("missing start %q", start)
		}
		body, _, ok = strings.Cut(body, end)
		if !ok {
			t.Fatalf("missing end %q", end)
		}
		return body
	}
	start := "    IF jsonb_typeof(candidates) IS DISTINCT FROM 'array' THEN"
	end := "    SELECT fence.source_id INTO target_source"
	old := reprocessingSQL(t, "025_owner_aware_source_inventory.sql")
	if between(old, start, end) != between(sql, start, end) {
		t.Fatal("candidate whitelist/null/type/size validation changed")
	}
	lookup := between(reprocessingSQL(t, "027_coverage_recovery_baseline.sql"), "$replacement$", "$replacement$")
	if !strings.Contains(sql, lookup) {
		t.Fatal("027 exact private-capability runtime lookup changed")
	}
	reader := sql[strings.Index(sql, "CREATE OR REPLACE FUNCTION app.read_ingest_candidate_actions"):]
	last := -1
	for _, anchor := range []string{
		"IF EXISTS (SELECT 1 FROM app.job_file_candidate_sets", "ancestors :=", "WHEN EXISTS (SELECT 1 FROM app.source_files own_file",
		"SELECT 1 FROM unnest(ancestors)", "WHEN effective_force THEN 'process'", "WHEN inventory.successful_checksum IS NOT NULL",
	} {
		index := strings.Index(reader, anchor)
		if index <= last {
			t.Fatalf("candidate priority missing/out of order: %s", anchor)
		}
		last = index
	}
	lineage := between(sql, "CREATE FUNCTION app.ingest_retry_ancestor_jobs(", "$function$;")
	if strings.Contains(lineage, "\n          JOIN app.job_config_snapshots") ||
		!strings.Contains(lineage, "node.parameters->>'generation' IS DISTINCT FROM node.source_generation::text") ||
		!strings.Contains(lineage, "node.snapshot_generation IS DISTINCT FROM node.source_generation))") {
		t.Fatal("only the current node requires a snapshot; durable contexts and retained snapshot consistency remain validated")
	}
	// A receipt is stamped from frozen metadata, but later reuse must not depend
	// on private manifest retention or the mutable global success pointer.
	durable := between(sql, "AND ((file.observed_identity = candidate.identity", "OR (file.observed_identity IS NULL")
	if strings.Contains(durable, "inventory.") || !strings.Contains(durable, "file.ownership_blocked_epoch = current_epoch") {
		t.Fatal("durable chain receipt depends on latest inventory or ignores ownership")
	}
	chain := between(sql, "SELECT 1 FROM unnest(ancestors)", ") THEN 'skip'")
	for _, forbidden := range []string{"app.job_file_candidates", "app.job_file_candidate_sets", "app.job_config_snapshots"} {
		if strings.Contains(chain, forbidden) {
			t.Fatalf("chain success depends on private retained data: %s", forbidden)
		}
	}
	for _, required := range []string{"file.size_bytes = candidate.size", "extract(epoch FROM file.modified_at)",
		"inventory.observed_size = candidate.size", "inventory.export_date = candidate.export_date", "inventory.observed_modified_at = file.modified_at"} {
		if !strings.Contains(chain, required) {
			t.Fatalf("chain success lacks exact file metadata: %s", required)
		}
	}
}

func TestIngestReprocessingSourceFileReadPrivileges(t *testing.T) {
	sql := strings.Join(strings.Fields(reprocessingSQL(t, "031_ingest_reprocessing.sql")), "")
	revoke := "REVOKESELECTONapp.source_filesFROMworkouts_api,workouts_worker;"
	grant := "GRANTSELECT(" + reprocessingSafeFileColumns + ")ONapp.source_filesTOworkouts_api,workouts_worker;"
	if strings.Index(sql, revoke) < 0 || strings.Index(sql, grant) <= strings.Index(sql, revoke) {
		t.Fatal("replace inherited table SELECT with exactly the fifteen old column grants for API and worker")
	}
	for _, forbidden := range []string{
		"GRANTSELECTONapp.source_files", "REVOKEALLONapp.source_files", "REVOKEINSERT", "REVOKEUPDATE",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("unexpected source-file privilege change: %s", forbidden)
		}
	}
	if !strings.Contains(sql, "GRANTSELECT(observed_identity,ownership_blocked_epoch)ONapp.source_filesTOworkouts_security_owner;") {
		t.Fatal("private receipt reads must remain available to the trusted security owner")
	}
}
