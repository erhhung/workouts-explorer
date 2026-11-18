package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

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
