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
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunnerManualIngestPostgreSQL(t *testing.T) {
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

	t.Run("golden import and unchanged reimport", func(t *testing.T) {
		root := t.TempDir()
		copyGoldenIngestFixtures(t, root)
		fixture := insertIngestSource(t, setupDB, root)
		first := enqueueIngestJob(t, setupDB, keys, fixture)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		assertRanIngest(t, runner, ctx)
		assertGoldenIngest(t, setupDB, fixture, first)
		assertIngestLogResults(t, logs.String(), 3, 5, 5, nil)

		second := enqueueIngestJob(t, setupDB, keys, fixture)
		logs.Reset()
		assertRanIngest(t, runner, ctx)
		assertIngestLogResults(t, logs.String(), 3, 5, 0, nil)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertCountQuery(t, tx, 5, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, 788, `SELECT count(*) FROM app.workout_route_points point
			JOIN app.workouts workout ON workout.id=point.workout_id AND workout.account_id=point.account_id
			WHERE workout.source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, 5, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged'`, second.childID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind<>'matched_unchanged'`, second.childID)
		assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, second.childID)
		assertTerminalIngest(t, tx, second, "succeeded", "succeeded")
	})

	t.Run("changed provider workout updates in place", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "HealthAutoExport-2026-08-01.json")
		writeSyntheticExport(t, path, "60.125", 2)
		fixture := insertIngestSource(t, setupDB, root)
		first := enqueueIngestJob(t, setupDB, keys, fixture)
		runner := testRunner(workerDB, keys, root, io.Discard)
		assertRanIngest(t, runner, ctx)

		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		var workoutID uuid.UUID
		var firstHash []byte
		if err := tx.QueryRow(ctx, `SELECT id,content_sha256 FROM app.workouts WHERE source_id=$1 AND provider_id='synthetic-provider'`, fixture.sourceID).Scan(&workoutID, &firstHash); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		writeSyntheticExport(t, path, "75.500", 3)
		second := enqueueIngestJob(t, setupDB, keys, fixture)
		assertRanIngest(t, runner, ctx)
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		var updatedID uuid.UUID
		var updatedHash []byte
		var duration string
		if err := tx.QueryRow(ctx, `SELECT id,content_sha256,provider_duration::text FROM app.workouts
			WHERE source_id=$1 AND provider_id='synthetic-provider'`, fixture.sourceID).Scan(&updatedID, &updatedHash, &duration); err != nil {
			t.Fatal(err)
		}
		if updatedID != workoutID || bytes.Equal(firstHash, updatedHash) || duration != "75.5" {
			t.Fatalf("updated workout id/hash/duration=%s/%x/%s", updatedID, updatedHash, duration)
		}
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.workout_route_points WHERE workout_id=$1`, workoutID)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM (
			SELECT recorded_at FROM app.workout_route_points WHERE workout_id=$1 GROUP BY recorded_at HAVING count(*)=3
		) duplicate`, workoutID)
		var sequences []int32
		rows, err := tx.Query(ctx, `SELECT sequence FROM app.workout_route_points WHERE workout_id=$1 ORDER BY sequence`, workoutID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var sequence int32
			if err := rows.Scan(&sequence); err != nil {
				t.Fatal(err)
			}
			sequences = append(sequences, sequence)
		}
		rows.Close()
		if !slices.Equal(sequences, []int32{0, 1, 2}) {
			t.Fatalf("route sequences=%v", sequences)
		}
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, first.childID)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='updated'`, second.childID)
		assertTerminalIngest(t, tx, second, "succeeded", "succeeded")
	})

	t.Run("newest export deterministically wins overlapping provider workout", func(t *testing.T) {
		root := t.TempDir()
		olderName := "HealthAutoExport-2026-08-03.json"
		writeSyntheticExport(t, filepath.Join(root, olderName), "60", 2)
		writeSyntheticExport(t, filepath.Join(root, "HealthAutoExport-2026-08-04.json"), "90", 4)
		fixture := insertIngestSource(t, setupDB, root)
		first := enqueueIngestJob(t, setupDB, keys, fixture)
		runner := testRunner(workerDB, keys, root, io.Discard)
		runner.beforeProcessFile = func(file sourceFile) {
			if file.name == olderName {
				time.Sleep(100 * time.Millisecond)
			}
		}
		assertRanIngest(t, runner, ctx)
		assertSyntheticWorkoutState(t, setupDB, fixture, "90", 4)

		second := enqueueIncrementalIngestJob(t, setupDB, keys, fixture)
		runner.beforeProcessFile = nil
		assertRanIngest(t, runner, ctx)
		assertSyntheticWorkoutState(t, setupDB, fixture, "90", 4)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertCountQuery(t, tx, 2, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, first.childID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.source_files WHERE job_id=$1`, second.childID)
	})

	t.Run("committed file before progress recovers as succeeded", func(t *testing.T) {
		root := t.TempDir()
		writeSyntheticExport(t, filepath.Join(root, "HealthAutoExport-2026-08-02.json"), "60", 1)
		fixture := insertIngestSource(t, setupDB, root)
		jobFixture := enqueueIngestJob(t, setupDB, keys, fixture)
		runner := testRunner(workerDB, keys, root, io.Discard)
		runner.leaseDuration = 250 * time.Millisecond
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sourceID, _, _, err := runner.readSnapshot(ctx, job)
		if err != nil {
			t.Fatal(err)
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
		if err := runner.resolveCandidateActions(ctx, job, sourceID, files); err != nil {
			t.Fatal(err)
		}
		if state, err := runner.recordFileManifest(ctx, job, files); err != nil || state != "created" {
			t.Fatalf("manifest=%s err=%v", state, err)
		}
		outcome := runner.processSourceFile(ctx, job, sourceID, root, files[0])
		if !outcome.succeeded || outcome.fatal != nil {
			t.Fatalf("pre-progress outcome=%+v", outcome)
		}
		time.Sleep(runner.leaseDuration + 50*time.Millisecond)
		replacement := testRunner(workerDB, keys, root, io.Discard)
		replacementJob, err := replacement.claimNext(ctx)
		if err != nil || replacementJob.id != jobFixture.childID {
			t.Fatalf("replacement job=%s err=%v", replacementJob.id, err)
		}
		if _, err := replacement.execute(ctx, replacementJob); err != nil {
			t.Fatal(err)
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		var skipped, succeeded int
		if err := tx.QueryRow(ctx, `SELECT files_skipped,files_succeeded FROM app.job_progress WHERE job_id=$1`,
			jobFixture.childID).Scan(&skipped, &succeeded); err != nil {
			t.Fatal(err)
		}
		if skipped != 0 || succeeded != 1 {
			t.Fatalf("recovered progress skipped/succeeded=%d/%d", skipped, succeeded)
		}
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1`, jobFixture.childID)
	})

	t.Run("malformed file rolls back and redacts", func(t *testing.T) {
		root := t.TempDir()
		privatePayload := "private-payload-" + uuid.NewString()
		privateName := "HealthAutoExport-2026-09-09.json"
		if err := os.WriteFile(filepath.Join(root, privateName), []byte(privatePayload), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture := insertIngestSource(t, setupDB, root)
		job := enqueueIngestJob(t, setupDB, keys, fixture)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		assertRanIngest(t, runner, ctx)

		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='failed'
			AND failure_code='source-file-invalid'`, job.childID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_route_points point JOIN app.workouts workout
			ON workout.id=point.workout_id AND workout.account_id=point.account_id WHERE workout.source_id=$1`, fixture.sourceID)
		assertTerminalIngest(t, tx, job, "failed", "failed")
		assertIngestLogResults(t, logs.String(), 1, 0, 0, []string{privateName})
		var code, summary, fileCode, fileSummary string
		if err := tx.QueryRow(ctx, `SELECT failure_code,failure_summary FROM app.jobs WHERE id=$1`, job.childID).Scan(&code, &summary); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT failure_code,failure_summary FROM app.source_files WHERE job_id=$1`, job.childID).Scan(&fileCode, &fileSummary); err != nil {
			t.Fatal(err)
		}
		combined := code + " " + summary + " " + fileCode + " " + fileSummary + " " + logs.String()
		for _, private := range []string{root, privatePayload} {
			if strings.Contains(combined, private) {
				t.Fatalf("failure output leaked private value %q", private)
			}
		}
	})

	t.Run("source update does not change child snapshot", func(t *testing.T) {
		originalRoot := t.TempDir()
		writeSyntheticExport(t, filepath.Join(originalRoot, "HealthAutoExport-2026-10-01.json"), "60", 1)
		fixture := insertIngestSource(t, setupDB, originalRoot)
		job := enqueueIngestJob(t, setupDB, keys, fixture)

		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		if _, err := tx.Exec(ctx, `UPDATE app.sources SET generation=2,config_envelope=$2 WHERE id=$1`, fixture.sourceID, []byte("updated-source-envelope")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		runner := testRunner(workerDB, keys, originalRoot, io.Discard)
		assertRanIngest(t, runner, ctx)

		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workouts WHERE source_id=$1 AND provider_id='synthetic-provider'`, fixture.sourceID)
		var generation int64
		if err := tx.QueryRow(ctx, `SELECT generation FROM app.sources WHERE id=$1`, fixture.sourceID).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if generation != 2 {
			t.Fatalf("source generation=%d", generation)
		}
		assertTerminalIngest(t, tx, job, "succeeded", "succeeded")
	})

	t.Run("scheduled ingest is claimed fenced and logged", func(t *testing.T) {
		root := t.TempDir()
		writeSyntheticExport(t, filepath.Join(root, "HealthAutoExport-2026-11-01.json"), "60", 1)
		fixture := insertIngestSource(t, setupDB, root)
		job := enqueueIngestJobKind(t, setupDB, keys, fixture, "scheduled_ingest", "scheduled_ingest_source")
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		assertRanIngest(t, runner, ctx)
		assertIngestLogResults(t, logs.String(), 1, 1, 1, nil)
		if !strings.Contains(logs.String(), `"job_type":"scheduled_ingest_source"`) {
			t.Fatalf("scheduled ingest job type missing from lifecycle logs: %s", logs.String())
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, job.childID)
		assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, job.childID)
		assertTerminalIngest(t, tx, job, "succeeded", "succeeded")
	})

	t.Run("cancelled terminal log retains partial results", func(t *testing.T) {
		root := t.TempDir()
		fixture := insertIngestSource(t, setupDB, root)
		jobFixture := enqueueIngestJob(t, setupDB, keys, fixture)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		job, err := runner.claimNext(ctx)
		if err != nil || job.id != jobFixture.childID {
			t.Fatalf("claimed=%s err=%v", job.id, err)
		}
		runner.loadJobLogContext(ctx, &job)
		err = runner.executeLogged(ctx, job, func(ctx context.Context, job claimedJob) (executionResult, error) {
			results := ingestResults{FilesProcessed: 1, WorkoutsProcessed: 2, WorkoutsIngested: 1, FailedProcessing: []string{}}
			_, finishErr := runner.finishCancelled(ctx, job)
			return executionResult{ingest: &results}, finishErr
		})
		if err != nil {
			t.Fatal(err)
		}
		assertIngestLogResults(t, logs.String(), 1, 2, 1, nil)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertTerminalIngest(t, tx, jobFixture, "cancelled", "cancelled")
	})

	t.Run("reclaimed ingest lease does not log partial results as terminal", func(t *testing.T) {
		root := t.TempDir()
		fixture := insertIngestSource(t, setupDB, root)
		jobFixture := enqueueIngestJob(t, setupDB, keys, fixture)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		runner.leaseDuration = 5 * time.Millisecond
		job, err := runner.claimNext(ctx)
		if err != nil || job.id != jobFixture.childID {
			t.Fatalf("claimed=%s err=%v", job.id, err)
		}
		runner.loadJobLogContext(ctx, &job)
		time.Sleep(10 * time.Millisecond)
		replacement := testRunner(workerDB, keys, root, io.Discard)
		replacementJob, err := replacement.claimNext(ctx)
		if err != nil || replacementJob.id != job.id {
			t.Fatalf("replacement claimed=%s err=%v", replacementJob.id, err)
		}
		err = runner.executeLogged(ctx, job, func(context.Context, claimedJob) (executionResult, error) {
			results := ingestResults{FilesProcessed: 1, WorkoutsProcessed: 2, WorkoutsIngested: 1, FailedProcessing: []string{}}
			return executionResult{ingest: &results}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), `"msg":"job processing outcome unavailable"`) || strings.Contains(logs.String(), `"results"`) {
			t.Fatalf("reclaimed ingest lifecycle log is inaccurate: %s", logs.String())
		}
		if !workerCallBool(t, workerDB, fixture.accountID, `SELECT app.finish_job($1,$2,$3,'cancelled')`,
			job.id, replacement.workerID, replacementJob.lease) {
			t.Fatal("replacement ingest lease cleanup failed")
		}
	})

	t.Run("shutdown interruption leaves ingest retryable", func(t *testing.T) {
		root := t.TempDir()
		fixture := insertIngestSource(t, setupDB, root)
		jobFixture := enqueueIngestJob(t, setupDB, keys, fixture)
		runner := testRunner(workerDB, keys, root, io.Discard)
		runner.leaseDuration = 5 * time.Millisecond
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		interrupted, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := runner.execute(interrupted, job); !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown ingest error=%v", err)
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		var childStatus, parentStatus string
		var snapshots int
		if err := tx.QueryRow(ctx, `SELECT child.status,parent.status,
			(SELECT count(*) FROM app.job_config_snapshots WHERE job_id=child.id)
			FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id WHERE child.id=$1`,
			jobFixture.childID).Scan(&childStatus, &parentStatus, &snapshots); err != nil {
			t.Fatal(err)
		}
		if childStatus != "running" || parentStatus != "running" || snapshots != 1 {
			t.Fatalf("interrupted child/parent/snapshots=%s/%s/%d", childStatus, parentStatus, snapshots)
		}
		_ = tx.Rollback(ctx)
		time.Sleep(runner.leaseDuration + 10*time.Millisecond)
		replacement := testRunner(workerDB, keys, root, io.Discard)
		replacementJob, err := replacement.claimNext(ctx)
		if err != nil || replacementJob.id != jobFixture.childID {
			t.Fatalf("interrupted ingest recovery job=%s err=%v", replacementJob.id, err)
		}
		if _, err := replacement.execute(ctx, replacementJob); err != nil {
			t.Fatal(err)
		}
		tx = beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertTerminalIngest(t, tx, jobFixture, "succeeded", "succeeded")
	})
}

