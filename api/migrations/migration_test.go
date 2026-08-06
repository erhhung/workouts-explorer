package migrations

import (
	"fmt"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
)

func TestMigrationFailsClosedOnRolesAndPrivileges(t *testing.T) {
	source, err := Files.ReadFile("00001_job_foundations.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"required NOBYPASSRLS login role",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON TABLES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON SEQUENCES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration is missing security foundation %q", required)
		}
	}
}

func TestProceduralStatementsAreProtectedFromGooseSplitting(t *testing.T) {
	for _, name := range []string{"00001_job_foundations.sql", "00002_account_lifecycle.sql", "00003_sources_and_job_snapshots.sql", "00004_ingest_workouts.sql", "00005_worker_job_log_context.sql", "00006_worker_job_log_owner_username.sql"} {
		source, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(source)
		proceduralStatements := strings.Count(text, "DO $$") + strings.Count(text, "AS $$")
		starts := strings.Count(text, "-- +goose StatementBegin")
		ends := strings.Count(text, "-- +goose StatementEnd")
		if starts != proceduralStatements || ends != proceduralStatements {
			t.Fatalf("%s: procedural statements = %d, StatementBegin = %d, StatementEnd = %d", name, proceduralStatements, starts, ends)
		}
	}
}

func TestWorkerJobLogOwnerUsernameMigrationReplacesContractSecurely(t *testing.T) {
	source, err := Files.ReadFile("00006_worker_job_log_owner_username.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"RETURNS TABLE(owner_username text, source_name text, source_type text)",
		"SELECT principal.username, source.display_name, source.type",
		"job.status = 'running'",
		"job.worker_id = claiming_worker",
		"job.lease_token = current_lease_token",
		"job.lease_expires_at >= clock_timestamp()",
		"prior_account_id text := current_setting('app.account_id', true)",
		"EXCEPTION WHEN OTHERS THEN",
		"set_config('app.account_id', COALESCE(prior_account_id, ''), true)",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, app",
		"ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner",
		"GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker",
		"CREATE OR REPLACE FUNCTION app.clear_ingest_write_capability()",
		"CREATE OR REPLACE FUNCTION app.fence_ingest_job(job_id uuid, claiming_worker text, current_lease_token uuid)",
		"CREATE OR REPLACE FUNCTION app.claim_next_worker_job_internal",
		"job.kind IN ('manual_ingest_source', 'scheduled_ingest_source')",
		"parent.kind = expected_parent_kind",
		"job.kind IN ('manual_ingest_source', 'scheduled_ingest_source')",
		"candidate_kind = 'scheduled_ingest_source' AND parent.kind = 'scheduled_ingest'",
		"CREATE FUNCTION app.assert_no_active_scheduled_ingest()",
		"SELECT app.assert_no_active_scheduled_ingest();",
		"cannot downgrade while scheduled ingest jobs or snapshots are active",
		"ALTER FUNCTION app.assert_no_active_scheduled_ingest() OWNER TO workouts_security_owner",
		"GRANT EXECUTE ON FUNCTION app.assert_no_active_scheduled_ingest() TO workouts_migration",
		"UPDATE app.schema_metadata SET schema_version = 6",
		"UPDATE app.schema_metadata SET schema_version = 5",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("worker owner username migration is missing %q", required)
		}
	}
	down := strings.Split(text, "-- +goose Down")[1]
	for _, required := range []string{
		"RETURNS TABLE(owner_name text, source_name text, source_type text)",
		"SELECT principal.full_name, source.display_name, source.type",
		"ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner",
		"GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker",
		"include_manual_ingest AND job.kind = 'manual_ingest_source'",
		"AND job.kind = 'manual_ingest_source'",
		"AND parent.kind = 'manual_ingest'",
		"DROP FUNCTION app.assert_no_active_scheduled_ingest()",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("migration 00006 down does not restore migration 00005 contract %q", required)
		}
	}
	if strings.Count(text, "CREATE OR REPLACE FUNCTION app.clear_ingest_write_capability()") != 2 ||
		strings.Count(text, "CREATE OR REPLACE FUNCTION app.fence_ingest_job") != 2 ||
		strings.Count(text, "CREATE OR REPLACE FUNCTION app.claim_next_worker_job_internal") != 2 {
		t.Fatal("migration 00006 does not replace and restore all scheduled-ingest plumbing")
	}
	for _, forbidden := range []string{
		"GRANT SELECT ON app.authentication_principals TO workouts_worker",
		"GRANT SELECT ON app.users TO workouts_worker",
		"GRANT SELECT ON app.administrators TO workouts_worker",
		"GRANT SELECT ON app.sources TO workouts_worker",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("worker owner username migration contains unsafe grant %q", forbidden)
		}
	}
}

