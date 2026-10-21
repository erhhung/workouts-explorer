package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestICloudWorkerPostgreSQL(t *testing.T) {
	workerURL, migrationURL := os.Getenv("WORKER_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if workerURL == "" || migrationURL == "" {
		t.Skip("WORKER_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	workerDB := openWorkerTestPool(t, ctx, workerURL)
	setupDB := openWorkerTestPool(t, ctx, migrationURL)
	keys := testKeyring(t)
	if err := ConfigureFileSlotLimits(ctx, workerDB, 2, 4); err != nil {
		t.Fatal(err)
	}
	newRunner := func(t *testing.T, output io.Writer) (*Runner, string, string) {
		binary, root, staging := fakeWorkerRclone(t)
		runner := testRunner(workerDB, keys, root, output)
		runner.rcloneBinary, runner.stagingRoot = binary, staging
		return runner, root, staging
	}

	t.Run("normalization metadata skip and bounded force", func(t *testing.T) {
		var logs bytes.Buffer
		runner, root, staging := newRunner(t, &logs)
		writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json"), "60.125", 2)
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		first := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		setFakeMode(t, root, "refresh")
		assertRanIngest(t, runner, ctx)
		assertEmptyStaging(t, staging)
		assertSyntheticWorkoutState(t, setupDB, fixture, "60.125", 2)
		config, generation := readICloudWorkerConfig(t, setupDB, keys, fixture)
		if config.Cookies != "session=refreshed-cookies" || generation != 2 {
			t.Fatalf("refresh generation=%d cookies changed=%t", generation, config.Cookies != syntheticICloudConfig().Cookies)
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertTerminalIngest(t, tx, first, "succeeded", "succeeded")
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_objects WHERE source_id=$1 AND observed_identity ~ '^v2:rclone:[0-9a-f]{64}$'`, fixture.sourceID)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_file_candidates WHERE job_id=$1 AND device=0 AND inode=0 AND ctime_sec=0 AND observed_identity IS NOT NULL`, first.childID)
		_ = tx.Rollback(ctx)
		calls, _ := os.ReadFile(filepath.Join(root, "calls"))
		second := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		assertRanIngest(t, runner, ctx)
		after, _ := os.ReadFile(filepath.Join(root, "calls"))
		if strings.Count(string(calls), "cat\n") != strings.Count(string(after), "cat\n") {
			t.Fatal("unchanged incremental ingest downloaded bytes")
		}
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.source_files WHERE job_id=$1`, second.childID)
		assertCountQuery(t, tx, 1, `SELECT files_skipped FROM app.job_progress WHERE job_id=$1`, second.childID)
		assertTerminalIngest(t, tx, second, "succeeded", "succeeded")
		_ = tx.Rollback(ctx)
		third := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestBounded)
		assertRanIngest(t, runner, ctx)
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged'`, third.childID)
		assertTerminalIngest(t, tx, third, "succeeded", "succeeded")
		_ = tx.Rollback(ctx)
		assertEmptyStaging(t, staging)
		for _, private := range []string{root, staging, config.Password, config.Cookies, config.TrustToken, config.AppleID} {
			if strings.Contains(logs.String(), private) {
				t.Fatal("worker lifecycle leaked private fixture")
			}
		}
	})

	for _, mode := range []string{"auth", "download-auth", "refresh-download-auth", "adp", "transient", "refresh-transient", "invalid"} {
		t.Run("failure "+mode, func(t *testing.T) {
			runner, root, staging := newRunner(t, io.Discard)
			path := filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json")
			writeSyntheticExport(t, path, "60", 1)
			setFakeMode(t, root, mode)
			if mode == "invalid" {
				if err := os.WriteFile(path, []byte("private-payload"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
			job := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
			assertRanIngest(t, runner, ctx)
			assertEmptyStaging(t, staging)
			tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
			defer rollbackICloudTestTx(tx)
			assertTerminalIngest(t, tx, job, "failed", "failed")
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM app.sources WHERE id=$1`, fixture.sourceID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "connected"
			if mode == "auth" || mode == "download-auth" || mode == "refresh-download-auth" || mode == "adp" {
				want = "connection-failed"
			}
			if status != want {
				t.Fatalf("source status=%s want=%s", status, want)
			}
			if mode == "invalid" {
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND failure_code='source-file-invalid'`, job.childID)
			}
			_ = tx.Rollback(ctx)
			config, generation := readICloudWorkerConfig(t, setupDB, keys, fixture)
			wantGeneration := int64(1)
			if mode == "refresh-transient" {
				wantGeneration = 2
				if config.Cookies != "session=refreshed-cookies" {
					t.Fatal("non-auth failure lost final cookie refresh")
				}
			}
			if generation != wantGeneration {
				t.Fatalf("generation=%d want=%d", generation, wantGeneration)
			}
		})
	}

	for _, mode := range []string{"first-mutated", "first-unavailable"} {
		t.Run("file failure continues "+mode, func(t *testing.T) {
			runner, root, staging := newRunner(t, io.Discard)
			writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json"), "60", 1)
			writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-02.json"), "90", 2)
			setFakeMode(t, root, mode)
			fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
			job := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
			assertRanIngest(t, runner, ctx)
			assertEmptyStaging(t, staging)
			assertSyntheticWorkoutState(t, setupDB, fixture, "90", 2)
			failureCode := "source-file-mutated"
			if mode == "first-unavailable" {
				failureCode = "source-file-unavailable"
			}
			tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
			defer rollbackICloudTestTx(tx)
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND relative_name='HealthAutoExport-2026-08-01.json' AND state='failed' AND failure_code=$2`, job.childID, failureCode)
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND relative_name='HealthAutoExport-2026-08-02.json' AND state='succeeded'`, job.childID)
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_discovered=2 AND files_failed=1 AND files_succeeded=1`, job.childID)
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.sources WHERE id=$1 AND status='connected' AND generation=1`, fixture.sourceID)
			assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE job_id=$1`, job.childID)
			assertTerminalIngest(t, tx, job, "failed", "failed")
		})
	}

	t.Run("first writer preserves immutable snapshot and typed dispatch", func(t *testing.T) {
		runner, _, staging := newRunner(t, io.Discard)
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		job, err := runner.claimNext(ctx)
		if err != nil || job.id != jobFixture.childID {
			t.Fatalf("claim=%s err=%v", job.id, err)
		}
		snapshot, _, err := runner.readSnapshot(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		original := syntheticICloudConfig()
		updated := original
		updated.Cookies = "session=first-writer"
		if err := runner.refreshICloudCredentials(ctx, job, snapshot, original, updated); err != nil {
			t.Fatal(err)
		}
		updated.Cookies = "session=second-writer"
		if err := runner.refreshICloudCredentials(ctx, job, snapshot, original, updated); err != nil {
			t.Fatal(err)
		}
		current, generation := readICloudWorkerConfig(t, setupDB, keys, fixture)
		after, _, err := runner.readSnapshot(ctx, job)
		if err != nil || !bytes.Equal(snapshot.envelope, after.envelope) || generation != 2 || current.Cookies != "session=first-writer" {
			t.Fatalf("first-writer or snapshot isolation failed: generation=%d err=%v", generation, err)
		}
		job.sourceType = sourceconfig.LocalType // Logging context is never authoritative.
		if _, err := runner.execute(ctx, job); err != nil {
			t.Fatal(err)
		}
		assertEmptyStaging(t, staging)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertTerminalIngest(t, tx, jobFixture, "succeeded", "succeeded")
	})

	t.Run("connection cleanup precedes atomic refresh and completion", func(t *testing.T) {
		runner, root, staging := newRunner(t, io.Discard)
		fixture := insertICloudWorkerSource(t, setupDB, keys, "checking-connection")
		jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, "")
		job, err := runner.claimNext(ctx)
		if err != nil || job.id != jobFixture.childID {
			t.Fatalf("claim=%s err=%v", job.id, err)
		}
		snapshot, _, err := runner.readSnapshot(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		setFakeMode(t, root, "refresh")
		result, err := runner.checkICloudConnection(ctx, job, snapshot)
		if err != nil || result.refreshedEncrypted == nil {
			t.Fatalf("connection refresh missing: err=%v", err)
		}
		assertEmptyStaging(t, staging)
		connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: job.id}
		assertWorkerState(t, setupDB, connection, "checking-connection", 1, "running", 1)
		if finished, err := runner.complete(ctx, job, result); err != nil || !finished {
			t.Fatalf("completion=%t err=%v", finished, err)
		}
		assertWorkerState(t, setupDB, connection, "connected", 2, "succeeded", 0)
	})

	t.Run("owner update discards refresh", func(t *testing.T) {
		runner, root, staging := newRunner(t, io.Discard)
		writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json"), "60", 1)
		setFakeMode(t, root, "refresh")
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		job := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		runner.beforeProcessFile = func(sourceFile) {
			tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
			defer rollbackICloudTestTx(tx)
			if _, err := tx.Exec(ctx, `UPDATE app.sources SET generation=generation+1 WHERE id=$1`, fixture.sourceID); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		assertRanIngest(t, runner, ctx)
		config, generation := readICloudWorkerConfig(t, setupDB, keys, fixture)
		if generation != 2 || config.Cookies != syntheticICloudConfig().Cookies {
			t.Fatal("stale refresh overwrote owner update")
		}
		assertEmptyStaging(t, staging)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertTerminalIngest(t, tx, job, "succeeded", "succeeded")
	})

	t.Run("connection cleanup failure cannot terminalize", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses filesystem permission fences")
		}
		runner, root, staging := newRunner(t, io.Discard)
		t.Cleanup(func() { _ = os.Chmod(staging, 0700) })
		fixture := insertICloudWorkerSource(t, setupDB, keys, "checking-connection")
		jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, "")
		setFakeMode(t, root, "cleanup-failure")
		if worked, err := runner.RunOnce(ctx); !worked || !errors.Is(err, errPrivateCleanup) {
			t.Fatalf("cleanup failure: worked=%t err=%v", worked, err)
		}
		connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: jobFixture.childID}
		assertWorkerState(t, setupDB, connection, "checking-connection", 1, "running", 1)
		if worked, err := runner.RunOnce(ctx); worked || !errors.Is(err, errPrivateCleanup) {
			t.Fatalf("claim passed failed cleanup gate: worked=%t err=%v", worked, err)
		}
		if err := os.Chmod(staging, 0700); err != nil {
			t.Fatal(err)
		}
		setFakeMode(t, root, "")
		expireICloudWorkerLease(t, setupDB, workerDB, runner, connection)
		assertRanIngest(t, runner, ctx)
		assertEmptyStaging(t, staging)
		if runner.StagingCleanupPending() || len(runner.pendingCleanup) != 0 {
			t.Fatal("reclaim did not drain cleanup state")
		}
		assertWorkerState(t, setupDB, connection, "connected", 1, "succeeded", 0)
	})

	t.Run("ingest constructor cleanup failure cannot terminalize", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses filesystem permission fences")
		}
		runner, root, staging := newRunner(t, io.Discard)
		t.Cleanup(func() { _ = os.Chmod(staging, 0700) })
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		setFakeMode(t, root, "constructor-cleanup-failure")
		if worked, err := runner.RunOnce(ctx); !worked || !errors.Is(err, errPrivateCleanup) {
			t.Fatalf("constructor cleanup failure: worked=%t err=%v", worked, err)
		}
		connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: jobFixture.childID}
		assertWorkerState(t, setupDB, connection, "connected", 1, "running", 1)
		if worked, err := runner.RunOnce(ctx); worked || !errors.Is(err, errPrivateCleanup) {
			t.Fatalf("claim passed failed cleanup gate: worked=%t err=%v", worked, err)
		}
		if err := os.Chmod(staging, 0700); err != nil {
			t.Fatal(err)
		}
		setFakeMode(t, root, "")
		expireICloudWorkerLease(t, setupDB, workerDB, runner, connection)
		assertRanIngest(t, runner, ctx)
		assertEmptyStaging(t, staging)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertTerminalIngest(t, tx, jobFixture, "succeeded", "succeeded")
	})

	t.Run("partial cleanup retains slot and gates reclaim", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses filesystem permission fences")
		}
		runner, root, staging := newRunner(t, io.Discard)
		writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json"), "60", 1)
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		job := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		setFakeMode(t, root, "partial-cleanup-failure")
		t.Cleanup(func() {
			entries, _ := os.ReadDir(staging)
			for _, entry := range entries {
				_ = os.Chmod(filepath.Join(staging, entry.Name()), 0700)
			}
		})
		if worked, err := runner.RunOnce(ctx); !worked || !errors.Is(err, errPrivateCleanup) {
			t.Fatalf("partial cleanup failure: worked=%t err=%v", worked, err)
		}
		connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: job.childID}
		assertWorkerState(t, setupDB, connection, "connected", 1, "running", 1)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.ingest_file_slots WHERE job_id=$1`, job.childID)
		_ = tx.Rollback(ctx)
		if worked, err := runner.RunOnce(ctx); worked || !errors.Is(err, errPrivateCleanup) {
			t.Fatalf("claim passed failed partial cleanup: worked=%t err=%v", worked, err)
		}
		entries, err := os.ReadDir(staging)
		if err != nil || len(entries) != 1 {
			t.Fatalf("retained job directories=%d err=%v", len(entries), err)
		}
		partial := filepath.Join(staging, entries[0].Name(), "export.partial")
		if info, err := os.Stat(partial); err != nil || info.Size() == 0 {
			t.Fatalf("fixture did not retain partial plaintext: err=%v", err)
		}
		// Restore permissions only; the runtime, not the fixture, removes files.
		if err := os.Chmod(filepath.Dir(partial), 0700); err != nil {
			t.Fatal(err)
		}
		setFakeMode(t, root, "")
		expireICloudWorkerLease(t, setupDB, workerDB, runner, connection)
		assertRanIngest(t, runner, ctx)
		assertEmptyStaging(t, staging)
		assertSyntheticWorkoutState(t, setupDB, fixture, "60", 1)
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertTerminalIngest(t, tx, job, "succeeded", "succeeded")
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE job_id=$1`, job.childID)
	})

	t.Run("lease loss preserves snapshot and removes private files", func(t *testing.T) {
		runner, root, staging := newRunner(t, io.Discard)
		runner.leaseDuration = 3 * time.Second
		runner.heartbeatInterval = time.Minute
		writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json"), "60", 1)
		setFakeMode(t, root, "refresh")
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		runner.beforeProcessFile = func(sourceFile) { time.Sleep(runner.leaseDuration + 50*time.Millisecond) }
		if _, err := runner.execute(ctx, job); !errors.Is(err, errJobInterrupted) {
			t.Fatalf("lease loss=%v", err)
		}
		assertEmptyStaging(t, staging)
		connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: jobFixture.childID}
		assertWorkerState(t, setupDB, connection, "connected", 1, "running", 1)
		replacement := testRunner(workerDB, keys, root, io.Discard)
		replacementJob, err := replacement.claimNext(ctx)
		if err != nil || replacementJob.id != job.id {
			t.Fatalf("recover claim=%s err=%v", replacementJob.id, err)
		}
		if finished, err := replacement.finishCancelled(ctx, replacementJob); err != nil || !finished {
			t.Fatalf("fixture cleanup=%t err=%v", finished, err)
		}
	})

	t.Run("refresh persistence failure stays recoverable", func(t *testing.T) {
		runner, root, staging := newRunner(t, io.Discard)
		setFakeMode(t, root, "refresh")
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		injectICloudRefreshWriteFailure(t, setupDB, fixture)
		jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.execute(ctx, job); !errors.Is(err, errCredentialRefresh) {
			t.Fatalf("refresh write failure=%v", err)
		}
		assertEmptyStaging(t, staging)
		connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: jobFixture.childID}
		assertWorkerState(t, setupDB, connection, "connected", 1, "running", 1)
		config, generation := readICloudWorkerConfig(t, setupDB, keys, fixture)
		if generation != 1 || config != syntheticICloudConfig() {
			t.Fatal("failed refresh changed current source configuration")
		}
		if !workerCallBool(t, setupDB, fixture.accountID, `SELECT app.request_job_cancellation($1,$2)`, job.id, uuid.New()) {
			t.Fatal("request cancellation failed")
		}
		if finished, err := runner.finishCancellationPending(ctx, job); err != nil || !finished {
			t.Fatalf("fixture cleanup=%t err=%v", finished, err)
		}
	})

	for _, databaseCancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("download cancellation database=%t", databaseCancel), func(t *testing.T) {
			runner, root, staging := newRunner(t, io.Discard)
			runner.heartbeatInterval = 20 * time.Millisecond
			setFakeMode(t, root, "hang")
			writeSyntheticExport(t, filepath.Join(root, "exports", "HealthAutoExport-2026-08-01.json"), "60", 1)
			fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
			jobFixture := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
			job, err := runner.claimNext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := runner.execute(cancelCtx, job); done <- err }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("download did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
			defer rollbackICloudTestTx(tx)
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.ingest_file_slots WHERE job_id=$1`, job.id)
			_ = tx.Rollback(ctx)
			if databaseCancel {
				if !workerCallBool(t, setupDB, fixture.accountID, `SELECT app.request_job_cancellation($1,$2)`, job.id, uuid.New()) {
					t.Fatal("request cancellation failed")
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if databaseCancel && err != nil || !databaseCancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("download process group was not cancelled promptly")
			}
			assertEmptyStaging(t, staging)
			if !databaseCancel {
				connection := connectionFixture{accountID: fixture.accountID, sourceID: fixture.sourceID, jobID: jobFixture.childID}
				assertWorkerState(t, setupDB, connection, "connected", 1, "running", 1)
				workerCallBool(t, setupDB, fixture.accountID, `SELECT app.request_job_cancellation($1,$2)`, job.id, uuid.New())
				if finished, err := runner.finishCancellationPending(ctx, job); err != nil || !finished {
					t.Fatalf("fixture cleanup=%t err=%v", finished, err)
				}
			}
			tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
			defer rollbackICloudTestTx(tx)
			assertTerminalIngest(t, tx, jobFixture, "cancelled", "cancelled")
			assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE job_id=$1`, job.id)
		})
	}
}

func rollbackICloudTestTx(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func expireICloudWorkerLease(t *testing.T, setupDB, workerDB *pgxpool.Pool, runner *Runner, fixture connectionFixture) {
	t.Helper()
	tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
	defer rollbackICloudTestTx(tx)
	var lease uuid.UUID
	if err := tx.QueryRow(context.Background(), `SELECT lease_token FROM app.jobs WHERE id=$1`, fixture.jobID).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	rollbackICloudTestTx(tx)
	if !workerCallBool(t, workerDB, fixture.accountID, `SELECT app.heartbeat_job($1,$2,$3,interval '1 millisecond')`, fixture.jobID, runner.workerID, lease) {
		t.Fatal("fixture lease could not be shortened")
	}
	time.Sleep(20 * time.Millisecond)
}

func injectICloudRefreshWriteFailure(t *testing.T, db *pgxpool.Pool, fixture ingestSourceFixture) {
	t.Helper()
	// Disposable integration DB only: migration-role DDL targets one fixture
	// source's config writes without bypassing the normal integrity triggers.
	name := "worker_refresh_failure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackICloudTestTx(tx)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger
		LANGUAGE plpgsql SET search_path = pg_catalog AS $fixture$
		BEGIN RAISE EXCEPTION 'fixture credential refresh write failure' USING ERRCODE = 'XX000'; END;
		$fixture$`, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON FUNCTION app.%s() FROM PUBLIC`, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF config_envelope ON app.sources
		FOR EACH ROW WHEN (OLD.id = '%s'::uuid AND OLD.config_envelope IS DISTINCT FROM NEW.config_envelope)
		EXECUTE FUNCTION app.%s()`, name, fixture.sourceID, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Errorf("begin refresh failure fixture cleanup: %v", err)
			return
		}
		defer rollbackICloudTestTx(tx)
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON app.sources`, name)); err != nil {
			t.Errorf("drop refresh failure fixture trigger: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, name)); err != nil {
			t.Errorf("drop refresh failure fixture function: %v", err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("commit refresh failure fixture cleanup: %v", err)
		}
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertICloudWorkerSource(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, status string) ingestSourceFixture {
	t.Helper()
	fixture := ingestSourceFixture{accountID: uuid.New(), sourceID: uuid.New()}
	_, canonical, err := sourceconfig.CanonicalizeICloud(syntheticICloudConfig())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: fixture.accountID, RecordID: fixture.sourceID}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer rollbackICloudTestTx(tx)
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.accounts(id) VALUES($1)`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
		VALUES($1,$2,'synthetic icloud','synthetic icloud',$3,$4,$5)`, fixture.sourceID, fixture.accountID, sourceconfig.ICloudType, status, envelope); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func readICloudWorkerConfig(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture) (sourceconfig.ICloud, int64) {
	t.Helper()
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer rollbackICloudTestTx(tx)
	var envelope []byte
	var generation int64
	if err := tx.QueryRow(context.Background(), `SELECT config_envelope,generation FROM app.sources WHERE id=$1`, fixture.sourceID).Scan(&envelope, &generation); err != nil {
		t.Fatal(err)
	}
	plain, err := keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: fixture.accountID, RecordID: fixture.sourceID}, envelope)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := sourceconfig.DecodeICloud(plain)
	if err != nil {
		t.Fatal(err)
	}
	return config, generation
}

func enqueueICloudWorkerJob(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture, mode ingestMode) ingestJobFixture {
	t.Helper()
	config, generation := readICloudWorkerConfig(t, db, keys, fixture)
	_, canonical, _ := sourceconfig.CanonicalizeICloud(config)
	job := ingestJobFixture{parentID: uuid.New(), childID: uuid.New()}
	envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: fixture.accountID, RecordID: job.childID}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer rollbackICloudTestTx(tx)
	kind := "source_connection_check"
	parameters := map[string]any{"sourceId": strings.ToUpper(strings.ReplaceAll(fixture.sourceID.String(), "-", "")), "generation": generation}
	if mode != "" {
		kind = "manual_ingest_source"
		parameters["mode"] = mode
		if mode == ingestBounded {
			parameters["startDate"], parameters["endDate"] = "0001-01-01", "9999-12-31"
		}
		if _, err := tx.Exec(context.Background(), `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, job.parentID, fixture.accountID); err != nil {
			t.Fatal(err)
		}
	}
	encoded, _ := json.Marshal(parameters)
	var parent any
	if mode != "" {
		parent = job.parentID
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,parameters) VALUES($1,$2,$3,$4,80,$5)`, job.childID, fixture.accountID, parent, kind, encoded); err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3)`, job.parentID, job.childID, fixture.accountID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type) VALUES($1,$2,$3,$4,'synthetic icloud',$5)`, job.childID, fixture.accountID, fixture.sourceID, generation, sourceconfig.ICloudType); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,$4,$5)`, job.childID, fixture.accountID, fixture.sourceID, generation, envelope); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return job
}
