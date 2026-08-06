package migrations_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	ctx := context.Background()
	return testDatabases{
		ctx:       ctx,
		api:       openPool(t, ctx, apiURL, 1),
		worker:    openPool(t, ctx, workerURL, 1),
		migration: openPool(t, ctx, migrationURL, 2),
	}
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
	if _, err := db.migration.Exec(db.ctx, `UPDATE app.schema_metadata SET schema_version = 7, minimum_runtime_version = 7`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `UPDATE app.schema_metadata SET schema_version = 6, minimum_runtime_version = 1`)
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
	if callBool(t, db.ctx, db.worker, accountB, `SELECT app.request_job_cancellation($1, $2)`, jobID, uuid.Must(uuid.NewV7())) {
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

func TestDeferredSnapshotConsistencyAcceptsOrdinaryAndSourceJobCommits(t *testing.T) {
	db := openTestDatabases(t)
	ordinaryAccount, ordinaryJob := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tx := beginAccount(t, db.ctx, db.api, ordinaryAccount)
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
	if !callBool(t, db.ctx, db.api, ordinaryAccount, `SELECT app.request_job_cancellation($1,$2)`, ordinaryJob, uuid.Must(uuid.NewV7())) ||
		!callBool(t, db.ctx, db.api, sourceAccount, `SELECT app.request_job_cancellation($1,$2)`, sourceJob, uuid.Must(uuid.NewV7())) {
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
	requester := uuid.Must(uuid.NewV7())
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1, $2)`, jobID, requester) {
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
	if callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1, $2)`, jobID, requester) {
		t.Fatal("terminal cancellation unexpectedly succeeded")
	}

	queued := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "workout_deletion")
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1, $2)`, queued, requester) {
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
	if !callBool(t, db.ctx, db.api, account, `SELECT app.request_job_cancellation($1,$2)`, queued, uuid.Must(uuid.NewV7())) || snapshotCount(t, db, account, queued) != 0 {
		t.Fatal("queued cancellation did not atomically remove its snapshot")
	}

	recovering := insertQueued(t, db.ctx, db.api, account, uuid.Nil, "source_connection_check")
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'snapshot-worker',$2,interval '1 millisecond')`, recovering, uuid.New()) ||
		!callBool(t, db.ctx, db.api, account, `SELECT app.request_job_cancellation($1,$2)`, recovering, uuid.Must(uuid.NewV7())) {
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

	if !callBool(t, db.ctx, db.api, account, `SELECT app.request_job_cancellation($1,$2)`, runningJob, uuid.Must(uuid.NewV7())) {
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
	if callBool(t, db.ctx, db.api, account, `SELECT app.request_job_cancellation($1,$2)`, queuedJob, uuid.Must(uuid.NewV7())) {
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
		FROM app.claim_next_worker_job('import-worker',$1,interval '1 minute')`, lease).Scan(
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
		FROM app.claim_next_worker_job('recovery-worker',$1,interval '1 minute')`, recoveryLease).Scan(
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
		FROM app.claim_next_worker_job('scheduled-worker',$1,interval '1 minute')`, lease).Scan(
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
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority)
		VALUES($1,$2,$3,'scheduled_ingest_source',80)`, uuid.Must(uuid.NewV7()), wrongParent, account); err == nil {
		_ = tx.Rollback(db.ctx)
		t.Fatal("scheduled child with manual parent was accepted")
	}
	_ = tx.Rollback(db.ctx)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1,$2)`, wrongParent, uuid.Must(uuid.NewV7())) {
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
		FROM app.claim_next_worker_job('skip-locked-worker',$1,interval '1 minute')`, lease).Scan(&jobID, &accountID, &kind); err != nil {
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
	if !callBool(t, db.ctx, db.api, lockedAccount, `SELECT app.request_job_cancellation($1,$2)`, lockedParent, uuid.Must(uuid.NewV7())) {
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
	tx := beginAccount(t, db.ctx, db.worker, account)
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
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,'slow-fenced-worker',$2,interval '100 milliseconds')`, jobID, lease) {
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
	time.Sleep(150 * time.Millisecond)

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
	if !callBool(t, db.ctx, db.api, account, `SELECT app.request_job_cancellation($1,$2)`, parent, uuid.Must(uuid.NewV7())) {
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
	go run(db.api, `SELECT app.request_job_cancellation($1,$2)`, parent, uuid.Must(uuid.NewV7()))
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
		err = tx.QueryRow(db.ctx, `SELECT app.request_job_cancellation($1,$2)`, child, uuid.Must(uuid.NewV7())).Scan(&cancelled)
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
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1, $2)`, recoverID, uuid.Must(uuid.NewV7())) {
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
		})
	}

	parent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "scheduled_ingest")
	insertQueued(t, db.ctx, db.api, account, parent, "scheduled_ingest_source")
	insertQueued(t, db.ctx, db.api, account, parent, "scheduled_ingest_source")
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1, $2)`, parent, uuid.Must(uuid.NewV7())) {
		t.Fatal("parent cancellation failed")
	}
	if status, _ := readStatus(t, db.ctx, db.worker, account, parent); status != "cancelled" {
		t.Fatalf("cancelled parent status=%s", status)
	}

	mutableParent := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "manual_ingest")
	tx := beginAccount(t, db.ctx, db.migration, account)
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET status = 'running' WHERE id = $1`, mutableParent); err == nil {
		t.Fatal("arbitrary parent transition unexpectedly succeeded")
	}
	_ = tx.Rollback(db.ctx)
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

func openPool(t *testing.T, ctx context.Context, databaseURL string, maxConns int32) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = maxConns
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
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs (id, parent_job_id, account_id, kind, priority) VALUES ($1, $2, $3, $4, 80)`, id, parentValue, account, kind); err != nil {
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
		if !callBool(t, db.ctx, db.worker, account, `SELECT app.request_job_cancellation($1, $2)`, jobID, uuid.Must(uuid.NewV7())) {
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
