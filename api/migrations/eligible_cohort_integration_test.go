package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
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

// This suite never discovers application endpoints. It requires its own empty,
// explicitly named loopback scratch database, with provisioned synthetic roles.
func TestEligibleCohortPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_ELIGIBLE_COHORT_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit empty disposable eligible-cohort database required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_pipeline_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "::1") {
		t.Fatal("eligible-cohort suite requires a named loopback scratch database")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			t.Fatal("remote/fallback endpoints prohibited")
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
		t.Fatal("eligible-cohort database must be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION postgis`); err != nil {
		t.Fatal(err)
	}
	migration := config.Copy()
	migration.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*migration)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	for _, role := range []*pgxpool.Pool{db.api, db.worker, db.coverage} {
		if !database.Ready(ctx, role) {
			t.Fatal("fresh canonical 28 must satisfy deferred + eligible-cohort readiness")
		}
	}
	prepare := func(f bootstrapFixture, job, lease uuid.UUID, item pipelineSnapshot, readiness, region string) {
		t.Helper()
		payload := pipelineReady([]pipelineSnapshot{item}, readiness)
		if region != "" {
			payload[0]["generations"] = []map[string]any{{"regionId": region, "generation": 1}}
		}
		encoded, _ := json.Marshal(payload)
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var workout, event uuid.UUID
		var parent, child *uuid.UUID
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT * FROM app.apply_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,$4,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,28)`,
			f.account, job, lease, encoded).Scan(&workout, &event, &parent, &child, &outcome); err != nil {
			t.Fatal(err)
		}
		if parent != nil || child != nil || (outcome != "prepared" && outcome != "unavailable") {
			t.Fatalf("preparation created physical work: outcome=%s parent=%v child=%v", outcome, parent, child)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	seal := func(f bootstrapFixture, job, lease uuid.UUID, recovery bool, mutate bool) (int, string) {
		t.Helper()
		role, worker := db.worker, "pipeline-test"
		var authority any = job
		var revision any
		if recovery {
			role, worker, authority, revision = db.coverage, "cohort-recovery", nil, int64(14)
		}
		tx := beginAccount(t, ctx, role, f.account)
		defer tx.Rollback(ctx)
		var builder, root uuid.UUID
		var size int
		var rules, sampling, policy string
		var minimum float64
		var targets []byte
		err := tx.QueryRow(ctx, `SELECT * FROM app.read_ingest_coverage_prepared($1,$2,$3,$4,$5,1)`,
			f.account, authority, worker, lease, revision).Scan(&builder, &root, &size, &rules, &sampling, &policy, &minimum, &targets)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "held"
		}
		if err != nil {
			t.Fatal(err)
		}
		if mutate {
			var items []map[string]any
			if err := json.Unmarshal(targets, &items); err != nil {
				t.Fatal(err)
			}
			items[0]["generations"] = []any{}
			targets, _ = json.Marshal(items)
		}
		var parent *uuid.UUID
		var count int
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,$2,$3,$4,$5,$6,$7,28)`,
			f.account, authority, worker, lease, revision, builder, targets).Scan(&parent, &count, &outcome); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return count, outcome
	}
	sizes := func(f bootstrapFixture) []int {
		t.Helper()
		var result []int
		if err := admin.QueryRow(ctx, `SELECT COALESCE(array_agg(size ORDER BY created_at,id),'{}') FROM (
			SELECT parent.id,parent.created_at,count(child.id)::integer size FROM app.jobs parent
			JOIN app.jobs child ON child.parent_job_id=parent.id WHERE parent.account_id=$1 AND parent.kind='coverage_update'
			GROUP BY parent.id) parents`, f.account).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("EligibilityBeforeJobsAndMixedRegions", func(t *testing.T) {
		f, job, lease := pipelineSource(t, db, 28)
		pipelineRecord(t, db, f, job, lease, f.workouts[:25], "created")
		for i, item := range pipelineRead(t, db, f, job, lease, true) {
			readiness := "map_data_ready"
			if i < 3 {
				readiness = "unavailable"
			}
			region := ""
			if readiness == "map_data_ready" && i%2 == 0 {
				region = "geofabrik:other"
			}
			prepare(f, job, lease, item, readiness, region)
		}
		if got := sizes(f); len(got) != 0 {
			t.Fatalf("25 candidates/22 eligible created parents: %v", got)
		}
		if _, outcome := seal(f, job, lease, false, false); outcome != "held" {
			t.Fatal("flush/source EOF must not authorize partial work")
		}
		if got := pipelineRead(t, db, f, job, lease, false); len(got) != 0 {
			t.Fatal("prepared metadata was reread as unprepared intake")
		}
		pipelineRecord(t, db, f, job, lease, f.workouts[25:], "created")
		for _, item := range pipelineRead(t, db, f, job, lease, false) {
			prepare(f, job, lease, item, "map_data_ready", "")
		}
		if count, outcome := seal(f, job, lease, false, false); count != 25 || outcome != "sealed" {
			t.Fatalf("seal count=%d outcome=%s", count, outcome)
		}
		if got := sizes(f); !slices.Equal(got, []int{25}) {
			t.Fatalf("global mixed-region eligible cohort: %v", got)
		}
		tx := beginAccount(t, ctx, db.api, f.account)
		defer tx.Rollback(ctx)
		var regions []string
		var mixed bool
		if err := tx.QueryRow(ctx, `SELECT scope.* FROM app.jobs parent CROSS JOIN LATERAL app.read_coverage_job_scope(parent.id) scope
			WHERE parent.account_id=$1 AND parent.kind='coverage_update'`, f.account).Scan(&regions, &mixed); err != nil || !mixed ||
			!slices.Equal(regions, []string{"geofabrik:other", "geofabrik:test"}) {
			t.Fatalf("actual scope union=%v mixed=%v err=%v", regions, mixed, err)
		}
	})
	t.Run("CatalogProofCannotBlessCachedROI", func(t *testing.T) {
		pipelineConfigure(t, db, 1)
		defer pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 1)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		item := pipelineRead(t, db, f, job, lease, false)[0]
		for _, proof := range []any{"<missing>", nil, 42, "", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 65)} {
			payload := pipelineReady([]pipelineSnapshot{item}, "map_data_ready")
			payload[0]["catalogFingerprint"] = proof
			if proof == "<missing>" {
				delete(payload[0], "catalogFingerprint")
			}
			encoded, _ := json.Marshal(payload)
			tx := beginAccount(t, ctx, db.worker, f.account)
			_, err := tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, encoded)
			bootstrapSQLState(t, err, "22023")
			_ = tx.Rollback(ctx)
		}
		prepare(f, job, lease, item, "map_data_ready", "")
		var builder uuid.UUID
		var targets []byte
		read := beginAccount(t, ctx, db.worker, f.account)
		if err := read.QueryRow(ctx, `SELECT builder_id,targets FROM app.read_ingest_coverage_prepared($1,$2,'pipeline-test',$3,NULL,1)`, f.account, job, lease).Scan(&builder, &targets); err != nil {
			t.Fatal(err)
		}
		_ = read.Rollback(ctx)
		var refreshed []map[string]any
		if err := json.Unmarshal(targets, &refreshed); err != nil {
			t.Fatal(err)
		}
		if refreshed[0]["catalogFingerprint"] != pipelineCatalogProof {
			t.Fatal("prepared reader lost stored catalogue proof")
		}
		// Intersecting heads remain unchanged; a different full catalogue cut
		// must invalidate the old ROI rather than overwrite its evidence.
		refreshed[0]["catalogFingerprint"] = strings.Repeat("b", 64)
		targets, _ = json.Marshal(refreshed)
		write := beginAccount(t, ctx, db.worker, f.account)
		var parent *uuid.UUID
		var count int
		var outcome string
		if err := write.QueryRow(ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,$2,'pipeline-test',$3,NULL,$4,$5,28)`, f.account, job, lease, builder, targets).Scan(&parent, &count, &outcome); err != nil || outcome != "invalidated" || parent != nil || count != 0 {
			t.Fatalf("changed proof blessed cached ROI: %s/%d/%v %v", outcome, count, parent, err)
		}
		if err := write.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var safe bool
		if err := admin.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.ingest_coverage_prepared WHERE account_id=$1)
		 AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update')
		 AND (SELECT count(*)=1 AND bool_and(acknowledged_at IS NULL) FROM app.ingest_coverage_handoffs WHERE account_id=$1)`, f.account).Scan(&safe); err != nil || !safe {
			t.Fatal("invalidated proof lost obligation or created work", err)
		}
		payload := pipelineReady(pipelineRead(t, db, f, job, lease, false), "map_data_ready")
		payload[0]["catalogFingerprint"] = strings.Repeat("b", 64)
		encoded, _ := json.Marshal(payload)
		write = beginAccount(t, ctx, db.worker, f.account)
		if _, err := write.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, encoded); err != nil {
			t.Fatal(err)
		}
		pipelineSealPrepared(t, db, write, f.account, job, "pipeline-test", lease, nil)
		if err := write.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if got := sizes(f); !slices.Equal(got, []int{1}) {
			t.Fatalf("fresh ROI proof did not seal one target: %v", got)
		}
	})
	t.Run("SavedCapacityAndVectorInvalidation", func(t *testing.T) {
		pipelineConfigure(t, db, 5)
		defer pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 7)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		items := pipelineRead(t, db, f, job, lease, false)
		for _, item := range items[:3] {
			prepare(f, job, lease, item, "map_data_ready", "")
		}
		pipelineConfigure(t, db, 2)
		for _, item := range items[3:5] {
			prepare(f, job, lease, item, "map_data_ready", "")
		}
		if count, outcome := seal(f, job, lease, false, true); count != 0 || outcome != "invalidated" {
			t.Fatalf("stale vector count=%d outcome=%s", count, outcome)
		}
		for _, item := range pipelineRead(t, db, f, job, lease, false) {
			prepare(f, job, lease, item, "map_data_ready", "")
			_, _ = seal(f, job, lease, false, false)
		}
		if got := sizes(f); !slices.Equal(got, []int{5, 2}) {
			t.Fatalf("saved B5 must finish before new B2: %v", got)
		}
	})
	t.Run("UnderfullSealAndAuthorityCannotBeForged", func(t *testing.T) {
		f, job, lease := pipelineSource(t, db, 1)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		prepare(f, job, lease, pipelineRead(t, db, f, job, lease, true)[0], "map_data_ready", "")
		var builder uuid.UUID
		var targets []byte
		if err := admin.QueryRow(ctx, `SELECT builder.builder_id,jsonb_agg(jsonb_build_object('workoutId',prepared.workout_id,
			'eventId',prepared.event_id,'generations',prepared.generations,'catalogFingerprint',encode(prepared.catalog_fingerprint,'hex'))) FROM app.ingest_coverage_builders builder
			JOIN app.ingest_coverage_prepared prepared ON prepared.builder_id=builder.builder_id WHERE builder.account_id=$1
			GROUP BY builder.builder_id`, f.account).Scan(&builder, &targets); err != nil {
			t.Fatal(err)
		}
		// Bypass the actionable-reader filter deliberately: the constructor itself
		// must deny a source worker's attempt to turn active EOF into a partial job.
		tx := beginAccount(t, ctx, db.worker, f.account)
		var parent *uuid.UUID
		var count int
		var outcome string
		if err := tx.QueryRow(ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,$2,'pipeline-test',$3,NULL,$4,$5,28)`,
			f.account, job, lease, builder, targets).Scan(&parent, &count, &outcome); err != nil || outcome != "held" || count != 0 || parent != nil {
			t.Fatalf("forged active-root partial parent=%v count=%d outcome=%s err=%v", parent, count, outcome, err)
		}
		_ = tx.Rollback(ctx)
		for _, account := range []uuid.UUID{uuid.Nil, uuid.New()} {
			tx := beginAccount(t, ctx, db.worker, account)
			if account == uuid.Nil {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id','',true)`); err != nil {
					t.Fatal(err)
				}
			}
			_, err := tx.Exec(ctx, `SELECT * FROM app.read_ingest_coverage_prepared($1,$2,'pipeline-test',$3,NULL,1)`, f.account, job, lease)
			bootstrapSQLState(t, err, "42501")
			_ = tx.Rollback(ctx)
		}
		tx = beginAccount(t, ctx, db.worker, f.account)
		_, err := tx.Exec(ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,$2,'pipeline-test',$3,NULL,$4,$5,28)`,
			f.account, job, uuid.New(), builder, targets)
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(ctx)
		cancelTx := beginAccount(t, ctx, db.migration, f.account)
		defer cancelTx.Rollback(ctx)
		if _, err := cancelTx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		if _, err := cancelTx.Exec(ctx, `UPDATE app.jobs SET cancel_requested_at=clock_timestamp(),cancel_requested_by=gen_random_uuid() WHERE id=$1`, job); err != nil {
			t.Fatal(err)
		}
		if err := cancelTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = beginAccount(t, ctx, db.worker, f.account)
		_, err = tx.Exec(ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,25,true)`, f.account, job, lease)
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(ctx)
		if got := sizes(f); len(got) != 0 {
			t.Fatalf("cancelled/foreign/stale lease created a parent: %v", got)
		}
	})
	t.Run("MixedOriginsDoNotForceNormalCanonicalTargets", func(t *testing.T) {
		pipelineConfigure(t, db, 2)
		defer pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 3)
		bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,
			route_input_sha256,readiness_state,map_data_ready_at) SELECT account_id,id,1,$2,'map_data_ready',clock_timestamp()
			FROM app.workouts WHERE account_id=$1 AND id=ANY($3::uuid[])`, f.account, bootstrapDigest(), f.workouts[:2])
		prior := pipelineEnqueue(t, db, f, f.workouts[:2], "geofabrik:test", 1)
		pipelineRecord(t, db, f, job, lease, []uuid.UUID{f.workouts[0], f.workouts[2]}, "created")
		pipelineRecord(t, db, f, job, lease, f.workouts[1:2], "adopted")
		tx := beginAccount(t, ctx, db.worker, f.account)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, job, lease); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'pipeline-test',$2,$3)`, job, lease, f.workouts[1]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		items := pipelineRead(t, db, f, job, lease, true)
		payload, _ := json.Marshal(pipelineReady(items, "map_data_ready"))
		tx = beginAccount(t, ctx, db.worker, f.account)
		var prepared, existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE outcome='prepared'),count(*) FILTER(WHERE outcome='existing')
			FROM app.apply_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,$4,
			'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,28)`,
			f.account, job, lease, payload).Scan(&prepared, &existing); err != nil || prepared != 2 || existing != 1 {
			t.Fatalf("per-item force prepared=%d existing=%d err=%v", prepared, existing, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if count, outcome := seal(f, job, lease, false, false); count != 2 || outcome != "sealed" {
			t.Fatalf("mixed-origin seal count=%d outcome=%s", count, outcome)
		}
		var retained bool
		if err := admin.QueryRow(ctx, `SELECT child.parent_job_id=$3 FROM app.workout_coverage_states state
			JOIN app.jobs child ON child.id=state.target_job_id WHERE state.account_id=$1 AND state.workout_id=$2`,
			f.account, f.workouts[0], prior).Scan(&retained); err != nil || !retained {
			t.Fatalf("normal target was force-refreshed along with adoption: retained=%v err=%v", retained, err)
		}
	})
	t.Run("OnlyTerminalRootCanSealTail", func(t *testing.T) {
		f, job, lease := pipelineSource(t, db, 63)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		for {
			items := pipelineRead(t, db, f, job, lease, false)
			if len(items) == 0 {
				break
			}
			for _, item := range items {
				prepare(f, job, lease, item, "map_data_ready", "")
				_, _ = seal(f, job, lease, false, false)
			}
		}
		if got := sizes(f); !slices.Equal(got, []int{25, 25}) {
			t.Fatalf("active root admitted a tail: %v", got)
		}
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
			t.Fatal("finish source/root")
		}
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id='cohort-recovery',lease_token=$2,
			heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 minute',lease_runtime_version=28 WHERE account_id=$1`, f.account, lease)
		if count, outcome := seal(f, job, lease, true, false); count != 13 || outcome != "sealed" {
			t.Fatalf("terminal prepared-only recovery count=%d outcome=%s", count, outcome)
		}
		if got := sizes(f); !slices.Equal(got, []int{25, 25, 13}) {
			t.Fatalf("terminal tail distribution: %v", got)
		}
	})
}
