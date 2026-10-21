package worker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// One real producer root exercises eligible fill, mixed-region admission,
// existential retry eligibility/all-failed membership, and terminal-only tail.
func TestEligibleCohortCombinedLifecycleIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	apiConfig, err := parseCoverageAcceptanceConfig(os.Getenv("API_DATABASE_URL"), f.adminDB.Config().ConnConfig.Database)
	if err != nil || apiConfig.ConnConfig.Host != f.adminDB.Config().ConnConfig.Host || apiConfig.ConnConfig.Port != f.adminDB.Config().ConnConfig.Port {
		t.Fatalf("explicit paired API fixture URL required: %v", err)
	}
	f.apiDB, err = pgxpool.NewWithConfig(f.ctx, apiConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.apiDB.Close)
	var role string
	if err := f.apiDB.QueryRow(f.ctx, `SELECT current_user`).Scan(&role); err != nil || role != "workouts_api" {
		t.Fatalf("API scope role=%s: %v", role, err)
	}
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	otherRegion, _ := f.seedOSM()
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true,boundary=ST_Translate(boundary,20,0) WHERE id=$1`, otherRegion); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 25)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 25, 3)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-03.json"), 28, 13)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("producer claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `UPDATE app.workout_route_points point SET latitude=0,longitude=0 FROM app.workouts workout
	 WHERE workout.account_id=$1 AND workout.id=point.workout_id AND workout.provider_id IN
	 ('pipeline-synthetic-0','pipeline-synthetic-1','pipeline-synthetic-2')`, f.accountID)
	f.exec(tx, `UPDATE app.workout_route_points point SET longitude=longitude+20 FROM app.workouts workout
	 WHERE workout.account_id=$1 AND workout.id=point.workout_id AND substring(workout.provider_id FROM '[0-9]+$')::integer BETWEEN 14 AND 24`, f.accountID)
	f.commit(tx)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
		t.Fatalf("22 eligible targets created a placeholder: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 22, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	commitEligibleCohortFile(t, f, producer, job, source, 1)
	tx = f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `UPDATE app.workout_route_points point SET longitude=longitude+20 FROM app.workouts workout
	 WHERE workout.account_id=$1 AND workout.id=point.workout_id AND workout.provider_id='pipeline-synthetic-27' AND point.sequence=1`, f.accountID)
	f.commit(tx)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25}) {
		t.Fatalf("three fresh eligible targets did not fill one global25: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running'`, jobs.parentID)
	wantRegions := []string{f.regionID, otherRegion}
	slices.Sort(wantRegions)
	checkScope := func(parent uuid.UUID) {
		t.Helper()
		read := f.accountTx(f.ctx, f.apiDB)
		defer coverageIntegrationRollback(read)
		var regions []string
		var mixed bool
		f.scan(read, `SELECT * FROM app.read_coverage_job_scope($1)`, []any{parent}, &regions, &mixed)
		if !mixed || !slices.Equal(regions, wantRegions) {
			t.Fatalf("API parent scope=%v mixed=%v, want full %v", regions, mixed, wantRegions)
		}
	}
	matcher := NewCoverageRunner(f.coverageDB, f.osmDB, producer.logger)
	var firstParent uuid.UUID
	seenRegions := map[string]bool{}
	sawMultiRegion := false
	for i := 0; i < 25; i++ {
		claim, found, err := matcher.claim(f.ctx)
		if err != nil || !found {
			t.Fatalf("actual full-parent claim%d: %v", i, err)
		}
		if firstParent != uuid.Nil && claim.parentID != firstParent {
			t.Fatal("mixed regions fragmented the eligible parent")
		}
		firstParent = claim.parentID
		childRegions := make([]string, len(claim.targetGenerations))
		for i, target := range claim.targetGenerations {
			childRegions[i] = target.RegionID
			seenRegions[target.RegionID] = true
		}
		slices.Sort(childRegions)
		if len(childRegions) == 2 {
			sawMultiRegion = true
		}
		if len(childRegions) < 1 || len(childRegions) > 2 {
			t.Fatalf("invalid child vector: %+v", claim.targetGenerations)
		}
		slot := uuid.New()
		if acquired, err := matcher.acquireSlot(f.ctx, claim, slot); err != nil || !acquired {
			t.Fatalf("unchanged global admission limits: %v", err)
		}
		var admitted []string
		if err := f.adminDB.QueryRow(f.ctx, `SELECT region_ids FROM app.matcher_slots WHERE slot_token=$1`, slot).Scan(&admitted); err != nil {
			t.Fatal(err)
		}
		slices.Sort(admitted)
		if !slices.Equal(admitted, childRegions) {
			t.Fatalf("slot lost child vector: %v", admitted)
		}
		if err := matcher.releaseSlotDetached(claim, slot); err != nil {
			t.Fatal(err)
		}
		code := "coverage-route-too-large"
		if i == 0 {
			code = "coverage-transient-failed"
		}
		if err := matcher.failDetached(claim, time.Now(), code, "Synthetic combined-cohort failure."); err != nil {
			t.Fatal(err)
		}
	}
	if len(seenRegions) != 2 || !sawMultiRegion {
		t.Fatalf("fixture did not exercise global mixed and multi-region children: regions=%v multi=%v", seenRegions, sawMultiRegion)
	}
	checkScope(firstParent)
	tx = f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `UPDATE app.jobs SET terminal_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, firstParent)
	f.commit(tx)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
		t.Fatal(err)
	}
	var retry uuid.UUID
	var count int
	if err := f.coverageDB.QueryRow(f.ctx, `SELECT * FROM app.retry_next_coverage_update(1,28)`).Scan(&retry, &count); err != nil || count != 25 {
		t.Fatalf("one nonpermanent failure must carry all25 failed siblings: count=%d err=%v", count, err)
	}
	checkScope(retry)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_job_contexts WHERE job_id=$1 AND parent_batch_size=25 AND sealed_ingest_root_id=$2`, retry, jobs.parentID)
	assertCountQuery(t, f.adminDB, 25, `SELECT count(*) FROM app.jobs child JOIN app.jobs prior ON prior.id=child.retry_of_job_id
	 WHERE child.parent_job_id=$1 AND prior.parent_job_id=$2 AND prior.status='failed'`, retry, firstParent)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, firstParent)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 2)
	if err := producer.drainIngestCoverage(f.ctx, job, true); err != nil {
		t.Fatal(err)
	}
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25, 25}) {
		t.Fatalf("active source EOF sealed the13 tail: %v", sizes)
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	if finished, err := producer.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
		t.Fatalf("source terminal commit: %v", err)
	}
	pipelineRecoverTerminalRoot(t, f, producer, jobs.parentID)
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25, 25, 13}) {
		t.Fatalf("terminal account recovery cohort sizes=%v", sizes)
	}
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND outcome='unavailable' AND coverage_child_job_id IS NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, firstParent)
}
