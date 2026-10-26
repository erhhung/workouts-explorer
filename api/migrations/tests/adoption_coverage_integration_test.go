package migrations_test

import (
	"context"
	"errors"
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

// Only an empty, explicitly disposable loopback PostGIS database is accepted.
// These tests never fall back to application database URLs or cloud resources.
func TestAdoptionCoverageIntegration(t *testing.T) {
	dsn := os.Getenv("ADOPTION_COVERAGE_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("ADOPTION_COVERAGE_TEST_DISPOSABLE") != "1" {
		t.Skip("ADOPTION_COVERAGE_TEST_DATABASE_URL and ADOPTION_COVERAGE_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_adoption_coverage_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("adoption Coverage tests require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("scratch database must be empty")
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
	if _, err := provider.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if !t.Run("CompactFunctionUpgradePreservesContracts", func(t *testing.T) {
		adoptionCoverageUpgradeCompactFunctions(t, ctx, admin, provider)
	}) {
		return
	}
	pool := func(role string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
		cfg.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"),
		worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	t.Run("SecurityAndRuntimeFloor", func(t *testing.T) {
		var valid bool
		if err := db.migration.QueryRow(ctx, `SELECT schema_version=26 AND minimum_runtime_version=26 FROM app.schema_metadata WHERE singleton`).Scan(&valid); err != nil || !valid {
			t.Fatalf("schema metadata valid=%v err=%v", valid, err)
		}
		for signature, workerOnly := range map[string]bool{
			"app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid)":                                                                                 true,
			"app.read_pending_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid)":                                                                         true,
			"app.read_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid,bigint,uuid,integer)":                                                             false,
			"app.read_adopted_workout_coverage_route(uuid,uuid,text,uuid,bigint,uuid,uuid)":                                                                    false,
			"app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)": false,
		} {
			if err := db.migration.QueryRow(ctx, `SELECT pg_get_userbyid(proowner)='workouts_security_owner' AND prosecdef
				AND proconfig=ARRAY['search_path=pg_catalog, app']
				AND has_function_privilege('workouts_worker',oid,'EXECUTE')
				AND has_function_privilege('workouts_coverage_worker',oid,'EXECUTE')=$2
				AND NOT has_function_privilege('workouts_api',oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_tiles',oid,'EXECUTE')
				AND NOT EXISTS(SELECT 1 FROM aclexplode(proacl) WHERE grantee=0)
				FROM pg_proc WHERE oid=$1::regprocedure`, signature, !workerOnly).Scan(&valid); err != nil || !valid {
				t.Fatalf("helper security %s valid=%v err=%v", signature, valid, err)
			}
		}
		for _, role := range []string{"workouts_worker", "workouts_coverage_worker", "workouts_api", "workouts_tiles"} {
			if err := db.migration.QueryRow(ctx, `SELECT NOT has_table_privilege($1,'app.adopted_workout_coverage_refreshes','SELECT,INSERT,UPDATE,DELETE')`, role).Scan(&valid); err != nil || !valid {
				t.Fatalf("direct outbox access granted to %s", role)
			}
		}
		for _, runtime := range []any{nil, 23, 25} {
			_, err := db.coverage.Exec(ctx, `SELECT * FROM app.claim_next_coverage_route('old-worker',$1,interval '1 minute',$2)`, uuid.New(), runtime)
			bootstrapSQLState(t, err, "55000")
		}
		if err := db.migration.QueryRow(ctx, `SELECT relrowsecurity AND relforcerowsecurity
			AND (SELECT count(*)=1 AND bool_and(confrelid='app.workouts'::regclass)
			FROM pg_constraint WHERE conrelid=relation.oid AND contype='f')
			FROM pg_class relation WHERE oid='app.adopted_workout_coverage_refreshes'::regclass`).Scan(&valid); err != nil || !valid {
			t.Fatalf("outbox RLS/workout-only FK valid=%v err=%v", valid, err)
		}
		for role, pool := range map[string]*pgxpool.Pool{"api": db.api, "ingest": db.worker, "coverage": db.coverage} {
			if database.Ready(ctx, pool) {
				t.Fatalf("historical schema-26 %s role must not be current-runtime ready", role)
			}
		}
		// Keep this historical ABI suite at schema26. Current-runtime readiness and
		// the raised ingest minimum are exercised by the schema28 pipeline suites.
	})
	t.Run("CrashAfterFileCommitAndUnchangedDigest", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event, job, lease := adoptionCommitFile(t, db, f)
		// Simulate process death before repair and a terminal ingest job. Replay
		// must need only the durable account reconciliation lease, not job life.
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'adoption-ingest',$2,'succeeded',NULL,NULL)`, job, lease) {
			t.Fatal("complete ingest job")
		}
		adoptionClaim(t, db, &f)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("crash recovery rejected")
		}
		bootstrapAssertState(t, db, f, 1, bootstrapDigest())
		adoptionAssertCounts(t, db, f, 0, 1)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("idempotent replay rejected")
		}
		adoptionAssertCounts(t, db, f, 0, 1)
		// A second unchanged adoption is a distinct obligation, not a new raw revision.
		second := adoptionSeed(t, db, f)
		if !adoptionApply(t, db, f, second, "map_data_ready", bootstrapDigest()) {
			t.Fatal("second adoption rejected")
		}
		bootstrapAssertState(t, db, f, 1, bootstrapDigest())
		adoptionAssertCounts(t, db, f, 0, 2)
		var parentsHealthy bool
		if err := db.migration.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.jobs parent WHERE parent.account_id=$1
			AND parent.kind='coverage_update' AND parent.status IN ('queued','running') AND NOT EXISTS(
			SELECT 1 FROM app.jobs child WHERE child.parent_job_id=parent.id AND child.status IN ('queued','running')))`, f.account).Scan(&parentsHealthy); err != nil || !parentsHealthy {
			t.Fatalf("superseding a queued target stranded its old parent: healthy=%v err=%v", parentsHealthy, err)
		}
	})
	t.Run("FencedAdoptionCommitsAfterLeaseClockExpiry", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		parent := insertQueued(t, ctx, db.api, f.account, uuid.Nil, "manual_ingest")
		job := insertQueued(t, ctx, db.api, f.account, parent, "manual_ingest_source")
		bootstrapExec(t, db, f, `UPDATE app.sources SET deleted_at=clock_timestamp(),config_envelope=NULL,auto_sync_enabled=false
			WHERE account_id=$1 AND id=(SELECT source_id FROM app.workouts WHERE account_id=$1 AND id=$2)`, f.account, f.workouts[0])
		lease := uuid.New()
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'adoption-expiry',$2,interval '2 seconds')`, job, lease) {
			t.Fatal("claim bounded ingest lease")
		}
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'adoption-expiry',$2,26)`, job, lease).Scan(&source); err != nil {
			t.Fatal("fresh fence before expiry:", err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_sleep(GREATEST(EXTRACT(EPOCH FROM lease_expires_at-clock_timestamp()),0)+0.05)
			FROM app.jobs WHERE id=$1`, job); err != nil {
			t.Fatal(err)
		}
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT lease_expires_at<clock_timestamp() FROM app.jobs WHERE id=$1`, job).Scan(&expired); err != nil || !expired {
			t.Fatalf("lease clock did not expire: expired=%v err=%v", expired, err)
		}
		file := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at)
			VALUES($1,$2,$3,$4,'expired-adoption.json',1,'processing',clock_timestamp())`, file, f.account, source, job); err != nil {
			t.Fatal(err)
		}
		var workout uuid.UUID
		var priorDigest []byte
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,'adoption-expiry',$2,$3,$4,$5,NULL,NULL)`,
			job, lease, file, uuid.New(), f.workouts[0].String()).Scan(&workout, &priorDigest, &outcome); err != nil || workout != f.workouts[0] || outcome != "adopted" {
			t.Fatalf("post-expiry adoption workout=%s outcome=%s err=%v", workout, outcome, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.workouts SET source_id=$3,source_file_id=$4 WHERE account_id=$1 AND id=$2`,
			f.account, workout, source, file); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,warnings)
			VALUES($1,$2,$3,$4,$5,$6,'adopted',$7,'[]')`, uuid.New(), f.account, source, workout, file, job, priorDigest); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'adoption-expiry',$2,$3)`, job, lease, workout); err != nil {
			t.Fatal("record admitted adoption after lease clock expiry:", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=clock_timestamp() WHERE id=$1`, file, priorDigest); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal("commit admitted adoption after lease clock expiry:", err)
		}
		adoptionAssertCounts(t, db, f, 1, 0)
		bootstrapAssertState(t, db, f, 0, nil)
		var event uuid.UUID
		var committed bool
		check := beginAccount(t, ctx, db.migration, f.account)
		defer check.Rollback(ctx)
		if err := check.QueryRow(ctx, `SELECT request.event_id,workout.source_id=$3 AND workout.source_file_id=$4
			AND file.state='succeeded' AND EXISTS(SELECT 1 FROM app.workout_import_events
			WHERE account_id=$1 AND workout_id=$2 AND source_file_id=$4 AND job_id=$5 AND kind='adopted')
			FROM app.adopted_workout_coverage_refreshes request
			JOIN app.workouts workout ON workout.id=request.workout_id AND workout.account_id=request.account_id
			JOIN app.source_files file ON file.id=workout.source_file_id AND file.account_id=workout.account_id
			WHERE request.account_id=$1 AND request.workout_id=$2`, f.account, workout, source, file, job).Scan(&event, &committed); err != nil || !committed {
			t.Fatalf("adoption/file/event/request did not commit together: committed=%v err=%v", committed, err)
		}
		_ = check.Rollback(ctx)
		// Committed capabilities do not confer post-commit replay authority.
		tx = beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var requests int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_adopted_workout_coverage_refreshes($1,$2,'adoption-expiry',$3,NULL,NULL,100)`,
			f.account, job, lease).Scan(&requests); err != nil || requests != 0 {
			t.Fatalf("expired post-commit read returned requests=%d err=%v", requests, err)
		}
		var points int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_adopted_workout_coverage_route($1,$2,'adoption-expiry',$3,NULL,$4,$5)`,
			f.account, job, lease, workout, event).Scan(&points); err != nil || points != 0 {
			t.Fatalf("expired post-commit route read returned points=%d err=%v", points, err)
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT app.apply_adopted_workout_coverage_refresh($1,$2,'adoption-expiry',$3,NULL,$4,$5,$6,
			'map_data_ready',NULL,'[{"regionId":"geofabrik:test","generation":1}]',$7,$8,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`,
			f.account, job, lease, workout, event, bootstrapDigest(), uuid.New(), uuid.New()).Scan(&applied); err != nil || applied {
			t.Fatalf("expired post-commit apply=%v err=%v", applied, err)
		}
		_ = tx.Rollback(ctx)
		adoptionAssertCounts(t, db, f, 1, 0)
		adoptionClaim(t, db, &f)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("fresh reconciliation lease could not recover the committed obligation")
		}
		adoptionAssertCounts(t, db, f, 0, 1)
	})
	t.Run("ExpiredLeaseCannotObtainFreshIngestFence", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		parent := insertQueued(t, ctx, db.api, f.account, uuid.Nil, "manual_ingest")
		job := insertQueued(t, ctx, db.api, f.account, parent, "manual_ingest_source")
		lease := uuid.New()
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'adoption-expired-unfenced',$2,interval '1 second')`, job, lease) {
			t.Fatal("claim bounded unfenced lease")
		}
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT pg_sleep(GREATEST(EXTRACT(EPOCH FROM lease_expires_at-clock_timestamp()),0)+0.05)
			FROM app.jobs WHERE id=$1`, job); err != nil {
			t.Fatal(err)
		}
		var source uuid.UUID
		err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'adoption-expired-unfenced',$2,26)`, job, lease).Scan(&source)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expired lease obtained a fresh fence: source=%s err=%v", source, err)
		}
		_, err = tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'adoption-expired-unfenced',$2,$3)`, job, lease, f.workouts[0])
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(ctx)
		adoptionAssertCounts(t, db, f, 0, 0)
	})
	t.Run("DeferredRefreshIsHiddenFromReplayButRemainsPending", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		_, job, lease := adoptionCommitFile(t, db, f)
		adoptionClaim(t, db, &f)
		bootstrapExec(t, db, f, `UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE account_id=$1`, f.account)
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var due, pending int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.read_adopted_workout_coverage_refreshes($1,$2,'adoption-ingest',$3,NULL,NULL,100)),
			(SELECT count(*) FROM app.read_pending_adopted_workout_coverage_refreshes($1,$2,'adoption-ingest',$3))`, f.account, job, lease).Scan(&due, &pending); err != nil || due != 0 || pending != 1 {
			t.Fatalf("deferred worker visibility due=%d pending=%d err=%v", due, pending, err)
		}
		_ = tx.Rollback(ctx)
		tx = beginAccount(t, ctx, db.coverage, f.account)
		defer tx.Rollback(ctx)
		var ordinary int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.read_adopted_workout_coverage_refreshes($1,NULL,$2,$3,$4,NULL,100)),
			(SELECT count(*) FROM app.read_coverage_reconciliation_routes($1,$4,$2,$3,NULL,100))`,
			f.account, bootstrapWorker, f.lease, f.revision).Scan(&due, &ordinary); err != nil || due != 0 || ordinary != 0 {
			t.Fatalf("deferred reconciliation visibility due=%d ordinary=%d err=%v", due, ordinary, err)
		}
	})
	t.Run("MapDataWaitingSurvivesAndNoInventedGeneration", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		for _, readiness := range []string{"pending", "unavailable"} {
			if !adoptionApply(t, db, f, event, readiness, bootstrapDigest()) {
				t.Fatal("waiting readiness rejected")
			}
			adoptionAssertCounts(t, db, f, 1, 0)
		}
		var waiting bool
		if err := db.migration.QueryRow(ctx, `SELECT next_attempt_at>clock_timestamp() AND NOT EXISTS(
			SELECT 1 FROM app.workout_coverage_regions WHERE account_id=$1) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.account).Scan(&waiting); err != nil || !waiting {
			t.Fatalf("waiting obligation invalid=%v err=%v", !waiting, err)
		}
		if !callBoolNoAccount(t, ctx, db.coverage, `SELECT app.advance_coverage_reconciliation($1,$2,$3,$4,NULL,true,interval '24 hours')`,
			f.account, f.revision, bootstrapWorker, f.lease) {
			t.Fatal("complete waiting scan")
		}
		var nextAccount uuid.UUID
		if err := db.coverage.QueryRow(ctx, `SELECT account_id FROM app.claim_coverage_reconciliation('no-hot-loop',$1,interval '2 minutes',26)`, uuid.New()).Scan(&nextAccount); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("waiting outbox caused a hot claim loop: account=%s err=%v", nextAccount, err)
		}
		bootstrapExec(t, db, f, `UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at='-infinity' WHERE account_id=$1`, f.account)
		adoptionClaim(t, db, &f)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("map activation did not resume replay")
		}
		adoptionAssertCounts(t, db, f, 0, 1)
	})
	t.Run("StalePersisterLosesAuthorityWithoutChangingRawRevision", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("initial enqueue rejected")
		}
		persisterLease := uuid.New()
		var oldJob uuid.UUID
		if err := db.migration.QueryRow(ctx, `SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&oldJob); err != nil {
			t.Fatal(err)
		}
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'adoption-persister',$2,interval '2 minutes')`, oldJob, persisterLease) {
			t.Fatal("old persister claim rejected")
		}
		// Lost acknowledgement after consuming the outbox: the next normal
		// reconciliation must keep the already running target, not replace it.
		tx := beginAccount(t, ctx, db.coverage, f.account)
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT app.apply_coverage_reconciliation($1,$2,$3,$4,$5,1,$6,'map_data_ready',NULL,
			'[{"regionId":"geofabrik:test","generation":1}]',$7,$8,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`,
			f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], bootstrapDigest(), uuid.New(), uuid.New()).Scan(&applied); err != nil || !applied {
			t.Fatalf("normal reconciliation retry applied=%v err=%v", applied, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		adoptionAssertCounts(t, db, f, 0, 1)
		event = adoptionSeed(t, db, f)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("new adoption enqueue rejected")
		}
		tx = beginAccount(t, ctx, db.coverage, f.account)
		var outcome *string
		if err := tx.QueryRow(ctx, `SELECT app.persist_coverage_route($1,'adoption-persister',$2,1,
			'[{"regionId":"geofabrik:test","generation":1}]','[]')`, oldJob, persisterLease).Scan(&outcome); err != nil || outcome == nil || *outcome != "superseded" {
			t.Fatalf("old persister outcome=%v err=%v", outcome, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		bootstrapAssertState(t, db, f, 1, bootstrapDigest())
		adoptionAssertCounts(t, db, f, 0, 2)
	})
	t.Run("StaleEventAndStaleLeaseCannotClearNewRequest", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		old := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		current := adoptionSeed(t, db, f)
		if !adoptionApply(t, db, f, old, "map_data_ready", bootstrapDigest()) {
			t.Fatal("obsolete event must be a harmless no-op")
		}
		adoptionAssertCounts(t, db, f, 1, 0)
		stale := f
		stale.lease = uuid.New()
		if adoptionApply(t, db, stale, current, "map_data_ready", bootstrapDigest()) {
			t.Fatal("stale lease accepted")
		}
		adoptionAssertCounts(t, db, f, 1, 0)
	})
	t.Run("ConcurrentConsumersEnqueueOneLogicalTarget", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		first := beginAccount(t, ctx, db.coverage, f.account)
		second := beginAccount(t, ctx, db.coverage, f.account)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, tx := range []pgx.Tx{first, second} {
			go func(tx pgx.Tx) {
				<-start
				var applied bool
				err := tx.QueryRow(ctx, adoptionApplySQL, adoptionApplyArgs(f, event, "map_data_ready", bootstrapDigest())...).Scan(&applied)
				if err == nil {
					err = tx.Commit(ctx)
				} else {
					_ = tx.Rollback(ctx)
				}
				results <- err
			}(tx)
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				var sqlErr *pgconn.PgError
				if !errors.As(err, &sqlErr) || sqlErr.Code != "55P03" {
					t.Fatal(err)
				}
			}
		}
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("at-least-once retry rejected")
		}
		adoptionAssertCounts(t, db, f, 0, 1)
	})
	t.Run("EnqueueAndRequestDeletionRollBackTogether", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		tx := beginAccount(t, ctx, db.coverage, f.account)
		_, err := tx.Exec(ctx, strings.Replace(adoptionApplySQL, "coverage-path-policy-experimental-v82", "invalid-policy", 1),
			adoptionApplyArgs(f, event, "map_data_ready", bootstrapDigest())...)
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "22023")
		adoptionAssertCounts(t, db, f, 1, 0)
		bootstrapAssertState(t, db, f, 0, nil)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("valid retry rejected")
		}
		adoptionAssertCounts(t, db, f, 0, 1)
	})
	t.Run("ChangedCommittedInputCannotConsumeOldRead", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		bootstrapMutatePoints(t, db, f, `UPDATE app.workout_route_points SET altitude=12 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		tx := beginAccount(t, ctx, db.coverage, f.account)
		_, err := tx.Exec(ctx, adoptionApplySQL, adoptionApplyArgs(f, event, "map_data_ready", bootstrapDigest())...)
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "40001")
		adoptionAssertCounts(t, db, f, 1, 0)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapCurrentDigest(t, db, f)) {
			t.Fatal("current input retry rejected")
		}
	})
	t.Run("LeaseExpiryAfterMutationRollsBackEnqueueAndClear", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		bootstrapDelayTrigger(t, db, f, "AFTER", "0.8", false)
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET lease_expires_at=clock_timestamp()+interval '0.5 seconds' WHERE account_id=$1`, f.account)
		tx := beginAccount(t, ctx, db.coverage, f.account)
		_, err := tx.Exec(ctx, adoptionApplySQL, adoptionApplyArgs(f, event, "map_data_ready", bootstrapDigest())...)
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "40001")
		adoptionAssertCounts(t, db, f, 1, 0)
		bootstrapAssertState(t, db, f, 0, nil)
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET lease_expires_at=clock_timestamp()+interval '2 minutes' WHERE account_id=$1`, f.account)
	})
	t.Run("CancelledIngestCannotConsumeButReconcilerCanRecover", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event, job, lease := adoptionCommitFile(t, db, f)
		if !requestCancellation(t, db, f.account, job) {
			t.Fatal("fixture ingest cancellation rejected")
		}
		tx := beginAccount(t, ctx, db.worker, f.account)
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT app.apply_adopted_workout_coverage_refresh($1,$2,'adoption-ingest',$3,NULL,$4,$5,$6,
			'map_data_ready',NULL,'[{"regionId":"geofabrik:test","generation":1}]',$7,$8,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`,
			f.account, job, lease, f.workouts[0], event, bootstrapDigest(), uuid.New(), uuid.New()).Scan(&applied); err != nil || applied {
			t.Fatalf("cancelled ingest applied=%v err=%v", applied, err)
		}
		_ = tx.Rollback(ctx)
		adoptionAssertCounts(t, db, f, 1, 0)
		adoptionClaim(t, db, &f)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("reconciler did not recover committed adoption after ingest cancellation")
		}
	})
	t.Run("ConcurrentWorkoutLockFailsFastAndCancellationRetainsRequest", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event := adoptionSeed(t, db, f)
		adoptionClaim(t, db, &f)
		blocker := beginAccount(t, ctx, db.migration, f.account)
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM app.workouts WHERE account_id=$1 AND id=$2 FOR UPDATE`, f.account, f.workouts[0]); err != nil {
			t.Fatal(err)
		}
		tx := beginAccount(t, ctx, db.coverage, f.account)
		_, err := tx.Exec(ctx, adoptionApplySQL, adoptionApplyArgs(f, event, "map_data_ready", bootstrapDigest())...)
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "55P03")
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = db.coverage.Exec(cancelled, adoptionApplySQL, adoptionApplyArgs(f, event, "map_data_ready", bootstrapDigest())...)
		if err == nil {
			t.Fatal("cancelled replay succeeded")
		}
		adoptionAssertCounts(t, db, f, 1, 0)
		if !adoptionApply(t, db, f, event, "map_data_ready", bootstrapDigest()) {
			t.Fatal("retry after contention rejected")
		}
	})
	t.Run("NoGPSRetiresWithoutInitializingCoverage", func(t *testing.T) {
		for _, missingPoints := range []bool{false, true} {
			f := newBootstrapFixture(t, db, 1)
			event := adoptionSeed(t, db, f)
			adoptionClaim(t, db, &f)
			if missingPoints {
				bootstrapMutatePoints(t, db, f, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
			} else {
				bootstrapExec(t, db, f, `DELETE FROM app.workout_routes WHERE account_id=$1`, f.account)
			}
			if !adoptionApply(t, db, f, event, "unavailable", nil) {
				t.Fatal("missing route was not retired")
			}
			adoptionAssertCounts(t, db, f, 0, 0)
			bootstrapAssertState(t, db, f, 0, nil)
		}
	})
	t.Run("EpochChangeBeforeRequestIsTransient", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		event, job, lease := adoptionCommitFile(t, db, f)
		tx := beginAccount(t, ctx, db.worker, f.account)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'adoption-ingest',$2,26)`, job, lease); err != nil {
			t.Fatal(err)
		}
		// A source created after the fence is outside its locked source snapshot.
		newSource := uuid.New()
		bootstrapExec(t, db, f, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope)
			VALUES($1,$2,$1::uuid::text,$1::uuid::text,'health-auto-export-icloud',$3)`, newSource, f.account, []byte("synthetic"))
		bootstrapExec(t, db, f, `UPDATE app.sources SET deleted_at=clock_timestamp(),config_envelope=NULL,auto_sync_enabled=false WHERE account_id=$1 AND id=$2`, f.account, newSource)
		_, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'adoption-ingest',$2,$3)`, job, lease, f.workouts[0])
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "55P03")
		var unchanged bool
		if err := db.migration.QueryRow(ctx, `SELECT event_id=$3 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], event).Scan(&unchanged); err != nil || !unchanged {
			t.Fatalf("stale epoch replaced the committed event: unchanged=%v err=%v", unchanged, err)
		}
		tx = beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'adoption-ingest',$2,26)`, job, lease); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'adoption-ingest',$2,$3)`, job, lease, f.workouts[0]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		adoptionAssertCounts(t, db, f, 1, 0)
	})
}

