package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	apiapp "github.com/erhhung/workouts-explorer/api"
	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestCleanSchemaV1Upgrade(t *testing.T) {
	migrationURL := os.Getenv("UPGRADE_MIGRATION_DATABASE_URL")
	apiURL := os.Getenv("UPGRADE_API_DATABASE_URL")
	provisioningURL := os.Getenv("ROLE_PROVISIONING_DATABASE_URL")
	if migrationURL == "" || apiURL == "" || provisioningURL == "" {
		t.Skip("upgrade and role-provisioning database URLs are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", 1); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT schema_version FROM app.schema_metadata WHERE singleton`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema-v1 version=%d err=%v", version, err)
	}
	var lifecycleAbsent bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('app.authentication_principals') IS NULL`).Scan(&lifecycleAbsent); err != nil || !lifecycleAbsent {
		t.Fatalf("lifecycle schema exists before upgrade: %t err=%v", lifecycleAbsent, err)
	}
	provisioner, err := pgxpool.New(ctx, provisioningURL)
	if err != nil {
		t.Fatal(err)
	}
	defer provisioner.Close()
	if err := apiapp.ProvisionRoles(ctx, provisioner); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", 2); err != nil {
		t.Fatal(err)
	}
	var sourcesAbsent bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('app.sources') IS NULL`).Scan(&sourcesAbsent); err != nil || !sourcesAbsent {
		t.Fatalf("source schema exists at schema-v2: %t err=%v", !sourcesAbsent, err)
	}
	legacyAccount := uuid.Must(uuid.NewV7())
	legacyParent := uuid.Must(uuid.NewV7())
	legacyQueuedCheck, legacyRunningCheck := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	legacyQueuedChild, legacyRunningChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	legacyDeletion := uuid.Must(uuid.NewV7())
	legacyTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyTx.Rollback()
	if _, err := legacyTx.ExecContext(ctx, `SELECT set_config('app.account_id',$1,true)`, legacyAccount.String()); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	if _, err := legacyTx.ExecContext(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, legacyAccount); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	if _, err := legacyTx.ExecContext(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES
		($1,$5,'manual_ingest',80),
		($2,$5,'source_connection_check',100),
		($3,$5,'source_connection_check',100),
		($4,$5,'workout_deletion',50)`, legacyParent, legacyQueuedCheck, legacyRunningCheck, legacyDeletion, legacyAccount); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	if _, err := legacyTx.ExecContext(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority) VALUES
		($1,$3,$4,'manual_ingest_source',80),($2,$3,$4,'manual_ingest_source',80)`,
		legacyQueuedChild, legacyRunningChild, legacyParent, legacyAccount); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	legacyLeaseA, legacyLeaseB := uuid.New(), uuid.New()
	var claimed bool
	if err := legacyTx.QueryRowContext(ctx, `SELECT app.claim_job($1,'legacy-check-worker',$2,interval '1 hour')`, legacyRunningCheck, legacyLeaseA).Scan(&claimed); err != nil || !claimed {
		_ = legacyTx.Rollback()
		t.Fatalf("legacy running check claim=%t err=%v", claimed, err)
	}
	if err := legacyTx.QueryRowContext(ctx, `SELECT app.claim_job($1,'legacy-child-worker',$2,interval '1 hour')`, legacyRunningChild, legacyLeaseB).Scan(&claimed); err != nil || !claimed {
		_ = legacyTx.Rollback()
		t.Fatalf("legacy running child claim=%t err=%v", claimed, err)
	}
	if err := legacyTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
	var sourceSchemaReady bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=10 AND minimum_runtime_version=8
		AND EXISTS(SELECT 1 FROM pg_extension WHERE extname='postgis')
		AND to_regclass('app.sources') IS NOT NULL
		AND to_regclass('app.job_config_snapshots') IS NOT NULL
		AND to_regclass('app.source_files') IS NOT NULL
		AND to_regclass('app.workouts') IS NOT NULL
		AND to_regclass('app.workout_import_events') IS NOT NULL
		AND to_regclass('app.workout_routes') IS NOT NULL
		AND to_regclass('app.account_data_generations') IS NOT NULL
		AND to_regclass('app.map_selections') IS NOT NULL
		AND to_regclass('app.map_selection_workouts') IS NOT NULL
		AND to_regclass('app.workout_routes_route_gist_idx') IS NOT NULL
		AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='app.workout_routes'::regclass
			AND attname='route' AND NOT attisdropped)
		AND to_regprocedure('app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)') IS NOT NULL
		AND to_regclass('app.workout_deletion_targets') IS NOT NULL
		AND to_regclass('app.workout_deletion_capabilities') IS NOT NULL
		AND to_regclass('app.ingest_write_capabilities') IS NOT NULL
		AND to_regprocedure('app.valid_workout_warnings(jsonb)') IS NOT NULL
		AND to_regprocedure('app.delete_source(uuid,uuid)') IS NOT NULL
		AND to_regprocedure('app.read_worker_job_log_context(uuid,text,uuid)') IS NOT NULL
		AND to_regprocedure('app.claim_next_worker_job(text,uuid,interval,integer)') IS NOT NULL
		AND to_regclass('app.job_source_contexts') IS NOT NULL
		AND to_regclass('app.job_progress') IS NOT NULL
		AND to_regclass('app.source_objects') IS NOT NULL
		AND to_regclass('app.job_events') IS NOT NULL
		AND to_regclass('app.job_logs') IS NOT NULL
		AND to_regclass('app.notifications') IS NOT NULL
		AND to_regclass('app.auto_sync_policy') IS NOT NULL
		AND to_regclass('app.account_sync_schedules') IS NOT NULL
		AND to_regprocedure('app.read_owned_sync_schedule()') IS NOT NULL
		AND to_regprocedure('app.read_owned_job_files(uuid,integer,integer)') IS NOT NULL
		AND to_regprocedure('app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)') IS NOT NULL
		AND to_regprocedure('app.request_owned_job_cancellation(uuid,uuid)') IS NOT NULL
		AND to_regprocedure('app.create_legacy_ingest_read_models()') IS NOT NULL
		AND to_regprocedure('app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean)') IS NOT NULL
		AND to_regprocedure('app.build_segmented_workout_route(uuid,uuid)') IS NOT NULL
		AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='app.workout_routes'::regclass AND attname='route'
			AND postgis_typmod_type(atttypmod)='MultiLineString' AND postgis_typmod_srid(atttypmod)=4326)
		AND to_regprocedure('app.enqueue_workout_deletion(uuid,uuid)') IS NOT NULL
		AND to_regprocedure('app.enqueue_workout_range_deletion(date,date,uuid)') IS NOT NULL
		AND to_regprocedure('app.retry_workout_deletion(uuid,uuid,integer)') IS NOT NULL
		AND to_regprocedure('app.claim_next_workout_deletion(text,uuid,interval,integer)') IS NOT NULL
		AND to_regprocedure('app.fence_workout_deletion(uuid,text,uuid)') IS NOT NULL
		AND to_regprocedure('app.purge_workout_deletion(uuid,text,uuid)') IS NOT NULL
		AND EXISTS (SELECT 1 FROM pg_trigger
			WHERE tgname='job_config_snapshots_ingest_compatibility_after_insert' AND NOT tgisinternal)
		AND to_regprocedure('app.record_job_event(uuid,text,uuid,text,jsonb)') IS NOT NULL
		AND EXISTS (SELECT 1 FROM pg_attribute
			WHERE attrelid='app.workout_import_events'::regclass AND attname='warnings' AND NOT attisdropped)
		AND to_regclass('app.sources_active_canonical_display_name_idx') IS NOT NULL
		AND EXISTS (SELECT 1 FROM pg_attribute
			WHERE attrelid='app.sources'::regclass AND attname='canonical_display_name' AND NOT attisdropped)
		FROM app.schema_metadata WHERE singleton`).Scan(&sourceSchemaReady); err != nil || !sourceSchemaReady {
		t.Fatalf("source schema is incomplete after upgrade: ready=%t err=%v", sourceSchemaReady, err)
	}
	apiDB, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	if !database.Ready(ctx, apiDB) {
		t.Fatalf("API role is not ready after schema-v1 upgrade: %s", runtime9ReadinessDiagnostics(ctx, apiDB))
	}
	if err := goose.DownToContext(ctx, db, ".", 7); err == nil {
		t.Fatal("migration 00008 down ignored an existing workout deletion job")
	}
	cleanupTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cleanupTx.ExecContext(ctx, `SELECT set_config('app.account_id',$1,true)`, legacyAccount.String()); err != nil {
		_ = cleanupTx.Rollback()
		t.Fatal(err)
	}
	result, err := cleanupTx.ExecContext(ctx, `DELETE FROM app.jobs WHERE id=$1`, legacyDeletion)
	if err != nil {
		_ = cleanupTx.Rollback()
		t.Fatal(err)
	}
	if deleted, err := result.RowsAffected(); err != nil || deleted != 1 {
		_ = cleanupTx.Rollback()
		t.Fatalf("legacy deletion cleanup rows=%d err=%v", deleted, err)
	}
	if err := cleanupTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var narrowSyncStateRead bool
	if err := apiDB.QueryRow(ctx, `SELECT
		has_column_privilege(current_user,'app.source_sync_state','account_id','SELECT')
		AND has_column_privilege(current_user,'app.source_sync_state','source_id','SELECT')
		AND has_column_privilege(current_user,'app.source_sync_state','last_sync_started_at','SELECT')
		AND has_column_privilege(current_user,'app.source_sync_state','last_sync_succeeded_at','SELECT')
		AND has_column_privilege(current_user,'app.source_sync_state','last_new_export_discovered_at','SELECT')
		AND has_column_privilege(current_user,'app.source_sync_state','last_new_export_date','SELECT')
		AND has_column_privilege(current_user,'app.source_sync_state','stale_since','SELECT')
		AND NOT has_table_privilege(current_user,'app.source_sync_state','SELECT')`).Scan(&narrowSyncStateRead); err != nil || !narrowSyncStateRead {
		t.Fatalf("API source sync state privileges are not narrow after schema-v1 upgrade: ready=%t err=%v", narrowSyncStateRead, err)
	}
	scheduledAccount, scheduledSource := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scheduledParent, scheduledChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scheduledTx, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduledTx.Rollback(ctx)
	if _, err := scheduledTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, scheduledAccount.String()); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, scheduledAccount); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	scheduledRequester := uuid.Must(uuid.NewV7())
	scheduledIdentity := strings.ReplaceAll(scheduledRequester.String(), "-", "")
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.authentication_principals
		(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Scheduled Guard Owner')`, scheduledRequester,
		"scheduled"+scheduledIdentity, "scheduled"+scheduledIdentity+"@example.test"); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, scheduledRequester, scheduledAccount); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Scheduled Down Guard','scheduled down guard','health-auto-export-local',$3)`,
		scheduledSource, scheduledAccount, []byte(`{"encrypted":true}`)); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority)
		VALUES($1,$2,'scheduled_ingest',60)`, scheduledParent, scheduledAccount); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	scheduledParameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`,
		strings.ToUpper(strings.ReplaceAll(scheduledSource.String(), "-", "")))
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
		VALUES($1,$2,$3,'scheduled_ingest_source',60,$4)`, scheduledChild, scheduledParent, scheduledAccount, scheduledParameters); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		scheduledChild, scheduledAccount, scheduledSource, []byte(`{"snapshot":true}`)); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	var modernContexts, modernProgress int
	var modernLegacySchema6 bool
	if err := scheduledTx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM app.job_source_contexts WHERE job_id=$1),
		(SELECT count(*) FROM app.job_progress WHERE job_id IN ($1,$2)),
		(SELECT parameters ? 'legacySchema6' FROM app.jobs WHERE id=$1)`, scheduledChild, scheduledParent).
		Scan(&modernContexts, &modernProgress, &modernLegacySchema6); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if modernContexts != 0 || modernProgress != 0 || modernLegacySchema6 {
		_ = scheduledTx.Rollback(ctx)
		t.Fatalf("modern snapshot compatibility context/progress/marker=%d/%d/%t", modernContexts, modernProgress, modernLegacySchema6)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'Scheduled Down Guard','health-auto-export-local')`, scheduledChild, scheduledAccount, scheduledSource); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := scheduledTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 5); err == nil {
		t.Fatal("migration 00007 down ignored active scheduled ingest")
	}
	scheduledTx, err = apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduledTx.Rollback(ctx)
	if _, err := scheduledTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, scheduledAccount.String()); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	var scheduledCancelled bool
	if err := scheduledTx.QueryRow(ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, scheduledParent, scheduledRequester).Scan(&scheduledCancelled); err != nil || !scheduledCancelled {
		_ = scheduledTx.Rollback(ctx)
		t.Fatalf("scheduled down-guard cleanup failed: cancelled=%t err=%v", scheduledCancelled, err)
	}
	if err := scheduledTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 5); err != nil {
		t.Fatal(err)
	}
	var mergedMigrationFive bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=5
		AND pg_get_function_result('app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure)
			= 'TABLE(owner_username text, source_name text, source_type text)'
		AND pg_get_userbyid(p.proowner)='workouts_security_owner'
		AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
		AND to_regprocedure('app.assert_no_active_scheduled_ingest()') IS NOT NULL
		AND to_regprocedure('app.create_legacy_ingest_read_models()') IS NULL
		AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='job_config_snapshots_ingest_compatibility_after_insert')
		AND position('scheduled_ingest_source' in pg_get_functiondef(claim.oid))>0
		AND position('scheduled_ingest_source' in pg_get_functiondef(fence.oid))>0
		AND position('scheduled_ingest_source' in pg_get_functiondef(cleanup.oid))>0
		FROM app.schema_metadata
		JOIN pg_proc p ON p.oid='app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure
		JOIN pg_proc claim ON claim.oid='app.claim_next_worker_job_internal(text,uuid,interval,boolean)'::regprocedure
		JOIN pg_proc fence ON fence.oid='app.fence_ingest_job(uuid,text,uuid)'::regprocedure
		JOIN pg_proc cleanup ON cleanup.oid='app.clear_ingest_write_capability()'::regprocedure
		WHERE singleton`).Scan(&mergedMigrationFive); err != nil || !mergedMigrationFive {
		t.Fatalf("merged migration 00005 state is incomplete: correct=%t err=%v", mergedMigrationFive, err)
	}
	if database.Ready(ctx, apiDB) {
		t.Fatal("schema-v9 runtime reported ready at schema 5")
	}
	drainAccount, drainSource := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	drainParent, drainChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	drainTx, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer drainTx.Rollback(ctx)
	if _, err := drainTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, drainAccount.String()); err != nil {
		_ = drainTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := drainTx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, drainAccount); err != nil {
		_ = drainTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := drainTx.Exec(ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Legacy Drain','legacy drain','health-auto-export-local',$3)`,
		drainSource, drainAccount, []byte(`{"encrypted":true}`)); err != nil {
		_ = drainTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := drainTx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority)
		VALUES($1,$2,'manual_ingest',32767)`, drainParent, drainAccount); err != nil {
		_ = drainTx.Rollback(ctx)
		t.Fatal(err)
	}
	drainParameters := fmt.Sprintf(`{"sourceId":"%s","generation":1}`,
		strings.ToUpper(strings.ReplaceAll(drainSource.String(), "-", "")))
	if _, err := drainTx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
		VALUES($1,$2,$3,'manual_ingest_source',32767,$4)`, drainChild, drainParent, drainAccount, drainParameters); err != nil {
		_ = drainTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := drainTx.Exec(ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		drainChild, drainAccount, drainSource, []byte(`{"snapshot":true}`)); err != nil {
		_ = drainTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := drainTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", 6); err != nil {
		t.Fatal(err)
	}
	if !schema5APIReady(ctx, apiDB) {
		t.Fatal("schema-5 API readiness did not survive migration 6")
	}
	rolloutParent, rolloutChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rolloutKey := make([]byte, 32)
	rolloutKey[0] = 7
	legacyInsert, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyInsert.Rollback(ctx)
	if _, err := legacyInsert.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, drainAccount.String()); err != nil {
		_ = legacyInsert.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := legacyInsert.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,parameters)
		VALUES($1,$2,'manual_ingest',80,$3)`, rolloutParent, drainAccount, drainParameters); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyInsert.Exec(ctx, `INSERT INTO app.jobs
		(id,parent_job_id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key)
		VALUES($1,$2,$3,'manual_ingest_source',80,$4,1,'manual-ingest-source',$5)`,
		rolloutChild, rolloutParent, drainAccount, drainParameters, rolloutKey); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyInsert.Exec(ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		rolloutChild, drainAccount, drainSource, []byte(`{"legacy":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := legacyInsert.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	duplicate, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Rollback(ctx)
	if _, err := duplicate.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, drainAccount.String()); err != nil {
		t.Fatal(err)
	}
	duplicateParent := uuid.Must(uuid.NewV7())
	if _, err := duplicate.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,parameters)
		VALUES($1,$2,'manual_ingest',80,$3)`, duplicateParent, drainAccount, drainParameters); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.Exec(ctx, `INSERT INTO app.jobs
		(id,parent_job_id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key)
		VALUES($1,$2,$3,'manual_ingest_source',80,$4,1,'manual-ingest-source',$5)`,
		uuid.Must(uuid.NewV7()), duplicateParent, drainAccount, drainParameters, rolloutKey); err == nil {
		t.Fatal("duplicate schema-5 child coalescing key created a second active job")
	}
	_ = duplicate.Rollback(ctx)
	if err := goose.UpToContext(ctx, db, ".", 7); err != nil {
		t.Fatal(err)
	}
	oldClaim, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer oldClaim.Rollback()
	if _, err := oldClaim.ExecContext(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		_ = oldClaim.Rollback()
		t.Fatal(err)
	}
	if _, err := oldClaim.ExecContext(ctx, `SELECT * FROM app.claim_next_worker_job('schema5-rollout-worker',$1,interval '1 minute')`, uuid.New()); err == nil {
		_ = oldClaim.Rollback()
		t.Fatal("schema-5 worker claim was accepted after migration 6")
	}
	_ = oldClaim.Rollback()
	var drainContextRows int
	var drainMode string
	var drainLegacySchema6 bool
	drainInspect, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer drainInspect.Rollback(ctx)
	if _, err := drainInspect.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, drainAccount.String()); err != nil {
		_ = drainInspect.Rollback(ctx)
		t.Fatal(err)
	}
	if err := drainInspect.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM app.job_source_contexts WHERE job_id=$1),parameters->>'mode',parameters->'legacySchema6'='true'::jsonb
		FROM app.jobs WHERE id=$1`, drainChild).Scan(&drainContextRows, &drainMode, &drainLegacySchema6); err != nil {
		_ = drainInspect.Rollback(ctx)
		t.Fatal(err)
	}
	if err := drainInspect.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if drainContextRows != 1 || drainMode != "bounded" || !drainLegacySchema6 {
		t.Fatalf("legacy drain context/mode/marker=%d/%v/%t", drainContextRows, drainMode, drainLegacySchema6)
	}
	var rolloutContexts, rolloutProgress int
	var rolloutMode string
	var rolloutLegacySchema6 bool
	rolloutInspect, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rolloutInspect.Rollback(ctx)
	if _, err := rolloutInspect.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, drainAccount.String()); err != nil {
		t.Fatal(err)
	}
	if err := rolloutInspect.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM app.job_source_contexts WHERE job_id=$1),
		(SELECT count(*) FROM app.job_progress WHERE job_id IN ($1,$2)),
		(SELECT parameters->>'mode' FROM app.jobs WHERE id=$1),
		(SELECT parameters->'legacySchema6'='true'::jsonb FROM app.jobs WHERE id=$1)`, rolloutChild, rolloutParent).
		Scan(&rolloutContexts, &rolloutProgress, &rolloutMode, &rolloutLegacySchema6); err != nil {
		t.Fatal(err)
	}
	if err := rolloutInspect.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if rolloutContexts != 1 || rolloutProgress != 2 || rolloutMode != "bounded" || !rolloutLegacySchema6 {
		t.Fatalf("post-migration legacy artifacts context/progress/mode/marker=%d/%d/%s/%t", rolloutContexts, rolloutProgress, rolloutMode, rolloutLegacySchema6)
	}
	drainClaim, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer drainClaim.Rollback()
	if _, err := drainClaim.ExecContext(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		_ = drainClaim.Rollback()
		t.Fatal(err)
	}
	drainLease := uuid.New()
	var claimedDrainJob, claimedDrainAccount uuid.UUID
	var claimedDrainKind string
	if err := drainClaim.QueryRowContext(ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job('schema6-drain-worker',$1,interval '1 minute',6)`, drainLease).Scan(
		&claimedDrainJob, &claimedDrainAccount, &claimedDrainKind); err != nil {
		_ = drainClaim.Rollback()
		t.Fatal(err)
	}
	if claimedDrainJob != drainChild || claimedDrainAccount != drainAccount || claimedDrainKind != "manual_ingest_source" {
		_ = drainClaim.Rollback()
		t.Fatalf("legacy drain claim=%s/%s/%s", claimedDrainJob, claimedDrainAccount, claimedDrainKind)
	}
	var drainFinished bool
	if err := drainClaim.QueryRowContext(ctx, `SELECT app.finish_job($1,'schema6-drain-worker',$2,'succeeded')`,
		drainChild, drainLease).Scan(&drainFinished); err != nil || !drainFinished {
		_ = drainClaim.Rollback()
		t.Fatalf("legacy drain finish=%t err=%v", drainFinished, err)
	}
	if err := drainClaim.Commit(); err != nil {
		t.Fatal(err)
	}
	rolloutClaim, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rolloutClaim.Rollback()
	if _, err := rolloutClaim.ExecContext(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		t.Fatal(err)
	}
	rolloutLease := uuid.New()
	if err := rolloutClaim.QueryRowContext(ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job('schema6-rollout-worker',$1,interval '1 minute',6)`, rolloutLease).Scan(
		&claimedDrainJob, &claimedDrainAccount, &claimedDrainKind); err != nil {
		t.Fatal(err)
	}
	if claimedDrainJob != rolloutChild || claimedDrainAccount != drainAccount || claimedDrainKind != "manual_ingest_source" {
		t.Fatalf("post-migration legacy claim=%s/%s/%s", claimedDrainJob, claimedDrainAccount, claimedDrainKind)
	}
	if err := rolloutClaim.QueryRowContext(ctx, `SELECT app.finish_job($1,'schema6-rollout-worker',$2,'succeeded')`,
		rolloutChild, rolloutLease).Scan(&drainFinished); err != nil || !drainFinished {
		t.Fatalf("post-migration legacy finish=%t err=%v", drainFinished, err)
	}
	if err := rolloutClaim.Commit(); err != nil {
		t.Fatal(err)
	}
	if database.Ready(ctx, apiDB) {
		t.Fatal("runtime-8 API role reported ready at schema 7")
	}
	legacyTx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyTx.Rollback()
	if _, err := legacyTx.ExecContext(ctx, `SELECT set_config('app.account_id',$1,true)`, legacyAccount.String()); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	var repairedChildren int
	if err := legacyTx.QueryRowContext(ctx, `SELECT count(*) FROM app.jobs
		WHERE id IN ($1,$2,$3,$4) AND status='cancelled' AND terminal_at IS NOT NULL
		  AND worker_id IS NULL AND lease_token IS NULL AND claimed_at IS NULL
		  AND heartbeat_at IS NULL AND lease_expires_at IS NULL
		  AND failure_code='source-snapshot-missing'
		  AND failure_summary='Job cancelled during schema upgrade because its source snapshot was missing.'`,
		legacyQueuedCheck, legacyRunningCheck, legacyQueuedChild, legacyRunningChild).Scan(&repairedChildren); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	var parentStatus string
	if err := legacyTx.QueryRowContext(ctx, `SELECT status FROM app.jobs WHERE id=$1`, legacyParent).Scan(&parentStatus); err != nil {
		_ = legacyTx.Rollback()
		t.Fatal(err)
	}
	_ = legacyTx.Rollback()
	if repairedChildren != 4 || parentStatus != "cancelled" {
		t.Fatalf("legacy orphan repair count=%d parent_status=%s", repairedChildren, parentStatus)
	}

	if err := goose.DownToContext(ctx, db, ".", 4); err != nil {
		t.Fatal(err)
	}
	var migrationFiveDown bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=4
		AND to_regprocedure('app.read_worker_job_log_context(uuid,text,uuid)') IS NULL
		AND to_regprocedure('app.repair_orphaned_source_jobs()') IS NULL
		FROM app.schema_metadata WHERE singleton`).Scan(&migrationFiveDown); err != nil || !migrationFiveDown {
		t.Fatalf("migration 00005 down is incomplete: correct=%t err=%v", migrationFiveDown, err)
	}
	if database.Ready(ctx, apiDB) {
		t.Fatal("schema-v5 runtime reported ready after migration 00005 down")
	}
	if err := goose.UpToContext(ctx, db, ".", 6); err != nil {
		t.Fatal(err)
	}
	if database.Ready(ctx, apiDB) {
		t.Fatal("runtime-8 API role reported ready after reapplying only through schema 6")
	}
	guardAccount, guardSource := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	guardParent, guardChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	guardTx, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guardTx.Rollback(ctx)
	if _, err := guardTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, guardAccount.String()); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, guardAccount); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	guardRequester := uuid.Must(uuid.NewV7())
	guardIdentity := strings.ReplaceAll(guardRequester.String(), "-", "")
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.authentication_principals
		(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Down Guard Owner')`, guardRequester,
		"guard"+guardIdentity, "guard"+guardIdentity+"@example.test"); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, guardRequester, guardAccount); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,'Down Guard','down guard','health-auto-export-local',$3)`, guardSource, guardAccount, []byte(`{"encrypted":true}`)); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority)
		VALUES($1,$2,'manual_ingest',80)`, guardParent, guardAccount); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	guardParameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`,
		strings.ToUpper(strings.ReplaceAll(guardSource.String(), "-", "")))
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
		VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, guardChild, guardParent, guardAccount, guardParameters); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		guardChild, guardAccount, guardSource, []byte(`{"snapshot":true}`)); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := guardTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 3); err == nil {
		t.Fatal("migration 00004 down ignored active manual ingest")
	}
	guardTx, err = apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guardTx.Rollback(ctx)
	if _, err := guardTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, guardAccount.String()); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	var cancelled bool
	// Migration 6 has already rolled back while migration 4's active-ingest guard stopped the downgrade.
	if err := guardTx.QueryRow(ctx, `SELECT app.request_job_cancellation($1,$2)`, guardParent, guardRequester).Scan(&cancelled); err != nil || !cancelled {
		_ = guardTx.Rollback(ctx)
		t.Fatalf("could not clear down-guard fixture: cancelled=%t err=%v", cancelled, err)
	}
	if err := guardTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 3); err != nil {
		t.Fatal(err)
	}
	var ingestDownCorrect bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=3
		AND to_regclass('app.sources') IS NOT NULL
		AND to_regclass('app.source_files') IS NULL
		AND to_regclass('app.ingest_write_capabilities') IS NULL
		AND to_regprocedure('app.valid_workout_warnings(jsonb)') IS NULL
		AND to_regprocedure('app.claim_next_worker_job(text,uuid,interval)') IS NULL
		AND to_regprocedure('app.claim_next_worker_job(text,uuid,interval,integer)') IS NULL
		AND to_regprocedure('app.read_worker_job_log_context(uuid,text,uuid)') IS NULL
		AND to_regprocedure('app.fence_ingest_job(uuid,text,uuid)') IS NULL
		AND to_regprocedure('app.claim_next_source_connection_check(text,uuid,interval)') IS NOT NULL
		AND to_regprocedure('app.delete_source(uuid,uuid)') IS NOT NULL
		AND (SELECT pg_get_userbyid(proowner)='workouts_security_owner'
			FROM pg_proc WHERE oid='app.delete_source(uuid,uuid)'::regprocedure)
		AND pg_get_userbyid(p.proowner)='workouts_migration'
		AND position('FROM app.sources source' in pg_get_functiondef(finish.oid))=0
		AND position('FROM app.sources source' in pg_get_functiondef(recover.oid))=0
		AND position('FROM app.sources source' in pg_get_functiondef(cancel.oid))=0
		AND NOT EXISTS (SELECT 1 FROM pg_policy WHERE polname IN
			('jobs_cross_account_claim_policy','job_config_snapshots_cross_account_guard_policy'))
		FROM app.schema_metadata
		JOIN pg_proc p ON p.oid='app.claim_next_source_connection_check(text,uuid,interval)'::regprocedure
		JOIN pg_proc finish ON finish.oid='app.finish_job(uuid,text,uuid,text,text,text)'::regprocedure
		JOIN pg_proc recover ON recover.oid='app.recover_expired_job(uuid)'::regprocedure
		JOIN pg_proc cancel ON cancel.oid='app.request_job_cancellation(uuid,uuid)'::regprocedure
		WHERE singleton`).Scan(&ingestDownCorrect); err != nil || !ingestDownCorrect {
		t.Fatalf("migration 00004 down is incomplete: correct=%t err=%v", ingestDownCorrect, err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
	if !database.Ready(ctx, apiDB) {
		t.Fatal("API role is not ready after migrations 00004 through 00010 reapply")
	}
	if err := goose.DownToContext(ctx, db, ".", 2); err != nil {
		t.Fatal(err)
	}
	var downCorrect bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=2
		AND to_regclass('app.sources') IS NULL
		AND to_regclass('app.job_config_snapshots') IS NULL
		AND to_regprocedure('app.delete_source(uuid,uuid)') IS NULL
		AND pg_get_userbyid(p.proowner)='workouts_migration'
		AND pg_get_userbyid(heartbeat.proowner)='workouts_migration'
		AND position('lease_expires_at >= clock_timestamp()' in pg_get_functiondef(heartbeat.oid))=0
		FROM app.schema_metadata
		JOIN pg_proc p ON p.oid='app.finish_job(uuid,text,uuid,text,text,text)'::regprocedure
		JOIN pg_proc heartbeat ON heartbeat.oid='app.heartbeat_job(uuid,text,uuid,interval)'::regprocedure
		WHERE singleton`).Scan(&downCorrect); err != nil || !downCorrect {
		t.Fatalf("migration 00003 down is incomplete: correct=%t err=%v", downCorrect, err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
	if !database.Ready(ctx, apiDB) {
		t.Fatal("API role is not ready after migrations 00003 through 00010 reapply")
	}
}

// schema5APIReady preserves the API branch of runtime 5's shipped readiness contract as rollout proof.
func schema5APIReady(ctx context.Context, pool *pgxpool.Pool) bool {
	var ready bool
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(max(version_id) FILTER (WHERE is_applied),0)>=5
		   AND to_regclass('app.schema_metadata') IS NOT NULL
		   AND to_regclass('app.jobs') IS NOT NULL
		   AND to_regclass('app.sources') IS NOT NULL
		   AND to_regclass('app.job_config_snapshots') IS NOT NULL
		   AND to_regclass('app.source_files') IS NOT NULL
		   AND to_regclass('app.workout_types') IS NOT NULL
		   AND to_regclass('app.workouts') IS NOT NULL
		   AND to_regclass('app.workout_aggregates') IS NOT NULL
		   AND to_regclass('app.workout_route_points') IS NOT NULL
		   AND to_regclass('app.workout_import_events') IS NOT NULL
		   AND to_regclass('app.ingest_write_capabilities') IS NOT NULL
		   AND has_schema_privilege(current_user,'app','USAGE')
		   AND has_table_privilege(current_user,'app.schema_metadata','SELECT')
		   AND has_table_privilege(current_user,'app.jobs','SELECT')
		   AND has_table_privilege(current_user,'app.jobs','INSERT')
		   AND has_function_privilege(current_user,'app.current_account_id()','EXECUTE')
		   AND has_function_privilege(current_user,'app.request_job_cancellation(uuid,uuid)','EXECUTE')
		   AND (SELECT count(*)=10 AND bool_and(pg_get_userbyid(proc.proowner)='workouts_security_owner')
		          FROM pg_proc proc WHERE proc.oid IN (
		            'app.claim_next_worker_job_internal(text,uuid,interval,boolean)'::regprocedure,
		            'app.claim_next_worker_job(text,uuid,interval)'::regprocedure,
		            'app.claim_next_source_connection_check(text,uuid,interval)'::regprocedure,
		            'app.fence_ingest_job(uuid,text,uuid)'::regprocedure,
		            'app.clear_ingest_write_capability()'::regprocedure,
		            'app.finish_job(uuid,text,uuid,text,text,text)'::regprocedure,
		            'app.recover_expired_job(uuid)'::regprocedure,
		            'app.request_job_cancellation(uuid,uuid)'::regprocedure,
		            'app.delete_source(uuid,uuid)'::regprocedure,
		            'app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure))
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname='workouts_security_owner'
		       AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls)
		   AND to_regclass('app.authentication_principals') IS NOT NULL
		   AND to_regclass('app.sessions') IS NOT NULL
		   AND has_column_privilege(current_user,'app.authentication_principals','password_hash','SELECT')
		   AND has_column_privilege(current_user,'app.authentication_principals','full_name','UPDATE')
		   AND has_column_privilege(current_user,'app.sessions','credential_verifier','INSERT')
		   AND has_function_privilege(current_user,'app.consume_rate_limit(text,text,bytea)','EXECUTE')
		   AND has_function_privilege(current_user,'app.issue_password_reset(text,boolean,bytea)','EXECUTE')
		   AND has_function_privilege(current_user,'app.complete_password_reset(bytea,text,uuid,text)','EXECUTE')
		   AND has_column_privilege(current_user,'app.sources','config_envelope','SELECT')
		   AND has_column_privilege(current_user,'app.sources','config_envelope','UPDATE')
		   AND has_column_privilege(current_user,'app.sources','canonical_display_name','INSERT')
		   AND has_column_privilege(current_user,'app.sources','canonical_display_name','UPDATE')
		   AND has_function_privilege(current_user,'app.delete_source(uuid,uuid)','EXECUTE')
		   AND NOT has_column_privilege(current_user,'app.sources','deleted_at','UPDATE')
		   AND has_column_privilege(current_user,'app.job_config_snapshots','config_envelope','INSERT')
		   AND has_column_privilege(current_user,'app.job_config_snapshots','source_id','SELECT')
		   AND NOT has_column_privilege(current_user,'app.job_config_snapshots','config_envelope','SELECT')
		   AND NOT has_column_privilege(current_user,'app.job_config_snapshots','created_at','SELECT')
		   AND has_table_privilege(current_user,'app.source_files','SELECT')
		   AND has_table_privilege(current_user,'app.workouts','SELECT')
		   AND has_table_privilege(current_user,'app.workout_import_events','SELECT')
		   AND has_column_privilege(current_user,'app.workout_import_events','warnings','SELECT')
		   AND NOT has_table_privilege(current_user,'app.workouts','INSERT')
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname=current_user AND rolcanlogin
		       AND NOT rolsuper AND NOT rolbypassrls AND rolname='workouts_api')
		   AND EXISTS (SELECT 1 FROM app.schema_metadata WHERE singleton
		       AND schema_version>=5 AND minimum_runtime_version<=5)
		FROM public.goose_db_version`).Scan(&ready)
	return err == nil && ready
}

