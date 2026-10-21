package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func commitEligibleCohortFile(t *testing.T, f *coverageIntegrationFixture, runner *Runner, job claimedJob, source ingestSourceFixture, index int) {
	t.Helper()
	directory, err := os.Open(source.root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := discoverSourceFiles(directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[index], nil); outcome.fatal != nil || !outcome.succeeded {
		t.Fatalf("commit synthetic cohort file: %+v", outcome)
	}
}

func traceEligibleCohortOSM(t *testing.T, f *coverageIntegrationFixture, appDB *pgxpool.Pool, lookups *atomic.Int64) *pgxpool.Pool {
	t.Helper()
	cfg := f.osmDB.Config()
	cfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if appDB.Stat().AcquiredConns() != 0 {
			t.Error("APP checkout retained during OSM readiness or prepared-vector validation")
		}
		if strings.Contains(query.SQL, "WITH route_point AS") {
			lookups.Add(1)
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE account_id=$1`, f.accountID)
		}
		return ctx
	})
	db, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// Cohort replay must reuse prepared ROI. The following ordinary reconciliation
// scan still resolves ROI, including newly configured overlapping regions.
func traceEligibleCohortRecovery(t *testing.T, f *coverageIntegrationFixture, lookups *atomic.Int64) (*pgxpool.Pool, *pgxpool.Pool, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var ordinary atomic.Bool
	ordinaryLookups, sourceSnapshots := new(atomic.Int64), new(atomic.Int64)
	appConfig := f.coverageDB.Config()
	appConfig.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if strings.Contains(query.SQL, "app.read_coverage_reconciliation_routes(") {
			ordinary.Store(true)
		}
		if strings.Contains(query.SQL, "app.read_ingest_coverage_prepared(") || strings.Contains(query.SQL, "app.read_ingest_coverage_batch(") {
			ordinary.Store(false)
		}
		if strings.Contains(query.SQL, "app.read_ingest_coverage_route_snapshot(") {
			sourceSnapshots.Add(1)
		}
		return ctx
	})
	appDB, err := pgxpool.NewWithConfig(f.ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appDB.Close)
	osmConfig := f.osmDB.Config()
	osmConfig.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if appDB.Stat().AcquiredConns() != 0 {
			t.Error("APP checkout retained during cohort or ordinary OSM query")
		}
		if strings.Contains(query.SQL, "WITH route_point AS") {
			if ordinary.Load() {
				ordinaryLookups.Add(1)
			} else {
				lookups.Add(1)
			}
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE account_id=$1`, f.accountID)
		}
		return ctx
	})
	osmDB, err := pgxpool.NewWithConfig(f.ctx, osmConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(osmDB.Close)
	return appDB, osmDB, ordinaryLookups, sourceSnapshots
}

func TestEligibleCohortCountsTargetsNotCandidatesIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 25)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 25, 3)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `UPDATE app.workout_route_points point SET latitude=0,longitude=0
		FROM app.workouts workout WHERE workout.id=point.workout_id AND workout.account_id=$1
		AND workout.provider_id IN ('pipeline-synthetic-0','pipeline-synthetic-1','pipeline-synthetic-2')`, f.accountID)
	f.commit(tx)
	var lookups atomic.Int64
	runner.osmDB = traceEligibleCohortOSM(t, f, runner.db, &lookups)
	if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
		t.Fatalf("25 candidates, only 22 eligible, created parents: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 22, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND outcome='unavailable'`, f.accountID)
	// Replaying an underfull builder is a no-op, not 22 fresh ROI queries.
	if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 25 {
		t.Fatalf("underfull replay reran prepared ROI: %d", lookups.Load())
	}
	commitEligibleCohortFile(t, f, runner, job, source, 1)
	if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25}) || lookups.Load() != 28 {
		t.Fatalf("next three must seal exactly 25 actual eligible targets: sizes=%v ROI=%d", sizes, lookups.Load())
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running'`, jobs.parentID)
	coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
	if claim, found, err := coverageRunner.claim(f.ctx); err != nil || !found || claim.accountID != f.accountID {
		t.Fatalf("full cohort not claimable during ingest: found=%v err=%v", found, err)
	}
}

