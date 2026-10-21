package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type handoffApplyTraceKey struct{}

type handoffContentionTrace struct {
	batches, snapshots, applies atomic.Int64
	stale                       atomic.Bool
	failures                    chan error
	lastFailure                 atomic.Pointer[pgconn.PgError]
}

func (trace *handoffContentionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	switch {
	case strings.Contains(query.SQL, "app.read_ingest_coverage_batch("):
		trace.batches.Add(1)
	case strings.Contains(query.SQL, "app.read_ingest_coverage_route_snapshot("):
		trace.snapshots.Add(1)
	case strings.Contains(query.SQL, "app.apply_ingest_coverage_batch("):
		attempt := trace.applies.Add(1)
		if trace.batches.Load() != attempt || trace.snapshots.Load() != attempt {
			trace.stale.Store(true)
		}
		return context.WithValue(ctx, handoffApplyTraceKey{}, true)
	}
	return ctx
}

func (trace *handoffContentionTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, result pgx.TraceQueryEndData) {
	if ctx.Value(handoffApplyTraceKey{}) == nil || result.Err == nil {
		return
	}
	var cause *pgconn.PgError
	if errors.As(result.Err, &cause) {
		trace.lastFailure.Store(cause)
	}
	select {
	case trace.failures <- result.Err:
	default:
	}
}

