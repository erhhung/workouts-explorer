package migrations_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testDatabases struct {
	ctx       context.Context
	api       *pgxpool.Pool
	worker    *pgxpool.Pool
	migration *pgxpool.Pool
}

func openTestDatabases(t *testing.T) testDatabases {
	t.Helper()
	apiURL, workerURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("WORKER_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if apiURL == "" || workerURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL, WORKER_DATABASE_URL, and MIGRATION_DATABASE_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	result := testDatabases{
		ctx:       ctx,
		api:       openPool(t, ctx, apiURL, 1),
		worker:    openPool(t, ctx, workerURL, 1),
		migration: openPool(t, ctx, migrationURL, 2),
	}
	// Register cancellation last so it runs before pool cleanup and releases blocked goroutines first.
	t.Cleanup(cancel)
	return result
}

func TestRoleDefaultsAndReadiness(t *testing.T) {
	db := openTestDatabases(t)
	assertRuntimeRole(t, db.ctx, db.api, "workouts_api")
	assertRuntimeRole(t, db.ctx, db.worker, "workouts_worker")
	for _, pool := range []*pgxpool.Pool{db.api, db.worker} {
		if !database.Ready(db.ctx, pool) {
			t.Fatal("runtime role is not schema-ready")
		}
		var canUpdate, canDelete bool
		if err := pool.QueryRow(db.ctx, `SELECT has_table_privilege(current_user, 'app.jobs', 'UPDATE'), has_table_privilege(current_user, 'app.jobs', 'DELETE')`).Scan(&canUpdate, &canDelete); err != nil {
			t.Fatal(err)
		}
		if canUpdate || canDelete {
			t.Fatal("runtime role has unsafe direct job mutation privileges")
		}
	}
	if database.Ready(db.ctx, db.migration) {
		t.Fatal("migration role unexpectedly passed runtime readiness")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT app.request_job_cancellation($1,$2)`, uuid.New(), uuid.New()); err == nil {
		t.Fatal("worker retained direct cancellation authority")
	}
	var publicCreate, publicFunction, publicDefaultFunction bool
	if err := db.migration.QueryRow(db.ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_namespace n,
			LATERAL aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) acl
			WHERE n.nspname = 'public' AND acl.grantee = 0 AND acl.privilege_type = 'CREATE'
		), EXISTS (
			SELECT 1 FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace,
			LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) acl
			WHERE n.nspname = 'app' AND p.proname = 'current_account_id'
			  AND acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
		), EXISTS (
			SELECT 1 FROM pg_default_acl defaults
			JOIN pg_namespace n ON n.oid = defaults.defaclnamespace,
			LATERAL aclexplode(defaults.defaclacl) acl
			WHERE n.nspname = 'app' AND defaults.defaclobjtype = 'f'
			  AND acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
		)`).Scan(&publicCreate, &publicFunction, &publicDefaultFunction); err != nil {
		t.Fatal(err)
	}
	if publicCreate || publicFunction || publicDefaultFunction {
		t.Fatal("PUBLIC retains unsafe schema or function privileges")
	}

	if _, err := db.migration.Exec(db.ctx, `REVOKE SELECT ON app.schema_metadata FROM workouts_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT SELECT ON app.schema_metadata TO workouts_api`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored a missing required privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT SELECT ON app.schema_metadata TO workouts_api`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE SELECT(source_id) ON app.job_config_snapshots FROM workouts_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT SELECT(source_id) ON app.job_config_snapshots TO workouts_api`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored a missing snapshot metadata lookup privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT SELECT(source_id) ON app.job_config_snapshots TO workouts_api`); err != nil {
		t.Fatal(err)
	}
	var broadSyncStateRead bool
	if err := db.api.QueryRow(db.ctx, `SELECT has_table_privilege(current_user,'app.source_sync_state','SELECT')`).Scan(&broadSyncStateRead); err != nil {
		t.Fatal(err)
	}
	if broadSyncStateRead {
		t.Fatal("API role has broad source sync state read privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE SELECT(stale_since) ON app.source_sync_state FROM workouts_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT SELECT(stale_since) ON app.source_sync_state TO workouts_api`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored a missing DataSync freshness column privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT SELECT(stale_since) ON app.source_sync_state TO workouts_api`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.delete_source(uuid,uuid) FROM workouts_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.delete_source(uuid,uuid) TO workouts_api`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored a missing source deletion privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.delete_source(uuid,uuid) TO workouts_api`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid) FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid) TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing ingest fence privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid) TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing worker logging context privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.claim_next_worker_job(text,uuid,interval,integer) FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.claim_next_worker_job(text,uuid,interval,integer) TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing schema-7 claim privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.claim_next_worker_job(text,uuid,interval,integer) TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.record_ingest_file_manifest(uuid,text,uuid,jsonb) FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.record_ingest_file_manifest(uuid,text,uuid,jsonb) TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing manifest privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.record_ingest_file_manifest(uuid,text,uuid,jsonb) TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.claim_due_sync_account(text,uuid,interval,integer) FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.claim_due_sync_account(text,uuid,interval,integer) TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing scheduler claim privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.claim_due_sync_account(text,uuid,interval,integer) TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.read_owned_sync_schedule() FROM workouts_api`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.read_owned_sync_schedule() TO workouts_api`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored a missing owner schedule read privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.read_owned_sync_schedule() TO workouts_api`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT SELECT ON app.account_sync_schedules TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `REVOKE SELECT ON app.account_sync_schedules FROM workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored direct worker schedule access")
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE SELECT ON app.account_sync_schedules FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `ALTER FUNCTION app.read_owned_sync_schedule() OWNER TO workouts_migration`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `ALTER FUNCTION app.read_owned_sync_schedule() OWNER TO workouts_security_owner`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored an unsafe scheduler function owner")
	}
	if _, err := db.migration.Exec(db.ctx, `ALTER FUNCTION app.read_owned_sync_schedule() OWNER TO workouts_security_owner`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `ALTER TABLE app.auto_sync_policy RENAME TO auto_sync_policy_readiness_test`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `ALTER TABLE IF EXISTS app.auto_sync_policy_readiness_test RENAME TO auto_sync_policy`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing scheduler policy table")
	}
	if _, err := db.migration.Exec(db.ctx, `ALTER TABLE app.auto_sync_policy_readiness_test RENAME TO auto_sync_policy`); err != nil {
		t.Fatal(err)
	}
	var broadProgressRead, requiredProgressRead, extraProgressRead bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		bool_or(has_table_privilege('workouts_worker','app.job_progress','SELECT')),
		bool_and(has_column_privilege('workouts_worker','app.job_progress',column_name,'SELECT')),
		bool_or(has_column_privilege('workouts_worker','app.job_progress','updated_at','SELECT'))
		FROM unnest(ARRAY['job_id','account_id','files_discovered','files_skipped','files_succeeded','files_failed',
			'workouts_created','workouts_updated','workouts_unchanged','workouts_rejected']) AS required(column_name)`).Scan(
		&broadProgressRead, &requiredProgressRead, &extraProgressRead); err != nil {
		t.Fatal(err)
	}
	if broadProgressRead || !requiredProgressRead || extraProgressRead {
		t.Fatalf("worker progress read grants broad/required/extra=%t/%t/%t", broadProgressRead, requiredProgressRead, extraProgressRead)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE SELECT(files_failed) ON app.job_progress FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT SELECT(files_failed) ON app.job_progress TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing progress reconciliation column")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT SELECT(files_failed) ON app.job_progress TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE EXECUTE ON FUNCTION app.valid_workout_warnings(jsonb) FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `GRANT EXECUTE ON FUNCTION app.valid_workout_warnings(jsonb) TO workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored a missing warning validator privilege")
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT EXECUTE ON FUNCTION app.valid_workout_warnings(jsonb) TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `GRANT SELECT ON app.ingest_write_capabilities TO workouts_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `REVOKE SELECT ON app.ingest_write_capabilities FROM workouts_worker`)
	})
	if database.Ready(db.ctx, db.worker) {
		t.Fatal("readiness ignored direct worker access to ingest capabilities")
	}
	if _, err := db.migration.Exec(db.ctx, `REVOKE SELECT ON app.ingest_write_capabilities FROM workouts_worker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `UPDATE app.schema_metadata SET schema_version = 8, minimum_runtime_version = 8`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `UPDATE app.schema_metadata SET schema_version = 7, minimum_runtime_version = 1`)
	})
	if database.Ready(db.ctx, db.api) {
		t.Fatal("readiness ignored an incompatible minimum runtime version")
	}
}

func TestTenantIsolationAndSingleConnectionReuse(t *testing.T) {
	db := openTestDatabases(t)
	accountA, accountB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, accountA, uuid.Nil, "workout_deletion")
	assertJobVisible(t, db.ctx, db.api, accountA.String(), jobID, true)
	assertJobVisible(t, db.ctx, db.api, accountB.String(), jobID, false)
	requester := accountRequester(t, db, accountA)
	if callBool(t, db.ctx, db.api, accountB, `SELECT app.request_owned_job_cancellation($1,$2)`, jobID, requester) {
		t.Fatal("cross-account cancellation unexpectedly succeeded")
	}
	assertJobVisible(t, db.ctx, db.api, "not-a-uuid", jobID, false)
	assertJobVisible(t, db.ctx, db.api, "", jobID, false)

	tx := beginAccount(t, db.ctx, db.api, accountA)
	if err := tx.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	assertJobVisible(t, db.ctx, db.api, "", jobID, false)
	if _, err := db.api.Exec(db.ctx, `UPDATE app.jobs SET progress_current = 1 WHERE id = $1`, jobID); err == nil {
		t.Fatal("direct update unexpectedly succeeded")
	}
	if _, err := db.api.Exec(db.ctx, `DELETE FROM app.jobs WHERE id = $1`, jobID); err == nil {
		t.Fatal("direct delete unexpectedly succeeded")
	}
}

func TestScheduledEnqueueIsolatesInvalidSourcesAndDerivesParents(t *testing.T) {
	db := openTestDatabases(t)
	type artifacts struct {
		account uuid.UUID
		parent  uuid.UUID
		healthy uuid.UUID
		invalid uuid.UUID
	}
	enqueue := func(t *testing.T, mixed bool) artifacts {
		t.Helper()
		result := artifacts{account: uuid.Must(uuid.NewV7()), parent: uuid.Must(uuid.NewV7()),
			healthy: uuid.Must(uuid.NewV7()), invalid: uuid.Must(uuid.NewV7())}
		token := uuid.New()
		tx := beginAccount(t, db.ctx, db.migration, result.account)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, result.account); err != nil {
			t.Fatal(err)
		}
		for _, sourceID := range []uuid.UUID{result.healthy, result.invalid} {
			if !mixed && sourceID == result.healthy {
				continue
			}
			if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources
				(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope)
				VALUES($1,$2,$3,$3,'health-auto-export-local','connected',true,$4)`,
				sourceID, result.account, "scheduled-"+sourceID.String(), []byte("private-envelope")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.account_sync_schedules
			(account_id,next_run_at,lease_worker,lease_token,lease_expires_at)
			VALUES($1,clock_timestamp()-interval '10 minutes','scheduler-test',$2,clock_timestamp()+interval '2 minutes')`, result.account, token); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}

		children := fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","failureCode":"source-config-invalid"}]`,
			result.invalid, uuid.Must(uuid.NewV7()))
		if mixed {
			children = fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","snapshot":"c2FmZS1zbmFwc2hvdA=="},`+
				`{"sourceId":"%s","generation":1,"childId":"%s","failureCode":"source-config-invalid"}]`,
				result.healthy, uuid.Must(uuid.NewV7()), result.invalid, uuid.Must(uuid.NewV7()))
		}
		var parent uuid.UUID
		var reused bool
		if err := db.worker.QueryRow(db.ctx, `SELECT job_id,reused FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
			result.account, "scheduler-test", token, result.parent, make([]byte, 32), children).Scan(&parent, &reused); err != nil {
			t.Fatal(err)
		}
		if parent != result.parent || reused {
			t.Fatalf("scheduled parent=%s reused=%t", parent, reused)
		}
		if _, err := db.worker.Exec(db.ctx, `SELECT app.finish_sync_account($1,$2,$3,$4)`, result.account, "scheduler-test", token, parent); err != nil {
			t.Fatal(err)
		}
		return result
	}

	mixed := enqueue(t, true)
	check := beginAccount(t, db.ctx, db.migration, mixed.account)
	defer check.Rollback(db.ctx)
	var parentStatus string
	var childCount, failedCount, snapshotCount, contextCount, progressCount int
	if err := check.QueryRow(db.ctx, `SELECT parent.status,count(child.id),count(*) FILTER(WHERE child.status='failed'),
		count(snapshot.job_id),count(context.job_id),count(progress.job_id)
		FROM app.jobs parent JOIN app.jobs child ON child.parent_job_id=parent.id
		LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id=child.id
		LEFT JOIN app.job_source_contexts context ON context.job_id=child.id
		LEFT JOIN app.job_progress progress ON progress.job_id=child.id
		WHERE parent.id=$1 GROUP BY parent.status`, mixed.parent).Scan(&parentStatus, &childCount, &failedCount, &snapshotCount, &contextCount, &progressCount); err != nil {
		t.Fatal(err)
	}
	if parentStatus != "running" || childCount != 2 || failedCount != 1 || snapshotCount != 1 || contextCount != 2 || progressCount != 2 {
		t.Fatalf("mixed artifacts parent=%s children=%d failed=%d snapshots=%d contexts=%d progress=%d",
			parentStatus, childCount, failedCount, snapshotCount, contextCount, progressCount)
	}
	if err := check.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	var healthyChild uuid.UUID
	childLookup := beginAccount(t, db.ctx, db.migration, mixed.account)
	if err := childLookup.QueryRow(db.ctx, `SELECT job.id FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id
		WHERE job.parent_job_id=$1 AND context.source_id=$2`, mixed.parent, mixed.healthy).Scan(&healthyChild); err != nil {
		t.Fatal(err)
	}
	if err := childLookup.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	workerTx := beginAccount(t, db.ctx, db.worker, mixed.account)
	jobLease := uuid.New()
	var claimed, finished bool
	if err := workerTx.QueryRow(db.ctx, `SELECT app.claim_job($1,'ingest-test',$2,interval '2 minutes')`, healthyChild, jobLease).Scan(&claimed); err != nil || !claimed {
		t.Fatalf("claim healthy child=%t err=%v", claimed, err)
	}
	if err := workerTx.QueryRow(db.ctx, `SELECT app.finish_job($1,'ingest-test',$2,'succeeded',NULL,NULL)`, healthyChild, jobLease).Scan(&finished); err != nil || !finished {
		t.Fatalf("finish healthy child=%t err=%v", finished, err)
	}
	if err := workerTx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	partialCheck := beginAccount(t, db.ctx, db.migration, mixed.account)
	var partialStatus, severity string
	if err := partialCheck.QueryRow(db.ctx, `SELECT job.status,notification.severity FROM app.jobs job
		JOIN app.notifications notification ON notification.job_id=job.id WHERE job.id=$1`, mixed.parent).Scan(&partialStatus, &severity); err != nil {
		t.Fatal(err)
	}
	if partialStatus != "partially_succeeded" || severity != "warning" {
		t.Fatalf("mixed terminal parent=%s notification=%s", partialStatus, severity)
	}
	if err := partialCheck.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}

	allInvalid := enqueue(t, false)
	invalidCheck := beginAccount(t, db.ctx, db.migration, allInvalid.account)
	defer invalidCheck.Rollback(db.ctx)
	var invalidParentStatus, failureCode string
	var notifications int
	if err := invalidCheck.QueryRow(db.ctx, `SELECT parent.status,child.failure_code,
		(SELECT count(*) FROM app.notifications WHERE account_id=$1 AND job_id=parent.id)
		FROM app.jobs parent JOIN app.jobs child ON child.parent_job_id=parent.id WHERE parent.id=$2`,
		allInvalid.account, allInvalid.parent).Scan(&invalidParentStatus, &failureCode, &notifications); err != nil {
		t.Fatal(err)
	}
	if invalidParentStatus != "failed" || failureCode != "source-config-invalid" || notifications != 1 {
		t.Fatalf("all-invalid parent=%s code=%s notifications=%d", invalidParentStatus, failureCode, notifications)
	}
}

func TestSchedulerPolicyCadenceAndExponentialBackoff(t *testing.T) {
	db := openTestDatabases(t)
	account, token := uuid.Must(uuid.NewV7()), uuid.New()
	prior := time.Now().UTC().Add(-3*time.Hour - 10*time.Minute).Truncate(time.Microsecond)
	if _, err := db.migration.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, account); err != nil {
		t.Fatal(err)
	}
	if _, err := db.migration.Exec(db.ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at,lease_worker,lease_token,lease_expires_at)
		VALUES($1,$2,'scheduler-policy-test',$3,clock_timestamp()+interval '2 minutes')`, account, prior, token); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.worker.Exec(context.Background(), `SELECT app.configure_auto_sync_policy(interval '24 hours',3)`)
		_, _ = db.migration.Exec(context.Background(), `DELETE FROM app.accounts WHERE id=$1`, account)
	})
	var configured bool
	if err := db.worker.QueryRow(db.ctx, `SELECT app.configure_auto_sync_policy(interval '1 hour',4)`).Scan(&configured); err != nil || !configured {
		t.Fatalf("active-lease policy update configured=%t err=%v", configured, err)
	}
	var finished bool
	if err := db.worker.QueryRow(db.ctx, `SELECT app.finish_sync_account($1,'scheduler-policy-test',$2,NULL)`, account, token).Scan(&finished); err != nil || !finished {
		t.Fatalf("finish schedule=%t err=%v", finished, err)
	}
	var next time.Time
	if err := db.migration.QueryRow(db.ctx, `SELECT next_run_at FROM app.account_sync_schedules WHERE account_id=$1`, account).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.After(time.Now()) || next.Sub(prior) != 4*time.Hour {
		t.Fatalf("next cadence boundary prior=%s next=%s delta=%s", prior, next, next.Sub(prior))
	}

	var failures int
	for failure := 1; failure <= 2; failure++ {
		token = uuid.New()
		if _, err := db.migration.Exec(db.ctx, `UPDATE app.account_sync_schedules SET next_run_at=clock_timestamp()-interval '1 second',
			lease_worker='scheduler-policy-test',lease_token=$2,lease_expires_at=clock_timestamp()+interval '2 minutes'
			WHERE account_id=$1`, account, token); err != nil {
			t.Fatal(err)
		}
		before := time.Now()
		var released bool
		if err := db.worker.QueryRow(db.ctx, `SELECT app.release_sync_account($1,'scheduler-policy-test',$2,interval '30 seconds')`, account, token).Scan(&released); err != nil || !released {
			t.Fatalf("release %d=%t err=%v", failure, released, err)
		}
		if err := db.migration.QueryRow(db.ctx, `SELECT consecutive_failures,next_run_at FROM app.account_sync_schedules WHERE account_id=$1`, account).Scan(&failures, &next); err != nil {
			t.Fatal(err)
		}
		wantDelay := time.Duration(1<<failure) * 30 * time.Second
		if failures != failure || next.Before(before.Add(wantDelay-time.Second)) || next.After(before.Add(wantDelay+2*time.Second)) {
			t.Fatalf("failure=%d stored=%d delay=%s", failure, failures, next.Sub(before))
		}
	}
	token = uuid.New()
	if _, err := db.migration.Exec(db.ctx, `UPDATE app.account_sync_schedules SET consecutive_failures=1000000,
		next_run_at=clock_timestamp()-interval '1 second',lease_worker='scheduler-policy-test',lease_token=$2,
		lease_expires_at=clock_timestamp()+interval '2 minutes' WHERE account_id=$1`, account, token); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	var released bool
	if err := db.worker.QueryRow(db.ctx, `SELECT app.release_sync_account($1,'scheduler-policy-test',$2,interval '5 minutes')`, account, token).Scan(&released); err != nil || !released {
		t.Fatalf("release saturated failure count=%t err=%v", released, err)
	}
	if err := db.migration.QueryRow(db.ctx, `SELECT consecutive_failures,next_run_at FROM app.account_sync_schedules WHERE account_id=$1`, account).Scan(&failures, &next); err != nil {
		t.Fatal(err)
	}
	if failures != 1000000 || next.Before(before.Add(5*time.Minute-time.Second)) || next.After(before.Add(5*time.Minute+2*time.Second)) {
		t.Fatalf("saturated failure stored=%d delay=%s", failures, next.Sub(before))
	}
	token = uuid.New()
	if _, err := db.migration.Exec(db.ctx, `UPDATE app.account_sync_schedules SET next_run_at=clock_timestamp()-interval '1 second',
		lease_worker='scheduler-policy-test',lease_token=$2,lease_expires_at=clock_timestamp()+interval '2 minutes' WHERE account_id=$1`, account, token); err != nil {
		t.Fatal(err)
	}
	if err := db.worker.QueryRow(db.ctx, `SELECT app.finish_sync_account($1,'scheduler-policy-test',$2,NULL)`, account, token).Scan(&finished); err != nil || !finished {
		t.Fatalf("success reset finish=%t err=%v", finished, err)
	}
	if err := db.migration.QueryRow(db.ctx, `SELECT consecutive_failures FROM app.account_sync_schedules WHERE account_id=$1`, account).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("success reset failures=%d err=%v", failures, err)
	}
}

