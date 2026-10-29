package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCoverageMetadataObservationMonotonicIntegration(t *testing.T) {
	f, producer, _, _ := newCoveragePipelineFixture(t)
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{})
	if err := r.observe(f.ctx); err != nil {
		t.Fatal(err)
	}
	catalog, err := osm.ReadRouteRegionCatalog(f.ctx, f.osmDB, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	heads, _ := json.Marshal(catalog.ActiveGenerations)
	configured, _ := json.Marshal(catalog.ConfiguredRegions)
	descriptor, _ := json.Marshal(productionMatchingDescriptor(6))
	state := func() string {
		t.Helper()
		var value string
		if err := f.adminDB.QueryRow(f.ctx, `SELECT row_to_json(state)::text||xmin::text FROM app.coverage_reconciliation_state state WHERE singleton`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := state()
	var accepted bool
	if err := f.coverageDB.QueryRow(f.ctx, `SELECT app.observe_coverage_catalog($1,$2,$3,$4,$5,$6,$7,$8)`, catalog.Revision-1,
		catalog.PromotionEventID, strings.Repeat("a", 64), catalog.SelectionFingerprint, heads, coverageSelectionPolicy(true), descriptor, configured).Scan(&accepted); err != nil || accepted {
		t.Fatalf("lagged catalog accepted or failed destructively: accepted=%t err=%v", accepted, err)
	}
	if state() != before {
		t.Fatal("lagged catalog discarded accepted heads/policy/descriptor/progress")
	}
	err = f.coverageDB.QueryRow(f.ctx, `SELECT app.observe_coverage_catalog($1,$2,$3,$4,$5,$6,$7,$8)`, catalog.Revision,
		catalog.PromotionEventID, strings.Repeat("f", 64), catalog.SelectionFingerprint, heads, coverageSelectionPolicy(false), descriptor, configured).Scan(&accepted)
	var sql interface{ SQLState() string }
	if !errors.As(err, &sql) || sql.SQLState() != "40001" {
		t.Fatalf("equal revision/conflicting fingerprint was not rejected: %v", err)
	}
	if state() != before {
		t.Fatal("conflicting equal revision changed accepted observation")
	}
}

// Uses only the existing explicitly disposable, synthetic paired APP/OSM harness.
func traceCoverageMetadata(t *testing.T, f *coverageIntegrationFixture) (*pgxpool.Pool, *pgxpool.Pool, *atomic.Int64, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	snapshots, roi, heads := new(atomic.Int64), new(atomic.Int64), new(atomic.Int64)
	appConfig := f.coverageDB.Config()
	appConfig.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, q pgx.TraceQueryStartData) context.Context {
		if strings.Contains(q.SQL, "app.read_coverage_reconciliation_snapshot(") || strings.Contains(q.SQL, "app.read_ingest_coverage_route_snapshot(") {
			snapshots.Add(1)
		}
		return ctx
	})
	appDB, err := pgxpool.NewWithConfig(f.ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appDB.Close)
	osmConfig := f.osmDB.Config()
	osmConfig.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, q pgx.TraceQueryStartData) context.Context {
		if strings.Contains(q.SQL, "WITH route_point AS") {
			roi.Add(1)
		}
		if strings.Contains(q.SQL, "osm_catalog.route_region_catalog_state") {
			heads.Add(1)
		}
		return ctx
	})
	osmDB, err := pgxpool.NewWithConfig(f.ctx, osmConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(osmDB.Close)
	return appDB, osmDB, snapshots, roi, heads
}

