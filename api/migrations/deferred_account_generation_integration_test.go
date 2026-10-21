package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
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

// This suite creates and removes only its own synthetic databases. Runtime
// connections log in as the actual roles, never as a superuser with SET ROLE.
func TestDeferredAccountGenerationPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_DEFERRED_GENERATION_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_DEFERRED_GENERATION_DISPOSABLE") != "1" {
		t.Skip("explicit disposable deferred-generation cluster required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || (cfg.Host != "127.0.0.1" && cfg.Host != "::1") {
		t.Fatal("deferred-generation suite requires an explicit loopback admin endpoint")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
			t.Fatal("fallback endpoints prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var version string
	if err := admin.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil || strings.Fields(version)[0] != "18.6" {
		t.Fatalf("requires PostgreSQL 18.6, got %q: %v", version, err)
	}
	for index, mode := range []string{"upgrade27", "fresh", "native_cli"} {
		t.Run(mode, func(t *testing.T) {
			name := fmt.Sprintf("workouts_deferred_test_%d_%d", os.Getpid(), index)
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" OWNER workouts_migration"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name); err != nil {
					t.Error(err)
				}
			})
			config := cfg.Copy()
			config.Database = name
			bootstrap, err := pgx.ConnectConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bootstrap.Close(context.Background()) })
			if _, err := bootstrap.Exec(ctx, `CREATE EXTENSION postgis`); err != nil {
				t.Fatal(err)
			}
			if err := bootstrap.QueryRow(ctx, `SELECT postgis_lib_version()`).Scan(&version); err != nil || version != "3.6.4" {
				t.Fatalf("requires PostGIS 3.6.4, got %q: %v", version, err)
			}
			config.User, config.Password = "workouts_migration", ""
			sqlDB := stdlib.OpenDB(*config)
			t.Cleanup(func() { _ = sqlDB.Close() })
			provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
			if err != nil {
				t.Fatal(err)
			}
			pool := func(role string) *pgxpool.Pool {
				pc, err := pgxpool.ParseConfig(dsn)
				if err != nil {
					t.Fatal(err)
				}
				pc.ConnConfig.Database, pc.ConnConfig.User, pc.ConnConfig.Password = name, role, ""
				delete(pc.ConnConfig.RuntimeParams, "role")
				pc.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
				pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "30s"
				pc.MaxConns = 6
				p, err := pgxpool.NewWithConfig(ctx, pc)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(p.Close)
				return p
			}
			db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker"), tiles: pool("workouts_tiles")}
			var before, beforeScopes string
			if mode == "upgrade27" {
				if _, err := provider.UpTo(ctx, 27); err != nil {
					t.Fatal(err)
				}
				before = deferredSecurityContract(t, db)
				beforeScopes = deferredTriggerScopes(t, db)
				t.Run("Immutable27FiveSecondFailure", func(t *testing.T) { deferredFileCoverageRace(t, db, false, 28) })
				t.Run("Immutable27SelectionDeadlock", func(t *testing.T) { deferredSelectionDeadlock(t, db) })
				t.Run("FailClosedZeroAnchorWrongTable", func(t *testing.T) {
					var definition string
					if err := bootstrap.QueryRow(ctx, `SELECT pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)`).Scan(&definition); err != nil {
						t.Fatal(err)
					}
					changed := strings.Replace(definition,
						"PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account FOR UPDATE NOWAIT;",
						"PERFORM 1 FROM app.accounts WHERE id = target_account FOR UPDATE NOWAIT;", 1)
					if changed == definition {
						t.Fatal("missing old anchor")
					}
					if _, err := bootstrap.Exec(ctx, changed); err != nil {
						t.Fatal(err)
					}
					_, err := provider.UpTo(ctx, 28)
					bootstrapSQLState(t, err, "P0001")
					var atomic bool
					if err := bootstrap.QueryRow(ctx, `SELECT (SELECT schema_version=27 AND minimum_runtime_version=26 FROM app.schema_metadata WHERE singleton)
					 AND (SELECT count(*)=28 AND max(version_id)=27 FROM public.goose_db_version WHERE is_applied)
					 AND to_regclass('app.ingest_coverage_builders') IS NULL
					 AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgfoid='app.advance_account_data_generation()'::regprocedure AND tgdeferrable)`).Scan(&atomic); err != nil || !atomic {
						t.Fatal("failed migration left partial changes", err)
					}
					if _, err := bootstrap.Exec(ctx, definition); err != nil {
						t.Fatal(err)
					}
				})
			}
			if mode == "native_cli" {
				binary := os.Getenv("PROJECT_DEFERRED_GOOSE_BIN")
				if binary == "" {
					t.Skip("PROJECT_DEFERRED_GOOSE_BIN required for native Goose CLI proof")
				}
				cliURL := url.URL{Scheme: "postgresql", User: url.User(config.User), Host: net.JoinHostPort(config.Host, fmt.Sprint(config.Port)), Path: "/" + name, RawQuery: "sslmode=disable"}
				command := exec.CommandContext(ctx, binary, "-dir", ".", "postgres", cliURL.String(), "up")
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("native Goose: %v\n%s", err, output)
				}
			} else if _, err := provider.Up(ctx); err != nil {
				t.Fatal(err)
			}
			if err := migrations.ValidateHistory(ctx, sqlDB); err != nil {
				t.Fatal(err)
			}
			var valid bool
			if err := db.migration.QueryRow(ctx, `SELECT schema_version=28 AND minimum_runtime_version=28 AND migration_baseline=27
				AND (SELECT count(*)=29 AND count(DISTINCT version_id)=29 AND min(version_id)=0 AND max(version_id)=28 AND bool_and(is_applied) FROM public.goose_db_version)
				FROM app.schema_metadata WHERE singleton`).Scan(&valid); err != nil || !valid {
				t.Fatalf("schema28/min28/marker27 exact ledger: %t %v", valid, err)
			}
			if before != "" && before != deferredSecurityContract(t, db) {
				t.Fatal("folded generation fix changed its protected OIDs, ABI, grants, RLS or policies")
			}
			if beforeScopes != "" && beforeScopes != deferredTriggerScopes(t, db) {
				t.Fatal("folded generation fix changed trigger event/UPDATE OF/WHEN scopes")
			}
			var deferred bool
			if err := db.migration.QueryRow(ctx, `SELECT
			 (SELECT count(*)=4 AND bool_and(tgdeferrable AND tginitdeferred AND tgconstraint<>0 AND tgenabled='O')
			 FROM pg_trigger WHERE tgfoid='app.advance_account_data_generation()'::regprocedure AND NOT tgisinternal)
			 AND (SELECT count(*)=1 AND bool_and(NOT tgdeferrable AND NOT tginitdeferred) FROM pg_trigger WHERE tgname='accounts_data_generation_after_insert' AND NOT tgisinternal)`).Scan(&deferred); err != nil || !deferred {
				t.Fatal("four deferred constraints and immediate seed required", err)
			}
			deferredReadiness(t, db)
			if mode == "native_cli" {
				return
			}
			for _, runtime := range []int{database.SupportedSchemaVersion} {
				t.Run(fmt.Sprintf("CoverageCommitsBeforeSourceFileRelease/Runtime%d", runtime), func(t *testing.T) { deferredFileCoverageRace(t, db, true, runtime) })
			}
			t.Run("AtomicGenerationAndCommitOrders", func(t *testing.T) { deferredAtomicWriters(t, db) })
			t.Run("CounterLockOnlyAtCommit", func(t *testing.T) { deferredCommitSerialization(t, db) })
			t.Run("ExactEventScopeAndWrap", func(t *testing.T) { deferredEventScope(t, db) })
			t.Run("SelectionMVCCAndFocusedFK", func(t *testing.T) { deferredSelections(t, db) })
			t.Run("SelectionsDuringGenuineSourceFile", func(t *testing.T) { deferredFileSelections(t, db) })
			t.Run("CommitFailureRollsBackFileAndCoverage", func(t *testing.T) { deferredCommitFailure(t, db, bootstrap) })
			t.Run("FencesStillFailClosed", func(t *testing.T) { deferredFences(t, db, bootstrap) })
			t.Run("CoverageGuardsStillEnforced", func(t *testing.T) { deferredCoverageGuards(t, db, bootstrap) })
		})
	}
}

