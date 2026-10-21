package migrations_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const bootstrapWorker = "legacy-bootstrap-test"

type bootstrapFixture struct {
	account  uuid.UUID
	workouts []uuid.UUID
	lease    uuid.UUID
	revision int64
}

// Supply an EMPTY PostGIS-capable loopback scratch database and an admin URL
// able to SET ROLE to the standard preprovisioned roles. No application URL fallback.
// SCHEMA20=1 applies immutable 001-020 plus ONLY the marked function patch;
// The default proves the full supported upgrade with legacy fixtures at schema 20.
func TestLegacyCoverageBootstrapIntegration(t *testing.T) {
	dsn := os.Getenv("COVERAGE_BOOTSTRAP_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("COVERAGE_BOOTSTRAP_TEST_DISPOSABLE") != "1" {
		t.Skip("COVERAGE_BOOTSTRAP_TEST_DATABASE_URL and COVERAGE_BOOTSTRAP_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch database configuration")
	}
	if !strings.HasPrefix(config.Database, "workouts_coverage_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("coverage bootstrap tests require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect to scratch database")
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT current_database()=$1 AND to_regnamespace('app') IS NULL
		AND to_regclass('public.goose_db_version') IS NULL`, config.Database).Scan(&empty); err != nil || !empty {
		t.Fatal("scratch database must be empty before the suite runs")
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
	if _, err := provider.UpTo(ctx, 20); err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid scratch pool configuration")
		}
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
		cfg.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal("create scratch pool")
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"),
		worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	idle := newBootstrapFixture(t, db, 3)
	live := newBootstrapFixture(t, db, 1)
	covered := newBootstrapFixture(t, db, 1)
	deleted := newBootstrapFixture(t, db, 1)
	unseeded := newBootstrapFixture(t, db, 1)
	bootstrapExec(t, db, unseeded, `DELETE FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, unseeded.account)
	digest := bootstrapDigest()
	bootstrapExec(t, db, covered, `INSERT INTO app.workout_coverage_states
		(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state)
		VALUES($1,$2,7,$3,'unresolved')`, covered.account, covered.workouts[0], digest)
	// A real owner deletion, not a synthetic deletion marker.
	requester := accountRequester(t, db, deleted.account)
	deleteTx := beginAccount(t, ctx, db.api, deleted.account)
	if _, err := deleteTx.Exec(ctx, `SELECT * FROM app.enqueue_workout_deletion($1,$2)`, deleted.workouts[0], requester); err != nil {
		t.Fatal(err)
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claimBootstrap(t, db, &live)
	if !callBoolNoAccount(t, ctx, db.coverage, `SELECT app.advance_coverage_reconciliation($1,$2,$3,$4,$5,false,interval '24 hours')`,
		live.account, live.revision, bootstrapWorker, live.lease, live.workouts[0]) {
		t.Fatal("could not advance pre-upgrade campaign")
	}
	source, err := migrations.Files.ReadFile("022_legacy_coverage_bootstrap.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema20 := os.Getenv("COVERAGE_BOOTSTRAP_TEST_SCHEMA20") == "1"
	if schema20 {
		patch := strings.Split(string(source), "-- END SCHEMA 20 COMPATIBLE FUNCTION PATCH")[0]
		if _, err := db.migration.Exec(ctx, patch); err != nil {
			t.Fatalf("schema-20 function-only hotpatch: %v", err)
		}
	} else if _, err := provider.UpTo(ctx, database.SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	t.Run("MetadataACLsAndCampaignWakeup", func(t *testing.T) {
		wantVersion, wantMinimum := database.SupportedSchemaVersion, 28
		if schema20 {
			wantVersion, wantMinimum = 20, 18
		}
		var version, minimum, gooseVersion int
		if err := db.migration.QueryRow(ctx, `SELECT schema_version,minimum_runtime_version,
			(SELECT max(version_id) FROM public.goose_db_version WHERE is_applied) FROM app.schema_metadata WHERE singleton`).Scan(
			&version, &minimum, &gooseVersion); err != nil || version != wantVersion || minimum != wantMinimum || gooseVersion != wantVersion {
			t.Fatalf("schema/minimum/goose=%d/%d/%d, want %d/%d/%d err=%v", version, minimum, gooseVersion, wantVersion, wantMinimum, wantVersion, err)
		}
		for _, name := range []string{
			"app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)",
			"app.read_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea)",
			"app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)",
			"app.read_coverage_route(uuid,text,uuid)",
			"app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)",
		} {
			var safe bool
			if err := db.migration.QueryRow(ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner'
				AND p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
				AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
				AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
				FROM pg_proc p WHERE p.oid=$1::regprocedure`, name).Scan(&safe); err != nil || !safe {
				t.Fatalf("function %s security=%t err=%v", name, safe, err)
			}
		}
		var directWrite bool
		if err := db.coverage.QueryRow(ctx, `SELECT has_table_privilege(current_user,'app.workout_coverage_states','INSERT,UPDATE,DELETE')`).Scan(&directWrite); err != nil || directWrite {
			t.Fatalf("coverage direct mutation privilege=%t err=%v", directWrite, err)
		}
		for _, f := range []bootstrapFixture{idle, live, covered, deleted} {
			var desired, active int64
			var cursor *uuid.UUID
			var due bool
			if err := db.migration.QueryRow(ctx, `SELECT desired_revision,active_revision,cursor_workout_id,next_scan_at<=clock_timestamp()
				FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, f.account).Scan(&desired, &active, &cursor, &due); err != nil {
				t.Fatal(err)
			}
			woken := !schema20 && (f.account == idle.account || f.account == live.account)
			wantDesired := int64(14)
			if woken {
				wantDesired++
			}
			if desired != wantDesired || active != 14 || (f.account != live.account && due != woken) {
				t.Fatalf("campaign %s desired/active/due=%d/%d/%t", f.account, desired, active, due)
			}
			if f.account == live.account && (cursor == nil || *cursor != live.workouts[0]) {
				t.Fatal("migration reset a live lease's cursor")
			}
		}
		var unseededCount int
		if err := db.migration.QueryRow(ctx, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, unseeded.account).Scan(&unseededCount); err != nil || unseededCount != map[bool]int{true: 0, false: 1}[schema20] {
			t.Fatalf("unseeded campaign count=%d err=%v", unseededCount, err)
		}
		if !schema20 {
			for _, runtime := range []*pgxpool.Pool{db.api, db.worker, db.coverage} {
				if !database.Ready(ctx, runtime) {
					t.Fatalf("schema-%d runtime is not ready", database.SupportedSchemaVersion)
				}
			}
			if callBoolNoAccount(t, ctx, db.coverage, `SELECT app.advance_coverage_reconciliation($1,$2,$3,$4,$5,true,interval '24 hours')`,
				live.account, live.revision, bootstrapWorker, live.lease, live.workouts[0]) {
				t.Fatal("pre-pipeline lease completed without a persisted runtime28 fence")
			}
			var due bool
			if err := db.migration.QueryRow(ctx, `SELECT next_scan_at<=clock_timestamp() FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, live.account).Scan(&due); err != nil || !due {
				t.Fatalf("newer bootstrap campaign postponed by old completion: due=%t err=%v", due, err)
			}
			bootstrapExec(t, db, live, `UPDATE app.coverage_reconciliation_accounts SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, live.account)
			claimBootstrap(t, db, &live)
			if live.revision != 15 {
				t.Fatal("next claim did not restart the newer campaign")
			}
		}
	})
	t.Run("PaginationRealDigestAndNormalQueue", func(t *testing.T) {
		claimBootstrap(t, db, &idle)
		tx := beginAccount(t, ctx, db.coverage, idle.account)
		defer tx.Rollback(ctx)
		var after *uuid.UUID
		for i, workout := range idle.workouts {
			var id uuid.UUID
			var revision int64
			var scanned []byte
			if err := tx.QueryRow(ctx, `SELECT * FROM app.read_coverage_reconciliation_routes($1,$2,$3,$4,$5,1)`,
				idle.account, idle.revision, bootstrapWorker, idle.lease, after).Scan(&id, &revision, &scanned); err != nil {
				t.Fatal(err)
			}
			if id != workout || revision != 0 || scanned != nil {
				t.Fatalf("page %d id/revision/digest=%s/%d/%x", i, id, revision, scanned)
			}
			after = &idle.workouts[i]
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_reconciliation_routes($1,$2,$3,$4,$5,1)`,
			idle.account, idle.revision, bootstrapWorker, idle.lease, after).Scan(&count); err != nil || count != 0 {
			t.Fatalf("pagination did not finish: count=%d err=%v", count, err)
		}
		bootstrapPointCount(t, db, tx, idle, idle.workouts[0], 0, nil, 2)
		var revision *int64
		if err := tx.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`,
			idle.account, idle.revision, bootstrapWorker, idle.lease, idle.workouts[0], digest).Scan(&revision); err != nil || revision == nil || *revision != 1 {
			t.Fatalf("bootstrap revision=%v err=%v", revision, err)
		}
		bootstrapPointCount(t, db, tx, idle, idle.workouts[0], 0, nil, 0)
		bootstrapPointCount(t, db, tx, idle, idle.workouts[0], 1, digest, 2)
		var stored []byte
		var readiness, processing string
		// Inspect inside this uncommitted scratch transaction using the admin session.
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_migration`); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT route_input_sha256,readiness_state,processing_state FROM app.workout_coverage_states
			WHERE account_id=$1 AND workout_id=$2`, idle.account, idle.workouts[0]).Scan(&stored, &readiness, &processing); err != nil ||
			!bytes.Equal(stored, digest) || readiness != "unresolved" || processing != "not_started" {
			t.Fatalf("stored digest/state=%x/%s/%s err=%v", stored, readiness, processing, err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_coverage_worker`); err != nil {
			t.Fatal(err)
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT app.apply_coverage_reconciliation($1,$2,$3,$4,$5,1,$6,'map_data_ready',NULL,
			'[{"regionId":"geofabrik:test","generation":1}]',$7,$8,'coverage-experimental-v1',
			'coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`,
			idle.account, idle.revision, bootstrapWorker, idle.lease, idle.workouts[0], digest, uuid.New(), uuid.New()).Scan(&applied); err != nil || !applied {
			t.Fatalf("normal readiness/queue applied=%t err=%v", applied, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		check := beginAccount(t, ctx, db.migration, idle.account)
		defer check.Rollback(ctx)
		if err := check.QueryRow(ctx, `SELECT count(*) FROM app.coverage_route_job_contexts
			WHERE account_id=$1 AND workout_id=$2 AND route_input_revision=1 AND route_input_sha256=$3`,
			idle.account, idle.workouts[0], digest).Scan(&count); err != nil || count != 1 {
			t.Fatalf("normal queued children=%d err=%v", count, err)
		}
	})
	t.Run("LeaseScopeDeletionAndStrictValidation", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		claimBootstrap(t, db, &f)
		for _, mutation := range []string{"wrong token", "wrong worker", "wrong revision", "wrong account", "wrong scope", "deleted", "expired", "inactive"} {
			t.Run(mutation, func(t *testing.T) {
				candidate := f
				worker := bootstrapWorker
				scope := f.account
				switch mutation {
				case "wrong token":
					candidate.lease = uuid.New()
				case "wrong worker":
					worker = "other-worker"
				case "wrong revision":
					candidate.revision++
				case "wrong account":
					candidate.account = covered.account
				case "wrong scope":
					scope = covered.account
				}
				tx := beginAccount(t, ctx, db.coverage, scope)
				defer tx.Rollback(ctx)
				if mutation == "deleted" || mutation == "expired" || mutation == "inactive" {
					if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_migration; SET LOCAL session_replication_role='replica'`); err != nil {
						t.Fatal(err)
					}
					queries := map[string]string{
						"deleted":  `UPDATE app.workouts SET deletion_requested_at=clock_timestamp(),deletion_target_id=id WHERE account_id=$1`,
						"expired":  `UPDATE app.coverage_reconciliation_accounts SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`,
						"inactive": `UPDATE app.accounts SET state='deleting' WHERE id=$1`,
					}
					if _, err := tx.Exec(ctx, queries[mutation], f.account); err != nil {
						t.Fatal(err)
					}
					if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='origin'; SET LOCAL ROLE workouts_coverage_worker`); err != nil {
						t.Fatal(err)
					}
				}
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_reconciliation_routes($1,$2,$3,$4,NULL,10)`,
					candidate.account, candidate.revision, worker, candidate.lease).Scan(&count); err != nil || count != 0 {
					t.Fatalf("unfenced listing count=%d err=%v", count, err)
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_reconciliation_route($1,$2,$3,$4,$5,0,NULL)`,
					candidate.account, candidate.revision, worker, candidate.lease, f.workouts[0]).Scan(&count); err != nil || count != 0 {
					t.Fatalf("unfenced points count=%d err=%v", count, err)
				}
				var revision *int64
				if err := tx.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`,
					candidate.account, candidate.revision, worker, candidate.lease, f.workouts[0], digest).Scan(&revision); err != nil || revision != nil {
					t.Fatalf("unfenced repair revision=%v err=%v", revision, err)
				}
			})
		}
		for _, invalid := range []struct {
			revision any
			old, new []byte
		}{{0, digest, digest}, {1, nil, digest}, {1, []byte{1}, digest}, {-1, digest, digest}, {0, nil, nil}, {0, nil, []byte{1}}, {nil, nil, digest}} {
			tx := beginAccount(t, ctx, db.coverage, f.account)
			var result *int64
			err := tx.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`,
				f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], invalid.revision, invalid.old, invalid.new).Scan(&result)
			_ = tx.Rollback(ctx)
			var denied *pgconn.PgError
			if !errors.As(err, &denied) || denied.Code != "22023" {
				t.Fatalf("invalid digest accepted: %v", err)
			}
		}
		// Deletion committed after a leased read must still block initialization.
		read := beginAccount(t, ctx, db.coverage, f.account)
		bootstrapPointCount(t, db, read, f, f.workouts[0], 0, nil, 2)
		if err := read.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		requester := accountRequester(t, db, f.account)
		tx := beginAccount(t, ctx, db.api, f.account)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.enqueue_workout_deletion($1,$2)`, f.workouts[0], requester); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = beginAccount(t, ctx, db.coverage, f.account)
		defer tx.Rollback(ctx)
		var result *int64
		if err := tx.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`,
			f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest).Scan(&result); err != nil || result != nil {
			t.Fatalf("owner deletion resurrected state: %v err=%v", result, err)
		}
	})
	t.Run("ConcurrentInitializerAndExistingCAS", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		claimBootstrap(t, db, &f)
		parent := insertQueued(t, ctx, db.api, f.account, uuid.Nil, "manual_ingest")
		child := insertQueued(t, ctx, db.api, f.account, parent, "manual_ingest_source")
		ingestLease := uuid.New()
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '2 minutes')`, child, "bootstrap-ingest", ingestLease) {
			t.Fatal("ingest fixture claim failed")
		}
		actor := beginAccount(t, ctx, db.worker, f.account)
		defer actor.Rollback(ctx)
		var revision int64
		var changed bool
		if err := actor.QueryRow(ctx, `SELECT * FROM app.initialize_workout_coverage($1,$2,$3,$4,$5,$6)`,
			f.account, f.workouts[0], digest, child, "bootstrap-ingest", ingestLease).Scan(&revision, &changed); err != nil || revision != 1 {
			t.Fatalf("ingest initialization revision=%d err=%v", revision, err)
		}
		repair := beginAccount(t, ctx, db.coverage, f.account)
		defer repair.Rollback(ctx)
		done := make(chan error, 1)
		go func() {
			var result *int64
			err := repair.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`,
				f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest).Scan(&result)
			if err == nil && result != nil {
				err = errors.New("bootstrap replaced a concurrently initialized state")
			}
			done <- err
		}()
		select {
		case err := <-done:
			bootstrapSQLState(t, err, "55P03")
		case <-time.After(2 * time.Second):
			t.Fatal("repair waited for initializer while holding the account lock")
		}
		if err := repair.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		// Continue the actual ingest path: readiness then enqueue takes account
		// FOR UPDATE while the initializer still retains its workout FK lock.
		var ready bool
		if err := actor.QueryRow(ctx, `SELECT app.set_workout_coverage_readiness($1,$2,1,'map_data_ready',NULL,
			'[{"regionId":"geofabrik:test","generation":1}]',$3,$4,$5)`, f.account, f.workouts[0], child,
			"bootstrap-ingest", ingestLease).Scan(&ready); err != nil || !ready {
			t.Fatalf("ingest readiness=%t err=%v", ready, err)
		}
		enqueueCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var enqueued uuid.UUID
		if err := actor.QueryRow(enqueueCtx, `SELECT job_id FROM app.enqueue_coverage_update($1,$2,'geofabrik:test',1,1,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,$3)`,
			f.account, uuid.New(), fmt.Sprintf(`[{"jobId":"%s","workoutId":"%s","routeRevision":1,"generations":[{"regionId":"geofabrik:test","generation":1}]}]`,
				uuid.New(), f.workouts[0])).Scan(&enqueued); err != nil {
			t.Fatalf("initializer could not enqueue under account lock: %v", err)
		}
		if err := actor.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		repair = beginAccount(t, ctx, db.coverage, f.account)
		defer repair.Rollback(ctx)
		bootstrapPointCount(t, db, repair, f, f.workouts[0], 0, nil, 0)
		if err := repair.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		bootstrapMutatePoints(t, db, f, `UPDATE app.workout_route_points SET altitude=123.5 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		newDigest := bootstrapCurrentDigest(t, db, f)
		repair = beginAccount(t, ctx, db.coverage, f.account)
		defer repair.Rollback(ctx)
		var result *int64
		if err := repair.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,1,$6,$7)`,
			f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest, newDigest).Scan(&result); err != nil || result == nil || *result != 2 {
			t.Fatalf("existing CAS repair=%v err=%v", result, err)
		}
		if err := repair.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,1,$6,$7)`,
			f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest, newDigest).Scan(&result); err != nil || result != nil {
			t.Fatalf("stale CAS repair=%v err=%v", result, err)
		}
	})
	t.Run("PointOverflowSentinelDoesNotSeedState", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		claimBootstrap(t, db, &f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
			SELECT $1,$2,n,'2026-01-01T00:00:00Z'::timestamptz+n*interval '1 second',37,-122
			FROM generate_series(2,50001) n`, f.account, f.workouts[0]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = beginAccount(t, ctx, db.coverage, f.account)
		bootstrapPointCount(t, db, tx, f, f.workouts[0], 0, nil, 50001)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		check := beginAccount(t, ctx, db.migration, f.account)
		defer check.Rollback(ctx)
		var count int
		if err := check.QueryRow(ctx, `SELECT count(*) FROM app.workout_coverage_states WHERE account_id=$1`, f.account).Scan(&count); err != nil || count != 0 {
			t.Fatalf("overflow read seeded state: count=%d err=%v", count, err)
		}
	})
	testBootstrapHardening(t, db, admin)
}

