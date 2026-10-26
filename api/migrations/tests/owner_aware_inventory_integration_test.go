package migrations_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const ownershipInventoryWorker = "ownership-inventory-worker"
const ownershipInventoryName = "HealthAutoExport-2026-01-01.json"
const ownershipInventoryIdentity = "v1:10:100:1:2:3:101:4"
const ownershipInventoryCandidates = `[{"name":"HealthAutoExport-2026-01-01.json","export_date":"2026-01-01","size":10,"modified_sec":100,"modified_ns":1,"device":2,"inode":3,"ctime_sec":101,"ctime_ns":4,"action":"process"}]`

type ownershipInventoryFixture struct {
	account, source, parent, job, lease uuid.UUID
}

// These tests commit synthetic history to exercise independent transactions.
// They require an explicit disposable-database opt-in and never load app config.
func TestOwnerAwareInventoryPostgreSQL(t *testing.T) {
	if os.Getenv("owner_aware_inventory_DISPOSABLE") != "1" {
		t.Skip("owner_aware_inventory_DISPOSABLE=1 and disposable role database URLs are required")
	}
	db := ownershipInventoryDatabases(t)

	t.Run("runtime readiness and narrow helper grants", func(t *testing.T) {
		apiReady, workerReady, coverageReady := database.Ready(db.ctx, db.api), database.Ready(db.ctx, db.worker), database.Ready(db.ctx, db.coverage)
		if !apiReady || !workerReady || !coverageReady {
			t.Fatalf("runtime readiness api/worker/coverage=%t/%t/%t", apiReady, workerReady, coverageReady)
		}
		var safe bool
		if err := db.migration.QueryRow(db.ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner' AND p.prosecdef
			AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) WHERE grantee=0)
			AND NOT has_table_privilege('workouts_worker','app.job_file_candidates','SELECT')
			AND to_regprocedure('app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamp with time zone,text,bytea)') IS NOT NULL
			FROM pg_proc p WHERE p.oid='app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)'::regprocedure`).Scan(&safe); err != nil || !safe {
			t.Fatalf("unsafe helper grants/signature: safe=%t err=%v", safe, err)
		}
	})

	t.Run("epoch advances only on committed source deletion", func(t *testing.T) {
		f := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		other := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		ownershipInventoryExec(t, db, tx, `UPDATE app.sources SET auto_sync_enabled=false,status='connection-failed',generation=generation+1,config_envelope=$2 WHERE id=$1`, f.source, []byte(`{"synthetic":"auth-refresh"}`))
		ownershipInventoryCommit(t, db, tx)
		ownershipInventoryEpoch(t, db, f, 0)
		tx = beginAccount(t, db.ctx, db.api, f.account)
		var deleted bool
		if err := tx.QueryRow(db.ctx, `SELECT app.delete_source($1,$2)`, f.source, uuid.New()).Scan(&deleted); err != nil || !deleted {
			t.Fatalf("rolled-back deletion=%t err=%v", deleted, err)
		}
		_ = tx.Rollback(db.ctx)
		ownershipInventoryEpoch(t, db, f, 0)
		if !callBool(t, db.ctx, db.api, f.account, `SELECT app.delete_source($1,$2)`, f.source, uuid.New()) {
			t.Fatal("source deletion failed")
		}
		ownershipInventoryEpoch(t, db, f, 1)
		ownershipInventoryEpoch(t, db, other, 0)
		if callBool(t, db.ctx, db.api, f.account, `SELECT app.delete_source($1,$2)`, f.source, uuid.New()) ||
			callBool(t, db.ctx, db.api, other.account, `SELECT app.delete_source($1,$2)`, f.source, uuid.New()) {
			t.Fatal("repeated or cross-account deletion succeeded")
		}
		ownershipInventoryEpoch(t, db, f, 1)
	})

	t.Run("mixed and all-blocked files retain bytes and retry once per epoch", func(t *testing.T) {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("mixed=%t", mixed), func(t *testing.T) {
				owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
				workout := ownershipInventoryWorkout(t, db, owner)
				f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
				file := ownershipInventoryBlockedFile(t, db, f, workout, mixed)
				ownershipInventoryRecord(t, db, f, ownershipInventoryName, ownershipInventoryIdentity)
				ownershipInventoryMarker(t, db, f, int64ptr(0))
				ownershipInventoryAction(t, db, f, ownershipInventoryCandidates, "process")
				ownershipInventoryFinish(t, db, f)
				fresh := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
				ownershipInventoryAction(t, db, fresh, ownershipInventoryCandidates, "skip")
				// Config generations and connection status are not ownership changes.
				tx := beginAccount(t, db.ctx, db.migration, f.account)
				ownershipInventoryExec(t, db, tx, `UPDATE app.sources SET generation=generation+1,status='connection-failed' WHERE id=$1`, owner.source)
				ownershipInventoryCommit(t, db, tx)
				ownershipInventoryAction(t, db, fresh, ownershipInventoryCandidates, "skip")
				ownershipInventoryFinish(t, db, fresh)
				if !callBool(t, db.ctx, db.api, owner.account, `SELECT app.delete_source($1,$2)`, owner.source, uuid.New()) {
					t.Fatal("owner deletion failed")
				}
				ownershipInventoryMarker(t, db, f, int64ptr(0))
				next := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
				ownershipInventoryAction(t, db, next, ownershipInventoryCandidates, "process")
				// A completed checkpoint is history, not something to reopen.
				tx = beginAccount(t, db.ctx, db.migration, f.account)
				var terminal bool
				if err := tx.QueryRow(db.ctx, `SELECT state='succeeded' FROM app.source_files WHERE id=$1`, file).Scan(&terminal); err != nil || !terminal {
					t.Fatalf("checkpoint terminal=%t err=%v", terminal, err)
				}
				var firstSeen time.Time
				if err := tx.QueryRow(db.ctx, `SELECT last_new_export_discovered_at FROM app.source_sync_state WHERE source_id=$1`, f.source).Scan(&firstSeen); err != nil {
					t.Fatal(err)
				}
				_ = tx.Rollback(db.ctx)
				// No skipped_live_owner event in the new job: same successful bytes
				// now clear the marker instead of inheriting an old job's blockers.
				ownershipInventoryRecord(t, db, next, ownershipInventoryName, ownershipInventoryIdentity)
				ownershipInventoryMarker(t, db, next, nil)
				ownershipInventoryFinish(t, db, next)
				last := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
				ownershipInventoryAction(t, db, last, ownershipInventoryCandidates, "skip")
				tx = beginAccount(t, db.ctx, db.migration, f.account)
				var unchanged bool
				if err := tx.QueryRow(db.ctx, `SELECT last_new_export_discovered_at=$2 FROM app.source_sync_state WHERE source_id=$1`, f.source, firstSeen).Scan(&unchanged); err != nil || !unchanged {
					t.Fatalf("reprocessing reset firstSeen=%t err=%v", !unchanged, err)
				}
				_ = tx.Rollback(db.ctx)
			})
		}
	})

	t.Run("frozen skip survives deletion but metadata remains exact", func(t *testing.T) {
		owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		workout := ownershipInventoryWorkout(t, db, owner)
		f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
		ownershipInventoryBlockedFile(t, db, f, workout, false)
		ownershipInventoryRecord(t, db, f, ownershipInventoryName, ownershipInventoryIdentity)
		ownershipInventoryFinish(t, db, f)
		resume := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		skip := strings.Replace(ownershipInventoryCandidates, `"action":"process"`, `"action":"skip"`, 1)
		ownershipInventoryManifest(t, db, resume, skip, "created")
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		var recorded bool
		if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_progress($1,$2,$3,1,1,0,0,0,0,0,0)`, resume.job, ownershipInventoryWorker, resume.lease).Scan(&recorded); err != nil || !recorded {
			t.Fatalf("skip progress recorded=%t err=%v", recorded, err)
		}
		ownershipInventoryCommit(t, db, tx)
		if !callBool(t, db.ctx, db.api, owner.account, `SELECT app.delete_source($1,$2)`, owner.source, uuid.New()) {
			t.Fatal("owner source deletion failed")
		}
		ownershipInventoryAction(t, db, resume, ownershipInventoryCandidates, "skip")
		ownershipInventoryManifest(t, db, resume, skip, "matched")
		changed := strings.Replace(skip, `"inode":3`, `"inode":30`, 1)
		ownershipInventoryAction(t, db, resume, changed, "skip")
		ownershipInventoryManifest(t, db, resume, changed, "mismatch")
		ownershipInventoryManifest(t, db, resume, `[]`, "mismatch")
		tx = beginAccount(t, db.ctx, db.migration, f.account)
		var unchanged bool
		if err := tx.QueryRow(db.ctx, `SELECT files_discovered=1 AND files_skipped=1 AND files_succeeded=0 AND files_failed=0
			AND workouts_created=0 AND workouts_updated=0 AND workouts_unchanged=0 AND workouts_rejected=0
			AND NOT EXISTS(SELECT 1 FROM app.source_files WHERE job_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.workout_import_events WHERE job_id=$1)
			FROM app.job_progress WHERE job_id=$1`, resume.job).Scan(&unchanged); err != nil || !unchanged {
			t.Fatalf("frozen replay reopened/recounted file or changed counters: unchanged=%t err=%v", unchanged, err)
		}
		_ = tx.Rollback(db.ctx)
		ownershipInventoryFinish(t, db, resume)
		fresh := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		ownershipInventoryAction(t, db, fresh, ownershipInventoryCandidates, "process")
	})

	t.Run("adoption does not advance deletion epoch", func(t *testing.T) {
		owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		workout := ownershipInventoryWorkout(t, db, owner)
		f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
		if !callBool(t, db.ctx, db.api, owner.account, `SELECT app.delete_source($1,$2)`, owner.source, uuid.New()) {
			t.Fatal("owner source deletion failed")
		}
		tx := ownershipInventoryFence(t, db, f)
		file := uuid.New()
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at)
			VALUES($1,$2,$3,$4,$5,10,now())`, file, f.account, f.source, f.job, ownershipInventoryName)
		var outcome string
		if err := tx.QueryRow(db.ctx, `SELECT outcome FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,
			(SELECT provider_id FROM app.workouts WHERE id=$6),NULL,NULL)`, f.job, ownershipInventoryWorker, f.lease, file, uuid.New(), workout).Scan(&outcome); err != nil || outcome != "adopted" {
			t.Fatalf("adoption outcome=%q err=%v", outcome, err)
		}
		ownershipInventoryExec(t, db, tx, `UPDATE app.workouts SET source_id=$2,source_file_id=$3 WHERE id=$1`, workout, f.source, file)
		ownershipInventoryCommit(t, db, tx)
		ownershipInventoryEpoch(t, db, f, 1)
	})

	t.Run("bounded remote identity empty and malformed candidates", func(t *testing.T) {
		f := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		remoteID := "v2:rclone:" + strings.Repeat("a", 64)
		remote := strings.Replace(ownershipInventoryCandidates, `"action":"process"`, `"action":"process","observed_identity":"`+remoteID+`"`, 1)
		ownershipInventoryRecord(t, db, f, ownershipInventoryName, remoteID)
		ownershipInventoryFinish(t, db, f)
		incremental := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		ownershipInventoryAction(t, db, incremental, remote, "skip")
		ownershipInventoryAction(t, db, incremental, ownershipInventoryCandidates, "process")
		ownershipInventoryManifest(t, db, incremental, strings.Replace(remote, `"action":"process"`, `"action":"skip"`, 1), "created")
		ownershipInventoryManifest(t, db, incremental, strings.Replace(remote, remoteID, "v2:rclone:"+strings.Repeat("b", 64), 1), "mismatch")
		ownershipInventoryFinish(t, db, incremental)
		bounded := ownershipInventoryJob(t, db, f.account, f.source, "bounded")
		ownershipInventoryAction(t, db, bounded, remote, "process")
		ownershipInventoryAction(t, db, bounded, `[]`, "")
		for _, malformed := range []string{
			`null`, `{}`, `[null]`, `[{}]`,
			strings.Replace(remote, `"size":10`, `"size":null`, 1),
			strings.Replace(remote, `"action":"process"`, `"action":"unknown"`, 1),
			strings.Replace(remote, remoteID, "bad-identity", 1),
			strings.Replace(remote, `"modified_ns":1`, `"modified_ns":1000000000`, 1),
			strings.Replace(remote, `"device":2`, `"device":18446744073709551616`, 1),
			strings.Replace(remote, `"inode":3`, `"inode":3,"private":"payload"`, 1),
			"[" + strings.Trim(remote, "[]") + "," + strings.Trim(remote, "[]") + "]",
			"[" + strings.Repeat(strings.Trim(remote, "[]")+",", 10000) + strings.Trim(remote, "[]") + "]",
		} {
			tx := beginAccount(t, db.ctx, db.worker, bounded.account)
			_, err := tx.Exec(db.ctx, `SELECT * FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, bounded.job, ownershipInventoryWorker, bounded.lease, malformed)
			_ = tx.Rollback(db.ctx)
			var postgres *pgconn.PgError
			if !errors.As(err, &postgres) || postgres.Code != "22023" {
				t.Fatalf("malformed candidate error=%v, want 22023", err)
			}
		}
	})

	t.Run("leased account boundary and private tables", func(t *testing.T) {
		f := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		other := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		for _, attempt := range []struct {
			account, lease uuid.UUID
			worker         string
		}{
			{other.account, f.lease, ownershipInventoryWorker},
			{f.account, uuid.New(), ownershipInventoryWorker},
			{f.account, f.lease, "wrong-worker"},
		} {
			tx := beginAccount(t, db.ctx, db.worker, attempt.account)
			var count int
			if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, f.job, attempt.worker, attempt.lease, ownershipInventoryCandidates).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unfenced candidates=%d err=%v", count, err)
			}
			_ = tx.Rollback(db.ctx)
		}
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		var recorded bool
		if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,'2026-01-01',10,now(),$5,$6)`,
			f.job, ownershipInventoryWorker, f.lease, ownershipInventoryName, ownershipInventoryIdentity, bytes.Repeat([]byte{7}, 32)).Scan(&recorded); err != nil || recorded {
			t.Fatalf("recorder accepted missing capability=%t err=%v", recorded, err)
		}
		_ = tx.Rollback(db.ctx)
		for _, query := range []string{
			`SELECT * FROM app.job_file_candidates`,
			`SELECT * FROM app.job_file_candidate_sets`,
			`INSERT INTO app.ingest_write_capabilities(backend_pid,transaction_id,account_id,source_id,job_id,worker_id,lease_token,ownership_epoch) VALUES(pg_backend_pid(),txid_current(),$1,$2,$3,$4,$5,0)`,
		} {
			tx = beginAccount(t, db.ctx, db.worker, f.account)
			var args []any
			if strings.HasPrefix(query, "INSERT") {
				args = []any{f.account, f.source, f.job, ownershipInventoryWorker, f.lease}
			}
			_, err := tx.Exec(db.ctx, query, args...)
			_ = tx.Rollback(db.ctx)
			var postgres *pgconn.PgError
			if !errors.As(err, &postgres) || postgres.Code != "42501" {
				t.Fatalf("private inventory/capability error=%v", err)
			}
		}
		ownershipInventoryFinish(t, db, f)
		tx = beginAccount(t, db.ctx, db.worker, f.account)
		var count int
		if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, f.job, ownershipInventoryWorker, f.lease, ownershipInventoryCandidates).Scan(&count); err != nil || count != 0 {
			t.Fatalf("terminal job candidates=%d err=%v", count, err)
		}
		_ = tx.Rollback(db.ctx)
	})

	t.Run("inactive cancelled expired and deleted jobs fail closed", func(t *testing.T) {
		for _, scenario := range []string{"account-deleting", "cancelled", "parent-cancelled", "expired", "source-deleted"} {
			t.Run(scenario, func(t *testing.T) {
				f := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
				tx := beginAccount(t, db.ctx, db.migration, f.account)
				switch scenario {
				case "account-deleting":
					ownershipInventoryExec(t, db, tx, `UPDATE app.accounts SET state='deleting' WHERE id=$1`, f.account)
				case "cancelled", "parent-cancelled":
					job := f.job
					if scenario == "parent-cancelled" {
						job = f.parent
					}
					ownershipInventoryExec(t, db, tx, `SELECT app.request_job_cancellation($1,$2)`, job, uuid.New())
				case "expired":
					ownershipInventoryExec(t, db, tx, `SELECT set_config('app.job_transition','heartbeat',true)`)
					ownershipInventoryExec(t, db, tx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.job)
				case "source-deleted":
					ownershipInventoryExec(t, db, tx, `SELECT app.delete_source($1,$2)`, f.source, uuid.New())
				}
				ownershipInventoryCommit(t, db, tx)
				ownershipInventoryAction(t, db, f, ownershipInventoryCandidates, "")
			})
		}
	})
}

func ownershipInventoryDatabases(t *testing.T) testDatabases {
	t.Helper()
	var databaseName string
	var port uint16
	for _, name := range []string{"API_DATABASE_URL", "WORKER_DATABASE_URL", "MIGRATION_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL", "TILE_DATABASE_URL"} {
		value := os.Getenv(name)
		if value == "" && (name == "COVERAGE_WORKER_DATABASE_URL" || name == "TILE_DATABASE_URL") {
			continue
		}
		config, err := pgx.ParseConfig(value)
		if value == "" || err != nil || !strings.HasPrefix(config.Database, "workouts_ownership_inventory_test_") ||
			(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
			t.Fatalf("%s must explicitly name a disposable loopback ownership-inventory database", name)
		}
		if databaseName == "" {
			databaseName, port = config.Database, config.Port
		}
		if config.Database != databaseName || config.Port != port {
			t.Fatalf("%s must target the same disposable database and port", name)
		}
		for _, fallback := range config.Fallbacks {
			if fallback.Host != config.Host || fallback.Port != port {
				t.Fatalf("%s must not include remote/fallback database endpoints", name)
			}
		}
	}
	return openTestDatabases(t)
}

func ownershipInventoryJob(t *testing.T, db testDatabases, account, source uuid.UUID, mode string) ownershipInventoryFixture {
	t.Helper()
	if account == uuid.Nil {
		account = uuid.New()
	}
	f := ownershipInventoryFixture{account: account, source: source, parent: uuid.New(), job: uuid.New(), lease: uuid.New()}
	tx := beginAccount(t, db.ctx, db.migration, account)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT DO NOTHING`, account)
	if source == uuid.Nil {
		f.source = uuid.New()
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
			VALUES($1,$2,$3,$3,'health-auto-export-local','connected',$4)`, f.source, account, "inventory-"+f.source.String(), []byte(`{"synthetic":true}`))
	}
	var generation int64
	if err := tx.QueryRow(db.ctx, `SELECT generation FROM app.sources WHERE id=$1`, f.source).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":%d,"mode":"%s"}`, strings.ToUpper(strings.ReplaceAll(f.source.String(), "-", "")), generation, mode)
	if mode == "bounded" {
		parameters = strings.TrimSuffix(parameters, "}") + `,"startDate":"2026-01-01","endDate":"2026-01-01"}`
	}
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, f.parent, account)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.job, f.parent, account, parameters)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,$4,$5)`, f.job, account, f.source, generation, []byte(`{"synthetic":true}`))
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,$4,'Inventory source','health-auto-export-local') ON CONFLICT DO NOTHING`, f.job, account, f.source, generation)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3)`, f.parent, f.job, account)
	ownershipInventoryCommit(t, db, tx)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, f.job, ownershipInventoryWorker, f.lease) {
		t.Fatal("could not claim inventory job")
	}
	return f
}

func ownershipInventoryFence(t *testing.T, db testDatabases, f ownershipInventoryFixture) pgx.Tx {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, ownershipInventoryWorker, f.lease).Scan(&source); err != nil || source != f.source {
		t.Fatalf("inventory fence source=%s err=%v", source, err)
	}
	return tx
}

func ownershipInventoryWorkout(t *testing.T, db testDatabases, f ownershipInventoryFixture) uuid.UUID {
	t.Helper()
	tx := ownershipInventoryFence(t, db, f)
	file, workout, workoutType := uuid.New(), uuid.New(), uuid.New()
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at) VALUES($1,$2,$3,$4,$5,10,now())`, file, f.account, f.source, f.job, ownershipInventoryName)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, workoutType, f.account)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
		VALUES($1,$2,$3,$4,$5,$6,$7,'Walking','2026-01-01Z','2026-01-01T00:01:00Z',60)`, workout, f.account, f.source, file, workoutType, uuid.NewString(), bytes.Repeat([]byte{7}, 32))
	ownershipInventoryCommit(t, db, tx)
	ownershipInventoryFinish(t, db, f)
	return workout
}

func ownershipInventoryBlockedFile(t *testing.T, db testDatabases, f ownershipInventoryFixture, workout uuid.UUID, mixed bool) uuid.UUID {
	t.Helper()
	tx := ownershipInventoryFence(t, db, f)
	file := uuid.New()
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,state,processing_started_at)
		VALUES($1,$2,$3,$4,$5,10,now(),'processing',now())`, file, f.account, f.source, f.job, ownershipInventoryName)
	ownershipInventoryExec(t, db, tx, `INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256)
		VALUES($1,$2,$3,$4,$5,$6,'skipped_live_owner',$7)`, uuid.New(), f.account, f.source, file, f.job, workout, bytes.Repeat([]byte{7}, 32))
	if mixed {
		workoutType, ownWorkout := uuid.New(), uuid.New()
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'running','Running')`, workoutType, f.account)
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
			VALUES($1,$2,$3,$4,$5,$6,$7,'Running','2026-01-01Z','2026-01-01T00:01:00Z',60)`, ownWorkout, f.account, f.source, file, workoutType, uuid.NewString(), bytes.Repeat([]byte{7}, 32))
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256)
			VALUES($1,$2,$3,$4,$5,$6,'created',$7)`, uuid.New(), f.account, f.source, file, f.job, ownWorkout, bytes.Repeat([]byte{7}, 32))
	}
	ownershipInventoryExec(t, db, tx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=now() WHERE id=$1`, file, bytes.Repeat([]byte{7}, 32))
	ownershipInventoryCommit(t, db, tx)
	return file
}