func TestCoverageMetadataUnchangedBeyondDailyIntervalIntegration(t *testing.T) {
	for _, negative := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "negative"}[negative], func(t *testing.T) {
			f, producer, source, keys := newCoveragePipelineFixture(t)
			if negative {
				if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
					t.Fatal(err)
				}
			}
			var initialROI atomic.Int64
			producer.osmDB = traceEligibleCohortOSM(t, f, producer.db, &initialROI)
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
			enqueueIngestJob(t, f.adminDB, keys, source)
			assertRanIngest(t, producer, f.ctx)
			if initialROI.Load() != 2 {
				t.Fatalf("first import/EOF repeated ROI: got %d want 2", initialROI.Load())
			}
			assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.coverage_route_selection_proofs WHERE account_id=$1`, f.accountID)
			appDB, osmDB, snapshots, roi, heads := traceCoverageMetadata(t, f)
			r := NewCoverageReconciler(appDB, osmDB, producer.logger, CoverageReconcilerOptions{})
			if _, err := r.RunOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
			if snapshots.Load() != 0 || roi.Load() != 0 {
				t.Fatal("first reconciliation duplicated resolved ingest GPS work")
			}
			observed := heads.Load()
			for range 3 {
				if _, err := r.RunOnce(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			if heads.Load() != observed {
				t.Fatal("busy RunOnce loop observed OSM more than once per poll interval")
			}
			if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET next_scan_at=clock_timestamp()-interval '25 hours'
				WHERE account_id=$1`, f.accountID); err != nil {
				t.Fatal(err)
			}
			var before, after string
			durableState := func() string {
				t.Helper()
				var value string
				if err := f.adminDB.QueryRow(f.ctx, `SELECT jsonb_build_array(
				 (SELECT array_agg(xmin::text ORDER BY workout_id) FROM app.workout_coverage_states WHERE account_id=$1),
				 (SELECT array_agg(xmin::text ORDER BY workout_id) FROM app.coverage_route_selection_proofs WHERE account_id=$1),
				 (SELECT array_agg(xmin::text ORDER BY workout_id) FROM app.coverage_route_changes WHERE account_id=$1),
				 (SELECT array_agg(xmin::text ORDER BY transaction_id) FROM app.coverage_reconciliation_wakes WHERE account_id=$1),
				 (SELECT xmin::text FROM app.coverage_reconciliation_accounts WHERE account_id=$1),
				 (SELECT array_agg(xmin::text ORDER BY id) FROM app.jobs WHERE account_id=$1),
				 (SELECT count(*) FROM app.job_events WHERE account_id=$1 AND code LIKE 'ingest-phase-%'))::text`, f.accountID).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			beforeDurable := durableState()
			if err := f.adminDB.QueryRow(f.ctx, `SELECT xmin::text FROM app.coverage_reconciliation_state WHERE singleton`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				r.nextObservation = time.Time{}
				if worked, err := r.RunOnce(f.ctx); err != nil || worked {
					t.Fatalf("unchanged expired daily timer claimed work: %t %v", worked, err)
				}
			}
			if err := f.adminDB.QueryRow(f.ctx, `SELECT xmin::text FROM app.coverage_reconciliation_state WHERE singleton`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after || snapshots.Load() != 0 || roi.Load() != 0 {
				t.Fatalf("idle mutated observation or scanned GPS: xmin %s/%s snapshots=%d ROI=%d", before, after, snapshots.Load(), roi.Load())
			}
			if durableState() != beforeDurable {
				t.Fatal("idle/daily-expired polling wrote route proofs, queue state, jobs, or required phase events")
			}
		})
	}
}