func assertIngestLogResults(t *testing.T, output string, files, processed, ingested int, failed []string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var started, terminal map[string]any
	for _, line := range lines {
		record := decodeLogLine(t, line)
		switch record["msg"] {
		case "job processing started":
			started = record
		case "job processing completed", "job processing failed", "job processing cancelled":
			terminal = record
		}
	}
	if started == nil || terminal == nil {
		t.Fatalf("ingest lifecycle logs incomplete: %s", output)
	}
	if _, ok := started["results"]; ok {
		t.Fatalf("start log contains results: %v", started)
	}
	value, ok := terminal["results"].(map[string]any)
	if !ok || len(value) != 4 || value["files_processed"] != float64(files) ||
		value["workouts_processed"] != float64(processed) || value["workouts_ingested"] != float64(ingested) {
		t.Fatalf("terminal results=%v", terminal["results"])
	}
	actualFailed, ok := value["failed_processing"].([]any)
	if !ok || len(actualFailed) != len(failed) {
		t.Fatalf("failed_processing=%v, want %v", value["failed_processing"], failed)
	}
	for index, name := range failed {
		if actualFailed[index] != name || filepath.Base(name) != name {
			t.Fatalf("failed_processing[%d]=%v, want basename %q", index, actualFailed[index], name)
		}
	}
}

