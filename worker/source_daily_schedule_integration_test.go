package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
)

func TestIndependentDailySourceSchedulesIntegration(t *testing.T) {
	workerURL, migrationURL := os.Getenv("WORKER_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if workerURL == "" || migrationURL == "" {
		t.Skip("requires disposable scheduler databases")
	}
	ctx := context.Background()
	db, owner := openWorkerTestPool(t, ctx, workerURL), openWorkerTestPool(t, ctx, migrationURL)
	keys := testKeyring(t)
	scheduler := NewScheduler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), keys, []string{"/synthetic"}, SchedulerOptions{PollInterval: time.Minute, Lease: 5 * time.Minute})
	account := uuid.New()
	first, second := uuid.New(), uuid.New()
	tx := beginWorkerTestAccount(t, owner, account)
	if _, err := tx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1);`, account); err != nil {
		t.Fatal(err)
	}
	for index, id := range []uuid.UUID{first, second} {
		envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: account, RecordID: id}, []byte(`{"version":1,"path":"/synthetic/exports"}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,auto_sync_time,config_envelope)
			VALUES($1,$2,$3,$3,'health-auto-export-local','connected',true,$4,$5)`, id, account, id.String(), []string{"08:00", "18:30"}[index], envelope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at) VALUES($1,clock_timestamp()+interval '1 day')`, account); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	forceDue := func(id uuid.UUID) {
		t.Helper()
		tx := beginWorkerTestAccount(t, owner, account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.source_sync_schedules SET next_run_at=clock_timestamp()-interval '1 hour' WHERE source_id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, sourceID := range []uuid.UUID{first, second} {
		forceDue(sourceID)
		claimed, token, err := scheduler.claim(ctx)
		if err != nil || claimed != account {
			t.Fatalf("claim account=%s error=%v", claimed, err)
		}
		if _, err := scheduler.enqueue(ctx, account, uuid.New()); err == nil {
			t.Fatal("foreign lease could enqueue timed sources")
		}
		parent, err := scheduler.enqueue(ctx, account, token)
		if err != nil || parent == nil {
			t.Fatalf("enqueue source %s: %v", sourceID, err)
		}
		tx := beginWorkerTestAccount(t, owner, account)
		var count int
		var actual uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT count(*),min(source_id::text)::uuid FROM app.job_source_contexts WHERE job_id IN (SELECT id FROM app.jobs WHERE parent_job_id=$1)`, *parent).Scan(&count, &actual); err != nil {
			t.Fatal(err)
		}
		if count != 1 || actual != sourceID {
			t.Fatal("scheduler included a source whose independent time was not due")
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
			t.Fatal(err)
		}
		var next time.Time
		if err := tx.QueryRow(ctx, `SELECT next_run_at FROM app.source_sync_schedules WHERE source_id=$1`, sourceID).Scan(&next); err != nil || !next.After(time.Now()) {
			t.Fatalf("daily timer did not advance: %v", err)
		}
		_ = tx.Rollback(ctx)
	}
	// A legacy account-cadence wake must not include either timed source again.
	tx = beginWorkerTestAccount(t, owner, account)
	if _, err := tx.Exec(ctx, `UPDATE app.account_sync_schedules SET next_run_at='-infinity' WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, token, err := scheduler.claim(ctx)
	if err != nil || claimed != account {
		t.Fatalf("legacy wake: %v", err)
	}
	if parent, err := scheduler.enqueue(ctx, account, token); err != nil || parent != nil {
		t.Fatalf("timed sources leaked into legacy wake: %v", err)
	}
}

func TestDailySourceScheduleDSTIntegration(t *testing.T) {
	url := os.Getenv("MIGRATION_DATABASE_URL")
	if url == "" {
		t.Skip("requires disposable scheduler database")
	}
	ctx := context.Background()
	db := openWorkerTestPool(t, ctx, url)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ clock, after, want string }{
		{"02:30", "2026-03-08T08:00:00Z", "2026-03-08T10:30:00Z"},
		{"02:30", "2026-03-08T11:00:00Z", "2026-03-09T09:30:00Z"},
		{"01:30", "2026-11-01T07:00:00Z", "2026-11-01T09:30:00Z"},
		{"01:30", "2026-11-01T09:31:00Z", "2026-11-02T09:30:00Z"},
		{"00:00", "2026-11-23T08:00:00Z", "2026-11-24T08:00:00Z"},
	} {
		var next time.Time
		if err := tx.QueryRow(ctx, `SELECT app.next_daily_sync_run($1,'America/Los_Angeles',$2::timestamptz)`, fixture.clock, fixture.after).Scan(&next); err != nil || next.UTC().Format(time.RFC3339) != fixture.want {
			t.Fatalf("next %s after %s: %s, error=%v", fixture.clock, fixture.after, next, err)
		}
	}
}
