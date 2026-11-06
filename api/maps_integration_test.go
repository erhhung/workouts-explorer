package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mapCoverageQueryHook func(context.Context, pgx.TraceQueryStartData)

func (hook mapCoverageQueryHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	hook(ctx, query)
	return ctx
}

func (mapCoverageQueryHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

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
		'[{"regionId":"geofabrik:test","generation":1}]',transaction_timestamp())`, accountID, fixture.workouts[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.Exec(ctx, `INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
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
	if selection.CoverageDisplayPolicy != "coverage-display-identity-v1" || selection.CoverageDisplayRevision < 1 {
		t.Fatalf("selection must return prepared display metadata: %+v", selection)
	}
	var boundPolicy string
	var boundRevision int64
	if err := adminDB.QueryRow(ctx, `SELECT context.policy_version,context.revision
		FROM app.map_selections selection JOIN app.coverage_display_contexts context
		ON context.account_id=selection.account_id AND context.id=selection.coverage_display_context_id
		WHERE selection.account_id=$1 AND selection.id=$2`, accountID, selection.Id).Scan(&boundPolicy, &boundRevision); err != nil ||
		boundPolicy != selection.CoverageDisplayPolicy || boundRevision != selection.CoverageDisplayRevision {
		t.Fatalf("response display metadata must match the bound context: policy=%s revision=%d err=%v", boundPolicy, boundRevision, err)
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
	for _, query := range []string{"&bulk=false&page=2&pageSize=100", "&bulk=true"} {
		response := routeMapRequest(handler, http.MethodGet, "/api/map-selections/"+selection.Id+"/coverage/paths?generation="+
			strconv.FormatInt(selection.DataGeneration, 10)+query, "", firstBearer)
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &paths) != nil ||
			len(paths.Items) != 0 || paths.Pagination.TotalItems != 0 || paths.Pagination.TotalPages != 0 {
			t.Fatalf("empty coverage %s: status=%d body=%s", query, response.Code, response.Body.String())
		}
		if query == "&bulk=true" && (paths.Pagination.Page != 1 || paths.Pagination.PageSize != maximumBulkCoverageEntities) {
			t.Fatalf("bulk pagination=%+v", paths.Pagination)
		}
		if query != "&bulk=true" && (paths.Pagination.Page != 2 || paths.Pagination.PageSize != 100) {
			t.Fatalf("nonbulk pagination changed: %+v", paths.Pagination)
		}
		validateRecordedResponse(t, http.MethodGet, "/api/map-selections/"+selection.Id+"/coverage/paths", response)
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

	t.Run("bulk generation snapshot", func(t *testing.T) {
		pathID, segmentID := uuid.New(), uuid.New()
		tx := integrationAccountTransaction(t, adminDB, accountID)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `INSERT INTO app.coverage_paths(account_id,logical_path_id,name,normalized_name,broad_class)
			VALUES($1,$2,'Snapshot Road','snapshot road','road')`, accountID, pathID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.path_segments(account_id,region_id,generation_id,physical_segment_id,
			logical_path_id,derivation_version,source_way_id,source_way_version,name,normalized_name,highway,broad_class,tags,segment_meters,geom)
			VALUES($1,'geofabrik:test',1,$2,$3,4,1,1,'Snapshot Road','snapshot road','residential','road','{}',30,
			ST_GeomFromText('LINESTRING(-105.1 39.9,-105 40)',4326))`, accountID, segmentID, pathID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.workout_segment_matches(account_id,workout_id,physical_segment_id,
			region_id,generation_id,logical_path_id,first_traversed_at,first_route_order,covered_meters,geom)
			SELECT account_id,$2,physical_segment_id,region_id,generation_id,logical_path_id,
			'2026-03-07T15:01:00Z',0,30,ST_Multi(geom) FROM app.path_segments
			WHERE account_id=$1 AND physical_segment_id=$3`, accountID, fixture.workouts[0], segmentID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		created := routeMapRequest(handler, http.MethodPost, "/api/map-selections", `{"startDate":"2026-03-07","endDate":"2026-03-09","workoutIds":["`+
			compactUUID(fixture.workouts[1])+`"],"focusedWorkoutId":"`+compactUUID(fixture.workouts[1])+`"}`, firstBearer)
		var snapshot generated.MapSelection
		if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &snapshot) != nil {
			t.Fatalf("snapshot selection status=%d body=%s", created.Code, created.Body.String())
		}
		if len(snapshot.Workouts) != 1 || snapshot.Workouts[0].Id != compactUUID(fixture.workouts[1]) {
			t.Fatalf("expected a subset excluding the matched workout: %+v", snapshot.Workouts)
		}
		path := "/api/map-selections/" + snapshot.Id + "/coverage/paths?generation=" + strconv.FormatInt(snapshot.DataGeneration, 10)
		baseline := routeMapRequest(handler, http.MethodGet, path+"&pageSize=1", "", firstBearer)
		var page generated.RoadCoverageList
		if baseline.Code != http.StatusOK || json.Unmarshal(baseline.Body.Bytes(), &page) != nil ||
			len(page.Items) != 1 || page.Items[0].EntityId == compactUUID(pathID) || page.Items[0].EntityKind != "path" || page.Pagination.PageSize != 1 || page.Pagination.TotalItems != 1 {
			t.Fatalf("nonbulk coverage status=%d body=%s", baseline.Code, baseline.Body.String())
		}
		canonicalID := page.Items[0].EntityId
		statsPath := "/api/map-selections/" + snapshot.Id + "/coverage/path/" + canonicalID + "/stats?generation=" + strconv.FormatInt(snapshot.DataGeneration, 10)
		t.Run("entity statistics", func(t *testing.T) {
			response := routeMapRequest(handler, http.MethodGet, statsPath, "", firstBearer)
			var stats generated.RoadCoverageEntity
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &stats) != nil ||
				!reflect.DeepEqual(stats, page.Items[0]) || stats.RangeFirstWorkoutId != compactUUID(fixture.workouts[0]) {
				t.Fatalf("stats must cover the entire date range, not the focused subset: status=%d body=%s", response.Code, response.Body.String())
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["geometry"]; present {
				t.Fatal("stats response includes geometry")
			}
			if _, present := fields["fitBounds"]; present {
				t.Fatal("stats response includes fit bounds")
			}
			validateRecordedResponse(t, http.MethodGet, statsPath, response)
			foreignPrincipal, _ := insertSourceTestUser(t, adminDB)
			foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
			for _, test := range []struct{ name, path, bearer string }{
				{"other session", statsPath, secondBearer},
				{"other account", statsPath, foreignBearer},
				{"missing selection", strings.Replace(statsPath, snapshot.Id, compactUUID(uuid.New()), 1), firstBearer},
				{"missing entity", strings.Replace(statsPath, canonicalID, compactUUID(uuid.New()), 1), firstBearer},
				{"wrong kind", strings.Replace(statsPath, "/coverage/path/", "/coverage/park/", 1), firstBearer},
				{"wrong generation", strings.Split(statsPath, "?")[0] + "?generation=" + strconv.FormatInt(snapshot.DataGeneration+1, 10), firstBearer},
			} {
				t.Run(test.name, func(t *testing.T) {
					response := routeMapRequest(handler, http.MethodGet, test.path, "", test.bearer)
					if response.Code != http.StatusNotFound {
						t.Fatalf("stats escaped its scope: status=%d body=%s", response.Code, response.Body.String())
					}
					validateRecordedResponse(t, http.MethodGet, test.path, response)
				})
			}
			detailPath := strings.Replace(statsPath, "/stats?", "?", 1)
			detail := routeMapRequest(handler, http.MethodGet, detailPath, "", firstBearer)
			if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &fields) != nil || fields["geometry"] == nil || fields["fitBounds"] == nil {
				t.Fatalf("click detail must retain geometry and fit bounds: status=%d body=%s", detail.Code, detail.Body.String())
			}
			validateRecordedResponse(t, http.MethodGet, detailPath, detail)
		})
		advanced := false
		config := apiDB.Config()
		config.ConnConfig.Tracer = mapCoverageQueryHook(func(ctx context.Context, query pgx.TraceQueryStartData) {
			if advanced || !strings.Contains(query.SQL, "FROM app.map_selection_coverage_entities(") {
				return
			}
			advanced = true
			// This runs just before the coverage query, after the handler's validation.
			if _, err := adminDB.Exec(ctx, `UPDATE app.account_data_generations SET generation=generation+1
				WHERE account_id=$1`, accountID); err != nil {
				t.Fatal(err)
			}
		})
		tracedDB, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer tracedDB.Close()
		tracedHandler, err := NewHandlerContext(routerContext, server.config, tracedDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		response := routeMapRequest(tracedHandler, http.MethodGet, path+"&bulk=true", "", firstBearer)
		var bulk generated.RoadCoverageList
		if !advanced || response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &bulk) != nil ||
			len(bulk.Items) != 1 || bulk.Items[0].EntityId != canonicalID || bulk.Items[0].RangeWorkoutCount != 1 ||
			bulk.Pagination != pagination(1, maximumBulkCoverageEntities, 1) {
			t.Fatalf("generation changed between validation and bulk read: advanced=%t status=%d body=%s", advanced, response.Code, response.Body.String())
		}
		t.Logf("bulk returned the complete nonempty snapshot after a concurrent generation commit in %s", time.Since(started))
		validateRecordedResponse(t, http.MethodGet, path, response)
		stale := routeMapRequest(tracedHandler, http.MethodGet, path+"&bulk=true", "", firstBearer)
		if stale.Code != http.StatusNotFound {
			t.Fatalf("next request accepted stale generation: status=%d body=%s", stale.Code, stale.Body.String())
		}
		staleStats := routeMapRequest(handler, http.MethodGet, statsPath, "", firstBearer)
		if staleStats.Code != http.StatusNotFound {
			t.Fatalf("stats accepted a now-stale generation: status=%d body=%s", staleStats.Code, staleStats.Body.String())
		}
	})

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

