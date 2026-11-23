package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMapSelectionFitUsesDisplayedGeometryIntegration(t *testing.T) {
	url, apiURL, workerURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("API_DATABASE_URL"), os.Getenv("WORKER_DATABASE_URL")
	if url == "" || apiURL == "" || workerURL == "" {
		t.Skip("owned route-fit fixture required")
	}
	if !strings.Contains(url, "workouts_admin_test_") {
		t.Fatal("refusing non-test fit fixture")
	}
	ctx := context.Background()
	connect := func(url string) *pgxpool.Pool {
		p, e := pgxpool.New(ctx, url)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(p.Close)
		return p
	}
	owner, apiDB, worker := connect(url), connect(apiURL), connect(workerURL)
	principal, account := insertSourceTestUser(t, owner)
	fixture := insertWorkoutReadFixtures(t, owner, worker, account)
	server := integrationServer(t, apiDB, &recordingSender{})
	handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	response := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"startDate":"2026-03-07","endDate":"2026-03-09"}`, bearer)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Code, response.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/map-selections", response)
	var selection generated.MapSelection
	if err = json.Unmarshal(response.Body.Bytes(), &selection); err != nil || len(selection.Workouts) != 2 {
		t.Fatal(err)
	}
	for _, workout := range selection.Workouts {
		box := workout.Bounds
		// Imported extrema are deliberately wider than the drawn fixture lines.
		if workout.Id == compactUUID(fixture.workouts[0]) && (box.MaximumLatitude > 39.901 || box.MaximumLongitude > -105.099) {
			t.Fatal("2D route fits undrawn observations", box)
		}
		if workout.Id == compactUUID(fixture.workouts[1]) && (box.MaximumLatitude > 40.001 || box.MaximumLongitude > -104.999) {
			t.Fatal("3D route fits undrawn observations", box)
		}
	}
	var storedNorth float64
	if err = owner.QueryRow(ctx, `SELECT maximum_latitude FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, account, fixture.workouts[1]).Scan(&storedNorth); err != nil || storedNorth != 40.1 {
		t.Fatal("fit correction modified imported metadata", storedNorth, err)
	}
	box, err := selection.Bounds.Get()
	if err != nil || box.MaximumLatitude > 40.001 {
		t.Fatal("aggregate fit still contains imported extrema", box, err)
	}
	t.Log("owner-scoped map selection uses visible 2D/3D bounds without changing raw observations/metadata")
}
