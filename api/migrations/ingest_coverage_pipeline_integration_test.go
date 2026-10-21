package migrations_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
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

// The suite only accepts an empty, explicitly named disposable loopback database.
// All routes, events and roles are synthetic; no application configuration is read.
func TestIngestCoveragePipelinePostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable coverage pipeline database required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_pipeline_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("pipeline tests require a named loopback scratch database")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			t.Fatal("pipeline tests prohibit remote/fallback endpoints")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("pipeline scratch database must be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION postgis`); err != nil {
		t.Fatal(err)
	}
	migrationConfig := config.Copy()
	migrationConfig.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*migrationConfig)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 27); err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "8s"
		cfg.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	legacy := newBootstrapFixture(t, db, 1001)
	bootstrapExec(t, db, legacy, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
		SELECT account_id,id,1,$2,'map_data_ready',clock_timestamp() FROM app.workouts WHERE account_id=$1`, legacy.account, bootstrapDigest())
	legacyParent := pipelineEnqueue(t, db, legacy, legacy.workouts[:1000], "geofabrik:test", 1)
	if got := pipelineEnqueue(t, db, legacy, legacy.workouts[1000:], "geofabrik:test", 1); got != legacyParent {
		t.Fatal("legacy fixture did not create a 1001-child parent")
	}
	legacySetup := beginAccount(t, ctx, db.migration, legacy.account)
	if _, err := legacySetup.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacySetup.Exec(ctx, `UPDATE app.jobs SET status='succeeded',terminal_at=clock_timestamp()
		WHERE id=(SELECT job_id FROM app.coverage_route_job_contexts WHERE account_id=$1 AND workout_id=$2);
		`, legacy.account, legacy.workouts[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := legacySetup.Exec(ctx, `UPDATE app.coverage_route_job_contexts SET result_outcome='superseded' WHERE account_id=$1 AND workout_id=$2`, legacy.account, legacy.workouts[0]); err != nil {
		t.Fatal(err)
	}
	if err := legacySetup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	legacyForce, legacySourceJob, legacySourceLease := pipelineSource(t, db, 1)
	pipelineRecord(t, db, legacyForce, legacySourceJob, legacySourceLease, legacyForce.workouts, "adopted")
	bootstrapExec(t, db, legacyForce, `INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,source_id,source_file_id)
		SELECT account_id,id,source_id,source_file_id FROM app.workouts WHERE account_id=$1 AND id=$2`, legacyForce.account, legacyForce.workouts[0])
	// Formatting-only changes exercise bounded SQL anchors without rebasing DDL.
	for signature, replacements := range map[string][2]string{
		"app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid)":                                                                     {"INSERT INTO app.adopted_workout_coverage_refreshes", "insert INTO APP . ADOPTED_WORKOUT_COVERAGE_REFRESHES"},
		"app.construct_coverage_retry(uuid,uuid,integer,integer)":                                                                              {"context.matcher_contract_version", "CONTEXT . MATCHER_CONTRACT_VERSION"},
		"app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)": {"PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation", "perform 1 from app . coverage_reconciliation_accounts RECONCILIATION"},
		"app.claim_coverage_reconciliation(text,uuid,interval,integer)":                                                                        {"SET active_revision =", "set ACTIVE_REVISION="},
	} {
		var definition string
		if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure)`, signature).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		if strings.Count(definition, replacements[0]) != 1 {
			t.Fatalf("compact fixture anchor must occur once: %s", signature)
		}
		if _, err := admin.Exec(ctx, strings.Replace(definition, replacements[0], replacements[1], 1)); err != nil {
			t.Fatal(err)
		}
	}
	var retryDefinition string
	if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef('app.construct_coverage_retry(uuid,uuid,integer,integer)'::regprocedure)`).Scan(&retryDefinition); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		strings.Replace(retryDefinition, "CONTEXT . MATCHER_CONTRACT_VERSION", "'coverage-production-v1'", 1),
		strings.Replace(retryDefinition, "\nBEGIN\n", "\n-- context.matcher_contract_version\nBEGIN\n", 1),
	} {
		if _, err := admin.Exec(ctx, mutation); err != nil {
			t.Fatal(err)
		}
		_, err := provider.Up(ctx)
		bootstrapSQLState(t, err, "P0001")
		var rolledBack bool
		if err := admin.QueryRow(ctx, `SELECT to_regclass('app.coverage_batch_policy') IS NULL AND
			(SELECT max(version_id)=27 FROM public.goose_db_version WHERE is_applied)`).Scan(&rolledBack); err != nil || !rolledBack {
			t.Fatalf("ambiguous DDL was not atomic: %t %v", rolledBack, err)
		}
	}
	if _, err := admin.Exec(ctx, retryDefinition); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("ReadinessAndCapacitySnapshot", func(t *testing.T) {
		for name, rolePool := range map[string]*pgxpool.Pool{"api": db.api, "worker": db.worker, "coverage": db.coverage} {
			if !database.Ready(ctx, rolePool) {
				t.Fatalf("schema28 %s is not ready", name)
			}
		}
		var capacity int
		if err := admin.QueryRow(ctx, `SELECT parent_batch_size FROM app.coverage_job_contexts WHERE job_id=$1`, legacyParent).Scan(&capacity); err != nil || capacity != 1001 {
			t.Fatalf("legacy capacity=%d err=%v", capacity, err)
		}
		for _, rolePool := range []*pgxpool.Pool{db.worker, db.coverage} {
			if _, err := rolePool.Exec(ctx, `SELECT app.configure_coverage_batch_size(27,25)`); err == nil {
				t.Fatal("obsolete runtime or wrong role configured pipeline")
			}
		}
		for _, runtime := range []any{nil, 26, 27} {
			_, err := db.coverage.Exec(ctx, `SELECT * FROM app.claim_next_coverage_route('obsolete',$1,interval '1 minute',$2)`, uuid.New(), runtime)
			bootstrapSQLState(t, err, "55000")
			_, err = db.coverage.Exec(ctx, `SELECT * FROM app.claim_coverage_reconciliation('obsolete',$1,interval '1 minute',$2)`, uuid.New(), runtime)
			bootstrapSQLState(t, err, "55000")
		}
	})
	t.Run("LegacyForceOriginsBackfillAndGlobalRecovery", func(t *testing.T) {
		var backfilled bool
		if err := admin.QueryRow(ctx, `SELECT (SELECT count(*)=1 FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND origin='adopted')
			AND (SELECT count(*)=1 AND bool_and(ingest_job_id=$2 AND import_event_id IS NOT NULL) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1)`, legacyForce.account, legacySourceJob).Scan(&backfilled); err != nil || !backfilled {
			t.Fatalf("legacy force origins not backfilled: %t %v", backfilled, err)
		}
		if !callBool(t, ctx, db.worker, legacyForce.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, legacySourceJob, legacySourceLease) {
			t.Fatal("finish legacy source")
		}
		var rootStatus string
		var terminal, candidates bool
		if err := admin.QueryRow(ctx, `SELECT root.status,app.ingest_coverage_root_terminal(child.account_id,root.id),
		 EXISTS(SELECT 1 FROM app.ingest_coverage_candidates(child.account_id,NULL)) FROM app.jobs child JOIN app.jobs root ON root.id=child.parent_job_id WHERE child.id=$1`, legacySourceJob).Scan(&rootStatus, &terminal, &candidates); err != nil {
			t.Fatal(err)
		}
		t.Logf("legacy root status=%s terminal=%t candidates=%t", rootStatus, terminal, candidates)
		scoped := beginAccount(t, ctx, db.migration, legacyForce.account)
		defer scoped.Rollback(ctx)
		var scopedCandidates bool
		if err := scoped.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.ingest_coverage_candidates($1,NULL))`, legacyForce.account).Scan(&scopedCandidates); err != nil {
			t.Fatal(err)
		}
		_ = scoped.Rollback(ctx)
		t.Logf("same genuine legacy adoption visible with account scope=%t", scopedCandidates)
		lease := uuid.New()
		var account uuid.UUID
		var revision int64
		var cursor *uuid.UUID
		if err := db.coverage.QueryRow(ctx, `SELECT * FROM app.claim_coverage_reconciliation('legacy-force-recovery',$1,interval '1 minute',28)`, lease).Scan(&account, &revision, &cursor); err != nil || account != legacyForce.account {
			t.Fatalf("global legacy discovery account=%s: %v", account, err)
		}
		var payload []byte
		if err := admin.QueryRow(ctx, `SELECT jsonb_build_array(jsonb_build_object('workoutId',request.workout_id,'eventId',request.event_id,'origin','adopted',
			'contentSha256',encode(workout.content_sha256,'hex'),'routeDigest',encode(app.adopted_coverage_route_digest(request.account_id,request.workout_id),'hex'),
			'ownershipEpoch',account.workout_ownership_epoch,'readiness','map_data_ready','reason',NULL,'catalogFingerprint',repeat('a',64),'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb))
			FROM app.adopted_workout_coverage_refreshes request JOIN app.workouts workout ON workout.id=request.workout_id
			JOIN app.accounts account ON account.id=request.account_id WHERE request.account_id=$1`, account).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		tx := beginAccount(t, ctx, db.coverage, account)
		if _, err := tx.Exec(ctx, pipelineApplySQL, account, nil, "legacy-force-recovery", lease, revision, payload); err != nil {
			t.Fatal(err)
		}
		pipelineSealPrepared(t, db, tx, account, nil, "legacy-force-recovery", lease, revision)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL)
			FROM app.ingest_coverage_handoffs WHERE account_id=$1`, account).Scan(&backfilled); err != nil || !backfilled {
			t.Fatalf("legacy force provenance not acknowledged: %t %v", backfilled, err)
		}
	})
	t.Run("CapsDuplicatesAndAtomicOverflow", func(t *testing.T) {
		for _, size := range []int{1, 25, 1000} {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				pipelineConfigure(t, db, size)
				f := newBootstrapFixture(t, db, size+2)
				bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
					SELECT account_id,id,1,$2,'map_data_ready',clock_timestamp() FROM app.workouts WHERE account_id=$1`, f.account, bootstrapDigest())
				initial := f.workouts[:size]
				if size == 1 {
					initial = []uuid.UUID{f.workouts[0], f.workouts[0]}
				}
				parent := pipelineEnqueue(t, db, f, initial, "geofabrik:test", 1)
				if got := pipelineEnqueue(t, db, f, f.workouts[:1], "geofabrik:test", 1); got != parent {
					t.Fatal("exact duplicate against full parent allocated another bucket")
				}
				other := pipelineEnqueue(t, db, f, f.workouts[size:size+1], "geofabrik:test", 1)
				if other == parent {
					t.Fatal("full parent exceeded capacity")
				}
				if size < 1000 {
					tx := beginAccount(t, ctx, db.worker, f.account)
					_, err := tx.Exec(ctx, pipelineEnqueueSQL, f.account, uuid.New(), "geofabrik:other", 1, pipelineTargets(f.workouts[:size+1], 2))
					bootstrapSQLState(t, err, "40001") // Every stale revision fails before allocation.
					_ = tx.Rollback(ctx)
				}
				var bounded bool
				if err := admin.QueryRow(ctx, `SELECT bool_and(children.count<=context.parent_batch_size) AND count(*)=2
					FROM app.coverage_job_contexts context CROSS JOIN LATERAL (SELECT count(*) FROM app.jobs child WHERE child.parent_job_id=context.job_id) children
					WHERE context.account_id=$1`, f.account).Scan(&bounded); err != nil || !bounded {
					t.Fatalf("parent capacity violated: %t %v", bounded, err)
				}
			})
		}
	})
	t.Run("CommitBatchAndDurableOrigins", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 26)
		pipelineRecord(t, db, f, job, lease, f.workouts[:24], "created")
		items := pipelineRead(t, db, f, job, lease, false)
		if len(items) != 24 {
			t.Fatalf("metadata intake must not impose cohort capacity: %d", len(items))
		}
		pipelineApply(t, db, f, job, lease, items, "map_data_ready")
		var parents int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account).Scan(&parents); err != nil || parents != 0 {
			t.Fatalf("24 prepared targets created work: parents=%d %v", parents, err)
		}
		pipelineRecord(t, db, f, job, lease, f.workouts[24:], "updated")
		items = pipelineRead(t, db, f, job, lease, false)
		if len(items) != 2 {
			t.Fatalf("prepared targets must not be reread: metadata=%d", len(items))
		}
		pipelineApply(t, db, f, job, lease, items, "map_data_ready")
		if got := pipelineRead(t, db, f, job, lease, false); len(got) != 0 {
			t.Fatal("remaining one-row batch escaped before flush")
		}
		pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, true), "map_data_ready")
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.account).Scan(&parents); err != nil || parents != 1 {
			t.Fatalf("active EOF must retain one prepared tail: %d %v", parents, err)
		}
		pipelineTerminalRecovery(t, db, f, job, lease)
		var linked bool
		if err := admin.QueryRow(ctx, `SELECT count(*)=26 AND bool_and(acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL
			AND coverage_parent_job_id IS NOT NULL AND ingest_job_id=$2) FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.account, job).Scan(&linked); err != nil || !linked {
			t.Fatalf("durable file/event/job/child links missing: %t %v", linked, err)
		}
	})
	t.Run("CapacitySupersessionConfigurationAndNormalizedVectors", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f := newBootstrapFixture(t, db, 27)
		bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
			SELECT account_id,id,1,$2,'map_data_ready',clock_timestamp() FROM app.workouts WHERE account_id=$1`, f.account, bootstrapDigest())
		tx := beginAccount(t, ctx, db.worker, f.account)
		_, err := tx.Exec(ctx, pipelineEnqueueSQL, f.account, uuid.New(), "geofabrik:test", 1, pipelineTargets(f.workouts[:26], 1))
		bootstrapSQLState(t, err, "22023")
		_ = tx.Rollback(ctx)
		first := pipelineEnqueue(t, db, f, f.workouts[:25], "geofabrik:test", 1)
		bootstrapExec(t, db, f, `UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		second := pipelineEnqueue(t, db, f, f.workouts[:1], "geofabrik:test", 2)
		if first == second {
			t.Fatal("superseded child incorrectly released a full parent's capacity")
		}
		pipelineConfigure(t, db, 1)
		third := pipelineEnqueue(t, db, f, f.workouts[25:26], "geofabrik:test", 1)
		var snapshots bool
		if err := admin.QueryRow(ctx, `SELECT (SELECT parent_batch_size=25 FROM app.coverage_job_contexts WHERE job_id=$1)
			AND (SELECT parent_batch_size=25 FROM app.coverage_job_contexts WHERE job_id=$2)
			AND (SELECT parent_batch_size=1 FROM app.coverage_job_contexts WHERE job_id=$3)
			AND (SELECT count(*)=25 FROM app.jobs WHERE parent_job_id=$1)`, first, second, third).Scan(&snapshots); err != nil || !snapshots {
			t.Fatalf("configuration changed persisted capacities: %t %v", snapshots, err)
		}
		// All but the last target are authoritative duplicates; request length26
		// does not exceed capacity1 because actual new child count is exactly one.
		targets := make([]map[string]any, 0, 26)
		for _, workout := range f.workouts[1:] {
			targets = append(targets, map[string]any{"jobId": uuid.New(), "workoutId": workout, "routeRevision": 1,
				"generations": []map[string]any{{"regionId": "geofabrik:test", "generation": 1}}})
		}
		encoded, _ := json.Marshal(targets)
		tx = beginAccount(t, ctx, db.worker, f.account)
		if _, err := tx.Exec(ctx, pipelineEnqueueSQL, f.account, uuid.New(), "geofabrik:test", 1, encoded); err != nil {
			t.Fatal("request bound was confused with parent capacity:", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		vector := func(reverse bool) []byte {
			regions := []map[string]any{{"regionId": "geofabrik:test", "generation": 1}, {"regionId": "geofabrik:other", "generation": 2}}
			if reverse {
				regions[0], regions[1] = regions[1], regions[0]
			}
			encoded, _ := json.Marshal([]map[string]any{{"jobId": uuid.New(), "workoutId": f.workouts[26], "routeRevision": 1, "generations": regions}})
			return encoded
		}
		var vectorParent uuid.UUID
		for _, reverse := range []bool{false, true} {
			tx = beginAccount(t, ctx, db.worker, f.account)
			var got uuid.UUID
			if err := tx.QueryRow(ctx, pipelineEnqueueSQL, f.account, uuid.New(), "geofabrik:other", 2, vector(reverse)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if vectorParent != uuid.Nil && got != vectorParent {
				t.Fatal("generation vector order created another bucket")
			}
			vectorParent = got
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// Authoritative running and current/no-evidence targets deduplicate even
		// though the full parent is no longer eligible for normal coalescing.
		coverageLease := uuid.New()
		tx = beginAccount(t, ctx, db.migration, f.account)
		var child uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[26]).Scan(&child); err != nil {
			t.Fatal(err)
		}
		var claimed bool
		if err := tx.QueryRow(ctx, `SELECT app.claim_job($1,'pipeline-matcher',$2,interval '1 minute')`, child, coverageLease).Scan(&claimed); err != nil || !claimed {
			t.Fatalf("claim running fixture: %t %v", claimed, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, current := range []bool{false, true} {
			tx = beginAccount(t, ctx, db.migration, f.account)
			if current {
				var finished bool
				if err := tx.QueryRow(ctx, `SELECT app.finish_coverage_route($1,'pipeline-matcher',$2,'no_evidence',1,NULL,NULL)`, child, coverageLease).Scan(&finished); err != nil || !finished {
					t.Fatalf("finish current fixture: %t %v", finished, err)
				}
			}
			var got uuid.UUID
			if err := tx.QueryRow(ctx, pipelineEnqueueSQL, f.account, uuid.New(), "geofabrik:other", 2, vector(true)).Scan(&got); err != nil || got != vectorParent {
				t.Fatalf("running/current dedup got %s: %v", got, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("SnapshotCASLeaseCancellationAndDeferred", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 2)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		items := pipelineRead(t, db, f, job, lease, true)
		for _, accountSetting := range []string{"", uuid.New().String()} {
			tx := beginAccount(t, ctx, db.worker, f.account)
			if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountSetting); err != nil {
				t.Fatal(err)
			}
			_, err := tx.Exec(ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,25,true)`, f.account, job, lease)
			bootstrapSQLState(t, err, "42501")
			_ = tx.Rollback(ctx)
		}
		for _, mutation := range []string{"eventId", "routeDigest", "contentSha256", "ownershipEpoch"} {
			bad := pipelineReady(items, "map_data_ready")
			if mutation == "ownershipEpoch" {
				bad[1][mutation] = int64(99)
			} else if mutation == "eventId" {
				bad[1][mutation] = uuid.New().String()
			} else {
				bad[1][mutation] = strings.Repeat("00", 32)
			}
			tx := beginAccount(t, ctx, db.worker, f.account)
			encoded, _ := json.Marshal(bad)
			_, err := tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, encoded)
			bootstrapSQLState(t, err, "40001")
			_ = tx.Rollback(ctx)
		}
		if _, err := db.worker.Exec(ctx, `SELECT app.configure_coverage_batch_size(28,0)`); err == nil {
			t.Fatal("invalid configuration accepted")
		}
		encoded, _ := json.Marshal(pipelineReady(items, "map_data_ready"))
		tx := beginAccount(t, ctx, db.worker, f.account)
		if _, err := tx.Exec(ctx, `SELECT app.defer_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,$4,28)`, f.account, job, lease, encoded); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if got := pipelineRead(t, db, f, job, lease, true); len(got) != 0 {
			t.Fatal("deferred outbox was immediately due")
		}
		tx = beginAccount(t, ctx, db.worker, f.account)
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_pending_ingest_coverage_workouts($1,$2,'pipeline-test',$3)`, f.account, job, lease).Scan(&pending); err != nil || pending != 2 {
			t.Fatalf("deferred exclusion=%d %v", pending, err)
		}
		_ = tx.Rollback(ctx)
		bootstrapExec(t, db, f, `UPDATE app.ingest_coverage_handoffs SET next_attempt_at='-infinity' WHERE account_id=$1`, f.account)
		pipelineApply(t, db, f, job, lease, items, "unavailable")
		var noJobs bool
		if err := admin.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update')
			AND (SELECT count(*)=2 AND bool_and(acknowledged_at IS NOT NULL AND outcome='unavailable') FROM app.ingest_coverage_handoffs WHERE account_id=$1)`, f.account).Scan(&noJobs); err != nil || !noJobs {
			t.Fatalf("unavailable readiness created jobs or lost acknowledgement: %t %v", noJobs, err)
		}
		pipelineRecord(t, db, f, job, lease, f.workouts, "updated")
		items = pipelineRead(t, db, f, job, lease, true)
		tx = beginAccount(t, ctx, db.worker, f.account)
		encoded, _ = json.Marshal(pipelineReady(items, "map_data_ready"))
		_, err := tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", uuid.New(), nil, encoded)
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(ctx)
		requester := accountRequester(t, db, f.account)
		if !callBool(t, ctx, db.api, f.account, `SELECT app.request_owned_job_cancellation($1,$2)`, job, requester) {
			t.Fatal("cancel synthetic source")
		}
		tx = beginAccount(t, ctx, db.worker, f.account)
		_, err = tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, encoded)
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(ctx)
	})
	t.Run("MixedAdoptionAndNormalDoNotResetRetryBudget", func(t *testing.T) {
		pipelineConfigure(t, db, 1)
		f, job, lease := pipelineSource(t, db, 2)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		importEvents := []uuid.UUID{
			uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff1"),
			uuid.MustParse("11111111-1111-4111-8111-111111111112"),
			uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		}
		importIndex := 0
		adopt := func() uuid.UUID {
			t.Helper()
			tx := beginAccount(t, ctx, db.worker, f.account)
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, job, lease); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,has_route,created_at)
				SELECT $4,account_id,source_id,id,source_file_id,$3,'adopted',content_sha256,true,'2026-01-01Z' FROM app.workouts WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0], job, importEvents[importIndex]); err != nil {
				t.Fatal(err)
			}
			importIndex++
			if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'pipeline-test',$2,$3)`, job, lease, f.workouts[0]); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var event uuid.UUID
			if err := admin.QueryRow(ctx, `SELECT event_id FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&event); err != nil {
				t.Fatal(err)
			}
			return event
		}
		first, second := adopt(), adopt()
		if first == second {
			t.Fatal("adoption event did not advance")
		}
		items := pipelineRead(t, db, f, job, lease, true)
		if len(items) != 2 || items[0].origin != "adopted" || items[1].origin != "normal" {
			t.Fatalf("mixed origin coalescing: %+v", items)
		}
		pipelineApply(t, db, f, job, lease, items, "unavailable")
		var deferred bool
		if err := admin.QueryRow(ctx, `SELECT (SELECT count(*)=1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
			AND (SELECT count(*)=1 FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL)
			AND NOT EXISTS(SELECT 1 FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND acknowledged_at IS NOT NULL)
			AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update')`, f.account).Scan(&deferred); err != nil || !deferred {
			t.Fatalf("deferred force obligation or normal unavailable ACK lost: %t %v", deferred, err)
		}
		bootstrapExec(t, db, f, `UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at='-infinity' WHERE account_id=$1`, f.account)
		items = pipelineRead(t, db, f, job, lease, true)
		if len(items) != 1 || items[0].origin != "adopted" {
			t.Fatalf("deferred force replay: %+v", items)
		}
		pipelineApply(t, db, f, job, lease, items, "map_data_ready")
		var receipts bool
		if err := admin.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL
			AND ingest_job_id=$2 AND import_event_id IS NOT NULL) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1`, f.account, job).Scan(&receipts); err != nil || !receipts {
			t.Fatalf("coalesced adoption origins not retained: %t %v", receipts, err)
		}
		if err := admin.QueryRow(ctx, `SELECT (SELECT import_event_id=$2 FROM app.adopted_coverage_handoff_receipts WHERE event_id=$1)
			AND (SELECT import_event_id=$4 FROM app.adopted_coverage_handoff_receipts WHERE event_id=$3)`, first, importEvents[0], second, importEvents[1]).Scan(&receipts); err != nil || !receipts {
			t.Fatalf("receipt used timestamp/UUID order instead of exact import order: %t %v", receipts, err)
		}
		var failedChild uuid.UUID
		if err := admin.QueryRow(ctx, `SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&failedChild); err != nil {
			t.Fatal(err)
		}
		tx := beginAccount(t, ctx, db.migration, f.account)
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.jobs SET status='failed',terminal_at=clock_timestamp(),failure_code='coverage-transient-failed',failure_summary='Synthetic failure' WHERE id=$1`, failedChild); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.workout_coverage_states SET processing_state='failed' WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		pipelineRecord(t, db, f, job, lease, f.workouts[:1], "updated")
		pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, true), "map_data_ready")
		var preserved bool
		if err := admin.QueryRow(ctx, `SELECT target_job_id=$3 AND processing_state='failed' FROM app.workout_coverage_states
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], failedChild).Scan(&preserved); err != nil || !preserved {
			t.Fatalf("normal event bypassed failed target policy: %t %v", preserved, err)
		}
		tx = beginAccount(t, ctx, db.worker, f.account)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, job, lease); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'pipeline-test',$2,$3)`, job, lease, f.workouts[0])
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(ctx)
		adopt()
		pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, true), "map_data_ready")
		if err := admin.QueryRow(ctx, `SELECT target_job_id<>$3 AND processing_state='queued' FROM app.workout_coverage_states
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], failedChild).Scan(&preserved); err != nil || !preserved {
			t.Fatalf("force adoption lost its separate semantics: %t %v", preserved, err)
		}
	})
	t.Run("TerminalRecoveryDoesNotStealActivePartialBatch", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 2)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id='pipeline-recovery',lease_token=$2,
			heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 minute',lease_runtime_version=28 WHERE account_id=$1`, f.account, lease)
		tx := beginAccount(t, ctx, db.coverage, f.account)
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_ingest_coverage_batch($1,NULL,'pipeline-recovery',$2,14,NULL,25,true)`, f.account, lease).Scan(&count); err != nil || count != 0 {
			t.Fatalf("active partial stolen: %d %v", count, err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_reconciliation_routes($1,14,'pipeline-recovery',$2,NULL,100)`, f.account, lease).Scan(&count); err != nil || count != 0 {
			t.Fatalf("ordinary scan stole outbox: %d %v", count, err)
		}
		_ = tx.Rollback(ctx)
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
			t.Fatal("finish source before simulated crash")
		}
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id=NULL,lease_token=NULL,heartbeat_at=NULL,
			lease_expires_at=NULL,next_scan_at='infinity' WHERE account_id=$1`, f.account)
		var claimedAccount uuid.UUID
		var claimedRevision int64
		var claimedCursor *uuid.UUID
		if err := db.coverage.QueryRow(ctx, `SELECT * FROM app.claim_coverage_reconciliation('pipeline-recovery',$1,interval '1 minute',28)`, lease).
			Scan(&claimedAccount, &claimedRevision, &claimedCursor); err != nil || claimedAccount != f.account || claimedRevision != 14 || claimedCursor != nil {
			t.Fatalf("global terminal outbox discovery account=%s revision=%d cursor=%v: %v", claimedAccount, claimedRevision, claimedCursor, err)
		}
		tx = beginAccount(t, ctx, db.coverage, f.account)
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_ingest_coverage_batch($1,NULL,'pipeline-recovery',$2,14,NULL,25,false)`, f.account, lease).Scan(&count); err != nil || count != 2 {
			t.Fatalf("terminal partial not recoverable: %d %v", count, err)
		}
		_ = tx.Rollback(ctx)
		// A fresh account lease can apply the snapshot even though the source job
		// is terminal; its old ingest lease cannot authorize the apply.
		var payload []byte
		if err := admin.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('workoutId',handoff.workout_id,'eventId',handoff.event_id,
			'origin','normal','ownershipEpoch',account.workout_ownership_epoch,'contentSha256',encode(workout.content_sha256,'hex'),
			'routeDigest',encode(app.adopted_coverage_route_digest(handoff.account_id,handoff.workout_id),'hex'),
			'readiness','map_data_ready','reason',NULL,'catalogFingerprint',repeat('a',64),'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb))
			FROM app.ingest_coverage_handoffs handoff JOIN app.workouts workout ON workout.id=handoff.workout_id
			JOIN app.accounts account ON account.id=handoff.account_id WHERE handoff.account_id=$1`, f.account).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		tx = beginAccount(t, ctx, db.coverage, f.account)
		if _, err := tx.Exec(ctx, pipelineApplySQL, f.account, nil, "pipeline-recovery", lease, int64(14), payload); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ConcurrentProducersAndLegacyRetry", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f := newBootstrapFixture(t, db, 64)
		bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
			SELECT account_id,id,1,$2,'map_data_ready',clock_timestamp() FROM app.workouts WHERE account_id=$1`, f.account, bootstrapDigest())
		var group sync.WaitGroup
		errors := make(chan error, 4)
		for part := range 4 {
			group.Add(1)
			go func() {
				defer group.Done()
				tx, err := db.worker.Begin(ctx)
				if err != nil {
					errors <- err
					return
				}
				defer tx.Rollback(ctx)
				if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, f.account.String()); err == nil {
					for _, workout := range f.workouts[part*16 : (part+1)*16] {
						if _, err = tx.Exec(ctx, pipelineEnqueueSQL, f.account, uuid.New(), "geofabrik:test", 1, pipelineTargets([]uuid.UUID{workout}, 1)); err != nil {
							break
						}
					}
				}
				if err == nil {
					err = tx.Commit(ctx)
				}
				errors <- err
			}()
		}
		group.Wait()
		for range 4 {
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
		}
		var valid bool
		if err := admin.QueryRow(ctx, `SELECT count(*)=3 AND max(children.count)=25 AND sum(children.count)=64
			FROM app.coverage_job_contexts context CROSS JOIN LATERAL (SELECT count(*) FROM app.jobs WHERE parent_job_id=context.job_id) children WHERE context.account_id=$1`, f.account).Scan(&valid); err != nil || !valid {
			t.Fatalf("concurrent buckets invalid: %t %v", valid, err)
		}
		// A legacy large retry is one complete cohort and inherits persisted 1001,
		// even after the live policy changes to one child per new normal parent.
		pipelineConfigure(t, db, 1)
		tx := beginAccount(t, ctx, db.migration, legacy.account)
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'; UPDATE app.jobs SET status='failed',terminal_at=clock_timestamp(),failure_code='coverage-transient-failed',failure_summary='Synthetic failure'
			WHERE kind IN ('coverage_update','coverage_update_route') AND account_id=app.current_account_id();
			UPDATE app.workout_coverage_states SET processing_state='failed' WHERE account_id=app.current_account_id();
			UPDATE app.coverage_route_job_contexts SET result_outcome=NULL WHERE account_id=app.current_account_id()`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = beginAccount(t, ctx, db.migration, legacy.account)
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='30s'`); err != nil {
			t.Fatal(err)
		}
		var retry uuid.UUID
		var count int
		if err := tx.QueryRow(ctx, `SELECT * FROM app.construct_coverage_retry($1,$2,100,NULL)`, legacyParent, legacy.account).Scan(&retry, &count); err != nil || count != 1001 {
			t.Fatalf("legacy retry cohort count=%d err=%v", count, err)
		}
		if err := tx.QueryRow(ctx, `SELECT parent_batch_size=1001 FROM app.coverage_job_contexts WHERE job_id=$1`, retry).Scan(&valid); err != nil || !valid {
			t.Fatalf("retry capacity not inherited: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("FreshAuthorityRejectsChangesDuringOSM", func(t *testing.T) {
		for _, change := range []string{"parent cancellation", "source deletion", "ownership epoch", "raw digest", "file event", "lease reclaim"} {
			t.Run(change, func(t *testing.T) {
				pipelineConfigure(t, db, 25)
				f, job, lease := pipelineSource(t, db, 1)
				pipelineRecord(t, db, f, job, lease, f.workouts, "created")
				items := pipelineRead(t, db, f, job, lease, true)
				want := "40001"
				switch change {
				case "parent cancellation":
					var parent uuid.UUID
					if err := admin.QueryRow(ctx, `SELECT parent_job_id FROM app.jobs WHERE id=$1`, job).Scan(&parent); err != nil {
						t.Fatal(err)
					}
					if !callBool(t, ctx, db.api, f.account, `SELECT app.request_owned_job_cancellation($1,$2)`, parent, accountRequester(t, db, f.account)) {
						t.Fatal("cancel parent")
					}
					want = "42501"
				case "source deletion":
					var source uuid.UUID
					if err := admin.QueryRow(ctx, `SELECT source_id FROM app.job_config_snapshots WHERE job_id=$1`, job).Scan(&source); err != nil {
						t.Fatal(err)
					}
					tx := beginAccount(t, ctx, db.api, f.account)
					if _, err := tx.Exec(ctx, `SELECT app.delete_source($1,$2)`, source, accountRequester(t, db, f.account)); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					want = "42501"
				case "ownership epoch":
					source := uuid.New()
					bootstrapExec(t, db, f, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope)
						VALUES($1,$2,$1::uuid::text,$1::uuid::text,'health-auto-export-local',$3)`, source, f.account, []byte("synthetic"))
					bootstrapExec(t, db, f, `UPDATE app.sources SET deleted_at=clock_timestamp(),config_envelope=NULL,auto_sync_enabled=false WHERE account_id=$1 AND id=$2`, f.account, source)
				case "raw digest":
					bootstrapMutatePoints(t, db, f, `UPDATE app.workout_route_points SET altitude=7 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
				case "file event":
					pipelineRecord(t, db, f, job, lease, f.workouts, "updated")
				case "lease reclaim":
					tx := beginAccount(t, ctx, db.migration, f.account)
					if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
						t.Fatal(err)
					}
					if _, err := tx.Exec(ctx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, job); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					if !callBool(t, ctx, db.worker, f.account, `SELECT app.recover_expired_job($1)`, job) {
						t.Fatal("recover old lease")
					}
					if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'pipeline-test',$2,interval '1 minute')`, job, uuid.New()) {
						t.Fatal("reclaim source")
					}
					want = "42501"
				}
				encoded, _ := json.Marshal(pipelineReady(items, "map_data_ready"))
				tx := beginAccount(t, ctx, db.worker, f.account)
				_, err := tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, encoded)
				bootstrapSQLState(t, err, want)
				_ = tx.Rollback(ctx)
				var unchanged bool
				if err := admin.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update')
					AND NOT EXISTS(SELECT 1 FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL)`, f.account).Scan(&unchanged); err != nil || !unchanged {
					t.Fatalf("stale payload changed durable state: %t %v", unchanged, err)
				}
			})
		}
	})
	t.Run("CurrentBatchBorrowsExactFutureRuntimeFence", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 1)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		items := pipelineRead(t, db, f, job, lease, true)
		encoded, _ := json.Marshal(pipelineReady(items, "map_data_ready"))
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'pipeline-test',$2,29)`, job, lease); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, encoded); err != nil {
			t.Fatal("batch replaced captured runtime29:", err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
			t.Fatal(err)
		}
		var runtime int
		if err := tx.QueryRow(ctx, `SELECT runtime_version FROM app.ingest_write_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current()`).Scan(&runtime); err != nil || runtime != 29 {
			t.Fatalf("borrowed runtime=%d %v", runtime, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

const pipelineEnqueueSQL = `SELECT job_id FROM app.enqueue_coverage_update($1,$2,$3,$4,1,
 'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,$5)`
const pipelineApplySQL = `SELECT * FROM app.apply_ingest_coverage_batch($1,$2,$3,$4,$5,$6,
 'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,28)`

func pipelineTargets(workouts []uuid.UUID, revision int) []byte {
	targets := make([]map[string]any, len(workouts))
	for i, workout := range workouts {
		targets[i] = map[string]any{"jobId": uuid.New(), "workoutId": workout, "routeRevision": revision, "generations": []map[string]any{{"regionId": "geofabrik:test", "generation": 1}}}
	}
	encoded, _ := json.Marshal(targets)
	return encoded
}

func pipelineEnqueue(t *testing.T, db testDatabases, f bootstrapFixture, workouts []uuid.UUID, region string, revision int) uuid.UUID {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	var parent uuid.UUID
	if err := tx.QueryRow(db.ctx, pipelineEnqueueSQL, f.account, uuid.New(), region, 1, pipelineTargets(workouts, revision)).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return parent
}

func pipelineConfigure(t *testing.T, db testDatabases, size int) {
	t.Helper()
	if _, err := db.worker.Exec(db.ctx, `SELECT app.configure_coverage_batch_size(28,$1)`, size); err != nil {
		t.Fatal(err)
	}
}

func pipelineSource(t *testing.T, db testDatabases, count int) (bootstrapFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := newBootstrapFixture(t, db, count)
	parent := insertQueued(t, db.ctx, db.api, f.account, uuid.Nil, "manual_ingest")
	job := insertQueued(t, db.ctx, db.api, f.account, parent, "manual_ingest_source")
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	if _, err := tx.Exec(db.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.workouts SET source_id=(SELECT source_id FROM app.job_config_snapshots WHERE job_id=$2) WHERE account_id=$1`, f.account, job); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,'pipeline-test',$2,interval '15 minutes')`, job, lease) {
		t.Fatal("claim pipeline source")
	}
	return f, job, lease
}

func pipelineRecord(t *testing.T, db testDatabases, f bootstrapFixture, job, lease uuid.UUID, workouts []uuid.UUID, kind string) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, job, lease).Scan(&source); err != nil {
		t.Fatal(err)
	}
	file := uuid.New()
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at)
		VALUES($1,$2,$3,$4,$1::uuid::text,1,'processing',clock_timestamp())`, file, f.account, source, job); err != nil {
		t.Fatal(err)
	}
	for _, workout := range workouts {
		if _, err := tx.Exec(db.ctx, `UPDATE app.workouts SET source_file_id=$3 WHERE account_id=$1 AND id=$2`, f.account, workout, file); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,has_route)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,EXISTS(SELECT 1 FROM app.workout_route_points WHERE account_id=$2 AND workout_id=$4))`, uuid.New(), f.account, source, workout, file, job, kind, bootstrapDigest()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(db.ctx, `UPDATE app.source_files SET state='succeeded',processed_at=clock_timestamp(),checksum_sha256=$2 WHERE id=$1`, file, bootstrapDigest()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

type pipelineSnapshot struct {
	workout, event  uuid.UUID
	origin          string
	content, digest []byte
	epoch           int64
}

func pipelineRead(t *testing.T, db testDatabases, f bootstrapFixture, job, lease uuid.UUID, flush bool) []pipelineSnapshot {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	rows, err := tx.Query(db.ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,25,$4)`, f.account, job, lease, flush)
	if err != nil {
		t.Fatal(err)
	}
	var result []pipelineSnapshot
	for rows.Next() {
		var item pipelineSnapshot
		if err := rows.Scan(&item.workout, &item.event, &item.origin, &item.content, &item.epoch); err != nil {
			t.Fatal(err)
		}
		result = append(result, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for i := range result {
		item := &result[i]
		var content, coordinates []byte
		var epoch int64
		var count int
		if err := tx.QueryRow(db.ctx, `SELECT * FROM app.read_ingest_coverage_route_snapshot($1,$2,'pipeline-test',$3,NULL,$4,$5,$6)`,
			f.account, job, lease, item.workout, item.event, item.origin).Scan(&content, &epoch, &item.digest, &count, &coordinates); err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(content) != hex.EncodeToString(item.content) || epoch != item.epoch || count != 2 || hex.EncodeToString(item.digest) != hex.EncodeToString(bootstrapDigest()) {
			t.Fatal("single-route snapshot differs from metadata or Go canonical encoding")
		}
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return result
}

// SQL-only fixtures have no OSM database. Their explicit stable proof is not a
// simulated ROI lookup; paired worker fixtures must use the real OSM proof.
const pipelineCatalogProof = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func pipelineReady(items []pipelineSnapshot, readiness string) []map[string]any {
	result := make([]map[string]any, len(items))
	for i, item := range items {
		var reason any
		generations := []map[string]any{}
		if readiness == "map_data_ready" {
			generations = append(generations, map[string]any{"regionId": "geofabrik:test", "generation": 1})
		} else if readiness == "pending" {
			reason = "region_not_active"
		} else {
			reason = "no_provider_region"
		}
		result[i] = map[string]any{"workoutId": item.workout, "eventId": item.event, "origin": item.origin, "contentSha256": hex.EncodeToString(item.content), "routeDigest": hex.EncodeToString(item.digest), "ownershipEpoch": item.epoch, "readiness": readiness, "reason": reason, "generations": generations, "catalogFingerprint": pipelineCatalogProof}
	}
	return result
}

func pipelineApply(t *testing.T, db testDatabases, f bootstrapFixture, job, lease uuid.UUID, items []pipelineSnapshot, readiness string) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	for _, item := range items {
		encoded, _ := json.Marshal(pipelineReady([]pipelineSnapshot{item}, readiness))
		var count int
		if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM (`+pipelineApplySQL+`) prepared`, f.account, job, "pipeline-test", lease, nil, encoded).Scan(&count); err != nil || count != 1 {
			t.Fatalf("prepared rows=%d, want 1: %v", count, err)
		}
		pipelineSealPrepared(t, db, tx, f.account, job, "pipeline-test", lease, nil)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

// Synthetic vectors are already refreshed by the caller. Exercise the real
// metadata reader and seal transaction, never an EOF or singleton bypass.
func pipelineSealPrepared(t *testing.T, db testDatabases, tx pgx.Tx, account uuid.UUID, job any, worker string, lease uuid.UUID, revision any) {
	t.Helper()
	if err := pipelineSealPreparedSQL(db.ctx, tx, account, job, worker, lease, revision); err != nil {
		t.Fatal(err)
	}
}

func pipelineSealPreparedSQL(ctx context.Context, tx pgx.Tx, account uuid.UUID, job any, worker string, lease uuid.UUID, revision any) error {
	for {
		var builder uuid.UUID
		var targets []byte
		err := tx.QueryRow(ctx, `SELECT builder_id,targets FROM app.read_ingest_coverage_prepared($1,$2,$3,$4,$5,1)`, account, job, worker, lease, revision).Scan(&builder, &targets)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var parent *uuid.UUID
		var count int
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,$2,$3,$4,$5,$6,$7,28)`, account, job, worker, lease, revision, builder, targets).Scan(&parent, &count, &outcome); err != nil {
			return err
		}
		if outcome == "held" {
			return nil
		}
		if outcome != "sealed" || parent == nil || count == 0 {
			return fmt.Errorf("unexpected synthetic seal outcome=%s count=%d parent=%v", outcome, count, parent)
		}
	}
}

func pipelineTerminalRecovery(t *testing.T, db testDatabases, f bootstrapFixture, job, lease uuid.UUID) {
	t.Helper()
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
		t.Fatal("finish source before terminal-root recovery")
	}
	recoveryLease := uuid.New()
	bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id='pipeline-recovery',lease_token=$2,heartbeat_at=clock_timestamp(),
	 lease_expires_at=clock_timestamp()+interval '2 minutes',lease_runtime_version=28,active_revision=14 WHERE account_id=$1`, f.account, recoveryLease)
	tx := beginAccount(t, db.ctx, db.coverage, f.account)
	defer tx.Rollback(db.ctx)
	for {
		pipelineSealPrepared(t, db, tx, f.account, nil, "pipeline-recovery", recoveryLease, int64(14))
		// A source-scoped adoption ACK may expose an older still-valid normal
		// origin. Real recovery re-reads intake and deduplicates it against the
		// sealed canonical target, rather than ACKing unrelated origins broadly.
		var payload []byte
		if err := tx.QueryRow(db.ctx, `SELECT jsonb_agg(jsonb_build_object('workoutId',batch.workout_id,'eventId',batch.event_id,'origin',batch.origin,
		 'contentSha256',encode(snapshot.content_sha256,'hex'),'routeDigest',encode(snapshot.route_input_sha256,'hex'),
		 'ownershipEpoch',snapshot.ownership_epoch,'readiness','map_data_ready','reason',NULL,'catalogFingerprint',repeat('a',64),
		 'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb))
		 FROM app.read_ingest_coverage_batch($1,NULL,'pipeline-recovery',$2,14,NULL,1,true) batch
		 CROSS JOIN LATERAL app.read_ingest_coverage_route_snapshot($1,NULL,'pipeline-recovery',$2,14,batch.workout_id,batch.event_id,batch.origin) snapshot`, f.account, recoveryLease).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if payload == nil {
			break
		}
		if _, err := tx.Exec(db.ctx, pipelineApplySQL, f.account, nil, "pipeline-recovery", recoveryLease, int64(14), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}
