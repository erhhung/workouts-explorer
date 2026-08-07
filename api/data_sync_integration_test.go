package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDataSyncScheduleFieldsAndTenantIsolation(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	server := integrationServer(t, apiDB, &recordingSender{})
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	principalA, accountA := insertSourceTestUser(t, adminDB)
	principalB, _ := insertSourceTestUser(t, adminDB)
	bearerA := insertTestSession(t, apiDB, principalA, "bearer", "")
	bearerB := insertTestSession(t, apiDB, principalB, "bearer", "")
	source := createIntegrationSource(t, server, bearerA, "Scheduled API source", "/data/workouts/scheduled-api")
	sourceID, _ := parseCompactUUID(source.Id)
	tx := integrationAccountTransaction(t, adminDB, accountA)
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE app.sources SET status='connected',auto_sync_enabled=true,checked_at=transaction_timestamp() WHERE id=$1`, sourceID); err != nil {
		t.Fatal(err)
	}
	nextRun := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	lastEnqueued := nextRun.Add(-time.Hour)
	if _, err := tx.Exec(ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at,last_enqueued_at)
		VALUES($1,$2,$3)`, accountA, nextRun, lastEnqueued); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	direct, err := server.accountTransactionWithOptions(ctx, accountA, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if err != nil {
		t.Fatalf("begin direct repeatable-read data sync transaction: %v", err)
	}
	defer direct.Rollback(ctx)
	var directCadence, directStaleDays int
	var directNext, directLast *time.Time
	var directJob *uuid.UUID
	if err := direct.QueryRow(ctx, `SELECT cadence_seconds,stale_days,next_run_at,last_enqueued_at,last_job_id
		FROM app.read_owned_sync_schedule()`).Scan(&directCadence, &directStaleDays, &directNext, &directLast, &directJob); err != nil {
		t.Fatalf("direct schedule reader: %v", err)
	}
	if directCadence < 300 || directStaleDays < 1 || directNext == nil || directLast == nil || directJob != nil {
		t.Fatalf("direct schedule values cadence=%d stale=%d next=%v last=%v job=%v", directCadence, directStaleDays, directNext, directLast, directJob)
	}
	var evaluated int
	if err := direct.QueryRow(ctx, `SELECT app.evaluate_source_staleness(NULL,$1,clock_timestamp())`, directStaleDays).Scan(&evaluated); err != nil {
		t.Fatalf("direct source staleness evaluation: %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT source.id,source.display_name,source.type,source.status,source.auto_sync_enabled,source.checked_at,
		state.last_sync_started_at,state.last_sync_succeeded_at,state.last_new_export_discovered_at,state.last_new_export_date,state.stale_since
		FROM app.sources source LEFT JOIN app.source_sync_state state ON state.account_id=source.account_id AND state.source_id=source.id
		ORDER BY source.id`)
	if err != nil {
		t.Fatalf("direct sources/freshness query: %v", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var displayName, sourceType, status string
		var autoSync bool
		var checkedAt, startedAt, succeededAt, discoveredAt, exportDate, staleSince *time.Time
		if err := rows.Scan(&id, &displayName, &sourceType, &status, &autoSync, &checkedAt, &startedAt, &succeededAt, &discoveredAt, &exportDate, &staleSince); err != nil {
			rows.Close()
			t.Fatalf("direct sources/freshness scan: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("direct sources/freshness rows: %v", err)
	}
	rows.Close()
	for _, active := range []bool{true, false} {
		if _, err := readDataSyncJob(ctx, direct, active); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("direct data sync job reader active=%t: %v", active, err)
		}
	}
	var notificationCount int64
	if err := direct.QueryRow(ctx, `SELECT count(*) FROM app.notifications
		WHERE state='unresolved' OR (state='remind' AND remind_at<=clock_timestamp())`).Scan(&notificationCount); err != nil {
		t.Fatalf("direct active notification count: %v", err)
	}
	if _, err := readNotifications(ctx, direct, "active", 100, 0); err != nil {
		t.Fatalf("direct active notification reader: %v", err)
	}
	if err := direct.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	read := func(credential string) generated.DataSync {
		call := routeJobRequest(handler, http.MethodGet, "/api/data-sync", "", credential, "", false)
		if call.recorder.Code != http.StatusOK {
			t.Fatalf("data sync status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
		var result generated.DataSync
		if err := json.Unmarshal(call.recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	owned := read(bearerA)
	if !owned.Schedule.Enabled || owned.Schedule.SourceCount != 1 || owned.Schedule.CadenceSeconds < 300 ||
		owned.Schedule.StaleDays < 1 || owned.Schedule.NextRunAt == nil || !owned.Schedule.NextRunAt.Equal(nextRun) ||
		owned.Schedule.LastEnqueuedAt == nil || !owned.Schedule.LastEnqueuedAt.Equal(lastEnqueued) {
		t.Fatalf("owned schedule=%+v", owned.Schedule)
	}
	isolated := read(bearerB)
	if isolated.Schedule.Enabled || isolated.Schedule.SourceCount != 0 || isolated.Schedule.NextRunAt != nil ||
		isolated.Schedule.LastEnqueuedAt != nil || isolated.Schedule.LastJobId != nil {
		t.Fatalf("cross-account schedule exposure=%+v", isolated.Schedule)
	}
}