func TestEligibleCohortTailAfterTerminalCommitIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 63)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	cfg := runner.db.Config()
	var finalizations atomic.Int64
	cfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if strings.Contains(query.SQL, "app.finish_job(") {
			finalizations.Add(1)
			if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25, 25}) {
				t.Fatalf("EOF/repair sealed a tail before source terminal commit: %v", sizes)
			}
		}
		return ctx
	})
	db, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	runner.db = db
	assertRanIngest(t, runner, f.ctx)
	if finalizations.Load() != 1 {
		t.Fatalf("source finalizations=%d", finalizations.Load())
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25, 25}) {
		t.Fatalf("source lease sealed final 13 instead of leaving recovery: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE id=ANY($1::uuid[]) AND status='succeeded'`, []uuid.UUID{jobs.parentID, jobs.childID})
	var lookups atomic.Int64
	appDB, osmDB, ordinaryLookups, sourceSnapshots := traceEligibleCohortRecovery(t, f, &lookups)
	reconciler := NewCoverageReconciler(appDB, osmDB, runner.logger, CoverageReconcilerOptions{})
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("prepared-only terminal recovery: worked=%v err=%v", worked, err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25, 25, 13}) || lookups.Load() != 0 {
		t.Fatalf("terminal tail must reuse prepared ROI: sizes=%v ROI=%d", sizes, lookups.Load())
	}
	if ordinaryLookups.Load() != 63 || sourceSnapshots.Load() != 0 {
		t.Fatalf("ordinary/replayed-source reads=%d/%d, want63/0", ordinaryLookups.Load(), sourceSnapshots.Load())
	}
}

func TestEligibleCohortAllSourceSiblingsIntegration(t *testing.T) {
	for _, secondCount := range []int{0, 12} {
		t.Run(fmt.Sprintf("secondRoutes%d", secondCount), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
				t.Fatal(err)
			}
			other := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, t.TempDir())
			runner.localRoots = append(runner.localRoots, other.root)
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
			writePipelineExport(t, filepath.Join(other.root, "HealthAutoExport-2026-08-01.json"), 13, secondCount)
			first := enqueueIngestJob(t, f.adminDB, keys, source)
			second := enqueueIngestJob(t, f.adminDB, keys, other)
			// Build one visible root with two independently fenced source children.
			tx := f.accountTx(f.ctx, f.adminDB)
			f.exec(tx, `SET LOCAL session_replication_role='replica'`)
			f.exec(tx, `UPDATE app.jobs SET parent_job_id=$2 WHERE id=$1`, second.childID, first.parentID)
			f.exec(tx, `DELETE FROM app.job_progress WHERE job_id=$1`, second.parentID)
			f.exec(tx, `DELETE FROM app.jobs WHERE id=$1`, second.parentID)
			f.commit(tx)
			assertRanIngest(t, runner, f.ctx)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='succeeded'`, first.childID)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status IN ('queued','running')`, first.parentID)
			reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
			if _, err := reconciler.RunOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
			if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
				t.Fatalf("terminal source child leaked tail while sibling active: %v", sizes)
			}
			assertRanIngest(t, runner, f.ctx)
			want := []int{25}
			if secondCount == 0 {
				if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
					t.Fatalf("source EOF sealed partial: %v", sizes)
				}
				want = []int{13}
				if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
					t.Fatalf("all siblings terminal: worked=%v err=%v", worked, err)
				}
			}
			if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, want) {
				t.Fatalf("root-wide eligible cohort: sizes=%v want=%v", sizes, want)
			}
		})
	}
}