func deferredTriggerScopes(t *testing.T, db testDatabases) string {
	t.Helper()
	var scopes string
	if err := db.migration.QueryRow(db.ctx, `SELECT jsonb_agg(jsonb_build_array(tgname,tgrelid,tgfoid,tgtype,tgattr::text,regexp_replace(tgqual::text,':location -?[0-9]+',':location 0','g'),tgenabled) ORDER BY tgname)::text
	 FROM pg_trigger WHERE tgfoid='app.advance_account_data_generation()'::regprocedure AND NOT tgisinternal`).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	return scopes
}

func deferredReadiness(t *testing.T, db testDatabases) {
	t.Helper()
	for role, pool := range map[string]*pgxpool.Pool{"workouts_api": db.api, "workouts_worker": db.worker, "workouts_coverage_worker": db.coverage} {
		assertRuntimeRole(t, db.ctx, pool, role)
		if !database.Ready(db.ctx, pool) {
			t.Fatalf("canonical cohort28 readiness failed for %s", role)
		}
	}
}

// Cohort ABIs deliberately change in 028. The generation fix itself retains its
// existing function identities, protection and counter-table authorization.
func deferredSecurityContract(t *testing.T, db testDatabases) string {
	t.Helper()
	var result string
	if err := db.migration.QueryRow(db.ctx, `SELECT jsonb_build_array(
	 (SELECT jsonb_agg(jsonb_build_array(p.oid,p.proname,p.proargtypes::text,p.prorettype,p.proretset,p.proowner,p.proacl::text,p.prosecdef,p.proconfig,p.provolatile,p.proparallel,p.proisstrict,p.proleakproof,
	 CASE WHEN p.proname IN ('fence_ingest_job','lock_account_data_generation') THEN NULL ELSE p.prosrc END) ORDER BY p.oid)
	 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='app'
	 AND p.proname IN ('advance_account_data_generation','seed_account_data_generation','lock_account_data_generation','fence_ingest_job')),
	 (SELECT jsonb_agg(jsonb_build_array(c.oid,c.relowner,c.relacl::text,c.relrowsecurity,c.relforcerowsecurity) ORDER BY c.oid)
	 FROM pg_class c WHERE c.oid='app.account_data_generations'::regclass),
	 (SELECT jsonb_agg(to_jsonb(p) ORDER BY p.oid) FROM pg_policy p WHERE p.polrelid='app.account_data_generations'::regclass))::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func deferredBeginFile(t *testing.T, db testDatabases, f providerIdentitySource, runtime int) (pgx.Tx, uuid.UUID) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,$4)`, f.job, providerIdentityWorker, f.lease, runtime).Scan(&source); err != nil || source != f.source {
		t.Fatalf("runtime%d file fence: %s %v", runtime, source, err)
	}
	providerIdentityExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at)
	 VALUES($1,$2,$3,$4,$1::uuid::text,2,'processing',clock_timestamp())`, f.file, f.account, f.source, f.job)
	workout := uuid.New()
	providerIdentityInsert(t, db, tx, f, workout, workout.String(), nil, nil)
	providerIdentityExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
	 VALUES($1,$2,0,'2026-01-01T00:00:00Z',37,-122),($1,$2,1,'2026-01-01T00:00:01Z',37.0001,-122.0001)`, f.account, workout)
	return tx, workout
}

