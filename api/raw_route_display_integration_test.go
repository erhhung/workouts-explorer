package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRawRouteDisplayExportsIntegration(t *testing.T) {
	if os.Getenv("API_DATABASE_URL") == "" {
		t.Skip("disposable PostgreSQL required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, os.Getenv("API_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	admin, err := pgxpool.New(ctx, os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	worker, err := pgxpool.New(ctx, os.Getenv("WORKER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	for _, test := range []struct {
		name                        string
		cycling, stationary, allBad bool
		scenario                    string
	}{
		{name: "excursion endpoints and connecting lines removed"}, {name: "cycling outside foot policy", cycling: true}, {name: "stationary components retained", stationary: true}, {name: "all rejected has no export", allBad: true},
		{name: "slow recovery samples between bursts removed", scenario: "recovery"},
		{name: "recovery persists across a recording gap", scenario: "gap"},
		{name: "valid running after a spike is preserved", scenario: "running"},
		{name: "unconfirmed recovery does not expand rejection", scenario: "unresolved"},
		{name: "fast valid running is not globally capped", scenario: "fast"},
		{name: "poor accuracy alone does not remove a route", scenario: "poor"},
		{name: "recovery lookahead cannot exceed its sample budget", scenario: "budget"},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal, account := insertSourceTestUser(t, admin)
			f := insertWorkoutReadFixtures(t, admin, worker, account)
			workout := f.workouts[0]
			if test.cycling {
				workout = f.workouts[1]
			}
			tx := integrationAccountTransaction(t, worker, account)
			defer tx.Rollback(ctx)
			var source uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2,$3)`, f.jobID, f.childLease, database.SupportedSchemaVersion).Scan(&source); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, account, workout); err != nil {
				t.Fatal(err)
			}
			distances := []float64{0, 2, 4, 44, 84, 86, 88}
			accuracies := []float64{1, 1, 1, 1, 1, 1, 1}
			expected := []int{0, 1, 5, 6}
			if test.cycling || test.stationary {
				expected = []int{0, 1, 2, 3, 4, 5, 6}
			}
			if test.stationary {
				distances = []float64{0, 0, 0, 0, 0, 0, 0}
			}
			if test.allBad {
				distances = []float64{0, 40, 80, 120, 160, 200, 240}
			}
			if test.scenario != "" {
				distances = []float64{0}
				accuracies = []float64{1}
				expected = []int{}
				size := 60
				if test.scenario == "budget" {
					size = 700
				}
				if test.scenario == "unresolved" {
					size = 150
				}
				if test.scenario == "fast" || test.scenario == "poor" {
					size = 80
				}
				for i := 1; i < size; i++ {
					speed, accuracy := 1.5, 1.0
					if test.scenario == "fast" {
						speed = 8
						accuracy = 8
					} else if test.scenario == "poor" {
						speed = 2
						accuracy = 10
					} else if i == 36 {
						speed = 45
						accuracy = 10
					} else if i >= 37 && i <= 44 {
						speed = 4
						accuracy = 8
					}
					if test.scenario == "running" && i >= 37 {
						speed = 5
						accuracy = 1
					}
					if test.scenario == "unresolved" && i >= 37 {
						speed = 2
						accuracy = 8
					}
					if test.scenario == "budget" && i >= 37 {
						speed = 2
						accuracy = 8
						if i >= 650 {
							accuracy = 1
						}
					}
					distances = append(distances, distances[i-1]+speed)
					accuracies = append(accuracies, accuracy)
				}
				for i := range distances {
					keep := true
					switch test.scenario {
					case "recovery", "gap":
						keep = i < 35 || i >= 45
					case "running", "unresolved", "budget":
						keep = i != 35 && i != 36
					}
					if keep {
						expected = append(expected, i)
					}
				}
			}
			for i, x := range distances {
				seconds := float64(i)
				if test.scenario == "budget" {
					seconds *= 0.1
					x *= 0.1
				}
				if test.scenario == "gap" && i >= 38 {
					seconds += 15
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,longitude,latitude,altitude,horizontal_accuracy)
   VALUES($1,$2,$3::integer,'2026-03-07 15:00Z'::timestamptz+$7::double precision*interval '1 second',$4,0,$5,$6)`, account, workout, i, x/111195.0802335329, 1600+float64(i), accuracies[i], seconds); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.Exec(ctx, `SELECT app.replace_workout_route_summary($1,$2,0,0,1,1,1600,2000,6,true)`, workout, len(distances)); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			bearer := insertTestSession(t, apiDB, principal, "bearer", "")
			server := integrationServer(t, apiDB, &recordingSender{})
			handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/workouts/" + compactUUID(workout)
			pointsResponse := routeOwnerRead(handler, path+"/route/points", bearer)
			geoResponse := routeOwnerRead(handler, path+"/route", bearer)
			if test.allBad {
				if pointsResponse.Code != http.StatusConflict || geoResponse.Code != http.StatusConflict {
					t.Fatal("rejected points leaked", pointsResponse.Body, geoResponse.Body)
				}
				return
			}
			if pointsResponse.Code != 200 || geoResponse.Code != 200 {
				t.Fatal(pointsResponse.Body, geoResponse.Body)
			}
			validateRecordedResponse(t, http.MethodGet, path+"/route/points", pointsResponse)
			validateRecordedResponse(t, http.MethodGet, path+"/route", geoResponse)
			var points generated.WorkoutPointsExport
			var geo generated.WorkoutGeoJSONFeature
			if err := json.Unmarshal(pointsResponse.Body.Bytes(), &points); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(geoResponse.Body.Bytes(), &geo); err != nil {
				t.Fatal(err)
			}
			actual := []int{}
			for _, p := range points.Points {
				actual = append(actual, p.Sequence)
			}
			if !reflect.DeepEqual(actual, expected) || geo.Properties.PointCount != len(expected) {
				t.Fatal("render/export membership mismatch", actual, expected)
			}
			read := integrationAccountTransaction(t, admin, account)
			defer read.Rollback(ctx)
			var count int
			var equal bool
			geometry, _ := geo.Geometry.MarshalJSON()
			if err := read.QueryRow(ctx, `SELECT count(*) FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, account, workout).Scan(&count); err != nil || count != len(distances) {
				t.Fatal("evidence changed", count, err)
			}
			if err := read.QueryRow(ctx, `SELECT ST_Equals(route,ST_Force2D(ST_GeomFromGeoJSON($3::text))) AND ST_NPoints(route)=$4 FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, account, workout, string(geometry), len(expected)).Scan(&equal); err != nil || !equal {
				t.Fatal("stored rendered geometry/export mismatch", err)
			}
			if test.scenario == "" && !test.cycling && !test.stationary {
				parts, err := geo.Geometry.AsGeoJSONMultiLineString()
				if err != nil || len(parts.Coordinates) != 2 || points.Points[1].Component == points.Points[2].Component {
					t.Fatal("gap reconnected", err)
				}
				e, _ := geo.Properties.ElevationSummary.Get()
				if e.GainMeters != 2 {
					t.Fatal("elevation bridged rejected samples", e)
				}
			}
			var denied int
			foreignPrincipal, foreignAccount := insertSourceTestUser(t, admin)
			_ = foreignPrincipal
			other := integrationAccountTransaction(t, apiDB, foreignAccount)
			defer other.Rollback(ctx)
			if err := other.QueryRow(ctx, `SELECT count(*) FROM app.raw_route_display_points($1,$2)`, account, workout).Scan(&denied); err != nil || denied != 0 {
				t.Fatal("cross-account point disclosure", fmt.Sprint(err))
			}
		})
	}
}