func TestWorkerJobLogContextMigrationContainsSecurityBoundaries(t *testing.T) {
	source, err := Files.ReadFile("00005_worker_job_log_context.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"CREATE FUNCTION app.repair_orphaned_source_jobs",
		"job.kind IN ('source_connection_check', 'manual_ingest_source', 'scheduled_ingest_source')",
		"NOT EXISTS (SELECT 1 FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = job.id)",
		"failure_code = 'source-snapshot-missing'",
		"PERFORM app.derive_parent_status(orphan.parent_job_id)",
		"SELECT app.repair_orphaned_source_jobs()",
		"DROP FUNCTION app.repair_orphaned_source_jobs()",
		"CREATE FUNCTION app.read_worker_job_log_context",
		"RETURNS TABLE(owner_name text, source_name text, source_type text)",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, app",
		"job.status = 'running'",
		"job.worker_id = claiming_worker",
		"job.lease_token = current_lease_token",
		"job.lease_expires_at >= clock_timestamp()",
		"prior_account_id text := current_setting('app.account_id', true)",
		"set_config('app.account_id', COALESCE(prior_account_id, ''), true)",
		"JOIN app.authentication_principals principal",
		"LEFT JOIN app.job_config_snapshots snapshot",
		"LEFT JOIN app.sources source",
		"ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner",
		"GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker",
		"UPDATE app.schema_metadata SET schema_version = 5",
		"UPDATE app.schema_metadata SET schema_version = 4",
		"DROP FUNCTION app.read_worker_job_log_context(uuid,text,uuid)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("worker log context migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"GRANT SELECT ON app.authentication_principals TO workouts_worker",
		"GRANT SELECT ON app.users TO workouts_worker",
		"GRANT SELECT ON app.administrators TO workouts_worker",
		"GRANT SELECT ON app.sources TO workouts_worker",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("worker log context migration contains unsafe grant %q", forbidden)
		}
	}
}