func deferredFinishFile(t *testing.T, db testDatabases, tx pgx.Tx, f providerIdentitySource, workout uuid.UUID) {
	t.Helper()
	providerIdentityExec(t, db, tx, `SELECT app.replace_workout_route_summary($1,2,-122.0001,37,-122,37.0001,NULL,NULL,NULL,false)`, workout)
	providerIdentityEvent(t, db, tx, f, workout, "created")
	providerIdentityExec(t, db, tx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=clock_timestamp() WHERE id=$1`, f.file, bootstrapDigest())
	var recorded bool
	if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,'2026-01-01',2,NULL,'synthetic-v1',$5)`,
		f.job, providerIdentityWorker, f.lease, f.file.String(), bootstrapDigest()).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("file checkpoint: %t %v", recorded, err)
	}
}

type deferredClaim struct {
	f                           providerIdentitySource
	workout, job, parent, lease uuid.UUID
}

func deferredCoverage(t *testing.T, db testDatabases, runtime int) deferredClaim {
	t.Helper()
	f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
	tx, workout := deferredBeginFile(t, db, f, runtime)
	deferredFinishFile(t, db, tx, f, workout)
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	bootstrapExec(t, db, bootstrapFixture{account: f.account}, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
	 VALUES($1,$2,1,$3,'map_data_ready',clock_timestamp())`, f.account, workout, bootstrapDigest())
	pipelineEnqueue(t, db, bootstrapFixture{account: f.account}, []uuid.UUID{workout}, "geofabrik:test", 1)
	c := deferredClaim{f: f, workout: workout, lease: uuid.New()}
	tx = beginAccount(t, db.ctx, db.coverage, f.account)
	defer tx.Rollback(db.ctx)
	var account, claimedWorkout uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT job_id,parent_job_id,workout_id,account_id FROM app.claim_next_coverage_route('deferred-matcher',$1,interval '2 minutes',$2)`, c.lease, runtime).
		Scan(&c.job, &c.parent, &claimedWorkout, &account); err != nil || account != f.account || claimedWorkout != workout {
		t.Fatalf("runtime%d Coverage claim: %v", runtime, err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return c
}

const deferredPersistSQL = `SELECT app.persist_coverage_route($1,'deferred-matcher',$2,1,'[{"regionId":"geofabrik:test","generation":1}]',$3::jsonb)`

func deferredMatches() string {
	return fmt.Sprintf(`[{"physicalSegmentId":"%s","regionId":"geofabrik:test","generation":1,"derivationVersion":3,"logicalPathId":"%s","sourceWayId":42,"sourceWayVersion":1,"highway":"path","broadClass":"road","tags":{},"segmentMeters":15,"segmentGeometry":{"type":"LineString","coordinates":[[-122,37],[-122.0001,37.0001]]},"firstTraversedAt":"2026-01-01T00:00:00Z","firstRouteOrder":0,"coveredGeometry":{"type":"MultiLineString","coordinates":[[[-122,37],[-122.0001,37.0001]]]}}]`, uuid.New(), uuid.New())
}

func deferredFileCoverageRace(t *testing.T, db testDatabases, fixed bool, runtime int) {
	c := deferredCoverage(t, db, runtime)
	f := c.f
	f.file = uuid.New()
	file, workout := deferredBeginFile(t, db, f, runtime)
	defer file.Rollback(db.ctx)
	// This is the long source-file pause: a genuine fence and early workout/GPS
	// writes have completed, and source/parent/child/file locks remain held.
	for _, lock := range []struct {
		table string
		id    uuid.UUID
	}{{"sources", f.source}, {"jobs", f.parent}, {"jobs", f.job}} {
		probe := beginAccount(t, db.ctx, db.migration, f.account)
		_, err := probe.Exec(db.ctx, `SELECT 1 FROM app.`+lock.table+` WHERE id=$1 FOR UPDATE NOWAIT`, lock.id)
		bootstrapSQLState(t, err, "55P03")
		_ = probe.Rollback(db.ctx)
	}
	cov := beginAccount(t, db.ctx, db.coverage, f.account)
	defer cov.Rollback(db.ctx)
	providerIdentityExec(t, db, cov, `SET LOCAL lock_timeout='5s'`)
	started := time.Now()
	var outcome string
	err := cov.QueryRow(db.ctx, deferredPersistSQL, c.job, c.lease, deferredMatches()).Scan(&outcome)
	if !fixed {
		bootstrapSQLState(t, err, "55P03")
		if elapsed := time.Since(started); elapsed < 4900*time.Millisecond || elapsed > 6500*time.Millisecond {
			t.Fatalf("old failure must be the five-second generation lock timeout, elapsed=%s", elapsed)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || !strings.Contains(pgErr.Where, "advance_account_data_generation") {
			t.Fatalf("baseline failed somewhere other than the generation trigger: %v", err)
		}
		t.Log("immutable schema27 reproduces 55P03 in advance_account_data_generation after five seconds")
		return
	}
	if err != nil || outcome != "applied" {
		t.Fatalf("Coverage persistence while source paused: %q %v", outcome, err)
	}
	if err := cov.Commit(db.ctx); err != nil {
		t.Fatal("Coverage COMMIT must succeed before releasing source file:", err)
	}
	elapsed := time.Since(started)
	if elapsed >= 5*time.Second {
		t.Fatalf("Coverage waited for source file: %s", elapsed)
	}
	var current bool
	if err := db.migration.QueryRow(db.ctx, `SELECT processing_state='current' AND applied_route_input_revision=1
	 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, c.workout).Scan(&current); err != nil || !current {
		t.Fatalf("Coverage did not become current before ingest release: %t %v", current, err)
	}
	deferredFinishFile(t, db, file, f, workout)
	if err := file.Commit(db.ctx); err != nil {
		t.Fatal("paused file must still commit:", err)
	}
	t.Logf("runtime%d Coverage committed evidence/current state in %s before runtime%d ingest file released", runtime, elapsed, runtime)
}

func deferredGeneration(t *testing.T, ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, account uuid.UUID) int64 {
	t.Helper()
	var generation int64
	if err := q.QueryRow(ctx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, account).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func deferredAtomicWriters(t *testing.T, db testDatabases) {
	// Only this same-transaction counter inspection uses the migration fixture
	// role; the worker deliberately has no direct counter SELECT grant.
	db.worker = db.migration
	f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
	before := deferredGeneration(t, db.ctx, db.migration, f.account)
	if before != 1 {
		t.Fatal("account seed must be immediate before domain writes")
	}
	tx, workout := deferredBeginFile(t, db, f, 28)
	deferredFinishFile(t, db, tx, f, workout)
	if got := deferredGeneration(t, db.ctx, tx, f.account); got != before {
		t.Fatalf("writer sees generation before commit: %d != %d", got, before)
	}
	if got := deferredGeneration(t, db.ctx, db.migration, f.account); got != before {
		t.Fatal("uncommitted counter escaped")
	}
	var hidden bool
	reader := beginAccount(t, db.ctx, db.migration, f.account)
	defer reader.Rollback(db.ctx)
	if err := reader.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.workouts WHERE id=$1) AND NOT EXISTS(SELECT 1 FROM app.source_objects WHERE source_id=$2)`, workout, f.source).Scan(&hidden); err != nil || !hidden {
		t.Fatal("uncommitted file escaped", err)
	}
	if err := tx.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	if got := deferredGeneration(t, db.ctx, db.migration, f.account); got != before {
		t.Fatal("rollback advanced generation")
	}
	tx, workout = deferredBeginFile(t, db, f, 28)
	deferredFinishFile(t, db, tx, f, workout)
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	// Header insert and route insert; type INSERT is intentionally not an event.
	if got := deferredGeneration(t, db.ctx, db.migration, f.account); got != before+2 {
		t.Fatalf("committed file event sum=%d, want %d", got, before+2)
	}
	bootstrapExec(t, db, bootstrapFixture{account: f.account}, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state) VALUES($1,$2,1,$3,'unresolved')`, f.account, workout, bootstrapDigest())
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse_%t", reverse), func(t *testing.T) {
			before := deferredGeneration(t, db.ctx, db.migration, f.account)
			a := beginAccount(t, db.ctx, db.migration, f.account)
			b := beginAccount(t, db.ctx, db.migration, f.account)
			defer a.Rollback(db.ctx)
			defer b.Rollback(db.ctx)
			providerIdentityExec(t, db, a, `SELECT * FROM app.fence_ingest_job($1,$2,$3,28)`, f.job, providerIdentityWorker, f.lease)
			providerIdentityExec(t, db, a, `UPDATE app.workout_types SET provider_label=provider_label WHERE account_id=$1`, f.account)
			providerIdentityExec(t, db, b, `UPDATE app.workout_coverage_states SET processing_state='current',processing_finished_at=clock_timestamp() WHERE account_id=$1 AND workout_id=$2`, f.account, workout)
			if deferredGeneration(t, db.ctx, a, f.account) != before || deferredGeneration(t, db.ctx, b, f.account) != before {
				t.Fatal("concurrent writers advanced early")
			}
			first, second := a, b
			if reverse {
				first, second = b, a
			}
			if err := first.Commit(db.ctx); err != nil {
				t.Fatal(err)
			}
			if got := deferredGeneration(t, db.ctx, db.migration, f.account); got != before+1 {
				t.Fatalf("first commit=%d", got)
			}
			if err := second.Commit(db.ctx); err != nil {
				t.Fatal(err)
			}
			if got := deferredGeneration(t, db.ctx, db.migration, f.account); got != before+2 {
				t.Fatalf("lost update: %d", got)
			}
		})
	}
}

func deferredEventScope(t *testing.T, db testDatabases) {
	seed := uuid.New()
	seedTx := beginAccount(t, db.ctx, db.migration, seed)
	defer seedTx.Rollback(db.ctx)
	providerIdentityExec(t, db, seedTx, `INSERT INTO app.accounts(id) VALUES($1)`, seed)
	if deferredGeneration(t, db.ctx, seedTx, seed) != 1 {
		t.Fatal("account must seed a generation before its first commit")
	}
	if err := seedTx.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	c := deferredCoverage(t, db, 28)
	f := bootstrapFixture{account: c.f.account, workouts: []uuid.UUID{c.workout}}
	write := func(query string) {
		t.Helper()
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		defer tx.Rollback(db.ctx)
		providerIdentityExec(t, db, tx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,28)`, c.f.job, providerIdentityWorker, c.f.lease)
		providerIdentityExec(t, db, tx, query, f.account)
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}
	before := deferredGeneration(t, db.ctx, db.migration, f.account)
	bootstrapExec(t, db, f, `UPDATE app.workout_coverage_states SET updated_at=clock_timestamp() WHERE account_id=$1`, f.account)
	write(`UPDATE app.workout_types SET created_at=created_at WHERE account_id=$1`)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != before {
		t.Fatal("non-invalidating writes advanced generation")
	}
	bootstrapExec(t, db, f, `UPDATE app.workout_coverage_states SET processing_state='current',processing_finished_at=clock_timestamp() WHERE account_id=$1`, f.account)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != before+1 {
		t.Fatal("current transition did not invalidate once")
	}
	bootstrapExec(t, db, f, `UPDATE app.workout_coverage_states SET processing_finished_at=processing_finished_at WHERE account_id=$1`, f.account)
	bootstrapExec(t, db, f, `UPDATE app.workout_coverage_states SET processing_state='queued',processing_finished_at=clock_timestamp() WHERE account_id=$1`, f.account)
	bootstrapExec(t, db, f, `DELETE FROM app.workout_coverage_states WHERE account_id=$1`, f.account)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != before+1 {
		t.Fatal("017 coverage current-only policy broadened")
	}
	write(`UPDATE app.workout_types SET provider_label=provider_label WHERE account_id=$1`)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != before+2 {
		t.Fatal("UPDATE OF same-value event was lost")
	}
	write(`UPDATE app.workout_types SET type_key=type_key WHERE account_id=$1`)
	write(`UPDATE app.workouts SET provider_label=provider_label WHERE account_id=$1`)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != before+4 {
		t.Fatal("type-key/header UPDATE events were lost")
	}
	bootstrapExec(t, db, f, `UPDATE app.account_data_generations SET generation=9223372036854775807 WHERE account_id=$1`, f.account)
	write(`UPDATE app.workout_routes SET point_count=point_count WHERE account_id=$1`)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != 1 {
		t.Fatal("signed bigint wrap changed")
	}
	write(`DELETE FROM app.workout_routes WHERE account_id=$1`)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != 2 {
		t.Fatal("route DELETE did not invalidate")
	}
	write(`INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
	 SELECT account_id,id,2,-122.0001,37,-122,37.0001,false,ST_GeomFromText('MULTILINESTRING((-122 37,-122.0001 37.0001))',4326) FROM app.workouts WHERE account_id=$1`)
	if deferredGeneration(t, db.ctx, db.migration, f.account) != 3 {
		t.Fatal("route INSERT did not invalidate")
	}
	providerIdentityDeleteWorkout(t, db, c.f, c.workout, true)
	// The deletion marker UPDATE, route DELETE and workout DELETE each count.
	if deferredGeneration(t, db.ctx, db.migration, f.account) != 6 {
		t.Fatal("durable deletion event sum changed")
	}
}

func deferredCommitSerialization(t *testing.T, db testDatabases) {
	f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
	before := deferredGeneration(t, db.ctx, db.migration, f.account)
	holder := beginAccount(t, db.ctx, db.migration, f.account)
	defer holder.Rollback(db.ctx)
	var holderPID, writerPID int
	if err := holder.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	providerIdentityExec(t, db, holder, `SELECT 1 FROM app.account_data_generations WHERE account_id=$1 FOR UPDATE`, f.account)
	file, workout := deferredBeginFile(t, db, f, 28)
	deferredFinishFile(t, db, file, f, workout)
	if err := file.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- file.Commit(db.ctx) }()
	deferredWaitBlocked(t, db, writerPID, holderPID)
	var atomic bool
	reader := beginAccount(t, db.ctx, db.migration, f.account)
	defer reader.Rollback(db.ctx)
	if err := reader.QueryRow(db.ctx, `SELECT generation=$2 AND NOT EXISTS(SELECT 1 FROM app.workouts WHERE id=$3)
	 AND NOT EXISTS(SELECT 1 FROM app.source_objects WHERE source_id=$4) FROM app.account_data_generations WHERE account_id=$1`, f.account, before, workout, f.source).Scan(&atomic); err != nil || !atomic {
		t.Fatal("data/counter escaped blocked commit", err)
	}
	if err := holder.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow(db.ctx, `SELECT generation=$2 AND EXISTS(SELECT 1 FROM app.workouts WHERE id=$3)
	 AND EXISTS(SELECT 1 FROM app.source_objects WHERE source_id=$4) FROM app.account_data_generations WHERE account_id=$1`, f.account, before+2, workout, f.source).Scan(&atomic); err != nil || !atomic {
		t.Fatal("committed data/counter not atomic", err)
	}
}

