package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCanonicalActivityOwnerReadsPostgreSQL(t *testing.T) {
	if os.Getenv("COVERAGE_ACCEPTANCE_DISPOSABLE") != "true" {
		t.Skip("requires COVERAGE_ACCEPTANCE_DISPOSABLE=true and guarded disposable database URLs")
	}
	var destination string
	for _, name := range []string{"MIGRATION_DATABASE_URL", "API_DATABASE_URL", "WORKER_DATABASE_URL"} {
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u.Scheme != "postgresql" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") ||
			(u.Port() != "15432" && u.Port() != "5432") || (u.Path != "/acceptance_test" && u.Path != "/coverage_test") ||
			(u.RawQuery != "" && u.RawQuery != "sslmode=disable") || u.Fragment != "" {
			t.Fatalf("refusing non-disposable %s (URL omitted)", name)
		}
		if destination != "" && destination != u.Host+u.Path {
			t.Fatal("database URLs must address the same disposable database")
		}
		destination = u.Host + u.Path
	}
	ctx := context.Background()
	pools := make([]*pgxpool.Pool, 3)
	for index, name := range []string{"MIGRATION_DATABASE_URL", "API_DATABASE_URL", "WORKER_DATABASE_URL"} {
		pool, err := pgxpool.New(ctx, os.Getenv(name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		pools[index] = pool
	}
	adminDB, apiDB, workerDB := pools[0], pools[1], pools[2]
	for _, expectedRawAlias := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Cycling", "IndoorRunning", "IndoorWalking", "IndoorBike"} {
		t.Run(expectedRawAlias, func(t *testing.T) {
			principalID, accountID := insertSourceTestUser(t, adminDB)
			fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, accountID)
			typeID := uuid.Must(uuid.NewV7())
			rawKey := healthautoexport.NormalizedTypeKey(expectedRawAlias)
			canonical := healthautoexport.CanonicalActivityLabel(expectedRawAlias)
			tx := integrationAccountTransaction(t, workerDB, accountID)
			defer tx.Rollback(ctx)
			var sourceID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2,$3)`, fixture.jobID, fixture.childLease, database.SupportedSchemaVersion).Scan(&sourceID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,$3,$4)`, typeID, accountID, rawKey, expectedRawAlias); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE app.workouts SET workout_type_id=$2,provider_label=$3 WHERE id=$1`, fixture.workouts[0], typeID, expectedRawAlias); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
			server := integrationServer(t, apiDB, &recordingSender{})
			routerCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			handler, err := NewHandlerContext(routerCtx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/api/workout-types", "/api/workouts?startDate=2026-03-07&endDate=2026-03-09", "/api/summary?startDate=2026-03-07&endDate=2026-03-09"} {
				response := routeOwnerRead(handler, path, bearer)
				if response.Code != http.StatusOK {
					t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
				}
				validateRecordedResponse(t, http.MethodGet, path, response)
				var body any
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var found bool
				var check func(any)
				check = func(value any) {
					switch value := value.(type) {
					case map[string]any:
						if value["key"] == rawKey {
							found = true
							if value["id"] != compactUUID(typeID) || value["displayName"] != canonical {
								t.Fatalf("legacy type identity/display=%v", value)
							}
						}
						for _, child := range value {
							check(child)
						}
					case []any:
						for _, child := range value {
							check(child)
						}
					}
				}
				check(body)
				if !found {
					t.Fatalf("%s did not retain legacy key %s", path, rawKey)
				}
			}
			for _, suffix := range []string{"/route/points", "/route"} {
				path := "/api/workouts/" + compactUUID(fixture.workouts[0]) + suffix
				response := routeOwnerRead(handler, path, bearer)
				if response.Code != http.StatusOK {
					t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
				}
				validateRecordedResponse(t, http.MethodGet, path, response)
				var body struct {
					WorkoutType string `json:"workoutType"`
					Properties  struct {
						WorkoutType string `json:"workoutType"`
					} `json:"properties"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || (body.WorkoutType != canonical && body.Properties.WorkoutType != canonical) {
					t.Fatalf("export display=%+v err=%v", body, err)
				}
			}
			response := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"startDate":"2026-03-07","endDate":"2026-03-09"}`, bearer)
			var selection generated.MapSelection
			if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &selection) != nil || len(selection.Workouts) != 2 {
				t.Fatalf("selection status=%d body=%s", response.Code, response.Body.String())
			}
			if got := selection.Workouts[0].Type; got.Id != compactUUID(typeID) || got.Key != rawKey || got.Name != canonical {
				t.Fatalf("legacy map type=%+v", got)
			}
			// Reads must work without rewriting legacy rows or reparsing exports.
			tx = integrationAccountTransaction(t, adminDB, accountID)
			defer tx.Rollback(ctx)
			var storedType, storedWorkout string
			if err := tx.QueryRow(ctx, `SELECT wt.provider_label,w.provider_label FROM app.workouts w
				JOIN app.workout_types wt ON wt.id=w.workout_type_id WHERE w.id=$1`, fixture.workouts[0]).Scan(&storedType, &storedWorkout); err != nil ||
				storedType != expectedRawAlias || storedWorkout != expectedRawAlias {
				t.Fatalf("read rewrote raw stored alias=%q/%q err=%v", storedType, storedWorkout, err)
			}
		})
	}
}
