package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestIngestRouteCountsPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_INGEST_ROUTE_COUNTS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_INGEST_ROUTE_COUNTS_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable route-counter database is required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_ingest_route_counts_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("route counters require a named loopback scratch database")
	}
	ctx := context.Background()
	setup := openWorkerTestPool(t, ctx, dsn)
	var empty bool
	if err := setup.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("route-counter scratch database must be empty")
	}
	if _, err := setup.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	keys := testKeyring(t)
	type history struct {
		name                                           string
		job                                            ingestJobFixture
		created, updated, routesCreated, routesUpdated int
	}
	histories := []history{
		{name: "matching routed events", created: 1, updated: 2, routesCreated: 1, routesUpdated: 2},
		{name: "matching empty route", created: 1, updated: 2},
		{name: "changed content", created: 1, updated: 2, routesCreated: -1, routesUpdated: 2},
		{name: "pending deletion", created: 1, updated: 2, routesCreated: -1, routesUpdated: -1},
		{name: "deleted events", created: 5, updated: 3, routesCreated: -1, routesUpdated: -1},
		{name: "retained totals exceed events", created: 5, updated: 3, routesCreated: -1, routesUpdated: -1},
		{name: "genuine zero"},
	}
	for index := range histories {
		h := &histories[index]
		fixture := insertIngestSource(t, setup, t.TempDir())
		h.job = enqueueIngestJob(t, setup, keys, fixture)
		tx := beginWorkerTestAccount(t, setup, fixture.accountID)
		defer tx.Rollback(ctx)
		// Synthetic legacy snapshots only; this connection is the disposable superuser.
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.jobs SET status='succeeded',started_at=clock_timestamp(),terminal_at=clock_timestamp() WHERE id=ANY($1::uuid[])`, []uuid.UUID{h.job.childID, h.job.parentID}); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.job_progress SET workouts_created=$2,workouts_updated=$3 WHERE job_id=ANY($1::uuid[])`, []uuid.UUID{h.job.childID, h.job.parentID}, h.created, h.updated); err != nil {
			t.Fatal(err)
		}
		if h.name != "deleted events" && h.name != "genuine zero" {
			file, workout, activity := uuid.New(), uuid.New(), uuid.New()
			for _, statement := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,checksum_sha256,processing_started_at,processed_at) VALUES($1,$2,$3,$4,'HealthAutoExport-2026-08-01.json',1,'succeeded',decode(repeat('01',32),'hex'),transaction_timestamp(),transaction_timestamp())`, []any{file, fixture.accountID, fixture.sourceID, h.job.childID}},
				{`INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, []any{activity, fixture.accountID}},
				{`INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration) VALUES($1,$2,$3,$4,$5,'synthetic',decode(repeat('01',32),'hex'),'Walking','2026-08-01T00:00:00Z','2026-08-01T00:00:00Z',0)`, []any{workout, fixture.accountID, fixture.sourceID, file, activity}},
				{`INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256) SELECT gen_random_uuid(),$1,$2,$3,$4,$5,kind,decode(repeat(CASE WHEN kind='created' AND $6 THEN '02' ELSE '01' END,32),'hex') FROM unnest(ARRAY['created','updated','adopted']) kind`, []any{fixture.accountID, fixture.sourceID, file, h.job.childID, workout, h.name == "changed content"}},
			} {
				if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
					t.Fatal(err)
				}
			}
			if h.name != "matching empty route" {
				if _, err := tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude) VALUES($1,$2,0,'2026-08-01T00:00:00Z',0,0)`, fixture.accountID, workout); err != nil {
					t.Fatal(err)
				}
			}
			if h.name == "pending deletion" {
				if _, err := tx.Exec(ctx, `UPDATE app.workouts SET deletion_requested_at=clock_timestamp(),deletion_target_id=gen_random_uuid() WHERE id=$1`, workout); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, migrationfixture.RouteCounts); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.schema_metadata WHERE schema_version=26 AND minimum_runtime_version=26`)
	assertCountQuery(t, setup, 27, `SELECT count(*) FROM public.goose_db_version WHERE is_applied AND version_id BETWEEN 0 AND 26`)
	assertCountQuery(t, setup, 5, `SELECT count(*) FROM pg_class WHERE oid=ANY(ARRAY['app.workout_import_events'::regclass,'app.workouts'::regclass,'app.workout_route_points'::regclass,'app.jobs'::regclass,'app.job_progress'::regclass]) AND relforcerowsecurity`)
	for _, h := range histories {
		t.Run("backfill/"+h.name, func(t *testing.T) {
			for _, id := range []uuid.UUID{h.job.childID, h.job.parentID} {
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=$2 AND workouts_updated=$3`, id, h.created, h.updated)
				var created, updated *int
				if err := setup.QueryRow(ctx, `SELECT workouts_created_with_route,workouts_updated_with_route FROM app.job_progress WHERE job_id=$1`, id).Scan(&created, &updated); err != nil {
					t.Fatal(err)
				}
				for _, check := range []struct {
					got  *int
					want int
				}{{created, h.routesCreated}, {updated, h.routesUpdated}} {
					if (check.want < 0 && check.got != nil) || (check.want >= 0 && (check.got == nil || *check.got != check.want)) {
						t.Fatalf("backfilled route count=%v want=%d", check.got, check.want)
					}
				}
			}
		})
	}
	var narrow bool
	if err := setup.QueryRow(ctx, `SELECT
		has_function_privilege('workouts_worker','app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)','EXECUTE')
		AND NOT has_function_privilege('workouts_api','app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)','EXECUTE')
		AND NOT has_function_privilege('workouts_worker','app.refresh_ingest_route_counts(uuid)','EXECUTE')
		AND NOT has_table_privilege('workouts_worker','app.job_progress','UPDATE')
		AND NOT has_column_privilege('workouts_worker','app.workout_import_events','has_route','UPDATE')
		AND has_column_privilege('workouts_worker','app.workout_import_events','has_route','SELECT')`).Scan(&narrow); err != nil || !narrow {
		t.Fatalf("route-counter ACLs are not narrow: %t %v", narrow, err)
	}
	if err := migrationfixture.FinishCanonical27(ctx, sqlDB, migrations.Files, migrationfixture.RouteCounts); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.schema_metadata WHERE schema_version=27 AND minimum_runtime_version=26 AND migration_baseline=27`)
	if _, err := provider.UpTo(ctx, database.SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	workerConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	workerConfig.ConnConfig.RuntimeParams["role"] = "workouts_worker"
	workerDB, err := pgxpool.NewWithConfig(ctx, workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	if !database.Ready(ctx, workerDB) {
		t.Fatal("current ingest pipeline runtime is not ready")
	}
	if err := ConfigureFileSlotLimits(ctx, workerDB, 2, 4); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []string{"manual", "scheduled"} {
		t.Run(trigger+" commit before progress and replay", func(t *testing.T) {
			root := t.TempDir()
			for index, points := range []int{1, 0, 3} {
				path := filepath.Join(root, fmt.Sprintf("HealthAutoExport-2026-08-%02d.json", index+1))
				writeSyntheticExport(t, path, fmt.Sprint(60+index), points)
				if index == 0 {
					addAdoptionFreshWorkout(t, path)
				}
			}
			fixture := insertIngestSource(t, setup, root)
			jobs := enqueueIngestJobKind(t, setup, keys, fixture, trigger+"_ingest", trigger+"_ingest_source")
			runner := testRunner(workerDB, keys, root, io.Discard)
			job, err := runner.claimNext(ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			files, err := discoverSourceFiles(directory)
			_ = directory.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.resolveCandidateActions(ctx, job, fixture.sourceID, files); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.recordFileManifest(ctx, job, files); err != nil {
				t.Fatal(err)
			}
			// Exercise the unchanged schema-26 helper on a fresh job before journaling.
			tx := beginWorkerTestAccount(t, workerDB, fixture.accountID)
			var recorded bool
			if err := tx.QueryRow(ctx, `SELECT app.record_ingest_progress($1,$2,$3,3,0,0,0,0,0,0,0)`, job.id, runner.workerID, job.lease).Scan(&recorded); err != nil || !recorded {
				t.Fatalf("legacy helper: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				outcome := runner.processSourceFile(ctx, job, fixture.sourceID, root, file, nil)
				if outcome.fatal != nil || !outcome.succeeded {
					t.Fatalf("file commit: %+v", outcome)
				}
			}
			assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=0 AND workouts_updated=0`, job.id)
			invalid := newIngestResults()
			if err := runner.loadIngestProgress(ctx, job, &invalid); err != nil {
				t.Fatal(err)
			}
			*invalid.workoutsCreatedWithRoute = 2
			var rejected *pgconn.PgError
			if err := runner.recordIngestProgress(ctx, job, invalid); !errors.As(err, &rejected) || rejected.Code != "22023" {
				t.Fatalf("fabricated route count accepted: %v", err)
			}
			assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=0 AND workouts_updated=0`, job.id)
			stale := job
			stale.lease = uuid.New()
			if err := runner.recordIngestProgress(ctx, stale, invalid); !errors.Is(err, errLeaseLost) {
				t.Fatalf("stale route progress accepted: %v", err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				results := newIngestResults()
				if err := runner.loadIngestProgress(ctx, job, &results); err != nil {
					t.Fatal(err)
				}
				if results.workoutsCreated != 2 || results.workoutsUpdated != 2 || results.workoutsCreatedWithRoute == nil || *results.workoutsCreatedWithRoute != 1 || results.workoutsUpdatedWithRoute == nil || *results.workoutsUpdatedWithRoute != 1 {
					t.Fatalf("replayed counters: %+v", results)
				}
				if err := runner.recordIngestProgress(ctx, job, results); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runner.executeIngest(ctx, job); err != nil {
				t.Fatal(err)
			}
			for _, id := range []uuid.UUID{jobs.childID, jobs.parentID} {
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=2 AND workouts_updated=2 AND workouts_created_with_route=1 AND workouts_updated_with_route=1`, id)
			}
			assertCountQuery(t, setup, 4, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND has_route IS NOT NULL`, job.id)
			// A bounded replay updates two older snapshots, then matches the latest;
			// these are events, even though there is only one routed workout ID.
			repeat := enqueueIngestJobKind(t, setup, keys, fixture, trigger+"_ingest", trigger+"_ingest_source")
			assertRanIngest(t, runner, ctx)
			assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created_with_route=0 AND workouts_updated=3 AND workouts_updated_with_route=2 AND workouts_unchanged=1`, repeat.childID)
			for _, file := range files[:2] {
				if err := os.Remove(filepath.Join(root, file.name)); err != nil {
					t.Fatal(err)
				}
			}
			unchanged := enqueueIngestJobKind(t, setup, keys, fixture, trigger+"_ingest", trigger+"_ingest_source")
			assertRanIngest(t, runner, ctx)
			assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged' AND has_route`, unchanged.childID)
			assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_unchanged=1 AND workouts_created_with_route=0 AND workouts_updated_with_route=0`, unchanged.childID)

			// Model a schema-26 writer's committed event, which omits has_route.
			legacy := enqueueIngestJobKind(t, setup, keys, fixture, trigger+"_ingest", trigger+"_ingest_source")
			legacyJob, err := runner.claimNext(ctx)
			if err != nil || legacyJob.id != legacy.childID {
				t.Fatalf("claim legacy caller: %v", err)
			}
			seed := beginWorkerTestAccount(t, setup, fixture.accountID)
			defer seed.Rollback(ctx)
			if _, err := seed.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
				t.Fatal(err)
			}
			fileID := uuid.New()
			if _, err := seed.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,checksum_sha256,processing_started_at,processed_at)
				VALUES($1,$2,$3,$4,'HealthAutoExport-2026-08-01.json',1,'succeeded',decode(repeat('01',32),'hex'),transaction_timestamp(),transaction_timestamp())`, fileID, fixture.accountID, fixture.sourceID, legacy.childID); err != nil {
				t.Fatal(err)
			}
			if _, err := seed.Exec(ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256)
				SELECT gen_random_uuid(),account_id,source_id,$2,$3,id,'updated',content_sha256 FROM app.workouts WHERE account_id=$1 AND provider_id='synthetic-provider'`, fixture.accountID, fileID, legacy.childID); err != nil {
				t.Fatal(err)
			}
			if err := seed.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			tx = beginWorkerTestAccount(t, workerDB, fixture.accountID)
			defer tx.Rollback(ctx)
			if err := tx.QueryRow(ctx, `SELECT app.record_ingest_progress($1,$2,$3,1,0,1,0,0,1,0,0)`, legacyJob.id, runner.workerID, legacyJob.lease).Scan(&recorded); err != nil || !recorded {
				t.Fatalf("legacy unknown snapshot: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			for _, id := range []uuid.UUID{legacy.childID, legacy.parentID} {
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created_with_route=0 AND workouts_updated_with_route IS NULL`, id)
			}
			results := newIngestResults()
			if err := runner.loadIngestProgress(ctx, legacyJob, &results); err != nil || results.workoutsUpdatedWithRoute != nil {
				t.Fatalf("legacy replay fabricated route snapshot: %+v %v", results, err)
			}
			if err := runner.recordIngestProgress(ctx, legacyJob, results); err != nil {
				t.Fatal(err)
			}
			if finished, err := runner.finishJob(ctx, legacyJob, "succeeded", nil); err != nil || !finished {
				t.Fatalf("finish legacy fixture: %v", err)
			}
		})
	}
}