func TestEligibleCohortPreparedSurvivesNewLeaseIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	if !workerCallBool(t, f.workerDB, f.accountID, `SELECT app.heartbeat_job($1,$2,$3,interval '1 millisecond')`, job.id, runner.workerID, job.lease) {
		t.Fatal("shorten old source lease")
	}
	time.Sleep(10 * time.Millisecond)
	replacement := testRunner(f.workerDB, keys, source.root, io.Discard)
	var lookups atomic.Int64
	replacement.osmDB = traceEligibleCohortOSM(t, f, replacement.db, &lookups)
	current, err := replacement.claimNext(f.ctx)
	if err != nil || current.id != job.id || current.lease == job.lease {
		t.Fatalf("replacement claim: %v", err)
	}
	if err := replacement.drainIngestCoverage(f.ctx, current, false); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 0 || len(pipelineParentSizes(t, f)) != 0 {
		t.Fatal("new lease reran prepared ROI or prematurely sealed partial")
	}
	if finished, err := replacement.finishJob(f.ctx, current, "failed", &ingestFailure{"source-unavailable", "Synthetic source failure."}); err != nil || !finished {
		t.Fatalf("terminal failed source: %v", err)
	}
	appDB, osmDB, ordinaryLookups, sourceSnapshots := traceEligibleCohortRecovery(t, f, &lookups)
	reconciler := NewCoverageReconciler(appDB, osmDB, runner.logger, CoverageReconcilerOptions{})
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("failed-source prepared recovery: worked=%v err=%v", worked, err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{13}) || lookups.Load() != 0 {
		t.Fatalf("failed-source tail must retain prepared cache: sizes=%v ROI=%d", sizes, lookups.Load())
	}
	if ordinaryLookups.Load() != 13 || sourceSnapshots.Load() != 0 {
		t.Fatalf("ordinary/replayed-source reads=%d/%d, want13/0", ordinaryLookups.Load(), sourceSnapshots.Load())
	}
}