type ingestSourceFixture struct {
	accountID uuid.UUID
	sourceID  uuid.UUID
	root      string
}

type ingestJobFixture struct {
	parentID uuid.UUID
	childID  uuid.UUID
}

func insertIngestSource(t *testing.T, db *pgxpool.Pool, root string) ingestSourceFixture {
	t.Helper()
	fixture := ingestSourceFixture{accountID: uuid.New(), sourceID: uuid.New(), root: root}
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.accounts(id) VALUES($1)`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	name := "ingest-source-" + fixture.sourceID.String()
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
		VALUES($1,$2,$3,$3,'health-auto-export-local','connected',$4)`, fixture.sourceID, fixture.accountID, name, []byte("source-envelope")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func enqueueIngestJob(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture) ingestJobFixture {
	return enqueueIngestJobKind(t, db, keys, fixture, "manual_ingest", "manual_ingest_source")
}

func enqueueIncrementalIngestJob(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture) ingestJobFixture {
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`,
		strings.ToUpper(strings.ReplaceAll(fixture.sourceID.String(), "-", "")))
	return enqueueIngestJobWithParameters(t, db, keys, fixture, "manual_ingest", "manual_ingest_source", parameters)
}

func enqueueIngestJobKind(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture, parentKind, childKind string) ingestJobFixture {
	t.Helper()
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"bounded","startDate":"0001-01-01","endDate":"9999-12-31"}`,
		strings.ToUpper(strings.ReplaceAll(fixture.sourceID.String(), "-", "")))
	return enqueueIngestJobWithParameters(t, db, keys, fixture, parentKind, childKind, parameters)
}