func TestSchedulerClaimAndEnqueueConflictAreUnambiguous(t *testing.T) {
	db := openTestDatabases(t)
	account, sourceID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	setup := beginAccount(t, db.ctx, db.migration, account)
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, account); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope)
		VALUES($1,$2,'Ambiguity source','ambiguity source','health-auto-export-local','connected',true,$3)`,
		sourceID, account, []byte("private-envelope")); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at)
		VALUES($1,'-infinity'::timestamptz)`, account); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	worker, token := "scheduler-ambiguity-test", uuid.New()
	var claimedAccount, claimedToken uuid.UUID
	var claimedNext string
	if err := db.worker.QueryRow(db.ctx, `SELECT account_id,lease_token,next_run_at::text
		FROM app.claim_due_sync_account($1,$2,interval '2 minutes',7)`, worker, token).
		Scan(&claimedAccount, &claimedToken, &claimedNext); err != nil {
		t.Fatalf("direct scheduler claim: %v", err)
	}
	if claimedAccount != account || claimedToken != token || claimedNext != "-infinity" {
		t.Fatalf("direct scheduler claim account/token/next=%s/%s/%v", claimedAccount, claimedToken, claimedNext)
	}

	coalescingKey := bytes.Repeat([]byte{7}, 32)
	parentA, childA := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	childrenA := fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","snapshot":"c2FmZS1zbmFwc2hvdA=="}]`, sourceID, childA)
	var jobID uuid.UUID
	var reused bool
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,reused FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
		account, worker, token, parentA, coalescingKey, childrenA).Scan(&jobID, &reused); err != nil {
		t.Fatalf("direct scheduled enqueue: %v", err)
	}
	if jobID != parentA || reused {
		t.Fatalf("first scheduled enqueue job/reused=%s/%t", jobID, reused)
	}

	parentB, childB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	childrenB := fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","snapshot":"c2FmZS1zbmFwc2hvdA=="}]`, sourceID, childB)
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,reused FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
		account, worker, token, parentB, coalescingKey, childrenB).Scan(&jobID, &reused); err != nil {
		t.Fatalf("direct scheduled enqueue conflict: %v", err)
	}
	if jobID != parentA || !reused {
		t.Fatalf("reused scheduled enqueue job/reused=%s/%t", jobID, reused)
	}
}

func TestSchedulerAccountLifecycleStopsClaimedAndUnseededAccounts(t *testing.T) {
	db := openTestDatabases(t)
	claimedAccount, sourceID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	seededBeforeDeleting, neverActiveDeleting := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	setup := beginAccount(t, db.ctx, db.migration, claimedAccount)
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.accounts(id,state) VALUES($1,'active'),($2,'active'),($3,'deleting')`,
		claimedAccount, seededBeforeDeleting, neverActiveDeleting); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope)
		VALUES($1,$2,'Lifecycle source','lifecycle source','health-auto-export-local','connected',true,$3)`,
		sourceID, claimedAccount, []byte("private-envelope")); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at)
		VALUES($1,'-infinity'::timestamptz)`, claimedAccount); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	token := uuid.New()
	var account, claimedToken uuid.UUID
	if err := db.worker.QueryRow(db.ctx, `SELECT account_id,lease_token
		FROM app.claim_due_sync_account('lifecycle-worker',$1,interval '2 minutes',7)`, token).Scan(&account, &claimedToken); err != nil {
		t.Fatal(err)
	}
	if account != claimedAccount || claimedToken != token {
		t.Fatalf("claimed account/token=%s/%s", account, claimedToken)
	}
	// Model a scheduler process that read its leased sources before account deletion committed.
	workerTx, err := db.worker.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := workerTx.Query(db.ctx, `SELECT * FROM app.read_leased_sync_sources($1,'lifecycle-worker',$2)`, claimedAccount, token)
	if err != nil {
		_ = workerTx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		_ = workerTx.Rollback(db.ctx)
		t.Fatal("active account did not expose its leased scheduler source")
	}
	rows.Close()
	if _, err := db.migration.Exec(db.ctx, `UPDATE app.accounts SET state='deleting' WHERE id IN ($1,$2)`, claimedAccount, seededBeforeDeleting); err != nil {
		_ = workerTx.Rollback(db.ctx)
		t.Fatal(err)
	}
	var blockedJob *uuid.UUID
	err = workerTx.QueryRow(db.ctx, `SELECT job_id FROM app.enqueue_leased_scheduled_ingest(
		$1,'lifecycle-worker',$2,$3,$4,$5::jsonb)`, claimedAccount, token, uuid.Must(uuid.NewV7()), make([]byte, 32),
		fmt.Sprintf(`[{
			"sourceId":"%s","generation":1,"childId":"%s","snapshot":"c2FmZS1zbmFwc2hvdA=="
		}]`, sourceID, uuid.Must(uuid.NewV7()))).Scan(&blockedJob)
	if !errors.Is(err, pgx.ErrNoRows) {
		_ = workerTx.Rollback(db.ctx)
		t.Fatalf("deleting account enqueue job=%v err=%v, want no rows", blockedJob, err)
	}
	if err := workerTx.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	var released bool
	if err := db.worker.QueryRow(db.ctx, `SELECT app.release_sync_account(
		$1,'lifecycle-worker',$2,interval '1 second')`, claimedAccount, token).Scan(&released); err != nil || !released {
		t.Fatalf("deleting account lease release=%t err=%v", released, err)
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT * FROM app.claim_due_sync_account(
		'lifecycle-seed-worker',$1,interval '2 minutes',7)`, uuid.New()); err != nil {
		t.Fatal(err)
	}
	var retainedSchedules, neverActiveSchedules, scheduledJobs, activeLeases int
	if err := db.migration.QueryRow(db.ctx, `SELECT
		(SELECT count(*) FROM app.account_sync_schedules WHERE account_id=$1),
		(SELECT count(*) FROM app.account_sync_schedules WHERE account_id=$2),
		(SELECT count(*) FROM app.jobs WHERE account_id=$3 AND kind='scheduled_ingest'),
		(SELECT count(*) FROM app.account_sync_schedules WHERE account_id=$3 AND lease_token IS NOT NULL)`,
		seededBeforeDeleting, neverActiveDeleting, claimedAccount).Scan(&retainedSchedules, &neverActiveSchedules, &scheduledJobs, &activeLeases); err != nil {
		t.Fatal(err)
	}
	if retainedSchedules != 1 || neverActiveSchedules != 0 || scheduledJobs != 0 || activeLeases != 0 {
		t.Fatalf("deleting lifecycle retained/never-active schedules/jobs/leases=%d/%d/%d/%d",
			retainedSchedules, neverActiveSchedules, scheduledJobs, activeLeases)
	}
}

func TestConcurrentSchedulerReplicasEnqueueOneParent(t *testing.T) {
	db := openTestDatabases(t)
	account, sourceID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	setup := beginAccount(t, db.ctx, db.migration, account)
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, account); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope)
		VALUES($1,$2,'Replica source','replica source','health-auto-export-local','connected',true,$3)`,
		sourceID, account, []byte("private-envelope")); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at)
		VALUES($1,'-infinity'::timestamptz)`, account); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	workerURL := os.Getenv("WORKER_DATABASE_URL")
	type result struct {
		target bool
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for replica := 0; replica < 2; replica++ {
		pool := openPool(t, db.ctx, workerURL, 1)
		wg.Add(1)
		go func(replica int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			worker, token := fmt.Sprintf("scheduler-replica-%d", replica), uuid.New()
			var claimedAccount, claimedToken uuid.UUID
			err := pool.QueryRow(ctx, `SELECT account_id,lease_token
				FROM app.claim_due_sync_account($1,$2,interval '2 minutes',7)`, worker, token).Scan(&claimedAccount, &claimedToken)
			if errors.Is(err, pgx.ErrNoRows) {
				results <- result{}
				return
			}
			if err != nil {
				results <- result{err: err}
				return
			}
			if claimedAccount != account {
				_, err = pool.Exec(ctx, `SELECT app.release_sync_account($1,$2,$3,interval '1 second')`, claimedAccount, worker, claimedToken)
				results <- result{err: err}
				return
			}
			parent, child := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			children := fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","snapshot":"c2FmZS1zbmFwc2hvdA=="}]`, sourceID, child)
			var jobID uuid.UUID
			var reused bool
			err = pool.QueryRow(ctx, `SELECT job_id,reused FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
				account, worker, claimedToken, parent, make([]byte, 32), children).Scan(&jobID, &reused)
			if err == nil {
				var finished bool
				err = pool.QueryRow(ctx, `SELECT app.finish_sync_account($1,$2,$3,$4)`, account, worker, claimedToken, jobID).Scan(&finished)
				if err == nil && !finished {
					err = errors.New("scheduler lease was lost")
				}
			}
			results <- result{target: true, err: err}
		}(replica)
	}
	close(start)
	wg.Wait()
	close(results)
	targetClaims := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.target {
			targetClaims++
		}
	}
	var parents int
	if err := db.migration.QueryRow(db.ctx, `SELECT count(*) FROM app.jobs
		WHERE account_id=$1 AND kind='scheduled_ingest' AND parent_job_id IS NULL`, account).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if targetClaims != 1 || parents != 1 {
		t.Fatalf("concurrent scheduler target claims/parents=%d/%d", targetClaims, parents)
	}
}

func TestDeferredSnapshotConsistencyAcceptsOrdinaryAndSourceJobCommits(t *testing.T) {
	db := openTestDatabases(t)
	ordinaryAccount, ordinaryJob := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tx := beginAccount(t, db.ctx, db.api, ordinaryAccount)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, ordinaryAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,account_id,kind,priority)
		VALUES($1,$2,'workout_deletion',80)`, ordinaryJob, ordinaryAccount); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatalf("ordinary non-source job failed deferred commit: %v", err)
	}

	sourceAccount, sourceID, sourceJob := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tx = beginAccount(t, db.ctx, db.api, sourceAccount)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, sourceAccount); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Deferred Trigger','deferred trigger','health-auto-export-local',$3)`,
		sourceID, sourceAccount, []byte(`{"encrypted":true}`)); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,account_id,kind,priority)
		VALUES($1,$2,'source_connection_check',100)`, sourceJob, sourceAccount); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope)
		VALUES($1,$2,$3,1,$4)`, sourceJob, sourceAccount, sourceID, []byte(`{"snapshot":true}`)); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatalf("source job and snapshot failed deferred commit: %v", err)
	}
	if status, _ := readStatus(t, db.ctx, db.api, ordinaryAccount, ordinaryJob); status != "queued" {
		t.Fatalf("ordinary committed job status=%s", status)
	}
	if status, _ := readStatus(t, db.ctx, db.api, sourceAccount, sourceJob); status != "queued" || snapshotCount(t, db, sourceAccount, sourceJob) != 1 {
		t.Fatalf("source committed job status=%s snapshot count=%d", status, snapshotCount(t, db, sourceAccount, sourceJob))
	}
	if !requestCancellation(t, db, ordinaryAccount, ordinaryJob) || !requestCancellation(t, db, sourceAccount, sourceJob) {
		t.Fatal("could not clean up deferred trigger regression fixtures")
	}
}

func TestDeleteSourceFunctionOwnershipRLSAndAtomicCleanup(t *testing.T) {
	db := openTestDatabases(t)
	account, foreignAccount := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	sourceID := snapshotSourceID(t, db, account, jobID)
	requester := uuid.Must(uuid.NewV7())
	if callBool(t, db.ctx, db.api, foreignAccount, `SELECT app.delete_source($1,$2)`, sourceID, requester) {
		t.Fatal("foreign account deleted a source")
	}
	if callBool(t, db.ctx, db.api, account, `SELECT app.delete_source($1,$2)`, uuid.Must(uuid.NewV7()), requester) {
		t.Fatal("absent source deletion returned true")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT app.delete_source($1,$2)`, sourceID, requester); err == nil {
		t.Fatal("worker executed API-only source deletion")
	}
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.sources
		SET auto_sync_enabled=false,config_envelope=NULL,deleted_at=transaction_timestamp() WHERE id=$1`, sourceID); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("API directly tombstoned a source")
	}
	_ = tx.Rollback(db.ctx)

	if !callBool(t, db.ctx, db.api, account, `SELECT app.delete_source($1,$2)`, sourceID, requester) {
		t.Fatal("source deletion returned false")
	}
	if callBool(t, db.ctx, db.api, account, `SELECT app.delete_source($1,$2)`, sourceID, requester) {
		t.Fatal("already deleted source deletion returned true")
	}
	if status, _ := readStatus(t, db.ctx, db.api, account, jobID); status != "cancelled" {
		t.Fatalf("deleted source job status=%s", status)
	}
	if snapshotCount(t, db, account, jobID) != 0 {
		t.Fatal("deleted source retained queued job snapshot")
	}
	tx = beginAccount(t, db.ctx, db.migration, account)
	var autoSync bool
	var config []byte
	var deletedAt *time.Time
	if err := tx.QueryRow(db.ctx, `SELECT auto_sync_enabled,config_envelope,deleted_at FROM app.sources WHERE id=$1`, sourceID).Scan(
		&autoSync, &config, &deletedAt); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	_ = tx.Rollback(db.ctx)
	if autoSync || config != nil || deletedAt == nil {
		t.Fatalf("source tombstone state auto_sync=%t config=%v deleted_at=%v", autoSync, config, deletedAt)
	}
	var owner string
	var apiExecute, workerExecute, publicExecute, apiDirectTombstone bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		(SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid='app.delete_source(uuid,uuid)'::regprocedure),
		has_function_privilege('workouts_api','app.delete_source(uuid,uuid)','EXECUTE'),
		has_function_privilege('workouts_worker','app.delete_source(uuid,uuid)','EXECUTE'),
		EXISTS (SELECT 1 FROM pg_proc p,
			LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
			WHERE p.oid='app.delete_source(uuid,uuid)'::regprocedure
			  AND acl.grantee=0 AND acl.privilege_type='EXECUTE'),
		has_column_privilege('workouts_api','app.sources','deleted_at','UPDATE')`).Scan(
		&owner, &apiExecute, &workerExecute, &publicExecute, &apiDirectTombstone); err != nil {
		t.Fatal(err)
	}
	if owner != "workouts_security_owner" || !apiExecute || workerExecute || publicExecute || apiDirectTombstone {
		t.Fatalf("unsafe delete source ownership/grants: owner=%s api=%t worker=%t public=%t direct=%t",
			owner, apiExecute, workerExecute, publicExecute, apiDirectTombstone)
	}
}

func TestAccountLifecycleRolesAndPreferenceRLS(t *testing.T) {
	db := openTestDatabases(t)
	accountA, accountB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principalA, principalB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	runSuffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	for _, fixture := range []struct {
		account, principal uuid.UUID
		suffix             string
	}{{accountA, principalA, "a"}, {accountB, principalB, "b"}} {
		tx := beginAccount(t, db.ctx, db.api, fixture.account)
		defer tx.Rollback(db.ctx)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Test User')`, fixture.principal, "user"+fixture.suffix+runSuffix, "user"+fixture.suffix+runSuffix+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, fixture.account); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, fixture.principal, fixture.account); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.preferences(account_id) VALUES($1)`, fixture.account); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}
	tx := beginAccount(t, db.ctx, db.api, accountA)
	var visible int
	if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.preferences`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 1 {
		t.Fatalf("account A saw %d preference rows", visible)
	}
	if command, err := tx.Exec(db.ctx, `UPDATE app.preferences SET theme='light' WHERE account_id=$1`, accountB); err != nil || command.RowsAffected() != 0 {
		t.Fatalf("foreign preference update affected %d: %v", command.RowsAffected(), err)
	}
	_ = tx.Rollback(db.ctx)
	if _, err := db.worker.Exec(db.ctx, `SELECT id FROM app.authentication_principals LIMIT 1`); err == nil {
		t.Fatal("worker unexpectedly read global authentication principals")
	}
	var ownerLogin, ownerBypass bool
	if err := db.migration.QueryRow(db.ctx, `SELECT rolcanlogin,rolbypassrls FROM pg_roles WHERE rolname='workouts_security_owner'`).Scan(&ownerLogin, &ownerBypass); err != nil || ownerLogin || ownerBypass {
		t.Fatalf("unsafe security owner: login=%t bypass=%t err=%v", ownerLogin, ownerBypass, err)
	}
	var ownerCreate, apiAdminRead, apiResetInsert, apiPasswordUpdate, apiRoleUpdate bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		has_schema_privilege('workouts_security_owner','app','CREATE'),
		has_table_privilege('workouts_api','app.administrators','SELECT'),
		has_table_privilege('workouts_api','app.password_resets','INSERT'),
		has_column_privilege('workouts_api','app.authentication_principals','password_hash','UPDATE'),
		has_column_privilege('workouts_api','app.authentication_principals','role','UPDATE')`).Scan(&ownerCreate, &apiAdminRead, &apiResetInsert, &apiPasswordUpdate, &apiRoleUpdate); err != nil {
		t.Fatal(err)
	}
	if ownerCreate || apiAdminRead || apiResetInsert || !apiPasswordUpdate || apiRoleUpdate {
		t.Fatalf("unexpected lifecycle grants: ownerCreate=%t adminRead=%t resetInsert=%t passwordUpdate=%t roleUpdate=%t", ownerCreate, apiAdminRead, apiResetInsert, apiPasswordUpdate, apiRoleUpdate)
	}
	for _, function := range []string{"consume_rate_limit", "issue_password_reset", "complete_password_reset", "enforce_principal_role_consistency"} {
		var owner string
		if err := db.migration.QueryRow(db.ctx, `SELECT pg_get_userbyid(proowner) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='app' AND p.proname=$1`, function).Scan(&owner); err != nil || owner != "workouts_security_owner" {
			t.Fatalf("function %s owner=%q err=%v", function, owner, err)
		}
	}
	inconsistent := uuid.Must(uuid.NewV7())
	tx = beginAccount(t, db.ctx, db.api, uuid.Must(uuid.NewV7()))
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Test')`, inconsistent, "orphan"+inconsistent.String()[:8], "orphan"+inconsistent.String()[:8]+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err == nil {
		t.Fatal("orphan user principal passed deferred role consistency")
	}
}

