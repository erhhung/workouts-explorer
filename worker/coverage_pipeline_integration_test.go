package worker

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pipelineQueryTrace func(context.Context, pgx.TraceQueryStartData) context.Context

func (trace pipelineQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	return trace(ctx, query)
}

func (pipelineQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// All connections are explicit scratch URLs. Source payloads and credentials are
// generated here; neither local sample exports nor a host rclone are consulted.
func newCoveragePipelineFixture(t *testing.T, budget ...time.Duration) (*coverageIntegrationFixture, *Runner, ingestSourceFixture, *sourcecrypto.Keyring) {
	t.Helper()
	if os.Getenv("COVERAGE_ACCEPTANCE_DISPOSABLE") != "true" {
		t.Skip("requires explicitly disposable paired coverage APP/OSM databases")
	}
	timeout := 2 * time.Minute
	if len(budget) > 0 {
		timeout = budget[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	f := &coverageIntegrationFixture{t: t, ctx: ctx}
	for i, item := range []struct {
		name, role string
		pool       **pgxpool.Pool
	}{
		{"MIGRATION_DATABASE_URL", "", &f.adminDB},
		{"WORKER_DATABASE_URL", "workouts_worker", &f.workerDB},
		{"COVERAGE_WORKER_DATABASE_URL", "workouts_coverage_worker", &f.coverageDB},
		{"OSM_MIGRATION_DATABASE_URL", "", &f.osmAdminDB},
		{"COVERAGE_WORKER_OSM_DATABASE_URL", "workouts_coverage_worker", &f.osmDB},
	} {
		value := os.Getenv(item.name)
		if value == "" {
			t.Fatalf("%s is required", item.name)
		}
		name := ""
		if i > 0 {
			name = f.adminDB.Config().ConnConfig.Database
			if i >= 3 {
				name += "_osm"
			}
		}
		cfg, err := parseCoverageAcceptanceConfig(value, name)
		if err != nil {
			t.Fatalf("unsafe scratch configuration %s: %v", item.name, err)
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		*item.pool = pool
		t.Cleanup(pool.Close)
		var role string
		if err := pool.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || item.role != "" && role != item.role {
			t.Fatalf("%s role=%s err=%v", item.name, role, err)
		}
	}
	if err := validateCoverageAcceptancePair(f.adminDB.Config(), f.osmAdminDB.Config()); err != nil {
		t.Fatal(err)
	}
	if err := validateCoverageAcceptanceOSMReader(ctx, f.osmDB); err != nil {
		t.Fatal(err)
	}
	f.seedOSM()
	if _, err := f.osmAdminDB.Exec(ctx, `UPDATE osm_catalog.regions SET configured=true WHERE id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := insertIngestSource(t, f.adminDB, root)
	f.accountID, f.sourceID = source.accountID, source.sourceID
	keys := testKeyring(t)
	runner := testRunner(f.workerDB, keys, root, io.Discard)
	runner.osmDB = f.osmDB
	if err := ConfigureFileSlotLimits(ctx, f.workerDB, 2, 4); err != nil {
		t.Fatal(err)
	}
	policyDB := f.workerDB
	t.Cleanup(func() {
		// Cleanup only this synthetic account, independent of cancellation/lease
		// state. Trigger bypass is confined to the scratch superuser connection.
		tx := f.accountTx(ctx, f.adminDB)
		f.exec(tx, `SET LOCAL session_replication_role='replica'`)
		rows, err := tx.Query(ctx, `SELECT DISTINCT relation.relname FROM pg_class relation
			JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
			JOIN pg_attribute attribute ON attribute.attrelid=relation.oid
			WHERE namespace.nspname='app' AND relation.relkind IN ('r','p')
			AND attribute.attname='account_id' AND NOT attribute.attisdropped`)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, table)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			f.exec(tx, `DELETE FROM `+pgx.Identifier{"app", table}.Sanitize()+` WHERE account_id=$1`, f.accountID)
		}
		f.exec(tx, `DELETE FROM app.accounts WHERE id=$1`, f.accountID)
		f.commit(tx)
		if err := ConfigureCoverageBatchSize(ctx, policyDB, 25); err != nil {
			t.Error(err)
		}
	})
	return f, runner, source, keys
}

func writePipelineExport(t *testing.T, path string, first, count int) {
	t.Helper()
	workouts := make([]any, count)
	for i := range workouts {
		workouts[i] = map[string]any{"id": fmt.Sprintf("pipeline-synthetic-%d", first+i), "name": "Outdoor Walk",
			"start": "2026-08-01 12:00:00 +0000", "end": "2026-08-01 12:02:00 +0000", "duration": 120,
			"route": []any{
				map[string]any{"timestamp": "2026-08-01 12:00:00 +0000", "latitude": 37, "longitude": -122},
				map[string]any{"timestamp": "2026-08-01 12:01:00 +0000", "latitude": 37, "longitude": -121.999},
			}}
	}
	encoded, err := json.Marshal(map[string]any{"data": map[string]any{"workouts": workouts}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func pipelineParentSizes(t *testing.T, f *coverageIntegrationFixture) []int {
	t.Helper()
	rows, err := f.adminDB.Query(f.ctx, `SELECT count(child.id) FROM app.jobs parent
		JOIN app.coverage_job_contexts context ON context.job_id=parent.id
		LEFT JOIN app.jobs child ON child.parent_job_id=parent.id
		WHERE parent.account_id=$1 AND parent.kind='coverage_update' GROUP BY parent.id,context.parent_batch_size
		HAVING count(child.id)<=context.parent_batch_size ORDER BY count(child.id) DESC`, f.accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sizes []int
	for rows.Next() {
		var size int
		if err := rows.Scan(&size); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, size)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, len(sizes), `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.accountID)
	return sizes
}

func pipelineRecoverTerminalRoot(t *testing.T, f *coverageIntegrationFixture, runner *Runner, root uuid.UUID) {
	t.Helper()
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs root WHERE id=$1
	 AND status IN ('succeeded','partially_succeeded','failed','cancelled') AND NOT EXISTS
	 (SELECT 1 FROM app.jobs source WHERE source.parent_job_id=root.id AND source.status IN ('queued','running'))`, root)
	reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("terminal-root account recovery worked=%v err=%v", worked, err)
	}
}

func TestCoveragePipelineFileBoundaryIntegration(t *testing.T) {
	for _, test := range []struct{ size, count int }{{25, 25}, {25, 63}, {10, 23}, {1, 3}, {25, 0}} {
		t.Run(fmt.Sprintf("B%d_routes%d", test.size, test.count), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			runner.coverageBatchSize = test.size
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, test.size); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, test.count)
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), test.count, 0)
			job := enqueueIngestJob(t, f.adminDB, keys, source)
			inTransactionChecks := atomic.Int64{}
			workerCfg := f.workerDB.Config()
			workerCfg.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
				if strings.Contains(query.SQL, "INSERT INTO app.workout_import_events") {
					inTransactionChecks.Add(1)
					assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1`, source.accountID)
					assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, source.accountID)
				}
				return ctx
			})
			tracedWorker, err := pgxpool.NewWithConfig(f.ctx, workerCfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tracedWorker.Close)
			f.workerDB, runner.db = tracedWorker, tracedWorker
			lookups := atomic.Int64{}
			trace := pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
				if f.workerDB.Stat().AcquiredConns() != 0 {
					t.Error("APP connection retained across OSM query")
				}
				if strings.Contains(query.SQL, "WITH route_point AS") {
					lookups.Add(1)
					assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE account_id=$1`, source.accountID)
				}
				return ctx
			})
			cfg := f.osmDB.Config()
			cfg.ConnConfig.Tracer = trace
			traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(traced.Close)
			runner.osmDB = traced
			second := false
			runner.beforeProcessFile = func(file sourceFile) {
				if strings.HasSuffix(file.name, "01.json") {
					assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, source.accountID)
					return
				}
				second = true
				full := test.count / test.size
				want := make([]int, full)
				for i := range want {
					want[i] = test.size
				}
				if got := pipelineParentSizes(t, f); !slices.Equal(got, want) {
					t.Fatalf("before file2 parent sizes=%v want=%v", got, want)
				}
				assertCountQuery(t, f.adminDB, full*test.size, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='queued'`, source.accountID)
				if full > 0 {
					coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
					claim, found, err := coverageRunner.claim(f.ctx)
					if err != nil || !found || claim.accountID != source.accountID {
						t.Fatalf("first file work not claimable before file2: found=%v err=%v", found, err)
					}
				}
			}
			assertRanIngest(t, runner, f.ctx)
			want := make([]int, test.count/test.size)
			for i := range want {
				want[i] = test.size
			}
			if got := pipelineParentSizes(t, f); !second || !slices.Equal(got, want) {
				t.Fatalf("source EOF must retain only full parents=%v want=%v second=%v", got, want, second)
			}
			if remainder := test.count % test.size; remainder > 0 {
				assertCountQuery(t, f.adminDB, remainder, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
				pipelineRecoverTerminalRoot(t, f, runner, job.parentID)
				want = append(want, remainder)
				if got := pipelineParentSizes(t, f); !slices.Equal(got, want) {
					t.Fatalf("terminal recovered sizes=%v want=%v", got, want)
				}
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=$2 AND workouts_created_with_route=$2 AND workouts_updated=0`, job.childID, test.count)
			if test.count > 0 && (lookups.Load() == 0 || inTransactionChecks.Load() != int64(test.count)) {
				t.Fatal("pipeline never resolved fresh OSM readiness")
			}
		})
	}
}

func TestCoveragePipelineCommittedFileNewLeaseIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 63)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := runner.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim=%s err=%v", job.id, err)
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
	outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil)
	if outcome.fatal != nil || !outcome.succeeded {
		t.Fatalf("file commit=%+v", outcome)
	}
	assertCountQuery(t, f.adminDB, 63, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=0`, job.id)
	if !workerCallBool(t, f.workerDB, f.accountID, `SELECT app.heartbeat_job($1,$2,$3,interval '1 millisecond')`, job.id, runner.workerID, job.lease) {
		t.Fatal("shorten original lease")
	}
	time.Sleep(10 * time.Millisecond)
	replacement := testRunner(f.workerDB, keys, source.root, io.Discard)
	replacement.osmDB = f.osmDB
	replacement.beforeProcessFile = func(sourceFile) {
		if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25, 25}) {
			t.Fatalf("startup recovery did not precede already-counted file: %v", got)
		}
	}
	assertRanIngest(t, replacement, f.ctx)
	if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25, 25}) {
		t.Fatalf("replacement source EOF sealed a partial cohort: %v", got)
	}
	pipelineRecoverTerminalRoot(t, f, replacement, jobs.parentID)
	if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25, 25, 13}) {
		t.Fatalf("recovered file parent sizes=%v", got)
	}
	assertCountQuery(t, f.adminDB, 63, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 63, `SELECT count(DISTINCT coverage_child_job_id) FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=63 AND workouts_created_with_route=63`, job.id)
	if err := runner.drainIngestCoverage(f.ctx, job, true); !errors.Is(err, errLeaseLost) {
		t.Fatalf("old producer lease retained authority: %v", err)
	}
}