func TestCoveragePipelineParentContentionBackoffIntegration(t *testing.T) {
	for _, scenario := range []string{"release", "exhausted", "parent_cancel", "reclaim", "context_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			runner.coverageBatchSize = 1
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
			enqueueIngestJob(t, f.adminDB, keys, source)
			assertRanIngest(t, runner, f.ctx)
			var coverageParent, coverageChild uuid.UUID
			if err := f.adminDB.QueryRow(f.ctx, `SELECT id,parent_job_id FROM app.jobs
				WHERE account_id=$1 AND kind='coverage_update_route' AND status='queued'`, f.accountID).
				Scan(&coverageChild, &coverageParent); err != nil {
				t.Fatal(err)
			}
			if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, source.sourceID, uuid.New()) {
				t.Fatal("delete prior owner")
			}
			incoming := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, source.root)
			jobs := enqueueAdoptionJob(t, f.adminDB, keys, incoming, sourceconfig.LocalType)
			job, err := runner.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("adoption claim: %v", err)
			}
			// A coverage claimer owns its parent before touching workout/state.
			holder := f.accountTx(f.ctx, f.adminDB)
			defer holder.Rollback(context.Background())
			f.exec(holder, `SELECT id FROM app.jobs WHERE id=$1 FOR UPDATE`, coverageParent)
			directory, err := os.Open(source.root)
			if err != nil {
				t.Fatal(err)
			}
			files, err := discoverSourceFiles(directory)
			_ = directory.Close()
			if err != nil {
				t.Fatal(err)
			}
			if outcome := runner.processSourceFile(f.ctx, job, incoming.sourceID, source.root, files[0], nil); outcome.fatal != nil || outcome.workouts.updated != 1 {
				t.Fatalf("atomic adopted file: %+v", outcome)
			}
			results := newIngestResults()
			if err := runner.loadIngestProgress(f.ctx, job, &results); err != nil {
				t.Fatal(err)
			}
			results.filesDiscovered = len(files)
			if err := runner.recordIngestProgress(f.ctx, job, results); err != nil {
				t.Fatal(err)
			}
			trace := &handoffContentionTrace{failures: make(chan error, 8)}
			workerConfig := f.workerDB.Config()
			workerConfig.ConnConfig.Tracer = trace
			workerDB, err := pgxpool.NewWithConfig(f.ctx, workerConfig)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(workerDB.Close)
			runner.db = workerDB
			var lookups atomic.Int64
			osmConfig := f.osmDB.Config()
			osmConfig.ConnConfig.Tracer = &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, _ pgx.TraceQueryStartData) context.Context {
				lookups.Add(1)
				if workerDB.Stat().AcquiredConns() != 0 {
					t.Error("APP connection retained during fresh OSM resolution")
				}
				return ctx
			}}
			osmDB, err := pgxpool.NewWithConfig(f.ctx, osmConfig)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(osmDB.Close)
			runner.osmDB = osmDB
			opCtx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			done := make(chan error, 1)
			started := time.Now()
			go func() { done <- runner.drainIngestCoverage(opCtx, job, true) }()
			select {
			case cause := <-trace.failures:
				var sql *pgconn.PgError
				if !errors.As(cause, &sql) || sql.Code != "55P03" {
					t.Fatalf("parent prelock did not yield typed NOWAIT contention: %v", cause)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("apply blocked instead of yielding the coverage parent")
			}
			deadline := time.Now().Add(5 * time.Second)
			for workerDB.Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if workerDB.Stat().AcquiredConns() != 0 {
				t.Fatal("APP transaction/connection retained during contention backoff")
			}
			var replacement *Runner
			var replacementJob claimedJob
			switch scenario {
			case "release":
				if err := holder.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
			case "parent_cancel":
				if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.request_job_cancellation($1,$2)`, jobs.parentID, uuid.New()) {
					t.Fatal("cancel ingest parent during backoff")
				}
			case "reclaim":
				if !workerCallBool(t, f.workerDB, f.accountID, `SELECT app.heartbeat_job($1,$2,$3,interval '1 millisecond')`, job.id, runner.workerID, job.lease) {
					t.Fatal("shorten source lease during backoff")
				}
				time.Sleep(10 * time.Millisecond)
				replacement = testRunner(f.workerDB, keys, source.root, io.Discard)
				replacement.osmDB, replacement.coverageBatchSize = f.osmDB, 1
				replacementJob, err = replacement.claimNext(f.ctx)
				if err != nil || replacementJob.id != job.id || replacementJob.lease == job.lease {
					t.Fatalf("reclaim source during backoff: %v", err)
				}
			case "context_cancel":
				cancel()
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("contention retry did not terminate within its bounded budget")
			}
			if attempts := trace.applies.Load(); attempts < 1 || attempts > 3 || lookups.Load() > 3 || trace.stale.Load() {
				t.Fatalf("retry reused stale data or exceeded budget: apply=%d OSM=%d stale=%v", attempts, lookups.Load(), trace.stale.Load())
			}
			switch scenario {
			case "release":
				if err != nil || trace.applies.Load() < 2 {
					t.Fatalf("released parent was not freshly retried: %v", err)
				}
			case "exhausted":
				var sql *pgconn.PgError
				if !errors.As(err, &sql) || sql.Code != "55P03" || !errors.Is(err, trace.lastFailure.Load()) || trace.applies.Load() != 3 || lookups.Load() != 3 {
					t.Fatalf("exhaustion lost original error or retry bound: %v", err)
				}
				if time.Since(started) < 75*time.Millisecond {
					t.Fatal("exhausted contention retried without 25ms/50ms backoff")
				}
			case "parent_cancel", "reclaim":
				if !errors.Is(err, errLeaseLost) && !errors.Is(err, errCancellationRequested) {
					t.Fatalf("authority loss retried as contention: %v", err)
				}
			case "context_cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("context cancellation lost: %v", err)
				}
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, job.id)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running' AND failure_code IS NULL`, job.id)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_failed=0 AND workouts_created=0 AND workouts_updated=1 AND workouts_updated_with_route=1`, job.id)
			if scenario != "release" {
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND acknowledged_at IS NULL`, f.accountID)
			}
			_ = holder.Rollback(f.ctx)
			if scenario == "exhausted" {
				if err := runner.drainIngestCoverage(f.ctx, job, true); err != nil {
					t.Fatalf("durable recovery after contention exhaustion: %v", err)
				}
			} else if scenario == "reclaim" {
				if err := replacement.drainIngestCoverage(f.ctx, replacementJob, true); err != nil {
					t.Fatalf("durable recovery by replacement lease: %v", err)
				}
			}
			if scenario == "release" || scenario == "exhausted" || scenario == "reclaim" {
				if scenario != "reclaim" {
					if err := runner.drainIngestCoverage(f.ctx, job, true); err != nil {
						t.Fatalf("idempotent committed adoption replay: %v", err)
					}
				}
				assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL AND coverage_child_job_id<>$2`, f.accountID, coverageChild)
				assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.accountID)
			}
		})
	}
}