func TestDatabaseOwnedRateLimitProfiles(t *testing.T) {
	db := openTestDatabases(t)
	digest := make([]byte, 32)
	if _, err := rand.Read(digest); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 11; attempt++ {
		var allowed bool
		var retry int
		if err := db.api.QueryRow(db.ctx, `SELECT allowed,retry_after_seconds FROM app.consume_rate_limit('signin','subject',$1)`, digest).Scan(&allowed, &retry); err != nil {
			t.Fatal(err)
		}
		if allowed != (attempt <= 10) || retry < 1 || retry > 600 {
			t.Fatalf("attempt=%d allowed=%t retry=%d", attempt, allowed, retry)
		}
	}
	if _, err := db.api.Exec(db.ctx, `SELECT app.consume_rate_limit('invented','subject',$1)`, digest); err == nil {
		t.Fatal("unknown rate-limit operation was accepted")
	}
	var windowAligned bool
	if err := db.migration.QueryRow(db.ctx, `SELECT bool_and(extract(epoch FROM window_start)::bigint % 600 = 0 AND window_end-window_start=interval '10 minutes') FROM app.rate_limits WHERE operation='signin'`).Scan(&windowAligned); err != nil || !windowAligned {
		t.Fatalf("database rate window is not aligned: %t err=%v", windowAligned, err)
	}
	concurrentDigest := make([]byte, 32)
	if _, err := rand.Read(concurrentDigest); err != nil {
		t.Fatal(err)
	}
	results := make(chan bool, 20)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var allowed bool
			var retry int
			if err := db.api.QueryRow(db.ctx, `SELECT allowed,retry_after_seconds FROM app.consume_rate_limit('signin','subject',$1)`, concurrentDigest).Scan(&allowed, &retry); err != nil {
				t.Error(err)
				return
			}
			results <- allowed
		}()
	}
	wait.Wait()
	close(results)
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 10 {
		t.Fatalf("concurrent rate-limit allowed=%d, want 10", allowedCount)
	}
}

func TestStandaloneTransitionsFencingCancellationAndAttempts(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1, $2, $3, interval '1 minute')`, jobID, "worker-a", lease) {
		t.Fatal("claim failed")
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.heartbeat_job($1, $2, $3, interval '1 minute')`, jobID, "worker-a", uuid.New()) {
		t.Fatal("stale lease heartbeat succeeded")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.heartbeat_job($1, $2, $3, interval '1 minute')`, jobID, "worker-a", lease) {
		t.Fatal("heartbeat failed")
	}
	if !requestCancellation(t, db, account, jobID) {
		t.Fatal("running cancellation request failed")
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1, $2, $3, 'succeeded')`, jobID, "worker-a", uuid.New()) {
		t.Fatal("stale lease completion succeeded")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1, $2, $3, 'cancelled')`, jobID, "worker-a", lease) {
		t.Fatal("fenced cancellation completion failed")
	}
	status, attempt := readStatus(t, db.ctx, db.worker, account, jobID)
	if status != "cancelled" || attempt != 1 {
		t.Fatalf("status=%s attempt=%d", status, attempt)
	}
	if requestCancellation(t, db, account, jobID) {
		t.Fatal("terminal cancellation unexpectedly succeeded")
	}

	queued := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "workout_deletion")
	if !requestCancellation(t, db, account, queued) {
		t.Fatal("queued cancellation failed")
	}
	status, attempt = readStatus(t, db.ctx, db.worker, account, queued)
	if status != "cancelled" || attempt != 0 {
		t.Fatalf("queued cancellation status=%s attempt=%d", status, attempt)
	}
}

func TestExpiredLeaseCannotHeartbeatOrFinish(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'late-worker',$2,interval '1 millisecond')`, jobID, lease) {
		t.Fatal("claim failed")
	}
	time.Sleep(5 * time.Millisecond)
	if callBool(t, db.ctx, db.worker, account, `SELECT app.heartbeat_job($1,'late-worker',$2,interval '1 minute')`, jobID, lease) {
		t.Fatal("late heartbeat renewed an expired matching lease")
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'late-worker',$2,'succeeded')`, jobID, lease) {
		t.Fatal("stale direct finish terminalized an expired matching lease")
	}
	if status, _ := readStatus(t, db.ctx, db.worker, account, jobID); status != "running" {
		t.Fatalf("expired job status=%s, want running until recovery", status)
	}
	if snapshotCount(t, db, account, jobID) != 1 {
		t.Fatal("stale direct finish deleted the job snapshot")
	}
}

func TestSourceCompletionAndDeletionUseSameLockOrder(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	sourceID := snapshotSourceID(t, db, account, jobID)
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'lock-worker',$2,interval '1 minute')`, jobID, lease) {
		t.Fatal("claim failed")
	}

	deletion := beginAccount(t, db.ctx, db.migration, account)
	deletionOpen := true
	defer func() {
		if deletionOpen {
			_ = deletion.Rollback(context.Background())
		}
	}()
	if _, err := deletion.Exec(db.ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := deletion.Exec(db.ctx, `SELECT true FROM app.sources WHERE id=$1 FOR UPDATE`, sourceID); err != nil {
		t.Fatal(err)
	}
	completion, err := db.worker.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := completion.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
		_ = completion.Rollback(db.ctx)
		t.Fatal(err)
	}
	pid := completion.Conn().PgConn().PID()

	type completionResult struct {
		finished, sourceUpdated bool
		err                     error
	}
	completed := make(chan completionResult, 1)
	go func() {
		defer completion.Rollback(db.ctx)
		var result completionResult
		result.err = completion.QueryRow(db.ctx, `SELECT finished,source_updated FROM app.complete_source_connection_check($1,'lock-worker',$2,'connected')`, jobID, lease).Scan(&result.finished, &result.sourceUpdated)
		if result.err == nil {
			result.err = completion.Commit(db.ctx)
		}
		completed <- result
	}()

	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		if err := db.migration.QueryRow(db.ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completion did not block on the source lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := deletion.Exec(db.ctx, `SELECT j.id FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id WHERE s.source_id=$1 AND j.status IN ('queued','running') FOR UPDATE OF j`, sourceID); err != nil {
		t.Fatalf("deletion could not acquire job after source: %v", err)
	}
	if _, err := deletion.Exec(db.ctx, `SELECT app.request_job_cancellation($1,$2)`, jobID, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatal(err)
	}
	if _, err := deletion.Exec(db.ctx, `UPDATE app.sources SET auto_sync_enabled=false,config_envelope=NULL,deleted_at=transaction_timestamp() WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := deletion.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	deletionOpen = false

	var result completionResult
	select {
	case result = <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("completion remained blocked after deletion committed")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.finished || result.sourceUpdated {
		t.Fatalf("completion after tombstone finished=%t source_updated=%t", result.finished, result.sourceUpdated)
	}
	if status, _ := readStatus(t, db.ctx, db.worker, account, jobID); status != "cancelled" {
		t.Fatalf("completion after deletion status=%s, want cancelled", status)
	}
}

func TestSourceSnapshotIsolationFencingImmutabilityAndCleanup(t *testing.T) {
	db := openTestDatabases(t)
	account, foreignAccount := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")

	if _, err := db.worker.Exec(db.ctx, `SELECT config_envelope FROM app.job_config_snapshots WHERE job_id=$1`, jobID); err == nil {
		t.Fatal("worker unexpectedly has an unfenced snapshot table read")
	}
	if snapshotCount(t, db, foreignAccount, jobID) != 0 {
		t.Fatal("foreign account saw a job snapshot")
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1, 'snapshot-worker', $2, interval '1 minute')`, jobID, lease) {
		t.Fatal("snapshot job claim failed")
	}
	tx := beginAccount(t, db.ctx, db.migration, account)
	var envelope []byte
	if err := tx.QueryRow(db.ctx, `SELECT config_envelope FROM app.read_job_config_snapshot($1,'snapshot-worker',$2)`, jobID, uuid.New()).Scan(&envelope); err != pgx.ErrNoRows {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("stale lease snapshot read error=%v, want no rows", err)
	}
	if err := tx.QueryRow(db.ctx, `SELECT config_envelope FROM app.read_job_config_snapshot($1,'snapshot-worker',$2)`, jobID, lease).Scan(&envelope); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	_ = tx.Rollback(db.ctx)

	tx = beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.job_config_snapshots SET config_envelope=$2 WHERE job_id=$1`, jobID, []byte("changed")); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("snapshot update unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)

	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'snapshot-worker',$2,'succeeded')`, jobID, lease) {
		t.Fatal("snapshot job finish failed")
	}
	if snapshotCount(t, db, account, jobID) != 0 {
		t.Fatal("finished job retained its snapshot")
	}

	queued := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	if !requestCancellation(t, db, account, queued) || snapshotCount(t, db, account, queued) != 0 {
		t.Fatal("queued cancellation did not atomically remove its snapshot")
	}

	recovering := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'snapshot-worker',$2,interval '1 millisecond')`, recovering, uuid.New()) ||
		!requestCancellation(t, db, account, recovering) {
		t.Fatal("could not prepare cancelled lease recovery")
	}
	time.Sleep(5 * time.Millisecond)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.recover_expired_job($1)`, recovering) || snapshotCount(t, db, account, recovering) != 0 {
		t.Fatal("cancelled lease recovery did not atomically remove its snapshot")
	}
}

func TestWorkerJobLogContextFencingIdentityIsolationAndSourceLifecycle(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	sourceID := snapshotSourceID(t, db, account, jobID)
	principalID := uuid.Must(uuid.NewV7())
	identity := strings.ReplaceAll(principalID.String(), "-", "")
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.authentication_principals
		(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Context Owner')`,
		principalID, "context-"+identity, "context-"+identity+"@example.test"); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principalID, account); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'context-worker',$2,interval '1 minute')`, jobID, lease) {
		t.Fatal("context fixture claim failed")
	}
	ownerUsername := "context-" + identity
	readContext := func(id, token uuid.UUID, worker string) (string, string, string, error) {
		var owner, sourceName, sourceType string
		err := db.worker.QueryRow(db.ctx, `SELECT owner_username,source_name,source_type
			FROM app.read_worker_job_log_context($1,$2,$3)`, id, worker, token).
			Scan(&owner, &sourceName, &sourceType)
		return owner, sourceName, sourceType, err
	}
	owner, sourceName, sourceType, err := readContext(jobID, lease, "context-worker")
	if err != nil || owner != ownerUsername || sourceName != "source-"+sourceID.String() || sourceType != "health-auto-export-local" {
		t.Fatalf("context owner/source/type=%q/%q/%q err=%v", owner, sourceName, sourceType, err)
	}
	if _, _, _, err := readContext(jobID, uuid.New(), "context-worker"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong lease context error=%v, want no rows", err)
	}
	if _, _, _, err := readContext(jobID, lease, "other-worker"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong worker context error=%v, want no rows", err)
	}
	if _, err := db.api.Exec(db.ctx, `SELECT * FROM app.read_worker_job_log_context($1,'context-worker',$2)`, jobID, lease); err == nil {
		t.Fatal("API executed worker-only logging context function")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT full_name FROM app.authentication_principals WHERE id=$1`, principalID); err == nil {
		t.Fatal("worker directly read principal identity")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT display_name FROM app.sources WHERE id=$1`, sourceID); err == nil {
		t.Fatal("worker directly read source identity")
	}
	priorAccount := uuid.Must(uuid.NewV7())
	contextTx := beginAccount(t, db.ctx, db.worker, priorAccount)
	var contextOwner string
	if err := contextTx.QueryRow(db.ctx, `SELECT owner_username FROM app.read_worker_job_log_context($1,'context-worker',$2)`, jobID, lease).Scan(&contextOwner); err != nil {
		_ = contextTx.Rollback(db.ctx)
		t.Fatal(err)
	}
	var restoredAccount *uuid.UUID
	if err := contextTx.QueryRow(db.ctx, `SELECT app.current_account_id()`).Scan(&restoredAccount); err != nil || restoredAccount == nil || *restoredAccount != priorAccount {
		_ = contextTx.Rollback(db.ctx)
		t.Fatalf("nonempty account context was not restored: account=%v err=%v", restoredAccount, err)
	}
	_ = contextTx.Rollback(db.ctx)
	contextTx, err = db.worker.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := contextTx.QueryRow(db.ctx, `SELECT owner_username FROM app.read_worker_job_log_context($1,'context-worker',$2)`, jobID, lease).Scan(&contextOwner); err != nil {
		_ = contextTx.Rollback(db.ctx)
		t.Fatal(err)
	}
	restoredAccount = nil
	if err := contextTx.QueryRow(db.ctx, `SELECT app.current_account_id()`).Scan(&restoredAccount); err != nil || restoredAccount != nil {
		_ = contextTx.Rollback(db.ctx)
		t.Fatalf("unset account context was not restored: account=%v err=%v", restoredAccount, err)
	}
	_ = contextTx.Rollback(db.ctx)

	deleteSourceFixture(t, db, account, sourceID)
	owner, sourceName, sourceType, err = readContext(jobID, lease, "context-worker")
	if err != nil || owner != ownerUsername || sourceName != "source-"+sourceID.String() || sourceType != "health-auto-export-local" {
		t.Fatalf("tombstoned snapshot context=%q/%q/%q err=%v", owner, sourceName, sourceType, err)
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'context-worker',$2,'cancelled')`, jobID, lease) {
		t.Fatal("context fixture cleanup failed")
	}
	if _, _, _, err := readContext(jobID, lease, "context-worker"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("terminal job context error=%v, want no rows", err)
	}

	reclaimedJob := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	expiredLease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'expired-context-worker',$2,interval '1 millisecond')`, reclaimedJob, expiredLease) {
		t.Fatal("expired context fixture claim failed")
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, _, err := readContext(reclaimedJob, expiredLease, "expired-context-worker"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired lease context error=%v, want no rows", err)
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.recover_expired_job($1)`, reclaimedJob) {
		t.Fatal("expired context fixture recovery failed")
	}
	reclaimedLease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'reclaimed-context-worker',$2,interval '1 minute')`, reclaimedJob, reclaimedLease) {
		t.Fatal("reclaimed context fixture claim failed")
	}
	if _, _, _, err := readContext(reclaimedJob, expiredLease, "expired-context-worker"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("replaced ownership context error=%v, want no rows", err)
	}
	owner, _, _, err = readContext(reclaimedJob, reclaimedLease, "reclaimed-context-worker")
	if err != nil || owner != ownerUsername {
		t.Fatalf("reclaimed context owner=%q err=%v", owner, err)
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'reclaimed-context-worker',$2,'succeeded')`, reclaimedJob, reclaimedLease) {
		t.Fatal("reclaimed context fixture cleanup failed")
	}

	var ownerRole string
	var workerExecute, apiExecute, publicExecute bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		pg_get_userbyid(proowner),
		has_function_privilege('workouts_worker',p.oid,'EXECUTE'),
		has_function_privilege('workouts_api',p.oid,'EXECUTE'),
		EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
			WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE')
		FROM pg_proc p WHERE p.oid='app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure`).Scan(
		&ownerRole, &workerExecute, &apiExecute, &publicExecute); err != nil {
		t.Fatal(err)
	}
	if ownerRole != "workouts_security_owner" || !workerExecute || apiExecute || publicExecute {
		t.Fatalf("unsafe context function grants owner=%s worker=%t api=%t public=%t", ownerRole, workerExecute, apiExecute, publicExecute)
	}
}

