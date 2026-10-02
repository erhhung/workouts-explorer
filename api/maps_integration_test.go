package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
	if _, err := adminDB.Exec(ctx, `INSERT INTO app.workout_coverage_states(
		account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,
		processing_state,applied_route_input_revision,applied_route_input_sha256,applied_rules_version,
		applied_sampling_version,applied_path_policy_version,applied_generations,processing_finished_at)
		VALUES($1,$2,1,decode(repeat('11',32),'hex'),'map_data_ready',transaction_timestamp(),
		'current',1,decode(repeat('11',32),'hex'),'coverage-experimental-v1',
		'coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',
		'[{"regionId":"geofabrik:test","generation":1}]',transaction_timestamp());
		INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
		VALUES($1,$2,'geofabrik:test',1)`, accountID, fixture.workouts[0]); err != nil {
		t.Fatal(err)
	}
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
	if !selection.FocusedWorkoutId.IsNull() {
		t.Fatalf("omitted focus was not returned as null: %#v", selection.FocusedWorkoutId)
	}
	firstCalories, firstCaloriesErr := selection.Workouts[0].Calories.Get()
	secondCalories, secondCaloriesErr := selection.Workouts[1].Calories.Get()
	if firstCaloriesErr != nil || firstCalories.Value != "300.25" || secondCaloriesErr != nil || secondCalories.Value != "150.25" {
		t.Fatalf("map calories must match Summary total calories: first=%+v/%v second=%+v/%v", firstCalories, firstCaloriesErr, secondCalories, secondCaloriesErr)
	}
	if readiness := selection.Workouts[0].CoverageReadiness; readiness.MapDataStatus != generated.CoverageReadinessMapDataStatusReady ||
		readiness.ProcessingStatus != generated.CoverageReadinessProcessingStatusCurrent ||
		readiness.ResultStatus != generated.CoverageReadinessResultStatusCurrent {
		t.Fatalf("current workout readiness=%+v", readiness)
	}
	if readiness := selection.Workouts[1].CoverageReadiness; readiness.MapDataStatus != generated.CoverageReadinessMapDataStatusPending ||
		readiness.ProcessingStatus != generated.CoverageReadinessProcessingStatusUnprocessed ||
		readiness.ResultStatus != generated.CoverageReadinessResultStatusNone {
		t.Fatalf("legacy workout readiness=%+v", readiness)
	}
	if !strings.Contains(selection.RouteTileUrl, "/route-tiles/") || !strings.HasSuffix(selection.RouteTileUrl, "/{z}/{x}/{y}.pbf") {
		t.Fatalf("unexpected tile URL %q", selection.RouteTileUrl)
	}
	if !strings.Contains(selection.CoverageTileUrl, "/coverage-tiles/") || !strings.HasSuffix(selection.CoverageTileUrl, "/{z}/{x}/{y}.pbf") {
		t.Fatalf("unexpected coverage tile URL %q", selection.CoverageTileUrl)
	}
	pathsResponse := routeMapRequest(handler, http.MethodGet, "/api/map-selections/"+selection.Id+"/coverage/paths?generation="+
		strconv.FormatInt(selection.DataGeneration, 10), "", firstBearer)
	var paths generated.RoadCoverageList
	if pathsResponse.Code != http.StatusOK || json.Unmarshal(pathsResponse.Body.Bytes(), &paths) != nil ||
		len(paths.Items) != 0 || paths.Pagination.TotalItems != 0 {
		t.Fatalf("empty coverage paths status=%d body=%s", pathsResponse.Code, pathsResponse.Body.String())
	}
	var sessionID string
	if err := adminDB.QueryRow(ctx, `SELECT session_id::text FROM app.map_selections WHERE account_id=$1 AND id=$2`, accountID, selection.Id).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	var tile []byte
	if err := tileDB.QueryRow(ctx, `SELECT app.raw_route_mvt(0,0,0,json_build_object(
		'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint))`,
		accountID, sessionID, selection.Id, selection.DataGeneration).Scan(&tile); err != nil {
		t.Fatalf("tile role could not execute the approved MVT function: %v", err)
	}
	if len(tile) == 0 {
		t.Fatal("approved world tile did not contain selected route features")
	}
	if _, err := tileDB.Exec(ctx, `SELECT app.raw_route_mvt(0,0,0,json_build_object(
		'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,
		'target_generation',$4::bigint,'unexpected','value'))`, accountID, sessionID, selection.Id, selection.DataGeneration); err == nil {
		t.Fatal("tile function accepted an additional Martin query parameter")
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
	focusOutsideSubset := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"startDate":"2026-03-07","endDate":"2026-03-09","workoutIds":["`+
		compactUUID(fixture.workouts[0])+`"],"focusedWorkoutId":"`+compactUUID(fixture.workouts[1])+`"}`, firstBearer)
	if focusOutsideSubset.Code != http.StatusBadRequest {
		t.Fatalf("focus outside subset status=%d body=%s", focusOutsideSubset.Code, focusOutsideSubset.Body.String())
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