func ownershipInventoryRecord(t *testing.T, db testDatabases, f ownershipInventoryFixture, name, identity string) {
	t.Helper()
	tx := ownershipInventoryFence(t, db, f)
	ownershipInventoryRecordTx(t, db, tx, f, name, identity)
	ownershipInventoryCommit(t, db, tx)
}

func ownershipInventoryRecordTx(t *testing.T, db testDatabases, tx pgx.Tx, f ownershipInventoryFixture, name, identity string) {
	t.Helper()
	var recorded bool
	if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,'2026-01-01',10,now(),$5,$6)`,
		f.job, ownershipInventoryWorker, f.lease, name, identity, bytes.Repeat([]byte{7}, 32)).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("inventory recorded=%t err=%v", recorded, err)
	}
}

func ownershipInventoryAction(t *testing.T, db testDatabases, f ownershipInventoryFixture, candidates, want string) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	var action string
	if err := tx.QueryRow(db.ctx, `SELECT COALESCE(string_agg(action,',' ORDER BY relative_name),'') FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`,
		f.job, ownershipInventoryWorker, f.lease, candidates).Scan(&action); err != nil || action != want {
		t.Fatalf("candidate action=%q want=%q err=%v", action, want, err)
	}
	ownershipInventoryCommit(t, db, tx)
}

func ownershipInventoryManifest(t *testing.T, db testDatabases, f ownershipInventoryFixture, candidates, want string) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	var state *string
	if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, f.job, ownershipInventoryWorker, f.lease, candidates).Scan(&state); err != nil || state == nil || *state != want {
		t.Fatalf("manifest state=%v want=%q err=%v", state, want, err)
	}
	ownershipInventoryCommit(t, db, tx)
}

func ownershipInventoryMarker(t *testing.T, db testDatabases, f ownershipInventoryFixture, want *int64) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	var epoch *int64
	var checksum []byte
	if err := tx.QueryRow(db.ctx, `SELECT ownership_blocked_epoch,successful_checksum FROM app.source_objects WHERE source_id=$1 AND relative_name=$2`, f.source, ownershipInventoryName).Scan(&epoch, &checksum); err != nil {
		t.Fatal(err)
	}
	if (epoch == nil) != (want == nil) || (epoch != nil && want != nil && *epoch != *want) || !bytes.Equal(checksum, bytes.Repeat([]byte{7}, 32)) {
		t.Fatalf("inventory epoch=%v want=%v or successful bytes changed", epoch, want)
	}
	_ = tx.Rollback(db.ctx)
}

func ownershipInventoryEpoch(t *testing.T, db testDatabases, f ownershipInventoryFixture, want int64) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	var epoch int64
	if err := tx.QueryRow(db.ctx, `SELECT workout_ownership_epoch FROM app.accounts WHERE id=$1`, f.account).Scan(&epoch); err != nil || epoch != want {
		t.Fatalf("account epoch=%d want=%d err=%v", epoch, want, err)
	}
	_ = tx.Rollback(db.ctx)
}

func ownershipInventoryFinish(t *testing.T, db testDatabases, f ownershipInventoryFixture) {
	t.Helper()
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.finish_job($1,$2,$3,'succeeded')`, f.job, ownershipInventoryWorker, f.lease) {
		t.Fatal("inventory job completion failed")
	}
}

func ownershipInventoryExec(t *testing.T, db testDatabases, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(db.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func ownershipInventoryCommit(t *testing.T, db testDatabases, tx pgx.Tx) {
	t.Helper()
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func int64ptr(value int64) *int64 { return &value }
