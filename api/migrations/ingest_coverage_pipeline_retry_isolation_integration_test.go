package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This suite deliberately uses an already migrated empty scratch database, not
// archived SQL or the retired rebaseline path. All ingest events/routes are synthetic.
func TestCoveragePipelineChainedRetryIsolationPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_PIPELINE_RETRY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit migrated disposable retry-isolation database required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "workouts_pipeline_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("retry isolation requires a named loopback scratch database")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("retry isolation prohibits remote/fallback endpoints")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
		var actual string
		if err := result.QueryRow(ctx, `SELECT current_user`).Scan(&actual); err != nil || actual != role {
			t.Fatalf("scratch role=%s want=%s err=%v", actual, role, err)
		}
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"),
		worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	var empty bool
	if err := db.migration.QueryRow(ctx, `SELECT (SELECT schema_version=$1 AND minimum_runtime_version=28 FROM app.schema_metadata WHERE singleton)
		AND NOT EXISTS(SELECT 1 FROM app.jobs)`, database.SupportedSchemaVersion).Scan(&empty); err != nil || !empty {
		t.Fatalf("retry isolation requires an empty migrated supported scratch database: %v", err)
	}
	for _, mode := range []string{"manual", "automatic"} {
		for _, sizes := range [][2]int{{25, 25}, {10, 10}, {25, 10}, {25, 1}} {
			t.Run(fmt.Sprintf("%s/original%d_current%d", mode, sizes[0], sizes[1]), func(t *testing.T) {
				pipelineChainedRetryIsolation(t, db, mode, sizes[0], sizes[1])
			})
		}
	}
}

