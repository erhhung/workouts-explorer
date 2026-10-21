package migrations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only an empty migrated public scratch database is accepted. SQL generates all
// points; no application exports, private profiles or external OSM data are read.
func TestCoveragePipelineFullInputPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_PIPELINE_BOUNDS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit migrated disposable pipeline bounds database required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "workouts_pipeline_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("pipeline bounds require a named loopback scratch database")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("pipeline bounds prohibit remote fallbacks")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := func(role string) *pgxpool.Pool {
		cfg := config.Copy()
		cfg.MaxConns = 4
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "60s"
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	var empty bool
	if err := db.migration.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.jobs) AND
		(SELECT schema_version=$1 AND minimum_runtime_version=28 FROM app.schema_metadata WHERE singleton)`, database.SupportedSchemaVersion).Scan(&empty); err != nil || !empty {
		t.Fatalf("requires empty migrated supported schema: %v", err)
	}
	for _, p := range []*pgxpool.Pool{db.api, db.worker, db.coverage} {
		if !database.Ready(ctx, p) {
			t.Fatal("V2 runtime is not ready")
		}
	}
	seed := func(f bootstrapFixture, workouts []uuid.UUID, count int) {
		t.Helper()
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=ANY($2::uuid[])`, f.account, workouts); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude,
			altitude,speed,course,horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy)
			SELECT $1,workout,n,'2026-01-01 00:00:00+00'::timestamptz+n*interval '1 microsecond',
			37::double precision+(n%100)::double precision/1048576,-122::double precision-(n%100)::double precision/1048576,
			CASE WHEN n%2<>0 THEN -17.375+(n%8)*0.25 END,CASE WHEN n%3<>0 THEN 3.75 END,
			CASE WHEN n%5<>0 THEN 179.125 END,CASE WHEN n%7<>0 THEN 3.5 END,CASE WHEN n%11<>0 THEN 4.25 END,
			CASE WHEN n%13<>0 THEN 0.125 END,CASE WHEN n%17<>0 THEN 0.0625 END
			FROM unnest($2::uuid[]) workout CROSS JOIN generate_series(0,$3::integer-1) n`, f.account, workouts, count); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	read := func(f bootstrapFixture, job, lease uuid.UUID, size int, flush bool) []pipelineSnapshot {
		t.Helper()
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,$4,$5)`, f.account, job, lease, size, flush)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows.FieldDescriptions()) != 5 {
			t.Fatal("batch is not metadata-only")
		}
		var items []pipelineSnapshot
		for rows.Next() {
			var item pipelineSnapshot
			if err := rows.Scan(&item.workout, &item.event, &item.origin, &item.content, &item.epoch); err != nil {
				t.Fatal(err)
			}
			items = append(items, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return items
	}
	snapshot := func(f bootstrapFixture, job, lease uuid.UUID, item pipelineSnapshot, want int) pipelineSnapshot {
		t.Helper()
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var content, coordinates []byte
		var epoch int64
		var count int
		if err := tx.QueryRow(ctx, `SELECT * FROM app.read_ingest_coverage_route_snapshot($1,$2,'pipeline-test',$3,NULL,$4,$5,$6)`,
			f.account, job, lease, item.workout, item.event, item.origin).Scan(&content, &epoch, &item.digest, &count, &coordinates); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, item.content) || epoch != item.epoch || count != want || len(item.digest) != 32 {
			t.Fatalf("bad single snapshot count=%d want=%d digest=%d", count, want, len(item.digest))
		}
		var pairs [][2]float64
		if err := json.Unmarshal(coordinates, &pairs); err != nil || len(pairs) != count {
			t.Fatalf("coordinates count=%d: %v", len(pairs), err)
		}
		if len(coordinates) > count*64+2 {
			t.Fatalf("snapshot carried telemetry instead of compact coordinate pairs: %d bytes", len(coordinates))
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return item
	}
	adopt := func(f bootstrapFixture, job, lease uuid.UUID, workout uuid.UUID) {
		t.Helper()
		pipelineRecord(t, db, f, job, lease, []uuid.UUID{workout}, "adopted")
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, job, lease); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'pipeline-test',$2,$3)`, job, lease, workout); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("FullCanonicalParityIncludingEmptyAndParserLimit", func(t *testing.T) {
		f, job, lease := pipelineSource(t, db, 1)
		for _, count := range []int{0, 1, 50000, 50001, 250000} {
			seed(f, f.workouts, count)
			var full, legacy []byte
			if err := db.migration.QueryRow(ctx, `SELECT app.ingest_coverage_snapshot_digest($1,$2),app.adopted_coverage_route_digest($1,$2)`, f.account, f.workouts[0]).Scan(&full, &legacy); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(full, pipelineFullReferenceDigest(count)) {
				t.Fatalf("full SQL/Go canonical mismatch at%d", count)
			}
			if count == 0 || count > 50000 {
				if legacy != nil {
					t.Fatalf("legacy bounded digest changed at%d", count)
				}
			} else if !bytes.Equal(full, legacy) {
				t.Fatalf("bounded canonical parity changed at%d", count)
			}
			if count > 0 {
				pipelineRecord(t, db, f, job, lease, f.workouts, "updated")
				items := read(f, job, lease, 25, true)
				if len(items) != 1 {
					t.Fatal("missing full route metadata")
				}
				got := snapshot(f, job, lease, items[0], count)
				if !bytes.Equal(got.digest, full) {
					t.Fatal("snapshot hash was truncated")
				}
			}
		}
		seed(f, f.workouts, 250001)
		tx := beginAccount(t, ctx, db.worker, f.account)
		item := read(f, job, lease, 25, true)[0]
		_, err := tx.Exec(ctx, `SELECT * FROM app.read_ingest_coverage_route_snapshot($1,$2,'pipeline-test',$3,NULL,$4,$5,$6)`, f.account, job, lease, item.workout, item.event, item.origin)
		bootstrapSQLState(t, err, "22023")
		_ = tx.Rollback(ctx)
	})
	t.Run("Batch25By50000ContainsNoHiddenRouteWork", func(t *testing.T) {
		pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 25)
		seed(f, f.workouts, 50000)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		items := read(f, job, lease, 25, false)
		if len(items) != 25 {
			t.Fatalf("batch rows=%d", len(items))
		}
		tx := beginAccount(t, ctx, db.worker, f.account)
		var encoded []byte
		if err := tx.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(batch)) FROM app.read_ingest_coverage_batch($1,$2,'pipeline-test',$3,NULL,NULL,25,false) batch`, f.account, job, lease).Scan(&encoded); err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 16384 {
			t.Fatalf("metadata result is unbounded: %d bytes", len(encoded))
		}
		_ = tx.Rollback(ctx)
		var definition string
		if err := db.migration.QueryRow(ctx, `SELECT lower(pg_get_functiondef('app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean)'::regprocedure))`).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"workout_route_points", "jsonb_agg", "snapshot_digest", "route_input_sha256", "points jsonb"} {
			if strings.Contains(definition, forbidden) {
				t.Fatalf("metadata reader still performs hidden route work: %s", forbidden)
			}
		}
		snapshot(f, job, lease, items[0], 50000)
		t.Logf("25 x 50000 points: batch JSON only %d bytes", len(encoded))
	})
	t.Run("EmptyAdoptionFlushWithOneAndFullBatchSiblings", func(t *testing.T) {
		for _, siblings := range []int{1, 25} {
			f, job, lease := pipelineSource(t, db, siblings+1)
			seed(f, f.workouts[:1], 0)
			pipelineRecord(t, db, f, job, lease, f.workouts[1:], "created")
			adopt(f, job, lease, f.workouts[0])
			for {
				items := read(f, job, lease, 25, true)
				if len(items) == 0 {
					break
				}
				ready := pipelineReady(items, "map_data_ready")
				for i, item := range items {
					count := 2
					if item.origin == "adopted" {
						count = 0
					}
					item = snapshot(f, job, lease, item, count)
					ready[i] = pipelineReady([]pipelineSnapshot{item}, "map_data_ready")[0]
					if count == 0 {
						ready[i]["readiness"] = "unavailable"
						ready[i]["reason"] = "no_provider_region"
						ready[i]["generations"] = []any{}
					}
				}
				payload, _ := json.Marshal(ready)
				tx := beginAccount(t, ctx, db.worker, f.account)
				if _, err := tx.Exec(ctx, pipelineApplySQL, f.account, job, "pipeline-test", lease, nil, payload); err != nil {
					t.Fatal(err)
				}
				pipelineSealPrepared(t, db, tx, f.account, job, "pipeline-test", lease, nil)
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			pipelineTerminalRecovery(t, db, f, job, lease)
			var valid bool
			if err := db.migration.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
				AND NOT EXISTS(SELECT 1 FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL)
				AND NOT EXISTS(SELECT 1 FROM app.coverage_route_job_contexts WHERE account_id=$1 AND workout_id=$2)
				AND (SELECT count(*)=$3 FROM app.coverage_route_job_contexts WHERE account_id=$1)`, f.account, f.workouts[0], siblings).Scan(&valid); err != nil || !valid {
				t.Fatalf("empty adoption poisoned sibling handoff: %v", err)
			}
		}
	})
	t.Run("OversizeNormalAndAdoptedDoNotPoisonSiblingOrSource", func(t *testing.T) {
		for _, origin := range []string{"normal", "adopted", "unavailable", "failed-source-recovery"} {
			f, job, lease := pipelineSource(t, db, 2)
			seed(f, f.workouts[:1], 50001)
			pipelineRecord(t, db, f, job, lease, f.workouts, "created")
			if origin == "adopted" {
				adopt(f, job, lease, f.workouts[0])
			}
			items := read(f, job, lease, 25, true)
			var oversizedDigest []byte
			for i := range items {
				count := 2
				if items[i].workout == f.workouts[0] {
					count = 50001
				}
				items[i] = snapshot(f, job, lease, items[i], count)
				if count == 50001 {
					oversizedDigest = items[i].digest
				}
			}
			readiness := "map_data_ready"
			if origin == "unavailable" {
				readiness = "unavailable"
			}
			payload, _ := json.Marshal(pipelineReady(items, readiness))
			var authorityJob any = job
			var revision any
			worker := "pipeline-test"
			authorityLease := lease
			role := db.worker
			if origin == "failed-source-recovery" {
				if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'failed','source-read-failed','Synthetic source failure.')`, job, lease) {
					t.Fatal("finish failed source")
				}
				authorityJob = nil
				revision = int64(14)
				worker = "bounds-recovery"
				authorityLease = uuid.New()
				role = db.coverage
				bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id=$2,lease_token=$3,heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '2 minutes',lease_runtime_version=28 WHERE account_id=$1`, f.account, worker, authorityLease)
			}
			tx := beginAccount(t, ctx, role, f.account)
			if _, err := tx.Exec(ctx, pipelineApplySQL, f.account, authorityJob, worker, authorityLease, revision, payload); err != nil {
				t.Fatal(err)
			}
			pipelineSealPrepared(t, db, tx, f.account, authorityJob, worker, authorityLease, revision)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if origin == "normal" || origin == "adopted" {
				pipelineTerminalRecovery(t, db, f, job, lease)
			}
			var valid bool
			if err := db.migration.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL)
				AND NOT EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)`, f.account).Scan(&valid); err != nil || !valid {
				t.Fatalf("%s left poison handoff: %v", origin, err)
			}
			if origin == "unavailable" {
				if err := db.migration.QueryRow(ctx, `SELECT count(*)=0 FROM app.coverage_route_job_contexts WHERE account_id=$1`, f.account).Scan(&valid); err != nil || !valid {
					t.Fatal("unavailable route created a job")
				}
				continue
			}
			var child uuid.UUID
			var hash []byte
			if err := db.migration.QueryRow(ctx, `SELECT target_job_id,route_input_sha256 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&child, &hash); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(hash, oversizedDigest) {
				t.Fatal("coverage child did not retain the complete oversized input digest")
			}
			claim := uuid.New()
			if !callBool(t, ctx, db.migration, f.account, `SELECT app.claim_job($1,'bounds-matcher',$2,interval '1 minute')`, child, claim) {
				t.Fatal("claim processing-limit child")
			}
			tx = beginAccount(t, ctx, db.coverage, f.account)
			var fetched int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_route($1,'bounds-matcher',$2)`, child, claim).Scan(&fetched); err != nil || fetched != 50001 {
				t.Fatalf("original matching guard changed: points=%d %v", fetched, err)
			}
			_ = tx.Rollback(ctx)
			if !callBool(t, ctx, db.coverage, f.account, `SELECT app.fail_coverage_route($1,'bounds-matcher',$2,1,'coverage-route-too-large','Coverage route exceeded a processing limit.')`, child, claim) {
				t.Fatal("finish deterministic matching limit failure")
			}
			if err := db.migration.QueryRow(ctx, `SELECT processing_state='failed' AND processing_failure_code='coverage-route-too-large' FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&valid); err != nil || !valid {
				t.Fatal("oversized input did not become a deterministic coverage failure")
			}
		}
	})
	t.Run("UnavailableLargeRouteCanUseOrdinaryFutureGenerationSweep", func(t *testing.T) {
		f, job, lease := pipelineSource(t, db, 1)
		seed(f, f.workouts, 50001)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		items := read(f, job, lease, 25, true)
		items[0] = snapshot(f, job, lease, items[0], 50001)
		pipelineApply(t, db, f, job, lease, items, "unavailable")
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
			t.Fatal("finish unavailable source")
		}
		reconciliationLease := uuid.New()
		bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET worker_id='full-input-reconcile',lease_token=$2,
			heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '2 minutes',lease_runtime_version=28 WHERE account_id=$1`, f.account, reconciliationLease)
		// Change only synthetic fixture input to prove repair verifies the FULL
		// parser domain, not the old50001 prefix or matching-cap NULL sentinel.
		seed(f, f.workouts, 250000)
		tx := beginAccount(t, ctx, db.coverage, f.account)
		defer tx.Rollback(ctx)
		var content, digest, coordinates []byte
		var epoch int64
		var count int
		if err := tx.QueryRow(ctx, `SELECT * FROM app.read_coverage_reconciliation_snapshot($1,14,'full-input-reconcile',$2,$3,1,$4)`,
			f.account, reconciliationLease, f.workouts[0], items[0].digest).Scan(&content, &epoch, &digest, &count, &coordinates); err != nil || count != 250000 {
			t.Fatalf("ordinary full snapshot count=%d: %v", count, err)
		}
		if !bytes.Equal(digest, pipelineFullReferenceDigest(250000)) || !bytes.Equal(content, items[0].content) || epoch != items[0].epoch {
			t.Fatal("ordinary snapshot is truncated/inconsistent")
		}
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,14,'full-input-reconcile',$2,$3,1,$4,$5)`,
			f.account, reconciliationLease, f.workouts[0], items[0].digest, digest).Scan(&revision); err != nil || revision != 2 {
			t.Fatalf("full ordinary repair revision=%d: %v", revision, err)
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT app.apply_coverage_reconciliation($1,14,'full-input-reconcile',$2,$3,$4,$5,'map_data_ready',NULL,
			'[{"regionId":"geofabrik:test","generation":1}]',$6,$7,'coverage-experimental-v1','coverage-sampling-experimental-v1',
			'coverage-path-policy-experimental-v82',5)`, f.account, reconciliationLease, f.workouts[0], revision, digest, uuid.New(), uuid.New()).Scan(&applied); err != nil || !applied {
			t.Fatalf("ordinary future ready apply=%t: %v", applied, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func pipelineFullReferenceDigest(count int) []byte {
	points := make([]bootstrapCanonicalPoint, count)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	moduli := [7]int{2, 3, 5, 7, 11, 13, 17}
	for i := range points {
		stamp := base.Add(time.Duration(i) * time.Microsecond)
		points[i] = bootstrapCanonicalPoint{sequence: int64(i), timestamp: &stamp, latitude: 37 + float64(i%100)/1048576, longitude: -122 - float64(i%100)/1048576}
		values := [7]float64{-17.375 + float64(i%8)*0.25, 3.75, 179.125, 3.5, 4.25, 0.125, 0.0625}
		for j, modulus := range moduli {
			if i%modulus != 0 {
				value := values[j]
				points[i].optional[j] = &value
			}
		}
	}
	return bootstrapCanonicalDigest(points)
}
