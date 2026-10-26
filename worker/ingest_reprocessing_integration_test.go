package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/rclonesource"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/jackc/pgx/v5/pgconn"
)

// This wrapper records exact cat pathnames, then delegates metadata and normal
// downloads to the existing synthetic CLI. Corruption never changes metadata.
func TestReprocessingRcloneHelper(t *testing.T) {
	if os.Getenv("WORKER_FAKE_RCLONE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(40)
	}
	args = args[1:]
	if slices.Contains(args, "cat") {
		name := ""
		for index, arg := range args {
			if arg == "--include" && index+1 < len(args) {
				name = strings.TrimPrefix(args[index+1], "/")
			}
		}
		if name == "" || filepath.Base(name) != name {
			os.Exit(41)
		}
		root := os.Getenv("WORKER_FAKE_ROOT")
		calls, err := os.OpenFile(filepath.Join(root, "cat-paths"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(42)
		}
		_, err = fmt.Fprintln(calls, name)
		if closeErr := calls.Close(); err != nil || closeErr != nil {
			os.Exit(43)
		}
		invalid, _ := os.ReadFile(filepath.Join(root, "invalid-download"))
		if string(invalid) == name {
			info, err := os.Stat(filepath.Join(root, "exports", name))
			if err != nil {
				os.Exit(44)
			}
			_, _ = os.Stdout.Write(bytes.Repeat([]byte(" "), int(info.Size())))
			os.Exit(0)
		}
	}
	TestWorkerRcloneHelper(t)
}

func fakeReprocessingRclone(t *testing.T) (binary, root, staging string) {
	t.Helper()
	binary, root, staging = fakeWorkerRclone(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec "+quoted+" -test.run=^TestReprocessingRcloneHelper$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return binary, root, staging
}

func mockCatCounts(t *testing.T, root string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "cat-paths"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, name := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if name != "" {
			counts[name]++
		}
	}
	return counts
}

func assertMockCatDelta(t *testing.T, root string, before map[string]int, want map[string]int) {
	t.Helper()
	after := mockCatCounts(t, root)
	for name, count := range after {
		if delta := count - before[name]; delta != want[name] {
			t.Fatalf("cat %q delta=%d, want %d (before=%v after=%v)", name, delta, want[name], before, after)
		}
	}
	for name, count := range want {
		if after[name]-before[name] != count {
			t.Fatalf("cat %q delta=%d, want %d", name, after[name]-before[name], count)
		}
	}
}

func TestReprocessingRcloneFixture(t *testing.T) {
	binary, root, staging := fakeReprocessingRclone(t)
	name := "HealthAutoExport-2026-08-01.json"
	writeSyntheticExport(t, filepath.Join(root, "exports", name), "60", 2)
	ctx := context.Background()
	remote, err := rclonesource.New(ctx, binary, staging, syntheticICloudConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	objects, err := remote.List(ctx)
	if err != nil || len(objects) != 1 {
		t.Fatalf("objects=%d err=%v", len(objects), err)
	}
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			if invalid {
				if err := os.WriteFile(filepath.Join(root, "invalid-download"), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := mockCatCounts(t, root)
			path, err := remote.Download(ctx, objects[0])
			if err != nil {
				t.Fatal(err)
			}
			file := remoteSourceFiles(objects)[0]
			parsed, failure := readStagedSourceFile(ctx, path, file)
			if invalid && (failure == nil || failure.code != "source-file-invalid") ||
				!invalid && (failure != nil || len(parsed.document.Workouts) != 1) {
				t.Fatalf("staged parse invalid=%t failure=%v workouts=%d", invalid, failure, len(parsed.document.Workouts))
			}
			assertMockCatDelta(t, root, before, map[string]int{name: 1})
			if err := remote.DiscardDownload(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	assertEmptyStaging(t, staging)
}

func TestIngestReprocessingPostgreSQL(t *testing.T) {
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

	t.Run("bounded metadata skip explicit force and legacy force", func(t *testing.T) {
		binary, root, staging := fakeReprocessingRclone(t)
		name := "HealthAutoExport-2026-08-01.json"
		writeSyntheticExport(t, filepath.Join(root, "exports", name), "60.125", 2)
		fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
		runner := testRunner(workerDB, keys, root, io.Discard)
		runner.rcloneBinary, runner.stagingRoot = binary, staging
		enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
		before := mockCatCounts(t, root)
		assertRanIngest(t, runner, ctx)
		assertMockCatDelta(t, root, before, map[string]int{name: 1})
		for _, policy := range []string{"false", "true", "legacy"} {
			t.Run(policy, func(t *testing.T) {
				var flag *bool
				if policy != "legacy" {
					value := policy == "true"
					flag = &value
				}
				job := enqueueICloudWorkerPolicyJob(t, setupDB, keys, fixture, ingestBounded, flag, nil)
				before := mockCatCounts(t, root)
				assertRanIngest(t, runner, ctx)
				processed, skipped := 1, 0
				if policy == "false" {
					processed, skipped = 0, 1
				}
				assertMockCatDelta(t, root, before, map[string]int{name: processed})
				assertEmptyStaging(t, staging)
				assertSyntheticWorkoutState(t, setupDB, fixture, "60.125", 2)
				tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
				defer rollbackICloudTestTx(tx)
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
				assertCountQuery(t, tx, processed, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, job.childID)
				assertCountQuery(t, tx, processed, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged'`, job.childID)
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1
					AND files_discovered=1 AND files_succeeded=$2 AND files_skipped=$3 AND files_failed=0
					AND workouts_created=0 AND workouts_updated=0 AND workouts_unchanged=$2`, job.childID, processed, skipped)
				assertTerminalIngest(t, tx, job, "succeeded", "succeeded")
			})
		}
	})

	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry ancestry force=%t", force), func(t *testing.T) {
			binary, root, staging := fakeReprocessingRclone(t)
			names := []string{"HealthAutoExport-2026-08-01.json", "HealthAutoExport-2026-08-02.json", "HealthAutoExport-2026-08-03.json"}
			writeExport := func(index int) {
				path := filepath.Join(root, "exports", names[index])
				writeSyntheticExport(t, path, "60", 2)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("synthetic-provider"), []byte(fmt.Sprintf("synthetic-provider-%d", index))), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeExport(0)
			if force {
				writeExport(1)
				writeExport(2)
			}
			fixture := insertICloudWorkerSource(t, setupDB, keys, "connected")
			runner := testRunner(workerDB, keys, root, io.Discard)
			runner.rcloneBinary, runner.stagingRoot = binary, staging
			warm := enqueueICloudWorkerJob(t, setupDB, keys, fixture, ingestIncremental)
			assertRanIngest(t, runner, ctx)
			if !force {
				writeExport(1)
				writeExport(2)
			}
			if err := os.WriteFile(filepath.Join(root, "invalid-download"), []byte(names[1]), 0600); err != nil {
				t.Fatal(err)
			}
			rootJob := enqueueICloudWorkerPolicyJob(t, setupDB, keys, fixture, ingestBounded, &force, nil)
			job, err := runner.claimNext(ctx)
			if err != nil || job.id != rootJob.childID {
				t.Fatalf("claim root=%s err=%v", job.id, err)
			}
			// Stop before C touches the transport, after A/B progress is durable.
			interrupted, cancel := context.WithCancel(ctx)
			defer cancel()
			runner.beforeProcessFile = func(file sourceFile) {
				if file.name == names[2] {
					cancel()
				}
			}
			before := mockCatCounts(t, root)
			if _, err := runner.execute(interrupted, job); !errors.Is(err, context.Canceled) {
				t.Fatalf("interruption=%v", err)
			}
			rootA := 0
			if force {
				rootA = 1
			}
			assertMockCatDelta(t, root, before, map[string]int{names[0]: rootA, names[1]: 1, names[2]: 0})
			assertEmptyStaging(t, staging)
			failure := &ingestFailure{"source-file-invalid", "Synthetic interrupted ingest."}
			if finished, err := runner.finishJob(ctx, job, "failed", failure); err != nil || !finished {
				t.Fatalf("finish root=%t err=%v", finished, err)
			}
			tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
			defer rollbackICloudTestTx(tx)
			assertCountQuery(t, tx, rootA, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND relative_name=$2 AND state='succeeded'`, job.id, names[0])
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND relative_name=$2 AND state='failed'`, job.id, names[1])
			assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND relative_name=$2`, job.id, names[2])
			assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_discovered=3
				AND files_succeeded=$2 AND files_skipped=$3 AND files_failed=1`, job.id, rootA, 1-rootA)
			if force {
				assertCountQuery(t, tx, 2, `SELECT count(*) FROM app.source_objects WHERE source_id=$1
					AND relative_name=ANY($2::text[]) AND successful_job_id=$3 AND successful_checksum IS NOT NULL`,
					fixture.sourceID, names[1:], warm.childID)
			} else {
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_objects WHERE source_id=$1
					AND relative_name=$2 AND successful_job_id=$3`, fixture.sourceID, names[0], warm.childID)
			}
			_ = tx.Rollback(ctx)
			runner.beforeProcessFile = nil
			prior := rootJob
			for attempt := 1; attempt <= 2; attempt++ {
				if attempt == 2 {
					if err := os.Remove(filepath.Join(root, "invalid-download")); err != nil {
						t.Fatal(err)
					}
				}
				retry := enqueueICloudWorkerPolicyJob(t, setupDB, keys, fixture, ingestBounded, &force, &prior)
				before := mockCatCounts(t, root)
				assertRanIngest(t, runner, ctx)
				wantC, skipped, succeeded, failed := 1, 1, 1, 1
				if attempt == 2 {
					wantC, skipped, failed = 0, 2, 0
				}
				assertMockCatDelta(t, root, before, map[string]int{names[0]: 0, names[1]: 1, names[2]: wantC})
				assertEmptyStaging(t, staging)
				tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
				defer rollbackICloudTestTx(tx)
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_file_candidates WHERE job_id=$1 AND relative_name=$2 AND action='skip'`, retry.childID, names[0])
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1
					AND files_discovered=3 AND files_skipped=$2 AND files_succeeded=$3 AND files_failed=$4`, retry.childID, skipped, succeeded, failed)
				assertCountQuery(t, tx, succeeded, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1`, retry.childID)
				if force {
					assertCountQuery(t, tx, succeeded, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged'`, retry.childID)
				}
				if attempt == 2 {
					assertTerminalIngest(t, tx, retry, "succeeded", "succeeded")
					assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
					assertCountQuery(t, tx, 6, `SELECT count(*) FROM app.workout_route_points point JOIN app.workouts workout
						ON workout.id=point.workout_id AND workout.account_id=point.account_id WHERE workout.source_id=$1`, fixture.sourceID)
				} else {
					assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='failed'`, retry.childID)
				}
				_ = tx.Rollback(ctx)
				prior = retry
			}
		})
	}

	t.Run("inventory failure rolls back success receipt and immutable success recovers", func(t *testing.T) {
		root := t.TempDir()
		name := "HealthAutoExport-2026-08-01.json"
		writeSyntheticExport(t, filepath.Join(root, name), "60", 2)
		fixture := insertIngestSource(t, setupDB, root)
		jobFixture := enqueueBoundedIngestPolicyJob(t, setupDB, keys, fixture, true)
		runner := testRunner(workerDB, keys, root, io.Discard)
		job, err := runner.claimNext(ctx)
		if err != nil || job.id != jobFixture.childID {
			t.Fatalf("claim=%s err=%v", job.id, err)
		}
		directory, err := sourceconfig.OpenDirectory(root, []string{root})
		if err != nil {
			t.Fatal(err)
		}
		files, err := discoverSourceFiles(directory)
		_ = directory.Close()
		if err != nil || len(files) != 1 {
			t.Fatalf("files=%d err=%v", len(files), err)
		}
		if err := runner.resolveCandidateActions(ctx, job, fixture.sourceID, files); err != nil {
			t.Fatal(err)
		}
		if state, err := runner.recordFileManifest(ctx, job, files); err != nil || state != "created" {
			t.Fatalf("manifest=%s err=%v", state, err)
		}
		// Disposable integration DB only; fail after the success transition so
		// the test detects non-atomic receipt/workout/inventory persistence.
		trigger := "worker_inventory_failure_" + strings.ReplaceAll(fixture.sourceID.String(), "-", "")
		if _, err := setupDB.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql
			SET search_path=pg_catalog AS $fixture$ BEGIN RAISE EXCEPTION 'synthetic inventory failure' USING ERRCODE='XX000'; END; $fixture$`, trigger)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := setupDB.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON app.source_objects`, trigger)); err != nil {
				t.Errorf("drop inventory failure trigger: %v", err)
				return
			}
			if _, err := setupDB.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION app.%s()`, trigger)); err != nil {
				t.Errorf("drop inventory failure function: %v", err)
			}
		})
		if _, err := setupDB.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON FUNCTION app.%s() FROM PUBLIC`, trigger)); err != nil {
			t.Fatal(err)
		}
		if _, err := setupDB.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON app.source_objects
			FOR EACH ROW WHEN (NEW.source_id='%s'::uuid) EXECUTE FUNCTION app.%s()`, trigger, fixture.sourceID, trigger)); err != nil {
			t.Fatal(err)
		}
		outcome := runner.processSourceFile(ctx, job, fixture.sourceID, root, files[0], nil)
		var sqlErr *pgconn.PgError
		if !errors.As(outcome.fatal, &sqlErr) || sqlErr.Code != "XX000" {
			t.Fatalf("inventory rollback outcome=%+v err=%v", outcome, outcome.fatal)
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='discovered'
			AND observed_identity IS NULL AND ownership_blocked_epoch IS NULL AND checksum_sha256 IS NULL`, job.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1`, job.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.source_objects WHERE source_id=$1`, fixture.sourceID)
		_ = tx.Rollback(ctx)
		if _, err := setupDB.Exec(ctx, fmt.Sprintf(`DROP TRIGGER %s ON app.source_objects`, trigger)); err != nil {
			t.Fatal(err)
		}
		outcome = runner.processSourceFile(ctx, job, fixture.sourceID, root, files[0], nil)
		if !outcome.succeeded || outcome.fatal != nil || outcome.workouts.created != 1 {
			t.Fatalf("recovered file=%+v err=%v", outcome, outcome.fatal)
		}
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files file JOIN app.source_objects inventory
			ON inventory.source_id=file.source_id AND inventory.account_id=file.account_id AND inventory.relative_name=file.relative_name
			WHERE file.job_id=$1 AND file.state='succeeded' AND file.observed_identity=inventory.observed_identity
			AND file.ownership_blocked_epoch IS NULL AND file.checksum_sha256=inventory.successful_checksum
			AND inventory.successful_job_id=file.job_id`, job.id)
		_ = tx.Rollback(ctx)
		writeTx, _, err := runner.beginIngestWrite(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		_, err = writeTx.Exec(ctx, `UPDATE app.source_files SET state='failed',failure_code='source-file-invalid',failure_summary='Synthetic failure.' WHERE job_id=$1`, job.id)
		_ = writeTx.Rollback(ctx)
		if err == nil {
			t.Fatal("succeeded checkpoint was mutable")
		}
		if _, err := runner.execute(ctx, job); err != nil {
			t.Fatal(err)
		}
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_discovered=1
			AND files_succeeded=1 AND files_skipped=0 AND files_failed=0 AND workouts_created=1`, job.id)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, job.id)
		assertTerminalIngest(t, tx, jobFixture, "succeeded", "succeeded")
	})

	t.Run("failed checkpoint replay retains persisted progress", func(t *testing.T) {
		root := t.TempDir()
		name := "HealthAutoExport-2026-08-01.json"
		if err := os.WriteFile(filepath.Join(root, name), []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		fixture := insertIngestSource(t, setupDB, root)
		jobFixture := enqueueBoundedIngestPolicyJob(t, setupDB, keys, fixture, true)
		runner := testRunner(workerDB, keys, root, io.Discard)
		job, err := runner.claimNext(ctx)
		if err != nil || job.id != jobFixture.childID {
			t.Fatalf("claim=%s err=%v", job.id, err)
		}
		directory, err := sourceconfig.OpenDirectory(root, []string{root})
		if err != nil {
			t.Fatal(err)
		}
		files, err := discoverSourceFiles(directory)
		_ = directory.Close()
		if err != nil || len(files) != 1 {
			t.Fatalf("files=%d err=%v", len(files), err)
		}
		if err := runner.resolveCandidateActions(ctx, job, fixture.sourceID, files); err != nil {
			t.Fatal(err)
		}
		if state, err := runner.recordFileManifest(ctx, job, files); err != nil || state != "created" {
			t.Fatalf("manifest=%s err=%v", state, err)
		}
		outcome := runner.processSourceFile(ctx, job, fixture.sourceID, root, files[0], nil)
		if !outcome.failed || outcome.fatal != nil {
			t.Fatalf("failed file=%+v", outcome)
		}
		results := newIngestResults()
		if err := runner.loadIngestProgress(ctx, job, &results); err != nil || results.filesFailed != 1 || !slices.Equal(results.FailedProcessing, []string{name}) {
			t.Fatalf("restored results=%+v err=%v", results, err)
		}
		results.filesDiscovered = len(files)
		if err := runner.recordIngestProgress(ctx, job, results); err != nil {
			t.Fatal(err)
		}
		outcome = runner.processSourceFile(ctx, job, fixture.sourceID, root, files[0], nil)
		if !outcome.alreadyCounted || outcome.failed || outcome.fatal != nil {
			t.Fatalf("checkpoint replay=%+v", outcome)
		}
		if _, err := runner.execute(ctx, job); err != nil {
			t.Fatal(err)
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer rollbackICloudTestTx(tx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND files_discovered=1 AND files_failed=1 AND files_succeeded=0 AND files_skipped=0`, job.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1`, job.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.source_objects WHERE source_id=$1`, fixture.sourceID)
		assertTerminalIngest(t, tx, jobFixture, "failed", "failed")
	})
}
