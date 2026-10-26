package migrations_test

import (
	"bytes"
	"encoding/json"
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
	"github.com/jackc/pgx/v5/pgxpool"
)

const reprocessingWorker = "ingest-reprocessing-test"

type reprocessingFixture struct {
	account, source, parent, job, lease uuid.UUID
}

// This suite never applies migrations or provisions a database. The caller must
// supply an isolated, already migrated local PostGIS database and actual roles.
func TestIngestReprocessingPostgreSQL(t *testing.T) {
	if os.Getenv("INGEST_REPROCESSING_TEST_DISPOSABLE") != "1" {
		t.Skip("INGEST_REPROCESSING_TEST_DISPOSABLE=1 and disposable role database URLs are required")
	}
	var databaseName string
	var port uint16
	for _, name := range []string{"API_DATABASE_URL", "WORKER_DATABASE_URL", "MIGRATION_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL", "TILE_DATABASE_URL"} {
		value := os.Getenv(name)
		if value == "" && (name == "COVERAGE_WORKER_DATABASE_URL" || name == "TILE_DATABASE_URL") {
			continue
		}
		config, err := pgx.ParseConfig(value)
		if value == "" || err != nil || !strings.HasPrefix(config.Database, "workouts_") || !strings.Contains(config.Database, "_test_") ||
			(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
			t.Fatalf("%s must name a disposable loopback workouts_*_test_* database", name)
		}
		if databaseName == "" {
			databaseName, port = config.Database, config.Port
		}
		if config.Database != databaseName || config.Port != port {
			t.Fatalf("%s must target the same disposable database and port", name)
		}
		for _, fallback := range config.Fallbacks {
			if fallback.Host != config.Host || fallback.Port != port {
				t.Fatalf("%s must not include remote/fallback endpoints", name)
			}
		}
	}
	db := openTestDatabases(t)
	assertRuntimeRole(t, db.ctx, db.api, "workouts_api")
	assertRuntimeRole(t, db.ctx, db.worker, "workouts_worker")
	assertRuntimeRole(t, db.ctx, db.migration, "workouts_migration")
	if os.Getenv("COVERAGE_WORKER_DATABASE_URL") != "" {
		assertRuntimeRole(t, db.ctx, db.coverage, "workouts_coverage_worker")
	}
	if os.Getenv("TILE_DATABASE_URL") != "" {
		assertRuntimeRole(t, db.ctx, db.tiles, "workouts_tiles")
	}
	manifest := reprocessingManifest(3)

	t.Run("MetadataSecurityAndUnchangedFence", func(t *testing.T) {
		var safe bool
		if err := db.migration.QueryRow(db.ctx, `SELECT
			(SELECT schema_version=33 AND minimum_runtime_version=33 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton)
			AND (SELECT bool_and(p.prosecdef AND pg_get_userbyid(p.proowner)='workouts_security_owner'
			 AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			 AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			 AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')=expected.worker
			 AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee<>p.proowner
			     AND (NOT expected.worker OR acl.grantee<>'workouts_worker'::regrole) OR acl.is_grantable))
			 FROM (VALUES ('app.ingest_retry_ancestor_jobs(uuid,uuid,uuid)',false),
			 ('app.stamp_ingest_source_file_receipt()',false),
			 ('app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)',true)) expected(signature,worker)
			 JOIN pg_proc p ON p.oid=expected.signature::regprocedure)
			AND NOT has_schema_privilege('workouts_security_owner','app','CREATE')
			AND NOT has_column_privilege('workouts_api','app.source_files','observed_identity','SELECT')
			AND NOT has_column_privilege('workouts_worker','app.source_files','observed_identity','SELECT')
			AND NOT has_column_privilege('workouts_api','app.source_files','ownership_blocked_epoch','SELECT')
			AND NOT has_column_privilege('workouts_worker','app.source_files','ownership_blocked_epoch','SELECT')
			AND NOT has_table_privilege('workouts_api','app.source_files','SELECT')
			AND NOT has_table_privilege('workouts_worker','app.source_files','SELECT')
			AND has_table_privilege('workouts_security_owner','app.source_files','SELECT')
			AND NOT EXISTS(SELECT 1 FROM unnest(ARRAY['workouts_api','workouts_worker']) role_name
			 CROSS JOIN unnest(string_to_array($1,',')) column_name
			 WHERE NOT has_column_privilege(role_name,'app.source_files',column_name,'SELECT'))
			AND has_table_privilege('workouts_worker','app.source_files','INSERT')
			AND has_table_privilege('workouts_worker','app.source_files','UPDATE')
			AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='app.source_files'::regclass
			 AND tgname='source_files_receipt_before_write' AND tgenabled='O' AND tgtype=23
			 AND tgfoid='app.stamp_ingest_source_file_receipt()'::regprocedure)
			AND (SELECT position('FOR UPDATE NOWAIT' IN substring(prosrc FROM 'PERFORM 1 FROM app.account_data_generations[^;]+'))=0
			 FROM pg_proc WHERE oid='app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)
			AND (SELECT position('observed_identity' IN pg_get_function_result(oid))=0
			 AND position('ownership_blocked_epoch' IN pg_get_function_result(oid))=0
			 FROM pg_proc WHERE oid='app.read_owned_job_files(uuid,integer,integer)'::regprocedure)`, reprocessingSafeFileColumns).Scan(&safe); err != nil || !safe {
			t.Fatalf("reprocessing metadata/security/fence contract: safe=%t err=%v", safe, err)
		}
	})

	t.Run("SourceFileOldColumnsAndAPIWrappersRemainReadable", func(t *testing.T) {
		root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		one := reprocessingManifest(1)
		reprocessingFreeze(t, db, root, one, "created")
		file := reprocessingFile(t, db, root, one, 0, "succeeded", uuid.Nil, false)
		for _, pool := range []*pgxpool.Pool{db.api, db.worker} {
			tx := beginAccount(t, db.ctx, pool, root.account)
			var data map[string]any
			err := tx.QueryRow(db.ctx, `SELECT to_jsonb(safe) FROM (SELECT `+reprocessingSafeFileColumns+`
			 FROM app.source_files WHERE id=$1) safe`, file).Scan(&data)
			_ = tx.Rollback(db.ctx)
			if err != nil || len(data) != 15 || data["id"] != file.String() || data["job_id"] != root.job.String() || data["state"] != "succeeded" {
				t.Fatalf("old fifteen-column source-file read changed: data=%v err=%v", data, err)
			}
			for _, projection := range []string{"observed_identity", "ownership_blocked_epoch", "*", "to_jsonb(file)"} {
				tx := beginAccount(t, db.ctx, pool, root.account)
				_, err := tx.Exec(db.ctx, `SELECT `+projection+` FROM app.source_files file WHERE id=$1`, file)
				_ = tx.Rollback(db.ctx)
				reprocessingError(t, err, "42501")
			}
		}
		for _, arguments := range []string{"$1,100,0", "$1,100,0,false"} {
			tx := beginAccount(t, db.ctx, db.api, root.account)
			var data map[string]any
			err := tx.QueryRow(db.ctx, `SELECT to_jsonb(file) FROM app.read_owned_job_files(`+arguments+`) file WHERE id=$2`, root.job, file).Scan(&data)
			_ = tx.Rollback(db.ctx)
			_, identityExposed := data["observed_identity"]
			_, epochExposed := data["ownership_blocked_epoch"]
			if err != nil || data["id"] != file.String() || data["state"] != "succeeded" || identityExposed || epochExposed {
				t.Fatalf("API file wrapper changed or exposed receipt columns: data=%v err=%v", data, err)
			}
		}
		reprocessingFinish(t, db, root)
	})

	t.Run("ParameterWhitelistAndNullTypes", func(t *testing.T) {
		tx := beginAccount(t, db.ctx, db.migration, uuid.New())
		defer tx.Rollback(db.ctx)
		base := `{"sourceId":"0123456789ABCDEF0123456789ABCDEF","generation":1,"mode":"bounded","startDate":"2026-01-01","endDate":"2026-01-03"}`
		for _, tc := range []struct {
			value string
			want  bool
		}{
			{base, true}, {strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":false}`, true},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":true}`, true},
			{`{"sourceId":"0123456789ABCDEF0123456789ABCDEF","generation":1,"mode":"incremental"}`, true},
			{`{"sourceId":"0123456789ABCDEF0123456789ABCDEF","generation":1,"mode":"bounded","startDate":"0001-01-01","endDate":"9999-12-31","legacySchema6":true}`, true},
			{`null`, false}, {`{}`, false}, {`[]`, false},
			{strings.TrimSuffix(base, "}") + `,"unknown":false}`, false},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":null}`, false},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":0}`, false},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":"false"}`, false},
			{strings.TrimSuffix(base, "}") + `,"legacySchema6":true}`, false},
			{strings.Replace(base, `"generation":1`, `"generation":null`, 1), false},
			{strings.Replace(base, `"mode":"bounded"`, `"mode":null`, 1), false},
			{strings.Replace(base, `"sourceId":"0123456789ABCDEF0123456789ABCDEF"`, `"sourceId":null`, 1), false},
			{strings.Replace(base, `"startDate":"2026-01-01"`, `"startDate":null`, 1), false},
			{strings.Replace(base, `2026-01-01`, `2026-02-30`, 1), false},
			{strings.Replace(base, `2026-01-03`, `2025-12-31`, 1), false},
		} {
			var got *bool
			if err := tx.QueryRow(db.ctx, `SELECT app.valid_ingest_child_parameters($1)`, tc.value).Scan(&got); err != nil || got == nil || *got != tc.want {
				t.Fatalf("parameters=%s valid=%v want=%t err=%v", tc.value, got, tc.want, err)
			}
		}
		for _, mode := range []string{"incremental", "bounded"} {
			for _, extra := range []string{`"reprocessAllFiles":false`, `"reprocessAllFiles":true`, `"reprocessAllFiles":null`} {
				value := `{"sourceId":"0123456789ABCDEF0123456789ABCDEF","generation":1,"mode":"` + mode + `",` + extra + `}`
				if mode == "bounded" {
					value = strings.TrimSuffix(value, "}") + `,"startDate":"0001-01-01","endDate":"9999-12-31","legacySchema6":true}`
				}
				var got bool
				if err := tx.QueryRow(db.ctx, `SELECT app.valid_ingest_child_parameters($1)`, value).Scan(&got); err != nil || got {
					t.Fatalf("incremental/legacy presence accepted: %s: %v", value, err)
				}
			}
		}
		_ = tx.Rollback(db.ctx)
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		reprocessingFinish(t, db, f)
		for _, tc := range []struct {
			parameters string
			valid      bool
		}{
			{base, true},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":false}`, true},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":true}`, true},
			{strings.TrimSuffix(base, "}") + `,"reprocessAllFiles":null}`, false},
			{`{"sourceId":"0123456789ABCDEF0123456789ABCDEF","generation":1,"mode":"incremental","reprocessAllFiles":false}`, false},
		} {
			api := beginAccount(t, db.ctx, db.api, f.account)
			_, err := api.Exec(db.ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
			 VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, uuid.New(), f.parent, f.account, tc.parameters)
			_ = api.Rollback(db.ctx)
			if tc.valid {
				if err != nil {
					t.Fatalf("API INSERT cannot invoke parameter validator: %v", err)
				}
			} else {
				reprocessingError(t, err, "22023")
			}
		}
	})

	t.Run("FreshBoundedPoliciesAndOrdinaryRetryGlobalReuse", func(t *testing.T) {
		old := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		reprocessingFreeze(t, db, old, manifest, "created")
		for index := range 3 {
			reprocessingFile(t, db, old, manifest, index, "succeeded", uuid.Nil, false)
		}
		reprocessingFinish(t, db, old)
		for _, policy := range []string{"false", "true", "legacy", "incremental"} {
			root := reprocessingJob(t, db, nil, old.account, old.source, policy)
			want := "skip,skip,skip"
			if policy == "true" || policy == "legacy" {
				want = "process,process,process"
			}
			reprocessingActions(t, db, root, manifest, want)
			if policy == "false" {
				reprocessingFreeze(t, db, root, strings.ReplaceAll(manifest, `"action":"process"`, `"action":"skip"`), "created")
				reprocessingFinish(t, db, root)
				retry := reprocessingJob(t, db, &root, root.account, root.source, policy)
				reprocessingActions(t, db, retry, manifest, "skip,skip,skip")
				reprocessingFinish(t, db, retry)
			}
			reprocessingFinish(t, db, root)
		}
	})

	t.Run("ForcedRetryReceiptsIntermediatesBranchesAndOverwrite", func(t *testing.T) {
		old := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		reprocessingFreeze(t, db, old, manifest, "created")
		for index := range 3 {
			reprocessingFile(t, db, old, manifest, index, "succeeded", uuid.Nil, false)
		}
		reprocessingFinish(t, db, old)
		root := reprocessingJob(t, db, nil, old.account, old.source, "true")
		reprocessingActions(t, db, root, manifest, "process,process,process")
		reprocessingFreeze(t, db, root, manifest, "created")
		a := reprocessingFile(t, db, root, manifest, 0, "succeeded", uuid.Nil, false)
		reprocessingFile(t, db, root, manifest, 1, "failed", uuid.Nil, false)
		reprocessingFinish(t, db, root)
		retry := reprocessingJob(t, db, &root, root.account, root.source, "true")
		reprocessingActions(t, db, retry, manifest, "skip,process,process")
		intermediate := strings.Replace(manifest, `"action":"process"`, `"action":"skip"`, 1)
		reprocessingFreeze(t, db, retry, intermediate, "created")
		reprocessingActions(t, db, retry, manifest, "skip,process,process")
		reprocessingFile(t, db, retry, manifest, 1, "succeeded", uuid.Nil, false)
		reprocessingFinish(t, db, retry)
		branch := reprocessingJob(t, db, &root, root.account, root.source, "true")
		reprocessingActions(t, db, branch, manifest, "skip,process,process")
		reprocessingFinish(t, db, branch)
		// A config refresh can change generations without changing object identity.
		tx := beginAccount(t, db.ctx, db.migration, root.account)
		reprocessingExec(t, db, tx, `UPDATE app.sources SET generation=generation+1 WHERE id=$1`, root.source)
		reprocessingCommit(t, db, tx)
		next := reprocessingJob(t, db, &retry, root.account, root.source, "true")
		reprocessingActions(t, db, next, manifest, "skip,skip,process")
		changed := strings.Replace(manifest, `"inode":3`, `"inode":30`, 1)
		reprocessingActions(t, db, next, changed, "process,skip,process")
		// Later unrelated jobs may overwrite inventory, but cannot erase receipts.
		later := reprocessingJob(t, db, nil, root.account, root.source, "true")
		reprocessingFreeze(t, db, later, changed, "created")
		reprocessingFile(t, db, later, changed, 0, "succeeded", uuid.Nil, false)
		reprocessingFinish(t, db, later)
		reprocessingActions(t, db, next, manifest, "skip,skip,process")
		tx = beginAccount(t, db.ctx, db.migration, root.account)
		var receipt bool
		if err := tx.QueryRow(db.ctx, `SELECT observed_identity='v1:10:100:1:2:3:101:4' AND ownership_blocked_epoch IS NULL
			FROM app.source_files WHERE id=$1`, a).Scan(&receipt); err != nil || !receipt {
			t.Fatalf("durable full-precision clean receipt: %t %v", receipt, err)
		}
		_ = tx.Rollback(db.ctx)
		reprocessingFinish(t, db, next)
	})

	t.Run("TerminalCleanupPreservesRetryLineageButCurrentSnapshotIsRequired", func(t *testing.T) {
		one := reprocessingManifest(1)
		root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		reprocessingFreeze(t, db, root, one, "created")
		reprocessingFile(t, db, root, one, 0, "succeeded", uuid.Nil, false)
		tx := beginAccount(t, db.ctx, db.migration, root.account)
		var live bool
		if err := tx.QueryRow(db.ctx, `SELECT child.kind='manual_ingest_source' AND child.status='running'
		 AND parent.kind='manual_ingest' AND parent.status='running'
		 AND EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=child.id)
		 AND EXISTS(SELECT 1 FROM app.job_source_contexts WHERE job_id=child.id)
		 FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id WHERE child.id=$1`, root.job).Scan(&live); err != nil || !live {
			t.Fatalf("live predecessor fixture lacks active snapshot/context: %t %v", live, err)
		}
		_ = tx.Rollback(db.ctx)
		// Exercise the actual worker finish path, not synthetic snapshot deletion.
		reprocessingFinish(t, db, root)
		tx = beginAccount(t, db.ctx, db.migration, root.account)
		var retained bool
		if err := tx.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=$1)
		 AND EXISTS(SELECT 1 FROM app.jobs WHERE id=$1 AND status='failed')
		 AND EXISTS(SELECT 1 FROM app.job_source_contexts WHERE job_id=$1)
		 AND EXISTS(SELECT 1 FROM app.job_file_candidate_sets WHERE job_id=$1)
		 AND EXISTS(SELECT 1 FROM app.job_file_candidates WHERE job_id=$1)
		 AND EXISTS(SELECT 1 FROM app.source_files WHERE job_id=$1 AND state='succeeded' AND observed_identity IS NOT NULL)`, root.job).Scan(&retained); err != nil || !retained {
			t.Fatalf("terminal cleanup lost durable evidence or retained credentials: %t %v", retained, err)
		}
		_ = tx.Rollback(db.ctx)
		retry := reprocessingJob(t, db, &root, root.account, root.source, "true")
		reprocessingActions(t, db, retry, one, "skip")
		tx = beginAccount(t, db.ctx, db.migration, root.account)
		reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
		var lineage bool
		if err := tx.QueryRow(db.ctx, `SELECT app.ingest_retry_ancestor_jobs($1,$2,$3)=ARRAY[$4::uuid]`, retry.job, retry.account, retry.source, root.job).Scan(&lineage); err != nil || !lineage {
			t.Fatalf("cleaned predecessor not returned by lineage helper: %t %v", lineage, err)
		}
		_ = tx.Rollback(db.ctx)
		// Missing credentials are valid only for ancestors, never the active node.
		tx = beginAccount(t, db.ctx, db.migration, retry.account)
		reprocessingExec(t, db, tx, `DELETE FROM app.job_config_snapshots WHERE job_id=$1`, retry.job)
		reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
		var count int
		if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.fence_ingest_job($1,$2,$3,$4)`, retry.job, reprocessingWorker, retry.lease, database.SupportedSchemaVersion).Scan(&count); err != nil || count != 0 {
			t.Fatalf("active fence accepted missing snapshot: count=%d err=%v", count, err)
		}
		_, err := tx.Exec(db.ctx, `SELECT app.ingest_retry_ancestor_jobs($1,$2,$3)`, retry.job, retry.account, retry.source)
		_ = tx.Rollback(db.ctx)
		reprocessingError(t, err, "22023")
		reprocessingActions(t, db, retry, one, "skip")
		reprocessingFinish(t, db, retry)
	})

	t.Run("ReceiptsAndExactLegacyInventorySurviveMissingAncestorManifests", func(t *testing.T) {
		for _, historical := range []bool{false, true} {
			t.Run(fmt.Sprintf("historical=%t", historical), func(t *testing.T) {
				one := reprocessingManifest(1)
				root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
				reprocessingFreeze(t, db, root, one, "created")
				reprocessingFile(t, db, root, one, 0, "succeeded", uuid.Nil, historical)
				reprocessingFinish(t, db, root)
				retry := reprocessingJob(t, db, &root, root.account, root.source, "true")
				tx := beginAccount(t, db.ctx, db.migration, root.account)
				defer tx.Rollback(db.ctx)
				// Normal finish retains manifests. Emulate future private-data
				// retention only in a rolled-back transaction over these fixtures.
				reprocessingExec(t, db, tx, `ALTER TABLE app.job_file_candidates DISABLE TRIGGER job_file_candidates_immutable`)
				reprocessingExec(t, db, tx, `ALTER TABLE app.job_file_candidate_sets DISABLE TRIGGER job_file_candidate_sets_immutable`)
				reprocessingExec(t, db, tx, `DELETE FROM app.job_file_candidates WHERE job_id=$1`, root.job)
				reprocessingExec(t, db, tx, `DELETE FROM app.job_file_candidate_sets WHERE job_id=$1`, root.job)
				if !historical {
					// A new receipt needs neither a manifest nor current inventory.
					reprocessingExec(t, db, tx, `UPDATE app.source_objects SET successful_checksum=NULL,
					 successful_observed_at=NULL,successful_job_id=NULL WHERE source_id=$1`, root.source)
				}
				reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
				reprocessingFenceTx(t, db, tx, retry)
				var action string
				if err := tx.QueryRow(db.ctx, `SELECT action FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, retry.job, reprocessingWorker, retry.lease, one).Scan(&action); err != nil || action != "skip" {
					t.Fatalf("manifest removal erased successful proof: action=%s err=%v", action, err)
				}
				_ = tx.Rollback(db.ctx)
				reprocessingFinish(t, db, retry)
			})
		}
	})

	t.Run("FrozenLegacyRecoveryAndOwnTerminalCheckpoint", func(t *testing.T) {
		root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "legacy")
		reprocessingActions(t, db, root, manifest, "process,process,process")
		frozen := strings.Replace(manifest, `"action":"process"`, `"action":"skip"`, 1)
		reprocessingFreeze(t, db, root, frozen, "created")
		reprocessingFile(t, db, root, manifest, 1, "succeeded", uuid.Nil, false)
		reprocessingActions(t, db, root, manifest, "skip,process,process")
		changed := strings.Replace(frozen, `"inode":3`, `"inode":30`, 1)
		reprocessingActions(t, db, root, changed, "skip,process,process")
		reprocessingFreeze(t, db, root, frozen, "matched")
		reprocessingFreeze(t, db, root, changed, "mismatch")
		reprocessingFreeze(t, db, root, `[]`, "mismatch")
		// Corrupt ancestry is ignored only for an already frozen manifest.
		tx := beginAccount(t, db.ctx, db.migration, root.account)
		defer tx.Rollback(db.ctx)
		reprocessingExec(t, db, tx, `ALTER TABLE app.jobs DISABLE TRIGGER jobs_state_before_write`)
		reprocessingExec(t, db, tx, `UPDATE app.jobs SET retry_of_job_id=id WHERE id=$1`, root.job)
		reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
		reprocessingFenceTx(t, db, tx, root)
		var got string
		if err := tx.QueryRow(db.ctx, `SELECT string_agg(action,',' ORDER BY relative_name) FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`,
			root.job, reprocessingWorker, root.lease, manifest).Scan(&got); err != nil || got != "skip,process,process" {
			t.Fatalf("frozen recovery consulted lineage: %q %v", got, err)
		}
	})

	t.Run("OwnerEpochReceiptsAndCleanSuccess", func(t *testing.T) {
		owner := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		workout := reprocessingWorkout(t, db, owner)
		reprocessingFinish(t, db, owner)
		root := reprocessingJob(t, db, nil, owner.account, uuid.Nil, "true")
		reprocessingFreeze(t, db, root, manifest, "created")
		blocked := reprocessingFile(t, db, root, manifest, 0, "succeeded", workout, false)
		reprocessingFile(t, db, root, manifest, 1, "succeeded", uuid.Nil, false)
		reprocessingFinish(t, db, root)
		retry := reprocessingJob(t, db, &root, root.account, root.source, "true")
		reprocessingActions(t, db, retry, manifest, "skip,skip,process")
		if !callBool(t, db.ctx, db.api, owner.account, `SELECT app.delete_source($1,$2)`, owner.source, uuid.New()) {
			t.Fatal("synthetic owner deletion failed")
		}
		reprocessingActions(t, db, retry, manifest, "process,skip,process")
		tx := beginAccount(t, db.ctx, db.migration, root.account)
		var stamped bool
		if err := tx.QueryRow(db.ctx, `SELECT ownership_blocked_epoch=0 AND observed_identity IS NOT NULL FROM app.source_files WHERE id=$1`, blocked).Scan(&stamped); err != nil || !stamped {
			t.Fatalf("same-transaction blocked receipt: %t %v", stamped, err)
		}
		_ = tx.Rollback(db.ctx)
		reprocessingFinish(t, db, retry)
	})

	t.Run("LegacyAncestorRequiresCurrentExactInventoryProof", func(t *testing.T) {
		for _, policy := range []string{"legacy", "legacy6"} {
			candidates := manifest
			if policy == "legacy6" {
				candidates = strings.ReplaceAll(manifest, "2026-", "2024-")
			}
			root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, policy)
			reprocessingFreeze(t, db, root, candidates, "created")
			file := reprocessingFile(t, db, root, candidates, 0, "succeeded", uuid.Nil, true)
			reprocessingFinish(t, db, root)
			retryPolicy := "true"
			if policy == "legacy6" {
				retryPolicy = "legacy6"
			}
			retry := reprocessingJob(t, db, &root, root.account, root.source, retryPolicy)
			reprocessingActions(t, db, retry, candidates, "skip,process,process")
			for _, assignment := range []string{
				`successful_checksum=decode(repeat('ab',32),'hex')`,
				`observed_identity='v1:changed'`, `ownership_blocked_epoch=1`,
				`successful_checksum=NULL,successful_observed_at=NULL,successful_job_id=NULL`,
			} {
				tx := beginAccount(t, db.ctx, db.migration, root.account)
				reprocessingExec(t, db, tx, `UPDATE app.source_objects SET `+assignment+` WHERE source_id=$1`, root.source)
				reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
				reprocessingFenceTx(t, db, tx, retry)
				var actions string
				err := tx.QueryRow(db.ctx, `SELECT string_agg(action,',' ORDER BY relative_name) FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`,
					retry.job, reprocessingWorker, retry.lease, candidates).Scan(&actions)
				_ = tx.Rollback(db.ctx)
				if err != nil || actions != "process,process,process" {
					t.Fatalf("legacy proof accepted %s: %s %v", assignment, actions, err)
				}
			}
			tx := beginAccount(t, db.ctx, db.migration, root.account)
			var historical bool
			if err := tx.QueryRow(db.ctx, `SELECT observed_identity IS NULL AND ownership_blocked_epoch IS NULL FROM app.source_files WHERE id=$1`, file).Scan(&historical); err != nil || !historical {
				t.Fatalf("historical NULL receipt changed: %t %v", historical, err)
			}
			_ = tx.Rollback(db.ctx)
			other := reprocessingJob(t, db, nil, root.account, root.source, "true")
			reprocessingFreeze(t, db, other, candidates, "created")
			reprocessingFile(t, db, other, candidates, 0, "succeeded", uuid.Nil, false)
			reprocessingFinish(t, db, other)
			reprocessingActions(t, db, retry, candidates, "process,process,process")
			reprocessingFinish(t, db, retry)
		}
	})

	t.Run("V2IdentityAndNanosecondIdentity", func(t *testing.T) {
		remote := strings.Replace(reprocessingManifest(1), `"action":"process"`, `"action":"process","observed_identity":"v2:rclone:`+strings.Repeat("a", 64)+`"`, 1)
		for _, candidate := range []string{remote, strings.Replace(reprocessingManifest(1), `"modified_ns":1`, `"modified_ns":123456789`, 1)} {
			root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
			reprocessingFreeze(t, db, root, candidate, "created")
			reprocessingFile(t, db, root, candidate, 0, "succeeded", uuid.Nil, false)
			reprocessingFinish(t, db, root)
			retry := reprocessingJob(t, db, &root, root.account, root.source, "true")
			reprocessingActions(t, db, retry, candidate, "skip")
			reprocessingActions(t, db, retry, strings.Replace(candidate, `"size":10`, `"size":11`, 1), "process")
			reprocessingFinish(t, db, retry)
		}
	})

	t.Run("CallerSpoofNonterminalNullAndTerminalImmutability", func(t *testing.T) {
		root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		frozen := strings.Replace(manifest, `"ctime_ns":4,"action":"process"}]`, `"ctime_ns":4,"action":"skip"}]`, 1)
		reprocessingFreeze(t, db, root, frozen, "created")
		file := reprocessingFile(t, db, root, manifest, 0, "succeeded", uuid.Nil, false)
		for _, query := range []string{
			`UPDATE app.source_files SET state='processing',processed_at=NULL WHERE id=$1`,
			`UPDATE app.source_files SET observed_identity=NULL WHERE id=$1`,
			`UPDATE app.source_files SET ownership_blocked_epoch=42 WHERE id=$1`,
		} {
			tx := reprocessingFence(t, db, root)
			_, err := tx.Exec(db.ctx, query, file)
			_ = tx.Rollback(db.ctx)
			reprocessingError(t, err, "23514")
		}
		for _, column := range []string{"observed_identity", "ownership_blocked_epoch"} {
			tx := reprocessingFence(t, db, root)
			id := uuid.New()
			reprocessingExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
			 VALUES($1,$2,$3,$4,'spoof',10)`, id, root.account, root.source, root.job)
			value := `'forged'`
			if column == "ownership_blocked_epoch" {
				value = "0"
			}
			_, err := tx.Exec(db.ctx, `UPDATE app.source_files SET `+column+`=`+value+` WHERE id=$1`, id)
			_ = tx.Rollback(db.ctx)
			reprocessingError(t, err, "42501")
			tx = reprocessingFence(t, db, root)
			_, err = tx.Exec(db.ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,`+column+`)
			 VALUES($1,$2,$3,$4,'spoof',10,`+value+`)`, uuid.New(), root.account, root.source, root.job)
			_ = tx.Rollback(db.ctx)
			reprocessingError(t, err, "42501")
		}
		for _, mutation := range []string{"size", "modified", "skip", "absent"} {
			tx := reprocessingFence(t, db, root)
			name, size, modified := "HealthAutoExport-2026-01-02.json", int64(10), time.Unix(100, 1)
			switch mutation {
			case "size":
				size = 11
			case "modified":
				modified = time.Unix(101, 0)
			case "skip":
				name = "HealthAutoExport-2026-01-03.json"
			case "absent":
				name = "HealthAutoExport-2026-01-04.json"
			}
			id := uuid.New()
			reprocessingExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,state,processing_started_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,'processing',transaction_timestamp())`, id, root.account, root.source, root.job, name, size, modified)
			_, err := tx.Exec(db.ctx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=transaction_timestamp() WHERE id=$1`, id, bytes.Repeat([]byte{7}, 32))
			_ = tx.Rollback(db.ctx)
			reprocessingError(t, err, "22023")
		}
		reprocessingFinish(t, db, root)
	})

	t.Run("MalformedCandidatesAndPrivateReceiptAccess", func(t *testing.T) {
		root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		one := reprocessingManifest(1)
		for _, malformed := range []string{
			`null`, `{}`, `[null]`, `[{}]`,
			strings.Replace(one, `"size":10`, `"size":null`, 1),
			strings.Replace(one, `"modified_ns":1`, `"modified_ns":1000000000`, 1),
			strings.Replace(one, `"device":2`, `"device":18446744073709551616`, 1),
			strings.Replace(one, `"action":"process"`, `"action":"process","observed_identity":null`, 1),
			strings.Replace(one, `"action":"process"`, `"action":"process","observed_identity":"v1:forged"`, 1),
			strings.Replace(one, `"action":"process"`, `"action":"process","private":"forged"`, 1),
			"[" + strings.Trim(one, "[]") + "," + strings.Trim(one, "[]") + "]",
		} {
			tx := reprocessingFence(t, db, root)
			_, err := tx.Exec(db.ctx, `SELECT * FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, root.job, reprocessingWorker, root.lease, malformed)
			_ = tx.Rollback(db.ctx)
			reprocessingError(t, err, "22023")
		}
		tx := reprocessingFence(t, db, root)
		var count int
		if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.read_ingest_candidate_actions($1,$2,$3,'[]')`, root.job, reprocessingWorker, root.lease).Scan(&count); err != nil || count != 0 {
			t.Fatalf("empty candidates=%d err=%v", count, err)
		}
		reprocessingCommit(t, db, tx)
		for _, pool := range []*pgxpool.Pool{db.api, db.worker} {
			for _, query := range []string{
				`SELECT observed_identity FROM app.source_files`,
				`SELECT ownership_blocked_epoch FROM app.source_files`,
				`SELECT app.ingest_retry_ancestor_jobs(NULL,NULL,NULL)`,
				`SELECT app.stamp_ingest_source_file_receipt()`,
			} {
				tx := beginAccount(t, db.ctx, pool, root.account)
				_, err := tx.Exec(db.ctx, query)
				_ = tx.Rollback(db.ctx)
				reprocessingError(t, err, "42501")
			}
		}
		reprocessingFinish(t, db, root)
	})

	t.Run("InvalidLineageFailsClosedAndDepthBound", func(t *testing.T) {
		root := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		reprocessingFinish(t, db, root)
		retry := reprocessingJob(t, db, &root, root.account, root.source, "true")
		foreign := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "true")
		otherSource := reprocessingJob(t, db, nil, root.account, uuid.Nil, "true")
		reprocessingFinish(t, db, foreign)
		reprocessingFinish(t, db, otherSource)
		for _, tc := range []struct {
			name, query string
			args        []any
		}{
			{"account", `UPDATE app.jobs SET retry_of_job_id=$2 WHERE id=$1`, []any{retry.job, foreign.job}},
			{"source", `UPDATE app.jobs SET retry_of_job_id=$2 WHERE id=$1`, []any{retry.job, otherSource.job}},
			{"parent-edge", `UPDATE app.jobs SET retry_of_job_id=NULL WHERE id=$1`, []any{retry.parent}},
			{"parent-kind", `UPDATE app.jobs SET kind='scheduled_ingest' WHERE id=$1`, []any{root.parent}},
			{"child-kind", `UPDATE app.jobs SET kind='scheduled_ingest_source' WHERE id=$1`, []any{root.job}},
			{"root-parent-retry", `UPDATE app.jobs SET retry_of_job_id=$2 WHERE id=$1`, []any{root.parent, otherSource.parent}},
			{"date-policy", `UPDATE app.jobs SET parameters=jsonb_set(parameters,'{endDate}','"2026-01-04"') WHERE id=$1`, []any{root.job}},
			{"force-policy", `UPDATE app.jobs SET parameters=jsonb_set(parameters,'{reprocessAllFiles}','false') WHERE id=$1`, []any{root.job}},
			{"cycle", `UPDATE app.jobs SET retry_of_job_id=$2 WHERE id=$1`, []any{root.job, retry.job}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// Deliberately hostile persisted fixtures, with only the immutable
				// job guard suspended inside a transaction that always rolls back.
				tx := beginAccount(t, db.ctx, db.migration, root.account)
				defer tx.Rollback(db.ctx)
				reprocessingExec(t, db, tx, `ALTER TABLE app.jobs DISABLE TRIGGER jobs_state_before_write`)
				_, err := tx.Exec(db.ctx, tc.query, tc.args...)
				if tc.name == "child-kind" {
					// The native hierarchy guard rejects this before lineage lookup.
					reprocessingError(t, err, "23514")
					var postgres *pgconn.PgError
					if !errors.As(err, &postgres) || postgres.Message != "invalid job hierarchy" || postgres.Detail != "" {
						t.Fatalf("hierarchy error leaks details: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("synthetic lineage mutation failed: %v", err)
				}
				reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
				reprocessingFenceTx(t, db, tx, retry)
				_, err = tx.Exec(db.ctx, `SELECT * FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, retry.job, reprocessingWorker, retry.lease, manifest)
				reprocessingError(t, err, "22023")
				var postgres *pgconn.PgError
				if !errors.As(err, &postgres) || postgres.Message != "invalid ingest retry lineage" || postgres.Detail != "" {
					t.Fatalf("lineage error leaks details: %v", err)
				}
			})
		}
		reprocessingFinish(t, db, retry)
		// Build the depth fixture in one transaction to keep this test bounded.
		tx := beginAccount(t, db.ctx, db.migration, root.account)
		defer tx.Rollback(db.ctx)
		parent, child := root.parent, root.job
		for depth := 2; depth <= 101; depth++ {
			newParent, newChild := uuid.New(), uuid.New()
			reprocessingExec(t, db, tx, `INSERT INTO app.jobs(id,account_id,kind,priority,retry_of_job_id) VALUES($1,$2,'manual_ingest',80,$3)`, newParent, root.account, parent)
			reprocessingExec(t, db, tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,retry_of_job_id,parameters)
			 SELECT $1,account_id,$2,kind,priority,$3,parameters FROM app.jobs WHERE id=$3`, newChild, newParent, child)
			reprocessingExec(t, db, tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
			 SELECT $1,account_id,source_id,source_generation,$3 FROM app.job_source_contexts WHERE job_id=$2`, newChild, child, []byte(`{"synthetic":true}`))
			reprocessingExec(t, db, tx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			 SELECT $1,account_id,source_id,source_generation,display_name,source_type FROM app.job_source_contexts WHERE job_id=$2`, newChild, child)
			parent, child = newParent, newChild
			if depth == 100 {
				reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
				var count int
				if err := tx.QueryRow(db.ctx, `SELECT cardinality(app.ingest_retry_ancestor_jobs($1,$2,$3))`, child, root.account, root.source).Scan(&count); err != nil || count != 99 {
					t.Fatalf("exactly 100 nodes rejected: %d %v", count, err)
				}
				reprocessingExec(t, db, tx, `RESET ROLE`)
			}
		}
		reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
		_, err := tx.Exec(db.ctx, `SELECT app.ingest_retry_ancestor_jobs($1,$2,$3)`, child, root.account, root.source)
		reprocessingError(t, err, "22023")
	})
}

func reprocessingManifest(count int) string {
	var items []string
	for day := 1; day <= count; day++ {
		items = append(items, fmt.Sprintf(`{"name":"HealthAutoExport-2026-01-%02d.json","export_date":"2026-01-%02d","size":10,"modified_sec":100,"modified_ns":1,"device":2,"inode":3,"ctime_sec":101,"ctime_ns":4,"action":"process"}`, day, day))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func reprocessingJob(t *testing.T, db testDatabases, prior *reprocessingFixture, account, source uuid.UUID, policy string) reprocessingFixture {
	t.Helper()
	if account == uuid.Nil {
		account = uuid.New()
	}
	f := reprocessingFixture{account: account, source: source, parent: uuid.New(), job: uuid.New(), lease: uuid.New()}
	tx := beginAccount(t, db.ctx, db.migration, account)
	defer tx.Rollback(db.ctx)
	reprocessingExec(t, db, tx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT DO NOTHING`, account)
	if source == uuid.Nil {
		f.source = uuid.New()
		reprocessingExec(t, db, tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
		 VALUES($1,$2,$3,$3,'health-auto-export-local','connected',$4)`, f.source, account, "reprocessing-"+f.source.String(), []byte(`{"synthetic":true}`))
	}
	var generation int64
	if err := tx.QueryRow(db.ctx, `SELECT generation FROM app.sources WHERE id=$1`, f.source).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	parameters := map[string]any{"mode": "bounded", "startDate": "2026-01-01", "endDate": "2026-01-03"}
	switch policy {
	case "incremental":
		parameters = map[string]any{"mode": "incremental"}
	case "legacy6":
		parameters["startDate"], parameters["endDate"], parameters["legacySchema6"] = "0001-01-01", "9999-12-31", true
	case "legacy":
	default:
		parameters["reprocessAllFiles"] = policy == "true"
	}
	parentParameters, _ := json.Marshal(parameters)
	parameters["sourceId"], parameters["generation"] = strings.ToUpper(strings.ReplaceAll(f.source.String(), "-", "")), generation
	childParameters, _ := json.Marshal(parameters)
	var parentRetry, childRetry any
	if prior != nil {
		parentRetry, childRetry = prior.parent, prior.job
	}
	reprocessingExec(t, db, tx, `INSERT INTO app.jobs(id,account_id,kind,priority,parameters,retry_of_job_id)
	 VALUES($1,$2,'manual_ingest',80,$3,$4)`, f.parent, account, parentParameters, parentRetry)
	reprocessingExec(t, db, tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters,retry_of_job_id)
	 VALUES($1,$2,$3,'manual_ingest_source',80,$4,$5)`, f.job, f.parent, account, childParameters, childRetry)
	reprocessingExec(t, db, tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
	 VALUES($1,$2,$3,$4,$5)`, f.job, account, f.source, generation, []byte(`{"synthetic":true}`))
	reprocessingExec(t, db, tx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
	 VALUES($1,$2,$3,$4,'Synthetic reprocessing source','health-auto-export-local') ON CONFLICT DO NOTHING`, f.job, account, f.source, generation)
	reprocessingExec(t, db, tx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3) ON CONFLICT DO NOTHING`, f.parent, f.job, account)
	reprocessingCommit(t, db, tx)
	if !callBool(t, db.ctx, db.worker, account, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, f.job, reprocessingWorker, f.lease) {
		t.Fatal("could not claim synthetic reprocessing job")
	}
	return f
}

func reprocessingFenceTx(t *testing.T, db testDatabases, tx pgx.Tx, f reprocessingFixture) {
	t.Helper()
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,$4)`, f.job, reprocessingWorker, f.lease, database.SupportedSchemaVersion).Scan(&source); err != nil || source != f.source {
		t.Fatalf("runtime%d fence source=%s err=%v", database.SupportedSchemaVersion, source, err)
	}
}