func TestPolymorphicTriggersBranchBeforeRecordFieldAccess(t *testing.T) {
	for _, name := range []string{"00002_account_lifecycle.sql", "00003_sources_and_job_snapshots.sql", "00004_ingest_workouts.sql"} {
		source, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(source)
		if strings.Contains(text, "CASE WHEN TG_TABLE_NAME") || strings.Contains(text, "CASE WHEN TG_OP") {
			t.Fatalf("%s contains polymorphic CASE record field access", name)
		}
	}
	source, err := Files.ReadFile("00003_sources_and_job_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, string(source), "CREATE FUNCTION app.enforce_job_snapshot_consistency")
	jobsBranch := strings.Index(body, "IF TG_TABLE_NAME = 'jobs' THEN")
	jobsAccess := strings.Index(body, "target_job_id := NEW.id")
	snapshotsBranch := strings.Index(body, "ELSIF TG_TABLE_NAME = 'job_config_snapshots' THEN")
	snapshotsAccess := strings.Index(body, "target_job_id := NEW.job_id")
	if jobsBranch < 0 || jobsAccess < 0 || snapshotsBranch < 0 || snapshotsAccess < 0 ||
		jobsBranch >= jobsAccess || jobsAccess >= snapshotsBranch || snapshotsBranch >= snapshotsAccess {
		t.Fatal("snapshot consistency trigger does not branch on table before NEW field access")
	}
}

func TestNormalizedWorkoutMigrationContainsPersistenceBoundaries(t *testing.T) {
	source, err := Files.ReadFile("00004_ingest_workouts.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"CREATE TABLE app.source_files",
		"CREATE TABLE app.workout_types",
		"CREATE TABLE app.workouts",
		"CREATE TABLE app.workout_aggregates",
		"CREATE TABLE app.workout_route_points",
		"CREATE TABLE app.workout_import_events",
		"CREATE TABLE app.ingest_write_capabilities",
		"UNIQUE (account_id, job_id, relative_name)",
		"workouts_source_provider_id_idx",
		"workouts_source_fallback_idx",
		"UNIQUE (account_id, type_key)",
		"octet_length(content_sha256) = 32",
		"octet_length(checksum_sha256) = 32",
		"provider_duration numeric NOT NULL",
		"warnings jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (app.valid_workout_warnings(warnings))",
		"jsonb_array_length(warnings) > 4096",
		"octet_length(warnings::text) > 262144",
		"invalid_optional_route_value",
		"route_point NOT BETWEEN 0 AND 249999",
		"GRANT EXECUTE ON FUNCTION app.valid_workout_warnings(jsonb)",
		"PRIMARY KEY (account_id, workout_id, sequence)",
		"workout import events are append-only",
		"immutable workout provider identity changed",
		"ALTER TABLE app.workout_import_events FORCE ROW LEVEL SECURITY",
		"CREATE FUNCTION app.require_ingest_write_capability",
		"capability.backend_pid = pg_backend_pid()",
		"capability.transaction_id = txid_current()",
		"ingest domain write requires a live transaction fence",
		"DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.clear_ingest_write_capability()",
		"CREATE FUNCTION app.fence_ingest_job",
		"job.cancel_requested_at IS NULL",
		"job.lease_expires_at >= clock_timestamp()",
		"CREATE FUNCTION app.claim_next_worker_job",
		"RETURNS TABLE(job_id uuid, account_id uuid, kind text)",
		"include_manual_ingest AND job.kind = 'manual_ingest_source'",
		"ORDER BY job.priority DESC, job.created_at, job.id",
		"FOR UPDATE SKIP LOCKED",
		"ALTER FUNCTION app.claim_next_worker_job(text,uuid,interval) OWNER TO workouts_security_owner",
		"ALTER FUNCTION app.claim_next_source_connection_check(text,uuid,interval) OWNER TO workouts_security_owner",
		"ALTER FUNCTION app.claim_next_worker_job_internal(text,uuid,interval,boolean) OWNER TO workouts_security_owner",
		"CREATE POLICY jobs_cross_account_claim_policy",
		"CREATE POLICY job_config_snapshots_cross_account_guard_policy",
		"terminal source files are immutable",
		"invalid source file state transition",
		"REFERENCES app.workouts(id, account_id, source_id) ON DELETE RESTRICT",
		"future deletion",
		"SELECT app.assert_no_active_manual_ingest();",
		"LOCK TABLE app.jobs, app.job_config_snapshots IN SHARE ROW EXCLUSIVE MODE",
		"UPDATE app.schema_metadata SET schema_version = 4",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("normalized workout migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"GRANT INSERT ON app.workouts TO workouts_api",
		"GRANT UPDATE ON app.workouts TO workouts_api",
		"GRANT DELETE ON app.workouts TO workouts_worker",
		"GRANT DELETE ON app.source_files TO workouts_worker",
		"GRANT UPDATE ON app.workout_import_events TO workouts_worker",
		"GRANT DELETE ON app.workout_import_events TO workouts_worker",
		"GRANT SELECT ON app.ingest_write_capabilities TO workouts_worker",
		"GRANT INSERT ON app.ingest_write_capabilities TO workouts_worker",
		"GRANT EXECUTE ON FUNCTION app.claim_next_worker_job_internal",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("normalized workout migration contains unsafe privilege %q", forbidden)
		}
	}
	if strings.Count(text, "CREATE FUNCTION app.claim_next_source_connection_check") != 2 {
		t.Fatal("migration 4 does not preserve and restore the migration-3 claim function")
	}
	typeKeyBound := fmt.Sprintf("octet_length(type_key) BETWEEN 1 AND %d", healthautoexport.MaxTypeKeyBytes)
	if !strings.Contains(text, typeKeyBound) {
		t.Fatalf("workout type key bound does not match parser MaxTypeKeyBytes: want %q", typeKeyBound)
	}
	for _, table := range []string{"source_files", "workout_types", "workouts", "workout_aggregates", "workout_route_points", "workout_import_events"} {
		if !strings.Contains(text, "CREATE TRIGGER "+table+"_capability_before_write") {
			t.Fatalf("ingest table %s is missing its capability trigger", table)
		}
	}
	fence := functionBody(t, strings.Split(text, "-- +goose Down")[0], "CREATE FUNCTION app.fence_ingest_job")
	sourceLock := strings.Index(fence, "FROM app.sources source")
	parentLock := strings.Index(fence, "PERFORM 1 FROM app.jobs parent")
	childLock := -1
	if parentLock >= 0 {
		if relative := strings.Index(fence[parentLock:], "PERFORM 1 FROM app.jobs job"); relative >= 0 {
			childLock = parentLock + relative
		}
	}
	if sourceLock < 0 || parentLock < 0 || childLock < 0 || sourceLock >= parentLock || parentLock >= childLock {
		t.Fatal("ingest fence does not acquire source, parent, then child locks")
	}
	claim := functionBody(t, strings.Split(text, "-- +goose Down")[0], "CREATE FUNCTION app.claim_next_worker_job_internal")
	claimSource := strings.Index(claim, "FROM app.sources source")
	claimParent := strings.Index(claim, "FROM app.jobs parent")
	claimChild := strings.Index(claim, "SELECT job.status INTO candidate_status")
	if claimSource < 0 || claimParent < 0 || claimChild < 0 || claimSource >= claimParent || claimParent >= claimChild {
		t.Fatal("worker claim does not acquire source, parent, then child locks")
	}
	down := strings.Split(text, "-- +goose Down")[1]
	if strings.Index(down, "SELECT app.assert_no_active_manual_ingest();") > strings.Index(down, "UPDATE app.schema_metadata") {
		t.Fatal("migration 4 down guard runs after downgrade starts")
	}
	cleanup := functionBody(t, strings.Split(text, "-- +goose Down")[0], "CREATE FUNCTION app.clear_ingest_write_capability")
	cleanupSource := strings.Index(cleanup, "FROM app.sources source")
	cleanupParent := strings.Index(cleanup, "FROM app.jobs parent")
	cleanupChild := strings.Index(cleanup, "PERFORM 1 FROM app.jobs job")
	cleanupDelete := strings.Index(cleanup, "DELETE FROM app.ingest_write_capabilities")
	for _, required := range []string{
		"snapshot.source_id = NEW.source_id",
		"source.deleted_at IS NULL",
		"parent.cancel_requested_at IS NULL",
		"job.cancel_requested_at IS NULL",
		"job.worker_id = NEW.worker_id",
		"job.lease_token = NEW.lease_token",
	} {
		if !strings.Contains(cleanup, required) {
			t.Fatalf("deferred capability cleanup is missing %q", required)
		}
	}
	if cleanupSource < 0 || cleanupParent < 0 || cleanupChild < 0 || cleanupDelete < 0 ||
		cleanupSource >= cleanupParent || cleanupParent >= cleanupChild || cleanupChild >= cleanupDelete {
		t.Fatal("deferred capability cleanup does not validate source, parent, child before deleting capability")
	}
	if strings.Contains(cleanup, "lease_expires_at >= clock_timestamp()") ||
		!strings.Contains(cleanup, "recovery and") || !strings.Contains(cleanup, "cannot change ownership before commit") {
		t.Fatal("deferred cleanup incorrectly expires a row-lock-fenced transaction or lacks its recovery rationale")
	}
	for _, transition := range []struct {
		declaration string
		childLock   string
	}{
		{"CREATE OR REPLACE FUNCTION app.finish_job", "PERFORM 1 FROM app.jobs job"},
		{"CREATE OR REPLACE FUNCTION app.recover_expired_job", "SELECT job.cancel_requested_at IS NOT NULL"},
	} {
		body := functionBody(t, strings.Split(text, "-- +goose Down")[0], transition.declaration)
		source := strings.Index(body, "PERFORM 1 FROM app.sources source")
		parent := strings.Index(body, "PERFORM 1 FROM app.jobs parent")
		child := strings.Index(body, transition.childLock)
		if source < 0 || parent < 0 || child < 0 || source >= parent || parent >= child {
			t.Fatalf("%s does not lock source, parent, then child", transition.declaration)
		}
	}
	cancellation := functionBody(t, strings.Split(text, "-- +goose Down")[0], "CREATE OR REPLACE FUNCTION app.request_job_cancellation")
	parentSources := strings.Index(cancellation, "FOR source_to_lock IN")
	cancelParentLock := strings.Index(cancellation, "SELECT job.kind, job.status INTO target_kind, target_status FROM app.jobs job")
	childrenLock := strings.Index(cancellation, "FOR child_to_lock IN")
	if parentSources < 0 || cancelParentLock < 0 || childrenLock < 0 || parentSources >= cancelParentLock || cancelParentLock >= childrenLock {
		t.Fatal("parent cancellation does not lock child sources, parent, then children")
	}
	if strings.Count(text, "CREATE OR REPLACE FUNCTION app.finish_job") != 2 ||
		strings.Count(text, "CREATE OR REPLACE FUNCTION app.recover_expired_job") != 2 ||
		strings.Count(text, "CREATE OR REPLACE FUNCTION app.request_job_cancellation") != 2 {
		t.Fatal("migration 4 does not replace and restore all ordered transition functions")
	}
}

func TestSourceSnapshotMigrationContainsSecurityBoundaries(t *testing.T) {
	source, err := Files.ReadFile("00003_sources_and_job_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"ALTER TABLE app.sources FORCE ROW LEVEL SECURITY",
		"ALTER TABLE app.job_config_snapshots FORCE ROW LEVEL SECURITY",
		`canonical_display_name text COLLATE "C" NOT NULL CHECK (length(canonical_display_name) BETWEEN 1 AND 200)`,
		"ON app.sources (account_id, canonical_display_name) WHERE deleted_at IS NULL",
		"display name and canonical display name must change together",
		"Current source state is a creation-time fence",
		"IF TG_TABLE_NAME = 'job_config_snapshots' THEN",
		"CREATE FUNCTION app.read_job_config_snapshot",
		"CREATE FUNCTION app.claim_next_source_connection_check",
		"FOR UPDATE SKIP LOCKED",
		"CREATE FUNCTION app.complete_source_connection_check",
		"CREATE FUNCTION app.delete_source",
		"ALTER FUNCTION app.delete_source(uuid,uuid) OWNER TO workouts_security_owner",
		"GRANT EXECUTE ON FUNCTION app.delete_source(uuid,uuid) TO workouts_api",
		"DROP FUNCTION app.delete_source(uuid,uuid)",
		"CREATE OR REPLACE FUNCTION app.heartbeat_job",
		"source.generation = snapshot_generation",
		"finished := app.finish_job",
		"job.worker_id = claiming_worker",
		"job.lease_token = current_lease_token",
		"job.lease_expires_at >= transaction_timestamp()",
		"RAISE EXCEPTION 'job config snapshots are immutable'",
		"DELETE FROM app.job_config_snapshots",
		"OWNER TO workouts_security_owner",
		"GRANT INSERT (job_id,account_id,source_id,source_generation,config_envelope)",
		"GRANT SELECT (job_id,account_id,source_id,source_generation) ON app.job_config_snapshots TO workouts_api",
		"UPDATE app.schema_metadata SET schema_version = 3",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("source snapshot migration is missing %q", required)
		}
	}
	if strings.Contains(text, "GRANT SELECT ON app.job_config_snapshots TO workouts_worker") {
		t.Fatal("worker has an unfenced snapshot table read grant")
	}
	if strings.Contains(text, "GRANT EXECUTE ON FUNCTION app.delete_source(uuid,uuid) TO workouts_worker") ||
		strings.Contains(text, "GRANT EXECUTE ON FUNCTION app.delete_source(uuid,uuid) TO PUBLIC") {
		t.Fatal("source deletion is executable outside the API role")
	}
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "GRANT UPDATE (") && strings.Contains(line, "deleted_at") &&
			strings.Contains(line, "workouts_api") {
			t.Fatal("API retains direct source tombstone privilege")
		}
	}
	const safeSnapshotSelect = "GRANT SELECT (job_id,account_id,source_id,source_generation) ON app.job_config_snapshots TO workouts_api;"
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, "GRANT SELECT") && strings.Contains(line, "app.job_config_snapshots") &&
			strings.Contains(line, "TO workouts_api") && strings.TrimSpace(line) != safeSnapshotSelect {
			t.Fatalf("unsafe snapshot SELECT grant %q", line)
		}
	}
	if strings.Contains(text, "lower(display_name)") {
		t.Fatal("database still derives source display-name canonicalization")
	}
	for _, privilege := range []string{
		"GRANT INSERT (id,account_id,display_name,canonical_display_name,type",
		"GRANT UPDATE (display_name,canonical_display_name,auto_sync_enabled",
	} {
		if !strings.Contains(text, privilege) {
			t.Fatalf("source grant is missing canonical display-name privilege %q", privilege)
		}
	}
	apiUpdateStart := strings.Index(text, "GRANT UPDATE (display_name,canonical_display_name")
	apiUpdateEnd := -1
	if apiUpdateStart >= 0 {
		if relative := strings.Index(text[apiUpdateStart:], "TO workouts_api;"); relative >= 0 {
			apiUpdateEnd = apiUpdateStart + relative
		}
	}
	if apiUpdateStart < 0 || apiUpdateEnd < 0 || strings.Contains(text[apiUpdateStart:apiUpdateEnd], "deleted_at") {
		t.Fatal("API source update grant includes direct tombstone access")
	}
	readStart := strings.Index(text, "CREATE FUNCTION app.read_job_config_snapshot")
	if readStart < 0 {
		t.Fatal("could not locate fenced snapshot read function")
	}
	readEnd := strings.Index(text[readStart:], "-- +goose StatementEnd")
	if readEnd < 0 {
		t.Fatal("could not locate fenced snapshot read function")
	}
	if strings.Contains(text[readStart:readStart+readEnd], "app.sources") {
		t.Fatal("fenced read incorrectly depends on mutable source lifecycle state")
	}
	if !strings.Contains(text, "IF target_status = 'queued' THEN\n            DELETE FROM app.job_config_snapshots") {
		t.Fatal("queued cancellation does not delete its snapshot separately from running cancellation intent")
	}
	if strings.Count(text, "CREATE OR REPLACE FUNCTION app.heartbeat_job") != 2 {
		t.Fatal("migration does not replace heartbeat on upgrade and restore it on downgrade")
	}
	up := strings.Split(text, "-- +goose Down")[0]
	heartbeat := functionBody(t, up, "CREATE OR REPLACE FUNCTION app.heartbeat_job")
	if !strings.Contains(heartbeat, "lease_expires_at >= clock_timestamp()") {
		t.Fatal("migration 3 heartbeat does not reject an expired matching lease")
	}
	finish := functionBody(t, up, "CREATE OR REPLACE FUNCTION app.finish_job")
	if !strings.Contains(finish, "lease_expires_at >= clock_timestamp() FOR UPDATE") {
		t.Fatal("migration 3 direct finish does not lock an unexpired matching lease")
	}
	completion := functionBody(t, up, "CREATE FUNCTION app.complete_source_connection_check")
	sourceLock := strings.Index(completion, "FROM app.sources source")
	jobLock := strings.Index(completion, "FROM app.jobs job")
	if sourceLock < 0 || jobLock < 0 || sourceLock >= jobLock {
		t.Fatal("source completion does not acquire source then job locks")
	}
	for _, fence := range []string{"source.deleted_at", "source_generation_value = snapshot_generation", "job.cancel_requested_at", "job.lease_expires_at >= clock_timestamp()"} {
		if !strings.Contains(completion, fence) {
			t.Fatalf("source completion is missing fence %q", fence)
		}
	}
	deletion := functionBody(t, up, "CREATE FUNCTION app.delete_source")
	deletionSource := strings.Index(deletion, "FROM app.sources source")
	deletionParent := strings.Index(deletion, "PERFORM 1 FROM app.jobs parent")
	deletionChild := strings.Index(deletion, "PERFORM 1 FROM app.jobs job")
	deletionCancel := strings.Index(deletion, "PERFORM app.request_job_cancellation")
	deletionTombstone := strings.Index(deletion, "UPDATE app.sources source SET")
	if deletionSource < 0 || deletionParent < 0 || deletionChild < 0 || deletionCancel < 0 || deletionTombstone < 0 ||
		deletionSource >= deletionParent || deletionParent >= deletionChild || deletionChild >= deletionCancel || deletionCancel >= deletionTombstone {
		t.Fatal("source deletion does not lock source, parents, children before cancellation and tombstone")
	}
}

