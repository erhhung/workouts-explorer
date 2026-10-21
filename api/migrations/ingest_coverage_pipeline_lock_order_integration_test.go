package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The three transactions reproduce parent/child -> state claim contention, not
// merely simultaneous goroutine starts. No real exports, OSM or profiles are used.
func TestCoveragePipelineClaimLockOrderPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_PIPELINE_LOCK_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit migrated disposable lock-order database required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "workouts_pipeline_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("lock-order tests require a named loopback scratch database")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("remote/fallback endpoints prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := func(role string) *pgxpool.Pool {
		cfg := config.Copy()
		cfg.MaxConns = 4
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "8s"
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	var empty bool
	if err := db.migration.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.jobs) AND
		(SELECT schema_version=$1 AND minimum_runtime_version=28 FROM app.schema_metadata WHERE singleton)`, database.SupportedSchemaVersion).Scan(&empty); err != nil || !empty {
		t.Fatalf("requires an empty migrated supported scratch database: %v", err)
	}
	for _, authority := range []string{"ingest", "reconciliation"} {
		for _, scenario := range []string{"normal_fresh_sealed_cohort", "normal_supersession", "adopted_queued_target", "adopted_running_target", "normal_child_only", "adopted_child_only"} {
			t.Run(authority+"/"+scenario, func(t *testing.T) { pipelineClaimLockOrder(t, db, authority, scenario) })
		}
	}
}

func pipelineClaimLockOrder(t *testing.T, db testDatabases, authority, scenario string) {
	t.Helper()
	ctx := db.ctx
	f, sourceJob, sourceLease := pipelineSource(t, db, 2)
	pipelineConfigure(t, db, 1)
	read := func() pipelineSnapshot {
		t.Helper()
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var item pipelineSnapshot
		if err := tx.QueryRow(ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,25,true)`,
			f.account, sourceJob, sourceLease).Scan(&item.workout, &item.event, &item.origin, &item.content, &item.epoch); err != nil {
			t.Fatal(err)
		}
		var content, coordinates []byte
		var epoch int64
		var count int
		if err := tx.QueryRow(ctx, `SELECT * FROM app.read_ingest_coverage_route_snapshot($1,$2,'pipeline-test',$3,NULL,$4,$5,$6)`,
			f.account, sourceJob, sourceLease, item.workout, item.event, item.origin).Scan(&content, &epoch, &item.digest, &count, &coordinates); err != nil {
			t.Fatal(err)
		}
		if string(content) != string(item.content) || epoch != item.epoch || count != 2 {
			t.Fatal("inconsistent synthetic route snapshot")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return item
	}
	pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[:1], "created")
	pipelineApply(t, db, f, sourceJob, sourceLease, []pipelineSnapshot{read()}, "map_data_ready")
	var parent, child uuid.UUID
	if err := db.migration.QueryRow(ctx, `SELECT child.parent_job_id,child.id FROM app.jobs child JOIN app.workout_coverage_states state ON state.target_job_id=child.id
		WHERE state.account_id=$1 AND state.workout_id=$2`, f.account, f.workouts[0]).Scan(&parent, &child); err != nil {
		t.Fatal(err)
	}
	running := scenario == "adopted_running_target"
	if running && !callBool(t, ctx, db.migration, f.account, `SELECT app.claim_job($1,'lock-order-running',$2,interval '2 minutes')`, child, uuid.New()) {
		t.Fatal("claim running target")
	}
	freshCohort := scenario == "normal_fresh_sealed_cohort"
	adopted := strings.HasPrefix(scenario, "adopted")
	if freshCohort {
		pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[1:], "created")
	} else if adopted {
		pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[:1], "adopted")
		tx := beginAccount(t, ctx, db.worker, f.account)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, sourceJob, sourceLease); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'pipeline-test',$2,$3)`, sourceJob, sourceLease, f.workouts[0]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		bootstrapMutatePoints(t, db, f, `UPDATE app.workout_route_points SET altitude=1 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[:1], "updated")
	}
	item := read()
	payload, err := json.Marshal(pipelineReady([]pipelineSnapshot{item}, "map_data_ready"))
	if err != nil {
		t.Fatal(err)
	}
	var job any = sourceJob
	var revision any
	worker := "pipeline-test"
	lease := sourceLease
	role := db.worker
	if authority == "reconciliation" {
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, sourceJob, sourceLease) {
			t.Fatal("finish source for account authority")
		}
		job = nil
		revision = int64(14)
		worker = "lock-order-recovery"
		lease = uuid.New()
		role = db.coverage
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id=$2,lease_token=$3,heartbeat_at=clock_timestamp(),
			lease_expires_at=clock_timestamp()+interval '2 minutes',lease_runtime_version=28 WHERE account_id=$1`, f.account, worker, lease)
	}
	// A state holder forces the claimer to stop at its parent/child -> state edge.
	stateHolder := beginAccount(t, ctx, db.migration, f.account)
	defer stateHolder.Rollback(ctx)
	var holderPID int
	if err := stateHolder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err := stateHolder.Exec(ctx, `SELECT 1 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2 FOR UPDATE`, f.account, f.workouts[0]); err != nil {
		t.Fatal(err)
	}
	claimer := beginAccount(t, ctx, db.migration, f.account)
	defer claimer.Rollback(ctx)
	var claimerPID int
	if err := claimer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&claimerPID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(scenario, "child_only") {
		if _, err := claimer.Exec(ctx, `SELECT 1 FROM app.jobs WHERE id=$1 FOR UPDATE`, parent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := claimer.Exec(ctx, `SELECT 1 FROM app.jobs WHERE id=$1 FOR UPDATE`, child); err != nil {
		t.Fatal(err)
	}
	claimDone := make(chan error, 1)
	go func() {
		_, err := claimer.Exec(ctx, `SELECT 1 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2 FOR UPDATE`, f.account, f.workouts[0])
		claimDone <- err
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		var waiting bool
		if err := db.migration.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2))`, holderPID, claimerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-claimDone:
			t.Fatalf("claimer did not wait for state: %v", err)
		case <-deadline.C:
			t.Fatal("could not observe claimer waiting for state")
		case <-time.After(time.Millisecond):
		}
	}
	applyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	applyDone := make(chan error, 1)
	go func() {
		tx, err := role.Begin(applyCtx)
		if err != nil {
			applyDone <- err
			return
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(applyCtx, `SELECT set_config('app.account_id',$1,true)`, f.account.String()); err != nil {
			applyDone <- err
			return
		}
		_, err = tx.Exec(applyCtx, pipelineApplySQL, f.account, job, worker, lease, revision, payload)
		applyDone <- err
	}()
	err = <-applyDone
	var busy *pgconn.PgError
	if !errors.As(err, &busy) || busy.Code != "55P03" || !strings.Contains(busy.Message, `relation "jobs"`) {
		t.Fatalf("apply must fail on Coverage parent/child BEFORE workout/state, not wait/deadlock: %v", err)
	}
	var stillWaiting bool
	if err := db.migration.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2))`, holderPID, claimerPID).Scan(&stillWaiting); err != nil || !stillWaiting {
		t.Fatalf("pipeline stole the claimer's state ordering: %v", err)
	}
	if err := stateHolder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-claimDone; err != nil {
		t.Fatal("claimer did not complete after holder released:", err)
	}
	if err := claimer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Every lock owner is gone. Fresh metadata + route CAS retry must consume the
	// origin once, with either a fresh sealed cohort or true target supersession.
	applyTx := beginAccount(t, ctx, role, f.account)
	if _, err := applyTx.Exec(ctx, pipelineApplySQL, f.account, job, worker, lease, revision, payload); err != nil {
		t.Fatal("apply after contention:", err)
	}
	pipelineSealPrepared(t, db, applyTx, f.account, job, worker, lease, revision)
	if err := applyTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var target, newParent uuid.UUID
	if err := db.migration.QueryRow(ctx, `SELECT state.target_job_id,child.parent_job_id FROM app.workout_coverage_states state
		JOIN app.jobs child ON child.id=state.target_job_id WHERE state.account_id=$1 AND state.workout_id=$2`, f.account, item.workout).Scan(&target, &newParent); err != nil {
		t.Fatal(err)
	}
	if target == child {
		t.Fatal("new target was not queued after contention")
	}
	if running && newParent == parent {
		t.Fatal("fresh work coalesced into a running parent")
	}
	if freshCohort && newParent == parent {
		t.Fatal("sealed ingest parent was reused by a later cohort")
	}
	var valid bool
	if err := db.migration.QueryRow(ctx, `SELECT count(*)=2 AND count(DISTINCT coverage_child_job_id)=2 AND bool_and(acknowledged_at IS NOT NULL)
		FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.account).Scan(&valid); err != nil || !valid {
		t.Fatalf("origin lost/duplicated during contention: %t %v", valid, err)
	}
	if err := db.migration.QueryRow(ctx, `SELECT count(*)=2 FROM app.coverage_route_job_contexts WHERE account_id=$1`, f.account).Scan(&valid); err != nil || !valid {
		t.Fatal("contention allocated an extra coverage child")
	}
	if !freshCohort && !running {
		if err := db.migration.QueryRow(ctx, `SELECT child.status='succeeded' AND context.result_outcome='superseded' FROM app.jobs child
			JOIN app.coverage_route_job_contexts context ON context.job_id=child.id WHERE child.id=$1`, child).Scan(&valid); err != nil || !valid {
			t.Fatal("old queued target was not superseded")
		}
	}
	if adopted {
		if err := db.migration.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
			AND (SELECT count(*)=1 AND bool_and(acknowledged_at IS NOT NULL AND coverage_child_job_id=$2)
			 FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1)`, f.account, target).Scan(&valid); err != nil || !valid {
			t.Fatal("adoption receipt lost its authoritative child")
		}
	}
	replay := beginAccount(t, ctx, role, f.account)
	_, err = replay.Exec(ctx, pipelineApplySQL, f.account, job, worker, lease, revision, payload)
	bootstrapSQLState(t, err, "40001")
	_ = replay.Rollback(ctx)
	if err := db.migration.QueryRow(ctx, `SELECT count(*)=2 FROM app.coverage_route_job_contexts WHERE account_id=$1`, f.account).Scan(&valid); err != nil || !valid {
		t.Fatal("acknowledged origin replay created another child")
	}
	t.Logf("%s: parent/child NOWAIT55P03 before state; post-release apply+replay CAS verified", scenario)
}
