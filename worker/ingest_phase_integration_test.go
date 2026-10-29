package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests use the existing explicitly disposable paired fixture, already
// migrated through 030. They never run migrations or fall back to a live DSN.
func ingestPhaseCodes(t *testing.T, f *coverageIntegrationFixture, jobID uuid.UUID) []string {
	t.Helper()
	rows, err := f.adminDB.Query(f.ctx, `SELECT code FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%' ORDER BY id`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatal(err)
		}
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return codes
}

func assertLatestIngestPhase(t *testing.T, f *coverageIntegrationFixture, jobID uuid.UUID, phase string) {
	t.Helper()
	codes := ingestPhaseCodes(t, f, jobID)
	if len(codes) == 0 || codes[len(codes)-1] != "ingest-phase-"+phase {
		t.Fatalf("latest phase want=%s journal=%v", phase, codes)
	}
}

func TestIngestPhaseWriterIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID || job.attempt != 1 {
		t.Fatalf("first claim=%+v err=%v", job, err)
	}
	job.ingestPhase = &ingestPhaseProgress{}
	current := int64(7)
	for range 2 {
		if err := runner.recordIngestPhase(f.ctx, job, phaseAnalyzingImportedRoutes, &current, nil); err != nil {
			t.Fatal(err)
		}
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_events WHERE job_id=$1 AND code='ingest-phase-analyzing-imported-routes'
		AND fields='{"current":7,"attempt":1}'::jsonb AND safe_message='Analyzing map data for imported routes.'`, job.id)
	zero := int64(0)
	for _, invalid := range []*int64{nil, &zero} {
		err := runner.recordIngestPhase(f.ctx, job, phaseAnalyzingImportedRoutes, invalid, nil)
		var sql *pgconn.PgError
		if !errors.As(err, &sql) || sql.Code != "22023" {
			t.Fatalf("regression SQL error was ignored: %v", err)
		}
	}
	if err := runner.recordIngestPhase(f.ctx, job, phaseAnalyzingExistingRoutes, &zero, nil); err != nil {
		t.Fatal("new phase may reset:", err)
	}
	stale := job
	stale.lease = uuid.New()
	if err := runner.recordIngestPhase(f.ctx, stale, phaseCleanup, nil, nil); !errors.Is(err, errLeaseLost) {
		t.Fatalf("false fenced write must lose lease: %v", err)
	}
	if job.ingestPhase.phase != phaseAnalyzingExistingRoutes {
		t.Fatal("rejected write changed local phase")
	}
	if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.request_job_cancellation($1,$2)`, job.id, uuid.New()) {
		t.Fatal("cancel ingest")
	}
	if err := runner.recordIngestPhase(f.ctx, job, phaseCleanup, nil, nil); !errors.Is(err, errLeaseLost) {
		t.Fatalf("cancelled write must lose lease: %v", err)
	}
	if got := ingestPhaseCodes(t, f, job.id); len(got) != 2 {
		t.Fatalf("deduplication/rejected writes changed journal: %v", got)
	}
}

func TestIngestPhaseLocalBoundariesIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 2, 1)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	runner.beforeProcessFile = func(sourceFile) { assertLatestIngestPhase(t, f, jobs.childID, "importing") }
	cfg := f.osmDB.Config()
	lookups := 0
	cfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if strings.Contains(query.SQL, "WITH route_point AS") {
			assertLatestIngestPhase(t, f, jobs.childID, "analyzing-imported-routes")
			// The route being resolved must not be counted before its acknowledgment.
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM (SELECT fields FROM app.job_events
				WHERE job_id=$1 AND code='ingest-phase-analyzing-imported-routes' ORDER BY id DESC LIMIT 1) latest
				WHERE (fields->>'current')::bigint=$2 AND NOT fields ? 'total'`, jobs.childID, lookups)
			lookups++
		}
		if strings.Contains(query.SQL, "SELECT region_id,id FROM osm_catalog.generations") {
			assertLatestIngestPhase(t, f, jobs.childID, "analyzing-existing-routes")
		}
		if runner.db.Stat().AcquiredConns() != 0 {
			t.Error("APP checkout retained across OSM work")
		}
		return ctx
	})
	traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	runner.osmDB = traced
	workerCfg := f.workerDB.Config()
	workerCfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		switch {
		case strings.Contains(query.SQL, "read_ingest_candidate_actions"), strings.Contains(query.SQL, "record_ingest_file_manifest"):
			assertLatestIngestPhase(t, f, jobs.childID, "discovering")
		case strings.Contains(query.SQL, "reconcile_account_workout_timezones"):
			assertLatestIngestPhase(t, f, jobs.childID, "updating-time-zones")
		case strings.Contains(query.SQL, "app.finish_job"):
			assertLatestIngestPhase(t, f, jobs.childID, "cleanup")
		}
		return ctx
	})
	worker, err := pgxpool.NewWithConfig(f.ctx, workerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Close)
	runner.db = worker
	assertRanIngest(t, runner, f.ctx)
	if lookups != 3 {
		t.Fatalf("resolved routes=%d want=3", lookups)
	}
	codes := slices.Compact(ingestPhaseCodes(t, f, jobs.childID))
	want := []string{"ingest-phase-preparing", "ingest-phase-discovering", "ingest-phase-importing",
		"ingest-phase-analyzing-imported-routes", "ingest-phase-importing", "ingest-phase-analyzing-imported-routes",
		"ingest-phase-analyzing-existing-routes", "ingest-phase-updating-time-zones", "ingest-phase-cleanup"}
	if !slices.Equal(codes, want) {
		t.Fatalf("phase boundaries=%v want=%v", codes, want)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_events WHERE job_id=$1
		AND code='ingest-phase-analyzing-imported-routes' AND fields='{"current":3,"attempt":1}'::jsonb`, jobs.childID)
	before := len(ingestPhaseCodes(t, f, jobs.childID))
	pipelineRecoverTerminalRoot(t, f, runner, jobs.parentID)
	if after := len(ingestPhaseCodes(t, f, jobs.childID)); before != after {
		t.Fatalf("background account recovery wrote ingest phases: %d -> %d", before, after)
	}
}

func TestIngestPhaseSkippedFilesIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 0)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 0, 0)
	first := enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	second := enqueueIncrementalIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	for _, id := range []uuid.UUID{first.childID, second.childID} {
		want := []string{"ingest-phase-preparing", "ingest-phase-discovering", "ingest-phase-importing",
			"ingest-phase-analyzing-existing-routes", "ingest-phase-updating-time-zones", "ingest-phase-cleanup"}
		if got := ingestPhaseCodes(t, f, id); !slices.Equal(got, want) {
			t.Fatalf("empty drains produced phase churn: %v", got)
		}
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_skipped=2`, second.childID)
}

func TestIngestPhaseResumeIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.attempt != 1 {
		t.Fatalf("claim attempt=%d err=%v", job.attempt, err)
	}
	directory, err := os.Open(source.root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := discoverSourceFiles(directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.resolveCandidateActions(f.ctx, job, source.sourceID, files); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.recordFileManifest(f.ctx, job, files); err != nil {
		t.Fatal(err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	current := int64(99)
	if err := runner.recordIngestPhase(f.ctx, job, phaseAnalyzingImportedRoutes, &current, nil); err != nil {
		t.Fatal(err)
	}
	if !workerCallBool(t, f.workerDB, f.accountID, `SELECT app.heartbeat_job($1,$2,$3,interval '1 millisecond')`, job.id, runner.workerID, job.lease) {
		t.Fatal("shorten original lease")
	}
	time.Sleep(10 * time.Millisecond)
	replacement := testRunner(f.workerDB, keys, source.root, io.Discard)
	replacement.osmDB = f.osmDB
	reclaimed, err := replacement.claimNext(f.ctx)
	if err != nil || reclaimed.id != job.id || reclaimed.attempt != 2 {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	if _, err := replacement.executeIngest(f.ctx, reclaimed); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_events WHERE job_id=$1
		AND code='ingest-phase-resuming' AND fields='{"attempt":2}'::jsonb`, jobs.childID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_events WHERE job_id=$1
		AND code='ingest-phase-analyzing-imported-routes' AND fields='{"current":1,"attempt":2}'::jsonb`, jobs.childID)
	codes := slices.Compact(ingestPhaseCodes(t, f, jobs.childID))
	if len(codes) < 4 || codes[1] != "ingest-phase-resuming" || codes[2] != "ingest-phase-analyzing-imported-routes" || codes[3] != "ingest-phase-preparing" {
		t.Fatalf("resume must precede recovery, then preparation: %v", codes)
	}
}

func TestIngestPhaseExistingRepairIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 27)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(source.root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := discoverSourceFiles(directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.resolveCandidateActions(f.ctx, job, source.sourceID, files); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.recordFileManifest(f.ctx, job, files); err != nil {
		t.Fatal(err)
	}
	commitEligibleCohortFile(t, f, runner, job, source, 0)
	// Model historical routes without producer handoffs. Keep the repair's exact
	// pagination/drain behavior, including its underfull EOF builder.
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `DELETE FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.accountID)
	f.commit(tx)
	job.ingestPhase = &ingestPhaseProgress{}
	lookups := 0
	cfg := f.osmDB.Config()
	cfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		assertLatestIngestPhase(t, f, job.id, "analyzing-existing-routes")
		if strings.Contains(query.SQL, "WITH route_point AS") {
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM (SELECT fields FROM app.job_events
				WHERE job_id=$1 AND code='ingest-phase-analyzing-existing-routes' ORDER BY id DESC LIMIT 1) latest
				WHERE (fields->>'current')::bigint=$2 AND NOT fields ? 'total'`, job.id, lookups)
			lookups++
		}
		return ctx
	})
	traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	runner.osmDB = traced
	if err := runner.repairCoverageReadiness(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	if lookups != 27 || job.ingestPhase.existing != 27 {
		t.Fatalf("existing progress=%d lookups=%d", job.ingestPhase.existing, lookups)
	}
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.job_events WHERE job_id=$1 AND code='ingest-phase-analyzing-imported-routes'`, jobs.childID)
	assertCountQuery(t, f.adminDB, 28, `SELECT count(*) FROM app.job_events WHERE job_id=$1 AND code='ingest-phase-analyzing-existing-routes'`, jobs.childID)
	if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25}) {
		t.Fatalf("phase reporting changed cohort behavior: %v", got)
	}
}

func TestIngestPhaseExistingReadyIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 25)
	enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	jobs := enqueueIncrementalIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.job_events WHERE job_id=$1
		AND code='ingest-phase-analyzing-imported-routes'`, jobs.childID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_events WHERE job_id=$1
		AND code='ingest-phase-analyzing-existing-routes' AND fields='{"current":25,"attempt":1}'::jsonb`, jobs.childID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_skipped=1`, jobs.childID)
}

func TestIngestPhaseRemoteBoundariesIntegration(t *testing.T) {
	f, runner, _, keys := newCoveragePipelineFixture(t)
	binary, remoteRoot, staging := fakeWorkerRclone(t)
	runner.rcloneBinary, runner.stagingRoot = binary, staging
	source := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.ICloudType, "")
	writePipelineExport(t, filepath.Join(remoteRoot, "exports", "HealthAutoExport-2026-08-01.json"), 0, 0)
	if err := os.WriteFile(filepath.Join(remoteRoot, "mode"), []byte("refresh"), 0600); err != nil {
		t.Fatal(err)
	}
	jobs := enqueueAdoptionJob(t, f.adminDB, keys, source, sourceconfig.ICloudType)
	refreshes := 0
	cfg := f.workerDB.Config()
	cfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if strings.Contains(query.SQL, "app.record_ingest_phase") {
			switch query.Args[3] {
			case string(phasePreparing):
				if _, err := os.Stat(filepath.Join(remoteRoot, "calls")); !os.IsNotExist(err) {
					t.Errorf("source constructed before preparing: %v", err)
				}
			case string(phaseDiscovering):
				assertLatestIngestPhase(t, f, jobs.childID, "preparing")
				calls, err := os.ReadFile(filepath.Join(remoteRoot, "calls"))
				if err != nil || string(calls) != "version\n" {
					t.Errorf("List ran before discovering: calls=%q err=%v", calls, err)
				}
			case string(phaseCleanup):
				entries, err := os.ReadDir(staging)
				if err != nil || len(entries) == 0 {
					t.Errorf("full remote close ran before cleanup phase: %v", err)
				}
			}
		}
		if strings.Contains(query.SQL, "app.refresh_source_credentials") {
			refreshes++
			assertLatestIngestPhase(t, f, jobs.childID, "cleanup")
			assertEmptyStaging(t, staging)
		}
		return ctx
	})
	traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	runner.db = traced
	runner.beforeProcessFile = func(sourceFile) { assertLatestIngestPhase(t, f, jobs.childID, "importing") }
	assertRanIngest(t, runner, f.ctx)
	if refreshes != 1 {
		t.Fatalf("credential refreshes=%d want=1", refreshes)
	}
	assertEmptyStaging(t, staging)
}

func TestIngestPhaseTimeZoneFailureIntegration(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			jobs := ingestJobFixture{}
			staging := ""
			if remote {
				binary, remoteRoot, privateRoot := fakeWorkerRclone(t)
				staging = privateRoot
				runner.rcloneBinary, runner.stagingRoot = binary, staging
				icloud := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.ICloudType, "")
				writePipelineExport(t, filepath.Join(remoteRoot, "exports", "HealthAutoExport-2026-08-01.json"), 0, 0)
				jobs = enqueueAdoptionJob(t, f.adminDB, keys, icloud, sourceconfig.ICloudType)
			} else {
				jobs = enqueueIngestJob(t, f.adminDB, keys, source)
			}
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			cfg := f.workerDB.Config()
			cfg.ConnConfig.Tracer = pipelineQueryTrace(func(queryCtx context.Context, query pgx.TraceQueryStartData) context.Context {
				if strings.Contains(query.SQL, "reconcile_account_workout_timezones") {
					assertLatestIngestPhase(t, f, jobs.childID, "updating-time-zones")
					cancel()
				}
				return queryCtx
			})
			traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(traced.Close)
			runner.db = traced
			job, err := runner.claimNext(ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			results := newIngestResults()
			if err := runner.ingest(ctx, job, &results); !errors.Is(err, context.Canceled) {
				t.Fatalf("time-zone cancellation=%v", err)
			}
			assertLatestIngestPhase(t, f, job.id, "updating-time-zones")
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.job_events WHERE job_id=$1 AND code='ingest-phase-cleanup'`, job.id)
			if remote {
				assertEmptyStaging(t, staging)
			}
		})
	}
}