func TestDeletedSourceSnapshotLifecycle(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	runningJob := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	runningSource := snapshotSourceID(t, db, account, runningJob)
	tx := beginAccount(t, db.ctx, db.api, account)
	var lookupJob, lookupAccount, lookupSource uuid.UUID
	var lookupGeneration int64
	if err := tx.QueryRow(db.ctx, `SELECT job_id,account_id,source_id,source_generation
		FROM app.job_config_snapshots WHERE source_id=$1`, runningSource).Scan(
		&lookupJob, &lookupAccount, &lookupSource, &lookupGeneration); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("API could not identify active source job without ciphertext: %v", err)
	}
	_ = tx.Rollback(db.ctx)
	if lookupJob != runningJob || lookupAccount != account || lookupSource != runningSource || lookupGeneration != 1 {
		t.Fatal("API source-job lookup returned inconsistent snapshot metadata")
	}
	if _, err := db.api.Exec(db.ctx, `SELECT config_envelope FROM app.job_config_snapshots WHERE job_id=$1`, runningJob); err == nil {
		t.Fatal("API unexpectedly read snapshot ciphertext")
	}
	var apiJobID, apiAccountID, apiSourceID, apiGeneration, apiEnvelope, apiCreatedAt, workerJobID bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		has_column_privilege('workouts_api','app.job_config_snapshots','job_id','SELECT'),
		has_column_privilege('workouts_api','app.job_config_snapshots','account_id','SELECT'),
		has_column_privilege('workouts_api','app.job_config_snapshots','source_id','SELECT'),
		has_column_privilege('workouts_api','app.job_config_snapshots','source_generation','SELECT'),
		has_column_privilege('workouts_api','app.job_config_snapshots','config_envelope','SELECT'),
		has_column_privilege('workouts_api','app.job_config_snapshots','created_at','SELECT'),
		has_column_privilege('workouts_worker','app.job_config_snapshots','job_id','SELECT')`).Scan(
		&apiJobID, &apiAccountID, &apiSourceID, &apiGeneration, &apiEnvelope, &apiCreatedAt, &workerJobID); err != nil {
		t.Fatal(err)
	}
	if !apiJobID || !apiAccountID || !apiSourceID || !apiGeneration || apiEnvelope || apiCreatedAt || workerJobID {
		t.Fatalf("unexpected snapshot SELECT grants: api job=%t account=%t source=%t generation=%t envelope=%t created=%t worker job=%t",
			apiJobID, apiAccountID, apiSourceID, apiGeneration, apiEnvelope, apiCreatedAt, workerJobID)
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'deleted-source-worker',$2,interval '1 minute')`, runningJob, lease) {
		t.Fatal("source job claim failed")
	}
	deleteSourceFixture(t, db, account, runningSource)

	tx = beginAccount(t, db.ctx, db.worker, account)
	var envelope []byte
	if err := tx.QueryRow(db.ctx, `SELECT config_envelope FROM app.read_job_config_snapshot($1,'deleted-source-worker',$2)`, runningJob, lease).Scan(&envelope); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("running job could not read snapshot after source deletion: %v", err)
	}
	_ = tx.Rollback(db.ctx)

	if !requestCancellation(t, db, account, runningJob) {
		t.Fatal("running cancellation intent failed after source deletion")
	}
	if snapshotCount(t, db, account, runningJob) != 1 {
		t.Fatal("running cancellation intent removed snapshot before cooperative cleanup")
	}
	tx = beginAccount(t, db.ctx, db.worker, account)
	if err := tx.QueryRow(db.ctx, `SELECT config_envelope FROM app.read_job_config_snapshot($1,'deleted-source-worker',$2)`, runningJob, lease).Scan(&envelope); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("cancel-requested lease holder could not read retained snapshot: %v", err)
	}
	_ = tx.Rollback(db.ctx)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'deleted-source-worker',$2,'cancelled')`, runningJob, lease) {
		t.Fatal("cooperative cancellation finish failed")
	}
	if snapshotCount(t, db, account, runningJob) != 0 {
		t.Fatal("terminal cooperative cancellation retained snapshot")
	}

	tx = beginAccount(t, db.ctx, db.api, account)
	newJob := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'source_connection_check',100)`, newJob, account); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		newJob, account, runningSource, []byte(`{"snapshot":true}`)); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err == nil {
		t.Fatal("new snapshot for deleted source unexpectedly committed")
	}

	queuedJob := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	queuedSource := snapshotSourceID(t, db, account, queuedJob)
	deleteSourceFixture(t, db, account, queuedSource)
	if requestCancellation(t, db, account, queuedJob) {
		t.Fatal("terminal queued cancellation unexpectedly changed twice")
	}
	if snapshotCount(t, db, account, queuedJob) != 0 {
		t.Fatal("queued cancellation retained deleted-source snapshot")
	}
	if status, _ := readStatus(t, db.ctx, db.api, account, queuedJob); status != "cancelled" {
		t.Fatalf("queued deleted-source job status=%s, want cancelled", status)
	}
}

func TestSourceCanonicalDisplayNameContractAndGrants(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	sourceID := uuid.Must(uuid.NewV7())
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, account); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Trail Source','trail source','health-auto-export-local',$3)`, sourceID, account, []byte(`{"encrypted":true}`)); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	tx = beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.sources SET display_name='Renamed Source' WHERE id=$1`, sourceID); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("one-sided source display-name update unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)

	tx = beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.sources
		SET display_name='Renamed Source',canonical_display_name='renamed source' WHERE id=$1`, sourceID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	tx = beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Different Spelling','renamed source','health-auto-export-local',$3)`, uuid.Must(uuid.NewV7()), account, []byte(`{"encrypted":true}`)); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("duplicate live canonical display name unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)

	tx = beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Empty Canonical','','health-auto-export-local',$3)`, uuid.Must(uuid.NewV7()), account, []byte(`{"encrypted":true}`)); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("empty canonical display name unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)

	var apiSelect, apiInsert, apiUpdate, workerSelect, workerUpdate bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		has_column_privilege('workouts_api','app.sources','canonical_display_name','SELECT'),
		has_column_privilege('workouts_api','app.sources','canonical_display_name','INSERT'),
		has_column_privilege('workouts_api','app.sources','canonical_display_name','UPDATE'),
		has_column_privilege('workouts_worker','app.sources','canonical_display_name','SELECT'),
		has_column_privilege('workouts_worker','app.sources','canonical_display_name','UPDATE')`).Scan(
		&apiSelect, &apiInsert, &apiUpdate, &workerSelect, &workerUpdate); err != nil {
		t.Fatal(err)
	}
	if !apiSelect || !apiInsert || !apiUpdate || workerSelect || workerUpdate {
		t.Fatalf("unexpected canonical display-name grants: api select=%t insert=%t update=%t worker select=%t update=%t",
			apiSelect, apiInsert, apiUpdate, workerSelect, workerUpdate)
	}
}

func TestNormalizedImportPersistenceClaimsRLSConstraintsAndPrivileges(t *testing.T) {
	db := openTestDatabases(t)
	var warningValidationCorrect bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		app.valid_workout_warnings('[{"code":"incomplete_metric","field":"distance","route_point":-1}]'::jsonb)
		AND NOT app.valid_workout_warnings('[{"code":"unexpected_unit","field":"speed_average","route_point":-1,"payload":"unsafe"}]'::jsonb)
		AND NOT app.valid_workout_warnings('[{"code":"provider_text","field":"distance","route_point":-1}]'::jsonb)
		AND NOT app.valid_workout_warnings((SELECT jsonb_agg(jsonb_build_object(
			'code','invalid_optional_route_value','field','route_speed','route_point',index))
			FROM generate_series(0,4096) index))`).Scan(&warningValidationCorrect); err != nil || !warningValidationCorrect {
		t.Fatalf("warning validator accepted unsafe or unbounded data: valid=%t err=%v", warningValidationCorrect, err)
	}
	account, foreignAccount := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	jobID := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, jobID)
	tx := beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET priority=32767 WHERE id=$1`, jobID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	lease := uuid.New()
	var claimedJob, claimedAccount uuid.UUID
	var claimedKind string
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job('import-worker',$1,interval '1 minute',7)`, lease).Scan(
		&claimedJob, &claimedAccount, &claimedKind); err != nil {
		t.Fatal(err)
	}
	if claimedJob != jobID || claimedAccount != account || claimedKind != "manual_ingest_source" {
		t.Fatalf("claimed job/account/kind=%s/%s/%s", claimedJob, claimedAccount, claimedKind)
	}

	fileID, typeID, workoutID, eventID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hash := bytes.Repeat([]byte{0x42}, 32)
	tx = beginAccount(t, db.ctx, db.worker, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,state)
		VALUES($1,$2,$3,$4,'exports/unfenced.json',1,'discovered')`, uuid.Must(uuid.NewV7()), account, sourceID, jobID); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("worker domain write succeeded without a transaction fence")
	}
	_ = tx.Rollback(db.ctx)
	tx = beginAccount(t, db.ctx, db.worker, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.ingest_write_capabilities
		(backend_pid,transaction_id,account_id,source_id,job_id,worker_id,lease_token)
		VALUES(pg_backend_pid(),txid_current(),$1,$2,$3,'import-worker',$4)`, account, sourceID, jobID, lease); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("worker forged an ingest write capability")
	}
	_ = tx.Rollback(db.ctx)

	tx = beginAccount(t, db.ctx, db.worker, account)
	var fencedSource uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'import-worker',$2)`, jobID, uuid.New()).Scan(&fencedSource); err != pgx.ErrNoRows {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("stale lease ingest fence error=%v, want no rows", err)
	}
	_ = tx.Rollback(db.ctx)

	tx = beginAccount(t, db.ctx, db.worker, account)
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'import-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil || fencedSource != sourceID {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("explicit import job fence source=%s err=%v", fencedSource, err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'exports/workouts.json',1234,$5,'succeeded',transaction_timestamp(),transaction_timestamp())`,
		fileID, account, sourceID, jobID, hash); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,'outdoor-run-identity','Outdoor Run')`, typeID, account); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,$3,'Long Provider Label')`, uuid.Must(uuid.NewV7()), account, strings.Repeat("x", 512)); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("parser-compatible workout type key was rejected: %v", err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workouts
		(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,
		 started_at,ended_at,provider_duration,is_indoor,location)
		VALUES($1,$2,$3,$4,$5,'provider-workout-1',$6,'Outdoor Run',
		 '2026-04-11T15:00:00Z','2026-04-11T15:36:35.6228786706924Z',2195.6228786706924,false,'Outdoor')`,
		workoutID, account, sourceID, fileID, typeID, hash); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_aggregates(account_id,workout_id,metric,value,unit,origin)
		VALUES($1,$2,'distance',5.125,'km','provider_direct')`, account, workoutID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	for sequence := range 2 {
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_route_points
			(account_id,workout_id,sequence,recorded_at,timestamp_offset_minutes,latitude,longitude,altitude,speed,course,
			 horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy)
			VALUES($1,$2,$3,'2026-04-11T15:01:00Z',-420,37.5,-122.2,12.5,2.5,180,3,4,0.5,2)`,
			account, workoutID, sequence); err != nil {
			_ = tx.Rollback(db.ctx)
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_import_events
		(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,warnings)
		VALUES($1,$2,$3,$4,$5,$6,'created',$7,$8::jsonb)`, eventID, account, sourceID, workoutID, fileID, jobID, hash,
		`[{"code":"unexpected_unit","field":"speed_average","route_point":-1},{"code":"invalid_optional_route_value","field":"route_speed","route_point":0}]`); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	var capabilityCount int
	if err := db.migration.QueryRow(db.ctx, `SELECT count(*) FROM app.ingest_write_capabilities`).Scan(&capabilityCount); err != nil {
		t.Fatal(err)
	}
	if capabilityCount != 0 {
		t.Fatalf("committed transaction retained %d ingest capabilities", capabilityCount)
	}

	stateFileID := uuid.Must(uuid.NewV7())
	tx = beginAccount(t, db.ctx, db.worker, account)
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'import-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,state)
		VALUES($1,$2,$3,$4,'exports/state.json',10,'discovered')`, stateFileID, account, sourceID, jobID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	tx = beginAccount(t, db.ctx, db.worker, account)
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'import-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.source_files
		SET state='succeeded',processing_started_at=transaction_timestamp(),processed_at=transaction_timestamp(),checksum_sha256=$2
		WHERE id=$1`, stateFileID, hash); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("discovered source file skipped the processing state")
	}
	_ = tx.Rollback(db.ctx)
	for _, transition := range []struct {
		state string
		query string
	}{
		{"processing", `UPDATE app.source_files SET state='processing',processing_started_at=transaction_timestamp(),checksum_sha256=$2 WHERE id=$1`},
		{"succeeded", `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=transaction_timestamp() WHERE id=$1`},
	} {
		tx = beginAccount(t, db.ctx, db.worker, account)
		if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'import-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil {
			_ = tx.Rollback(db.ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, transition.query, stateFileID, hash); err != nil {
			_ = tx.Rollback(db.ctx)
			t.Fatalf("source file transition to %s failed: %v", transition.state, err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}
	tx = beginAccount(t, db.ctx, db.worker, account)
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'import-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.source_files SET checksum_sha256=$2 WHERE id=$1`, stateFileID, bytes.Repeat([]byte{0x24}, 32)); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("terminal source file mutation unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)

	tx = beginAccount(t, db.ctx, db.api, account)
	var duration string
	var routePoints, warningCount int
	if err := tx.QueryRow(db.ctx, `SELECT w.provider_duration::text,
		(SELECT count(*) FROM app.workout_route_points p WHERE p.workout_id=w.id),
		(SELECT jsonb_array_length(e.warnings) FROM app.workout_import_events e WHERE e.workout_id=w.id)
		FROM app.workouts w WHERE w.id=$1`, workoutID).Scan(&duration, &routePoints, &warningCount); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	_ = tx.Rollback(db.ctx)
	if duration != "2195.6228786706924" || routePoints != 2 || warningCount != 2 {
		t.Fatalf("duration/duplicate-timestamp route points/warnings=%s/%d/%d", duration, routePoints, warningCount)
	}
	tx = beginAccount(t, db.ctx, db.api, foreignAccount)
	var foreignVisible int
	if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.workouts WHERE id=$1`, workoutID).Scan(&foreignVisible); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	_ = tx.Rollback(db.ctx)
	if foreignVisible != 0 {
		t.Fatal("foreign account saw normalized workout")
	}

	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `UPDATE app.workouts SET provider_id='changed' WHERE id=$1`, workoutID)
	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `UPDATE app.workout_import_events SET kind='updated' WHERE id=$1`, eventID)
	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,state)
		VALUES($1,$2,$3,$4,'exports/workouts.json',1,'discovered')`, uuid.Must(uuid.NewV7()), account, sourceID, jobID)
	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `INSERT INTO app.workouts
		(id,account_id,source_id,source_file_id,workout_type_id,provider_id,fallback_fingerprint_version,fallback_sha256,
		 content_sha256,provider_label,started_at,ended_at,provider_duration)
		VALUES($1,$2,$3,$4,$5,'both','health-auto-export-fallback-v2',$6,$6,'Outdoor Run',now(),now(),0)`,
		uuid.Must(uuid.NewV7()), account, sourceID, fileID, typeID, hash)
	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `INSERT INTO app.workouts
		(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
		VALUES($1,$2,$3,$4,$5,'provider-workout-2',$6,'Outdoor Run',now(),now(),0)`,
		uuid.Must(uuid.NewV7()), account, sourceID, fileID, typeID, hash[:31])
	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `INSERT INTO app.workout_types
		(id,account_id,type_key,provider_label) VALUES($1,$2,$3,'Oversized Type')`,
		uuid.Must(uuid.NewV7()), account, strings.Repeat("x", 513))
	expectFencedIngestMutationRejected(t, db, account, jobID, "import-worker", lease, `INSERT INTO app.workout_import_events
		(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,warnings)
		VALUES($1,$2,$3,$4,$5,$6,'matched_unchanged',$7,
		 '[{"code":"unexpected_unit","field":"speed_average","route_point":-1,"payload":"unsafe"}]'::jsonb)`,
		uuid.Must(uuid.NewV7()), account, sourceID, workoutID, fileID, jobID, hash)

	var apiInsert, workerWorkoutDelete, workerFileDelete, workerEventUpdate, workerEventDelete, workerRouteDelete bool
	var workerCapabilitySelect, workerCapabilityInsert bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		has_table_privilege('workouts_api','app.workouts','INSERT'),
		has_table_privilege('workouts_worker','app.workouts','DELETE'),
		has_table_privilege('workouts_worker','app.source_files','DELETE'),
		has_table_privilege('workouts_worker','app.workout_import_events','UPDATE'),
		has_table_privilege('workouts_worker','app.workout_import_events','DELETE'),
		has_table_privilege('workouts_worker','app.workout_route_points','DELETE'),
		has_table_privilege('workouts_worker','app.ingest_write_capabilities','SELECT'),
		has_table_privilege('workouts_worker','app.ingest_write_capabilities','INSERT')`).Scan(
		&apiInsert, &workerWorkoutDelete, &workerFileDelete, &workerEventUpdate, &workerEventDelete, &workerRouteDelete,
		&workerCapabilitySelect, &workerCapabilityInsert); err != nil {
		t.Fatal(err)
	}
	if apiInsert || workerWorkoutDelete || workerFileDelete || workerEventUpdate || workerEventDelete || !workerRouteDelete || workerCapabilitySelect || workerCapabilityInsert {
		t.Fatalf("unsafe ingest grants: apiInsert=%t workoutDelete=%t fileDelete=%t eventUpdate=%t eventDelete=%t routeDelete=%t capSelect=%t capInsert=%t",
			apiInsert, workerWorkoutDelete, workerFileDelete, workerEventUpdate, workerEventDelete, workerRouteDelete,
			workerCapabilitySelect, workerCapabilityInsert)
	}
	var generalizedOwner, compatibilityOwner, internalOwner, fenceOwner, cleanupOwner string
	var ownerBypass, ownerLogin bool
	if err := db.migration.QueryRow(db.ctx, `SELECT
		(SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid='app.claim_next_worker_job(text,uuid,interval)'::regprocedure),
		(SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid='app.claim_next_source_connection_check(text,uuid,interval)'::regprocedure),
		(SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid='app.claim_next_worker_job_internal(text,uuid,interval,boolean)'::regprocedure),
		(SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid='app.fence_ingest_job(uuid,text,uuid)'::regprocedure),
		(SELECT pg_get_userbyid(proowner) FROM pg_proc WHERE oid='app.clear_ingest_write_capability()'::regprocedure),
		rolbypassrls,rolcanlogin FROM pg_roles WHERE rolname='workouts_security_owner'`).Scan(
		&generalizedOwner, &compatibilityOwner, &internalOwner, &fenceOwner, &cleanupOwner, &ownerBypass, &ownerLogin); err != nil {
		t.Fatal(err)
	}
	if generalizedOwner != "workouts_security_owner" || compatibilityOwner != "workouts_security_owner" || internalOwner != "workouts_security_owner" || fenceOwner != "workouts_security_owner" || cleanupOwner != "workouts_security_owner" || ownerBypass || ownerLogin {
		t.Fatalf("unsafe function ownership: generalized=%s compatibility=%s internal=%s fence=%s cleanup=%s bypass=%t login=%t",
			generalizedOwner, compatibilityOwner, internalOwner, fenceOwner, cleanupOwner, ownerBypass, ownerLogin)
	}
	var workoutDeleteAction string
	if err := db.migration.QueryRow(db.ctx, `SELECT confdeltype::text FROM pg_constraint
		WHERE conrelid='app.workout_import_events'::regclass
		  AND confrelid='app.workouts'::regclass`).Scan(&workoutDeleteAction); err != nil {
		t.Fatal(err)
	}
	if workoutDeleteAction != "r" {
		t.Fatalf("import-event workout delete action=%q, want restrict", workoutDeleteAction)
	}

	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'import-worker',$2,'succeeded')`, jobID, lease) {
		t.Fatal("import job finish failed")
	}
	deleteSourceFixture(t, db, account, sourceID)
	tx = beginAccount(t, db.ctx, db.api, account)
	var retained bool
	if err := tx.QueryRow(db.ctx, `SELECT EXISTS(SELECT 1 FROM app.workouts WHERE id=$1)`, workoutID).Scan(&retained); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	_ = tx.Rollback(db.ctx)
	if !retained {
		t.Fatal("source tombstone removed workout provenance")
	}

	recoveryAccount := uuid.Must(uuid.NewV7())
	recoveryParent := insertQueued(t, db.ctx, db.worker, recoveryAccount, uuid.Nil, "manual_ingest")
	recoveryJob := insertQueued(t, db.ctx, db.api, recoveryAccount, recoveryParent, "manual_ingest_source")
	tx = beginAccount(t, db.ctx, db.migration, recoveryAccount)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET priority=32767 WHERE id=$1`, recoveryJob); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if !callBool(t, db.ctx, db.worker, recoveryAccount, `SELECT app.claim_job($1,'expired-worker',$2,interval '1 millisecond')`, recoveryJob, uuid.New()) {
		t.Fatal("could not prepare expired generalized claim")
	}
	time.Sleep(5 * time.Millisecond)
	recoveryLease := uuid.New()
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job('recovery-worker',$1,interval '1 minute',7)`, recoveryLease).Scan(
		&claimedJob, &claimedAccount, &claimedKind); err != nil {
		t.Fatal(err)
	}
	if claimedJob != recoveryJob || claimedAccount != recoveryAccount || claimedKind != "manual_ingest_source" {
		t.Fatalf("recovered claim job/account/kind=%s/%s/%s", claimedJob, claimedAccount, claimedKind)
	}
	if !callBool(t, db.ctx, db.worker, recoveryAccount, `SELECT app.finish_job($1,'recovery-worker',$2,'succeeded')`, recoveryJob, recoveryLease) {
		t.Fatal("recovered generalized job finish failed")
	}
}

