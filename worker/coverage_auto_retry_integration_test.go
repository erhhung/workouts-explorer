package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type coverageAutoRetryFixture struct {
	t                          *testing.T
	ctx                        context.Context
	admin, api, worker         *pgxpool.Pool
	account, principal, parent uuid.UUID
	children, workouts         []uuid.UUID
}

// No application/OSM URL fallbacks, source files, profiles or network services.
// This suite requires an empty, explicitly named loopback scratch database.
func TestCoverageAutoRetryPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_AUTO_RETRY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_AUTO_RETRY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable automatic-retry database is required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.Database, "workouts_coverage_auto_retry_test_") ||
		(cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") {
		t.Fatal("automatic retries require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin := openWorkerTestPool(t, ctx, dsn)
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("automatic-retry scratch database must be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*cfg)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, migrationfixture.RouteCounts); err != nil {
		t.Fatal(err)
	}
	rolePool := func(role string) *pgxpool.Pool {
		config, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["role"] = role
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	api, workerDB := rolePool("workouts_api"), rolePool("workouts_coverage_worker")
	runtimePools := []*pgxpool.Pool{api, rolePool("workouts_worker"), workerDB}
	for _, pool := range runtimePools {
		if database.Ready(ctx, pool) {
			t.Fatal("incomplete route-counter checkpoint must not be current-runtime ready")
		}
	}
	newFixture := func(t *testing.T, n int) *coverageAutoRetryFixture {
		return newCoverageAutoRetryFixture(t, ctx, admin, api, workerDB, n)
	}
	historical := newFixture(t, 1)
	historical.finish(0, "coverage-route-timeout")
	historical.age()
	const counterABIQuery = `SELECT md5(string_agg(pg_get_functiondef(oid)||COALESCE(proacl::text,'')||proowner::text,E'\n' ORDER BY oid))
		FROM pg_proc WHERE pronamespace='app'::regnamespace AND proname IN ('record_ingest_progress','refresh_ingest_route_counts')`
	var counterABIBefore, counterABIAfter string
	if err := admin.QueryRow(ctx, counterABIQuery).Scan(&counterABIBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO app.job_progress(job_id,account_id,workouts_created,workouts_updated,workouts_created_with_route,workouts_updated_with_route)
		SELECT id,account_id,2,3,1,2 FROM app.jobs WHERE account_id=$1 AND kind='manual_ingest_source'`, historical.account); err != nil {
		t.Fatal(err)
	}
	testCoverageRetryMigrationRollback(t, ctx, admin, sqlDB, historical)
	if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, migrationfixture.AutoRetries); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.schema_metadata WHERE schema_version=26 AND minimum_runtime_version=26`)
	assertCountQuery(t, admin, 0, `SELECT count(*) FROM app.coverage_retry_roots`)
	assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.job_progress WHERE account_id=$1 AND workouts_created=2 AND workouts_updated=3 AND workouts_created_with_route=1 AND workouts_updated_with_route=2`, historical.account)
	if err := admin.QueryRow(ctx, counterABIQuery).Scan(&counterABIAfter); err != nil || counterABIBefore != counterABIAfter {
		t.Fatalf("automatic retries changed counter definitions/ownership/ACLs: %v", err)
	}
	var checkpointRetries int
	if err := workerDB.QueryRow(ctx, `SELECT count(*) FROM app.retry_next_coverage_update(1,26)`).Scan(&checkpointRetries); err != nil || checkpointRetries != 0 {
		t.Fatalf("runtime26 checkpoint ABI enrolled historical failures: rows=%d err=%v", checkpointRetries, err)
	}
	if err := migrationfixture.FinishCanonical27(ctx, sqlDB, migrations.Files, migrationfixture.AutoRetries); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.schema_metadata WHERE schema_version=27 AND minimum_runtime_version=26 AND migration_baseline=27`)
	for _, pool := range runtimePools {
		if database.Ready(ctx, pool) {
			t.Fatal("canonical27 alone must not satisfy pipeline readiness")
		}
	}
	if _, err := provider.UpTo(ctx, database.SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	for _, pool := range runtimePools {
		if !database.Ready(ctx, pool) {
			t.Fatal("current retry runtime role is not ready")
		}
	}
	historical.automatic(20, false)
	testCoverageRetryReconciliation(t, historical, newFixture)

	t.Run("narrow ACL and argument contracts", func(t *testing.T) {
		var narrow bool
		if err := admin.QueryRow(ctx, `SELECT
			has_function_privilege('workouts_coverage_worker','app.retry_next_coverage_update(integer,integer)','EXECUTE')
			AND NOT has_function_privilege('workouts_api','app.retry_next_coverage_update(integer,integer)','EXECUTE')
			AND NOT has_function_privilege('workouts_worker','app.retry_next_coverage_update(integer,integer)','EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker','app.construct_coverage_retry(uuid,uuid,integer,integer)','EXECUTE')
			AND NOT has_function_privilege('workouts_api','app.construct_coverage_retry(uuid,uuid,integer,integer)','EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker','app.current_coverage_matcher_contract()','EXECUTE')
			AND NOT has_function_privilege('workouts_worker','app.current_coverage_matcher_contract()','EXECUTE')
			AND NOT has_function_privilege('workouts_api','app.current_coverage_matcher_contract()','EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker','app.coverage_auto_retry_parent_eligible(uuid,uuid)','EXECUTE')
			AND NOT has_function_privilege('workouts_api','app.coverage_auto_retry_parent_eligible(uuid,uuid)','EXECUTE')
			AND NOT has_function_privilege('workouts_worker','app.coverage_auto_retry_parent_eligible(uuid,uuid)','EXECUTE')
			AND has_function_privilege('workouts_api','app.retry_coverage_update(uuid,uuid,integer)','EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker','app.retry_coverage_update(uuid,uuid,integer)','EXECUTE')
			AND NOT has_table_privilege('workouts_coverage_worker','app.coverage_retry_roots','UPDATE')
			AND NOT has_table_privilege('workouts_api','app.coverage_retry_attempts','INSERT')`).Scan(&narrow); err != nil || !narrow {
			t.Fatalf("narrow helper ACLs=%t err=%v", narrow, err)
		}
		for _, args := range [][2]int{{-1, database.SupportedSchemaVersion}, {21, database.SupportedSchemaVersion}, {1, 25}, {1, 26}} {
			_, err := workerDB.Exec(ctx, `SELECT * FROM app.retry_next_coverage_update($1,$2)`, args[0], args[1])
			var sqlErr *pgconn.PgError
			if !errors.As(err, &sqlErr) || (sqlErr.Code != "22023" && sqlErr.Code != "55000") {
				t.Fatalf("invalid helper args: %v", err)
			}
		}
	})

	t.Run("cooldown default budget restart raise lower and manual continuation", func(t *testing.T) {
		f := newFixture(t, 1)
		f.finish(0, "coverage-route-timeout")
		f.automatic(1, false) // Real wall-clock failure is not immediately due.
		f.age()
		f.automatic(0, false)
		first := f.automatic(1, true)
		f.checkRetry(first, f.parent, 1, 1)
		// A fresh runner repeats a committed call as after a lost response.
		count := 1
		runner := NewCoverageRunnerWithOptions(workerDB, nil, slog.Default(), CoverageRunnerOptions{AutoRetryCount: &count})
		if worked, err := runner.retryCoverageOnce(ctx); err != nil || worked {
			t.Fatalf("lost-response replay: %t %v", worked, err)
		}
		f.adopt(first)
		f.finish(0, "coverage-route-timeout")
		f.age()
		f.automatic(1, false)
		second := f.automatic(2, true)
		f.checkRetry(second, first, 1, 2)
		f.automatic(0, false)
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='queued'`, second)
		f.adopt(second)
		f.finish(0, "coverage-osm-unavailable")
		f.age()
		f.automatic(1, false)
		f.automatic(2, false)
		manual, err := f.manual(second, f.principal)
		if err != nil || manual == uuid.Nil {
			t.Fatalf("manual after budget spent: %v", err)
		}
		f.checkRetry(manual, second, 1, 0)
		f.adopt(manual)
		f.finish(0, "coverage-route-timeout")
		f.age()
		f.automatic(2, false) // Manual descendants do not receive a new budget.
		third := f.automatic(3, true)
		f.checkRetry(third, manual, 1, 1)
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=3`, f.root())
		f.adopt(third)
		f.finish(0, "")
	})

	t.Run("partial parent retries failed children only and isolates coalescing", func(t *testing.T) {
		f := newFixture(t, 2)
		f.finish(0, "coverage-route-timeout")
		f.finish(1, "")
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='partially_succeeded'`, f.parent)
		f.age()
		retry := f.automatic(1, true)
		f.checkRetry(retry, f.parent, 1, 1)
		// Same account/contract/region: ordinary enqueue must get a fresh parent.
		tx := beginWorkerTestAccount(t, admin, f.account)
		var normal uuid.UUID
		var routes int
		var reused bool
		err := tx.QueryRow(ctx, `SELECT * FROM app.enqueue_adopted_coverage_update($1,$2,'geofabrik:test',1,2,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,
			jsonb_build_array(jsonb_build_object('jobId',$3::uuid,'workoutId',$4::uuid,'routeRevision',1,'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb)))`,
			f.account, uuid.New(), uuid.New(), f.workouts[1]).Scan(&normal, &routes, &reused)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if normal == retry || reused {
			t.Fatal("normal enqueue reused retry parent")
		}
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.jobs WHERE parent_job_id=$1`, retry)
		f.adopt(retry)
		f.finish(0, "")
	})

	t.Run("rolled back creation spends nothing and restores caller context", func(t *testing.T) {
		f := newFixture(t, 1)
		f.finish(0, "coverage-route-timeout")
		f.age()
		tx, err := workerDB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		caller := uuid.New().String()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true),set_config('app.principal_id',$1,true)`, caller); err != nil {
			t.Fatal(err)
		}
		var rolledBack uuid.UUID
		var routes int
		if err := tx.QueryRow(ctx, `SELECT * FROM app.retry_next_coverage_update(1,$1)`, database.SupportedSchemaVersion).Scan(&rolledBack, &routes); err != nil {
			t.Fatal(err)
		}
		var accountContext, principalContext string
		if err := tx.QueryRow(ctx, `SELECT current_setting('app.account_id'),current_setting('app.principal_id')`).Scan(&accountContext, &principalContext); err != nil || accountContext != caller || principalContext != caller {
			t.Fatalf("caller context changed: account=%s principal=%s err=%v", accountContext, principalContext, err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertCountQuery(t, admin, 0, `SELECT count(*) FROM app.jobs WHERE id=$1`, rolledBack)
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=0`, f.parent)
		retry := f.automatic(1, true)
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, f.parent)
		dummy := uuid.New()
		if _, err := admin.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'coverage_update',20)`, dummy, f.account); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO app.coverage_retry_attempts(job_id,root_job_id,predecessor_job_id,origin) VALUES($1,$2,$2,'manual')`, []any{dummy, f.parent}},
			{`INSERT INTO app.coverage_retry_attempts(job_id,root_job_id,predecessor_job_id,origin,automatic_ordinal) VALUES($1,$2,$1,'automatic',1)`, []any{dummy, f.parent}},
		} {
			_, err := admin.Exec(ctx, statement.sql, statement.args...)
			var sqlErr *pgconn.PgError
			if !errors.As(err, &sqlErr) || sqlErr.Code != "23505" {
				t.Fatalf("durable uniqueness constraint not enforced: %v", err)
			}
		}
		f.adopt(retry)
		f.finish(0, "")
	})

	t.Run("twenty additional attempts cannot renew their own budget", func(t *testing.T) {
		f := newFixture(t, 1)
		root := f.parent
		for ordinal := 1; ordinal <= 20; ordinal++ {
			f.finish(0, "coverage-route-timeout")
			f.age()
			retry := f.automatic(20, true)
			f.checkRetry(retry, f.parent, 1, min(ordinal, 16))
			assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.coverage_retry_attempts WHERE job_id=$1 AND root_job_id=$2 AND origin='automatic' AND automatic_ordinal=$3`, retry, root, ordinal)
			f.adopt(retry)
		}
		f.finish(0, "coverage-route-timeout")
		f.age()
		f.automatic(20, false)
		assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=20`, root)
		retry, err := f.manual(f.parent, f.principal)
		if err != nil || retry == uuid.Nil {
			t.Fatalf("manual continuation after twenty automatic attempts: %s %v", retry, err)
		}
		f.adopt(retry)
		f.finish(0, "")
	})

	t.Run("locked target does not block another root or revive superseded state", func(t *testing.T) {
		blocked := newFixture(t, 1)
		blocked.finish(0, "coverage-route-timeout")
		blocked.age()
		other := newFixture(t, 1)
		other.finish(0, "coverage-route-timeout")
		other.age()
		tx := beginWorkerTestAccount(t, admin, blocked.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT 1 FROM app.workouts WHERE id=$1 FOR UPDATE`, blocked.workouts[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.workout_coverage_states SET processing_state='stale',target_job_id=NULL WHERE workout_id=$1`, blocked.workouts[0]); err != nil {
			t.Fatal(err)
		}
		_, err := blocked.manual(blocked.parent, blocked.principal)
		var sqlErr *pgconn.PgError
		if !errors.As(err, &sqlErr) || sqlErr.Code != "55000" {
			t.Fatalf("manual lock contention must retain the API conflict contract: %v", err)
		}
		retry := other.automatic(1, true)
		other.checkRetry(retry, other.parent, 1, 1)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertCountQuery(t, admin, 0, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, blocked.parent)
		blocked.automatic(20, false)
		other.adopt(retry)
		other.finish(0, "")
	})

	t.Run("real user cancellation blocks automatic but permits explicit manual retry", func(t *testing.T) {
		f := newFixture(t, 2)
		f.finish(0, "coverage-route-timeout")
		tx := beginWorkerTestAccount(t, api, f.account)
		defer tx.Rollback(ctx)
		var cancelled bool
		if err := tx.QueryRow(ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, f.parent, f.principal).Scan(&cancelled); err != nil || !cancelled {
			t.Fatalf("user cancellation: %t %v", cancelled, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		f.age()
		f.automatic(20, false)
		if retry, err := f.manual(f.parent, f.principal); err != nil || retry == uuid.Nil {
			t.Fatalf("explicit retry of failed and cancelled routes refused: %s %v", retry, err)
		} else {
			assertCountQuery(t, admin, 2, `SELECT count(*) FROM app.jobs WHERE parent_job_id=$1`, retry)
		}
	})

	for _, exclusion := range []string{"current", "superseded", "changed input", "deleting workout", "inactive account", "child cancellation", "parent cancellation", "disabled principal"} {
		t.Run("safety/"+exclusion, func(t *testing.T) {
			f := newFixture(t, 1)
			f.finish(0, "coverage-route-timeout")
			f.age()
			sql := map[string]string{
				"current":             `UPDATE app.workout_coverage_states SET processing_state='current' WHERE account_id=$1`,
				"superseded":          `UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_state='stale' WHERE account_id=$1`,
				"changed input":       `UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE account_id=$1`,
				"deleting workout":    `UPDATE app.workouts SET deletion_requested_at=clock_timestamp(),deletion_target_id=gen_random_uuid() WHERE account_id=$1`,
				"inactive account":    `UPDATE app.accounts SET state='deleting' WHERE id=$1`,
				"child cancellation":  `UPDATE app.jobs SET cancel_requested_at=clock_timestamp(),cancel_requested_by=gen_random_uuid() WHERE account_id=$1 AND kind='coverage_update_route'`,
				"parent cancellation": `UPDATE app.jobs SET cancel_requested_at=clock_timestamp(),cancel_requested_by=gen_random_uuid() WHERE account_id=$1 AND kind='coverage_update'`,
				"disabled principal":  `UPDATE app.authentication_principals SET disabled_at=clock_timestamp() WHERE id IN (SELECT principal_id FROM app.users WHERE account_id=$1)`,
			}[exclusion]
			f.synthetic(sql, f.account)
			if exclusion == "disabled principal" {
				if _, err := f.manual(f.parent, f.principal); err == nil {
					t.Fatal("disabled real principal authorized manual retry")
				}
				// Automation has no principal requirement and does not impersonate one.
				retry := f.automatic(1, true)
				f.adopt(retry)
				f.finish(0, "")
				return
			}
			f.automatic(20, false)
			if exclusion == "child cancellation" || exclusion == "parent cancellation" {
				retry, err := f.manual(f.parent, f.principal)
				if err != nil || retry == uuid.Nil {
					t.Fatalf("explicit manual retry cannot reverse cancellation: %s %v", retry, err)
				}
				f.checkRetry(retry, f.parent, 1, 1)
				return
			}
			if retry, err := f.manual(f.parent, f.principal); err != nil || retry != uuid.Nil {
				t.Fatalf("manual safety exclusion: %s %v", retry, err)
			}
		})
	}

	t.Run("manual authentication and cross account", func(t *testing.T) {
		f := newFixture(t, 1)
		f.finish(0, "coverage-route-timeout")
		f.age()
		if _, err := f.manual(f.parent, uuid.New()); err == nil {
			t.Fatal("forged requester accepted")
		}
		other := newFixture(t, 1)
		if _, err := other.manual(f.parent, other.principal); err == nil {
			t.Fatal("cross-account parent accepted")
		}
		retry := f.automatic(1, true)
		f.adopt(retry)
		f.finish(0, "")
	})

	t.Run("manual automatic and automatic automatic races", func(t *testing.T) {
		for iteration := range 12 {
			f := newFixture(t, 1)
			f.finish(0, "coverage-route-timeout")
			f.age()
			start := make(chan struct{})
			results := make(chan error, 2)
			var group sync.WaitGroup
			for caller := range 2 {
				group.Go(func() {
					<-start
					var err error
					if caller == 0 && iteration%2 == 0 {
						_, err = f.manual(f.parent, f.principal)
					} else {
						var id uuid.UUID
						var n int
						err = workerDB.QueryRow(ctx, `SELECT * FROM app.retry_next_coverage_update(1,$1)`, database.SupportedSchemaVersion).Scan(&id, &n)
					}
					results <- err
				})
			}
			close(start)
			group.Wait()
			close(results)
			for err := range results {
				var sqlErr *pgconn.PgError
				if err != nil && !errors.Is(err, pgx.ErrNoRows) && !(errors.As(err, &sqlErr) && (sqlErr.Code == "55P03" || sqlErr.Code == "55000")) {
					t.Fatalf("unexpected race error: %v", err)
				}
			}
			assertCountQuery(t, admin, 1, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent)
			var retry uuid.UUID
			if err := admin.QueryRow(ctx, `SELECT id FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent).Scan(&retry); err != nil {
				t.Fatal(err)
			}
			f.adopt(retry)
			f.finish(0, "")
		}
	})
	testCoverageAutoRetryFailurePolicy(t, newFixture)
	testCoverageAutoRetryEligibleCohort(t, newFixture)
	testCoverageRetryManualCancellation(t, newFixture)
}