func TestCoverageMetadataSameBoundsPointMutationIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	var workout uuid.UUID
	if err := f.adminDB.QueryRow(f.ctx, `SELECT workout_id FROM app.coverage_route_selection_proofs WHERE account_id=$1 ORDER BY workout_id LIMIT 1`, f.accountID).Scan(&workout); err != nil {
		t.Fatal(err)
	}
	tx, _, err := producer.beginIngestWrite(f.ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	// Coordinates and point count are untouched; only canonical input changes.
	if _, err := tx.Exec(f.ctx, `UPDATE app.workout_route_points SET horizontal_accuracy=COALESCE(horizontal_accuracy,0)+1
		WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_changes change JOIN app.coverage_route_selection_proofs proof
		USING(account_id,workout_id) WHERE change.account_id=$1 AND change.workout_id=$2
		AND change.mutation_version>proof.mutation_version AND change.mutation_version>change.resolved_version`, f.accountID, workout)
	if finished, err := producer.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
		t.Fatalf("finish: %v", err)
	}
	appDB, osmDB, snapshots, roi, _ := traceCoverageMetadata(t, f)
	r := NewCoverageReconciler(appDB, osmDB, producer.logger, CoverageReconcilerOptions{})
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if snapshots.Load() != 1 || roi.Load() != 1 {
		t.Fatalf("point mutation reused stale proof or rechecked unrelated route: snapshots=%d ROI=%d", snapshots.Load(), roi.Load())
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_changes change JOIN app.coverage_route_selection_proofs proof
		USING(account_id,workout_id) WHERE change.account_id=$1 AND change.workout_id=$2
		AND change.mutation_version=proof.mutation_version AND change.mutation_version=change.resolved_version`, f.accountID, workout)
}

func TestCoverageMetadataChangedTargetsIntegration(t *testing.T) {
	for _, change := range []string{"generation", "minimum traversal", "boundary", "maps become available"} {
		t.Run(change, func(t *testing.T) {
			f, producer, source, keys := newCoveragePipelineFixture(t)
			if change == "maps become available" {
				if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
					t.Fatal(err)
				}
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
			enqueueIngestJob(t, f.adminDB, keys, source)
			assertRanIngest(t, producer, f.ctx)
			appDB, osmDB, snapshots, roi, _ := traceCoverageMetadata(t, f)
			r := NewCoverageReconciler(appDB, osmDB, producer.logger, CoverageReconcilerOptions{})
			if _, err := r.RunOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
			beforeJobs := len(pipelineParentSizes(t, f))
			generation := f.generation
			switch change {
			case "generation":
				if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&generation); err != nil {
					t.Fatal(err)
				}
				replicaFixtureHead(f, generation, false)
			case "minimum traversal":
				r.minimumTraversal = 6
			case "boundary":
				if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET boundary=ST_Multi(ST_Buffer(boundary,0.001)) WHERE id=$1`, f.regionID); err != nil {
					t.Fatal(err)
				}
			case "maps become available":
				replicaFixtureCatchUp(f)
			}
			r.nextObservation = time.Time{}
			if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
				t.Fatalf("changed target not reconciled: %t %v", worked, err)
			}
			wantROI := int64(0)
			if change == "boundary" || change == "maps become available" {
				wantROI = 2
			}
			if snapshots.Load() != wantROI || roi.Load() != wantROI {
				t.Fatalf("%s: GPS snapshots=%d ROI=%d want %d", change, snapshots.Load(), roi.Load(), wantROI)
			}
			if change == "boundary" && len(pipelineParentSizes(t, f)) != beforeJobs {
				t.Fatal("same actual target was rematched after honest selection-proof revalidation")
			}
			minimum := float64(5)
			if change == "minimum traversal" {
				minimum = 6
			}
			assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.workout_coverage_states state JOIN app.coverage_route_job_contexts context
				ON context.job_id=state.target_job_id JOIN app.jobs child ON child.id=context.job_id
				JOIN app.coverage_job_contexts parent ON parent.job_id=child.parent_job_id
				WHERE state.account_id=$1 AND context.target_generations=jsonb_build_array(jsonb_build_object('regionId',$2::text,'generation',$3::bigint))
				AND parent.minimum_traversal_meters=$4`, f.accountID, f.regionID, generation, minimum)
		})
	}
}

func TestCoverageMetadataPreparedTailIsQueueOnlyIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	appDB, osmDB, snapshots, roi, _ := traceCoverageMetadata(t, f)
	r := NewCoverageReconciler(appDB, osmDB, producer.logger, CoverageReconcilerOptions{})
	// Finish bootstrap while the source still owns the underfull prepared cohort.
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	cursor := uuid.New()
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET cursor_workout_id=$2,next_scan_at='infinity',
		queue_retry_at=clock_timestamp()+interval '1 day' WHERE account_id=$1`, f.accountID, cursor); err != nil {
		t.Fatal(err)
	}
	if finished, err := producer.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
		t.Fatalf("finish: %v", err)
	}
	claim, found, err := r.claim(f.ctx)
	if err != nil || !found || claim.scanRequired || claim.cursor == nil || *claim.cursor != cursor {
		t.Fatalf("terminal wake reset campaign or inherited backoff: claim=%+v found=%t err=%v", claim, found, err)
	}
	if err := r.process(f.ctx, claim); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1
		AND completed_revision=desired_revision AND cursor_workout_id=$2 AND scan_required=false AND worker_id IS NULL`, f.accountID, cursor)
	if snapshots.Load() != 0 || roi.Load() != 0 {
		t.Fatalf("prepared-only recovery entered GPS work: snapshots=%d ROI=%d", snapshots.Load(), roi.Load())
	}
}

func TestCoverageMetadataLateOldProducerUsesAcceptedTargetIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 13, 12)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	// The newer matching campaign can finish while the old source is still
	// building its underfull cohort. Later imports must not miss the new target.
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{MinimumTraversalMeters: 6})
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1
		AND completed_revision=desired_revision AND worker_id IS NULL`, f.accountID)
	commitEligibleCohortFile(t, f, producer, job, source, 1)
	var roi atomic.Int64
	producer.osmDB = traceEligibleCohortOSM(t, f, producer.db, &roi)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if roi.Load() != 12 {
		t.Fatalf("matcher-only cohort refresh repeated existing GPS: ROI=%d want 12 genuinely new routes", roi.Load())
	}
	assertCountQuery(t, f.adminDB, 25, `SELECT count(*) FROM app.jobs child JOIN app.coverage_job_contexts parent
		ON parent.job_id=child.parent_job_id AND parent.account_id=child.account_id
		WHERE child.account_id=$1 AND child.kind='coverage_update_route' AND parent.minimum_traversal_meters=6
		AND parent.matching_descriptor->>'minimumTraversalMeters'='6'`, f.accountID)
}

func TestCoverageMetadataOldProducerContractRejectedIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 13, 12)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	catalog, err := osm.ReadRouteRegionCatalog(f.ctx, f.osmDB, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	heads, _ := json.Marshal(catalog.ActiveGenerations)
	configured, _ := json.Marshal(catalog.ConfiguredRegions)
	newDescriptor := productionMatchingDescriptor(5)
	newDescriptor.Contract = "coverage-production-v2"
	descriptor, _ := json.Marshal(newDescriptor)
	var accepted bool
	if err := f.coverageDB.QueryRow(f.ctx, `SELECT app.observe_coverage_catalog($1,$2,$3,$4,$5,$6,$7,$8)`,
		catalog.Revision, catalog.PromotionEventID, catalog.Fingerprint, catalog.SelectionFingerprint, heads,
		coverageSelectionPolicy(false), descriptor, configured).Scan(&accepted); err != nil || !accepted {
		t.Fatalf("new contract observation: %t %v", accepted, err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 1)
	err = producer.drainIngestCoverage(f.ctx, job, false)
	var state interface{ SQLState() string }
	if !errors.As(err, &state) || state.SQLState() != "22023" {
		t.Fatalf("old producer must fail admission, not assert the new matcher contract: %v", err)
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 25, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.coverage_applied_match_proofs WHERE account_id=$1`, f.accountID)
}