func newBootstrapFixture(t *testing.T, db testDatabases, routes int) bootstrapFixture {
	t.Helper()
	f := bootstrapFixture{account: uuid.New(), revision: 14}
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, f.account); err != nil {
		t.Fatal(err)
	}
	source := uuid.New()
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Bootstrap fixture','bootstrap fixture','health-auto-export-local',$3)`, source, f.account, []byte("synthetic-envelope")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	workoutType := uuid.New()
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,'outdoor-run','Outdoor Run')`, workoutType, f.account); err != nil {
		t.Fatal(err)
	}
	for range routes {
		workout := uuid.Must(uuid.NewV7())
		f.workouts = append(f.workouts, workout)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
			VALUES($1,$2,$3,$4,$5,$1::uuid::text,$6,'Outdoor Run','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','2026-01-01',1)`,
			workout, f.account, source, uuid.New(), workoutType, bootstrapDigest()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
			VALUES($1,$2,0,'2026-01-01T00:00:00Z',37,-122),($1,$2,1,'2026-01-01T00:00:01Z',37.0001,-122.0001)`, f.account, workout); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,
			minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
			VALUES($1,$2,2,-122.0001,37,-122,37.0001,false,ST_GeomFromText('MULTILINESTRING((-122 37,-122.0001 37.0001))',4326))`, f.account, workout); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(db.ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,active_revision,completed_revision,next_scan_at)
		VALUES($1,14,14,14,clock_timestamp()+interval '1 day')`, f.account); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func bootstrapExec(t *testing.T, db testDatabases, f bootstrapFixture, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(db.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func claimBootstrap(t *testing.T, db testDatabases, f *bootstrapFixture) {
	t.Helper()
	// This helper runs only inside the guarded, initially empty scratch suite.
	// Expiry cases leave earlier campaigns due; isolate scheduling without
	// changing their leases or account state, so foreign-scope reads still work.
	bootstrapExec(t, db, *f, `UPDATE app.coverage_reconciliation_accounts SET
		next_scan_at='infinity' WHERE account_id<>$1`, f.account)
	bootstrapExec(t, db, *f, `UPDATE app.coverage_reconciliation_accounts SET
		next_scan_at='-infinity',completed_revision=LEAST(completed_revision,desired_revision-1) WHERE account_id=$1`, f.account)
	f.lease = uuid.New()
	var account uuid.UUID
	var cursor *uuid.UUID
	tx := beginAccount(t, db.ctx, db.coverage, f.account)
	defer tx.Rollback(context.Background())
	if err := tx.QueryRow(db.ctx, `SELECT * FROM app.claim_coverage_reconciliation($1,$2,interval '2 minutes',$3)`,
		bootstrapWorker, f.lease, database.SupportedSchemaVersion).Scan(&account, &f.revision, &cursor); err != nil || account != f.account || cursor != nil {
		t.Fatalf("claim account/revision/cursor=%s/%d/%v err=%v", account, f.revision, cursor, err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func bootstrapPointCount(t *testing.T, db testDatabases, tx pgx.Tx, f bootstrapFixture, workout uuid.UUID, revision int64, digest []byte, want int) {
	t.Helper()
	var count int
	if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.read_coverage_reconciliation_route($1,$2,$3,$4,$5,$6,$7)`,
		f.account, f.revision, bootstrapWorker, f.lease, workout, revision, digest).Scan(&count); err != nil || count != want {
		t.Fatalf("points=%d, want %d err=%v", count, want, err)
	}
}