func newCoverageAutoRetryFixture(t *testing.T, ctx context.Context, admin, api, worker *pgxpool.Pool, n int) *coverageAutoRetryFixture {
	t.Helper()
	source := insertIngestSource(t, admin, "/synthetic/unused")
	f := &coverageAutoRetryFixture{t: t, ctx: ctx, admin: admin, api: api, worker: worker, account: source.accountID, principal: uuid.New(), parent: uuid.New()}
	tx := beginWorkerTestAccount(t, admin, f.account)
	defer tx.Rollback(ctx)
	name := "retry-" + f.principal.String()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'synthetic-only','Synthetic Retry')`, []any{f.principal, name, name + "@example.test"}},
		{`INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, []any{f.principal, f.account}},
		{`SET LOCAL session_replication_role='replica'`, nil},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	file, activity, ingestParent, ingestChild := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// Only synthetic domain setup bypasses ingest fences. Retry construction,
	// claims, failures, parent derivation and enrollment run with real triggers.
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, ingestParent, f.account); err != nil {
		t.Fatal(err)
	}
	// Reconciliation models committed imports, not an active producer's partial batch.
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters,status,terminal_at)
		VALUES($1,$2,$3,'manual_ingest_source',80,jsonb_build_object('sourceId',$4::uuid,'generation',1,'mode','incremental'),'succeeded',clock_timestamp())`, ingestChild, ingestParent, f.account, source.sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state) VALUES($1,$2,$3,$4,'synthetic.json',1,'discovered')`, file, f.account, source.sourceID, ingestChild); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, activity, f.account); err != nil {
		t.Fatal(err)
	}
	for index := range n {
		workout := uuid.New()
		f.workouts = append(f.workouts, workout)
		f.children = append(f.children, uuid.New())
		if _, err := tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
		VALUES($1,$2,$3,$4,$5,$6,decode(repeat('01',32),'hex'),'Walking','2026-08-01','2026-08-01',0)`, workout, f.account, source.sourceID, file, activity, fmt.Sprint(index)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
		VALUES($1,$2,1,decode(repeat('01',32),'hex'),'map_data_ready',clock_timestamp())`, f.account, workout); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}
	var targets string
	for i, workout := range f.workouts {
		if i > 0 {
			targets += ","
		}
		targets += fmt.Sprintf(`{"jobId":%q,"workoutId":%q,"routeRevision":1,"generations":[{"regionId":"geofabrik:test","generation":1}]}`, f.children[i], workout)
	}
	var parent uuid.UUID
	var routes int
	var reused bool
	if err := tx.QueryRow(ctx, `SELECT * FROM app.enqueue_coverage_update($1,$2,'geofabrik:test',1,1,
		'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,$3::jsonb)`, f.account, f.parent, "["+targets+"]").Scan(&parent, &routes, &reused); err != nil {
		t.Fatal(err)
	}
	if parent != f.parent || routes != n || reused {
		t.Fatal("unexpected fixture enqueue")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *coverageAutoRetryFixture) finish(index int, code string) {
	f.t.Helper()
	lease := uuid.New()
	tx := beginWorkerTestAccount(f.t, f.admin, f.account)
	var ok bool
	if err := tx.QueryRow(f.ctx, `SELECT app.claim_job($1,'synthetic-retry-worker',$2,interval '7 minutes')`, f.children[index], lease).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("claim: %t %v", ok, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	tx = beginWorkerTestAccount(f.t, f.worker, f.account)
	defer tx.Rollback(f.ctx)
	query := `SELECT app.fail_coverage_route($1,'synthetic-retry-worker',$2,1,$3,'Synthetic matching failure.')`
	args := []any{f.children[index], lease, code}
	if code == "" {
		query = `SELECT app.persist_coverage_route($1,'synthetic-retry-worker',$2,1,'[{"regionId":"geofabrik:test","generation":1}]','[]') IN ('applied','no_evidence')`
		args = args[:2]
	}
	if err := tx.QueryRow(f.ctx, query, args...).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("finish: %t %v", ok, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *coverageAutoRetryFixture) synthetic(sql string, args ...any) {
	f.t.Helper()
	tx := beginWorkerTestAccount(f.t, f.admin, f.account)
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		f.t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *coverageAutoRetryFixture) age() {
	f.synthetic(`UPDATE app.jobs SET terminal_at=clock_timestamp()-interval '31 seconds' WHERE id=$1`, f.parent)
}

func (f *coverageAutoRetryFixture) automatic(limit int, want bool) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	var routes int
	err := f.worker.QueryRow(f.ctx, `SELECT * FROM app.retry_next_coverage_update($1,$2)`, limit, database.SupportedSchemaVersion).Scan(&id, &routes)
	if !want && errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil
	}
	if err != nil || !want || routes < 1 {
		f.t.Fatalf("automatic retry limit=%d want=%t id=%s routes=%d err=%v", limit, want, id, routes, err)
	}
	return id
}