func enqueueIngestJobWithParameters(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture, parentKind, childKind, parameters string) ingestJobFixture {
	t.Helper()
	job := ingestJobFixture{parentID: uuid.New(), childID: uuid.New()}
	canonical := []byte(fmt.Sprintf(`{"version":1,"path":%q}`, fixture.root))
	envelope, err := keys.Encrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: fixture.accountID, RecordID: job.childID,
	}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.jobs(id,account_id,kind,priority)
		VALUES($1,$2,$3,80)`, job.parentID, fixture.accountID, parentKind); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
		VALUES($1,$2,$3,$4,80,$5)`, job.childID, job.parentID, fixture.accountID, childKind, parameters); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3)`,
		job.parentID, job.childID, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_source_contexts
		(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,'integration source','health-auto-export-local')`, job.childID, fixture.accountID, fixture.sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		job.childID, fixture.accountID, fixture.sourceID, envelope); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return job
}

func assertSyntheticWorkoutState(t *testing.T, db *pgxpool.Pool, fixture ingestSourceFixture, duration string, routePoints int) {
	t.Helper()
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer tx.Rollback(context.Background())
	var actualDuration string
	var actualPoints int
	if err := tx.QueryRow(context.Background(), `SELECT workout.provider_duration::text,count(point.sequence)
		FROM app.workouts workout LEFT JOIN app.workout_route_points point
		  ON point.workout_id=workout.id AND point.account_id=workout.account_id
		WHERE workout.source_id=$1 AND workout.provider_id='synthetic-provider'
		GROUP BY workout.provider_duration`, fixture.sourceID).Scan(&actualDuration, &actualPoints); err != nil {
		t.Fatal(err)
	}
	if actualDuration != duration || actualPoints != routePoints {
		t.Fatalf("synthetic workout duration/route points=%s/%d want=%s/%d", actualDuration, actualPoints, duration, routePoints)
	}
}

