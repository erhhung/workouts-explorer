package migrations_test

import (
	"context"
	"crypto/sha256"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// No application URL fallback: this suite accepts only its own initially empty,
// explicitly named disposable loopback database and synthetic import fixtures.
func TestCoverageOriginCleanupPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_ORIGIN_CLEANUP_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit empty disposable origin-cleanup database required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.Database, "workouts_pipeline_test_") || (cfg.Host != "127.0.0.1" && cfg.Host != "::1") {
		t.Fatal("named loopback scratch database required")
	}
	for _, f := range cfg.Fallbacks {
		if f.Host != cfg.Host || f.Port != cfg.Port {
			t.Fatal("fallback endpoints prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("origin-cleanup database must be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION postgis`); err != nil {
		t.Fatal(err)
	}
	migration := cfg.Copy()
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
		pc, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		pc.ConnConfig.RuntimeParams["role"] = role
		pc.ConnConfig.RuntimeParams["statement_timeout"] = "10s"
		p, err := pgxpool.NewWithConfig(ctx, pc)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker"), coverage: pool("workouts_coverage_worker")}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Append a genuine fenced import event, preserving the raw route. Adoption
	// uses the shipped identity capability rather than bypassing owner checks.
	record := func(f bootstrapFixture, job, lease uuid.UUID, kind string, content []byte) uuid.UUID {
		t.Helper()
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'pipeline-test',$2,28)`, job, lease).Scan(&source); err != nil {
			t.Fatal(err)
		}
		file, event := uuid.New(), uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at) VALUES($1,$2,$3,$4,$1::uuid::text,1,'processing',clock_timestamp())`, file, f.account, source, job); err != nil {
			t.Fatal(err)
		}
		if kind == "adopted" {
			var adopted uuid.UUID
			var prior []byte
			var outcome string
			if err := tx.QueryRow(ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,'pipeline-test',$2,$3,$4,$5,NULL,NULL)`, job, lease, file, uuid.New(), f.workouts[0].String()).Scan(&adopted, &prior, &outcome); err != nil || outcome != "adopted" || adopted != f.workouts[0] {
				t.Fatalf("adoption capability: %s %v", outcome, err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE app.workouts SET source_id=$3,source_file_id=$4,content_sha256=$5,provider_duration=provider_duration+1 WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0], source, file, content); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,has_route) VALUES($1,$2,$3,$4,$5,$6,$7,$8,true)`, event, f.account, source, f.workouts[0], file, job, kind, content); err != nil {
			t.Fatal(err)
		}
		if kind == "adopted" {
			if _, err := tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,'pipeline-test',$2,$3)`, job, lease, f.workouts[0]); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE app.source_files SET state='succeeded',processed_at=clock_timestamp(),checksum_sha256=$2 WHERE id=$1`, file, content); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return event
	}
	t.Run("ClearedCancellationMetadataAndOrdinaryCoreShareGuard", func(t *testing.T) {
		pipelineConfigure(t, db, 1)
		defer pipelineConfigure(t, db, 25)
		f, job, lease := pipelineSource(t, db, 1)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, false), "map_data_ready")
		var parent, child uuid.UUID
		if err := admin.QueryRow(ctx, `SELECT child.parent_job_id,child.id FROM app.jobs child WHERE child.account_id=$1 AND kind='coverage_update_route'`, f.account).Scan(&parent, &child); err != nil {
			t.Fatal(err)
		}
		if !callBool(t, ctx, db.migration, f.account, `SELECT app.request_coverage_job_cancellation($1,$2)`, parent, uuid.New()) {
			t.Fatal("cancel target")
		}
		bootstrapExec(t, db, f, `UPDATE app.workout_coverage_states SET target_job_id=NULL,target_revision=NULL,target_rules_version=NULL,target_sampling_version=NULL,target_path_policy_version=NULL,target_generations=NULL,processing_finished_at=(SELECT terminal_at FROM app.jobs WHERE id=$2) WHERE workout_id=$1`, f.workouts[0], child)
		bootstrapExec(t, db, f, `INSERT INTO app.coverage_retry_roots(root_job_id,account_id,automatic_attempts_used) VALUES($1,$2,1) ON CONFLICT(root_job_id) DO UPDATE SET automatic_attempts_used=1`, parent, f.account)
		for _, revision := range []int{1, 2} {
			var matches int
			if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.ingest_coverage_existing_target($1,$2,$3,$4,'[{"regionId":"geofabrik:test","generation":1}]','coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`, f.account, f.workouts[0], revision, bootstrapDigest()).Scan(&matches); err != nil || matches != map[int]int{1: 1, 2: 0}[revision] {
				t.Fatalf("positional revision=%d matches=%d err=%v", revision, matches, err)
			}
		}
		for _, field := range []string{"digest", "generations", "rules", "sampling", "policy", "minimum"} {
			digest := bootstrapDigest()
			generations := `[{"regionId":"geofabrik:test","generation":1}]`
			rules, sampling, policy := "coverage-experimental-v1", "coverage-sampling-experimental-v1", "coverage-path-policy-experimental-v82"
			minimum := 5.0
			switch field {
			case "digest":
				changed := sha256.Sum256([]byte("different raw route"))
				digest = changed[:]
			case "generations":
				generations = `[{"regionId":"geofabrik:test","generation":2}]`
			case "rules":
				rules = "coverage-experimental-v2"
			case "sampling":
				sampling = "coverage-sampling-experimental-v2"
			case "policy":
				policy = "coverage-path-policy-experimental-v83"
			case "minimum":
				minimum = 6
			}
			if count(`SELECT count(*) FROM app.ingest_coverage_existing_target($1,$2,1,$3,$4,$5,$6,$7,$8)`, f.account, f.workouts[0], digest, generations, rules, sampling, policy, minimum) != 0 {
				t.Fatalf("historical cancellation swallowed changed %s", field)
			}
		}
		header := sha256.Sum256([]byte("synthetic changed header, identical raw points"))
		event := record(f, job, lease, "updated", header[:])
		pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, false), "map_data_ready")
		if got := pipelineEnqueue(t, db, f, f.workouts, "geofabrik:test", 1); got != parent {
			t.Fatal("ordinary core reversed cleared cancellation")
		}
		if count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account) != 1 {
			t.Fatal("metadata-only update allocated a root")
		}
		if count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND outcome='existing' AND coverage_parent_job_id=$2 AND coverage_child_job_id=$3`, event, parent, child) != 1 {
			t.Fatal("ACK lost actual cleared cancellation IDs")
		}
		if count(`SELECT automatic_attempts_used FROM app.coverage_retry_roots WHERE root_job_id=$1`, parent) != 1 {
			t.Fatal("normal update reset retry budget")
		}
		if count(`SELECT count(*) FROM app.coverage_retry_targets($1,$2)`, parent, f.account) != 1 {
			t.Fatal("manual retry cancellation footprint lost")
		}
	})
	for _, transfer := range []bool{false, true} {
		name := "DeletedSourceWithoutPreparedRows"
		if transfer {
			name = "TransferPreservesNewOwnerAndForceEvent"
		}
		t.Run(name, func(t *testing.T) {
			f, job, lease := pipelineSource(t, db, 1)
			pipelineRecord(t, db, f, job, lease, f.workouts, "created")
			pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, false), "map_data_ready")
			var oldSource, oldEvent uuid.UUID
			if err := admin.QueryRow(ctx, `SELECT source_id,event_id FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.account).Scan(&oldSource, &oldEvent); err != nil {
				t.Fatal(err)
			}
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
				t.Fatal("finish original root")
			}
			if !callBool(t, ctx, db.migration, f.account, `SELECT app.delete_source($1,$2)`, oldSource, uuid.New()) {
				t.Fatal("delete source")
			}
			if !transfer {
				bootstrapExec(t, db, f, `DELETE FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.account)
			}
			var newEvent, newJob uuid.UUID
			if transfer {
				parent := insertQueued(t, ctx, db.api, f.account, uuid.Nil, "manual_ingest")
				newJob = insertQueued(t, ctx, db.api, f.account, parent, "manual_ingest_source")
				newLease := uuid.New()
				if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'pipeline-test',$2,interval '15 minutes')`, newJob, newLease) {
					t.Fatal("claim replacement owner")
				}
				newEvent = record(f, newJob, newLease, "adopted", bootstrapDigest())
			}
			recoveryLease := uuid.New()
			var account uuid.UUID
			var revision int64
			var cursor *uuid.UUID
			if err := db.coverage.QueryRow(ctx, `SELECT * FROM app.claim_coverage_reconciliation('origin-cleanup',$1,interval '1 minute',28)`, recoveryLease).Scan(&account, &revision, &cursor); err != nil || account != f.account {
				t.Fatalf("orphan-only global discovery account=%s: %v", account, err)
			}
			tx := beginAccount(t, ctx, db.coverage, f.account)
			var builders int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_ingest_coverage_prepared($1,NULL,'origin-cleanup',$2,$3,1)`, f.account, recoveryLease, revision).Scan(&builders); err != nil || builders != 0 {
				t.Fatalf("cleanup metadata builders=%d err=%v", builders, err)
			}
			var routes int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_reconciliation_routes($1,$2,'origin-cleanup',$3,NULL,25)`, f.account, revision, recoveryLease).Scan(&routes); err != nil || routes != 0 {
				t.Fatalf("orphan reached raw snapshot intake: routes=%d err=%v", routes, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			outcome := "deleted"
			if transfer {
				outcome = "superseded"
			}
			if count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND acknowledged_at IS NOT NULL AND outcome=$2 AND coverage_child_job_id IS NULL`, oldEvent, outcome) != 1 {
				t.Fatal("old precise origin was not retired")
			}
			if transfer && (count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND ingest_job_id=$2 AND acknowledged_at IS NULL`, newEvent, newJob) != 1 || count(`SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.account) != 1) {
				t.Fatal("cleanup consumed new owner's force obligation")
			}
			if count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account) != 0 {
				t.Fatal("orphan cleanup created physical coverage work")
			}
			if !callBool(t, ctx, db.coverage, f.account, `SELECT app.advance_coverage_reconciliation($1,$2,'origin-cleanup',$3,NULL,true,interval '1 day')`, f.account, revision, recoveryLease) {
				t.Fatal("release reconciliation")
			}
		})
	}
	for _, change := range []string{"newer input", "unrelated ownership epoch"} {
		t.Run(change, func(t *testing.T) {
			f, job, lease := pipelineSource(t, db, 1)
			pipelineRecord(t, db, f, job, lease, f.workouts, "created")
			pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, false), "map_data_ready")
			var oldEvent, source uuid.UUID
			if err := admin.QueryRow(ctx, `SELECT event_id,source_id FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.account).Scan(&oldEvent, &source); err != nil {
				t.Fatal(err)
			}
			currentEvent := oldEvent
			if change == "newer input" {
				content := sha256.Sum256([]byte("new committed normal header"))
				currentEvent = record(f, job, lease, "updated", content[:])
			}
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
				t.Fatal("finish origin root")
			}
			if change == "unrelated ownership epoch" {
				var unrelated uuid.UUID
				if err := admin.QueryRow(ctx, `SELECT id FROM app.sources WHERE account_id=$1 AND id<>$2 LIMIT 1`, f.account, source).Scan(&unrelated); err != nil {
					t.Fatal(err)
				}
				if !callBool(t, ctx, db.migration, f.account, `SELECT app.delete_source($1,$2)`, unrelated, uuid.New()) {
					t.Fatal("advance unrelated ownership epoch")
				}
			}
			recoveryLease := uuid.New()
			var account uuid.UUID
			var revision int64
			var cursor *uuid.UUID
			if err := db.coverage.QueryRow(ctx, `SELECT * FROM app.claim_coverage_reconciliation('origin-cleanup',$1,interval '1 minute',28)`, recoveryLease).Scan(&account, &revision, &cursor); err != nil || account != f.account {
				t.Fatalf("discover preserved obligation: %s %v", account, err)
			}
			tx := beginAccount(t, ctx, db.coverage, f.account)
			defer tx.Rollback(ctx)
			if change == "newer input" {
				if _, err := tx.Exec(ctx, `SELECT * FROM app.read_ingest_coverage_prepared($1,NULL,'origin-cleanup',$2,$3,1)`, f.account, recoveryLease, revision); err != nil {
					t.Fatal(err)
				}
			} else {
				var builder uuid.UUID
				var targets []byte
				if err := tx.QueryRow(ctx, `SELECT builder_id,targets FROM app.read_ingest_coverage_prepared($1,NULL,'origin-cleanup',$2,$3,1)`, f.account, recoveryLease, revision).Scan(&builder, &targets); err != nil {
					t.Fatal(err)
				}
				var parent *uuid.UUID
				var routes int
				var outcome string
				if err := tx.QueryRow(ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,NULL,'origin-cleanup',$2,$3,$4,$5,28)`, f.account, recoveryLease, revision, builder, targets).Scan(&parent, &routes, &outcome); err != nil || outcome != "invalidated" || parent != nil || routes != 0 {
					t.Fatalf("epoch must reprepare, not ACK or seal: %s %v", outcome, err)
				}
			}
			var candidate uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT event_id FROM app.read_ingest_coverage_batch($1,NULL,'origin-cleanup',$2,$3,NULL,25,true)`, f.account, recoveryLease, revision).Scan(&candidate); err != nil || candidate != currentEvent {
				t.Fatalf("current origin lost after old preparation invalidation: %s %v", candidate, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND acknowledged_at IS NULL`, currentEvent) != 1 {
				t.Fatal("cleanup ACKed current input/epoch obligation")
			}
			if change == "newer input" && count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND outcome='superseded' AND coverage_child_job_id IS NULL`, oldEvent) != 1 {
				t.Fatal("older input origin poisoned future scan")
			}
			if count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account) != 0 {
				t.Fatal("invalidated preparation created a root")
			}
			// Now perform a fresh authorized snapshot/prepare before the terminal
			// seal. This also proves the preserved obligation can actually recover.
			fresh := beginAccount(t, ctx, db.coverage, f.account)
			var payload []byte
			if err := fresh.QueryRow(ctx, `SELECT jsonb_build_array(jsonb_build_object('workoutId',$4::uuid,'eventId',$5::uuid,
			 'origin','normal','contentSha256',encode(snapshot.content_sha256,'hex'),'routeDigest',encode(snapshot.route_input_sha256,'hex'),
			 'ownershipEpoch',snapshot.ownership_epoch,'readiness','map_data_ready','reason',NULL,
			 'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb,'catalogFingerprint',$6::text))
			 FROM app.read_ingest_coverage_route_snapshot($1,NULL,'origin-cleanup',$2,$3,$4,$5,'normal') snapshot`,
				f.account, recoveryLease, revision, f.workouts[0], currentEvent, pipelineCatalogProof).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if _, err := fresh.Exec(ctx, pipelineApplySQL, f.account, nil, "origin-cleanup", recoveryLease, revision, payload); err != nil {
				t.Fatal(err)
			}
			pipelineSealPrepared(t, db, fresh, f.account, nil, "origin-cleanup", recoveryLease, revision)
			if err := fresh.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account) != 1 {
				t.Fatal("fresh terminal obligation did not recover exactly once")
			}
			if !callBool(t, ctx, db.coverage, f.account, `SELECT app.advance_coverage_reconciliation($1,$2,'origin-cleanup',$3,NULL,true,interval '1 day')`, f.account, revision, recoveryLease) {
				t.Fatal("release preserved account")
			}
		})
	}
	t.Run("RetiredAdoptedOriginCannotClearNewForceEvent", func(t *testing.T) {
		f, job, lease := pipelineSource(t, db, 1)
		pipelineRecord(t, db, f, job, lease, f.workouts, "created")
		pipelineApply(t, db, f, job, lease, pipelineRead(t, db, f, job, lease, false), "map_data_ready")
		var source uuid.UUID
		if err := admin.QueryRow(ctx, `SELECT source_id FROM app.workouts WHERE id=$1`, f.workouts[0]).Scan(&source); err != nil {
			t.Fatal(err)
		}
		if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, job, lease) {
			t.Fatal("finish first owner")
		}
		if !callBool(t, ctx, db.migration, f.account, `SELECT app.delete_source($1,$2)`, source, uuid.New()) {
			t.Fatal("delete first owner")
		}
		var oldOrigin, newOrigin, oldForce, newForce uuid.UUID
		for stage := 0; stage < 2; stage++ {
			root := insertQueued(t, ctx, db.api, f.account, uuid.Nil, "manual_ingest")
			adopting := insertQueued(t, ctx, db.api, f.account, root, "manual_ingest_source")
			token := uuid.New()
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.claim_job($1,'pipeline-test',$2,interval '15 minutes')`, adopting, token) {
				t.Fatal("claim real adopting owner")
			}
			event := record(f, adopting, token, "adopted", bootstrapDigest())
			var force uuid.UUID
			if err := admin.QueryRow(ctx, `SELECT event_id FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.account).Scan(&force); err != nil {
				t.Fatal(err)
			}
			if stage == 1 {
				newOrigin, newForce = event, force
				break
			}
			oldOrigin, oldForce = event, force
			pipelineApply(t, db, f, adopting, token, pipelineRead(t, db, f, adopting, token, false), "map_data_ready")
			if !callBool(t, ctx, db.worker, f.account, `SELECT app.finish_job($1,'pipeline-test',$2,'succeeded',NULL,NULL)`, adopting, token) {
				t.Fatal("finish held force origin")
			}
			if err := admin.QueryRow(ctx, `SELECT source_id FROM app.workouts WHERE id=$1`, f.workouts[0]).Scan(&source); err != nil {
				t.Fatal(err)
			}
			if !callBool(t, ctx, db.migration, f.account, `SELECT app.delete_source($1,$2)`, source, uuid.New()) {
				t.Fatal("delete held force owner")
			}
		}
		recoveryLease := uuid.New()
		var account uuid.UUID
		var revision int64
		var cursor *uuid.UUID
		if err := db.coverage.QueryRow(ctx, `SELECT * FROM app.claim_coverage_reconciliation('origin-cleanup',$1,interval '1 minute',28)`, recoveryLease).Scan(&account, &revision, &cursor); err != nil || account != f.account {
			t.Fatalf("discover old force: %s %v", account, err)
		}
		tx := beginAccount(t, ctx, db.coverage, f.account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT * FROM app.read_ingest_coverage_prepared($1,NULL,'origin-cleanup',$2,$3,1)`, f.account, recoveryLease, revision); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND outcome='superseded' AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NULL`, oldOrigin) != 1 {
			t.Fatal("old force handoff leaked")
		}
		if count(`SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE event_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NULL`, oldForce) != 1 {
			t.Fatal("old force receipt leaked")
		}
		if count(`SELECT count(*) FROM app.ingest_coverage_handoffs WHERE event_id=$1 AND acknowledged_at IS NULL`, newOrigin) != 1 || count(`SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE event_id=$1`, newForce) != 1 || count(`SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE event_id=$1 AND acknowledged_at IS NULL`, newForce) != 1 {
			t.Fatal("cleanup consumed a different force event")
		}
		if count(`SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.account) != 0 || count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account) != 0 {
			t.Fatal("invalid force prepared/matching work survived cleanup")
		}
	})
}
