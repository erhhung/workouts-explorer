package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGPSPaceReadsAndFiltersIntegration(t *testing.T) {
	apiURL, ownerURL, workerURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("WORKER_DATABASE_URL")
	if apiURL == "" || ownerURL == "" || workerURL == "" {
		t.Skip("disposable workout databases required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	worker, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	principal, account := insertSourceTestUser(t, owner)
	fixture := insertWorkoutReadFixtures(t, owner, worker, account)
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE app.workout_routes SET pace_distance_meters=1500,pace_elapsed_seconds=650,
        fastest_kilometer_split_seconds=300,slowest_kilometer_split_seconds=500,
        fastest_mile_split_seconds=697.3824,slowest_mile_split_seconds=697.3824 WHERE account_id=$1 AND workout_id=$2`, account, fixture.workouts[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	server := integrationServer(t, apiDB, &recordingSender{})
	handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	token := insertTestSession(t, apiDB, principal, "bearer", "")
	for _, path := range []string{
		"/api/workouts?startDate=2026-03-07&endDate=2026-03-09&sort=pace:desc",
		"/api/workouts?startDate=2026-03-07&endDate=2026-03-09&filters=%7B%22pace%22%3A%7B%22operator%22%3A%22gt%22%2C%22value%22%3A%227.2%22%7D%7D",
	} {
		response := routeOwnerRead(handler, path, token)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var list generated.WorkoutList
		if err = json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) == 0 {
			t.Fatal(err)
		}
		item := list.Items[0]
		pace, err := item.Pace.Get()
		if err != nil {
			t.Fatal(err)
		}
		value, _ := strconv.ParseFloat(pace.Value, 64)
		if item.Id != compactUUID(fixture.workouts[0]) || math.Abs(value-650.0/1500*1000/60) > 1e-6 {
			t.Fatal("read/filter/sort did not use weighted GPS pace", pace, list.Items)
		}
		rangeKM, err := item.SplitPaces.Kilometer.Get()
		if err != nil || rangeKM.FastestSeconds != "300" || rangeKM.SlowestSeconds != "500" {
			t.Fatal("range mismatch", rangeKM, err)
		}
		if item.Duration != "240.125" {
			t.Fatal("source duration changed")
		}
		distance, _ := item.Distance.Get()
		if distance.Value != "5.5" {
			t.Fatal("source distance changed")
		}
	}
}