func TestMapSelectionFocusedMembershipIntegration(t *testing.T) {
	apiURL, migrationURL, workerURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("WORKER_DATABASE_URL")
	if apiURL == "" || migrationURL == "" || workerURL == "" {
		t.Skip("API_DATABASE_URL, MIGRATION_DATABASE_URL, and WORKER_DATABASE_URL are required")
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

	principalID, accountID := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, accountID)
	_, foreignAccountID := insertSourceTestUser(t, adminDB)
	foreignFixture := insertWorkoutReadFixtures(t, adminDB, workerDB, foreignAccountID)
	// The broad export-read fixture has >200m jumps and therefore no drawable
	// components. Membership tests require actual routed workouts, not GPS exports.
	for _, account := range []uuid.UUID{accountID, foreignAccountID} {
		tx, err := adminDB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err == nil {
			_, err = tx.Exec(ctx, `UPDATE app.workout_routes SET route=ST_Multi(ST_MakeLine(
				ST_SetSRID(ST_MakePoint(minimum_longitude,minimum_latitude),4326),
				ST_SetSRID(ST_MakePoint(minimum_longitude+0.0001,minimum_latitude+0.0001),4326)))
				WHERE account_id=$1`, account)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	missingID := uuid.New()
	cases := []struct {
		name       string
		focus      *uuid.UUID
		explicit   *uuid.UUID
		endDate    string
		markDelete bool
		status     int
	}{
		{name: "omitted focus", status: http.StatusCreated},
		{name: "valid complete-range focus", focus: &fixture.workouts[0], status: http.StatusCreated},
		{name: "valid explicit focus", focus: &fixture.workouts[0], explicit: &fixture.workouts[0], status: http.StatusCreated},
		{name: "missing focus", focus: &missingID, status: http.StatusNotFound},
		{name: "nonrouted focus", focus: &fixture.workouts[2], status: http.StatusNotFound},
		{name: "outside-range focus", focus: &fixture.workouts[1], endDate: "2026-03-07", status: http.StatusNotFound},
		{name: "deletion-marked focus", focus: &fixture.workouts[1], markDelete: true, status: http.StatusNotFound},
		{name: "foreign-account focus", focus: &foreignFixture.workouts[0], status: http.StatusNotFound},
		{name: "focus outside explicit subset", focus: &fixture.workouts[2], explicit: &fixture.workouts[0], status: http.StatusBadRequest},
		{name: "filtered explicit focus", focus: &fixture.workouts[2], explicit: &fixture.workouts[2], status: http.StatusNotFound},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.markDelete {
				deleted := routeMapRequest(handler, http.MethodDelete, "/api/workouts/"+compactUUID(*test.focus), "", bearer)
				if deleted.Code != http.StatusAccepted {
					t.Fatalf("mark deletion status=%d body=%s", deleted.Code, deleted.Body.String())
				}
				var marked bool
				if err := adminDB.QueryRow(ctx, `SELECT deletion_requested_at IS NOT NULL FROM app.workouts
					WHERE account_id=$1 AND id=$2`, accountID, *test.focus).Scan(&marked); err != nil || !marked {
					t.Fatalf("fixture deletion marker=%t err=%v", marked, err)
				}
			}
			var beforeSelections, beforeWorkouts int
			if err := adminDB.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM app.map_selections WHERE account_id IN ($1,$2)),
				(SELECT count(*) FROM app.map_selection_workouts WHERE account_id IN ($1,$2))`,
				accountID, foreignAccountID).Scan(&beforeSelections, &beforeWorkouts); err != nil {
				t.Fatal(err)
			}
			endDate := test.endDate
			if endDate == "" {
				endDate = "2026-03-09"
			}
			body := `{"startDate":"2026-03-07","endDate":"` + endDate + `"`
			if test.focus != nil {
				body += `,"focusedWorkoutId":"` + compactUUID(*test.focus) + `"`
			}
			if test.explicit != nil {
				body += `,"workoutIds":["` + compactUUID(*test.explicit) + `"]`
			}
			response := routeMapRequest(handler, http.MethodPost, "/api/map-selections", body+`}`, bearer)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			validateRecordedResponse(t, http.MethodPost, "/api/map-selections", response)
			if test.status != http.StatusCreated {
				var afterSelections, afterWorkouts int
				if err := adminDB.QueryRow(ctx, `SELECT
					(SELECT count(*) FROM app.map_selections WHERE account_id IN ($1,$2)),
					(SELECT count(*) FROM app.map_selection_workouts WHERE account_id IN ($1,$2))`,
					accountID, foreignAccountID).Scan(&afterSelections, &afterWorkouts); err != nil {
					t.Fatal(err)
				}
				if afterSelections != beforeSelections || afterWorkouts != beforeWorkouts {
					t.Fatalf("failed request leaked committed selection rows: selections %d -> %d, workouts %d -> %d",
						beforeSelections, afterSelections, beforeWorkouts, afterWorkouts)
				}
				if response.Header().Get("Location") != "" {
					t.Fatal("failed request returned a selection location")
				}
				if test.status == http.StatusNotFound {
					var problem struct{ Detail string }
					if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Detail != "one or more routed workouts are unavailable" {
						t.Fatalf("unexpected unavailable detail: %s", response.Body.String())
					}
					if strings.Contains(response.Body.String(), compactUUID(*test.focus)) || strings.Contains(response.Body.String(), test.focus.String()) {
						t.Fatal("unavailable response disclosed workout ID")
					}
				}
				t.Logf("HTTP %d; no committed selection or membership leak", response.Code)
				return
			}
			var selection generated.MapSelection
			if err := json.Unmarshal(response.Body.Bytes(), &selection); err != nil {
				t.Fatal(err)
			}
			wantWorkouts := fixture.workouts[:2]
			if test.explicit != nil {
				wantWorkouts = []uuid.UUID{*test.explicit}
			}
			if len(selection.Workouts) != len(wantWorkouts) {
				t.Fatalf("selected %d workouts, want %d", len(selection.Workouts), len(wantWorkouts))
			}
			for i, id := range wantWorkouts {
				if selection.Workouts[i].Id != compactUUID(id) {
					t.Fatal("selection changed requested scope")
				}
			}
			if test.focus == nil {
				if !selection.FocusedWorkoutId.IsNull() {
					t.Fatal("omitted focus must be returned as null")
				}
			} else if focus, err := selection.FocusedWorkoutId.Get(); err != nil || focus != compactUUID(*test.focus) {
				t.Fatal("selection dropped or changed requested focus")
			}
			var committed, memberships int
			if err := adminDB.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM app.map_selections WHERE account_id=$1 AND id=$2),
				(SELECT count(*) FROM app.map_selection_workouts WHERE account_id=$1 AND selection_id=$2)`,
				accountID, selection.Id).Scan(&committed, &memberships); err != nil || committed != 1 || memberships != len(wantWorkouts) {
				t.Fatalf("successful selection not committed: selections=%d workouts=%d err=%v", committed, memberships, err)
			}
			t.Logf("HTTP 201; %d workouts committed with requested focus and scope", memberships)
		})
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