func TestCoveragePipelineFakeICloudCleanupIntegration(t *testing.T) {
	f, runner, _, keys := newCoveragePipelineFixture(t)
	binary, remoteRoot, staging := fakeWorkerRclone(t)
	source := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.ICloudType, "")
	runner.rcloneBinary, runner.stagingRoot = binary, staging
	root := filepath.Join(remoteRoot, "exports")
	writePipelineExport(t, filepath.Join(root, "HealthAutoExport-2026-08-01.json"), 0, 25)
	writePipelineExport(t, filepath.Join(root, "HealthAutoExport-2026-08-02.json"), 25, 1)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	jobs := enqueueAdoptionJob(t, f.adminDB, keys, source, sourceconfig.ICloudType)
	checks := 0
	trace := &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, _ pgx.TraceQueryStartData) context.Context {
		checks++
		if f.workerDB.Stat().AcquiredConns() != 0 {
			t.Error("APP transaction across OSM")
		}
		assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE account_id=$1`, f.accountID)
		entries, err := os.ReadDir(staging)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			for _, name := range []string{"export.json", "export.partial"} {
				if _, err := os.Stat(filepath.Join(staging, entry.Name(), name)); !os.IsNotExist(err) {
					t.Errorf("private download survived post-file cleanup: %v", err)
				}
			}
		}
		return ctx
	}}
	cfg := f.osmDB.Config()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	runner.osmDB = traced
	runner.beforeProcessFile = func(file sourceFile) {
		if strings.HasSuffix(file.name, "02.json") {
			if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25}) {
				t.Fatalf("cloud file1 not handed off before file2: %v", got)
			}
		}
	}
	assertRanIngest(t, runner, f.ctx)
	assertEmptyStaging(t, staging)
	if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25}) {
		t.Fatalf("cloud source EOF sealed a partial cohort: %v", got)
	}
	pipelineRecoverTerminalRoot(t, f, runner, jobs.parentID)
	if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{25, 1}) || checks < 26 {
		t.Fatalf("cloud parent sizes=%v lookups=%d", got, checks)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=26 AND workouts_created_with_route=26`, jobs.childID)
}