func copyGoldenIngestFixtures(t *testing.T, target string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	for _, name := range []string{
		"HealthAutoExport-2026-04-11.json",
		"HealthAutoExport-2026-07-15.json",
		"HealthAutoExport-2026-07-20.json",
	} {
		source := filepath.Join(filepath.Dir(currentFile), "..", "data", "samples", name)
		contents, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			t.Skipf("fixture %s is not present", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertGoldenIngest(t *testing.T, db *pgxpool.Pool, fixture ingestSourceFixture, job ingestJobFixture) {
	t.Helper()
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer tx.Rollback(context.Background())
	assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded'`, job.childID)
	assertCountQuery(t, tx, 5, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
	assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.workout_types type WHERE EXISTS (
		SELECT 1 FROM app.workouts workout WHERE workout.workout_type_id=type.id AND workout.source_id=$1)`, fixture.sourceID)
	wantIDs := []string{
		"0ACE8792-3C73-4554-83A4-724434A75279", "3191EA2A-D556-4986-B169-46FF66CB42E1",
		"495C8ABF-1C7C-4C8F-A8DF-9AC2E3E96C1F", "AF30D204-3343-4E43-901A-7A793CD29D64",
		"E98FD4CD-0C14-4951-9128-7BAB9F872819",
	}
	rows, err := tx.Query(context.Background(), `SELECT provider_id FROM app.workouts WHERE source_id=$1 ORDER BY provider_id`, fixture.sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("provider IDs=%v", ids)
	}
	var duration, distance, energy string
	if err := tx.QueryRow(context.Background(), `SELECT workout.provider_duration::text,
		(SELECT value::text FROM app.workout_aggregates WHERE workout_id=workout.id AND metric='distance'),
		(SELECT value::text FROM app.workout_aggregates WHERE workout_id=workout.id AND metric='active_energy_burned')
		FROM app.workouts workout WHERE source_id=$1 AND provider_id='0ACE8792-3C73-4554-83A4-724434A75279'`, fixture.sourceID).
		Scan(&duration, &distance, &energy); err != nil {
		t.Fatal(err)
	}
	if duration != "2195.6228786706924" || distance != "2.9405940629292266" || energy != "122.48453388293568" {
		t.Fatalf("exact numerics=%s/%s/%s", duration, distance, energy)
	}
	assertCountQuery(t, tx, 788, `SELECT count(*) FROM app.workout_route_points point JOIN app.workouts workout
		ON workout.id=point.workout_id AND workout.account_id=point.account_id WHERE workout.source_id=$1`, fixture.sourceID)
	assertCountQuery(t, tx, 6, `SELECT count(*) FROM (
		SELECT point.workout_id,point.recorded_at FROM app.workout_route_points point JOIN app.workouts workout
		ON workout.id=point.workout_id AND workout.account_id=point.account_id WHERE workout.source_id=$1
		GROUP BY point.workout_id,point.recorded_at HAVING count(*)>1) duplicate`, fixture.sourceID)
	assertCountQuery(t, tx, 0, `SELECT count(*) FROM (
		SELECT workout_id,count(*) AS points,max(sequence) AS maximum FROM app.workout_route_points
		GROUP BY workout_id HAVING count(*)<>max(sequence)+1) broken`)
	assertCountQuery(t, tx, 5, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, job.childID)
	var warnings []byte
	if err := tx.QueryRow(context.Background(), `SELECT event.warnings FROM app.workout_import_events event
		JOIN app.workouts workout ON workout.id=event.workout_id AND workout.account_id=event.account_id
		WHERE event.job_id=$1 AND workout.provider_id='0ACE8792-3C73-4554-83A4-724434A75279'`, job.childID).Scan(&warnings); err != nil {
		t.Fatal(err)
	}
	var warningValues []map[string]any
	if err := json.Unmarshal(warnings, &warningValues); err != nil {
		t.Fatal(err)
	}
	foundInvalidCourseAccuracy := false
	for _, warning := range warningValues {
		if warning["code"] == "unexpected_unit" && (warning["field"] == "speed_average" || warning["field"] == "speed_maximum") {
			t.Fatalf("valid provider speed unit produced warning: %s", warnings)
		}
		if warning["code"] == "invalid_optional_route_value" && warning["field"] == "route_course_accuracy" {
			foundInvalidCourseAccuracy = true
		}
	}
	if !foundInvalidCourseAccuracy {
		t.Fatalf("warnings=%s", warnings)
	}
	assertTerminalIngest(t, tx, job, "succeeded", "succeeded")
}

func assertTerminalIngest(t *testing.T, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, job ingestJobFixture, childStatus, parentStatus string) {
	t.Helper()
	var child, parent string
	var snapshots int
	if err := tx.QueryRow(context.Background(), `SELECT status FROM app.jobs WHERE id=$1`, job.childID).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT status FROM app.jobs WHERE id=$1`, job.parentID).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM app.job_config_snapshots WHERE job_id=$1`, job.childID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if child != childStatus || parent != parentStatus || snapshots != 0 {
		t.Fatalf("child/parent/snapshots=%s/%s/%d", child, parent, snapshots)
	}
}

func assertCountQuery(t *testing.T, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, want int, query string, args ...any) {
	t.Helper()
	var count int
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("count=%d, want %d for %s", count, want, query)
	}
}

func assertRanIngest(t *testing.T, runner *Runner, ctx context.Context) {
	t.Helper()
	worked, err := runner.RunOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("worked=%t err=%v", worked, err)
	}
}

func writeSyntheticExport(t *testing.T, path, duration string, routePoints int) {
	t.Helper()
	points := make([]map[string]any, routePoints)
	for index := range points {
		points[index] = map[string]any{
			"timestamp": "2026-08-01 12:00:30 +0000",
			"latitude":  37.0 + float64(index)/1000,
			"longitude": -122.0 - float64(index)/1000,
			"altitude":  10.0 + float64(index),
		}
	}
	document := map[string]any{"data": map[string]any{"workouts": []any{map[string]any{
		"id": "synthetic-provider", "name": "Outdoor Walk",
		"start": "2026-08-01 12:00:00 +0000", "end": "2026-08-01 12:02:00 +0000",
		"duration": json.Number(duration), "route": points,
	}}}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
