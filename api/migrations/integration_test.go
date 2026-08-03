package migrations_test

import (
	"context"
	"os"
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
	if _, err := db.migration.Exec(db.ctx, `UPDATE app.schema_metadata SET schema_version = 2, minimum_runtime_version = 2`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `UPDATE app.schema_metadata SET schema_version = 1, minimum_runtime_version = 1`)
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

func TestStandaloneTransitionsFencingCancellationAndAttempts(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "source_connection_check")
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

func TestLeaseRecoveryAndTerminalImmutability(t *testing.T) {
	db := openTestDatabases(t)
	account := uuid.Must(uuid.NewV7())
	jobID := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "source_connection_check")
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
	jobID := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "source_connection_check")
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

	recoverID := insertQueued(t, db.ctx, db.worker, account, uuid.Nil, "source_connection_check")
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
			left := insertQueued(t, db.ctx, db.worker, account, parent, "manual_ingest_source")
			right := insertQueued(t, db.ctx, db.worker, account, parent, "manual_ingest_source")
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
	insertQueued(t, db.ctx, db.worker, account, parent, "scheduled_ingest_source")
	insertQueued(t, db.ctx, db.worker, account, parent, "scheduled_ingest_source")
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
	var parentValue any
	if parent != uuid.Nil {
		parentValue = parent
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs (id, parent_job_id, account_id, kind, priority) VALUES ($1, $2, $3, $4, 80)`, id, parentValue, account, kind); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
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
	var superuser, bypass bool
	if err := pool.QueryRow(ctx, `SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&role, &superuser, &bypass); err != nil {
		t.Fatal(err)
	}
	if role != expected || superuser || bypass {
		t.Fatalf("role=%q superuser=%t bypassrls=%t", role, superuser, bypass)
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
