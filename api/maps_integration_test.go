package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMapSelectionSessionIsolationIntegration(t *testing.T) {
	apiURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	workerURL, tileURL := os.Getenv("WORKER_DATABASE_URL"), os.Getenv("TILE_DATABASE_URL")
	if apiURL == "" || migrationURL == "" || workerURL == "" || tileURL == "" {
		t.Skip("API_DATABASE_URL, MIGRATION_DATABASE_URL, WORKER_DATABASE_URL, and TILE_DATABASE_URL are required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	workerDB, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	tileDB, err := pgxpool.New(ctx, tileURL)
	if err != nil {
		t.Fatal(err)
	}
	defer tileDB.Close()

	principalID, accountID := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, accountID)
	firstBearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	secondBearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	created := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"startDate":"2026-03-07","endDate":"2026-03-09"}`, firstBearer)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/map-selections", created)
	var selection generated.MapSelection
	if err := json.Unmarshal(created.Body.Bytes(), &selection); err != nil {
		t.Fatal(err)
	}
	if len(selection.Workouts) != 2 || selection.Workouts[0].Id != compactUUID(fixture.workouts[0]) || selection.Workouts[1].Id != compactUUID(fixture.workouts[1]) || selection.Bounds.IsNull() {
		t.Fatalf("unexpected selection: %#v", selection)
	}
	if !strings.Contains(selection.RouteTileUrl, "/route-tiles/") || !strings.HasSuffix(selection.RouteTileUrl, "/{z}/{x}/{y}.pbf") {
		t.Fatalf("unexpected tile URL %q", selection.RouteTileUrl)
	}
	var sessionID string
	if err := adminDB.QueryRow(ctx, `SELECT session_id::text FROM app.map_selections WHERE account_id=$1 AND id=$2`, accountID, selection.Id).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	var tile []byte
	if err := tileDB.QueryRow(ctx, `SELECT app.raw_route_mvt(0,0,0,$1,$2,$3,$4)`, accountID, sessionID, selection.Id, selection.DataGeneration).Scan(&tile); err != nil {
		t.Fatalf("tile role could not execute the approved MVT function: %v", err)
	}
	if len(tile) == 0 {
		t.Fatal("approved world tile did not contain selected route features")
	}

	selectionPath := "/api/map-selections/" + selection.Id
	foreignSessionDelete := routeMapRequest(handler, http.MethodDelete, selectionPath, "", secondBearer)
	if foreignSessionDelete.Code != http.StatusNoContent {
		t.Fatalf("foreign-session delete status=%d", foreignSessionDelete.Code)
	}
	var remaining int
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM app.map_selections WHERE account_id=$1 AND id=$2`, accountID, selection.Id).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("foreign session changed selection: count=%d err=%v", remaining, err)
	}
	ownedDelete := routeMapRequest(handler, http.MethodDelete, selectionPath, "", firstBearer)
	if ownedDelete.Code != http.StatusNoContent {
		t.Fatalf("owned delete status=%d body=%s", ownedDelete.Code, ownedDelete.Body.String())
	}
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM app.map_selections WHERE account_id=$1 AND id=$2`, accountID, selection.Id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("owned selection remains: count=%d err=%v", remaining, err)
	}

	missing := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"startDate":"2026-03-07","endDate":"2026-03-09","workoutIds":["FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"]}`, firstBearer)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing workout status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func routeMapRequest(handler http.Handler, method, path, body, bearer string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
