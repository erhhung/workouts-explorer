package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

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
	ctx := context.Background()
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
	legacyTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
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
		($4,$5,'workout_deletion',50)`, legacyParent, legacyQueuedCheck, legacyRunningCheck, uuid.Must(uuid.NewV7()), legacyAccount); err != nil {
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
	if err := db.QueryRowContext(ctx, `SELECT schema_version=6
		AND to_regclass('app.sources') IS NOT NULL
		AND to_regclass('app.job_config_snapshots') IS NOT NULL
		AND to_regclass('app.source_files') IS NOT NULL
		AND to_regclass('app.workouts') IS NOT NULL
		AND to_regclass('app.workout_import_events') IS NOT NULL
		AND to_regclass('app.ingest_write_capabilities') IS NOT NULL
		AND to_regprocedure('app.valid_workout_warnings(jsonb)') IS NOT NULL
		AND to_regprocedure('app.delete_source(uuid,uuid)') IS NOT NULL
		AND to_regprocedure('app.read_worker_job_log_context(uuid,text,uuid)') IS NOT NULL
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
		t.Fatal("API role is not ready after schema-v1 upgrade")
	}
	scheduledAccount, scheduledSource := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scheduledParent, scheduledChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	scheduledTx, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, scheduledAccount.String()); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, scheduledAccount); err != nil {
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
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority)
		VALUES($1,$2,$3,'scheduled_ingest_source',60)`, scheduledChild, scheduledParent, scheduledAccount); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		scheduledChild, scheduledAccount, scheduledSource, []byte(`{"snapshot":true}`)); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := scheduledTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 5); err == nil {
		t.Fatal("migration 00006 down ignored active scheduled ingest")
	}
	scheduledTx, err = apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduledTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, scheduledAccount.String()); err != nil {
		_ = scheduledTx.Rollback(ctx)
		t.Fatal(err)
	}
	var scheduledCancelled bool
	if err := scheduledTx.QueryRow(ctx, `SELECT app.request_job_cancellation($1,$2)`, scheduledParent, uuid.Must(uuid.NewV7())).Scan(&scheduledCancelled); err != nil || !scheduledCancelled {
		_ = scheduledTx.Rollback(ctx)
		t.Fatalf("scheduled down-guard cleanup failed: cancelled=%t err=%v", scheduledCancelled, err)
	}
	if err := scheduledTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownToContext(ctx, db, ".", 5); err != nil {
		t.Fatal(err)
	}
	var migrationSixDown bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=5
		AND pg_get_function_result('app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure)
			= 'TABLE(owner_name text, source_name text, source_type text)'
		AND pg_get_userbyid(p.proowner)='workouts_security_owner'
		AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
		AND to_regprocedure('app.assert_no_active_scheduled_ingest()') IS NULL
		AND position('scheduled_ingest_source' in pg_get_functiondef(claim.oid))=0
		AND position('scheduled_ingest_source' in pg_get_functiondef(fence.oid))=0
		AND position('scheduled_ingest_source' in pg_get_functiondef(cleanup.oid))=0
		FROM app.schema_metadata
		JOIN pg_proc p ON p.oid='app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure
		JOIN pg_proc claim ON claim.oid='app.claim_next_worker_job_internal(text,uuid,interval,boolean)'::regprocedure
		JOIN pg_proc fence ON fence.oid='app.fence_ingest_job(uuid,text,uuid)'::regprocedure
		JOIN pg_proc cleanup ON cleanup.oid='app.clear_ingest_write_capability()'::regprocedure
		WHERE singleton`).Scan(&migrationSixDown); err != nil || !migrationSixDown {
		t.Fatalf("migration 00006 down is incomplete: correct=%t err=%v", migrationSixDown, err)
	}
	if database.Ready(ctx, apiDB) {
		t.Fatal("schema-v6 runtime reported ready after migration 00006 down")
	}
	if err := goose.UpToContext(ctx, db, ".", 6); err != nil {
		t.Fatal(err)
	}
	if !database.Ready(ctx, apiDB) {
		t.Fatal("API role is not ready after migration 00006 reapply")
	}
	legacyTx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	if !database.Ready(ctx, apiDB) {
		t.Fatal("API role is not ready after migrations 00005 and 00006 reapply")
	}
	guardAccount, guardSource := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	guardParent, guardChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	guardTx, err := apiDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, guardAccount.String()); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, guardAccount); err != nil {
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
	if _, err := guardTx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority)
		VALUES($1,$2,$3,'manual_ingest_source',80)`, guardChild, guardParent, guardAccount); err != nil {
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
	if _, err := guardTx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, guardAccount.String()); err != nil {
		_ = guardTx.Rollback(ctx)
		t.Fatal(err)
	}
	var cancelled bool
	if err := guardTx.QueryRow(ctx, `SELECT app.request_job_cancellation($1,$2)`, guardParent, uuid.Must(uuid.NewV7())).Scan(&cancelled); err != nil || !cancelled {
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
		t.Fatal("API role is not ready after migrations 00004 through 00006 reapply")
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
		t.Fatal("API role is not ready after migrations 00003 through 00006 reapply")
	}
}