const adoptionApplySQL = `SELECT app.apply_adopted_workout_coverage_refresh($1,NULL,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
	'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`

func adoptionApplyArgs(f bootstrapFixture, event uuid.UUID, readiness string, digest []byte) []any {
	var reason any
	regions := `[]`
	if readiness == "map_data_ready" {
		regions = `[{"regionId":"geofabrik:test","generation":1}]`
	} else if readiness == "pending" {
		reason = "region_not_active"
	} else {
		reason = "no_provider_region"
	}
	return []any{f.account, bootstrapWorker, f.lease, f.revision, f.workouts[0], event, digest, readiness, reason, regions, uuid.New(), uuid.New()}
}

func adoptionApply(t *testing.T, db testDatabases, f bootstrapFixture, event uuid.UUID, readiness string, digest []byte) bool {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.coverage, f.account)
	defer tx.Rollback(db.ctx)
	var applied bool
	if err := tx.QueryRow(db.ctx, adoptionApplySQL, adoptionApplyArgs(f, event, readiness, digest)...).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return applied
}

func adoptionSeed(t *testing.T, db testDatabases, f bootstrapFixture) uuid.UUID {
	t.Helper()
	event := uuid.New()
	bootstrapExec(t, db, f, `INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,event_id,source_id,source_file_id)
		SELECT account_id,id,$3,source_id,source_file_id FROM app.workouts WHERE account_id=$1 AND id=$2
		ON CONFLICT(account_id,workout_id) DO UPDATE SET event_id=EXCLUDED.event_id,next_attempt_at='-infinity'`, f.account, f.workouts[0], event)
	return event
}

