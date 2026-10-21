package worker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCoveragePipelineLargeBatchMemoryIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t, 4*time.Minute)
	const batch, points = 25, 50_001
	const largest = 250_000
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, batch); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, batch)
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
	if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil); outcome.fatal != nil || !outcome.succeeded {
		t.Fatalf("source file: %+v", outcome)
	}
	// Stream synthetic large historical routes into the scratch database. Never
	// build B*points Go fixtures; the real file transaction above creates origins.
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	rows, err := tx.Query(f.ctx, `SELECT id FROM app.workouts WHERE account_id=$1 ORDER BY id`, f.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var workouts []uuid.UUID
	for rows.Next() {
		var workout uuid.UUID
		if err := rows.Scan(&workout); err != nil {
			t.Fatal(err)
		}
		workouts = append(workouts, workout)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	f.exec(tx, `DELETE FROM app.workout_route_points WHERE account_id=$1`, f.accountID)
	routeIndex, sequence := 0, 0
	values := []any{f.accountID, uuid.Nil, 0, time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), 37.0, -122.0,
		1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0}
	inserted, err := tx.CopyFrom(f.ctx, pgx.Identifier{"app", "workout_route_points"}, []string{
		"account_id", "workout_id", "sequence", "recorded_at", "latitude", "longitude", "altitude", "speed", "course",
		"horizontal_accuracy", "vertical_accuracy", "speed_accuracy", "course_accuracy",
	}, pgx.CopyFromFunc(func() ([]any, error) {
		if routeIndex == len(workouts) {
			return nil, nil
		}
		values[1], values[2] = workouts[routeIndex], sequence
		sequence++
		limit := points
		if routeIndex == 0 {
			limit = largest
		}
		if sequence == limit {
			routeIndex++
			sequence = 0
		}
		return values, nil
	}))
	totalPoints := (batch-1)*points + largest
	if err != nil || inserted != int64(totalPoints) {
		t.Fatalf("streamed raw fixture rows=%d err=%v", inserted, err)
	}
	f.exec(tx, `UPDATE app.workout_routes SET point_count=CASE WHEN workout_id=$3 THEN $4::integer ELSE $2::integer END WHERE account_id=$1`, f.accountID, points, workouts[0], largest)
	f.commit(tx)

	var peak atomic.Uint64
	runtime.GC()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			if memory.HeapAlloc > peak.Load() {
				peak.Store(memory.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	err = runner.drainIngestCoverage(f.ctx, job, false)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("24 x 50001 + 1 x 250000-point handoff peak Go heap: %.1f MiB", float64(peak.Load())/(1<<20))
	if peak.Load() > 200<<20 {
		t.Fatalf("handoff retained a batch of route payloads: peak=%d bytes", peak.Load())
	}
	assertCountQuery(t, f.adminDB, batch, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NOT NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, totalPoints, `SELECT count(*) FROM app.workout_route_points WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running'`, job.id)
	coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
	if worked, err := coverageRunner.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("oversized child execution: worked=%v err=%v", worked, err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='failed' AND failure_code='coverage-route-too-large'`, f.accountID)
}

func writePipelineRawRoute(t *testing.T, path string, count int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriter(file)
	_, err = io.WriteString(writer, `{"data":{"workouts":[{"id":"large-raw-provider","name":"Outdoor Walk","start":"2026-08-01 12:00:00 +0000","end":"2026-08-01 12:02:00 +0000","duration":120,"route":[`)
	for i := 0; i < count && err == nil; i++ {
		if i > 0 {
			err = writer.WriteByte(',')
		}
		if err == nil {
			_, err = io.WriteString(writer, `{"timestamp":"2026-08-01 12:00:00 +0000","latitude":37,"longitude":-122}`)
		}
	}
	if err == nil {
		_, err = io.WriteString(writer, `]}]}}`)
	}
	if err == nil {
		err = writer.Flush()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("stream synthetic raw source: %v %v", err, closeErr)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() >= 64<<20 {
		t.Fatalf("synthetic raw source must fit parser byte limit: %v", err)
	}
}

func TestCoveragePipelineParserBoundAndEmptyAdoptionIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t, 6*time.Minute)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	maximumPath := filepath.Join(source.root, "parser-bound.json")
	writePipelineRawRoute(t, maximumPath, 250_000)
	verifyPipelineParserBound(t, maximumPath)
	writePipelineRawRoute(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 50_001)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 1, 1)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	// The unchanged production lease may expire during a large atomic file
	// transaction under -race. Recovery must finish its already-committed file.
	for attempt := 0; ; attempt++ {
		worked, err := runner.RunOnce(f.ctx)
		if worked && err == nil {
			break
		}
		if !worked || !errors.Is(err, errJobInterrupted) || attempt >= 2 {
			t.Fatalf("parser-bound source/recovery: worked=%v err=%v", worked, err)
		}
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='succeeded'`, jobs.childID)
	assertCountQuery(t, f.adminDB, 50_003, `SELECT count(*) FROM app.workout_route_points WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	pipelineRecoverTerminalRoot(t, f, runner, jobs.parentID)
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
	for range 2 {
		if worked, err := coverageRunner.RunOnce(f.ctx); err != nil || !worked {
			t.Fatalf("coverage sibling: worked=%v err=%v", worked, err)
		}
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='failed' AND failure_code='coverage-route-too-large'`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='succeeded'`, f.accountID)
	// Unchanged oversized ownership adoption must use the same full input hash.
	if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, source.sourceID, uuid.New()) {
		t.Fatal("delete prior owner")
	}
	incoming := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, source.root)
	adopted := enqueueAdoptionJob(t, f.adminDB, keys, incoming, sourceconfig.LocalType)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	pipelineRecoverTerminalRoot(t, f, runner, adopted.parentID)
	assertCountQuery(t, f.adminDB, 4, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	// A real zero-GPS adoption acknowledges its origins without queuing matching.
	zeroRoot := t.TempDir()
	runner.localRoots = append(runner.localRoots, zeroRoot)
	writePipelineRawRoute(t, filepath.Join(zeroRoot, "HealthAutoExport-2026-08-01.json"), 0)
	if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, incoming.sourceID, uuid.New()) {
		t.Fatal("delete second owner")
	}
	zero := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, zeroRoot)
	zeroJob := enqueueAdoptionJob(t, f.adminDB, keys, zero, sourceconfig.LocalType)
	assertRanIngest(t, runner, f.ctx)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='succeeded'`, zeroJob.childID)
	assertCountQuery(t, f.adminDB, 4, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND ingest_job_id=$2 AND acknowledged_at IS NOT NULL AND coverage_child_job_id IS NULL`, f.accountID, zeroJob.childID)
}

func verifyPipelineParserBound(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := healthautoexport.Parse(file)
	if err != nil || len(document.Workouts) != 1 || len(document.Workouts[0].Route) != 250_000 {
		t.Fatalf("parser must preserve all 250000 raw points: %v", err)
	}
}

func TestCoveragePipelineMetadataOnlyThousandIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1000)
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
	if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil); outcome.fatal != nil || !outcome.succeeded {
		t.Fatalf("source commit: %+v", outcome)
	}
	tx := f.accountTx(f.ctx, f.workerDB)
	rows, err := tx.Query(f.ctx, `SELECT * FROM app.read_ingest_coverage_batch($1,$2,$3,$4,NULL,NULL,1000,false)`, f.accountID, job.id, runner.workerID, job.lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.FieldDescriptions()) != 5 {
		t.Fatal("batch reader includes route payload/digest computation instead of metadata only")
	}
	count, bytes := 0, 0
	for rows.Next() {
		var workout, event uuid.UUID
		var origin string
		var content []byte
		var epoch int64
		if err := rows.Scan(&workout, &event, &origin, &content, &epoch); err != nil {
			t.Fatal(err)
		}
		bytes += 16 + 16 + len(origin) + len(content) + 8
		count++
	}
	rows.Close()
	if err := rows.Err(); err != nil || count != 1000 || bytes > 128_000 {
		t.Fatalf("B1000 metadata count=%d bytes=%d error=%v", count, bytes, err)
	}
	f.commit(tx)
}

func TestCoveragePipelineRouteReadLimitIsPermanent(t *testing.T) {
	failure := classifyCoverageFailure(context.Background(), coverageStageRouteRead,
		errors.Join(context.DeadlineExceeded, &routepipeline.LimitError{Field: "original points", Limit: routepipeline.OriginalPointLimit}))
	if failure.code != "coverage-route-too-large" {
		t.Fatalf("oversized child was not a permanent processing limit: %s", failure.code)
	}
}

func TestCoveragePipelineMixedConnectionFailureDoesNotAcknowledgeIntegration(t *testing.T) {
	for _, cause := range []error{&pgconn.PgError{Code: "42501"}, errors.New("unknown connection cause"), context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			f, runner, source, keys := newCoveragePipelineFixture(t)
			runner.coverageBatchSize = 1
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
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
			if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil); outcome.fatal != nil || !outcome.succeeded {
				t.Fatalf("source commit: %+v", outcome)
			}
			cfg := f.osmDB.Config()
			cfg.ConnConfig.ValidateConnect = func(context.Context, *pgconn.PgConn) error {
				return errors.Join(context.DeadlineExceeded, cause)
			}
			failing, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(failing.Close)
			runner.osmDB = failing
			if _, err := runner.executeIngest(f.ctx, job); err == nil {
				t.Fatal("mixed permanent/unknown/cancel connection error became a successful source")
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running'`, job.id)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL AND next_attempt_at<=clock_timestamp()`, f.accountID)
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
		})
	}
}

func TestCoveragePipelineLargeUnavailableThenOrdinaryRecoveryIntegration(t *testing.T) {
	f, runner, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
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
	if outcome := runner.processSourceFile(f.ctx, job, source.sourceID, source.root, files[0], nil); outcome.fatal != nil || !outcome.succeeded {
		t.Fatalf("source commit: %+v", outcome)
	}
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	var large uuid.UUID
	f.scan(tx, `SELECT id FROM app.workouts WHERE account_id=$1 ORDER BY id LIMIT 1`, []any{f.accountID}, &large)
	f.exec(tx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.accountID, large)
	f.exec(tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
		SELECT $1,$2,sequence,'2026-08-01T12:00:00Z'::timestamptz,37,-122 FROM generate_series(0,50000) sequence`, f.accountID, large)
	f.exec(tx, `UPDATE app.workout_routes SET point_count=50001 WHERE account_id=$1 AND workout_id=$2`, f.accountID, large)
	f.commit(tx)
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	if err := runner.drainIngestCoverage(f.ctx, job, true); err != nil {
		t.Fatal(err)
	}
	if finished, err := runner.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
		t.Fatalf("finish source: %v", err)
	}
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NOT NULL AND outcome='unavailable'`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
	reconciler := NewCoverageReconciler(f.coverageDB, f.osmDB, runner.logger, CoverageReconcilerOptions{})
	if _, err := reconciler.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='active',retired_at=NULL WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET next_scan_at='-infinity' WHERE account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	if worked, err := reconciler.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("ordinary large-route readiness recovery: worked=%v err=%v", worked, err)
	}
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='queued'`, f.accountID)
	assertCountQuery(t, f.adminDB, 50_003, `SELECT count(*) FROM app.workout_route_points WHERE account_id=$1`, f.accountID)
	coverageRunner := NewCoverageRunner(f.coverageDB, f.osmDB, runner.logger)
	for range 2 {
		if worked, err := coverageRunner.RunOnce(f.ctx); err != nil || !worked {
			t.Fatalf("recovered coverage child: worked=%v err=%v", worked, err)
		}
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='failed' AND failure_code='coverage-route-too-large'`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='succeeded'`, f.accountID)
}