func TestCoveragePipelineTerminalRecoveryIntegration(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 3)
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := runner.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim err=%v", err)
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
			outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil)
			if outcome.fatal != nil || !outcome.succeeded {
				t.Fatalf("commit=%+v", outcome)
			}
			reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
			if _, err := reconciler.RunOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
			if status == "cancelled" {
				if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.request_job_cancellation($1,$2)`, job.id, uuid.New()) {
					t.Fatal("request source cancellation")
				}
				if finished, err := runner.finishCancelled(f.ctx, job); err != nil || !finished {
					t.Fatalf("cancel: %v", err)
				}
			} else {
				var failure *ingestFailure
				if status == "failed" {
					failure = &ingestFailure{"source-auth-expired", "Synthetic source authentication failure."}
				}
				if finished, err := runner.finishJob(f.ctx, job, status, failure); err != nil || !finished {
					t.Fatalf("finish: %v", err)
				}
			}
			if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
				t.Fatalf("terminal recovery worked=%v err=%v", worked, err)
			}
			assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
			if got := pipelineParentSizes(t, f); !slices.Equal(got, []int{3}) {
				t.Fatalf("terminal partial flush=%v", got)
			}
		})
	}
}

func TestCoveragePipelineMissingMapsAndTransientDeferralIntegration(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(fmt.Sprintf("transient=%t", transient), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			runner.coverageBatchSize = 1
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
			if !transient {
				if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
					t.Fatal(err)
				}
			} else {
				cfg := f.osmDB.Config()
				cfg.ConnConfig.Tracer = &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, _ pgx.TraceQueryStartData) context.Context {
					lookupCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
					cancel()
					return lookupCtx
				}}
				traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(traced.Close)
				runner.osmDB = traced
			}
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			assertRanIngest(t, runner, f.ctx)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='succeeded'`, jobs.childID)
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
			if !transient {
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND NOT repair AND acknowledged_at IS NOT NULL AND outcome='unavailable'`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND repair AND acknowledged_at IS NOT NULL AND outcome='unavailable'`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.workout_import_events WHERE account_id=$1 AND job_id=$2 AND kind='created'`, f.accountID, jobs.childID)
				return
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL AND next_attempt_at>clock_timestamp()+interval '55 minutes'`, f.accountID)
			reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
			if _, err := reconciler.RunOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
			if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.ingest_coverage_handoffs SET next_attempt_at='-infinity' WHERE account_id=$1`, f.accountID); err != nil {
				t.Fatal(err)
			}
			if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
				t.Fatalf("due durable recovery worked=%v err=%v", worked, err)
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
		})
	}
}

func TestCoveragePipelineDiscardsCancelledAndReclaimedProducerIntegration(t *testing.T) {
	for _, reclaim := range []bool{false, true} {
		t.Run(fmt.Sprintf("reclaim=%t", reclaim), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			runner.coverageBatchSize = 1
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := runner.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			replacement := testRunner(f.workerDB, keys, source.root, io.Discard)
			replacement.osmDB, replacement.coverageBatchSize = f.osmDB, 1
			var replacementJob claimedJob
			lookups := 0
			cfg := f.osmDB.Config()
			cfg.ConnConfig.Tracer = &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, _ pgx.TraceQueryStartData) context.Context {
				lookups++
				if lookups != 1 {
					t.Fatal("stale producer continued resolving after authority loss")
				}
				if reclaim {
					if !workerCallBool(t, f.workerDB, f.accountID, `SELECT app.heartbeat_job($1,$2,$3,interval '1 millisecond')`, job.id, runner.workerID, job.lease) {
						t.Fatal("shorten producer lease")
					}
					time.Sleep(10 * time.Millisecond)
					var err error
					replacementJob, err = replacement.claimNext(f.ctx)
					if err != nil || replacementJob.id != job.id || replacementJob.lease == job.lease {
						t.Fatalf("replacement claim: %v", err)
					}
				} else if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.request_job_cancellation($1,$2)`, job.id, uuid.New()) {
					t.Fatal("cancel producer")
				}
				return ctx
			}}
			traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(traced.Close)
			runner.osmDB = traced
			_, err = runner.executeIngest(f.ctx, job)
			if reclaim && !errors.Is(err, errJobInterrupted) || !reclaim && err != nil {
				t.Fatalf("stale producer execution: %v", err)
			}
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL`, f.accountID)
			if reclaim {
				if _, err := replacement.executeIngest(f.ctx, replacementJob); err != nil {
					t.Fatal(err)
				}
			} else {
				reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
				if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
					t.Fatalf("cancelled producer recovery: worked=%v err=%v", worked, err)
				}
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
		})
	}
}

func TestCoveragePipelineFreshSnapshotCASIntegration(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprintf("supersede=%t", supersede), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			runner.coverageBatchSize = 1
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 0, 1)
			secondPath := filepath.Join(source.root, "HealthAutoExport-2026-08-02.json")
			encoded, err := os.ReadFile(secondPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(secondPath, []byte(strings.Replace(string(encoded), `"duration":120`, `"duration":121`, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			other := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, t.TempDir())
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := runner.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
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
			if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil); outcome.fatal != nil || !outcome.succeeded {
				t.Fatalf("first commit: %+v", outcome)
			}
			lookups := 0
			cfg := f.osmDB.Config()
			cfg.ConnConfig.Tracer = &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, _ pgx.TraceQueryStartData) context.Context {
				lookups++
				assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
				if lookups != 1 {
					return ctx
				}
				if supersede {
					if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[1], nil); outcome.fatal != nil || outcome.workouts.updated != 1 {
						t.Fatalf("superseding file: %+v", outcome)
					}
				} else if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, other.sourceID, uuid.New()) {
					t.Fatal("advance account ownership epoch")
				}
				return ctx
			}}
			traced, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(traced.Close)
			runner.osmDB = traced
			if err := runner.drainIngestCoverage(f.ctx, job, false); err != nil {
				t.Fatal(err)
			}
			if lookups != 2 {
				t.Fatalf("stale content/event/epoch snapshot was not re-read: lookups=%d", lookups)
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
			if supersede {
				// New input is ACKed exactly. The old input is retired only after
				// its visible ingest root is terminal, never by a workout-wide ACK.
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL`, f.accountID)
				if finished, err := runner.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
					t.Fatalf("finish superseded-input root: %v", err)
				}
				pipelineRecoverTerminalRoot(t, f, runner, jobs.parentID)
				assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND outcome='superseded' AND coverage_child_job_id IS NULL`, f.accountID)
				assertCountQuery(t, f.adminDB, 1, `SELECT count(DISTINCT coverage_child_job_id) FROM app.ingest_coverage_handoffs WHERE account_id=$1`, f.accountID)
			}
		})
	}
}

func TestCoveragePipelineMetadataDoesNotResetFailureButAdoptionForcesIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	runner.coverageBatchSize = 1
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.root, "HealthAutoExport-2026-08-01.json")
	writePipelineExport(t, path, 0, 1)
	enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
	claim, found, err := coverageRunner.claim(f.ctx)
	if err != nil || !found {
		t.Fatalf("coverage claim: %v", err)
	}
	if err := coverageRunner.failDetached(claim, time.Now(), "coverage-matching-timeout", "Synthetic timeout."); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(encoded), `"duration":120`, `"duration":121`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	updated := enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.workout_coverage_states WHERE account_id=$1 AND target_job_id=$2 AND processing_state='failed' AND route_input_revision=1`, f.accountID, claim.jobID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_updated=1 AND workouts_updated_with_route=1`, updated.childID)
	unchanged := enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_updated=0 AND workouts_updated_with_route=0 AND workouts_unchanged=1`, unchanged.childID)
	if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, source.sourceID, uuid.New()) {
		t.Fatal("delete prior owner")
	}
	incoming := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, source.root)
	adopted := enqueueAdoptionJob(t, f.adminDB, keys, incoming, sourceconfig.LocalType)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.workout_coverage_states WHERE account_id=$1 AND target_job_id<>$2 AND route_input_revision=1`, f.accountID, claim.jobID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_updated=1 AND workouts_updated_with_route=1`, adopted.childID)
}

