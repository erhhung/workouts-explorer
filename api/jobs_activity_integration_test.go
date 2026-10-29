package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run only with explicitly supplied synthetic scratch role URLs. This test does
// not provision databases, invoke providers, or read application configuration.
func TestIngestActivityPostgreSQL(t *testing.T) {
	if os.Getenv("INGEST_PHASE_PROGRESS_TEST_DISPOSABLE") != "1" {
		t.Skip("INGEST_PHASE_PROGRESS_TEST_DISPOSABLE=1 and disposable API_DATABASE_URL/MIGRATION_DATABASE_URL required")
	}
	var endpoint string
	for _, role := range [][2]string{{"API_DATABASE_URL", "workouts_api"}, {"MIGRATION_DATABASE_URL", "workouts_migration"}} {
		value := os.Getenv(role[0])
		cfg, err := pgx.ParseConfig(value)
		if value == "" || err != nil {
			t.Fatal(role[0] + " must explicitly name a disposable database")
		}
		if (cfg.Host != "127.0.0.1" && cfg.Host != "::1") || cfg.User != role[1] || cfg.RuntimeParams["role"] != "" ||
			!strings.HasPrefix(cfg.Database, "workouts_") || !strings.Contains(cfg.Database, "_test_") ||
			strings.Contains(cfg.Database, "clone") || strings.Contains(cfg.Database, "actual") || strings.Contains(cfg.Database, "live") {
			t.Fatal("activity fixtures require genuine role logins on a named synthetic loopback workouts_*_test_* database")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("activity fixtures prohibit alternate endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("both role URLs must target the same disposable endpoint")
		}
		endpoint = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	apiDB, err := pgxpool.New(ctx, os.Getenv("API_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	setup, err := pgxpool.New(ctx, os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	var current bool
	if err := setup.QueryRow(ctx, `SELECT (SELECT schema_version=30 AND minimum_runtime_version=30 AND migration_baseline=27
		FROM app.schema_metadata WHERE singleton)
		AND (SELECT count(*)=31 AND count(DISTINCT version_id)=31 AND min(version_id)=0 AND max(version_id)=30
		AND bool_and(is_applied) FROM public.goose_db_version)
		AND to_regprocedure('app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)') IS NOT NULL`).Scan(&current); err != nil || !current {
		t.Fatalf("activity fixtures require canonical schema 30 with ingest phase reporting: current=%t err=%v", current, err)
	}
	server := integrationServer(t, apiDB, &recordingSender{})
	handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	principal, account := insertSourceTestUser(t, setup)
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	foreignPrincipal, foreignAccount := insertSourceTestUser(t, setup)
	foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
	parent := uuid.New()
	children := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	lease := uuid.New()
	tx := integrationAccountTransaction(t, setup, account)
	defer tx.Rollback(ctx)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parent, account)
	exec(`INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$2)`, parent, account)
	for index, child := range children {
		source := uuid.New()
		name := fmt.Sprintf("Synthetic activity %d", index)
		exec(`INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
			VALUES($1,$2,$3,lower($3),'health-auto-export-local','connected',$4)`, source, account, name, []byte("synthetic-envelope"))
		exec(`INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,parameters)
			VALUES($1,$2,$3,'manual_ingest_source',80,jsonb_build_object('sourceId',$4::text,'generation',1,'mode','incremental'))`, child, account, parent, compactUUID(source))
		exec(`INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			VALUES($1,$2,$3,1,$4,'health-auto-export-local')`, child, account, source, name)
		exec(`INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
			VALUES($1,$2,$3,1,$4)`, child, account, source, []byte("synthetic-envelope"))
		exec(`INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$2)`, child, account)
		if index != 1 {
			var claimed bool
			if err := tx.QueryRow(ctx, `SELECT app.claim_job($1,'api-activity-test',$2,interval '5 minutes')`, child, lease).Scan(&claimed); err != nil || !claimed {
				t.Fatalf("claim=%t err=%v", claimed, err)
			}
		}
	}
	updated := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	event := func(job uuid.UUID, code, fields string, at time.Time) {
		t.Helper()
		exec(`INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message,fields,created_at)
			VALUES($1,$2,'info',$3,'Synthetic activity event.',$4::jsonb,$5)`, account, job, code, fields, at)
	}
	// Equal timestamps cannot identify the latest event. A later identity with
	// an earlier timestamp must still win, but only for this claim attempt.
	event(children[0], "ingest-phase-preparing", `{"attempt":1}`, updated.Add(time.Hour))
	event(children[0], "ingest-phase-importing", `{"attempt":1,"current":1,"total":3}`, updated.Add(time.Hour))
	event(children[0], "ingest-phase-analyzing-imported-routes", `{"attempt":1,"current":2,"total":3}`, updated)
	event(children[0], "ingest-phase-invalid-old", `{"attempt":0}`, updated.Add(2*time.Hour))
	event(children[0], "ingest-phase-invalid-future", `{"attempt":2}`, updated.Add(2*time.Hour))
	event(children[0], "ingest-phase-invalid-legacy", `{}`, updated.Add(2*time.Hour))
	event(children[0], "source-diagnostic", `{"attempt":1}`, updated.Add(2*time.Hour))
	event(children[2], "ingest-phase-importing", `{"attempt":0,"current":1}`, updated)
	event(parent, "ingest-phase-invalid-root", `{"attempt":0}`, updated)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	read := func(id uuid.UUID) generated.JobDetail {
		t.Helper()
		path := "/api/jobs/" + compactUUID(id)
		call := routeJobRequest(handler, http.MethodGet, path, "", bearer, "", false)
		if call.recorder.Code != http.StatusOK {
			t.Fatalf("detail status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, call.recorder)
		var detail generated.JobDetail
		if err := json.Unmarshal(call.recorder.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	detail := read(parent)
	if detail.Activity != nil || len(detail.Children) != 3 {
		t.Fatalf("root activity/children=%+v", detail)
	}
	assertActivity := func(child generated.JobDetail) {
		t.Helper()
		activity := child.Activity
		if child.Attempt != 1 || activity == nil || activity.Phase != generated.AnalyzingImportedRoutes ||
			activity.Current == nil || *activity.Current != 2 || activity.Total == nil || *activity.Total != 3 || !activity.UpdatedAt.Equal(updated) {
			t.Fatalf("latest current-attempt activity=%+v attempt=%d", activity, child.Attempt)
		}
	}
	for _, child := range detail.Children {
		if child.Id == compactUUID(children[0]) {
			assertActivity(child)
		} else if child.Activity != nil {
			t.Fatalf("no current-attempt event must mean absence: %+v", child)
		}
	}
	assertActivity(read(children[0]))
	for _, child := range children[1:] {
		if detail := read(child); detail.Activity != nil {
			t.Fatalf("legacy/no-event detail fabricated activity: %+v", detail)
		}
	}
	for _, id := range append([]uuid.UUID{parent}, children...) {
		path := "/api/jobs/" + compactUUID(id)
		call := routeJobRequest(handler, http.MethodGet, path, "", foreignBearer, "", false)
		if call.recorder.Code != http.StatusNotFound {
			t.Fatalf("foreign detail status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
	}
	foreign := integrationAccountTransaction(t, apiDB, foreignAccount)
	defer foreign.Rollback(ctx)
	for _, child := range children {
		foreignDetail := generated.JobDetail{Id: compactUUID(child), Source: &generated.JobSourceContext{}}
		if err := readIngestActivities(ctx, foreign, &foreignDetail); err != nil || foreignDetail.Activity != nil {
			t.Fatalf("foreign activity=%+v err=%v", foreignDetail.Activity, err)
		}
	}
	if _, err := readJobDetail(ctx, foreign, parent); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign root err=%v", err)
	}
	if err := foreign.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.accountTransactionWithOptions(ctx, account, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback(ctx)
	if _, err := readJobDetail(ctx, snapshot, children[0]); err != nil {
		t.Fatal(err)
	}
	// Keep the last recorded activity on terminal details without inventing a
	// completed phase or modifying the terminal execution status.
	tx = integrationAccountTransaction(t, setup, account)
	defer tx.Rollback(ctx)
	event(children[0], "ingest-phase-cleanup", `{"attempt":1}`, updated)
	var finished bool
	if err := tx.QueryRow(ctx, `SELECT app.finish_job($1,'api-activity-test',$2,'succeeded',NULL,NULL)`, children[0], lease).Scan(&finished); err != nil || !finished {
		t.Fatalf("finish=%t err=%v", finished, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	previous, err := readJobDetail(ctx, snapshot, children[0])
	if err != nil || previous.Status != generated.JobStatus("running") {
		t.Fatalf("detail snapshot changed across concurrent completion: status=%s err=%v", previous.Status, err)
	}
	assertActivity(previous)
	if err := snapshot.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := read(children[0])
	if terminal.Status != generated.JobStatus("succeeded") || terminal.Activity == nil || terminal.Activity.Phase != generated.Cleanup ||
		terminal.Activity.Current != nil || terminal.Activity.Total != nil {
		t.Fatalf("terminal historical activity=%+v", terminal)
	}
	// A corrupt current-attempt event is an unavailable read, not an arbitrary
	// public phase or a database error leaked through the response.
	tx = integrationAccountTransaction(t, setup, account)
	defer tx.Rollback(ctx)
	event(children[2], "ingest-phase-private-invalid", `{"attempt":1}`, updated)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{parent, children[2]} {
		path := "/api/jobs/" + compactUUID(id)
		call := routeJobRequest(handler, http.MethodGet, path, "", bearer, "", false)
		if call.recorder.Code != http.StatusServiceUnavailable || strings.Contains(call.recorder.Body.String(), "private-invalid") ||
			!strings.Contains(call.recorder.Body.String(), "job service is temporarily unavailable") {
			t.Fatalf("invalid stored phase status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, call.recorder)
	}
}