func TestEligibleCohortMixedRegionsSingleParentIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	otherRegion, otherGeneration := f.seedOSM()
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true,
		boundary=ST_Translate(boundary,20,0) WHERE id=$1`, otherRegion); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 25)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `UPDATE app.workout_route_points point SET longitude=longitude+20
		FROM app.workouts workout WHERE workout.id=point.workout_id AND workout.account_id=$1
		AND (substring(workout.provider_id FROM '[0-9]+$')::integer BETWEEN 12 AND 23
			OR (workout.provider_id='pipeline-synthetic-24' AND point.sequence=1))`, f.accountID)
	f.commit(tx)
	if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25}) {
		t.Fatalf("mixed regions fragmented global cohort: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.matcher_slots WHERE account_id=$1`, f.accountID)
	coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
	seen := make(map[uuid.UUID]bool)
	var parentID uuid.UUID
	for range 25 {
		claim, found, err := coverageRunner.claim(f.ctx)
		if err != nil || !found || seen[claim.workoutID] || claim.accountID != f.accountID {
			t.Fatalf("actual child claim: found=%v err=%v", found, err)
		}
		seen[claim.workoutID] = true
		if parentID != uuid.Nil && claim.parentID != parentID {
			t.Fatal("region-specific parents replaced one global eligible cohort")
		}
		parentID = claim.parentID
		var provider string
		if err := f.adminDB.QueryRow(f.ctx, `SELECT provider_id FROM app.workouts WHERE account_id=$1 AND id=$2`, f.accountID, claim.workoutID).Scan(&provider); err != nil {
			t.Fatal(err)
		}
		var index int
		if _, err := fmt.Sscanf(provider, "pipeline-synthetic-%d", &index); err != nil {
			t.Fatal(err)
		}
		want := map[string]int64{f.regionID: f.generation}
		if index >= 12 && index <= 23 {
			want = map[string]int64{otherRegion: otherGeneration}
		} else if index == 24 {
			want[otherRegion] = otherGeneration
		}
		if len(claim.targetGenerations) != len(want) {
			t.Fatalf("child lost its complete vector: %+v want=%v", claim.targetGenerations, want)
		}
		for _, region := range claim.targetGenerations {
			if want[region.RegionID] != region.Generation {
				t.Fatalf("parent anchor overrode child vector: %+v want=%v", claim.targetGenerations, want)
			}
		}
		slot := uuid.New()
		if acquired, err := coverageRunner.acquireSlot(f.ctx, claim, slot); err != nil || !acquired {
			t.Fatalf("child-vector admission: acquired=%v err=%v", acquired, err)
		}
		var admitted []string
		if err := f.adminDB.QueryRow(f.ctx, `SELECT region_ids FROM app.matcher_slots WHERE account_id=$1 AND slot_token=$2`, f.accountID, slot).Scan(&admitted); err != nil {
			t.Fatal(err)
		}
		if len(admitted) != len(want) {
			t.Fatalf("admission omitted child vector regions: got=%v want=%v", admitted, want)
		}
		for _, region := range admitted {
			if _, exists := want[region]; !exists {
				t.Fatalf("admission used the parent anchor rather than the child vector: %v", admitted)
			}
		}
		if err := coverageRunner.releaseSlotDetached(claim, slot); err != nil {
			t.Fatal(err)
		}
	}
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.matcher_slots WHERE account_id=$1`, f.accountID)
}

func TestEligibleCohortStalePreparedVectorsRecomputeROIIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	region, generation := f.seedOSM()
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true WHERE id=$1`, region); err != nil {
		t.Fatal(err)
	}
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	if finished, err := runner.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
		t.Fatalf("finish: %v", err)
	}
	var lookups atomic.Int64
	appDB, osmDB, ordinaryLookups, sourceSnapshots := traceEligibleCohortRecovery(t, f, &lookups)
	reconciler := NewCoverageReconciler(appDB, osmDB, runner.logger, CoverageReconcilerOptions{})
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("stale prepared recovery: worked=%v err=%v", worked, err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{13}) || lookups.Load() != 13 {
		t.Fatalf("stale vectors must invalidate before fresh ROI: sizes=%v ROI=%d", sizes, lookups.Load())
	}
	if ordinaryLookups.Load() != 13 || sourceSnapshots.Load() != 13 {
		t.Fatalf("ordinary/recomputed-source reads=%d/%d, want13/13", ordinaryLookups.Load(), sourceSnapshots.Load())
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.coverage_route_job_contexts
		WHERE account_id=$1 AND target_generations=jsonb_build_array(jsonb_build_object('regionId',$2::text,'generation',$3::bigint))`, f.accountID, region, generation)
}

func TestEligibleCohortCapacityIndependentOfPageIntegration(t *testing.T) {
	for _, capacity := range []int{1, 1000} {
		t.Run(fmt.Sprintf("B%d", capacity), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, capacity); err != nil {
				t.Fatal(err)
			}
			// This is intentionally NOT the builder capacity.
			runner.coverageBatchSize = 25
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, capacity)
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := runner.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			commitEligibleCohortFile(t, f, runner, job, source, 0)
			if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
				t.Fatal(err)
			}
			if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{capacity}) {
				t.Fatalf("metadata page controlled parent capacity: sizes=%v want=%d", sizes, capacity)
			}
		})
	}
}

func TestEligibleCohortOwnedSourceRepairCannotBypassBuilderIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	// Model retained owned routes whose original handoff was not recorded by an
	// older producer. Repair must not manufacture import events or singleton jobs.
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `DELETE FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.accountID)
	f.commit(tx)
	if err := runner.repairCoverageReadiness(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
		t.Fatalf("owned-source EOF repair bypassed eligible builder: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.workout_import_events WHERE account_id=$1`, f.accountID)
	if finished, err := runner.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
		t.Fatalf("finish: %v", err)
	}
	reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("owned repair terminal recovery: worked=%v err=%v", worked, err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{13}) {
		t.Fatalf("owned repair terminal tail: %v", sizes)
	}
}
