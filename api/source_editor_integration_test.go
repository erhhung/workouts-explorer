package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSourceEditorRecheckIntegration(t *testing.T) {
	apiURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if apiURL == "" || migrationURL == "" {
		t.Skip("requires source integration databases")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	s := integrationServer(t, db, &recordingSender{})
	p, account := insertSourceTestUser(t, owner)
	token := insertTestSession(t, db, p, "bearer", "")
	create := callEndpoint(http.MethodPost, "/api/sources", `{"displayName":"Editor fixture","type":"health-auto-export-local","autoSyncEnabled":false,"config":{"version":1,"path":"/data/workouts/inbox"}}`, token, "")
	s.CreateSource(create.recorder, create.request, generated.CreateSourceParams{})
	if create.recorder.Code != http.StatusCreated {
		t.Fatalf("create %d %s", create.recorder.Code, create.recorder.Body.String())
	}
	var source generated.Source
	if err := json.Unmarshal(create.recorder.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	id, _ := parseCompactUUID(source.Id)
	for _, check := range []bool{false, true, false} {
		call := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id+"?checkConnection=true", `{"displayName":"Editor fixture","config":{"version":1,"path":"/data/workouts/inbox"}}`, token, "")
		s.UpdateSource(call.recorder, call.request, source.Id, generated.UpdateSourceParams{CheckConnection: &check})
		if call.recorder.Code != http.StatusOK {
			t.Fatalf("save %d %s", call.recorder.Code, call.recorder.Body.String())
		}
		if err := json.Unmarshal(call.recorder.Body.Bytes(), &source); err != nil {
			t.Fatal(err)
		}
		if source.AutoSyncEnabled {
			t.Fatal("editor changed Auto Sync")
		}
		if check && source.Generation != 2 {
			t.Fatal("explicit unchanged-config Save did not increment generation")
		}
	}
	assertSourceGenerationAndJobs(t, s, account, id, 2, 2)
}

func TestSourceDailyTimeEditingIntegration(t *testing.T) {
	apiURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if apiURL == "" || migrationURL == "" {
		t.Skip("requires disposable source databases")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	s := integrationServer(t, db, &recordingSender{})
	principal, account := insertSourceTestUser(t, owner)
	token := insertTestSession(t, db, principal, "bearer", "")
	sources := make([]generated.Source, 2)
	for index, clock := range []string{"08:15", "21:45"} {
		create := callEndpoint(http.MethodPost, "/api/sources", fmt.Sprintf(`{"displayName":"Daily %d","type":"health-auto-export-local","autoSyncEnabled":true,"autoSyncTime":%q,"config":{"version":1,"path":"/data/workouts/inbox"}}`, index, clock), token, "")
		s.CreateSource(create.recorder, create.request, generated.CreateSourceParams{})
		if create.recorder.Code != http.StatusCreated {
			t.Fatalf("create %d: %s", create.recorder.Code, create.recorder.Body.String())
		}
		if err := json.Unmarshal(create.recorder.Body.Bytes(), &sources[index]); err != nil {
			t.Fatal(err)
		}
		if sources[index].AutoSyncTime == nil || *sources[index].AutoSyncTime != clock {
			t.Fatal("daily time missing from source response")
		}
	}
	readTimer := func(source generated.Source) time.Time {
		t.Helper()
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
			t.Fatal(err)
		}
		id, _ := parseCompactUUID(source.Id)
		var next time.Time
		if err := tx.QueryRow(ctx, `SELECT next_run_at FROM app.source_sync_schedules WHERE source_id=$1`, id).Scan(&next); err != nil {
			t.Fatal(err)
		}
		return next
	}
	otherNext := readTimer(sources[1])
	patch := callEndpoint(http.MethodPatch, "/api/sources/"+sources[0].Id, `{"autoSyncTime":"00:00"}`, token, "")
	s.UpdateSource(patch.recorder, patch.request, sources[0].Id, generated.UpdateSourceParams{})
	if patch.recorder.Code != http.StatusOK {
		t.Fatalf("patch %d: %s", patch.recorder.Code, patch.recorder.Body.String())
	}
	var saved generated.Source
	if err := json.Unmarshal(patch.recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.AutoSyncTime == nil || *saved.AutoSyncTime != "00:00" || saved.Generation != 1 {
		t.Fatal("schedule-only edit changed connection generation or lost midnight")
	}
	if !readTimer(sources[1]).Equal(otherNext) {
		t.Fatal("editing one daily time changed another source")
	}
	firstID, _ := parseCompactUUID(sources[0].Id)
	assertSourceGenerationAndJobs(t, s, account, firstID, 1, 1)
	for _, invalid := range []string{"24:00", "12:60", "2:30", "12:30 PM", ""} {
		call := callEndpoint(http.MethodPatch, "/api/sources/"+saved.Id, fmt.Sprintf(`{"autoSyncTime":%q}`, invalid), token, "")
		s.UpdateSource(call.recorder, call.request, saved.Id, generated.UpdateSourceParams{})
		if call.recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid daily time %q: %d", invalid, call.recorder.Code)
		}
	}
	foreign, _ := insertSourceTestUser(t, owner)
	foreignToken := insertTestSession(t, db, foreign, "bearer", "")
	call := callEndpoint(http.MethodPatch, "/api/sources/"+saved.Id, `{"autoSyncTime":"10:00"}`, foreignToken, "")
	s.UpdateSource(call.recorder, call.request, saved.Id, generated.UpdateSourceParams{})
	if call.recorder.Code != http.StatusNotFound {
		t.Fatal("foreign owner changed a source schedule")
	}
	transaction, err := s.accountTransaction(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(ctx)
	if _, err := transaction.Exec(ctx, `UPDATE app.preferences SET timezone='Asia/Kolkata'`); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var localMidnight string
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT to_char(next_run_at AT TIME ZONE 'Asia/Kolkata','HH24:MI') FROM app.source_sync_schedules WHERE source_id=$1`, firstID).Scan(&localMidnight); err != nil || localMidnight != "00:00" {
		t.Fatalf("timezone change lost local schedule: %s %v", localMidnight, err)
	}
}