func TestScheduledIngestClaimFenceCommitAndKindPairing(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "scheduled_ingest")
	jobID := insertQueued(t, db.ctx, db.api, account, parent, "scheduled_ingest_source")
	sourceID := snapshotSourceID(t, db, account, jobID)
	tx := beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET priority=32767 WHERE id=$1`, jobID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	lease := uuid.New()
	var claimedJob, claimedAccount uuid.UUID
	var claimedKind string
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job('scheduled-worker',$1,interval '1 minute',7)`, lease).Scan(
		&claimedJob, &claimedAccount, &claimedKind); err != nil {
		t.Fatal(err)
	}
	if claimedJob != jobID || claimedAccount != account || claimedKind != "scheduled_ingest_source" {
		t.Fatalf("scheduled claim=%s/%s/%s", claimedJob, claimedAccount, claimedKind)
	}

	fileID := uuid.Must(uuid.NewV7())
	tx = beginAccount(t, db.ctx, db.worker, account)
	var fencedSource uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'scheduled-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil || fencedSource != sourceID {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("scheduled fence source=%s err=%v", fencedSource, err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,state)
		VALUES($1,$2,$3,$4,'scheduled.json',1,'discovered')`, fileID, account, sourceID, jobID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatalf("scheduled deferred capability validation failed: %v", err)
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'scheduled-worker',$2,'succeeded')`, jobID, lease) {
		t.Fatal("scheduled child finish failed")
	}
	if child, _ := readStatus(t, db.ctx, db.worker, account, jobID); child != "succeeded" {
		t.Fatalf("scheduled child status=%s", child)
	}
	if parentStatus, _ := readStatus(t, db.ctx, db.worker, account, parent); parentStatus != "succeeded" {
		t.Fatalf("scheduled parent status=%s", parentStatus)
	}

	wrongParent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	tx = beginAccount(t, db.ctx, db.api, account)
	wrongPairParameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`,
		strings.ToUpper(strings.ReplaceAll(sourceID.String(), "-", "")))
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
		VALUES($1,$2,$3,'scheduled_ingest_source',80,$4)`, uuid.Must(uuid.NewV7()), wrongParent, account, wrongPairParameters); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("scheduled child with manual parent was accepted")
	}
	_ = tx.Rollback(db.ctx)
	if !requestCancellation(t, db, account, wrongParent) {
		t.Fatal("wrong-parent fixture cleanup failed")
	}

	wrongKind := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	wrongLease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'wrong-kind-worker',$2,interval '1 minute')`, wrongKind, wrongLease) {
		t.Fatal("wrong-kind fixture claim failed")
	}
	tx = beginAccount(t, db.ctx, db.worker, account)
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'wrong-kind-worker',$2)`, wrongKind, wrongLease).Scan(&fencedSource); !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("non-ingest job fence error=%v, want no rows", err)
	}
	_ = tx.Rollback(db.ctx)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'wrong-kind-worker',$2,'cancelled')`, wrongKind, wrongLease) {
		t.Fatal("wrong-kind fixture cleanup failed")
	}
}

func TestWorkerClaimSkipsParentLockedForCancellation(t *testing.T) {
	db := openTestDatabases(t)
	lockedAccount, availableAccount := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	lockedParent := insertQueued(t, db.ctx, db.worker, lockedAccount, uuid.Nil, "manual_ingest")
	lockedChild := insertQueued(t, db.ctx, db.api, lockedAccount, lockedParent, "manual_ingest_source")
	availableParent := insertQueued(t, db.ctx, db.worker, availableAccount, uuid.Nil, "manual_ingest")
	availableChild := insertQueued(t, db.ctx, db.api, availableAccount, availableParent, "manual_ingest_source")
	for _, fixture := range []struct {
		account  uuid.UUID
		job      uuid.UUID
		priority int
	}{{lockedAccount, lockedChild, 32767}, {availableAccount, availableChild, 32766}} {
		tx := beginAccount(t, db.ctx, db.migration, fixture.account)
		if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET priority=$2 WHERE id=$1`, fixture.job, fixture.priority); err != nil {
			_ = tx.Rollback(db.ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}

	cancellation := beginAccount(t, db.ctx, db.migration, lockedAccount)
	if _, err := cancellation.Exec(db.ctx, `SELECT true FROM app.jobs WHERE id=$1 FOR UPDATE`, lockedParent); err != nil {
		_ = cancellation.Rollback(db.ctx)
		t.Fatal(err)
	}
	lease := uuid.New()
	var jobID, accountID uuid.UUID
	var kind string
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job('skip-locked-worker',$1,interval '1 minute',7)`, lease).Scan(&jobID, &accountID, &kind); err != nil {
		_ = cancellation.Rollback(db.ctx)
		t.Fatal(err)
	}
	if jobID != availableChild || accountID != availableAccount || kind != "manual_ingest_source" {
		_ = cancellation.Rollback(db.ctx)
		t.Fatalf("claim did not skip locked parent: job/account/kind=%s/%s/%s", jobID, accountID, kind)
	}
	if err := cancellation.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	if !callBool(t, db.ctx, db.worker, availableAccount, `SELECT app.finish_job($1,'skip-locked-worker',$2,'succeeded')`, availableChild, lease) {
		t.Fatal("could not finish claim selected after locked parent")
	}
	if !requestCancellation(t, db, lockedAccount, lockedParent) {
		t.Fatal("locked parent could not be cancelled after concurrent claim")
	}

	connectionAccount := uuid.Must(uuid.NewV7())
	connectionJob := insertQueued(t, db.ctx, db.api, connectionAccount, uuid.Nil, "source_connection_check")
	tx := beginAccount(t, db.ctx, db.migration, connectionAccount)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET priority=32767 WHERE id=$1`, connectionJob); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	compatibilityLease := uuid.New()
	if err := db.worker.QueryRow(db.ctx, `SELECT job_id,account_id
		FROM app.claim_next_source_connection_check('compatibility-worker',$1,interval '1 minute')`, compatibilityLease).Scan(&jobID, &accountID); err != nil {
		t.Fatal(err)
	}
	if jobID != connectionJob || accountID != connectionAccount {
		t.Fatalf("cross-account compatibility claim=%s/%s, want %s/%s", jobID, accountID, connectionJob, connectionAccount)
	}
	if !callBool(t, db.ctx, db.worker, connectionAccount, `SELECT app.finish_job($1,'compatibility-worker',$2,'succeeded')`, connectionJob, compatibilityLease) {
		t.Fatal("compatibility claim finish failed")
	}
}

func TestDeferredIngestFenceRollsBackCancelledCommit(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	jobID := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, jobID)
	lease, fileID := uuid.New(), uuid.Must(uuid.NewV7())
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'commit-fence-worker',$2,interval '1 minute')`, jobID, lease) {
		t.Fatal("could not claim deferred-fence fixture")
	}
	tx := beginAccount(t, db.ctx, db.migration, account)
	var fencedSource uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'commit-fence-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,state)
		VALUES($1,$2,$3,$4,$5,1,'discovered')`, fileID, account, sourceID, jobID, "commit-fence-"+fileID.String()+".json"); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	var cancelled bool
	if err := tx.QueryRow(db.ctx, `SELECT app.request_job_cancellation($1,$2)`, jobID, uuid.Must(uuid.NewV7())).Scan(&cancelled); err != nil || !cancelled {
		_ = tx.Rollback(db.ctx)
		t.Fatalf("could not invalidate fenced transaction with cancellation: cancelled=%t err=%v", cancelled, err)
	}
	if err := tx.Commit(db.ctx); err == nil {
		t.Fatal("cancelled deferred fence committed domain writes")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'commit-fence-worker',$2,'succeeded')`, jobID, lease) {
		t.Fatal("cancellation rollback did not restore the running lease")
	}
}

func TestFencedCommitSurvivesLeaseExpiryAndBlocksRecovery(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	jobID := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, jobID)
	lease, fileID := uuid.New(), uuid.Must(uuid.NewV7())
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'slow-fenced-worker',$2,interval '500 milliseconds')`, jobID, lease) {
		t.Fatal("could not claim slow fenced fixture")
	}
	tx := beginAccount(t, db.ctx, db.worker, account)
	var fencedSource uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'slow-fenced-worker',$2)`, jobID, lease).Scan(&fencedSource); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files
		(id,account_id,source_id,job_id,relative_name,size_bytes,state)
		VALUES($1,$2,$3,$4,$5,1,'discovered')`, fileID, account, sourceID, jobID, "slow-fence-"+fileID.String()+".json"); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)

	type recoveryResult struct {
		recovered bool
		err       error
	}
	recovered := make(chan recoveryResult, 1)
	go func() {
		recoveryTx, err := db.worker.Begin(db.ctx)
		if err != nil {
			recovered <- recoveryResult{err: err}
			return
		}
		defer recoveryTx.Rollback(db.ctx)
		if _, err = recoveryTx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
			recovered <- recoveryResult{err: err}
			return
		}
		var result bool
		err = recoveryTx.QueryRow(db.ctx, `SELECT app.recover_expired_job($1)`, jobID).Scan(&result)
		if err == nil {
			err = recoveryTx.Commit(db.ctx)
		}
		recovered <- recoveryResult{recovered: result, err: err}
	}()
	select {
	case result := <-recovered:
		_ = tx.Rollback(db.ctx)
		t.Fatalf("recovery passed held fence before commit: recovered=%t err=%v", result.recovered, result.err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatalf("row-lock-fenced commit failed only because lease expired: %v", err)
	}
	select {
	case result := <-recovered:
		if result.err != nil || !result.recovered {
			t.Fatalf("recovery after fence release: recovered=%t err=%v", result.recovered, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovery remained blocked after fenced commit")
	}

	oldWorker := beginAccount(t, db.ctx, db.worker, account)
	if err := oldWorker.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'slow-fenced-worker',$2)`, jobID, lease).Scan(&fencedSource); err != pgx.ErrNoRows {
		_ = oldWorker.Rollback(db.ctx)
		t.Fatalf("old worker began a new fence after recovery: err=%v", err)
	}
	_ = oldWorker.Rollback(db.ctx)
	check := beginAccount(t, db.ctx, db.migration, account)
	var retained bool
	if err := check.QueryRow(db.ctx, `SELECT EXISTS(SELECT 1 FROM app.source_files WHERE id=$1)`, fileID).Scan(&retained); err != nil {
		_ = check.Rollback(db.ctx)
		t.Fatal(err)
	}
	_ = check.Rollback(db.ctx)
	if !retained {
		t.Fatal("successful slow fenced commit lost domain writes")
	}
	if !requestCancellation(t, db, account, parent) {
		t.Fatal("could not cancel recovered slow-fence fixture")
	}
}

func TestParentCancellationAndChildFinishDoNotDeadlock(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'ordered-finish-worker',$2,interval '1 minute')`, child, lease) {
		t.Fatal("could not claim ordered finish fixture")
	}
	type transitionResult struct {
		changed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan transitionResult, 2)
	run := func(pool *pgxpool.Pool, query string, args ...any) {
		tx, err := pool.Begin(db.ctx)
		if err != nil {
			results <- transitionResult{err: err}
			return
		}
		defer tx.Rollback(db.ctx)
		if _, err = tx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
			results <- transitionResult{err: err}
			return
		}
		if _, err = tx.Exec(db.ctx, `SET LOCAL lock_timeout='1s'`); err != nil {
			results <- transitionResult{err: err}
			return
		}
		<-start
		var changed bool
		err = tx.QueryRow(db.ctx, query, args...).Scan(&changed)
		if err == nil {
			err = tx.Commit(db.ctx)
		}
		results <- transitionResult{changed: changed, err: err}
	}
	go run(db.worker, `SELECT app.finish_job($1,'ordered-finish-worker',$2,'succeeded')`, child, lease)
	requester := accountRequester(t, db, account)
	go run(db.api, `SELECT app.request_owned_job_cancellation($1,$2)`, parent, requester)
	close(start)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("ordered finish/cancellation transition failed: changed=%t err=%v", result.changed, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("parent cancellation and child finish deadlocked")
		}
	}
}

func TestSourceDeletionAndChildCancellationDoNotInvertLocks(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, child)
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'source-delete-worker',$2,interval '1 minute')`, child, lease) {
		t.Fatal("could not claim source-delete cancellation fixture")
	}
	deletion := beginAccount(t, db.ctx, db.migration, account)
	if _, err := deletion.Exec(db.ctx, `SELECT true FROM app.sources WHERE id=$1 FOR UPDATE`, sourceID); err != nil {
		_ = deletion.Rollback(db.ctx)
		t.Fatal(err)
	}
	type cancellationResult struct {
		cancelled bool
		err       error
	}
	result := make(chan cancellationResult, 1)
	requester := accountRequester(t, db, account)
	go func() {
		tx, err := db.api.Begin(db.ctx)
		if err != nil {
			result <- cancellationResult{err: err}
			return
		}
		defer tx.Rollback(db.ctx)
		if _, err = tx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
			result <- cancellationResult{err: err}
			return
		}
		var cancelled bool
		err = tx.QueryRow(db.ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, child, requester).Scan(&cancelled)
		if err == nil {
			err = tx.Commit(db.ctx)
		}
		result <- cancellationResult{cancelled: cancelled, err: err}
	}()
	select {
	case got := <-result:
		_ = deletion.Rollback(db.ctx)
		t.Fatalf("child cancellation bypassed held source lock: cancelled=%t err=%v", got.cancelled, got.err)
	case <-time.After(30 * time.Millisecond):
	}
	var cancelled bool
	if err := deletion.QueryRow(db.ctx, `SELECT app.request_job_cancellation($1,$2)`, child, uuid.Must(uuid.NewV7())).Scan(&cancelled); err != nil || !cancelled {
		_ = deletion.Rollback(db.ctx)
		t.Fatalf("source-first deletion cancellation failed: cancelled=%t err=%v", cancelled, err)
	}
	if _, err := deletion.Exec(db.ctx, `UPDATE app.sources
		SET auto_sync_enabled=false,config_envelope=NULL,deleted_at=transaction_timestamp() WHERE id=$1`, sourceID); err != nil {
		_ = deletion.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := deletion.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || !got.cancelled {
			t.Fatalf("concurrent child cancellation after source deletion: cancelled=%t err=%v", got.cancelled, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child cancellation remained blocked after source deletion commit")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'source-delete-worker',$2,'cancelled')`, child, lease) {
		t.Fatal("could not finish source-deleted cancelled child")
	}
}