func deferredCoverageGuards(t *testing.T, db testDatabases, admin *pgx.Conn) {
	for _, change := range []string{"child_cancel", "parent_cancel", "lease", "expired"} {
		t.Run(change, func(t *testing.T) {
			c := deferredCoverage(t, db, 28)
			before := deferredGeneration(t, db.ctx, db.migration, c.f.account)
			switch change {
			case "child_cancel", "parent_cancel":
				job := c.job
				if change == "parent_cancel" {
					job = c.parent
				}
				if !callBool(t, db.ctx, db.api, c.f.account, `SELECT app.request_owned_job_cancellation($1,$2)`, job, c.f.requester) {
					t.Fatal("cancel Coverage fixture")
				}
			case "lease":
				c.lease = uuid.New()
			case "expired":
				tx, err := admin.Begin(db.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(db.ctx)
				providerIdentityExec(t, db, tx, `SET LOCAL session_replication_role='replica'`)
				providerIdentityExec(t, db, tx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, c.job)
				if err := tx.Commit(db.ctx); err != nil {
					t.Fatal(err)
				}
			}
			tx := beginAccount(t, db.ctx, db.coverage, c.f.account)
			defer tx.Rollback(db.ctx)
			var result *string
			if err := tx.QueryRow(db.ctx, deferredPersistSQL, c.job, c.lease, deferredMatches()).Scan(&result); err != nil || result != nil {
				t.Fatalf("invalid Coverage persistence accepted: %v %v", result, err)
			}
			if err := tx.Commit(db.ctx); err != nil {
				t.Fatal(err)
			}
			if deferredGeneration(t, db.ctx, db.migration, c.f.account) != before {
				t.Fatal("invalid Coverage job invalidated map")
			}
			var empty bool
			if err := admin.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.workout_segment_matches WHERE account_id=$1)`, c.f.account).Scan(&empty); err != nil || !empty {
				t.Fatal("invalid Coverage job wrote evidence", err)
			}
		})
	}
}

func deferredCommitFailure(t *testing.T, db testDatabases, admin *pgx.Conn) {
	c := deferredCoverage(t, db, 28)
	f := c.f
	f.file = uuid.New()
	before := deferredGeneration(t, db.ctx, db.migration, f.account)
	const fileSnapshotSQL = `SELECT jsonb_build_array(
	 (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM app.source_files t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.relative_name) FROM app.source_objects t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t)) FROM app.source_sync_state t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t)) FROM app.job_progress t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM app.workout_import_events t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM app.workout_types t WHERE account_id=$1))::text`
	var fileBefore, fileAfter string
	if err := admin.QueryRow(db.ctx, fileSnapshotSQL, f.account).Scan(&fileBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(db.ctx, `CREATE FUNCTION app.deferred_test_fail_generation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic deferred generation failure' USING ERRCODE='P0001'; END $$;
	 CREATE TRIGGER deferred_test_fail_generation BEFORE UPDATE ON app.account_data_generations FOR EACH ROW EXECUTE FUNCTION app.deferred_test_fail_generation()`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), `DROP TRIGGER deferred_test_fail_generation ON app.account_data_generations; DROP FUNCTION app.deferred_test_fail_generation()`); err != nil {
			t.Error(err)
		}
	}()
	file, workout := deferredBeginFile(t, db, f, 28)
	deferredFinishFile(t, db, file, f, workout)
	bootstrapSQLState(t, file.Commit(db.ctx), "P0001")
	if err := admin.QueryRow(db.ctx, fileSnapshotSQL, f.account).Scan(&fileAfter); err != nil || fileBefore != fileAfter {
		t.Fatal("failed file commit changed checkpoint/sync/progress/type/import state", err)
	}
	var rolledBack bool
	if err := admin.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.source_files WHERE id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.workouts WHERE id=$2) AND NOT EXISTS(SELECT 1 FROM app.workout_route_points WHERE workout_id=$2)
	 AND NOT EXISTS(SELECT 1 FROM app.workout_routes WHERE workout_id=$2) AND NOT EXISTS(SELECT 1 FROM app.workout_import_events WHERE source_file_id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.source_objects WHERE relative_name=$1::uuid::text)
	 AND NOT EXISTS(SELECT 1 FROM app.ingest_coverage_handoffs WHERE workout_id=$2)`, f.file, workout).Scan(&rolledBack); err != nil || !rolledBack {
		t.Fatalf("file commit failure left partial data/checkpoint: %t %v", rolledBack, err)
	}
	var snapshot, after string
	const stateSQL = `SELECT jsonb_build_array(
	 (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM app.jobs t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t) ORDER BY t.workout_id) FROM app.workout_coverage_states t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t)) FROM app.coverage_job_progress t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t)) FROM app.job_logs t WHERE account_id=$1),
	 (SELECT jsonb_agg(to_jsonb(t)) FROM app.job_events t WHERE account_id=$1))::text`
	if err := admin.QueryRow(db.ctx, stateSQL, f.account).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	cov := beginAccount(t, db.ctx, db.coverage, f.account)
	var outcome string
	if err := cov.QueryRow(db.ctx, deferredPersistSQL, c.job, c.lease, deferredMatches()).Scan(&outcome); err != nil || outcome != "applied" {
		t.Fatalf("persist before commit: %s %v", outcome, err)
	}
	bootstrapSQLState(t, cov.Commit(db.ctx), "P0001")
	if err := admin.QueryRow(db.ctx, stateSQL, f.account).Scan(&after); err != nil || snapshot != after {
		t.Fatal("Coverage failure left partial state/log/progress", err)
	}
	if err := admin.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.workout_segment_matches WHERE account_id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.workout_path_attributions WHERE account_id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.account_path_daily_rollups WHERE account_id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.account_path_all_time WHERE account_id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.path_segments WHERE account_id=$1)
	 AND NOT EXISTS(SELECT 1 FROM app.coverage_paths WHERE account_id=$1)`, f.account).Scan(&rolledBack); err != nil || !rolledBack {
		t.Fatal("Coverage evidence escaped failed commit", err)
	}
	if deferredGeneration(t, db.ctx, db.migration, f.account) != before {
		t.Fatal("failed commit advanced generation")
	}
}

func deferredFences(t *testing.T, db testDatabases, admin *pgx.Conn) {
	for _, mutation := range []string{"cancel", "parent_cancel", "lease", "expired", "source_delete", "account_scope", "missing_generation", "epoch"} {
		t.Run(mutation, func(t *testing.T) {
			f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
			switch mutation {
			case "cancel":
				if !callBool(t, db.ctx, db.api, f.account, `SELECT app.request_owned_job_cancellation($1,$2)`, f.job, f.requester) {
					t.Fatal("cancel")
				}
			case "parent_cancel":
				if !callBool(t, db.ctx, db.api, f.account, `SELECT app.request_owned_job_cancellation($1,$2)`, f.parent, f.requester) {
					t.Fatal("cancel parent")
				}
			case "lease":
				f.lease = uuid.New()
			case "expired":
				tx, err := admin.Begin(db.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(db.ctx)
				providerIdentityExec(t, db, tx, `SET LOCAL session_replication_role='replica'`)
				providerIdentityExec(t, db, tx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.job)
				if err := tx.Commit(db.ctx); err != nil {
					t.Fatal(err)
				}
			case "source_delete":
				providerIdentityDeleteSource(t, db, f)
			case "missing_generation":
				if _, err := admin.Exec(db.ctx, `DELETE FROM app.account_data_generations WHERE account_id=$1`, f.account); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				// The capability must be rejected if its captured ownership epoch is
				// stale, even in the same file transaction after a valid fence.
			}
			tx := beginAccount(t, db.ctx, db.worker, f.account)
			defer tx.Rollback(db.ctx)
			if mutation == "account_scope" {
				providerIdentityExec(t, db, tx, `SELECT set_config('app.account_id',$1,true)`, uuid.NewString())
			}
			var count int
			err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.fence_ingest_job($1,$2,$3,28)`, f.job, providerIdentityWorker, f.lease).Scan(&count)
			if mutation == "missing_generation" {
				bootstrapSQLState(t, err, "55000")
				return
			}
			if mutation != "epoch" {
				if err != nil || count != 0 {
					t.Fatalf("invalid fence returned count=%d err=%v, want no capability", count, err)
				}
				return
			}
			if err != nil || count != 1 {
				t.Fatal("valid epoch fixture fence", err)
			}
			if _, err := admin.Exec(db.ctx, `UPDATE app.accounts SET workout_ownership_epoch=workout_ownership_epoch+1 WHERE id=$1`, f.account); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(db.ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes) VALUES($1,$2,$3,$4,'stale',1)`, f.file, f.account, f.source, f.job)
			bootstrapSQLState(t, err, "55P03")
		})
	}
}
