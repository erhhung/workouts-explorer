package migrations_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type readerReservation struct {
	account, job, parent, slot, lease uuid.UUID
	owner                             string
	pool                              *pgxpool.Pool
}

// No application URL fallback: only an explicitly disposable, initially empty,
// loopback PostGIS scratch database with preprovisioned standard test roles.
func TestCoordinatedCoverageReadersIntegration(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_READERS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_READERS_TEST_DISPOSABLE") != "1" {
		t.Skip("PROJECT_COVERAGE_READERS_TEST_DATABASE_URL and PROJECT_COVERAGE_READERS_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch reader database configuration")
	}
	if !strings.HasPrefix(config.Database, "workouts_coverage_readers_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("reader tests require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect to scratch reader database")
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT current_database()=$1 AND to_regnamespace('app') IS NULL
		AND to_regclass('public.goose_db_version') IS NULL`, config.Database).Scan(&empty); err != nil || !empty {
		t.Fatal("scratch reader database must be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	// Reproduce the restored live-20 formatting before the 22 -> 23 upgrade.
	// Only known code tokens are restyled; never collapse an entire SQL body.
	compactSecurity := map[string]string{}
	if os.Getenv("PROJECT_COVERAGE_READERS_TEST_COMPACT_22") == "1" {
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, signature := range []string{
			"app.claim_next_coverage_route(text,uuid,interval,integer)",
			"app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)",
			"app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)",
		} {
			var definition, before, after string
			if err := tx.QueryRow(ctx, `SELECT pg_get_functiondef(p.oid),jsonb_build_object('owner',p.proowner,'acl',p.proacl::text,'config',p.proconfig)::text
				FROM pg_proc p WHERE p.oid=$1::regprocedure`, signature).Scan(&definition, &before); err != nil {
				t.Fatal(err)
			}
			from := "IF runtime_version < 15 THEN"
			to := "iF runtime_version<15 THEN"
			if !strings.HasPrefix(signature, "app.claim_") {
				from = "SELECT job.account_id, context.workout_id, context.route_input_revision, context.route_input_sha256,"
				to = "sElEcT job.account_id,context.workout_id,context.route_input_revision,context.route_input_sha256,"
				value := "NULL"
				if strings.HasPrefix(signature, "app.finish_") {
					value = "false"
				}
				rejection := "IF NOT FOUND THEN RETURN " + value + "; END IF;"
				if strings.Count(definition, rejection) != 1 {
					t.Fatal("unexpected scratch rejection fixture")
				}
				definition = strings.Replace(definition, rejection, "if not found then return "+value+";end if;", 1)
			}
			if strings.Count(definition, from) != 1 {
				t.Fatal("unexpected scratch code-token fixture")
			}
			definition = strings.Replace(definition, from, "PERFORM 'literal  Keep\tTHIS  exactly';\n"+to, 1)
			if _, err := tx.Exec(ctx, definition); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow(ctx, `SELECT jsonb_build_object('owner',proowner,'acl',proacl::text,'config',proconfig)::text FROM pg_proc WHERE oid=$1::regprocedure`, signature).Scan(&after); err != nil || before != after {
				t.Fatalf("scratch restyling changed function security: %s err=%v", signature, err)
			}
			compactSecurity[signature] = before
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Prove 023 does not replace approved runtime limits with its new default.
	if _, err := admin.Exec(ctx, `UPDATE app.matcher_slot_limits SET global_limit=2,account_limit=2,region_limit=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PROJECT_COVERAGE_READERS_TEST_COMPACT_22") == "1" {
		for _, signature := range []string{
			"app.claim_next_coverage_route(text,uuid,interval,integer)",
			"app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)",
			"app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)",
		} {
			var preserved bool
			if err := admin.QueryRow(ctx, `SELECT position($2 IN pg_get_functiondef($1::regprocedure))>0`, signature, "'literal  Keep\tTHIS  exactly'").Scan(&preserved); err != nil || !preserved {
				t.Fatalf("upgrade changed compact fixture literal bytes: %s err=%v", signature, err)
			}
			var security string
			if err := admin.QueryRow(ctx, `SELECT jsonb_build_object('owner',proowner,'acl',proacl::text,'config',proconfig)::text FROM pg_proc WHERE oid=$1::regprocedure`, signature).Scan(&security); err != nil || security != compactSecurity[signature] {
				t.Fatalf("upgrade changed old function owner, ACL, or search path: %s err=%v", signature, err)
			}
		}
	}
	pool := func(role string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid scratch reader pool configuration")
		}
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
		cfg.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal("create scratch reader pool")
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"),
		worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	cluster := strings.Repeat("c", 64)
	keys := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	reset := func(t *testing.T, limit int) {
		t.Helper()
		if _, err := admin.Exec(ctx, `DELETE FROM app.matcher_slots`); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `UPDATE app.matcher_slot_limits SET global_limit=$1,account_limit=$1,region_limit=$1,reader_limit=1`, limit); err != nil {
			t.Fatal(err)
		}
	}
	uncancelled, cancelled := false, true
	assertCancellation := func(t *testing.T, r readerReservation, want *bool) {
		t.Helper()
		tx := beginAccount(t, ctx, db.coverage, r.account)
		defer tx.Rollback(ctx)
		var result *bool
		if err := tx.QueryRow(ctx, `SELECT app.job_cancel_requested($1,$2,$3)`, r.job, r.owner, r.lease).Scan(&result); err != nil {
			t.Fatal(err)
		}
		if want == nil && result != nil || want != nil && (result == nil || *result != *want) {
			t.Fatalf("cancellation observation=%v want=%v", result, want)
		}
	}
	t.Run("MetadataACLsAndOldRuntimeFence", func(t *testing.T) {
		var schema, minimum, global, account, region, reader int
		if err := admin.QueryRow(ctx, `SELECT schema_version,minimum_runtime_version,global_limit,account_limit,region_limit,reader_limit
			FROM app.schema_metadata CROSS JOIN app.matcher_slot_limits`).Scan(&schema, &minimum, &global, &account, &region, &reader); err != nil || schema != 23 || minimum != 23 || global != 2 || account != 2 || region != 2 || reader != 1 {
			t.Fatalf("metadata/limits=%d/%d/%d/%d/%d/%d err=%v", schema, minimum, global, account, region, reader, err)
		}
		// This historical upgrade stops at 23; latest runtime readiness is tested separately.
		for _, signature := range []string{"app.assign_matcher_reader(uuid,text,uuid,text,text[])", "app.clear_matcher_reader(uuid,text,uuid)"} {
			var safe bool
			if err := admin.QueryRow(ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner'
				AND p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
				AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
				AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
				AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
				FROM pg_proc p WHERE p.oid=$1::regprocedure`, signature).Scan(&safe); err != nil || !safe {
				t.Fatalf("unsafe reader function %s: %t err=%v", signature, safe, err)
			}
		}
		var safeHeartbeat bool
		if err := admin.QueryRow(ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner'
			AND p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
			FROM pg_proc p WHERE p.oid='app.heartbeat_diagnostic_matcher_slot(uuid,text,uuid,interval)'::regprocedure`).Scan(&safeHeartbeat); err != nil || !safeHeartbeat {
			t.Fatalf("unsafe diagnostic heartbeat: %t err=%v", safeHeartbeat, err)
		}
		var safeCancellation bool
		if err := admin.QueryRow(ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner'
			AND p.prosecdef AND p.provolatile='s' AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
			FROM pg_proc p WHERE p.oid='app.job_cancel_requested(uuid,text,uuid)'::regprocedure`).Scan(&safeCancellation); err != nil || !safeCancellation {
			t.Fatalf("unsafe cancellation helper: %t err=%v", safeCancellation, err)
		}
		for _, signature := range []string{
			"app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)",
			"app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)",
		} {
			var parentFirst bool
			if err := admin.QueryRow(ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner' AND p.prosecdef
				AND position('FOR UPDATE OF parent' IN pg_get_functiondef(p.oid)) > 0
				AND position('FOR UPDATE OF parent' IN pg_get_functiondef(p.oid)) <
				    position('FOR UPDATE OF job, context' IN pg_get_functiondef(p.oid))
				FROM pg_proc p WHERE p.oid=$1::regprocedure`, signature).Scan(&parentFirst); err != nil || !parentFirst {
				t.Fatalf("completion must preserve security and lock parent before child: %s %t err=%v", signature, parentFirst, err)
			}
		}
		for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			for _, table := range []string{"app.matcher_slots", "app.matcher_slot_limits", "app.matcher_slot_guard"} {
				var exposed bool
				if err := admin.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'SELECT,INSERT,UPDATE,DELETE')
					OR has_any_column_privilege($1,$2,'SELECT,INSERT,UPDATE')`, role, table).Scan(&exposed); err != nil || exposed {
					t.Fatalf("direct table/column access %s %s: %t err=%v", role, table, exposed, err)
				}
			}
		}
		for _, version := range []int{20, 22} {
			tx := beginAccount(t, ctx, db.coverage, uuid.New())
			_, err := tx.Exec(ctx, `SELECT * FROM app.claim_next_coverage_route('old-runtime',$1,interval '1 minute',$2)`, uuid.New(), version)
			_ = tx.Rollback(ctx)
			bootstrapSQLState(t, err, "55000")
		}
		// Typed source claims keep their runtime-21 path and accept runtime 23.
		tx := beginAccount(t, ctx, db.worker, uuid.New())
		if _, err := tx.Exec(ctx, `SELECT * FROM app.claim_next_worker_job('typed-runtime',$1,interval '1 minute',$2)`, uuid.New(), database.SupportedSchemaVersion); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
	})
	t.Run("CancellationHelperValidStaleAndInvalidScope", func(t *testing.T) {
		reset(t, 2)
		r := newReaderProduction(t, db, "cancellation-helper-owner")
		foreign := newReaderProductionJob(t, db, "cancellation-helper-foreign-owner")
		assertCancellation(t, r, &uncancelled)
		for _, variant := range []string{"owner", "lease", "account", "missing"} {
			bad := r
			switch variant {
			case "owner":
				bad.owner = "another-worker"
			case "lease":
				bad.lease = uuid.New()
			case "account":
				bad.account = foreign.account
			case "missing":
				bad.job = uuid.New()
			}
			assertCancellation(t, bad, nil)
		}
		for _, args := range [][]any{
			{nil, r.owner, r.lease}, {r.job, nil, r.lease}, {r.job, "", r.lease},
			{r.job, strings.Repeat("w", 201), r.lease}, {r.job, r.owner, nil},
		} {
			tx := beginAccount(t, ctx, db.coverage, r.account)
			var result *bool
			if err := tx.QueryRow(ctx, `SELECT app.job_cancel_requested($1,$2,$3)`, args...).Scan(&result); err != nil || result != nil {
				t.Fatalf("invalid cancellation arguments returned authority: %v err=%v", result, err)
			}
			_ = tx.Rollback(ctx)
		}
		t.Run("NoAccountScope", func(t *testing.T) {
			tx := beginAccount(t, ctx, db.coverage, r.account)
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id','',true)`); err != nil {
				t.Fatal(err)
			}
			var result *bool
			if err := tx.QueryRow(ctx, `SELECT app.job_cancel_requested($1,$2,$3)`, r.job, r.owner, r.lease).Scan(&result); err != nil || result != nil {
				t.Fatalf("unscoped cancellation observation=%v err=%v", result, err)
			}
		})
		t.Run("ParentOutsideAccount", func(t *testing.T) {
			// Corrupt only lineage in a rollback-only privileged scratch transaction.
			// The actual running child owner/lease remains unchanged.
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE app.jobs SET parent_job_id=$2 WHERE id=$1`, r.job, foreign.parent); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_coverage_worker`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, r.account.String()); err != nil {
				t.Fatal(err)
			}
			var result *bool
			if err := tx.QueryRow(ctx, `SELECT app.job_cancel_requested($1,$2,$3)`, r.job, r.owner, r.lease).Scan(&result); err != nil || result != nil {
				t.Fatalf("foreign parent yielded cancellation authority: %v err=%v", result, err)
			}
		})
		assertCancellation(t, r, &uncancelled)
		if !callBool(t, ctx, r.pool, r.account, `SELECT app.fail_coverage_route($1,$2,$3,1,'coverage-route-timeout','Synthetic timeout.')`, r.job, r.owner, r.lease) {
			t.Fatal("could not make real terminal helper fixture")
		}
		assertCancellation(t, r, nil)
	})
	t.Run("DiagnosticHeartbeatPreservesReaderAfterRequestDeadline", func(t *testing.T) {
		reset(t, 2)
		r := newReaderDiagnostic(t, db, "diagnostic-heartbeat-owner", "3 seconds")
		if key := mustReaderAssign(t, ctx, r, cluster, keys[:1]); key == nil {
			t.Fatal("diagnostic reader assignment failed")
		}
		var before, after time.Time
		if err := admin.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, r.slot).Scan(&before); err != nil {
			t.Fatal(err)
		}
		requestCtx, requestCancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer requestCancel()
		renewCtx, renewCancel := context.WithTimeout(context.WithoutCancel(requestCtx), 2*time.Second)
		defer renewCancel()
		if !callBool(t, renewCtx, r.pool, r.account, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,interval '2 minutes')`, r.slot, r.owner, r.lease) {
			t.Fatal("request deadline incorrectly prevented physical-reservation renewal")
		}
		var actualCluster, actualBackend string
		if err := admin.QueryRow(ctx, `SELECT expires_at,reader_cluster_key,reader_backend_key FROM app.matcher_slots WHERE slot_token=$1`, r.slot).Scan(&after, &actualCluster, &actualBackend); err != nil || !after.After(before) || actualCluster != cluster || actualBackend != keys[0] {
			t.Fatalf("renewal changed reader or failed to extend expiry: before=%s after=%s cluster=%s backend=%s err=%v", before, after, actualCluster, actualBackend, err)
		}
		other := newReaderDiagnostic(t, db, "diagnostic-after-renewal", "2 minutes")
		if mustReaderAssign(t, ctx, other, cluster, keys[:1]) != nil {
			t.Fatal("renewed reservation disappeared from shared reader counts")
		}
		for _, duration := range []string{"1 second", "2 minutes"} {
			if !callBool(t, ctx, r.pool, r.account, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,$4::interval)`, r.slot, r.owner, r.lease, duration) {
				t.Fatalf("valid heartbeat boundary %s was denied", duration)
			}
		}
		assertReaderSlots(t, admin, ctx, 2, 1)
	})
	t.Run("DiagnosticHeartbeatScopeKindAccountAndDurationFences", func(t *testing.T) {
		reset(t, 3)
		r := newReaderDiagnostic(t, db, "heartbeat-fenced-owner", "2 minutes")
		foreign := newReaderDiagnostic(t, db, "heartbeat-foreign-owner", "2 minutes")
		production := newReaderProduction(t, db, "heartbeat-production-owner")
		for _, variant := range []readerReservation{
			{account: r.account, slot: r.slot, owner: "wrong-owner", lease: r.lease},
			{account: r.account, slot: r.slot, owner: r.owner, lease: uuid.New()},
			{account: foreign.account, slot: r.slot, owner: r.owner, lease: r.lease},
			{account: r.account, slot: uuid.New(), owner: r.owner, lease: r.lease},
			production,
		} {
			if callBool(t, ctx, db.api, variant.account, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,interval '2 minutes')`, variant.slot, variant.owner, variant.lease) {
				t.Fatal("diagnostic heartbeat accepted a foreign or production reservation")
			}
		}
		for _, duration := range []any{nil, "0 seconds", "0.5 seconds", "121 seconds"} {
			tx := beginAccount(t, ctx, r.pool, r.account)
			_, err := tx.Exec(ctx, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,$4::interval)`, r.slot, r.owner, r.lease, duration)
			_ = tx.Rollback(ctx)
			bootstrapSQLState(t, err, "22023")
		}
		if _, err := admin.Exec(ctx, `UPDATE app.accounts SET state='deleting' WHERE id=$1`, r.account); err != nil {
			t.Fatal(err)
		}
		if callBool(t, ctx, r.pool, r.account, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,interval '2 minutes')`, r.slot, r.owner, r.lease) {
			t.Fatal("deleting account renewed its physical reservation")
		}
		assertReaderSlots(t, admin, ctx, 3, 0)
	})
	t.Run("DiagnosticHeartbeatCannotResurrectAfterLockWait", func(t *testing.T) {
		for _, lock := range []string{"guard", "slot"} {
			t.Run(lock, func(t *testing.T) {
				reset(t, 2)
				r := newReaderDiagnostic(t, db, "heartbeat-expiry-owner", "3 seconds")
				if mustReaderAssign(t, ctx, r, cluster, keys[:1]) == nil {
					t.Fatal("diagnostic assignment failed")
				}
				blocker := beginAccount(t, ctx, db.migration, r.account)
				defer blocker.Rollback(ctx)
				query := `SELECT 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE`
				var args []any
				if lock == "slot" {
					query, args = `SELECT 1 FROM app.matcher_slots WHERE slot_token=$1 FOR UPDATE`, []any{r.slot}
				}
				if _, err := blocker.Exec(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
				tx := beginAccount(t, ctx, r.pool, r.account)
				defer tx.Rollback(ctx)
				var pid int
				if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				var renewed bool
				done := make(chan error, 1)
				go func() {
					done <- tx.QueryRow(ctx, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,interval '2 minutes')`, r.slot, r.owner, r.lease).Scan(&renewed)
				}()
				bootstrapWaitEvent(t, ctx, admin, pid, "transactionid")
				var expired bool
				for !expired {
					if err := admin.QueryRow(ctx, `SELECT expires_at < clock_timestamp() FROM app.matcher_slots WHERE slot_token=$1`, r.slot).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if !expired {
						time.Sleep(20 * time.Millisecond)
					}
				}
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil || renewed {
					t.Fatalf("expired post-%s heartbeat renewed=%t err=%v", lock, renewed, err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if callBool(t, ctx, r.pool, r.account, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,interval '2 minutes')`, r.slot, r.owner, r.lease) {
					t.Fatal("already expired diagnostic lease was resurrected")
				}
				fresh := newReaderDiagnostic(t, db, "heartbeat-capacity-recovered", "2 minutes")
				if mustReaderAssign(t, ctx, fresh, cluster, keys[:1]) == nil {
					t.Fatal("expired diagnostic reader capacity was not recovered")
				}
				assertReaderSlots(t, admin, ctx, 1, 1)
			})
		}
	})
	t.Run("ConcurrentDifferentAccountsProductionAndDiagnostic", func(t *testing.T) {
		reset(t, 2)
		production := newReaderProduction(t, db, "coverage-pod-one")
		diagnostic := newReaderDiagnostic(t, db, "api-pod-two", "2 minutes")
		start := make(chan struct{})
		type result struct {
			key *string
			err error
		}
		done := make(chan result, 2)
		for _, reservation := range []readerReservation{production, diagnostic} {
			go func(r readerReservation) {
				<-start
				key, err := readerAssign(ctx, r, cluster, keys)
				done <- result{key, err}
			}(reservation)
		}
		close(start)
		first, second := <-done, <-done
		if first.err != nil || second.err != nil || first.key == nil || second.key == nil || *first.key == *second.key {
			t.Fatalf("concurrent choices=%v/%v errors=%v/%v", first.key, second.key, first.err, second.err)
		}
		if chosen := mustReaderAssign(t, ctx, production, cluster, keys); chosen == nil {
			t.Fatal("reassignment counted its own reservation")
		}
		assertReaderSlots(t, admin, ctx, 2, 2)
		var before, after time.Time
		if err := admin.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, production.slot).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if !callBool(t, ctx, db.coverage, production.account, `SELECT app.heartbeat_job($1,$2,$3,interval '3 minutes')`, production.job, production.owner, production.lease) {
			t.Fatal("real lease heartbeat failed")
		}
		if err := admin.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, production.slot).Scan(&after); err != nil || !after.After(before) {
			t.Fatalf("heartbeat expiry=%s -> %s err=%v", before, after, err)
		}
		assertReaderSlots(t, admin, ctx, 2, 2)
	})
	t.Run("HardCapClusterScopeAndAtomicClearReassign", func(t *testing.T) {
		reset(t, 4)
		one := newReaderDiagnostic(t, db, "pod-one", "2 minutes")
		two := newReaderDiagnostic(t, db, "pod-two", "2 minutes")
		three := newReaderDiagnostic(t, db, "pod-three", "2 minutes")
		mustReaderAssign(t, ctx, one, cluster, keys[:1])
		if mustReaderAssign(t, ctx, two, cluster, keys[:1]) != nil {
			t.Fatal("second caller exceeded shared reader limit 1")
		}
		if mustReaderAssign(t, ctx, two, strings.Repeat("d", 64), keys[:1]) == nil {
			t.Fatal("another cluster incorrectly shared this cluster's cap")
		}
		mustReaderAssign(t, ctx, three, cluster, keys[1:])
		if mustReaderAssign(t, ctx, one, cluster, keys[1:]) != nil {
			t.Fatal("failed reassignment exceeded occupied reader cap")
		}
		var retained string
		if err := admin.QueryRow(ctx, `SELECT reader_backend_key FROM app.matcher_slots WHERE slot_token=$1`, one.slot).Scan(&retained); err != nil || retained != keys[0] {
			t.Fatal("failed reassignment discarded existing reservation")
		}
		for range 2 {
			if !callBool(t, ctx, db.api, one.account, `SELECT app.clear_matcher_reader($1,$2,$3)`, one.slot, one.owner, one.lease) {
				t.Fatal("valid clear was not idempotent")
			}
		}
		assertReaderSlots(t, admin, ctx, 3, 2)
		if mustReaderAssign(t, ctx, one, cluster, keys[:1]) == nil {
			t.Fatal("clear/reassign could not reuse its admission row")
		}
		assertReaderSlots(t, admin, ctx, 3, 3)
		if _, err := admin.Exec(ctx, `UPDATE app.matcher_slot_limits SET reader_limit=2`); err != nil {
			t.Fatal(err)
		}
		four := newReaderDiagnostic(t, db, "pod-four", "2 minutes")
		if mustReaderAssign(t, ctx, four, cluster, keys[:1]) == nil {
			t.Fatal("global reader limit update was not shared by callers")
		}
		if !callBool(t, ctx, db.api, one.account, `SELECT app.release_matcher_slot($1,$2,$3)`, one.slot, one.owner, one.lease) {
			t.Fatal("existing release signature did not delete full assigned row")
		}
		assertReaderSlots(t, admin, ctx, 3, 3)
	})
	t.Run("LeastActiveAmongReadersBelowTheCap", func(t *testing.T) {
		reset(t, 3)
		if _, err := admin.Exec(ctx, `UPDATE app.matcher_slot_limits SET reader_limit=2`); err != nil {
			t.Fatal(err)
		}
		one := newReaderDiagnostic(t, db, "least-active-one", "2 minutes")
		two := newReaderDiagnostic(t, db, "least-active-two", "2 minutes")
		if key := mustReaderAssign(t, ctx, one, cluster, keys[:1]); key == nil || *key != keys[0] {
			t.Fatal("first reader assignment failed")
		}
		if key := mustReaderAssign(t, ctx, two, cluster, keys); key == nil || *key != keys[1] {
			t.Fatal("coordinator did not choose the less active eligible reader")
		}
	})
	t.Run("TerminalProductionRetainsPhysicalAdmission", func(t *testing.T) {
		for _, assigned := range []bool{false, true} {
			reset(t, 1)
			production := newReaderProduction(t, db, "terminal-owner")
			if assigned {
				mustReaderAssign(t, ctx, production, cluster, keys)
			}
			if !callBool(t, ctx, db.coverage, production.account, `SELECT app.fail_coverage_route($1,$2,$3,1,'coverage-route-timeout','Synthetic timeout.')`, production.job, production.owner, production.lease) {
				t.Fatal("real terminal transition failed")
			}
			candidate := readerReservation{account: uuid.New(), owner: "diagnostic-after-terminal", slot: uuid.New(), lease: uuid.New(), pool: db.api}
			if _, err := admin.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, candidate.account); err != nil {
				t.Fatal(err)
			}
			if callBool(t, ctx, db.api, candidate.account, `SELECT app.acquire_diagnostic_matcher_slot($1,$2,$3,$4,ARRAY[]::text[],interval '1 minute')`, candidate.account, candidate.owner, candidate.slot, candidate.lease) {
				t.Fatal("terminal production reservation disappeared before physical close")
			}
			otherProduction := newReaderProductionJob(t, db, "production-after-terminal")
			if callBool(t, ctx, db.coverage, otherProduction.account, `SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`, otherProduction.job, otherProduction.owner, otherProduction.lease, otherProduction.slot) {
				t.Fatal("production acquire retired another terminal reservation")
			}
			if mustReaderAssign(t, ctx, production, cluster, keys) != nil {
				t.Fatal("terminal production job received a new reader")
			}
			if !callBool(t, ctx, db.coverage, production.account, `SELECT app.clear_matcher_reader($1,$2,$3)`, production.slot, production.owner, production.lease) {
				t.Fatal("terminal physical cleanup could not clear reader")
			}
			assertReaderSlots(t, admin, ctx, 1, 0)
			if !callBool(t, ctx, db.coverage, production.account, `SELECT app.release_matcher_slot($1,$2,$3)`, production.slot, production.owner, production.lease) {
				t.Fatal("terminal reservation could not release after close")
			}
		}
	})
	t.Run("OwnerTenantCancellationAndInactiveAccountFences", func(t *testing.T) {
		reset(t, 4)
		production := newReaderProduction(t, db, "fenced-owner")
		mustReaderAssign(t, ctx, production, cluster, keys)
		for _, variant := range []string{"owner", "lease", "account"} {
			bad := production
			switch variant {
			case "owner":
				bad.owner = "another-owner"
			case "lease":
				bad.lease = uuid.New()
			case "account":
				bad.account = newReaderDiagnostic(t, db, "foreign-account", "2 minutes").account
			}
			if mustReaderAssign(t, ctx, bad, cluster, keys) != nil || callBool(t, ctx, bad.pool, bad.account, `SELECT app.clear_matcher_reader($1,$2,$3)`, bad.slot, bad.owner, bad.lease) ||
				callBool(t, ctx, bad.pool, bad.account, `SELECT app.release_matcher_slot($1,$2,$3)`, bad.slot, bad.owner, bad.lease) {
				t.Fatalf("%s fence accepted foreign reservation", variant)
			}
		}
		requester := accountRequester(t, db, production.account)
		if !callBool(t, ctx, db.api, production.account, `SELECT app.request_owned_job_cancellation($1,$2)`, production.parent, requester) {
			t.Fatal("real parent cancellation failed")
		}
		if mustReaderAssign(t, ctx, production, cluster, keys) != nil || callBool(t, ctx, db.coverage, production.account, `SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`, production.job, production.owner, production.lease, uuid.New()) {
			t.Fatal("cancelled running job was accepted")
		}
		diagnostic := newReaderDiagnostic(t, db, "inactive-owner", "2 minutes")
		mustReaderAssign(t, ctx, diagnostic, cluster, keys)
		if _, err := admin.Exec(ctx, `UPDATE app.accounts SET state='deleting' WHERE id=$1`, diagnostic.account); err != nil {
			t.Fatal(err)
		}
		if mustReaderAssign(t, ctx, diagnostic, cluster, keys) != nil || callBool(t, ctx, db.api, diagnostic.account, `SELECT app.acquire_diagnostic_matcher_slot($1,$2,$3,$4,ARRAY[]::text[],interval '1 minute')`, diagnostic.account, diagnostic.owner, uuid.New(), uuid.New()) {
			t.Fatal("deleting source account accepted a reader or admission")
		}
		if _, err := admin.Exec(ctx, `DELETE FROM app.accounts WHERE id=$1`, diagnostic.account); err != nil {
			t.Fatal(err)
		}
		if mustReaderAssign(t, ctx, diagnostic, cluster, keys) != nil {
			t.Fatal("deleted source account accepted assignment")
		}
	})
	t.Run("ClosedOpaqueCandidateSet", func(t *testing.T) {
		reset(t, 2)
		r := newReaderDiagnostic(t, db, "validation-owner", "2 minutes")
		for _, invalid := range [][]string{nil, {}, {keys[0], keys[0]}, {"https://reader.invalid"}, {strings.Repeat("A", 64)}, {""}, make([]string, 65)} {
			_, err := readerAssign(ctx, r, cluster, invalid)
			bootstrapSQLState(t, err, "22023")
		}
		for _, invalid := range []string{"", strings.Repeat("C", 64), "reader.internal"} {
			_, err := readerAssign(ctx, r, invalid, keys)
			bootstrapSQLState(t, err, "22023")
		}
		for _, expression := range []string{"ARRAY[NULL]::text[]", "ARRAY[ARRAY[repeat('a',64),repeat('b',64)]]"} {
			tx := beginAccount(t, ctx, r.pool, r.account)
			_, err := tx.Exec(ctx, `SELECT app.assign_matcher_reader($1,$2,$3,$4,`+expression+`)`, r.slot, r.owner, r.lease, cluster)
			_ = tx.Rollback(ctx)
			bootstrapSQLState(t, err, "22023")
		}
		assertReaderSlots(t, admin, ctx, 1, 0)
	})
	t.Run("ParentCancellationCannotDeadlockPersistence", func(t *testing.T) {
		for _, operation := range []struct{ name, query string }{
			{"persist", `SELECT app.persist_coverage_route($1,$2,$3,1,'[{"regionId":"geofabrik:test","generation":1}]'::jsonb,'[]'::jsonb)`},
			{"fail", `SELECT app.fail_coverage_route($1,$2,$3,1,'coverage-route-timeout','Synthetic timeout.')::text`},
			{"supersede", `SELECT app.supersede_coverage_route($1,$2,$3,1)::text`},
		} {
			t.Run(operation.name, func(t *testing.T) {
				reset(t, 2)
				r := newReaderProduction(t, db, "parent-lock-owner")
				requester := accountRequester(t, db, r.account)
				blocker := beginAccount(t, ctx, db.migration, r.account)
				defer blocker.Rollback(ctx)
				if _, err := blocker.Exec(ctx, `SELECT 1 FROM app.jobs WHERE id=$1 FOR UPDATE`, r.parent); err != nil {
					t.Fatal(err)
				}
				completion := beginAccount(t, ctx, r.pool, r.account)
				defer completion.Rollback(ctx)
				var pid int
				if err := completion.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				var outcome *string
				go func() {
					done <- completion.QueryRow(ctx, operation.query, r.job, r.owner, r.lease).Scan(&outcome)
				}()
				bootstrapWaitEvent(t, ctx, admin, pid, "transactionid")
				// A parent-first completion waiter must not hold the child. The
				// cancelling transaction can lock children while retaining its parent.
				if _, err := blocker.Exec(ctx, `SELECT 1 FROM app.jobs WHERE id=$1 FOR UPDATE NOWAIT`, r.job); err != nil {
					t.Fatalf("completion held child while waiting for parent: %v", err)
				}
				var cancelled bool
				if err := blocker.QueryRow(ctx, `SELECT app.request_coverage_job_cancellation($1,$2)`, r.parent, requester).Scan(&cancelled); err != nil || !cancelled {
					t.Fatalf("parent cancellation blocked on completion: %t err=%v", cancelled, err)
				}
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if operation.name == "persist" && outcome != nil || operation.name != "persist" && (outcome == nil || *outcome != "false") {
					t.Fatalf("completion accepted cancellation committed during parent wait: %s result=%v", operation.name, outcome)
				}
				if err := completion.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				var cancelledRunning bool
				if err := admin.QueryRow(ctx, `SELECT status='running' AND cancel_requested_at IS NOT NULL
					AND worker_id=$2 AND lease_token=$3 AND lease_expires_at>=clock_timestamp()
					FROM app.jobs WHERE id=$1`, r.job, r.owner, r.lease).Scan(&cancelledRunning); err != nil || !cancelledRunning {
					t.Fatalf("rejected completion changed cancelled running child: %t err=%v", cancelledRunning, err)
				}
			})
		}
	})
	t.Run("CommittedCancellationPreservesPriorEvidenceAndGeneration", func(t *testing.T) {
		for _, scope := range []string{"parent", "parent-only", "child"} {
			t.Run(scope, func(t *testing.T) {
				reset(t, 2)
				prior := newReaderProduction(t, db, "prior-evidence-owner")
				var workout uuid.UUID
				if err := admin.QueryRow(ctx, `SELECT workout_id FROM app.coverage_route_job_contexts WHERE job_id=$1`, prior.job).Scan(&workout); err != nil {
					t.Fatal(err)
				}
				matches := fmt.Sprintf(`[{"physicalSegmentId":"%s","regionId":"geofabrik:test","generation":1,"derivationVersion":3,
					"logicalPathId":"%s","sourceWayId":42,"sourceWayVersion":1,"highway":"service","broadClass":"road","tags":{},
					"segmentMeters":15,"segmentGeometry":{"type":"LineString","coordinates":[[-122,37],[-122.0001,37.0001]]},
					"firstTraversedAt":"2026-01-01T00:00:00Z","firstRouteOrder":0,
					"coveredGeometry":{"type":"MultiLineString","coordinates":[[[-122,37],[-122.0001,37.0001]]]}}]`, uuid.New(), uuid.New())
				tx := beginAccount(t, ctx, prior.pool, prior.account)
				defer tx.Rollback(ctx)
				var applied string
				if err := tx.QueryRow(ctx, `SELECT app.persist_coverage_route($1,$2,$3,1,'[{"regionId":"geofabrik:test","generation":1}]'::jsonb,$4::jsonb)`,
					prior.job, prior.owner, prior.lease, matches).Scan(&applied); err != nil || applied != "applied" {
					t.Fatalf("could not persist actual prior evidence: %s err=%v", applied, err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if !callBool(t, ctx, prior.pool, prior.account, `SELECT app.release_matcher_slot($1,$2,$3)`, prior.slot, prior.owner, prior.lease) {
					t.Fatal("could not retire prior physical reservation")
				}
				current := readerReservation{account: prior.account, job: uuid.New(), parent: uuid.New(), slot: uuid.New(), lease: uuid.New(), owner: "cancelled-evidence-owner", pool: db.coverage}
				tx = beginAccount(t, ctx, db.worker, current.account)
				defer tx.Rollback(ctx)
				routes := fmt.Sprintf(`[{"jobId":"%s","workoutId":"%s","routeRevision":1,"generations":[{"regionId":"geofabrik:test","generation":1}]}]`, current.job, workout)
				if _, err := tx.Exec(ctx, `SELECT * FROM app.enqueue_coverage_update($1,$2,'geofabrik:test',1,2,
					'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,$3::jsonb)`, current.account, current.parent, routes); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				tx = beginAccount(t, ctx, current.pool, current.account)
				defer tx.Rollback(ctx)
				var job uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT job_id FROM app.claim_next_coverage_route($1,$2,interval '2 minutes',$3)`, current.owner, current.lease, database.SupportedSchemaVersion).Scan(&job); err != nil || job != current.job {
					t.Fatalf("actual replacement claim=%s err=%v", job, err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				requester := accountRequester(t, db, current.account)
				assertCancellation(t, current, &uncancelled)
				target := current.parent
				if scope == "child" {
					target = current.job
				}
				cancelQuery := `SELECT app.request_owned_job_cancellation($1,$2)`
				if scope == "parent-only" {
					// The trusted generic entrypoint flags only this running parent,
					// independently exercising the parent fence without invented leases.
					cancelQuery = `SELECT app.request_job_cancellation($1,$2)`
				}
				if !callBool(t, ctx, db.api, current.account, cancelQuery, target, requester) {
					t.Fatal("actual committed cancellation was denied")
				}
				assertCancellation(t, current, &cancelled)
				// Compare full evidence, processing state, completion context, and the
				// explorer generation, not just row counts or a rolled-back result.
				capture := func() string {
					t.Helper()
					var snapshot string
					if err := admin.QueryRow(ctx, `SELECT jsonb_build_object(
						'matches',(SELECT jsonb_agg(to_jsonb(m) ORDER BY physical_segment_id) FROM app.workout_segment_matches m WHERE account_id=$1 AND workout_id=$2),
						'paths',(SELECT jsonb_agg(to_jsonb(p) ORDER BY logical_path_id) FROM app.workout_path_attributions p WHERE account_id=$1 AND workout_id=$2),
						'state',(SELECT to_jsonb(s) FROM app.workout_coverage_states s WHERE account_id=$1 AND workout_id=$2),
						'context',(SELECT to_jsonb(c) FROM app.coverage_route_job_contexts c WHERE job_id=$3),
						'events',(SELECT count(*) FROM app.job_events WHERE job_id=$3),
						'logs',(SELECT count(*) FROM app.job_logs WHERE job_id=$3),
						'generation',(SELECT to_jsonb(g) FROM app.account_data_generations g WHERE account_id=$1))::text`, current.account, workout, current.job).Scan(&snapshot); err != nil {
						t.Fatal(err)
					}
					return snapshot
				}
				before := capture()
				for _, payload := range []string{"[]", matches} {
					tx = beginAccount(t, ctx, current.pool, current.account)
					var result *string
					if err := tx.QueryRow(ctx, `SELECT app.persist_coverage_route($1,$2,$3,1,'[{"regionId":"geofabrik:test","generation":1}]'::jsonb,$4::jsonb)`, current.job, current.owner, current.lease, payload).Scan(&result); err != nil || result != nil {
						t.Fatalf("cancelled persistence accepted replacement payload: result=%v err=%v", result, err)
					}
					if err := tx.Commit(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if callBool(t, ctx, current.pool, current.account, `SELECT app.fail_coverage_route($1,$2,$3,1,'coverage-route-timeout','Synthetic timeout.')`, current.job, current.owner, current.lease) ||
					callBool(t, ctx, current.pool, current.account, `SELECT app.supersede_coverage_route($1,$2,$3,1)`, current.job, current.owner, current.lease) {
					t.Fatal("cancelled failure/supersede completion was accepted")
				}
				if after := capture(); before != after {
					t.Fatal("cancelled completion changed prior evidence, processing state, result context, diagnostics, or account generation")
				}
				var validCancelledLease, childCancelled, parentCancelled bool
				if err := admin.QueryRow(ctx, `SELECT child.status='running'
					AND (child.cancel_requested_at IS NOT NULL OR parent.cancel_requested_at IS NOT NULL)
					AND child.worker_id=$2 AND child.lease_token=$3 AND child.lease_expires_at>=clock_timestamp(),
					child.cancel_requested_at IS NOT NULL,parent.cancel_requested_at IS NOT NULL
					FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id
					WHERE child.id=$1`, current.job, current.owner, current.lease).Scan(&validCancelledLease, &childCancelled, &parentCancelled); err != nil || !validCancelledLease {
					t.Fatalf("rejection did not retain cancelled running job with valid owner/lease: %t err=%v", validCancelledLease, err)
				}
				if childCancelled != (scope != "parent-only") || parentCancelled != (scope != "child") {
					t.Fatalf("wrong committed cancellation scope: %s child=%t parent=%t", scope, childCancelled, parentCancelled)
				}
				// Cancellation cleanup still uses the existing trusted finish entrypoint.
				if !callBool(t, ctx, current.pool, current.account, `SELECT app.finish_job($1,$2,$3,'cancelled')`, current.job, current.owner, current.lease) {
					t.Fatal("cancellation cleanup could not finish the fenced child")
				}
				assertCancellation(t, current, nil)
			})
		}
	})
	t.Run("ProductionInactiveAccountAndExpiredLease", func(t *testing.T) {
		reset(t, 2)
		r := newReaderProduction(t, db, "production-expiry-owner")
		if _, err := admin.Exec(ctx, `UPDATE app.accounts SET state='deleting' WHERE id=$1`, r.account); err != nil {
			t.Fatal(err)
		}
		assertCancellation(t, r, nil)
		if mustReaderAssign(t, ctx, r, cluster, keys) != nil || callBool(t, ctx, r.pool, r.account,
			`SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`, r.job, r.owner, r.lease, uuid.New()) {
			t.Fatal("deleting production account accepted new physical work")
		}
		if _, err := admin.Exec(ctx, `UPDATE app.accounts SET state='active' WHERE id=$1`, r.account); err != nil {
			t.Fatal(err)
		}
		if !callBool(t, ctx, r.pool, r.account, `SELECT app.heartbeat_job($1,$2,$3,interval '3 seconds')`, r.job, r.owner, r.lease) {
			t.Fatal("could not shorten real production lease")
		}
		mustReaderAssign(t, ctx, r, cluster, keys)
		blocker := beginAccount(t, ctx, db.migration, r.account)
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		tx := beginAccount(t, ctx, r.pool, r.account)
		defer tx.Rollback(ctx)
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		var chosen *string
		done := make(chan error, 1)
		go func() {
			done <- tx.QueryRow(ctx, `SELECT app.assign_matcher_reader($1,$2,$3,$4,$5)`, r.slot, r.owner, r.lease, cluster, keys).Scan(&chosen)
		}()
		bootstrapWaitEvent(t, ctx, admin, pid, "transactionid")
		var expired bool
		for !expired {
			if err := admin.QueryRow(ctx, `SELECT lease_expires_at < clock_timestamp() FROM app.jobs WHERE id=$1`, r.job).Scan(&expired); err != nil {
				t.Fatal(err)
			}
			if !expired {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil || chosen != nil {
			t.Fatalf("expired production post-guard assignment=%v err=%v", chosen, err)
		}
		assertCancellation(t, r, nil)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if callBool(t, ctx, r.pool, r.account, `SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`, r.job, r.owner, r.lease, uuid.New()) ||
			callBool(t, ctx, r.pool, r.account, `SELECT app.clear_matcher_reader($1,$2,$3)`, r.slot, r.owner, r.lease) {
			t.Fatal("expired production owner obtained admission or cleared assignment")
		}
		assertReaderSlots(t, admin, ctx, 0, 0)
	})
	t.Run("LeaseExpiresWhileWaitingForGuard", func(t *testing.T) {
		reset(t, 2)
		r := newReaderDiagnostic(t, db, "expiry-owner", "3 seconds")
		blocker := beginAccount(t, ctx, db.migration, r.account)
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		tx := beginAccount(t, ctx, r.pool, r.account)
		defer tx.Rollback(ctx)
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		var chosen *string
		done := make(chan error, 1)
		go func() {
			done <- tx.QueryRow(ctx, `SELECT app.assign_matcher_reader($1,$2,$3,$4,$5)`, r.slot, r.owner, r.lease, cluster, keys).Scan(&chosen)
		}()
		bootstrapWaitEvent(t, ctx, admin, pid, "transactionid")
		var expired bool
		for !expired {
			if err := admin.QueryRow(ctx, `SELECT expires_at < clock_timestamp() FROM app.matcher_slots WHERE slot_token=$1`, r.slot).Scan(&expired); err != nil {
				t.Fatal(err)
			}
			if !expired {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil || chosen != nil {
			t.Fatalf("expired post-lock assignment=%v err=%v", chosen, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if callBool(t, ctx, r.pool, r.account, `SELECT app.clear_matcher_reader($1,$2,$3)`, r.slot, r.owner, r.lease) {
			t.Fatal("expired owner cleared a reservation")
		}
		assertReaderSlots(t, admin, ctx, 0, 0)
		fresh := newReaderDiagnostic(t, db, "fresh-owner", "2 minutes")
		if mustReaderAssign(t, ctx, fresh, cluster, keys[:1]) == nil {
			t.Fatal("expired reservation capacity was not recovered")
		}
	})
}

func newReaderDiagnostic(t *testing.T, db testDatabases, owner, duration string) readerReservation {
	t.Helper()
	r := readerReservation{account: uuid.New(), slot: uuid.New(), lease: uuid.New(), owner: owner, pool: db.api}
	if _, err := db.migration.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, r.account); err != nil {
		t.Fatal(err)
	}
	if !callBool(t, db.ctx, r.pool, r.account, `SELECT app.acquire_diagnostic_matcher_slot($1,$2,$3,$4,ARRAY[]::text[],$5::interval)`, r.account, r.owner, r.slot, r.lease, duration) {
		t.Fatal("diagnostic reservation was denied")
	}
	return r
}

func newReaderProduction(t *testing.T, db testDatabases, owner string) readerReservation {
	t.Helper()
	r := newReaderProductionJob(t, db, owner)
	if !callBool(t, db.ctx, r.pool, r.account, `SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`, r.job, r.owner, r.lease, r.slot) {
		t.Fatal("production reservation was denied")
	}
	return r
}

func newReaderProductionJob(t *testing.T, db testDatabases, owner string) readerReservation {
	t.Helper()
	f := newBootstrapFixture(t, db, 1)
	bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
		VALUES($1,$2,1,$3,'map_data_ready',clock_timestamp())`, f.account, f.workouts[0], bootstrapDigest())
	r := readerReservation{account: f.account, job: uuid.New(), parent: uuid.New(), slot: uuid.New(), lease: uuid.New(), owner: owner, pool: db.coverage}
	tx := beginAccount(t, db.ctx, db.worker, r.account)
	routes := fmt.Sprintf(`[{"jobId":"%s","workoutId":"%s","routeRevision":1,"generations":[{"regionId":"geofabrik:test","generation":1}]}]`, r.job, f.workouts[0])
	if _, err := tx.Exec(db.ctx, `SELECT * FROM app.enqueue_coverage_update($1,$2,'geofabrik:test',1,1,
		'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,$3::jsonb)`, r.account, r.parent, routes); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	tx = beginAccount(t, db.ctx, db.coverage, r.account)
	defer tx.Rollback(db.ctx)
	var job, account uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT job_id,account_id FROM app.claim_next_coverage_route($1,$2,interval '2 minutes',$3)`, owner, r.lease, database.SupportedSchemaVersion).Scan(&job, &account); err != nil || job != r.job || account != r.account {
		t.Fatalf("real production claim job/account=%s/%s err=%v", job, account, err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return r
}

func readerAssign(ctx context.Context, r readerReservation, cluster string, keys []string) (*string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, r.account.String()); err != nil {
		return nil, err
	}
	var chosen *string
	if err := tx.QueryRow(ctx, `SELECT app.assign_matcher_reader($1,$2,$3,$4,$5)`, r.slot, r.owner, r.lease, cluster, keys).Scan(&chosen); err != nil {
		return nil, err
	}
	return chosen, tx.Commit(ctx)
}

func mustReaderAssign(t *testing.T, ctx context.Context, r readerReservation, cluster string, keys []string) *string {
	t.Helper()
	chosen, err := readerAssign(ctx, r, cluster, keys)
	if err != nil {
		t.Fatal(err)
	}
	return chosen
}

func assertReaderSlots(t *testing.T, admin *pgx.Conn, ctx context.Context, wantSlots, wantAssigned int) {
	t.Helper()
	var slots, assigned int
	if err := admin.QueryRow(ctx, `SELECT count(*),count(reader_backend_key) FROM app.matcher_slots`).Scan(&slots, &assigned); err != nil || slots != wantSlots || assigned != wantAssigned {
		t.Fatalf("reservations/assigned=%d/%d want=%d/%d err=%v", slots, assigned, wantSlots, wantAssigned, err)
	}
}