func TestLeaseRecoveryAndTerminalImmutability(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1, 'worker-a', $2, interval '1 millisecond')`, jobID, uuid.New()) {
		t.Fatal("claim failed")
	}
	time.Sleep(5 * time.Millisecond)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.recover_expired_job($1)`, jobID) {
		t.Fatal("lease recovery failed")
	}
	status, attempt := readStatus(t, db.ctx, db.worker, account, jobID)
	if status != "queued" || attempt != 1 {
		t.Fatalf("recovery status=%s attempt=%d", status, attempt)
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1, 'worker-b', $2, interval '1 minute')`, jobID, lease) ||
		!callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1, 'worker-b', $2, 'succeeded')`, jobID, lease) {
		t.Fatal("reclaimed execution failed")
	}
	tx := beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET progress_current = 1 WHERE id = $1`, jobID); err == nil {
		t.Fatal("terminal row mutation unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)
}

func TestIllegalStateMutationsAndCancellationRecovery(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	expectOwnerMutationRejected(t, db, account, `UPDATE app.jobs SET attempt = attempt + 1 WHERE id = $1`, jobID)
	expectOwnerMutationRejected(t, db, account, `UPDATE app.jobs SET lease_token = $2 WHERE id = $1`, jobID, uuid.New())
	expectOwnerMutationRejected(t, db, account, `UPDATE app.jobs SET status = 'cancelled', terminal_at = now() WHERE id = $1`, jobID)
	expectOwnerMutationRejected(t, db, account, `INSERT INTO app.jobs
		(id, account_id, kind, priority, status, attempt, worker_id, lease_token, claimed_at, heartbeat_at, lease_expires_at, started_at)
		VALUES ($1, $2, 'source_connection_check', 100, 'running', 1, 'worker', $3, now(), now(), now() + interval '1 minute', now())`,
		uuid.Must(uuid.NewV7()), account, uuid.New())
	expectOwnerMutationRejected(t, db, account, `INSERT INTO app.jobs
		(id, account_id, kind, priority, lease_token) VALUES ($1, $2, 'source_connection_check', 100, $3)`,
		uuid.Must(uuid.NewV7()), account, uuid.New())

	recoverID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1, 'worker-a', $2, interval '1 millisecond')`, recoverID, uuid.New()) {
		t.Fatal("claim failed")
	}
	if !requestCancellation(t, db, account, recoverID) {
		t.Fatal("cancellation request failed")
	}
	time.Sleep(5 * time.Millisecond)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.recover_expired_job($1)`, recoverID) {
		t.Fatal("cancellation-aware recovery failed")
	}
	if status, attempt := readStatus(t, db.ctx, db.worker, account, recoverID); status != "cancelled" || attempt != 1 {
		t.Fatalf("recovered cancellation status=%s attempt=%d", status, attempt)
	}
}

func TestParentDerivationAndControlledMutation(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	for _, test := range []struct {
		left, right, want string
	}{
		{"succeeded", "succeeded", "succeeded"},
		{"succeeded", "failed", "partially_succeeded"},
		{"succeeded", "cancelled", "partially_succeeded"},
		{"failed", "failed", "failed"},
		{"failed", "cancelled", "failed"},
		{"cancelled", "cancelled", "cancelled"},
	} {
		t.Run(test.left+"_"+test.right, func(t *testing.T) {
			parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
			left := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
			right := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
			finishChild(t, db, account, left, test.left)
			intermediate, _ := readStatus(t, db.ctx, db.worker, account, parent)
			if intermediate != "running" {
				t.Fatalf("intermediate parent status=%s", intermediate)
			}
			finishChild(t, db, account, right, test.right)
			got, attempt := readStatus(t, db.ctx, db.worker, account, parent)
			if got != test.want || attempt != 0 {
				t.Fatalf("parent status=%s attempt=%d, want %s/0", got, attempt, test.want)
			}
			tx := beginAccount(t, db.ctx, db.migration, account)
			var notificationCount int
			if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.notifications WHERE job_id=$1 AND type='ingest-terminal'`, parent).Scan(&notificationCount); err != nil {
				t.Fatal(err)
			}
			// Repeated derivation must not create a second terminal notification.
			if _, err := tx.Exec(db.ctx, `SELECT app.derive_parent_status($1)`, parent); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.notifications WHERE job_id=$1 AND type='ingest-terminal'`, parent).Scan(&notificationCount); err != nil || notificationCount != 1 {
				t.Fatalf("terminal notification count=%d err=%v", notificationCount, err)
			}
			_ = tx.Rollback(db.ctx)
		})
	}
	scheduledSuccess := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "scheduled_ingest")
	scheduledChild := insertQueued(t, db.ctx, db.api, account, scheduledSuccess, "scheduled_ingest_source")
	finishChild(t, db, account, scheduledChild, "succeeded")
	silentCheck := beginAccount(t, db.ctx, db.migration, account)
	var scheduledSuccessNotifications int
	if err := silentCheck.QueryRow(db.ctx, `SELECT count(*) FROM app.notifications WHERE job_id=$1 AND type='ingest-terminal'`, scheduledSuccess).Scan(&scheduledSuccessNotifications); err != nil {
		t.Fatal(err)
	}
	if scheduledSuccessNotifications != 0 {
		t.Fatalf("scheduled success notifications=%d", scheduledSuccessNotifications)
	}
	_ = silentCheck.Rollback(db.ctx)

	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "scheduled_ingest")
	insertQueued(t, db.ctx, db.api, account, parent, "scheduled_ingest_source")
	insertQueued(t, db.ctx, db.api, account, parent, "scheduled_ingest_source")
	if !requestCancellation(t, db, account, parent) {
		t.Fatal("parent cancellation failed")
	}
	if status, _ := readStatus(t, db.ctx, db.worker, account, parent); status != "cancelled" {
		t.Fatalf("cancelled parent status=%s", status)
	}
	scheduledCheck := beginAccount(t, db.ctx, db.api, account)
	var scheduledNotifications int
	if err := scheduledCheck.QueryRow(db.ctx, `SELECT count(*) FROM app.notifications WHERE job_id=$1`, parent).Scan(&scheduledNotifications); err != nil || scheduledNotifications != 0 {
		t.Fatalf("scheduled cancellation notification count=%d err=%v", scheduledNotifications, err)
	}
	_ = scheduledCheck.Rollback(db.ctx)

	mutableParent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	tx := beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET status = 'running' WHERE id = $1`, mutableParent); err == nil {
		t.Fatal("arbitrary parent transition unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)
}

func TestSourceStalenessRemindAndNewDataResolution(t *testing.T) {
	db := openTestDatabases(t)
	account, _, child, sourceID, lease := insertDurableRunningChild(t, db, "freshness-worker")
	invalidTimezone := beginAccount(t, db.ctx, db.migration, account)
	if _, err := invalidTimezone.Exec(db.ctx, `UPDATE app.sources SET status='connected',auto_sync_enabled=true WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := invalidTimezone.Exec(db.ctx, `INSERT INTO app.preferences(account_id,timezone) VALUES($1,'Invalid/Timezone')
		ON CONFLICT(account_id) DO UPDATE SET timezone=EXCLUDED.timezone`, account); err != nil {
		t.Fatal(err)
	}
	if err := invalidTimezone.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	recordSuccessfulObject(t, db, account, child, lease, "freshness-worker",
		"HealthAutoExport-2026-02-01.json", "2026-02-01", "initial-object", 0x70)
	asOf := time.Now().UTC().AddDate(0, 0, 4)
	tx := beginAccount(t, db.ctx, db.api, account)
	var stale int
	if err := tx.QueryRow(db.ctx, `SELECT app.evaluate_source_staleness($1,3,$2)`, sourceID, asOf).Scan(&stale); err != nil || stale != 1 {
		t.Fatalf("staleness result=%d err=%v", stale, err)
	}
	var notificationID uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT id FROM app.notifications WHERE source_id=$1 AND type='source-stale'`, sourceID).Scan(&notificationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	requester := accountRequester(t, db, account)
	dismiss := beginAccount(t, db.ctx, db.api, account)
	var dismissedID uuid.UUID
	var state string
	if err := dismiss.QueryRow(db.ctx, `SELECT notification_id,new_state FROM app.dismiss_owned_notification($1,$2)`, notificationID, requester).Scan(&dismissedID, &state); err != nil || state != "remind" {
		t.Fatalf("dismissed=%s state=%s err=%v", dismissedID, state, err)
	}
	if err := dismiss.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	recordSuccessfulObject(t, db, account, child, lease, "freshness-worker",
		"HealthAutoExport-2026-02-01.json", "2026-02-01", "changed-object", 0x71)
	check := beginAccount(t, db.ctx, db.api, account)
	var unresolved string
	var staleSince *time.Time
	if err := check.QueryRow(db.ctx, `SELECT notification.state,sync.stale_since FROM app.notifications notification
		JOIN app.source_sync_state sync ON sync.source_id=notification.source_id AND sync.account_id=notification.account_id
		WHERE notification.id=$1`, notificationID).Scan(&unresolved, &staleSince); err != nil || unresolved != "remind" || staleSince == nil {
		t.Fatalf("changed historical object state=%s staleSince=%v err=%v", unresolved, staleSince, err)
	}
	_ = check.Rollback(db.ctx)

	due := beginAccount(t, db.ctx, db.migration, account)
	if _, err := due.Exec(db.ctx, `UPDATE app.notifications SET remind_at=clock_timestamp()-interval '1 second' WHERE id=$1`, notificationID); err != nil {
		t.Fatal(err)
	}
	var evaluated int
	if err := due.QueryRow(db.ctx, `SELECT app.evaluate_source_staleness($1,3,$2)`, sourceID, asOf).Scan(&evaluated); err != nil || evaluated != 1 {
		t.Fatalf("due reminder evaluation=%d err=%v", evaluated, err)
	}
	var sameNotification uuid.UUID
	if err := due.QueryRow(db.ctx, `SELECT id FROM app.notifications WHERE source_id=$1 AND state='remind' AND remind_at<=clock_timestamp()`, sourceID).Scan(&sameNotification); err != nil || sameNotification != notificationID {
		t.Fatalf("due reminder id=%s err=%v", sameNotification, err)
	}
	if err := due.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	recordSuccessfulObject(t, db, account, child, lease, "freshness-worker",
		"HealthAutoExport-2026-01-01.json", "2026-01-01", "out-of-order-new-object", 0x72)
	resolvedCheck := beginAccount(t, db.ctx, db.api, account)
	var resolved string
	var lastExportDate time.Time
	var remindAt *time.Time
	if err := resolvedCheck.QueryRow(db.ctx, `SELECT notification.state,notification.remind_at,sync.last_new_export_date
		FROM app.notifications notification JOIN app.source_sync_state sync
		ON sync.source_id=notification.source_id AND sync.account_id=notification.account_id WHERE notification.id=$1`,
		notificationID).Scan(&resolved, &remindAt, &lastExportDate); err != nil || resolved != "resolved" || remindAt != nil || lastExportDate.Format(time.DateOnly) != "2026-02-01" {
		t.Fatalf("resolved=%s remindAt=%v lastExportDate=%s err=%v", resolved, remindAt, lastExportDate, err)
	}
	_ = resolvedCheck.Rollback(db.ctx)

	for _, state := range []struct {
		status string
		auto   bool
	}{{"connected", false}, {"checking-connection", true}, {"connection-failed", true}} {
		ineligible := beginAccount(t, db.ctx, db.migration, account)
		if _, err := ineligible.Exec(db.ctx, `UPDATE app.sources SET status=$1,auto_sync_enabled=$2 WHERE id=$3`, state.status, state.auto, sourceID); err != nil {
			t.Fatal(err)
		}
		if _, err := ineligible.Exec(db.ctx, `UPDATE app.notifications SET state='unresolved',resolved_at=NULL WHERE id=$1`, notificationID); err != nil {
			t.Fatal(err)
		}
		if err := ineligible.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		check := beginAccount(t, db.ctx, db.api, account)
		var evaluated int
		if err := check.QueryRow(db.ctx, `SELECT app.evaluate_source_staleness($1,3,$2)`, sourceID, asOf).Scan(&evaluated); err != nil {
			t.Fatal(err)
		}
		var notificationState string
		var staleSince *time.Time
		if err := check.QueryRow(db.ctx, `SELECT notification.state,sync.stale_since
			FROM app.notifications notification JOIN app.source_sync_state sync
			ON sync.account_id=notification.account_id AND sync.source_id=notification.source_id
			WHERE notification.id=$1`, notificationID).Scan(&notificationState, &staleSince); err != nil {
			t.Fatal(err)
		}
		if evaluated != 0 || notificationState != "resolved" || staleSince != nil {
			t.Fatalf("ineligible source %s/%t evaluated/state/stale=%d/%s/%v", state.status, state.auto, evaluated, notificationState, staleSince)
		}
		if err := check.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}
	reenabled := beginAccount(t, db.ctx, db.migration, account)
	if _, err := reenabled.Exec(db.ctx, `UPDATE app.sources SET status='connected',auto_sync_enabled=true WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := reenabled.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	resume := beginAccount(t, db.ctx, db.api, account)
	var resumed int
	if err := resume.QueryRow(db.ctx, `SELECT app.evaluate_source_staleness($1,3,$2)`, sourceID, asOf).Scan(&resumed); err != nil || resumed != 1 {
		t.Fatalf("re-enabled freshness baseline result=%d err=%v", resumed, err)
	}
	if err := resume.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	deleted := beginAccount(t, db.ctx, db.migration, account)
	if _, err := deleted.Exec(db.ctx, `UPDATE app.sources SET auto_sync_enabled=false,config_envelope=NULL,
		deleted_at=transaction_timestamp() WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := deleted.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	deletedCheck := beginAccount(t, db.ctx, db.api, account)
	var deletedEvaluated int
	if err := deletedCheck.QueryRow(db.ctx, `SELECT app.evaluate_source_staleness($1,3,$2)`, sourceID, asOf).Scan(&deletedEvaluated); err != nil {
		t.Fatal(err)
	}
	var deletedState string
	if err := deletedCheck.QueryRow(db.ctx, `SELECT state FROM app.notifications WHERE id=$1`, notificationID).Scan(&deletedState); err != nil {
		t.Fatal(err)
	}
	if deletedEvaluated != 0 || deletedState != "resolved" {
		t.Fatalf("deleted source evaluated/state=%d/%s", deletedEvaluated, deletedState)
	}
	_ = deletedCheck.Rollback(db.ctx)
}

func TestAppendOnlyRowsPermitOnlyParentCascades(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	if _, err := db.migration.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT (id) DO NOTHING`, account); err != nil {
		t.Fatal(err)
	}
	parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
	jobCascade := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
	setup := beginAccount(t, db.ctx, db.migration, account)
	sourceID := uuid.Must(uuid.NewV7())
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Cascade source','cascade source','health-auto-export-local',$3)`, sourceID, account, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'Historical source','health-auto-export-local')`, parent, account, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message)
		VALUES($1,$2,'info','ingest-started','Ingest started.')`, account, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.job_logs(account_id,job_id,severity,code,redacted_message)
		VALUES($1,$2,'info','ingest-started','Ingest started.')`, account, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'Historical source','health-auto-export-local')`, jobCascade, account, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message)
		VALUES($1,$2,'info','ingest-started','Ingest started.')`, account, jobCascade); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(db.ctx, `INSERT INTO app.job_logs(account_id,job_id,severity,code,redacted_message)
		VALUES($1,$2,'info','ingest-started','Ingest started.')`, account, jobCascade); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"job_source_contexts", "job_events", "job_logs"} {
		direct := beginAccount(t, db.ctx, db.migration, account)
		if _, err := direct.Exec(db.ctx, `DELETE FROM app.`+table+` WHERE job_id=$1`, parent); err == nil {
			t.Fatalf("direct delete from %s was accepted", table)
		}
		_ = direct.Rollback(db.ctx)
	}
	jobCleanup := beginAccount(t, db.ctx, db.migration, account)
	if _, err := jobCleanup.Exec(db.ctx, `DELETE FROM app.jobs WHERE id=$1`, jobCascade); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"job_source_contexts", "job_events", "job_logs"} {
		var remaining int
		if err := jobCleanup.QueryRow(db.ctx, `SELECT count(*) FROM app.`+table+` WHERE job_id=$1`, jobCascade).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("%s job cascade remaining=%d err=%v", table, remaining, err)
		}
	}
	if err := jobCleanup.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	cleanup := beginAccount(t, db.ctx, db.migration, account)
	if _, err := cleanup.Exec(db.ctx, `DELETE FROM app.jobs WHERE id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"job_source_contexts", "job_events", "job_logs"} {
		var remaining int
		if err := cleanup.QueryRow(db.ctx, `SELECT count(*) FROM app.`+table+` WHERE job_id=$1`, parent).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("%s job cascade remaining=%d err=%v", table, remaining, err)
		}
	}
	if err := cleanup.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticCapsDoNotOwnTerminalLease(t *testing.T) {
	db := openTestDatabases(t)
	account, _, child, _, lease := insertDurableRunningChild(t, db, "diagnostic-cap-worker")
	seed := beginAccount(t, db.ctx, db.migration, account)
	if _, err := seed.Exec(db.ctx, `INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message,fields)
		SELECT $1,$2,'info','ingest-progress','Ingest progress was recorded.','{}'::jsonb FROM generate_series(1,1000)`, account, child); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(db.ctx, `INSERT INTO app.job_logs(account_id,job_id,severity,code,redacted_message,fields)
		SELECT $1,$2,'debug','ingest-progress','Ingest progress was recorded.','{}'::jsonb FROM generate_series(1,2000)`, account, child); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_event($1,'diagnostic-cap-worker',$2,'ingest-progress','{}')`, child, lease) {
		t.Fatal("event recorder exceeded its cap")
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_log($1,'diagnostic-cap-worker',$2,'ingest-progress','{}')`, child, lease) {
		t.Fatal("log recorder exceeded its cap")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'diagnostic-cap-worker',$2,'succeeded')`, child, lease) {
		t.Fatal("diagnostic caps prevented authoritative job completion")
	}
}

func TestActiveCoalescingConstraint(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	key := make([]byte, 32)
	tx := beginAccount(t, db.ctx, db.api, account)
	insert := `INSERT INTO app.jobs (id, account_id, kind, priority, coalescing_version, coalescing_scope, coalescing_key) VALUES ($1, $2, 'workout_deletion', 100, 1, 'workout-deletion', $3)`
	if _, err := tx.Exec(db.ctx, insert, uuid.Must(uuid.NewV7()), account, key); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, insert, uuid.Must(uuid.NewV7()), account, key); err == nil {
		t.Fatal("duplicate active coalescing key unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)
}

func TestSchema7WorkerRuntimeClaimGate(t *testing.T) {
	db := openTestDatabases(t)
	if _, err := db.worker.Exec(db.ctx, `SELECT * FROM app.claim_next_worker_job('schema6-worker',$1,interval '1 minute')`, uuid.New()); err == nil {
		t.Fatal("schema-6 worker claim signature was not blocked")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT * FROM app.claim_next_worker_job('old-runtime',$1,interval '1 minute',6)`, uuid.New()); err == nil {
		t.Fatal("old runtime version was accepted by schema-7 claim")
	}
	tx, err := db.worker.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.ctx)
	rows, err := tx.Query(db.ctx, `SELECT * FROM app.claim_next_worker_job('schema7-worker',$1,interval '1 minute',7)`, uuid.New())
	if err != nil {
		t.Fatalf("schema-7 worker claim failed: %v", err)
	}
	rows.Close()
}