func runtime9ReadinessDiagnostics(ctx context.Context, pool *pgxpool.Pool) string {
	var missingObjects, wrongOwners, failedPrivileges []string
	err := pool.QueryRow(ctx, `
		SELECT
			ARRAY(SELECT name FROM unnest(ARRAY[
				'app.schema_metadata','app.jobs','app.sources','app.job_config_snapshots','app.source_files',
				'app.workout_types','app.workouts','app.workout_aggregates','app.workout_route_points',
				'app.workout_import_events','app.ingest_write_capabilities','app.job_source_contexts',
				'app.job_progress','app.source_objects','app.ingest_file_slot_guard','app.ingest_file_slot_limits',
				'app.ingest_file_slots','app.job_file_candidate_sets','app.job_file_candidates','app.job_events',
				'app.job_logs','app.notifications','app.source_sync_state','app.auto_sync_policy','app.account_sync_schedules',
				'app.account_data_generations','app.map_selections','app.map_selection_workouts','app.workout_routes_route_gist_idx'
			]) name WHERE to_regclass(name) IS NULL),
			ARRAY(SELECT signature FROM unnest(ARRAY[
				'app.claim_next_worker_job_internal(text,uuid,interval,boolean)',
				'app.claim_next_worker_job(text,uuid,interval)','app.claim_next_worker_job(text,uuid,interval,integer)',
				'app.claim_next_source_connection_check(text,uuid,interval)','app.fence_ingest_job(uuid,text,uuid)',
				'app.clear_ingest_write_capability()','app.finish_job(uuid,text,uuid,text,text,text)',
				'app.recover_expired_job(uuid)','app.request_job_cancellation(uuid,uuid)',
				'app.request_owned_job_cancellation(uuid,uuid)','app.delete_source(uuid,uuid)',
				'app.read_worker_job_log_context(uuid,text,uuid)',
				'app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)',
				'app.record_job_event(uuid,text,uuid,text,jsonb)','app.record_job_log(uuid,text,uuid,text,jsonb)',
				'app.evaluate_source_staleness(uuid,integer,timestamp with time zone)',
				'app.read_owned_sync_schedule()',
				'app.advance_account_data_generation()','app.seed_account_data_generation()',
				'app.validate_map_selection()','app.validate_map_selection_workout()',
				'app.cleanup_expired_map_selections()',
				'app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)'
				,'app.create_legacy_ingest_read_models()'
			]) signature WHERE to_regprocedure(signature) IS NULL OR
				pg_get_userbyid((SELECT proowner FROM pg_proc WHERE oid=to_regprocedure(signature)))<>'workouts_security_owner'),
			ARRAY(SELECT label FROM (VALUES
				('app schema usage',has_schema_privilege(current_user,'app','USAGE')),
				('schema metadata select',has_table_privilege(current_user,'app.schema_metadata','SELECT')),
				('jobs select',has_table_privilege(current_user,'app.jobs','SELECT')),
				('jobs insert',has_table_privilege(current_user,'app.jobs','INSERT')),
				('current account execute',has_function_privilege(current_user,'app.current_account_id()','EXECUTE')),
				('staleness execute',has_function_privilege(current_user,'app.evaluate_source_staleness(uuid,integer,timestamp with time zone)','EXECUTE')),
				('schedule reader execute',has_function_privilege(current_user,'app.read_owned_sync_schedule()','EXECUTE')),
				('map generations select',has_table_privilege(current_user,'app.account_data_generations','SELECT')),
				('map selections select',has_table_privilege(current_user,'app.map_selections','SELECT')),
				('map selections insert',has_table_privilege(current_user,'app.map_selections','INSERT')),
				('map selections delete',has_table_privilege(current_user,'app.map_selections','DELETE')),
				('map selections no update',NOT has_table_privilege(current_user,'app.map_selections','UPDATE')),
				('tile function denied',NOT has_function_privilege(current_user,'app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)','EXECUTE')),
				('snapshot source select',has_column_privilege(current_user,'app.job_config_snapshots','source_id','SELECT')),
				('snapshot envelope insert',has_column_privilege(current_user,'app.job_config_snapshots','config_envelope','INSERT')),
				('notifications message select',has_column_privilege(current_user,'app.notifications','message','SELECT')),
				('source sync state account select',has_column_privilege(current_user,'app.source_sync_state','account_id','SELECT')),
				('source sync state source select',has_column_privilege(current_user,'app.source_sync_state','source_id','SELECT')),
				('source sync state started select',has_column_privilege(current_user,'app.source_sync_state','last_sync_started_at','SELECT')),
				('source sync state succeeded select',has_column_privilege(current_user,'app.source_sync_state','last_sync_succeeded_at','SELECT')),
				('source sync state discovered select',has_column_privilege(current_user,'app.source_sync_state','last_new_export_discovered_at','SELECT')),
				('source sync state export date select',has_column_privilege(current_user,'app.source_sync_state','last_new_export_date','SELECT')),
				('source sync state stale select',has_column_privilege(current_user,'app.source_sync_state','stale_since','SELECT')),
				('source sync state narrow select',NOT has_table_privilege(current_user,'app.source_sync_state','SELECT')),
				('minimum runtime metadata',EXISTS(SELECT 1 FROM app.schema_metadata WHERE singleton AND schema_version>=9 AND minimum_runtime_version<=9))
			) checks(label,ok) WHERE NOT ok)`).Scan(&missingObjects, &wrongOwners, &failedPrivileges)
	if err != nil {
		return fmt.Sprintf("diagnostic query failed: %v", err)
	}
	return fmt.Sprintf("missing_objects=%v wrong_function_owners=%v failed_privileges=%v", missingObjects, wrongOwners, failedPrivileges)
}
