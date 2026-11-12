package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAllTimePostgreSQL(t *testing.T) {
	if os.Getenv("ALL_TIME_TEST_DISPOSABLE") != "1" {
		t.Skip("ALL_TIME_TEST_DISPOSABLE=1 and isolated loopback role URLs are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	var pools []*pgxpool.Pool
	var endpoint string
	for i, env := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL", "TILE_DATABASE_URL"} {
		config, err := pgxpool.ParseConfig(os.Getenv(env))
		if err != nil || config == nil || config.ConnConfig.Host != "127.0.0.1" || !strings.HasPrefix(config.ConnConfig.Database, "workouts_stats_test_") {
			t.Fatal("All time fixtures require disposable named loopback databases")
		}
		key := fmt.Sprintf("%s:%d/%s", config.ConnConfig.Host, config.ConnConfig.Port, config.ConnConfig.Database)
		if endpoint != "" && key != endpoint {
			t.Fatal("all roles must use the same fixture database")
		}
		endpoint = key
		for _, fallback := range config.ConnConfig.Fallbacks {
			if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
				t.Fatal("alternate endpoints are prohibited")
			}
		}
		config.MaxConns = 2
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		var role string
		var privileged bool
		if err := pool.QueryRow(ctx, `SELECT current_user,rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&role, &privileged); err != nil || privileged || role != []string{"workouts_api", "workouts_migration", "workouts_worker", "workouts_tiles"}[i] {
			t.Fatal("fixture roles must be restricted")
		}
		pools = append(pools, pool)
	}
	apiDB, adminDB, workerDB, tileDB := pools[0], pools[1], pools[2], pools[3]
	principal, account := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, account)
	writeTransaction := func() pgx.Tx {
		tx := integrationAccountTransaction(t, adminDB, account)
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2,$3)`, fixture.jobID, fixture.childLease, database.SupportedSchemaVersion).Scan(&source); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	_, foreignAccount := insertSourceTestUser(t, adminDB)
	insertWorkoutReadFixtures(t, adminDB, workerDB, foreignAccount)
	tx := writeTransaction()
	if _, err := tx.Exec(ctx, `UPDATE app.workouts SET local_start_date=CASE id WHEN $1 THEN DATE '1900-01-01' WHEN $2 THEN NULL ELSE DATE '2099-12-31' END WHERE account_id=$3`, fixture.workouts[0], fixture.workouts[1], account); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app.workout_route_points SET longitude=-105+sequence*0.0001,latitude=40+sequence*0.0001,recorded_at='2026-03-07T15:00:00Z'::timestamptz+sequence*interval '5 seconds' WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	for _, workout := range fixture.workouts[:2] {
		if _, err := tx.Exec(ctx, `SELECT app.replace_workout_route_summary($1,2,-105,40,-104.9999,40.0001,NULL,NULL,NULL,false)`, workout); err != nil {
			t.Fatal(err)
		}
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
	for _, path := range []string{"/api/summary?dateRangeEnum=allTime&tz=UTC", "/api/workouts?dateRangeEnum=allTime&tz=UTC&pageSize=100"} {
		response := routeOwnerRead(handler, path, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, response)
		if strings.HasPrefix(path, "/api/summary") {
			var summary generated.WorkoutSummary
			if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil || summary.Totals.Count != 3 || summary.Totals.RouteCount != 2 || summary.Range.StartDate.Time.Format(time.DateOnly) != "1900-01-01" || summary.Range.EndDate.Time.Format(time.DateOnly) != "2099-12-31" {
				t.Fatalf("incorrect all-account summary: %+v error=%v", summary, err)
			}
		} else {
			var list generated.WorkoutList
			if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) != 3 || list.Pagination.TotalItems != 3 {
				t.Fatalf("old/future/undated workouts not all included: %+v error=%v", list, err)
			}
		}
	}
	selection := routeMapRequest(handler, http.MethodPost, "/api/map-selections", fmt.Sprintf(`{"dateRangeEnum":"allTime","focusedWorkoutId":%q}`, compactUUID(fixture.workouts[1])), bearer)
	if selection.Code != http.StatusCreated {
		t.Fatalf("undated route cannot be focused: %d %s", selection.Code, selection.Body.String())
	}
	var selected generated.MapSelection
	if err := json.Unmarshal(selection.Body.Bytes(), &selected); err != nil || len(selected.Workouts) != 2 {
		t.Fatalf("missing old or undated route: %+v error=%v", selected, err)
	}
	validateRecordedResponse(t, http.MethodPost, "/api/map-selections", selection)
	var selectedSession string
	if err := adminDB.QueryRow(ctx, `SELECT session_id::text FROM app.map_selections WHERE account_id=$1 AND id=$2`, account, selected.Id).Scan(&selectedSession); err != nil {
		t.Fatal(err)
	}
	longitude := -105.0
	tileX := int((longitude + 180) / 360 * (1 << 14))
	tileY := int((1 - math.Asinh(math.Tan(40*math.Pi/180))/math.Pi) / 2 * (1 << 14))
	var tile []byte
	if err := tileDB.QueryRow(ctx, `SELECT app.raw_route_mvt(14,$5,$6,json_build_object('target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint))`, account, selectedSession, selected.Id, selected.DataGeneration, tileX, tileY).Scan(&tile); err != nil || !bytes.Contains(tile, []byte(compactUUID(fixture.workouts[0]))) || !bytes.Contains(tile, []byte(compactUUID(fixture.workouts[1]))) {
		t.Fatalf("old and undated raw routes must both reach the tile: bytes=%d error=%v", len(tile), err)
	}
	for _, all := range []bool{false, true} {
		body := `{"startDate":"1900-01-01","endDate":"2099-12-31"}`
		if all {
			body = `{"dateRangeEnum":"allTime"}`
		}
		report := routeMapRequest(handler, http.MethodPost, "/api/stats/report-scopes", body, bearer)
		if report.Code != http.StatusCreated {
			t.Fatalf("report all=%t: %d %s", all, report.Code, report.Body.String())
		}
	}
	tx = integrationAccountTransaction(t, adminDB, account)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_security_owner`); err != nil {
		t.Fatal(err)
	}
	var modes int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT all_time) FROM app.coverage_display_contexts WHERE account_id=$1 AND start_date='1900-01-01' AND end_date='2099-12-31'`, account).Scan(&modes); err != nil || modes != 2 {
		t.Fatalf("bounded and unfiltered contexts reused the same scope: modes=%d error=%v", modes, err)
	}
	_ = tx.Rollback(ctx)

	// Metadata ends at today in the requested zone even with no recent imports.
	tx = writeTransaction()
	if _, err := tx.Exec(ctx, `UPDATE app.workouts SET local_start_date='2010-01-01' WHERE id=$1`, fixture.workouts[2]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx = integrationAccountTransaction(t, apiDB, account)
	shortcut, zone := generated.AllTimeRange, "Pacific/Kiritimati"
	resolved, _, err := resolveAccountRange(ctx, tx, rangeInput{shortcut: &shortcut, timezone: &zone}, readPreferences{timezone: "UTC"}, time.Date(2026, 11, 9, 12, 30, 0, 0, time.UTC))
	if err != nil || resolved.end.Format(time.DateOnly) != "2026-11-10" || resolved.start.Format(time.DateOnly) != "1900-01-01" {
		t.Fatalf("metadata does not respect zone/today: %+v %v", resolved, err)
	}
	_ = tx.Rollback(ctx)
	_, emptyAccount := insertSourceTestUser(t, adminDB)
	tx = integrationAccountTransaction(t, apiDB, emptyAccount)
	emptyRange, _, err := resolveAccountRange(ctx, tx, rangeInput{shortcut: &shortcut, timezone: &zone}, readPreferences{timezone: "UTC"}, time.Date(2026, 11, 9, 12, 30, 0, 0, time.UTC))
	if err != nil || emptyRange.start.Format(time.DateOnly) != "2026-11-10" || !emptyRange.start.Equal(emptyRange.end) {
		t.Fatalf("empty-account metadata leaked another account or invented a start: %+v %v", emptyRange, err)
	}
	_ = tx.Rollback(ctx)

	// Exceed the former complete-selection limit using actual routed rows. The
	// route geometries are synthetic copies; no external requests or matching run.
	tx = writeTransaction()
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE all_time_workout_ids AS SELECT gen_random_uuid() id,n FROM generate_series(1,5001)n;`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
		SELECT ids.id,template.account_id,template.source_id,template.source_file_id,template.workout_type_id,'all-time-'||ids.n,template.content_sha256,template.provider_label,template.started_at,template.ended_at,NULL,template.provider_duration
		FROM all_time_workout_ids ids CROSS JOIN app.workouts template WHERE template.id=$1`, fixture.workouts[0]); err != nil {
		t.Fatal(err)
	}
	var columns, projection string
	if err := tx.QueryRow(ctx, `SELECT string_agg(quote_ident(attname),',' ORDER BY attnum),string_agg(CASE WHEN attname='workout_id' THEN 'ids.id' ELSE 'template.'||quote_ident(attname) END,',' ORDER BY attnum) FROM pg_attribute WHERE attrelid='app.workout_routes'::regclass AND attnum>0 AND NOT attisdropped AND attgenerated=''`).Scan(&columns, &projection); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_routes(`+columns+`) SELECT `+projection+` FROM all_time_workout_ids ids CROSS JOIN app.workout_routes template WHERE template.workout_id=$1`, fixture.workouts[0]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	large := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"dateRangeEnum":"allTime"}`, bearer)
	if large.Code != http.StatusCreated {
		t.Fatalf("large All time selection rejected: %d %s", large.Code, large.Body.String())
	}
	if err := json.Unmarshal(large.Body.Bytes(), &selected); err != nil || len(selected.Workouts) != 5003 {
		t.Fatalf("large All time selection truncated: routes=%d error=%v", len(selected.Workouts), err)
	}
}