func functionBody(t *testing.T, migration, declaration string) string {
	t.Helper()
	start := strings.Index(migration, declaration)
	if start < 0 {
		t.Fatalf("could not locate %q", declaration)
	}
	end := strings.Index(migration[start:], "-- +goose StatementEnd")
	if end < 0 {
		t.Fatalf("could not locate end of %q", declaration)
	}
	return migration[start : start+end]
}

func TestAccountLifecycleMigrationContainsSecurityBoundaries(t *testing.T) {
	source, err := Files.ReadFile("00002_account_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		`canonical_username text COLLATE "C" NOT NULL UNIQUE`,
		`canonical_email text COLLATE "C" NOT NULL UNIQUE`,
		"ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY",
		"SET search_path = pg_catalog, app",
		"OWNER TO workouts_security_owner",
		"GRANT CREATE ON SCHEMA app TO workouts_security_owner",
		"REVOKE CREATE ON SCHEMA app FROM workouts_security_owner",
		"CREATE FUNCTION app.issue_password_reset",
		"CREATE FUNCTION app.complete_password_reset",
		"GRANT UPDATE (password_hash,full_name,updated_at)",
		"REVOKE ALL ON app.authentication_principals",
		"CHECK (expires_at = issued_at + interval '7 days')",
		"CHECK (expires_at = issued_at + interval '30 minutes')",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("account lifecycle migration is missing %q", required)
		}
	}
	grant := strings.Index(text, "GRANT CREATE ON SCHEMA app TO workouts_security_owner")
	owner := strings.Index(text, "ALTER FUNCTION app.consume_rate_limit(text,text,bytea) OWNER TO workouts_security_owner")
	revoke := strings.Index(text, "REVOKE CREATE ON SCHEMA app FROM workouts_security_owner")
	if grant < 0 || owner <= grant || revoke <= owner {
		t.Fatal("security-owner CREATE authority is not temporary around ownership transfer")
	}
	if strings.Contains(text, "GRANT SELECT, INSERT, UPDATE ON app.authentication_principals") {
		t.Fatal("API retains broad principal table privileges")
	}
}