func adoptionClaim(t *testing.T, db testDatabases, f *bootstrapFixture) {
	t.Helper()
	// Do not make this campaign due: the committed outbox must wake it itself.
	f.lease = uuid.New()
	var account uuid.UUID
	var cursor *uuid.UUID
	if err := db.coverage.QueryRow(db.ctx, `SELECT * FROM app.claim_coverage_reconciliation($1,$2,interval '2 minutes',26)`, bootstrapWorker, f.lease).
		Scan(&account, &f.revision, &cursor); err != nil || account != f.account || cursor != nil {
		t.Fatalf("outbox did not wake/reset campaign: account=%s cursor=%v err=%v", account, cursor, err)
	}
}

func adoptionAssertCounts(t *testing.T, db testDatabases, f bootstrapFixture, requests, children int) {
	t.Helper()
	var gotRequests, gotChildren int
	if err := db.migration.QueryRow(db.ctx, `SELECT (SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1),
		(SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')`, f.account).Scan(&gotRequests, &gotChildren); err != nil || gotRequests != requests || gotChildren != children {
		t.Fatalf("requests/children=%d/%d want=%d/%d err=%v", gotRequests, gotChildren, requests, children, err)
	}
}

func adoptionCommitFile(t *testing.T, db testDatabases, f bootstrapFixture) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
		VALUES($1,$2,1,$3,'map_data_ready',clock_timestamp())`, f.account, f.workouts[0], bootstrapDigest())
	parent := insertQueued(t, db.ctx, db.api, f.account, uuid.Nil, "manual_ingest")
	job := insertQueued(t, db.ctx, db.api, f.account, parent, "manual_ingest_source")
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,'adoption-ingest',$2,interval '2 minutes')`, job, lease) {
		t.Fatal("claim adoption ingest")
	}
	bootstrapExec(t, db, f, `UPDATE app.sources SET deleted_at=clock_timestamp(),config_envelope=NULL,auto_sync_enabled=false WHERE account_id=$1 AND id=(SELECT source_id FROM app.workouts WHERE id=$2 AND account_id=$1)`, f.account, f.workouts[0])
	var epochBefore int64
	if err := db.migration.QueryRow(db.ctx, `SELECT workout_ownership_epoch FROM app.accounts WHERE id=$1`, f.account).Scan(&epochBefore); err != nil {
		t.Fatal(err)
	}
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'adoption-ingest',$2,26)`, job, lease).Scan(&source); err != nil {
		t.Fatal(err)
	}
	file := uuid.New()
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
		VALUES($1,$2,$3,$4,'synthetic.json',1)`, file, f.account, source, job); err != nil {
		t.Fatal(err)
	}
	var workout uuid.UUID
	var digest []byte
	var outcome string
	if err := tx.QueryRow(db.ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,'adoption-ingest',$2,$3,$4,$5,NULL,NULL)`,
		job, lease, file, uuid.New(), f.workouts[0].String()).Scan(&workout, &digest, &outcome); err != nil || workout != f.workouts[0] || outcome != "adopted" {
		t.Fatalf("adoption identity: workout=%s outcome=%s err=%v", workout, outcome, err)
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.workouts SET source_id=$3,source_file_id=$4 WHERE account_id=$1 AND id=$2`, f.account, workout, source, file); err != nil {
		t.Fatal(err)
	}
	stale, err := tx.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stale.Exec(db.ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'adoption-ingest',$2,$3)`, job, uuid.New(), workout)
	bootstrapSQLState(t, err, "42501")
	if err := stale.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	blocker := beginAccount(t, db.ctx, db.migration, f.account)
	defer blocker.Rollback(db.ctx)
	if _, err := blocker.Exec(db.ctx, `SELECT 1 FROM app.accounts WHERE id=$1 FOR NO KEY UPDATE`, f.account); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(db.ctx, `SELECT 1 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2 FOR UPDATE`, f.account, workout); err != nil {
		t.Fatal(err)
	}
	fast, cancel := context.WithTimeout(db.ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := tx.Exec(fast, `SELECT app.record_adopted_workout_coverage_refresh($1,'adoption-ingest',$2,$3)`, job, lease, workout); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	var ownershipValid bool
	if err := db.migration.QueryRow(db.ctx, `SELECT source_id=$3 AND source_file_id=$4 AND provider_namespace='health-auto-export'
		AND (SELECT workout_ownership_epoch=$5 FROM app.accounts WHERE id=$1) FROM app.workouts WHERE account_id=$1 AND id=$2`,
		f.account, workout, source, file, epochBefore).Scan(&ownershipValid); err != nil || !ownershipValid {
		t.Fatalf("adoption changed the default namespace or ownership epoch: valid=%v err=%v", ownershipValid, err)
	}
	var event uuid.UUID
	if err := db.migration.QueryRow(db.ctx, `SELECT event_id FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND workout_id=$2`, f.account, workout).Scan(&event); err != nil {
		t.Fatal(err)
	}
	adoptionAssertCounts(t, db, f, 1, 0)
	return event, job, lease
}