func TestIngestFileSlotPolicyLifecycle(t *testing.T) {
	db := openTestDatabases(t)
	if _, err := db.worker.Exec(db.ctx, `SELECT app.configure_ingest_file_slot_limits(17,17)`); err == nil {
		t.Fatal("database accepted slot limits above the hard bound")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT app.configure_ingest_file_slot_limits(3,2)`); err == nil {
		t.Fatal("database accepted incoherent slot limits")
	}
	if !callBoolNoAccount(t, db.ctx, db.worker, `SELECT app.configure_ingest_file_slot_limits(2,4)`) {
		t.Fatal("initial slot policy configuration failed")
	}
	type claimed struct {
		account, parent, job, lease uuid.UUID
		worker                      string
	}
	claims := make([]claimed, 0, 8)
	claim := func(account uuid.UUID, worker string) claimed {
		parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
		child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
		sourceID := snapshotSourceID(t, db, account, child)
		tx := beginAccount(t, db.ctx, db.api, account)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts
			(job_id,account_id,source_id,source_generation,display_name,source_type)
			VALUES($1,$2,$3,1,'Slot source','health-auto-export-local')`, child, account, sourceID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		lease := uuid.New()
		if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,$2,$3,interval '1 minute')`, child, worker, lease) {
			t.Fatal("could not claim slot fixture")
		}
		value := claimed{account, parent, child, lease, worker}
		claims = append(claims, value)
		return value
	}
	t.Cleanup(func() {
		cleanupTx, err := db.migration.Begin(context.Background())
		if err == nil {
			if _, err = cleanupTx.Exec(context.Background(), `SET LOCAL ROLE workouts_security_owner`); err == nil {
				jobIDs := make([]uuid.UUID, len(claims))
				for index, value := range claims {
					jobIDs[index] = value.job
				}
				_, err = cleanupTx.Exec(context.Background(), `DELETE FROM app.ingest_file_slots WHERE job_id=ANY($1)`, jobIDs)
			}
			if err == nil {
				err = cleanupTx.Commit(context.Background())
			} else {
				_ = cleanupTx.Rollback(context.Background())
			}
		}
		for _, value := range claims {
			_ = callBool(t, db.ctx, db.worker, value.account, `SELECT app.finish_job($1,$2,$3,'cancelled')`,
				value.job, value.worker, value.lease)
			_ = requestCancellation(t, db, value.account, value.parent)
		}
		_, _ = db.worker.Exec(context.Background(), `SELECT app.configure_ingest_file_slot_limits(2,4)`)
	})
	acquire := func(value claimed) *uuid.UUID {
		tx := beginAccount(t, db.ctx, db.worker, value.account)
		defer tx.Rollback(db.ctx)
		var token *uuid.UUID
		if err := tx.QueryRow(db.ctx, `SELECT app.acquire_ingest_file_slot($1,$2,$3)`, value.job, value.worker, value.lease).Scan(&token); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		return token
	}
	release := func(value claimed, token uuid.UUID) {
		if !callBool(t, db.ctx, db.worker, value.account, `SELECT app.release_ingest_file_slot($1,$2,$3,$4)`,
			value.job, value.worker, value.lease, token) {
			t.Fatal("slot release failed")
		}
	}

	accountA, accountB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	a := []claimed{claim(accountA, "account-a-1"), claim(accountA, "account-a-2"), claim(accountA, "account-a-3")}
	b := []claimed{claim(accountB, "account-b-1"), claim(accountB, "account-b-2"), claim(accountB, "account-b-3")}
	tokens := make([]uuid.UUID, 0, 4)
	for _, value := range []claimed{a[0], a[1], b[0], b[1]} {
		token := acquire(value)
		if token == nil {
			t.Fatal("slot below configured account/global limits was denied")
		}
		tokens = append(tokens, *token)
	}
	if acquire(a[2]) != nil || acquire(b[2]) != nil {
		t.Fatal("account or global slot limit was exceeded")
	}
	if callBoolNoAccount(t, db.ctx, db.worker, `SELECT app.configure_ingest_file_slot_limits(1,2)`) {
		t.Fatal("active slots allowed a non-idempotent policy update")
	}
	if !callBoolNoAccount(t, db.ctx, db.worker, `SELECT app.configure_ingest_file_slot_limits(2,4)`) {
		t.Fatal("idempotent active slot policy configuration failed")
	}

	requester := accountRequester(t, db, accountA)
	if !callBool(t, db.ctx, db.api, accountA, `SELECT app.request_owned_job_cancellation($1,$2)`, a[0].job, requester) {
		t.Fatal("slot cancellation fixture failed")
	}
	if acquire(a[2]) != nil {
		t.Fatal("cancellation intent prematurely released an in-flight slot")
	}
	if !callBool(t, db.ctx, db.worker, accountA, `SELECT app.heartbeat_job($1,$2,$3,interval '1 minute')`,
		a[1].job, a[1].worker, a[1].lease) {
		t.Fatal("heartbeat failed while another slot was active")
	}

	if !callBool(t, db.ctx, db.worker, accountA, `SELECT app.finish_job($1,$2,$3,'cancelled')`, a[0].job, a[0].worker, a[0].lease) {
		t.Fatal("cancelled slot owner did not finish")
	}
	replacement := acquire(a[2])
	if replacement == nil {
		t.Fatal("terminal slot ownership was not reclaimed")
	}
	release(a[2], *replacement)
	for index, value := range []claimed{a[1], b[0], b[1]} {
		release(value, tokens[index+1])
	}
	if !callBoolNoAccount(t, db.ctx, db.worker, `SELECT app.configure_ingest_file_slot_limits(1,1)`) {
		t.Fatal("could not configure stale-ownership slot policy")
	}
	expiring := claim(uuid.Must(uuid.NewV7()), "expiring-owner")
	other := claim(uuid.Must(uuid.NewV7()), "other-owner")
	expiringToken := acquire(expiring)
	if expiringToken == nil {
		t.Fatal("expiring owner did not acquire its slot")
	}
	if !callBool(t, db.ctx, db.worker, expiring.account, `SELECT app.heartbeat_job($1,$2,$3,interval '5 milliseconds')`,
		expiring.job, expiring.worker, expiring.lease) {
		t.Fatal("could not shorten stale slot lease")
	}
	time.Sleep(10 * time.Millisecond)
	if acquire(other) != nil {
		t.Fatal("mere lease expiry prematurely reclaimed an in-flight slot")
	}
	recovered := false
	deadline := time.Now().Add(time.Second)
	for !recovered && time.Now().Before(deadline) {
		recovered = callBool(t, db.ctx, db.worker, expiring.account, `SELECT app.recover_expired_job($1)`, expiring.job)
		if !recovered {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !recovered {
		t.Fatal("expired slot job was not recovered within the deadline")
	}
	otherToken := acquire(other)
	if otherToken == nil {
		t.Fatal("recovered ownership did not make the old slot stale")
	}
	release(other, *otherToken)
	if !callBoolNoAccount(t, db.ctx, db.worker, `SELECT app.configure_ingest_file_slot_limits(2,4)`) {
		t.Fatal("could not restore default slot policy")
	}
}

func TestConcurrentIngestFileSlotLimitsAcrossPools(t *testing.T) {
	db := openTestDatabases(t)
	workerURL := os.Getenv("WORKER_DATABASE_URL")
	if !callBoolNoAccount(t, db.ctx, db.worker, `SELECT app.configure_ingest_file_slot_limits(2,4)`) {
		t.Fatal("could not configure concurrent slot policy")
	}
	type claim struct {
		account, parent, job, lease uuid.UUID
		worker                      string
	}
	accounts := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	claims := make([]claim, 0, 6)
	for accountIndex, account := range accounts {
		for childIndex := 0; childIndex < 3; childIndex++ {
			parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
			child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
			sourceID := snapshotSourceID(t, db, account, child)
			tx := beginAccount(t, db.ctx, db.api, account)
			if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts
				(job_id,account_id,source_id,source_generation,display_name,source_type)
				VALUES($1,$2,$3,1,'Concurrent slot source','health-auto-export-local')`, child, account, sourceID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(db.ctx); err != nil {
				t.Fatal(err)
			}
			worker := fmt.Sprintf("concurrent-slot-%d-%d", accountIndex, childIndex)
			lease := uuid.New()
			if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,$2,$3,interval '1 minute')`, child, worker, lease) {
				t.Fatal("could not claim concurrent slot fixture")
			}
			claims = append(claims, claim{account: account, parent: parent, job: child, lease: lease, worker: worker})
		}
	}
	t.Cleanup(func() {
		for _, value := range claims {
			_ = callBool(t, context.Background(), db.worker, value.account,
				`SELECT app.finish_job($1,$2,$3,'cancelled')`, value.job, value.worker, value.lease)
			_ = requestCancellation(t, db, value.account, value.parent)
		}
	})
	type result struct {
		index int
		token *uuid.UUID
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, len(claims))
	var wg sync.WaitGroup
	for index, value := range claims {
		pool := openPool(t, db.ctx, workerURL, 1)
		wg.Add(1)
		go func(index int, value claim) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, err := pool.Begin(ctx)
			if err != nil {
				results <- result{index: index, err: err}
				return
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, value.account.String()); err == nil {
				var token *uuid.UUID
				err = tx.QueryRow(ctx, `SELECT app.acquire_ingest_file_slot($1,$2,$3)`, value.job, value.worker, value.lease).Scan(&token)
				if err == nil {
					err = tx.Commit(ctx)
				}
				results <- result{index: index, token: token, err: err}
				return
			}
			results <- result{index: index, err: err}
		}(index, value)
	}
	close(start)
	wg.Wait()
	close(results)
	perAccount := map[uuid.UUID]int{}
	acquired := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.token == nil {
			continue
		}
		value := claims[result.index]
		acquired++
		perAccount[value.account]++
		if !callBool(t, db.ctx, db.worker, value.account, `SELECT app.release_ingest_file_slot($1,$2,$3,$4)`,
			value.job, value.worker, value.lease, *result.token) {
			t.Fatal("could not release concurrent slot")
		}
	}
	if acquired != 4 || perAccount[accounts[0]] > 2 || perAccount[accounts[1]] > 2 {
		t.Fatalf("concurrent slots total/accounts=%d/%d/%d", acquired, perAccount[accounts[0]], perAccount[accounts[1]])
	}
}

func TestIngestFileManifestIsExactAndPrivate(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
	child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, child)
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'Manifest source','health-auto-export-local')`, child, account, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'manifest-worker',$2,interval '1 minute')`, child, lease) {
		t.Fatal("could not claim manifest fixture")
	}
	t.Cleanup(func() {
		_ = callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'manifest-worker',$2,'cancelled')`, child, lease)
		_ = requestCancellation(t, db, account, parent)
	})
	manifest := `[{
		"name":"HealthAutoExport-2026-01-01.json","export_date":"2026-01-01","size":10,
		"modified_sec":100,"modified_ns":1,"device":2,"inode":3,"ctime_sec":101,"ctime_ns":4,"action":"process"
	}]`
	callManifest := func(value string) string {
		tx := beginAccount(t, db.ctx, db.worker, account)
		defer tx.Rollback(db.ctx)
		var state string
		if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_file_manifest($1,'manifest-worker',$2,$3::jsonb)`, child, lease, value).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if state := callManifest(manifest); state != "created" {
		t.Fatalf("initial manifest state=%q", state)
	}
	if state := callManifest(manifest); state != "matched" {
		t.Fatalf("identical recovery manifest state=%q", state)
	}
	changed := strings.Replace(manifest, `"inode":3`, `"inode":30`, 1)
	if state := callManifest(changed); state != "mismatch" {
		t.Fatalf("equal-count identity replacement state=%q", state)
	}
	if state := callManifest(`[]`); state != "mismatch" {
		t.Fatalf("disappearing candidate state=%q", state)
	}
	privacy := beginAccount(t, db.ctx, db.api, account)
	if _, err := privacy.Exec(db.ctx, `SAVEPOINT candidate_privacy`); err != nil {
		t.Fatal(err)
	}
	_, privacyErr := privacy.Exec(db.ctx, `SELECT * FROM app.job_file_candidates`)
	var postgres *pgconn.PgError
	if !errors.As(privacyErr, &postgres) || postgres.Code != "42501" {
		_ = privacy.Rollback(db.ctx)
		t.Fatalf("API candidate privacy error=%v, want SQLSTATE 42501", privacyErr)
	}
	if _, err := privacy.Exec(db.ctx, `ROLLBACK TO SAVEPOINT candidate_privacy`); err != nil {
		_ = privacy.Rollback(db.ctx)
		t.Fatal(err)
	}
	if err := privacy.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}

	inspection, err := db.migration.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Rollback(db.ctx)
	if _, err := inspection.Exec(db.ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		t.Fatal(err)
	}
	if _, err := inspection.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
		t.Fatal(err)
	}
	var candidateCount, storedRows int
	var storedInode string
	var storedAction string
	if err := inspection.QueryRow(db.ctx, `SELECT candidate_set.candidate_count,count(candidate.relative_name),
		max(candidate.inode::text),max(candidate.action)
		FROM app.job_file_candidate_sets candidate_set
		LEFT JOIN app.job_file_candidates candidate ON candidate.job_id=candidate_set.job_id
		WHERE candidate_set.job_id=$1 GROUP BY candidate_set.candidate_count`, child).Scan(
		&candidateCount, &storedRows, &storedInode, &storedAction); err != nil {
		t.Fatal(err)
	}
	if candidateCount != 1 || storedRows != 1 || storedInode != "3" || storedAction != "process" {
		t.Fatalf("stored manifest count/rows/inode/action=%d/%d/%s/%s", candidateCount, storedRows, storedInode, storedAction)
	}
	if err := inspection.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1,'manifest-worker',$2,'succeeded')`, child, lease) {
		t.Fatal("manifest fixture cleanup failed")
	}
}

func TestDurableProgressFencingAggregationAndAppendOnlyRows(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
	child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, child)
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'Captured source','health-auto-export-local')`, child, account, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$2),($3,$2)`, parent, account, child); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	legacy := beginAccount(t, db.ctx, db.api, account)
	if _, err := legacy.Exec(db.ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority)
		VALUES($1,$2,$3,'manual_ingest_source',80)`, uuid.Must(uuid.NewV7()), parent, account); err == nil {
		_ = legacy.Rollback(db.ctx)
		t.Fatal("schema-6 API child parameters were accepted after schema 7")
	}
	_ = legacy.Rollback(db.ctx)
	legacy = beginAccount(t, db.ctx, db.api, account)
	coalescedParameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`,
		strings.ToUpper(strings.ReplaceAll(sourceID.String(), "-", "")))
	if _, err := legacy.Exec(db.ctx, `INSERT INTO app.jobs
		(id,parent_job_id,account_id,kind,priority,coalescing_version,coalescing_scope,coalescing_key,parameters)
		VALUES($1,$2,$3,'manual_ingest_source',80,1,'legacy-child',$4,$5)`, uuid.Must(uuid.NewV7()), parent, account,
		make([]byte, 32), coalescedParameters); err == nil {
		_ = legacy.Rollback(db.ctx)
		t.Fatal("schema-6 child-level coalescing was accepted")
	}
	_ = legacy.Rollback(db.ctx)
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'progress-worker',$2,interval '1 minute')`, child, lease) {
		t.Fatal("could not claim progress fixture")
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.record_ingest_progress($1,'progress-worker',$2,4,1,2,1,3,2,1,1)`, child, uuid.New()) {
		t.Fatal("stale lease recorded progress")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.record_ingest_progress($1,'progress-worker',$2,4,1,2,1,3,2,1,1)`, child, lease) ||
		!callBool(t, db.ctx, db.worker, account, `SELECT app.record_ingest_progress($1,'progress-worker',$2,4,1,2,1,3,2,1,1)`, child, lease) {
		t.Fatal("live lease did not accept idempotent absolute progress")
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.record_ingest_progress($1,'progress-worker',$2,3,1,1,1,3,2,1,1)`, child, lease) {
		t.Fatal("decreasing absolute progress was accepted")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT app.record_ingest_progress($1,'progress-worker',$2,3,1,2,1,3,2,1,1)`, child, lease); err == nil {
		t.Fatal("file counters exceeding discovery were accepted")
	}
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_event($1,'progress-worker',$2,'ingest-progress',jsonb_build_object('count',1))`, child, lease) ||
		!callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_log($1,'progress-worker',$2,'ingest-progress',jsonb_build_object('count',1))`, child, lease) {
		t.Fatal("fenced safe event or log insertion failed")
	}
	for _, fields := range []string{
		`jsonb_build_object('path','/private/leak')`,
		`jsonb_build_object('relativeName','secret.json')`,
		`jsonb_build_object('reason','Bearer payload-secret')`,
		`jsonb_build_object('coordinates','1.2,3.4')`,
	} {
		if _, err := db.worker.Exec(db.ctx, `SELECT app.record_job_event($1,'progress-worker',$2,'ingest-progress',`+fields+`)`, child, lease); err == nil {
			t.Fatalf("hostile event fields were accepted: %s", fields)
		}
	}
	bounded := beginAccount(t, db.ctx, db.migration, account)
	if _, err := bounded.Exec(db.ctx, `INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message,fields)
		SELECT $1,$2,'info','ingest-progress','Ingest progress was recorded.','{}'::jsonb FROM generate_series(1,999)`, account, child); err != nil {
		t.Fatal(err)
	}
	if _, err := bounded.Exec(db.ctx, `INSERT INTO app.job_logs(account_id,job_id,severity,code,redacted_message,fields)
		SELECT $1,$2,'debug','ingest-progress','Ingest progress was recorded.','{}'::jsonb FROM generate_series(1,1999)`, account, child); err != nil {
		t.Fatal(err)
	}
	if err := bounded.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_event($1,'progress-worker',$2,'ingest-progress','{}'::jsonb)`, child, lease) ||
		callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_log($1,'progress-worker',$2,'ingest-progress','{}'::jsonb)`, child, lease) {
		t.Fatal("per-job event or log row bound was exceeded")
	}
	tx = beginAccount(t, db.ctx, db.api, account)
	var childCurrent, childTotal, parentCurrent, parentTotal int64
	if err := tx.QueryRow(db.ctx, `SELECT child.progress_current,child.progress_total,parent.progress_current,parent.progress_total
		FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id WHERE child.id=$1`, child).Scan(
		&childCurrent, &childTotal, &parentCurrent, &parentTotal); err != nil {
		t.Fatal(err)
	}
	if childCurrent != 4 || childTotal != 4 || parentCurrent != 4 || parentTotal != 4 {
		t.Fatalf("unexpected derived progress child=%d/%d parent=%d/%d", childCurrent, childTotal, parentCurrent, parentTotal)
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.job_source_contexts SET display_name='Changed' WHERE job_id=$1`, child); err == nil {
		t.Fatal("immutable source context was updated")
	}
	_ = tx.Rollback(db.ctx)
	if _, err := db.worker.Exec(db.ctx, `INSERT INTO app.job_logs(account_id,job_id,severity,code,redacted_message) VALUES($1,$2,'info','direct','direct')`, account, child); err == nil {
		t.Fatal("worker directly inserted a job log")
	}
}