func reprocessingFence(t *testing.T, db testDatabases, f reprocessingFixture) pgx.Tx {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	reprocessingFenceTx(t, db, tx, f)
	return tx
}

func reprocessingActions(t *testing.T, db testDatabases, f reprocessingFixture, manifest, want string) {
	t.Helper()
	tx := reprocessingFence(t, db, f)
	defer tx.Rollback(db.ctx)
	var got string
	if err := tx.QueryRow(db.ctx, `SELECT COALESCE(string_agg(action,',' ORDER BY relative_name),'') FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`,
		f.job, reprocessingWorker, f.lease, manifest).Scan(&got); err != nil || got != want {
		t.Fatalf("candidate actions=%q want=%q err=%v", got, want, err)
	}
	reprocessingCommit(t, db, tx)
}

func reprocessingFreeze(t *testing.T, db testDatabases, f reprocessingFixture, manifest, want string) {
	t.Helper()
	tx := reprocessingFence(t, db, f)
	defer tx.Rollback(db.ctx)
	var got string
	if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, f.job, reprocessingWorker, f.lease, manifest).Scan(&got); err != nil || got != want {
		t.Fatalf("frozen manifest=%q want=%q err=%v", got, want, err)
	}
	reprocessingCommit(t, db, tx)
}

func reprocessingFile(t *testing.T, db testDatabases, f reprocessingFixture, manifest string, index int, state string, blockedWorkout uuid.UUID, historical bool) uuid.UUID {
	t.Helper()
	var candidates []struct {
		Name        string `json:"name"`
		ExportDate  string `json:"export_date"`
		Size        int64  `json:"size"`
		ModifiedSec int64  `json:"modified_sec"`
		ModifiedNS  int64  `json:"modified_ns"`
		Device      uint64 `json:"device"`
		Inode       uint64 `json:"inode"`
		CTimeSec    int64  `json:"ctime_sec"`
		CTimeNS     int64  `json:"ctime_ns"`
		Observed    string `json:"observed_identity"`
	}
	if err := json.Unmarshal([]byte(manifest), &candidates); err != nil {
		t.Fatal(err)
	}
	candidate := candidates[index]
	pool := db.worker
	if historical {
		pool = db.migration
	}
	tx := beginAccount(t, db.ctx, pool, f.account)
	defer tx.Rollback(db.ctx)
	if historical {
		// Emulate a pre-031 success, without ever rewriting a terminal row.
		reprocessingExec(t, db, tx, `ALTER TABLE app.source_files DISABLE TRIGGER source_files_receipt_before_write`)
	}
	reprocessingFenceTx(t, db, tx, f)
	file := uuid.New()
	modified := time.Unix(candidate.ModifiedSec, candidate.ModifiedNS).UTC()
	reprocessingExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,state,processing_started_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,'processing',transaction_timestamp())`, file, f.account, f.source, f.job, candidate.Name, candidate.Size, modified)
	if blockedWorkout != uuid.Nil {
		reprocessingExec(t, db, tx, `INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256)
		 VALUES($1,$2,$3,$4,$5,$6,'skipped_live_owner',$7)`, uuid.New(), f.account, f.source, file, f.job, blockedWorkout, bytes.Repeat([]byte{7}, 32))
	}
	if state == "succeeded" {
		reprocessingExec(t, db, tx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=transaction_timestamp() WHERE id=$1`, file, bytes.Repeat([]byte{7}, 32))
		identity := candidate.Observed
		if identity == "" {
			identity = fmt.Sprintf("v1:%d:%d:%d:%d:%d:%d:%d", candidate.Size, candidate.ModifiedSec, candidate.ModifiedNS, candidate.Device, candidate.Inode, candidate.CTimeSec, candidate.CTimeNS)
		}
		var recorded bool
		if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,$5::date,$6,$7,$8,$9)`,
			f.job, reprocessingWorker, f.lease, candidate.Name, candidate.ExportDate, candidate.Size, modified, identity, bytes.Repeat([]byte{7}, 32)).Scan(&recorded); err != nil || !recorded {
			t.Fatalf("inventory after receipt=%t err=%v", recorded, err)
		}
	} else {
		reprocessingExec(t, db, tx, `UPDATE app.source_files SET state='failed',failure_code='source-read-failed',failure_summary='Synthetic failure.',processed_at=transaction_timestamp() WHERE id=$1`, file)
	}
	if historical {
		reprocessingExec(t, db, tx, `ALTER TABLE app.source_files ENABLE TRIGGER source_files_receipt_before_write`)
	}
	reprocessingCommit(t, db, tx)
	return file
}

func reprocessingWorkout(t *testing.T, db testDatabases, f reprocessingFixture) uuid.UUID {
	t.Helper()
	tx := reprocessingFence(t, db, f)
	defer tx.Rollback(db.ctx)
	file, workout, kind := uuid.New(), uuid.New(), uuid.New()
	reprocessingExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes) VALUES($1,$2,$3,$4,'synthetic-owner',1)`, file, f.account, f.source, f.job)
	reprocessingExec(t, db, tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, kind, f.account)
	reprocessingExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
	 VALUES($1,$2,$3,$4,$5,$6,$7,'Walking','2026-01-01Z','2026-01-01T00:01:00Z',60)`, workout, f.account, f.source, file, kind, uuid.NewString(), bytes.Repeat([]byte{7}, 32))
	reprocessingCommit(t, db, tx)
	return workout
}

func reprocessingFinish(t *testing.T, db testDatabases, f reprocessingFixture) {
	t.Helper()
	// Terminal retries remain legal even with zero imported workouts.
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.finish_job($1,$2,$3,'failed','source-read-failed','Synthetic failure.')`, f.job, reprocessingWorker, f.lease) {
		// Several policy checks intentionally call this twice on the same job.
		status, _ := readStatus(t, db.ctx, db.migration, f.account, f.job)
		if status != "failed" {
			t.Fatal("synthetic reprocessing job finish failed")
		}
	}
}

func reprocessingExec(t *testing.T, db testDatabases, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(db.ctx, query, args...); err != nil {
		t.Fatalf("synthetic SQL failed: %v", err)
	}
}

func reprocessingCommit(t *testing.T, db testDatabases, tx pgx.Tx) {
	t.Helper()
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func reprocessingError(t *testing.T, err error, code string) {
	t.Helper()
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) || postgres.Code != code {
		t.Fatalf("SQL error=%v, want %s", err, code)
	}
}