// Exact deployed worker canonicalRouteInputSHA256 encoding for the two fixture
// points, including timestamp and NULL optional-field markers (not SQL geometry).
func bootstrapDigest() []byte {
	points := make([]bootstrapCanonicalPoint, 2)
	for i := range points {
		stamp := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		points[i] = bootstrapCanonicalPoint{sequence: int64(i), timestamp: &stamp,
			latitude: 37 + float64(i)*0.0001, longitude: -122 - float64(i)*0.0001}
	}
	return bootstrapCanonicalDigest(points)
}

type bootstrapCanonicalPoint struct {
	sequence            int64
	timestamp           *time.Time
	latitude, longitude float64
	optional            [7]*float64
}

func bootstrapCanonicalDigest(points []bootstrapCanonicalPoint) []byte {
	digest := sha256.New()
	write := func(value uint64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], value)
		_, _ = digest.Write(encoded[:])
	}
	write(uint64(len(points)))
	for _, point := range points {
		write(uint64(point.sequence))
		if point.timestamp == nil {
			_, _ = digest.Write([]byte{0})
		} else {
			_, _ = digest.Write([]byte{1})
			stamp := point.timestamp.UTC().Format(time.RFC3339Nano)
			write(uint64(len(stamp)))
			_, _ = digest.Write([]byte(stamp))
		}
		write(math.Float64bits(point.latitude))
		write(math.Float64bits(point.longitude))
		for _, value := range point.optional {
			if value == nil {
				_, _ = digest.Write([]byte{0})
			} else {
				_, _ = digest.Write([]byte{1})
				write(math.Float64bits(*value))
			}
		}
	}
	return digest.Sum(nil)
}