func TestOwnedCancellationAttributionAndWorkerDenial(t *testing.T) {
	db := openTestDatabases(t)
	account, foreignAccount := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "workout_deletion")
	owner := accountRequester(t, db, account)
	foreign := accountRequester(t, db, foreignAccount)
	if callBool(t, db.ctx, db.api, account, `SELECT app.request_owned_job_cancellation($1,$2)`, jobID, uuid.New()) {
		t.Fatal("arbitrary principal attributed cancellation")
	}
	if callBool(t, db.ctx, db.api, account, `SELECT app.request_owned_job_cancellation($1,$2)`, jobID, foreign) {
		t.Fatal("foreign principal attributed cancellation")
	}
	if _, err := db.worker.Exec(db.ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, jobID, owner); err == nil {
		t.Fatal("worker executed owner cancellation wrapper")
	}
	if !callBool(t, db.ctx, db.api, account, `SELECT app.request_owned_job_cancellation($1,$2)`, jobID, owner) {
		t.Fatal("account owner could not request cancellation")
	}
}

func TestConcurrentSiblingProgressHasExactParentAggregate(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
	children := []uuid.UUID{
		insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source"),
		insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source"),
	}
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3)`, parent, children[0], account); err != nil {
		t.Fatal(err)
	}
	for index, child := range children {
		sourceID := snapshotSourceID(t, db, account, child)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts
			(job_id,account_id,source_id,source_generation,display_name,source_type)
			VALUES($1,$2,$3,1,$4,'health-auto-export-local')`, child, account, sourceID, fmt.Sprintf("Sibling %d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	leases := []uuid.UUID{uuid.New(), uuid.New()}
	for index, child := range children {
		if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,$2,$3,interval '1 minute')`, child, fmt.Sprintf("sibling-%d", index), leases[index]) {
			t.Fatal("could not claim sibling")
		}
	}
	type progressResult struct {
		changed bool
		err     error
	}
	results := make(chan progressResult, 2)
	calls := []struct {
		pool *pgxpool.Pool
		args []any
	}{
		{db.worker, []any{children[0], "sibling-0", leases[0], 5, 1, 3, 1, 2, 1, 1, 0}},
		{db.migration, []any{children[1], "sibling-1", leases[1], 7, 2, 4, 1, 3, 2, 1, 1}},
	}
	for _, call := range calls {
		go func() {
			tx, err := call.pool.Begin(db.ctx)
			if err != nil {
				results <- progressResult{err: err}
				return
			}
			defer tx.Rollback(db.ctx)
			_, err = tx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String())
			var changed bool
			if err == nil {
				err = tx.QueryRow(db.ctx, `SELECT app.record_ingest_progress($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, call.args...).Scan(&changed)
			}
			if err == nil {
				err = tx.Commit(db.ctx)
			}
			results <- progressResult{changed: changed, err: err}
		}()
	}
	for range 2 {
		result := <-results
		if result.err != nil || !result.changed {
			t.Fatalf("concurrent sibling progress failed: changed=%t err=%v", result.changed, result.err)
		}
	}
	tx = beginAccount(t, db.ctx, db.api, account)
	defer tx.Rollback(db.ctx)
	var discovered, skipped, succeeded, failed, current, total int64
	if err := tx.QueryRow(db.ctx, `SELECT p.files_discovered,p.files_skipped,p.files_succeeded,p.files_failed,j.progress_current,j.progress_total
		FROM app.job_progress p JOIN app.jobs j ON j.id=p.job_id WHERE p.job_id=$1`, parent).Scan(
		&discovered, &skipped, &succeeded, &failed, &current, &total); err != nil {
		t.Fatal(err)
	}
	if discovered != 12 || skipped != 3 || succeeded != 7 || failed != 2 || current != 12 || total != 12 {
		t.Fatalf("lost sibling aggregate: discovered=%d skipped=%d succeeded=%d failed=%d progress=%d/%d",
			discovered, skipped, succeeded, failed, current, total)
	}
}

func TestProgressDoesNotDeadlockCancellationOrSourceDeletion(t *testing.T) {
	for _, operation := range []string{"cancellation", "source-deletion"} {
		t.Run(operation, func(t *testing.T) {
			db := openTestDatabases(t)
			account, parent, child, sourceID, lease := insertDurableRunningChild(t, db, "progress-race")
			requester := accountRequester(t, db, account)
			type result struct {
				changed bool
				err     error
			}
			results := make(chan result, 2)
			run := func(pool *pgxpool.Pool, query string, args ...any) {
				tx, err := pool.Begin(db.ctx)
				if err != nil {
					results <- result{err: err}
					return
				}
				defer tx.Rollback(db.ctx)
				_, err = tx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String())
				if err == nil {
					_, err = tx.Exec(db.ctx, `SET LOCAL lock_timeout='1s'`)
				}
				var changed bool
				if err == nil {
					err = tx.QueryRow(db.ctx, query, args...).Scan(&changed)
				}
				if err == nil {
					err = tx.Commit(db.ctx)
				}
				results <- result{changed: changed, err: err}
			}
			go run(db.worker, `SELECT app.record_ingest_progress($1,'progress-race',$2,2,0,1,0,1,0,0,0)`, child, lease)
			if operation == "cancellation" {
				go run(db.api, `SELECT app.request_owned_job_cancellation($1,$2)`, parent, requester)
			} else {
				go run(db.api, `SELECT app.delete_source($1,$2)`, sourceID, requester)
			}
			for range 2 {
				got := <-results
				if got.err != nil {
					t.Fatalf("progress/%s race failed: %v", operation, got.err)
				}
			}
			if callBool(t, db.ctx, db.worker, account, `SELECT app.record_ingest_progress($1,'progress-race',$2,2,0,1,0,1,0,0,0)`, child, lease) {
				t.Fatalf("progress succeeded after %s", operation)
			}
		})
	}
}

func TestEventWritesSerializeWithFinishAndRecovery(t *testing.T) {
	t.Run("finish", func(t *testing.T) {
		db := openTestDatabases(t)
		account, _, child, _, lease := insertDurableRunningChild(t, db, "event-finish")
		type result struct {
			changed bool
			err     error
		}
		results := make(chan result, 2)
		queries := []struct {
			pool  *pgxpool.Pool
			query string
		}{
			{db.worker, `SELECT app.record_job_event($1,'event-finish',$2,'ingest-progress','{}'::jsonb)`},
			{db.migration, `SELECT app.finish_job($1,'event-finish',$2,'succeeded')`},
		}
		for _, call := range queries {
			go func() {
				tx, err := call.pool.Begin(db.ctx)
				if err != nil {
					results <- result{err: err}
					return
				}
				defer tx.Rollback(db.ctx)
				_, err = tx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true)`, account.String())
				var changed bool
				if err == nil {
					err = tx.QueryRow(db.ctx, call.query, child, lease).Scan(&changed)
				}
				if err == nil {
					err = tx.Commit(db.ctx)
				}
				results <- result{changed: changed, err: err}
			}()
		}
		for range 2 {
			got := <-results
			if got.err != nil {
				t.Fatal(got.err)
			}
		}
		if status, _ := readStatus(t, db.ctx, db.worker, account, child); status != "succeeded" {
			t.Fatalf("finish race left child status=%s", status)
		}
		if callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_event($1,'event-finish',$2,'ingest-progress','{}'::jsonb)`, child, lease) {
			t.Fatal("terminal lease appended an event")
		}
	})

	t.Run("recovery", func(t *testing.T) {
		db := openTestDatabases(t)
		account, _, child, _, lease := insertDurableRunningChild(t, db, "event-recovery")
		tx := beginAccount(t, db.ctx, db.migration, account)
		if _, err := tx.Exec(db.ctx, `SELECT set_config('app.job_transition','heartbeat',true)`); err == nil {
			_, err = tx.Exec(db.ctx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, child)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		if callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_log($1,'event-recovery',$2,'ingest-progress','{}'::jsonb)`, child, lease) {
			t.Fatal("expired lease appended a log")
		}
		if !callBool(t, db.ctx, db.migration, account, `SELECT app.recover_expired_job($1)`, child) {
			t.Fatal("expired job was not recovered")
		}
		if callBool(t, db.ctx, db.worker, account, `SELECT app.record_job_log($1,'event-recovery',$2,'ingest-progress','{}'::jsonb)`, child, lease) {
			t.Fatal("recovered lease appended a log")
		}
	})
}

func openPool(t *testing.T, ctx context.Context, databaseURL string, maxConns int32) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = maxConns
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func beginAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account uuid.UUID) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id', $1, true)`, account.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func insertQueued(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account, parent uuid.UUID, kind string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	tx := beginAccount(t, ctx, pool, account)
	var sourceID uuid.UUID
	if kind == "source_connection_check" || kind == "manual_ingest_source" || kind == "scheduled_ingest_source" {
		sourceID = uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT (id) DO NOTHING`, account); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.sources
			(id, account_id, display_name, canonical_display_name, type, config_envelope)
			VALUES ($1, $2, $3, $3, 'health-auto-export-local', $4)`, sourceID, account, "source-"+sourceID.String(), []byte(`{"encrypted":true}`)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
	}
	var parentValue any
	if parent != uuid.Nil {
		parentValue = parent
	}
	parameters := `{}`
	if kind == "manual_ingest_source" || kind == "scheduled_ingest_source" {
		parameters = fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`,
			strings.ToUpper(strings.ReplaceAll(sourceID.String(), "-", "")))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs (id, parent_job_id, account_id, kind, priority, parameters)
		VALUES ($1, $2, $3, $4, 80, $5)`, id, parentValue, account, kind, parameters); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if sourceID != uuid.Nil {
		if _, err := tx.Exec(ctx, `INSERT INTO app.job_config_snapshots
			(job_id, account_id, source_id, source_generation, config_envelope)
			VALUES ($1, $2, $3, 1, $4)`, id, account, sourceID, []byte(`{"snapshot":true}`)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func accountRequester(t *testing.T, db testDatabases, account uuid.UUID) uuid.UUID {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, account)
	defer tx.Rollback(db.ctx)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT (id) DO NOTHING`, account); err != nil {
		t.Fatal(err)
	}
	var principal uuid.UUID
	err := tx.QueryRow(db.ctx, `SELECT principal_id FROM app.users WHERE account_id=$1`, account).Scan(&principal)
	if errors.Is(err, pgx.ErrNoRows) {
		principal = uuid.Must(uuid.NewV7())
		identity := strings.ReplaceAll(principal.String(), "-", "")
		if _, err = tx.Exec(db.ctx, `INSERT INTO app.authentication_principals
			(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
			VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Cancellation Test')`,
			principal, "cancel"+identity, "cancel"+identity+"@example.test"); err == nil {
			_, err = tx.Exec(db.ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principal, account)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return principal
}

func requestCancellation(t *testing.T, db testDatabases, account, jobID uuid.UUID) bool {
	t.Helper()
	return callBool(t, db.ctx, db.api, account, `SELECT app.request_owned_job_cancellation($1,$2)`, jobID, accountRequester(t, db, account))
}

func insertDurableRunningChild(t *testing.T, db testDatabases, workerID string) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	account := uuid.Must(uuid.NewV7())
	parent := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "manual_ingest")
	child := insertQueued(t, db.ctx, db.api, account, parent, "manual_ingest_source")
	sourceID := snapshotSourceID(t, db, account, child)
	tx := beginAccount(t, db.ctx, db.api, account)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3)`, parent, child, account); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'Durable race source','health-auto-export-local')`, child, account, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,$2,$3,interval '1 minute')`, child, workerID, lease) {
		t.Fatal("could not claim durable child")
	}
	return account, parent, child, sourceID, lease
}

func recordSuccessfulObject(t *testing.T, db testDatabases, account, child, lease uuid.UUID, workerID, name, exportDate, identity string, checksumByte byte) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, account)
	defer tx.Rollback(db.ctx)
	var sourceID uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3)`, child, workerID, lease).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	var recorded bool
	if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,$5,1,
		transaction_timestamp(),$6,$7)`, child, workerID, lease, name, exportDate, identity,
		bytes.Repeat([]byte{checksumByte}, 32)).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("record successful object=%t err=%v", recorded, err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func callBoolNoAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) bool {
	t.Helper()
	var result bool
	if err := pool.QueryRow(ctx, query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func callBool(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account uuid.UUID, query string, args ...any) bool {
	t.Helper()
	tx := beginAccount(t, ctx, pool, account)
	var result bool
	if err := tx.QueryRow(ctx, query, args...).Scan(&result); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return result
}

func readStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account, jobID uuid.UUID) (string, int) {
	t.Helper()
	tx := beginAccount(t, ctx, pool, account)
	defer tx.Rollback(ctx)
	var status string
	var attempt int
	if err := tx.QueryRow(ctx, `SELECT status, attempt FROM app.jobs WHERE id = $1`, jobID).Scan(&status, &attempt); err != nil {
		t.Fatal(err)
	}
	return status, attempt
}

func snapshotCount(t *testing.T, db testDatabases, account, jobID uuid.UUID) int {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.api, account)
	defer tx.Rollback(db.ctx)
	var count int
	if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.job_config_snapshots WHERE job_id=$1`, jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func snapshotSourceID(t *testing.T, db testDatabases, account, jobID uuid.UUID) uuid.UUID {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, account)
	defer tx.Rollback(db.ctx)
	var sourceID uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.job_config_snapshots WHERE job_id=$1`, jobID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	return sourceID
}

func deleteSourceFixture(t *testing.T, db testDatabases, account, sourceID uuid.UUID) {
	t.Helper()
	if !callBool(t, db.ctx, db.api, account, `SELECT app.delete_source($1,$2)`, sourceID, uuid.Must(uuid.NewV7())) {
		t.Fatal("delete source fixture returned false")
	}
}

func finishChild(t *testing.T, db testDatabases, account, jobID uuid.UUID, status string) {
	t.Helper()
	if status == "cancelled" {
		if !requestCancellation(t, db, account, jobID) {
			t.Fatal("child cancellation failed")
		}
		return
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1, 'parent-test-worker', $2, interval '1 minute')`, jobID, lease) ||
		!callBool(t, db.ctx, db.worker, account, `SELECT app.finish_job($1, 'parent-test-worker', $2, $3)`, jobID, lease, status) {
		t.Fatal("child completion failed")
	}
}

func assertRuntimeRole(t *testing.T, ctx context.Context, pool *pgxpool.Pool, expected string) {
	t.Helper()
	var role string
	var superuser, bypass, createRole bool
	if err := pool.QueryRow(ctx, `SELECT current_user, rolsuper, rolbypassrls, rolcreaterole FROM pg_roles WHERE rolname = current_user`).Scan(&role, &superuser, &bypass, &createRole); err != nil {
		t.Fatal(err)
	}
	if role != expected || superuser || bypass || createRole {
		t.Fatalf("role=%q superuser=%t bypassrls=%t createrole=%t", role, superuser, bypass, createRole)
	}
}

func assertJobVisible(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account string, jobID uuid.UUID, want bool) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if account != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id', $1, true)`, account); err != nil {
			t.Fatal(err)
		}
	}
	var got bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.jobs WHERE id = $1)`, jobID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("visibility=%t, want %t", got, want)
	}
}

func expectOwnerMutationRejected(t *testing.T, db testDatabases, account uuid.UUID, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, query, args...); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("illegal owner-level job mutation unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)
}

func expectFencedIngestMutationRejected(t *testing.T, db testDatabases, account, jobID uuid.UUID, worker string, lease uuid.UUID, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, account)
	var sourceID uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3)`, jobID, worker, lease).Scan(&sourceID); err != nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, query, args...); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("invalid ingest-domain mutation unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)
}