func TestCoveragePipelineForcedMissingMapsDoesNotBlockSourceIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	runner.coverageBatchSize, runner.osmAutoAddRegions = 1, true
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
	enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, runner, f.ctx)
	if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, source.sourceID, uuid.New()) {
		t.Fatal("delete old owner")
	}
	incoming := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, source.root)
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	adopted := enqueueAdoptionJob(t, f.adminDB, keys, incoming, sourceconfig.LocalType)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='succeeded'`, adopted.childID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND next_attempt_at>clock_timestamp()+interval '55 minutes'`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND acknowledged_at IS NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='active',retired_at=NULL WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at='-infinity' WHERE account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{OSMAutoAddRegions: true})
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("due forced recovery worked=%v err=%v", worked, err)
	}
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
}

func TestCoveragePipelineUnknownReadinessFailureIsNotSuccessIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	runner.coverageBatchSize = 1
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	if _, err := f.osmAdminDB.Exec(f.ctx, `REVOKE SELECT ON osm_catalog.regions FROM workouts_coverage_worker`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.osmAdminDB.Exec(f.ctx, `GRANT SELECT ON osm_catalog.regions TO workouts_coverage_worker`); err != nil {
			t.Error(err)
		}
	})
	worked, err := runner.RunOnce(f.ctx)
	if !worked || err == nil {
		t.Fatalf("unknown/permanent OSM error became success: worked=%v err=%v", worked, err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running'`, jobs.childID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, jobs.childID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL AND next_attempt_at<=clock_timestamp()`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
}
