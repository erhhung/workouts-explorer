package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunnerPostgreSQL(t *testing.T) {
	workerURL, migrationURL := os.Getenv("WORKER_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if workerURL == "" || migrationURL == "" {
		t.Skip("WORKER_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	workerDB := openWorkerTestPool(t, ctx, workerURL)
	setupDB := openWorkerTestPool(t, ctx, migrationURL)
	keys := testKeyring(t)
	root := t.TempDir()
	validPath := filepath.Join(root, "valid")
	if err := os.Mkdir(validPath, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("success and snapshot cleanup", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		worked, err := runner.RunOnce(ctx)
		if err != nil || !worked {
			t.Fatalf("worked=%t err=%v", worked, err)
		}
		assertWorkerState(t, setupDB, fixture, "connected", 1, "succeeded", 0)
		if !strings.Contains(logs.String(), `"owner":"`+fixture.owner+`"`) ||
			!strings.Contains(logs.String(), `"source_name":"`+fixture.sourceName+`"`) ||
			!strings.Contains(logs.String(), `"source_type":"health-auto-export-local"`) ||
			!strings.Contains(logs.String(), `"msg":"job processing started"`) ||
			!strings.Contains(logs.String(), `"msg":"job processing completed"`) ||
			!strings.Contains(logs.String(), `"job_status":"succeeded"`) ||
			strings.Contains(logs.String(), `"owner_name"`) || strings.Contains(logs.String(), `"source":`) ||
			strings.Contains(logs.String(), `"results"`) {
			t.Fatalf("structured lifecycle context is incomplete: %s", logs.String())
		}
	})

	t.Run("missing logging metadata does not abandon claim", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		var principalID uuid.UUID
		if err := tx.QueryRow(ctx, `DELETE FROM app.users WHERE account_id=$1 RETURNING principal_id`, fixture.accountID).Scan(&principalID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.authentication_principals WHERE id=$1`, principalID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		worked, err := runner.RunOnce(ctx)
		if err != nil || !worked {
			t.Fatalf("worked=%t err=%v", worked, err)
		}
		assertWorkerState(t, setupDB, fixture, "connected", 1, "succeeded", 0)
		if !strings.Contains(logs.String(), `"msg":"job logging context unavailable"`) ||
			!strings.Contains(logs.String(), `"owner":"unknown owner"`) ||
			!strings.Contains(logs.String(), `"source_name":"unknown source"`) ||
			!strings.Contains(logs.String(), `"source_type":"unknown"`) {
			t.Fatalf("missing metadata fallback was not logged safely: %s", logs.String())
		}
	})

	for _, test := range []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "inaccessible path", path: func(*testing.T) string { return filepath.Join(root, "missing-private-value") }},
		{name: "symlink path", path: func(t *testing.T) string {
			link := filepath.Join(root, "private-symlink-"+uuid.NewString())
			if err := os.Symlink(validPath, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
	} {
		t.Run(test.name+" and redaction", func(t *testing.T) {
			path := test.path(t)
			fixture := insertConnectionFixture(t, setupDB, keys, root, path)
			var logs bytes.Buffer
			runner := testRunner(workerDB, keys, root, &logs)
			worked, err := runner.RunOnce(ctx)
			if err != nil || !worked {
				t.Fatalf("worked=%t err=%v", worked, err)
			}
			status, generation, jobStatus, snapshots, code, summary := readWorkerState(t, setupDB, fixture)
			if status != "connection-failed" || generation != 1 || jobStatus != "failed" || snapshots != 0 ||
				code != "source-unavailable" || summary != "Source directory is not accessible." {
				t.Fatalf("source=%s generation=%d job=%s snapshots=%d code=%q summary=%q",
					status, generation, jobStatus, snapshots, code, summary)
			}
			if strings.Contains(logs.String(), path) || strings.Contains(logs.String(), string(fixture.envelope)) {
				t.Fatal("worker log contains source path or snapshot envelope")
			}
			if !strings.Contains(logs.String(), `"msg":"job processing failed"`) ||
				!strings.Contains(logs.String(), `"error":"job failed: source-unavailable"`) ||
				!strings.Contains(logs.String(), `"job_status":"failed"`) {
				t.Fatalf("terminal source failure was not logged as an error: %s", logs.String())
			}
		})
	}

	t.Run("stale generation is discarded", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		runner := testRunner(workerDB, keys, root, io.Discard)
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		if _, err := tx.Exec(ctx, `UPDATE app.sources SET generation=generation+1 WHERE id=$1`, fixture.sourceID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := runner.execute(ctx, job); err != nil {
			t.Fatal(err)
		}
		assertWorkerState(t, setupDB, fixture, "checking-connection", 2, "succeeded", 0)
	})

	t.Run("lease loss does not update or clean up", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		runner.leaseDuration = 5 * time.Millisecond
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		runner.loadJobLogContext(ctx, &job)
		time.Sleep(10 * time.Millisecond)
		replacement := testRunner(workerDB, keys, root, io.Discard)
		replacementJob, err := replacement.claimNext(ctx)
		if err != nil || replacementJob.id != fixture.jobID {
			t.Fatalf("expired lease was not reclaimed: job=%s err=%v", replacementJob.id, err)
		}
		if err := runner.executeLogged(ctx, job, runner.execute); !errors.Is(err, errJobInterrupted) {
			t.Fatalf("lease loss error=%v", err)
		}
		assertWorkerState(t, setupDB, fixture, "checking-connection", 1, "running", 1)
		if !strings.Contains(logs.String(), `"msg":"job processing outcome unavailable"`) ||
			!strings.Contains(logs.String(), `"job_status":"running"`) ||
			strings.Contains(logs.String(), `"msg":"job processing completed"`) {
			t.Fatalf("reclaimed job lifecycle was logged inaccurately: %s", logs.String())
		}
		if !workerCallBool(t, workerDB, fixture.accountID, `SELECT app.finish_job($1,$2,$3,'cancelled')`,
			fixture.jobID, replacement.workerID, replacementJob.lease) {
			t.Fatal("replacement lease cleanup failed")
		}
	})

	t.Run("database cancellation wins", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		var logs bytes.Buffer
		runner := testRunner(workerDB, keys, root, &logs)
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		runner.loadJobLogContext(ctx, &job)
		if !workerCallBool(t, setupDB, fixture.accountID, `SELECT app.request_job_cancellation($1,$2)`, fixture.jobID, uuid.New()) {
			t.Fatal("cancellation request failed")
		}
		if err := runner.executeLogged(ctx, job, runner.execute); err != nil {
			t.Fatal(err)
		}
		assertWorkerState(t, setupDB, fixture, "checking-connection", 1, "cancelled", 0)
		if !strings.Contains(logs.String(), `"msg":"job processing cancelled"`) ||
			!strings.Contains(logs.String(), `"job_status":"cancelled"`) ||
			strings.Contains(logs.String(), `"msg":"job processing completed"`) {
			t.Fatalf("cancelled job lifecycle was logged inaccurately: %s", logs.String())
		}
	})

	t.Run("context cancellation leaves lease recoverable", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		runner := testRunner(workerDB, keys, root, io.Discard)
		runner.leaseDuration = 5 * time.Millisecond
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := runner.execute(cancelled, job); !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown interruption error=%v", err)
		}
		assertWorkerState(t, setupDB, fixture, "checking-connection", 1, "running", 1)
		time.Sleep(runner.leaseDuration + 10*time.Millisecond)
		replacement := testRunner(workerDB, keys, root, io.Discard)
		replacementJob, err := replacement.claimNext(ctx)
		if err != nil || replacementJob.id != fixture.jobID {
			t.Fatalf("shutdown job was not recoverable: job=%s err=%v", replacementJob.id, err)
		}
		if _, err := replacement.execute(ctx, replacementJob); err != nil {
			t.Fatal(err)
		}
		assertWorkerState(t, setupDB, fixture, "connected", 1, "succeeded", 0)
	})

	t.Run("cross-account completion is fenced", func(t *testing.T) {
		fixture := insertConnectionFixture(t, setupDB, keys, root, validPath)
		runner := testRunner(workerDB, keys, root, io.Discard)
		job, err := runner.claimNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		foreign := job
		foreign.accountID = uuid.New()
		finished, updated, err := runner.complete(ctx, foreign, connectedResult())
		if err != nil || finished || updated {
			t.Fatalf("finished=%t updated=%t err=%v", finished, updated, err)
		}
		assertWorkerState(t, setupDB, fixture, "checking-connection", 1, "running", 1)
		if _, err := runner.execute(ctx, job); err != nil {
			t.Fatal(err)
		}
		assertWorkerState(t, setupDB, fixture, "connected", 1, "succeeded", 0)
	})
}

type connectionFixture struct {
	accountID  uuid.UUID
	sourceID   uuid.UUID
	jobID      uuid.UUID
	envelope   []byte
	sourceName string
	owner      string
}

func insertConnectionFixture(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, root, path string) connectionFixture {
	t.Helper()
	fixture := connectionFixture{accountID: uuid.New(), sourceID: uuid.New(), jobID: uuid.New()}
	fixture.sourceName = "worker-source-" + fixture.sourceID.String()
	plaintext := []byte(fmt.Sprintf(`{"version":1,"path":%q}`, path))
	var err error
	fixture.envelope, err = keys.Encrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: fixture.accountID, RecordID: fixture.jobID,
	}, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.accounts(id) VALUES($1)`, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	principalID := uuid.New()
	identity := strings.ReplaceAll(principalID.String(), "-", "")
	fixture.owner = "worker-" + identity
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.authentication_principals
		(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Worker Test Owner')`,
		principalID, fixture.owner, "worker-"+identity+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principalID, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.sources
		(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,$3,$3,'health-auto-export-local',$4)`, fixture.sourceID, fixture.accountID,
		fixture.sourceName, []byte("source-envelope")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.jobs
		(id,account_id,kind,priority) VALUES($1,$2,'source_connection_check',32767)`, fixture.jobID, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.job_config_snapshots
		(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`,
		fixture.jobID, fixture.accountID, fixture.sourceID, fixture.envelope); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func testRunner(db *pgxpool.Pool, keys *sourcecrypto.Keyring, root string, output io.Writer) *Runner {
	return NewRunner(db, slog.New(slog.NewJSONHandler(output, nil)), keys, []string{root})
}

func openWorkerTestPool(t *testing.T, ctx context.Context, databaseURL string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func beginWorkerTestAccount(t *testing.T, db *pgxpool.Pool, accountID uuid.UUID) pgx.Tx {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	return tx
}

func workerCallBool(t *testing.T, db *pgxpool.Pool, accountID uuid.UUID, query string, args ...any) bool {
	t.Helper()
	tx := beginWorkerTestAccount(t, db, accountID)
	defer tx.Rollback(context.Background())
	var result bool
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertWorkerState(t *testing.T, db *pgxpool.Pool, fixture connectionFixture, sourceStatus string, generation int64, jobStatus string, snapshots int) {
	t.Helper()
	status, actualGeneration, actualJob, actualSnapshots, _, _ := readWorkerState(t, db, fixture)
	if status != sourceStatus || actualGeneration != generation || actualJob != jobStatus || actualSnapshots != snapshots {
		t.Fatalf("source=%s generation=%d job=%s snapshots=%d, want %s/%d/%s/%d",
			status, actualGeneration, actualJob, actualSnapshots, sourceStatus, generation, jobStatus, snapshots)
	}
}

func readWorkerState(t *testing.T, db *pgxpool.Pool, fixture connectionFixture) (string, int64, string, int, string, string) {
	t.Helper()
	tx := beginWorkerTestAccount(t, db, fixture.accountID)
	defer tx.Rollback(context.Background())
	var sourceStatus, jobStatus string
	var generation int64
	var code, summary *string
	if err := tx.QueryRow(context.Background(), `SELECT status,generation,status_code,status_summary FROM app.sources WHERE id=$1`, fixture.sourceID).
		Scan(&sourceStatus, &generation, &code, &summary); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT status FROM app.jobs WHERE id=$1`, fixture.jobID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	var snapshots int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM app.job_config_snapshots WHERE job_id=$1`, fixture.jobID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	return sourceStatus, generation, jobStatus, snapshots, stringValue(code), stringValue(summary)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