func pipelineChainedRetryIsolation(t *testing.T, db testDatabases, mode string, originalSize, currentSize int) {
	t.Helper()
	ctx := db.ctx
	tail, extra := currentSize*3/5, currentSize/5
	if tail == 0 {
		tail = 1
	}
	if extra == 0 {
		extra = 1
	}
	f, sourceJob, sourceLease := pipelineSource(t, db, originalSize+currentSize+tail+extra+1)
	requester := accountRequester(t, db, f.account)
	check := func(query string, args ...any) {
		t.Helper()
		var valid bool
		if err := db.migration.QueryRow(ctx, query, args...).Scan(&valid); err != nil || !valid {
			t.Fatalf("retry isolation invariant failed: %v\n%s", err, query)
		}
	}
	read := func(size int, flush bool) []pipelineSnapshot {
		t.Helper()
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,$4,$5)`,
			f.account, sourceJob, sourceLease, size, flush)
		if err != nil {
			t.Fatal(err)
		}
		var result []pipelineSnapshot
		for rows.Next() {
			var item pipelineSnapshot
			if err := rows.Scan(&item.workout, &item.event, &item.origin, &item.content, &item.epoch); err != nil {
				t.Fatal(err)
			}
			result = append(result, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		for i := range result {
			item := &result[i]
			var content, coordinates []byte
			var epoch int64
			var count int
			if err := tx.QueryRow(ctx, `SELECT * FROM app.read_ingest_coverage_route_snapshot($1,$2,'pipeline-test',$3,NULL,$4,$5,$6)`,
				f.account, sourceJob, sourceLease, item.workout, item.event, item.origin).Scan(&content, &epoch, &item.digest, &count, &coordinates); err != nil {
				t.Fatal(err)
			}
			if string(content) != string(item.content) || epoch != item.epoch || count != 2 {
				t.Fatal("route snapshot changed from metadata")
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return result
	}
	type routeClaim struct{ job, parent, workout, lease uuid.UUID }
	claim := func() routeClaim {
		t.Helper()
		result := routeClaim{lease: uuid.New()}
		tx := beginAccount(t, ctx, db.coverage, f.account)
		defer tx.Rollback(ctx)
		var account uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT job_id,parent_job_id,workout_id,account_id FROM app.claim_next_coverage_route(
			'retry-isolation-matcher',$1,interval '1 minute',$2)`, result.lease, database.SupportedSchemaVersion).
			Scan(&result.job, &result.parent, &result.workout, &account); err != nil || account != f.account {
			t.Fatalf("claim account=%s: %v", account, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return result
	}
	finish := func(route routeClaim, failure string) {
		t.Helper()
		var query string
		var args []any
		if failure == "" {
			query = `SELECT app.persist_coverage_route($1,'retry-isolation-matcher',$2,1,
				'[{"regionId":"geofabrik:test","generation":1}]','[]') IN ('applied','no_evidence')`
			args = []any{route.job, route.lease}
		} else {
			query = `SELECT app.fail_coverage_route($1,'retry-isolation-matcher',$2,1,$3,'Synthetic retry isolation failure.')`
			args = []any{route.job, route.lease, failure}
		}
		if !callBool(t, ctx, db.coverage, f.account, query, args...) {
			t.Fatal("finish synthetic coverage child")
		}
	}
	pipelineConfigure(t, db, originalSize)
	pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[:originalSize], "created")
	items := read(originalSize, false)
	if len(items) != originalSize {
		t.Fatalf("initial full batch=%d want=%d", len(items), originalSize)
	}
	pipelineApply(t, db, f, sourceJob, sourceLease, items, "map_data_ready")
	var root uuid.UUID
	if err := db.migration.QueryRow(ctx, `SELECT parent_job_id FROM app.jobs WHERE id=(SELECT target_job_id
		FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2)`, f.account, f.workouts[0]).Scan(&root); err != nil {
		t.Fatal(err)
	}
	failed := make([]uuid.UUID, 0, 3)
	for i := 0; i < originalSize; i++ {
		route := claim()
		if route.parent != root {
			t.Fatalf("initial child belongs to %s, want %s", route.parent, root)
		}
		if i == 0 {
			check(`SELECT status='running' FROM app.jobs WHERE id=$1`, root)
		}
		failure := ""
		if i >= originalSize-3 {
			failed = append(failed, route.job)
			failure = "coverage-route-timeout"
			if i == originalSize-2 {
				failure = "coverage-transient-failed"
			}
		}
		finish(route, failure)
	}
	check(`SELECT parent.status='partially_succeeded' AND progress.routes_total=$2 AND progress.routes_succeeded=$2-3
		AND progress.routes_failed=3 AND (SELECT count(*)=3 FROM app.coverage_retry_targets($1,$3))
		FROM app.jobs parent JOIN app.coverage_job_progress progress ON progress.job_id=parent.id WHERE parent.id=$1`, root, originalSize, f.account)
	// Model already persisted timeout history and advance only the retry due time.
	// Status transitions/enrollment above use the real claim/finish helpers.
	age := beginAccount(t, ctx, db.migration, f.account)
	if _, err := age.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := age.Exec(ctx, `UPDATE app.jobs SET terminal_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := age.Exec(ctx, `UPDATE app.coverage_route_job_contexts SET timeout_retry_count=CASE job_id WHEN $1 THEN 2 WHEN $2 THEN 7 ELSE 15 END
		WHERE job_id=ANY($3::uuid[])`, failed[0], failed[1], failed); err != nil {
		t.Fatal(err)
	}
	if err := age.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	pipelineConfigure(t, db, currentSize)
	pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[originalSize:originalSize+currentSize+tail], "created")
	fresh := read(currentSize, false)
	if len(fresh) != currentSize {
		t.Fatalf("fresh full batch=%d want=%d", len(fresh), currentSize)
	}
	payload, err := json.Marshal(pipelineReady(fresh, "map_data_ready"))
	if err != nil {
		t.Fatal(err)
	}
	apply := func() error {
		tx, err := db.worker.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, f.account.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, pipelineApplySQL, f.account, sourceJob, "pipeline-test", sourceLease, nil, payload); err != nil {
			return err
		}
		if err := pipelineSealPreparedSQL(ctx, tx, f.account, sourceJob, "pipeline-test", sourceLease, nil); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	role := db.api
	if mode == "automatic" {
		role = db.coverage
	}
	retryTx := beginAccount(t, ctx, role, f.account)
	defer retryTx.Rollback(ctx)
	var retry uuid.UUID
	var retryCount int
	if mode == "automatic" {
		err = retryTx.QueryRow(ctx, `SELECT * FROM app.retry_next_coverage_update(1,$1)`, database.SupportedSchemaVersion).Scan(&retry, &retryCount)
	} else {
		err = retryTx.QueryRow(ctx, `SELECT * FROM app.retry_coverage_update($1,$2,100)`, root, requester).Scan(&retry, &retryCount)
	}
	if err != nil || retryCount != 3 {
		t.Fatalf("%s retry count=%d: %v", mode, retryCount, err)
	}
	// The constructor keeps its account lock until commit. A real concurrent
	// ingest apply must fail atomically, then succeed after that retry is visible.
	result := make(chan error, 1)
	go func() { result <- apply() }()
	select {
	case err := <-result:
		var locked *pgconn.PgError
		if !errors.As(err, &locked) || locked.Code != "55P03" {
			t.Fatalf("concurrent apply ignored retry account fence: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent apply waited while the retry constructor held its account fence")
	}
	check(`SELECT NOT EXISTS(SELECT 1 FROM app.ingest_coverage_handoffs WHERE account_id=$1
		AND workout_id=ANY($2::uuid[]) AND acknowledged_at IS NOT NULL)`, f.account, f.workouts[originalSize:])
	if err := retryTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { result <- apply() }()
	if err := <-result; err != nil {
		t.Fatal("fresh work after retry commit:", err)
	}
	check(`SELECT retry.parent_job_id IS NULL AND retry.retry_of_job_id=$2 AND retry.status='queued'
		AND context.parent_batch_size=$3 AND progress.routes_total=3
		AND (context.region_id,context.target_osm_generation,context.rules_version,context.sampling_version,
		     context.path_policy_version,context.minimum_traversal_meters,context.matcher_contract_version)
		 IS NOT DISTINCT FROM (original.region_id,original.target_osm_generation,original.rules_version,original.sampling_version,
		     original.path_policy_version,original.minimum_traversal_meters,original.matcher_contract_version)
		AND context.target_work_revision=original.target_work_revision+1
		AND (SELECT count(*)=3 AND count(DISTINCT retry_of_job_id)=3 FROM app.jobs WHERE parent_job_id=retry.id)
		FROM app.jobs retry JOIN app.coverage_job_contexts context ON context.job_id=retry.id
		JOIN app.coverage_job_contexts original ON original.job_id=retry.retry_of_job_id
		JOIN app.coverage_job_progress progress ON progress.job_id=retry.id WHERE retry.id=$1`, retry, root, originalSize)
	check(`SELECT NOT EXISTS(SELECT 1 FROM app.jobs child
		JOIN app.coverage_route_job_contexts context ON context.job_id=child.id
		LEFT JOIN app.jobs prior ON prior.id=child.retry_of_job_id
		LEFT JOIN app.coverage_route_job_contexts prior_context ON prior_context.job_id=prior.id
		WHERE child.parent_job_id=$1 AND (prior.parent_job_id IS DISTINCT FROM $2 OR prior.status IS DISTINCT FROM 'failed'
		 OR context.workout_id IS DISTINCT FROM prior_context.workout_id
		 OR context.route_input_revision IS DISTINCT FROM prior_context.route_input_revision
		 OR context.route_input_sha256 IS DISTINCT FROM prior_context.route_input_sha256
		 OR NOT app.coverage_generation_vectors_equal(context.target_generations,prior_context.target_generations)
		 OR context.timeout_retry_count IS DISTINCT FROM CASE WHEN prior.failure_code='coverage-route-timeout'
		     THEN LEAST(prior_context.timeout_retry_count+1,16) ELSE 0 END))`, retry, root)
	used := 0
	if mode == "automatic" {
		used = 1
	}
	check(`SELECT root.automatic_attempts_used=$3 AND root.enrolled_at IS NOT NULL AND attempt.predecessor_job_id=$1
		AND attempt.origin=$4 AND attempt.automatic_ordinal IS NOT DISTINCT FROM CASE WHEN $4='automatic' THEN 1 END
		FROM app.coverage_retry_roots root JOIN app.coverage_retry_attempts attempt ON attempt.root_job_id=root.root_job_id
		WHERE root.root_job_id=$1 AND attempt.job_id=$2`, root, retry, used, mode)
	check(`SELECT fresh.retry_of_job_id IS NULL AND fresh.parent_job_id IS NULL AND context.parent_batch_size=$3
		AND fresh.id<>$2 AND progress.routes_total=$3 FROM app.workout_coverage_states state
		JOIN app.jobs child ON child.id=state.target_job_id JOIN app.jobs fresh ON fresh.id=child.parent_job_id
		JOIN app.coverage_job_contexts context ON context.job_id=fresh.id JOIN app.coverage_job_progress progress ON progress.job_id=fresh.id
		WHERE state.account_id=$1 AND state.workout_id=$4`, f.account, retry, currentSize, fresh[0].workout)
	remaining := read(currentSize, true)
	if len(remaining) != tail {
		t.Fatalf("partial tail=%d want=%d", len(remaining), tail)
	}
	pipelineApply(t, db, f, sourceJob, sourceLease, remaining, "map_data_ready")
	if tail < currentSize {
		check(`SELECT count(*)=$2 FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.account, tail)
		check(`SELECT NOT EXISTS(SELECT 1 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=ANY($2::uuid[]) AND target_job_id IS NOT NULL)`, f.account, f.workouts[originalSize+currentSize:originalSize+currentSize+tail])
	}
	start := originalSize + currentSize + tail
	pipelineRecord(t, db, f, sourceJob, sourceLease, f.workouts[start:start+extra], "created")
	pipelineApply(t, db, f, sourceJob, sourceLease, read(currentSize, true), "map_data_ready")
	// The underfull builder, not a queued placeholder parent, retains its saved
	// capacity even when policy changes while the same visible source root runs.
	otherSize := 25
	if currentSize == 25 {
		otherSize = 10
	}
	pipelineConfigure(t, db, otherSize)
	last := f.workouts[len(f.workouts)-1]
	pipelineRecord(t, db, f, sourceJob, sourceLease, []uuid.UUID{last}, "created")
	pipelineApply(t, db, f, sourceJob, sourceLease, read(otherSize, true), "map_data_ready")
	check(`SELECT retry.status='queued' AND context.parent_batch_size=$3 AND progress.routes_total=3
		AND (SELECT count(*)=3 AND bool_and(retry_of_job_id=ANY($4::uuid[])) FROM app.jobs WHERE parent_job_id=retry.id)
		AND (SELECT status='running' FROM app.jobs WHERE id=$5)
		AND (SELECT automatic_attempts_used=$6 FROM app.coverage_retry_roots WHERE root_job_id=$2)
		FROM app.jobs retry JOIN app.coverage_job_contexts context ON context.job_id=retry.id
		JOIN app.coverage_job_progress progress ON progress.job_id=retry.id WHERE retry.id=$1`, retry, root, originalSize, failed, sourceJob, used)
	check(`SELECT NOT EXISTS(SELECT 1 FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id
		JOIN app.coverage_route_job_contexts context ON context.job_id=child.id WHERE child.account_id=$1
		AND ((context.workout_id=ANY($2::uuid[]) AND (parent.retry_of_job_id IS NOT NULL OR child.retry_of_job_id IS NOT NULL))
		 OR (parent.id=$3 AND NOT child.retry_of_job_id=ANY($4::uuid[]))))`, f.account, f.workouts[originalSize:], retry, failed)
	// No later attempt or fresh work may burn another automatic ordinal or retry
	// the original parent after its chained successor has been created.
	manual := beginAccount(t, ctx, db.api, f.account)
	_, err = manual.Exec(ctx, `SELECT * FROM app.retry_coverage_update($1,$2,100)`, root, requester)
	bootstrapSQLState(t, err, "55000")
	_ = manual.Rollback(ctx)
	var repeats int
	if err := db.coverage.QueryRow(ctx, `SELECT count(*) FROM app.retry_next_coverage_update(1,$1)`, database.SupportedSchemaVersion).Scan(&repeats); err != nil || repeats != 0 {
		t.Fatalf("open retry was duplicated or budget bypassed: %d %v", repeats, err)
	}
	pipelineTerminalRecovery(t, db, f, sourceJob, sourceLease)
	var partial uuid.UUID
	if err := db.migration.QueryRow(ctx, `SELECT child.parent_job_id FROM app.workout_coverage_states state JOIN app.jobs child ON child.id=state.target_job_id WHERE state.account_id=$1 AND state.workout_id=$2`, f.account, remaining[0].workout).Scan(&partial); err != nil {
		t.Fatal(err)
	}
	if currentSize > 1 {
		check(`SELECT parent.id=$3 AND parent.retry_of_job_id IS NULL AND context.parent_batch_size=$4
		 AND progress.routes_total=$5 AND (SELECT count(*)=$5 FROM app.jobs WHERE parent_job_id=parent.id)
		 FROM app.workout_coverage_states state JOIN app.jobs child ON child.id=state.target_job_id
		 JOIN app.jobs parent ON parent.id=child.parent_job_id JOIN app.coverage_job_contexts context ON context.job_id=parent.id
		 JOIN app.coverage_job_progress progress ON progress.job_id=parent.id WHERE state.account_id=$1 AND state.workout_id=$2`, f.account, last, partial, currentSize, tail+extra+1)
	} else {
		check(`SELECT parent.id<>$3 AND parent.id<>$4 AND parent.retry_of_job_id IS NULL AND context.parent_batch_size=$5
		 FROM app.workout_coverage_states state JOIN app.jobs child ON child.id=state.target_job_id
		 JOIN app.jobs parent ON parent.id=child.parent_job_id JOIN app.coverage_job_contexts context ON context.job_id=parent.id
		 WHERE state.account_id=$1 AND state.workout_id=$2`, f.account, last, retry, partial, otherSize)
	}
	// Finish all newly queued work through the real worker helpers so subsequent
	// cases' global claim/automatic-discovery paths remain isolated.
	for range len(f.workouts) - originalSize + 3 {
		finish(claim(), "")
	}
	check(`SELECT status='succeeded' AND retry_of_job_id=$2 FROM app.jobs WHERE id=$1`, retry, root)
}
