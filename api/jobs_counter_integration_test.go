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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestJobRouteCountersPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_INGEST_ROUTE_COUNTS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_INGEST_ROUTE_COUNTS_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable route-counter database is required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_ingest_route_counts_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("route counters require a named loopback scratch database")
	}
	ctx := context.Background()
	setup, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	apiConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	apiConfig.ConnConfig.RuntimeParams["role"] = "workouts_api"
	apiDB, err := pgxpool.NewWithConfig(ctx, apiConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	server := integrationServer(t, apiDB, &recordingSender{})
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"manual_ingest", "scheduled_ingest"} {
		for _, known := range []bool{true, false} {
			t.Run(kind+"/known="+map[bool]string{true: "true", false: "false"}[known], func(t *testing.T) {
				principal, account := insertSourceTestUser(t, setup)
				bearer := insertTestSession(t, apiDB, principal, "bearer", "")
				parent, child, source := uuid.New(), uuid.New(), uuid.New()
				tx := integrationAccountTransaction(t, setup, account)
				defer tx.Rollback(ctx)
				if _, err := tx.Exec(ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
					VALUES($1,$2,'Synthetic counters','synthetic counters','health-auto-export-local','connected',$3)`, source, account, []byte("synthetic-envelope")); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,$3,80)`, parent, account, kind); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,parameters)
					VALUES($1,$2,$3,$4,80,jsonb_build_object('sourceId',$5::text,'generation',1,'mode','incremental'))`, child, account, parent, kind+"_source", compactUUID(source)); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
					VALUES($1,$2,$3,1,'Synthetic counters','health-auto-export-local')`, child, account, source); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
					VALUES($1,$2,$3,1,$4)`, child, account, source, []byte("synthetic-envelope")); err != nil {
					t.Fatal(err)
				}
				var createdRoutes, updatedRoutes *int64
				if known {
					created, updated := int64(491), int64(0)
					createdRoutes, updatedRoutes = &created, &updated
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.job_progress(job_id,account_id,workouts_created,workouts_updated,workouts_created_with_route,workouts_updated_with_route) SELECT id,$2,516,33,$3,$4 FROM unnest($1::uuid[]) id`, []uuid.UUID{parent, child}, account, createdRoutes, updatedRoutes); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				assertProgress := func(progress generated.JobProgress) {
					t.Helper()
					if progress.WorkoutsCreated != 516 || progress.WorkoutsUpdated != 33 {
						t.Fatalf("totals=%+v", progress)
					}
					if known {
						if progress.WorkoutsCreatedWithRoute == nil || *progress.WorkoutsCreatedWithRoute != 491 || progress.WorkoutsUpdatedWithRoute == nil || *progress.WorkoutsUpdatedWithRoute != 0 {
							t.Fatalf("known routes=%+v", progress)
						}
					} else if progress.WorkoutsCreatedWithRoute != nil || progress.WorkoutsUpdatedWithRoute != nil {
						t.Fatalf("unknown became zero: %+v", progress)
					}
				}
				for _, id := range []uuid.UUID{parent, child} {
					response := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(id), "", bearer, "", false)
					if response.recorder.Code != http.StatusOK {
						t.Fatalf("detail: %d %s", response.recorder.Code, response.recorder.Body.String())
					}
					validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+compactUUID(id), response.recorder)
					var detail generated.JobDetail
					if err := json.Unmarshal(response.recorder.Body.Bytes(), &detail); err != nil {
						t.Fatal(err)
					}
					assertProgress(detail.Progress)
					if detail.Results == nil || (detail.Results.WorkoutsCreatedWithRoute != nil) != known || (detail.Results.WorkoutsUpdatedWithRoute != nil) != known {
						t.Fatalf("results lost nullable counters: %+v", detail.Results)
					}
					if id == parent {
						if len(detail.Children) != 1 {
							t.Fatal("missing child")
						}
						assertProgress(detail.Children[0].Progress)
					}
				}
				response := routeJobRequest(handler, http.MethodGet, "/api/jobs", "", bearer, "", false)
				if response.recorder.Code != http.StatusOK {
					t.Fatalf("list: %d %s", response.recorder.Code, response.recorder.Body.String())
				}
				var list generated.JobList
				if err := json.Unmarshal(response.recorder.Body.Bytes(), &list); err != nil {
					t.Fatal(err)
				}
				if len(list.Items) != 1 {
					t.Fatalf("list=%+v", list)
				}
				assertProgress(list.Items[0].Progress)
				response = routeJobRequest(handler, http.MethodGet, "/api/data-sync", "", bearer, "", false)
				if response.recorder.Code != http.StatusOK {
					t.Fatalf("snapshot: %d %s", response.recorder.Code, response.recorder.Body.String())
				}
				var snapshot generated.DataSync
				if err := json.Unmarshal(response.recorder.Body.Bytes(), &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.ActiveJob == nil {
					t.Fatal("missing active job")
				}
				assertProgress(snapshot.ActiveJob.Progress)
			})
		}
	}
}