func (f *coverageAutoRetryFixture) manual(parent, requester uuid.UUID) (uuid.UUID, error) {
	tx, err := f.api.Begin(f.ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, `SELECT set_config('app.account_id',$1,true)`, f.account.String()); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	var n int
	err = tx.QueryRow(f.ctx, `SELECT * FROM app.retry_coverage_update($1,$2,100)`, parent, requester).Scan(&id, &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, tx.Commit(f.ctx)
	}
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(f.ctx)
}

func (f *coverageAutoRetryFixture) checkRetry(retry, prior uuid.UUID, n, timeout int) {
	f.t.Helper()
	assertCountQuery(f.t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND retry_of_job_id=$2 AND status='queued'`, retry, prior)
	assertCountQuery(f.t, f.admin, n, `SELECT count(*) FROM app.jobs child JOIN app.coverage_route_job_contexts context ON context.job_id=child.id
		JOIN app.jobs predecessor ON predecessor.id=child.retry_of_job_id WHERE child.parent_job_id=$1 AND predecessor.parent_job_id=$2 AND context.timeout_retry_count=$3`, retry, prior, timeout)
}

func (f *coverageAutoRetryFixture) adopt(parent uuid.UUID) {
	f.t.Helper()
	f.parent = parent
	f.children = nil
	rows, err := f.admin.Query(f.ctx, `SELECT id FROM app.jobs WHERE parent_job_id=$1 ORDER BY id`, parent)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		f.children = append(f.children, id)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *coverageAutoRetryFixture) root() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := f.admin.QueryRow(f.ctx, `SELECT root_job_id FROM app.coverage_retry_attempts WHERE job_id=$1`, f.parent).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}
