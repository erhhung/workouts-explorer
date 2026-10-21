package migrations_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

const icloudWorker = "synthetic-icloud-worker"
const icloudManifest = `[{"name":"HealthAutoExport-2026-01-01.json","export_date":"2026-01-01","size":10,"modified_sec":100,"modified_ns":1,"device":0,"inode":0,"ctime_sec":0,"ctime_ns":0,"action":"process"}]`

type icloudFixture struct {
	account, source, parent, job, lease uuid.UUID
	snapshot                            []byte
}

// This suite never falls back to application database URLs. Supply an empty,
// disposable loopback database named workouts_icloud_test_* with the standard
// roles already provisioned, and an admin URL able to SET ROLE to each role.
// ICLOUD_TEST_SOURCE_FOUNDATIONS_ONLY=1 runs the same source contracts on 001-006
// plus 021 without PostGIS; the default upgrades 020 through all current migrations.
func TestICloudMigrationIntegration(t *testing.T) {
	dsn := os.Getenv("ICLOUD_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("ICLOUD_TEST_DISPOSABLE") != "1" {
		t.Skip("ICLOUD_TEST_DATABASE_URL and ICLOUD_TEST_DISPOSABLE=1 are required for an isolated scratch database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch database configuration")
	}
	if !strings.HasPrefix(config.Database, "workouts_icloud_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("iCloud tests require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect to scratch database")
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("scratch database must be empty before the suite runs")
	}
	foundationsOnly := os.Getenv("ICLOUD_TEST_SOURCE_FOUNDATIONS_ONLY") == "1"
	baseline := int64(20)
	var options []goose.ProviderOption
	if foundationsOnly {
		baseline = 6
		options = append(options, goose.WithExcludeVersions([]int64{7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}))
	} else {
		if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
			t.Fatal(err)
		}
	}
	config.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files, options...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *pgxpool.Pool {
		poolConfig, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid scratch pool configuration")
		}
		poolConfig.ConnConfig.RuntimeParams["role"] = role
		poolConfig.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			t.Fatal("create scratch pool")
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker")}
	legacy := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-local")
	if state := icloudRecordManifest(t, db, legacy, icloudManifest); state != "created" {
		t.Fatalf("baseline manifest=%s", state)
	}
	oldScheduler := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-local")
	if !callBool(t, ctx, db.worker, oldScheduler.account, `SELECT app.finish_job($1,$2,$3,'succeeded')`, oldScheduler.job, icloudWorker, oldScheduler.lease) {
		t.Fatal("could not finish pre-upgrade scheduler fixture")
	}
	icloudFixtureExec(t, db, oldScheduler, `INSERT INTO app.account_sync_schedules(account_id,next_run_at) VALUES($1,'-infinity')`, oldScheduler.account)
	var oldClaimedAccount uuid.UUID
	if err := db.worker.QueryRow(ctx, `SELECT account_id FROM app.claim_due_sync_account($1,$2,interval '2 minutes',20)`,
		icloudWorker, oldScheduler.lease).Scan(&oldClaimedAccount); err != nil || oldClaimedAccount != oldScheduler.account {
		t.Fatalf("pre-upgrade v20 scheduler claim=%s err=%v", oldClaimedAccount, err)
	}
	wantVersion, wantMinimum := database.SupportedSchemaVersion, 28
	if foundationsOnly {
		wantVersion, wantMinimum = 21, 21
	}
	for range 2 {
		if _, err := provider.UpTo(ctx, int64(wantVersion)); err != nil {
			t.Fatal(err)
		}
	}
	var dbRuntime, minimum int
	if err := db.migration.QueryRow(ctx, `SELECT schema_version,minimum_runtime_version FROM app.schema_metadata WHERE singleton`).Scan(&dbRuntime, &minimum); err != nil {
		t.Fatal(err)
	}
	obsoleteRuntimes := []any{20, nil}
	if wantMinimum > 21 {
		obsoleteRuntimes = append(obsoleteRuntimes, wantMinimum-1)
	}
	t.Run("SchemaACLsAndReadiness", func(t *testing.T) {
		if dbRuntime != wantVersion || minimum != wantMinimum {
			t.Fatalf("schema=%d minimum=%d, want %d/%d", dbRuntime, minimum, wantVersion, wantMinimum)
		}
		for _, signature := range []string{
			"app.read_typed_job_source_snapshot(uuid,text,uuid)",
			"app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb)",
			"app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb)",
			"app.fail_source_connection(uuid,text,uuid,bigint,text,text)",
			"app.claim_next_worker_job(text,uuid,interval,integer)",
			"app.claim_due_sync_account(text,uuid,interval,integer)",
			"app.read_leased_sync_sources(uuid,text,uuid)",
			"app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb)",
			"app.finish_sync_account(uuid,text,uuid,uuid)",
		} {
			var safe bool
			if err := db.migration.QueryRow(ctx, `SELECT p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
				AND pg_get_userbyid(p.proowner)='workouts_security_owner'
				AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
				AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
				FROM pg_proc p WHERE p.oid=$1::regprocedure`, signature).Scan(&safe); err != nil || !safe {
				t.Fatalf("unsafe function %s: %t err=%v", signature, safe, err)
			}
		}
		for _, signature := range []string{
			"app.claim_due_sync_account_internal(text,uuid,interval,integer)",
			"app.enqueue_leased_scheduled_ingest_internal(uuid,text,uuid,uuid,bytea,jsonb)",
			"app.finish_sync_account_internal(uuid,text,uuid,uuid)",
		} {
			var private bool
			if err := db.migration.QueryRow(ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner' AND p.prosecdef
				AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
				AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
				FROM pg_proc p WHERE p.oid=$1::regprocedure`, signature).Scan(&private); err != nil || !private {
				t.Fatalf("scheduler runtime gate can be bypassed via %s: private=%t err=%v", signature, private, err)
			}
		}
		var private bool
		if err := db.migration.QueryRow(ctx, `SELECT NOT has_table_privilege('workouts_worker','app.sources','SELECT,UPDATE')
			AND NOT has_column_privilege('workouts_worker','app.sources','config_envelope','SELECT')
			AND NOT has_column_privilege('workouts_worker','app.sources','generation','UPDATE')
			AND NOT has_column_privilege('workouts_worker','app.account_sync_schedules','lease_runtime_version','UPDATE')
			AND NOT has_function_privilege('workouts_worker','app.lock_source_job_for_credentials(uuid,text,uuid)','EXECUTE')
			AND NOT has_function_privilege('workouts_api','app.lock_source_job_for_credentials(uuid,text,uuid)','EXECUTE')
			AND NOT has_table_privilege('workouts_worker','app.job_file_candidates','SELECT,INSERT,UPDATE,DELETE')
			AND NOT has_table_privilege('workouts_api','app.job_file_candidates','SELECT,INSERT,UPDATE,DELETE')
			AND (SELECT bool_and(relrowsecurity AND relforcerowsecurity) FROM pg_class
				WHERE oid IN ('app.sources'::regclass,'app.job_config_snapshots'::regclass,'app.job_file_candidates'::regclass))`).Scan(&private); err != nil || !private {
			t.Fatalf("credential/manifest privacy=%t err=%v", private, err)
		}
		if !foundationsOnly && (!database.Ready(ctx, db.api) || !database.Ready(ctx, db.worker)) {
			t.Fatal("fully migrated runtime is not ready")
		}
		if _, err := provider.Down(ctx); err == nil {
			t.Fatal("forward-only migration allowed downgrade")
		}
	})
	t.Run("OldRuntimeWorkerClaimsHaveNoSideEffects", func(t *testing.T) {
		for _, kind := range []string{"manual_ingest_source", "source_connection_check"} {
			f := newICloudFixture(t, db, kind, "health-auto-export-icloud")
			icloudFixtureExec(t, db, f, `UPDATE app.jobs SET priority=32767 WHERE id=$1`, f.job)
			icloudExpireLease(t, db, f)
			for _, version := range obsoleteRuntimes {
				var claimed int
				if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.claim_next_worker_job($1,$2,interval '2 minutes',$3)`,
					"obsolete-worker", uuid.New(), version).Scan(&claimed); err != nil || claimed != 0 {
					t.Fatalf("runtime %v claimed work: count=%d err=%v", version, claimed, err)
				}
			}
			var legacyClaims int
			if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.claim_next_source_connection_check($1,$2,interval '2 minutes')`,
				"legacy-worker", uuid.New()).Scan(&legacyClaims); err != nil || legacyClaims != 0 {
				t.Fatalf("versionless claim bypassed runtime gate: count=%d err=%v", legacyClaims, err)
			}
			inspect := beginAccount(t, ctx, db.migration, f.account)
			var unchanged bool
			if err := inspect.QueryRow(ctx, `SELECT job.status='running' AND job.attempt=1 AND job.worker_id=$2
				AND job.lease_token=$3 AND snapshot.config_envelope=$4
				FROM app.jobs job JOIN app.job_config_snapshots snapshot ON snapshot.job_id=job.id WHERE job.id=$1`,
				f.job, icloudWorker, f.lease, f.snapshot).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("obsolete claim recovered/mutated work: unchanged=%t err=%v", unchanged, err)
			}
			_ = inspect.Rollback(ctx)
			var claimedJob, claimedAccount uuid.UUID
			var claimedKind string
			if err := db.worker.QueryRow(ctx, `SELECT job_id,account_id,kind FROM app.claim_next_worker_job($1,$2,interval '2 minutes',$3)`,
				"current-worker", uuid.New(), dbRuntime).Scan(&claimedJob, &claimedAccount, &claimedKind); err != nil ||
				claimedJob != f.job || claimedAccount != f.account || claimedKind != kind {
				t.Fatalf("runtime %d did not recover expected work: %s/%s/%s err=%v", dbRuntime, claimedJob, claimedAccount, claimedKind, err)
			}
		}
	})
	t.Run("OldSchedulerClaimsAndPreUpgradeLeaseCannotEnact", func(t *testing.T) {
		f := oldScheduler
		remoteSource := uuid.New()
		icloudFixtureExec(t, db, f, `UPDATE app.sources SET auto_sync_enabled=false WHERE id=$1`, f.source)
		icloudFixtureExec(t, db, f, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope)
			VALUES($1,$2,'Remote source','remote source','health-auto-export-icloud','connected',true,$3)`, remoteSource, f.account, syntheticICloudEnvelope(t))
		var schedulesBefore, schedulesAfter int
		if err := db.migration.QueryRow(ctx, `SELECT count(*) FROM app.account_sync_schedules`).Scan(&schedulesBefore); err != nil {
			t.Fatal(err)
		}
		for _, version := range obsoleteRuntimes {
			var claimed int
			if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.claim_due_sync_account($1,$2,interval '2 minutes',$3)`,
				"obsolete-scheduler", uuid.New(), version).Scan(&claimed); err != nil || claimed != 0 {
				t.Fatalf("runtime %v claimed schedules: count=%d err=%v", version, claimed, err)
			}
		}
		if err := db.migration.QueryRow(ctx, `SELECT count(*) FROM app.account_sync_schedules`).Scan(&schedulesAfter); err != nil || schedulesAfter != schedulesBefore {
			t.Fatalf("obsolete scheduler seeded schedules: before=%d after=%d err=%v", schedulesBefore, schedulesAfter, err)
		}
		child, parent := uuid.New(), uuid.New()
		failures := fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","failureCode":"source-config-invalid"}]`, remoteSource, child)
		var readSources, enqueued int
		if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.read_leased_sync_sources($1,$2,$3)`, f.account, icloudWorker, f.lease).Scan(&readSources); err != nil || readSources != 0 {
			t.Fatalf("pre-upgrade scheduler read credentials: count=%d err=%v", readSources, err)
		}
		if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
			f.account, icloudWorker, f.lease, parent, make([]byte, 32), failures).Scan(&enqueued); err != nil || enqueued != 0 {
			t.Fatalf("pre-upgrade scheduler enqueued failure artifacts: count=%d err=%v", enqueued, err)
		}
		if callBool(t, ctx, db.worker, f.account, `SELECT app.finish_sync_account($1,$2,$3,NULL)`, f.account, icloudWorker, f.lease) {
			t.Fatal("pre-upgrade scheduler advanced schedule despite losing its capability")
		}
		inspect := beginAccount(t, ctx, db.migration, f.account)
		var unchanged bool
		if err := inspect.QueryRow(ctx, `SELECT schedule.lease_token=$2 AND schedule.lease_runtime_version IS NULL
			AND schedule.next_run_at='-infinity' AND schedule.last_job_id IS NULL AND schedule.consecutive_failures=0
			AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE id IN ($3,$4))
			AND NOT EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=$3)
			AND NOT EXISTS(SELECT 1 FROM app.job_source_contexts WHERE job_id=$3)
			AND NOT EXISTS(SELECT 1 FROM app.job_progress WHERE job_id IN ($3,$4))
			AND NOT EXISTS(SELECT 1 FROM app.notifications WHERE job_id IN ($3,$4) OR source_id=$5)
			FROM app.account_sync_schedules schedule WHERE schedule.account_id=$1`, f.account, f.lease, child, parent, remoteSource).Scan(&unchanged); err != nil || !unchanged {
			t.Fatalf("pre-upgrade lease enactment had side effects: unchanged=%t err=%v", unchanged, err)
		}
		_ = inspect.Rollback(ctx)
		icloudFixtureExec(t, db, f, `UPDATE app.account_sync_schedules SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, f.account)
		newLease := uuid.New()
		var claimedAccount uuid.UUID
		if err := db.worker.QueryRow(ctx, `SELECT account_id FROM app.claim_due_sync_account($1,$2,interval '2 minutes',$3)`,
			icloudWorker, newLease, dbRuntime).Scan(&claimedAccount); err != nil || claimedAccount != f.account {
			t.Fatalf("runtime %d scheduler claim=%s err=%v", dbRuntime, claimedAccount, err)
		}
		if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.read_leased_sync_sources($1,$2,$3)`, f.account, icloudWorker, newLease).Scan(&readSources); err != nil || readSources != 1 {
			t.Fatalf("current scheduler could not read remote source: count=%d err=%v", readSources, err)
		}
		children := fmt.Sprintf(`[{"sourceId":"%s","generation":1,"childId":"%s","snapshot":"%s"}]`,
			remoteSource, child, base64.StdEncoding.EncodeToString(syntheticICloudEnvelope(t)))
		var job uuid.UUID
		var reused bool
		if err := db.worker.QueryRow(ctx, `SELECT job_id,reused FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
			f.account, icloudWorker, newLease, parent, make([]byte, 32), children).Scan(&job, &reused); err != nil || job != parent || reused {
			t.Fatalf("current scheduler enqueue=%s/%t err=%v", job, reused, err)
		}
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_sync_account($1,$2,$3,$4)`, f.account, icloudWorker, newLease, parent) {
			t.Fatal("current scheduler could not finish schedule")
		}
	})
	t.Run("CheckStandaloneRefreshDeniedThenAtomicFinishSucceeds", func(t *testing.T) {
		f := newICloudFixture(t, db, "source_connection_check", "health-auto-export-icloud")
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		refresh := syntheticICloudEnvelope(t)
		var accepted bool
		if err := tx.QueryRow(ctx, `SELECT app.refresh_source_credentials($1,$2,$3,1,$4)`, f.job, icloudWorker, f.lease, refresh).Scan(&accepted); err != nil || accepted {
			t.Fatalf("standalone check refresh=%t err=%v", accepted, err)
		}
		if err := tx.QueryRow(ctx, `SELECT app.finish_connection_check_with_refresh($1,$2,$3,'connected',NULL,NULL,$4)`,
			f.job, icloudWorker, f.lease, refresh).Scan(&accepted); err != nil || !accepted {
			t.Fatalf("atomic check refresh=%t err=%v", accepted, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		inspect := beginAccount(t, ctx, db.migration, f.account)
		defer inspect.Rollback(ctx)
		var connected bool
		if err := inspect.QueryRow(ctx, `SELECT source.status='connected' AND source.generation=2
			AND convert_from(source.config_envelope,'UTF8')::jsonb=$3::jsonb AND job.status='succeeded'
			AND NOT EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=$1)
			FROM app.sources source JOIN app.jobs job ON job.id=$1 WHERE source.id=$2`, f.job, f.source, refresh).Scan(&connected); err != nil || !connected {
			t.Fatalf("check was stranded or refresh non-atomic: connected=%t err=%v", connected, err)
		}
	})
	t.Run("LegacyManifestSurvivesUpgrade", func(t *testing.T) {
		if state := icloudRecordManifest(t, db, legacy, icloudManifest); state != "matched" {
			t.Fatalf("legacy recovery=%s", state)
		}
	})
	t.Run("AuthoritativeTypeAndImmutableSnapshot", func(t *testing.T) {
		f := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-icloud")
		tx := beginAccount(t, ctx, db.worker, f.account)
		var source uuid.UUID
		var generation int64
		var sourceType string
		var envelope []byte
		if err := tx.QueryRow(ctx, `SELECT source_id,source_generation,source_type,config_envelope FROM app.read_typed_job_source_snapshot($1,$2,$3)`,
			f.job, icloudWorker, f.lease).Scan(&source, &generation, &sourceType, &envelope); err != nil {
			t.Fatal(err)
		}
		if source != f.source || generation != 1 || sourceType != "health-auto-export-icloud" {
			t.Fatalf("typed snapshot=%s/%d/%s", source, generation, sourceType)
		}
		for _, args := range [][]any{{f.job, "other-worker", f.lease}, {f.job, icloudWorker, uuid.New()}} {
			if err := tx.QueryRow(ctx, `SELECT source_type FROM app.read_typed_job_source_snapshot($1,$2,$3)`, args...).Scan(&sourceType); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("typed read ignored lease/worker fence: %v", err)
			}
		}
		_ = tx.Rollback(ctx)
		refresh := syntheticICloudEnvelope(t)
		if !icloudRefresh(t, db, f, 1, refresh) || icloudRefresh(t, db, f, 1, syntheticICloudEnvelope(t)) || icloudRefresh(t, db, f, 2, refresh) {
			t.Fatal("refresh was not first-writer-wins against snapshot AND current generation")
		}
		inspect := beginAccount(t, ctx, db.migration, f.account)
		defer inspect.Rollback(ctx)
		var snapshot []byte
		if err := inspect.QueryRow(ctx, `SELECT source.generation,snapshot.config_envelope FROM app.sources source
			JOIN app.job_config_snapshots snapshot ON snapshot.source_id=source.id WHERE snapshot.job_id=$1`, f.job).Scan(&generation, &snapshot); err != nil {
			t.Fatal(err)
		}
		if generation != 2 || !bytes.Equal(snapshot, f.snapshot) {
			t.Fatal("refresh mutated snapshot or incremented generation more than once")
		}
		if _, err := inspect.Exec(ctx, `UPDATE app.job_config_snapshots SET config_envelope=$2 WHERE job_id=$1`, f.job, refresh); err == nil {
			t.Fatal("snapshot update was accepted")
		}
	})
	t.Run("ConcurrentFirstWriterWins", func(t *testing.T) {
		f := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-icloud")
		first := beginAccount(t, ctx, db.worker, f.account)
		defer first.Rollback(ctx)
		var accepted bool
		if err := first.QueryRow(ctx, `SELECT app.refresh_source_credentials($1,$2,$3,1,$4)`, f.job, icloudWorker, f.lease, syntheticICloudEnvelope(t)).Scan(&accepted); err != nil || !accepted {
			t.Fatalf("first refresh=%t err=%v", accepted, err)
		}
		second := beginAccount(t, ctx, db.worker, f.account)
		defer second.Rollback(ctx)
		var secondPID int
		if err := second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondPID); err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			accepted bool
			err      error
		}
		result := make(chan outcome, 1)
		refresh := syntheticICloudEnvelope(t)
		go func() {
			var value outcome
			value.err = second.QueryRow(ctx, `SELECT app.refresh_source_credentials($1,$2,$3,1,$4)`, f.job, icloudWorker, f.lease, refresh).Scan(&value.accepted)
			result <- value
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var blocked bool
			if err := db.migration.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, secondPID).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("second refresh did not wait on the first writer's source lock")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := first.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		value := <-result
		if value.err != nil || value.accepted {
			t.Fatalf("second refresh=%t err=%v", value.accepted, value.err)
		}
		if err := second.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("RefreshAndFailureFences", func(t *testing.T) {
		for _, scenario := range []string{"cross-account", "worker", "lease", "expired", "lost-lease", "cancelled", "parent-cancelled", "deleted", "generation", "account-deleting", "connection-failed"} {
			t.Run(scenario, func(t *testing.T) {
				f := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-icloud")
				worker := icloudWorker
				account, lease := f.account, f.lease
				switch scenario {
				case "cross-account":
					account = uuid.New()
				case "worker":
					worker = "other-worker"
				case "lease":
					lease = uuid.New()
				case "expired", "lost-lease":
					icloudExpireLease(t, db, f)
					if scenario == "lost-lease" {
						if !callBool(t, ctx, db.worker, f.account, `SELECT app.recover_expired_job($1)`, f.job) ||
							!callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'replacement-worker',$2,interval '2 minutes')`, f.job, uuid.New()) {
							t.Fatal("could not transfer expired lease")
						}
					}
				case "cancelled", "parent-cancelled":
					job := f.job
					if scenario == "parent-cancelled" {
						job = f.parent
					}
					if !callBool(t, ctx, db.migration, f.account, `SELECT app.request_job_cancellation($1,$2)`, job, uuid.New()) {
						t.Fatal("could not cancel fixture")
					}
				case "deleted":
					if !callBool(t, ctx, db.migration, f.account, `SELECT app.delete_source($1,$2)`, f.source, uuid.New()) {
						t.Fatal("could not delete fixture source")
					}
				case "generation":
					icloudFixtureExec(t, db, f, `UPDATE app.sources SET generation=generation+1 WHERE id=$1`, f.source)
				case "account-deleting":
					icloudFixtureExec(t, db, f, `UPDATE app.accounts SET state='deleting' WHERE id=$1`, f.account)
				case "connection-failed":
					icloudFixtureExec(t, db, f, `UPDATE app.sources SET status='connection-failed' WHERE id=$1`, f.source)
				}
				if callBool(t, ctx, db.worker, account, `SELECT app.refresh_source_credentials($1,$2,$3,1,$4)`, f.job, worker, lease, syntheticICloudEnvelope(t)) ||
					callBool(t, ctx, db.worker, account, `SELECT app.fail_source_connection($1,$2,$3,1,'source-auth-expired','private-provider-detail')`, f.job, worker, lease) {
					t.Fatal("stale credential/source failure write was accepted")
				}
			})
		}
	})
	t.Run("ConnectionCheckRefreshAndCleanup", func(t *testing.T) {
		for _, scenario := range []string{"refresh", "null", "stale", "cancelled", "deleted", "expired", "failure"} {
			t.Run(scenario, func(t *testing.T) {
				f := newICloudFixture(t, db, "source_connection_check", "health-auto-export-icloud")
				var refresh any = syntheticICloudEnvelope(t)
				status := "connected"
				var code, summary any
				switch scenario {
				case "null":
					refresh = nil
				case "failure":
					status, code, summary, refresh = "connection-failed", "source-auth-failed", "Source authentication failed.", nil
				case "stale":
					icloudFixtureExec(t, db, f, `UPDATE app.sources SET generation=2,status='connection-failed' WHERE id=$1`, f.source)
				case "cancelled":
					_ = callBool(t, ctx, db.migration, f.account, `SELECT app.request_job_cancellation($1,$2)`, f.job, uuid.New())
				case "deleted":
					_ = callBool(t, ctx, db.migration, f.account, `SELECT app.delete_source($1,$2)`, f.source, uuid.New())
				case "expired":
					icloudExpireLease(t, db, f)
				}
				finished := callBool(t, ctx, db.worker, f.account, `SELECT app.finish_connection_check_with_refresh($1,$2,$3,$4,$5,$6,$7)`,
					f.job, icloudWorker, f.lease, status, code, summary, refresh)
				if finished != (scenario != "expired") {
					t.Fatalf("finished=%t", finished)
				}
				inspect := beginAccount(t, ctx, db.migration, f.account)
				defer inspect.Rollback(ctx)
				var generation int64
				var sourceStatus, jobStatus string
				var snapshots, notifications int
				var stored []byte
				if err := inspect.QueryRow(ctx, `SELECT source.generation,source.status,source.config_envelope,job.status,
					(SELECT count(*) FROM app.job_config_snapshots WHERE job_id=$1),
					(SELECT count(*) FROM app.notifications WHERE source_id=$2 AND type='source-connection-failed')
					FROM app.sources source JOIN app.jobs job ON job.id=$1 WHERE source.id=$2`, f.job, f.source).
					Scan(&generation, &sourceStatus, &stored, &jobStatus, &snapshots, &notifications); err != nil {
					t.Fatal(err)
				}
				wantGeneration, wantSource, wantJob, wantSnapshots, wantNotifications := int64(1), "checking-connection", "succeeded", 0, 0
				switch scenario {
				case "refresh":
					wantGeneration, wantSource = 2, "connected"
				case "null":
					wantSource = "connected"
				case "stale":
					wantGeneration, wantSource = 2, "connection-failed"
				case "cancelled", "deleted":
					wantJob = "cancelled"
				case "expired":
					wantJob, wantSnapshots = "running", 1
				case "failure":
					wantSource, wantJob, wantNotifications = "connection-failed", "failed", 1
				}
				if generation != wantGeneration || sourceStatus != wantSource || jobStatus != wantJob || snapshots != wantSnapshots || notifications != wantNotifications {
					t.Fatalf("completion state=%d/%s/%s/%d/%d", generation, sourceStatus, jobStatus, snapshots, notifications)
				}
				if scenario == "refresh" {
					var actual, expected any
					_ = json.Unmarshal(stored, &actual)
					_ = json.Unmarshal(refresh.([]byte), &expected)
					if fmt.Sprint(actual) != fmt.Sprint(expected) {
						t.Fatal("refreshed source envelope was not stored")
					}
				}
			})
		}
	})
	t.Run("CompletionExpiryRollsBackSourceAndNotifications", func(t *testing.T) {
		f := newICloudFixture(t, db, "source_connection_check", "health-auto-export-icloud")
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		// Expire the lease after the source write, before finish_job's second fence.
		if _, err := tx.Exec(ctx, `CREATE FUNCTION app.test_expire_check_lease() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				PERFORM set_config('app.job_transition','heartbeat',true);
				UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=TG_ARGV[0]::uuid;
				RETURN NEW;
			END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER test_expire_check_lease AFTER UPDATE ON app.sources
			FOR EACH ROW EXECUTE FUNCTION app.test_expire_check_lease('%s')`, f.job)); err != nil {
			t.Fatal(err)
		}
		var before []byte
		if err := tx.QueryRow(ctx, `SELECT config_envelope FROM app.sources WHERE id=$1`, f.source).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_worker`); err != nil {
			t.Fatal(err)
		}
		var finished bool
		if err := tx.QueryRow(ctx, `SELECT app.finish_connection_check_with_refresh($1,$2,$3,'connected',NULL,NULL,$4)`,
			f.job, icloudWorker, f.lease, syntheticICloudEnvelope(t)).Scan(&finished); err != nil || finished {
			t.Fatalf("expired completion=%t err=%v", finished, err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_migration`); err != nil {
			t.Fatal(err)
		}
		var intact bool
		if err := tx.QueryRow(ctx, `SELECT source.status='checking-connection' AND source.generation=1 AND source.config_envelope=$3
			AND job.status='running' AND EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.notifications WHERE source_id=$2)
			FROM app.sources source JOIN app.jobs job ON job.id=$1 WHERE source.id=$2`, f.job, f.source, before).Scan(&intact); err != nil || !intact {
			t.Fatalf("completion partially persisted after losing lease: intact=%t err=%v", intact, err)
		}
	})
	t.Run("SafeSourceFailureAndEligibility", func(t *testing.T) {
		for _, code := range []string{"source-auth-expired", "source-auth-failed", "source-adp-approval-required"} {
			f := newICloudFixture(t, db, "scheduled_ingest_source", "health-auto-export-icloud")
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.fail_source_connection($1,$2,$3,1,$4,$5)`, f.job, icloudWorker, f.lease, code, strings.Repeat("secret-marker", 100)) {
				t.Fatal("matching auth failure was rejected")
			}
			inspect := beginAccount(t, ctx, db.migration, f.account)
			var status, summary, message string
			var generation int64
			var snapshots, eligible int
			if err := inspect.QueryRow(ctx, `SELECT source.status,source.generation,source.status_summary,notification.message,
				(SELECT count(*) FROM app.job_config_snapshots WHERE job_id=$1),
				(SELECT count(*) FROM app.sources WHERE account_id=$2 AND status='connected' AND deleted_at IS NULL)
				FROM app.sources source JOIN app.notifications notification ON notification.source_id=source.id
				WHERE source.id=$3 AND notification.type='source-connection-failed'`, f.job, f.account, f.source).
				Scan(&status, &generation, &summary, &message, &snapshots, &eligible); err != nil {
				t.Fatal(err)
			}
			if status != "connection-failed" || generation != 1 || snapshots != 1 || eligible != 0 || summary != message || strings.Contains(summary, "secret-marker") {
				t.Fatal("source failure leaked provider details, changed generation/snapshot, or retained sync eligibility")
			}
			_ = inspect.Rollback(ctx)
			// Exercise the existing scheduler reader, not just an eligibility predicate.
			icloudFixtureExec(t, db, f, `INSERT INTO app.account_sync_schedules(account_id,next_run_at,lease_worker,lease_token,lease_expires_at,lease_runtime_version)
				VALUES($1,clock_timestamp(),$2,$3,clock_timestamp()+interval '1 minute',$4)`, f.account, icloudWorker, f.lease, dbRuntime)
			var leased int
			if err := db.worker.QueryRow(ctx, `SELECT count(*) FROM app.read_leased_sync_sources($1,$2,$3)`, f.account, icloudWorker, f.lease).Scan(&leased); err != nil || leased != 0 {
				t.Fatalf("failed source remained scheduled: count=%d err=%v", leased, err)
			}
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,$2,$3,'failed',$4,'Source authentication failed.')`, f.job, icloudWorker, f.lease, code) {
				t.Fatal("failure cleared live snapshot before the terminal transition")
			}
			checkID, checkLease := uuid.New(), uuid.New()
			setup := beginAccount(t, ctx, db.migration, f.account)
			if _, err := setup.Exec(ctx, `UPDATE app.sources SET status='checking-connection' WHERE id=$1`, f.source); err != nil {
				t.Fatal(err)
			}
			if _, err := setup.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'source_connection_check',100)`, checkID, f.account); err != nil {
				t.Fatal(err)
			}
			if _, err := setup.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
				VALUES($1,$2,$3,1,$4)`, checkID, f.account, f.source, syntheticICloudEnvelope(t)); err != nil {
				t.Fatal(err)
			}
			if err := setup.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '1 minute')`, checkID, icloudWorker, checkLease) ||
				!callBool(t, ctx, db.worker, f.account, `SELECT app.finish_connection_check_with_refresh($1,$2,$3,'connected',NULL,NULL,NULL)`, checkID, icloudWorker, checkLease) {
				t.Fatal("reconnection check failed")
			}
			resolved := beginAccount(t, ctx, db.api, f.account)
			var notificationState string
			if err := resolved.QueryRow(ctx, `SELECT state FROM app.notifications WHERE source_id=$1 AND type='source-connection-failed'`, f.source).Scan(&notificationState); err != nil || notificationState != "resolved" {
				t.Fatalf("reconnection notification=%s err=%v", notificationState, err)
			}
			_ = resolved.Rollback(ctx)
		}
		f := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-icloud")
		for _, code := range []string{"network-timeout", "source-config-invalid", "", "source-auth-expired secret"} {
			tx := beginAccount(t, ctx, db.worker, f.account)
			_, err := tx.Exec(ctx, `SELECT app.fail_source_connection($1,$2,$3,1,$4,'secret-marker')`, f.job, icloudWorker, f.lease, code)
			_ = tx.Rollback(ctx)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "22023" || strings.Contains(pgError.Message, "secret-marker") {
				t.Fatalf("unsafe/transient failure code was accepted or leaked: %v", err)
			}
		}
	})
	t.Run("OpaqueManifestExactRecoveryAndValidation", func(t *testing.T) {
		f := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-icloud")
		identity := "v2:rclone:" + strings.Repeat("a", 64)
		remote := strings.Replace(icloudManifest, `"action":"process"`, `"action":"process","observed_identity":"`+identity+`"`, 1)
		if icloudRecordManifest(t, db, f, remote) != "created" {
			t.Fatal("remote manifest not created")
		}
		icloudExpireLease(t, db, f)
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.recover_expired_job($1)`, f.job) {
			t.Fatal("manifest lease recovery failed")
		}
		oldLease := f.lease
		f.lease = uuid.New()
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '2 minutes')`, f.job, icloudWorker, f.lease) {
			t.Fatal("manifest reclaim failed")
		}
		if icloudRecordManifest(t, db, f, remote) != "matched" ||
			icloudRecordManifest(t, db, f, strings.Replace(remote, identity, "v2:rclone:"+strings.Repeat("b", 64), 1)) != "mismatch" ||
			icloudRecordManifest(t, db, f, icloudManifest) != "mismatch" || icloudRecordManifest(t, db, f, `[]`) != "mismatch" {
			t.Fatal("remote identity did not participate in exact recovery equality")
		}
		tx := beginAccount(t, ctx, db.worker, f.account)
		var state *string
		if err := tx.QueryRow(ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, f.job, icloudWorker, oldLease, remote).Scan(&state); err != nil || state != nil {
			t.Fatalf("old manifest lease accepted: %v err=%v", state, err)
		}
		_ = tx.Rollback(ctx)
		for _, bad := range []string{
			strings.Replace(remote, `"observed_identity":"`+identity+`"`, `"observed_identity":null`, 1),
			strings.Replace(remote, identity, "v2:rclone:bad", 1),
			strings.Replace(remote, identity, strings.Repeat("x", 513), 1),
			strings.Replace(remote, `"action":"process"`, `"action":"process","extra":true`, 1),
			strings.Replace(icloudManifest, `"device":0`, `"extra":0`, 1),
			strings.Replace(remote, `"size":10`, `"size":null`, 1),
			strings.Replace(remote, `"size":10`, `"size":"10"`, 1),
			strings.Replace(remote, `"modified_ns":1`, `"modified_ns":1000000000`, 1),
			"[" + strings.Trim(icloudManifest, "[]") + "," + strings.Trim(icloudManifest, "[]") + "]",
			"[" + strings.Repeat(strings.Trim(icloudManifest, "[]")+",", 10000) + strings.Trim(icloudManifest, "[]") + "]",
		} {
			tx := beginAccount(t, ctx, db.worker, f.account)
			_, err := tx.Exec(ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, f.job, icloudWorker, f.lease, bad)
			_ = tx.Rollback(ctx)
			if err == nil {
				t.Fatal("invalid remote manifest accepted")
			}
		}
		inspect := beginAccount(t, ctx, db.migration, f.account)
		defer inspect.Rollback(ctx)
		if _, err := inspect.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := inspect.QueryRow(ctx, `SELECT observed_identity FROM app.job_file_candidates WHERE job_id=$1`, f.job).Scan(&stored); err != nil || stored != identity {
			t.Fatalf("stored identity changed: %q err=%v", stored, err)
		}
	})
	t.Run("EnvelopeValidationFailsClosed", func(t *testing.T) {
		f := newICloudFixture(t, db, "manual_ingest_source", "health-auto-export-icloud")
		valid := syntheticICloudEnvelope(t)
		invalid := []string{"null", `{}`, `[]`, strings.Replace(string(valid), `"version":1`, `"version":2`, 1),
			strings.Replace(string(valid), `"keyId":"synthetic"`, `"keyId":"bad key"`, 1),
			strings.Replace(string(valid), `"version":1`, `"version":1,"plaintext":"secret-marker"`, 1)}
		for _, field := range []struct{ key, value string }{
			{"algorithm", "AES-128-GCM"},
			{"wrappedKeyNonce", base64.RawURLEncoding.EncodeToString(make([]byte, 11))},
			{"payloadNonce", strings.Repeat("A", 16) + "="},
			{"wrappedKey", base64.RawURLEncoding.EncodeToString(make([]byte, 47))},
			{"ciphertext", base64.RawURLEncoding.EncodeToString(make([]byte, 15))},
			{"ciphertext", base64.RawURLEncoding.EncodeToString(make([]byte, 65537))},
			{"ciphertext", strings.Repeat("A", 21) + "B"}, // Noncanonical trailing base64 bits.
		} {
			var document map[string]any
			if err := json.Unmarshal(valid, &document); err != nil {
				t.Fatal(err)
			}
			document[field.key] = field.value
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			invalid = append(invalid, string(encoded))
		}
		for _, bad := range invalid {
			tx := beginAccount(t, ctx, db.worker, f.account)
			_, err := tx.Exec(ctx, `SELECT app.refresh_source_credentials($1,$2,$3,1,$4)`, f.job, icloudWorker, f.lease, bad)
			_ = tx.Rollback(ctx)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "22023" || strings.Contains(pgError.Message, "secret-marker") {
				t.Fatalf("invalid envelope result: %v", err)
			}
		}
	})
}

func syntheticICloudEnvelope(t *testing.T) []byte {
	t.Helper()
	encoded := func(size int) string {
		value := make([]byte, size)
		if _, err := rand.Read(value); err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(value)
	}
	value, err := json.Marshal(map[string]any{"version": 1, "algorithm": "AES-256-GCM+AES-256-GCM", "keyId": "synthetic",
		"wrappedKeyNonce": encoded(12), "wrappedKey": encoded(48), "payloadNonce": encoded(12), "ciphertext": encoded(32)})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newICloudFixture(t *testing.T, db testDatabases, kind, sourceType string) icloudFixture {
	t.Helper()
	f := icloudFixture{account: uuid.New(), source: uuid.New(), job: uuid.New(), lease: uuid.New(), snapshot: syntheticICloudEnvelope(t)}
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, f.account); err != nil {
		t.Fatal(err)
	}
	status := "connected"
	if kind == "source_connection_check" {
		status = "checking-connection"
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope)
		VALUES($1,$2,'Synthetic source','synthetic source',$3,$4,true,$5)`, f.source, f.account, sourceType, status, syntheticICloudEnvelope(t)); err != nil {
		t.Fatal(err)
	}
	var parent any
	parameters := `{}`
	if kind != "source_connection_check" {
		f.parent = uuid.New()
		parent = f.parent
		parentKind := strings.TrimSuffix(kind, "_source")
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,$3,80)`, f.parent, f.account, parentKind); err != nil {
			t.Fatal(err)
		}
		parameters = fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.source.String(), "-", "")))
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,parameters) VALUES($1,$2,$3,$4,80,$5)`, f.job, f.account, parent, kind, parameters); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, f.job, f.account, f.source, f.snapshot); err != nil {
		t.Fatal(err)
	}
	if parent != nil {
		// Deliberately misleading display/log context must not control dispatch.
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			VALUES($1,$2,$3,1,'Public log context','health-auto-export-local')`, f.job, f.account, f.source); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '2 minutes')`, f.job, icloudWorker, f.lease) {
		t.Fatal("fixture claim failed")
	}
	return f
}

func icloudRefresh(t *testing.T, db testDatabases, f icloudFixture, generation int64, envelope []byte) bool {
	t.Helper()
	return callBool(t, db.ctx, db.worker, f.account, `SELECT app.refresh_source_credentials($1,$2,$3,$4,$5)`, f.job, icloudWorker, f.lease, generation, envelope)
}

func icloudRecordManifest(t *testing.T, db testDatabases, f icloudFixture, manifest string) string {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	var state string
	if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, f.job, icloudWorker, f.lease, manifest).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return state
}

func icloudFixtureExec(t *testing.T, db testDatabases, f icloudFixture, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	if _, err := tx.Exec(db.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func icloudExpireLease(t *testing.T, db testDatabases, f icloudFixture) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	if _, err := tx.Exec(db.ctx, `SELECT set_config('app.job_transition','heartbeat',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.job); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}
