package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type bulkCoverageTx struct {
	pgx.Tx
	rows      pgx.Rows
	queries   []string
	arguments []any
}

func (tx *bulkCoverageTx) Query(_ context.Context, query string, arguments ...any) (pgx.Rows, error) {
	tx.queries = append(tx.queries, query)
	tx.arguments = arguments
	return tx.rows, nil
}

func TestBulkRoadCoverageSingleEvaluationAndBounds(t *testing.T) {
	date := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	id := uuid.New()
	name := "Example Road"
	row := activityDisplayRow{id, "path", "road", &name, nil, "test-region", nil,
		int64(3), date, id, date, id, int64(7), date, id, date, id, -105.3, 39.8, -105.1, 40.1}
	for _, count := range []int{0, 1, 101, maximumBulkCoverageEntities, maximumBulkCoverageEntities + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			values := make([]activityDisplayRow, count)
			for index := range values {
				values[index] = row
			}
			tx := &bulkCoverageTx{rows: &activityDisplayRows{rows: values}}
			arguments := []any{uuid.New(), uuid.New(), uuid.New(), int64(7), nil}
			body, err := queryBulkRoadCoveragePaths(context.Background(), tx, arguments, "entity_kind,entity_id")
			if len(tx.queries) != 1 || strings.Count(tx.queries[0], "app.map_selection_coverage_entities(") != 1 ||
				strings.Contains(tx.queries[0], "count(") || strings.Contains(tx.queries[0], "OFFSET") ||
				!strings.Contains(tx.queries[0], "LIMIT $6") || len(tx.arguments) != 6 || tx.arguments[5] != maximumBulkCoverageEntities+1 {
				t.Fatalf("bulk must evaluate coverage once with a bounded overflow sentinel: queries=%v arguments=%v", tx.queries, tx.arguments)
			}
			if count > maximumBulkCoverageEntities {
				if !errors.Is(err, errBulkCoverageLimit) || body != nil {
					t.Fatalf("overflow must fail without partial results: bytes=%d err=%v", len(body), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result generated.RoadCoverageList
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != count || result.Pagination != pagination(1, maximumBulkCoverageEntities, int64(count)) {
				t.Fatalf("incomplete result: items=%d pagination=%+v", len(result.Items), result.Pagination)
			}
		})
	}
	// Large text fields can hit the byte bound well before the entity bound.
	name = strings.Repeat("x", 1<<20)
	values := make([]activityDisplayRow, 64)
	for index := range values {
		values[index] = row
	}
	tx := &bulkCoverageTx{rows: &activityDisplayRows{rows: values}}
	body, err := queryBulkRoadCoveragePaths(context.Background(), tx, make([]any, 5), "entity_kind,entity_id")
	if !errors.Is(err, errBulkCoverageLimit) || body != nil {
		t.Fatalf("byte overflow must fail without partial results: bytes=%d err=%v", len(body), err)
	}
}

func TestRoadCoverageBulkAndPaginatedRequestContracts(t *testing.T) {
	handler := testHandler(t)
	path := "/api/map-selections/018F8E7D7A4C7C03A1C23D4E5F607182/coverage/paths"
	for _, test := range []struct {
		query  string
		status int
	}{
		{"?generation=7", http.StatusUnauthorized},
		{"?generation=7&bulk=false&page=2&pageSize=100&search=road&sort=name:asc", http.StatusUnauthorized},
		{"?generation=7&pageSize=101", http.StatusBadRequest},
		{"?generation=7&page=0", http.StatusBadRequest},
		{"?generation=7&bulk=true", http.StatusUnauthorized},
		{"?generation=7&bulk=true&pageSize=101", http.StatusBadRequest},
		{"?generation=7&bulk=invalid", http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+test.query, nil))
		if response.Code != test.status {
			t.Fatalf("%s: status=%d want=%d body=%s", test.query, response.Code, test.status, response.Body.String())
		}
	}
}

type coverageStatsTx struct {
	pgx.Tx
	row       pgx.Row
	reads     int
	query     string
	arguments []any
}

func (tx *coverageStatsTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	tx.reads++
	tx.query, tx.arguments = query, arguments
	return tx.row
}

type coverageStatsScanError struct{ err error }

func (row coverageStatsScanError) Scan(...any) error { return row.err }

type coverageDetailTx struct {
	coverageStatsTx
	settings []string
	setError error
}

func (tx *coverageDetailTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	tx.settings = append(tx.settings, query)
	return pgconn.CommandTag{}, tx.setError
}

func TestRoadCoverageEntityDetailSingleTargetedQueryAndMapping(t *testing.T) {
	account, session, selection, entity := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	first, latest, allFirst, allLatest := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	date := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	geometry := []byte(`{"type":"MultiLineString","coordinates":[[[-105.3,39.8],[-105.1,40.1]]]}`)
	for _, kind := range []string{"path", "park"} {
		t.Run(kind, func(t *testing.T) {
			name, locality, region := "Example", "Example Town", "Example Region"
			row := activityDisplayRow{entity, kind, "other", &name, &locality, "geofabrik:test", &region,
				int64(3), date, first, date.AddDate(0, 0, 1), latest,
				int64(7), date.AddDate(0, -1, 0), allFirst, date.AddDate(0, 1, 0), allLatest,
				-105.3, 39.8, -105.1, 40.1, geometry, -105.4, 39.7, -105.0, 40.2}
			if kind == "park" {
				row[2], row[3], row[4], row[6] = "park", nil, nil, nil
			}
			tx := &coverageDetailTx{coverageStatsTx: coverageStatsTx{row: row}}
			result, err := queryRoadCoverageEntityDetail(context.Background(), tx, account, session, selection, 7, kind, entity)
			if err != nil {
				t.Fatal(err)
			}
			if tx.reads != 1 || tx.query != coverageEntityDetailSQL ||
				!reflect.DeepEqual(tx.arguments, []any{account, session, selection, int64(7), kind, entity}) ||
				!reflect.DeepEqual(tx.settings, []string{"SET LOCAL jit=off"}) {
				t.Fatalf("detail requires one bound statement with transaction-local JIT disabled: %+v", tx)
			}
			var metadata roadCoverageRow
			if err := activityDisplayRow(row[:21]).Scan(metadata.scanDestinations()...); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var gotMetadata generated.RoadCoverageEntity
			if err := json.Unmarshal(body, &gotMetadata); err != nil || !reflect.DeepEqual(gotMetadata, metadata.entity()) {
				t.Fatalf("detail changed metadata: %s err=%v", body, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["geometry"]) != string(geometry) || result.FitBounds != (generated.RouteBounds{
				MinimumLongitude: -105.4, MinimumLatitude: 39.7, MaximumLongitude: -105, MaximumLatitude: 40.2}) {
				t.Fatalf("detail geometry or fit bounds changed: %s", body)
			}
			response := httptest.NewRecorder()
			writeJSON(response, http.StatusOK, result)
			validateRecordedResponse(t, http.MethodGet, "/api/map-selections/"+compactUUID(selection)+"/coverage/"+kind+"/"+compactUUID(entity)+"?generation=7", response)
		})
	}
	for _, want := range []error{pgx.ErrNoRows, errors.New("detail scan failed")} {
		tx := &coverageDetailTx{coverageStatsTx: coverageStatsTx{row: coverageStatsScanError{err: want}}}
		if _, err := queryRoadCoverageEntityDetail(context.Background(), tx, account, session, selection, 7, "path", entity); !errors.Is(err, want) || tx.reads != 1 {
			t.Fatalf("detail must preserve its single-query failure: reads=%d err=%v", tx.reads, err)
		}
	}
	want := errors.New("JIT setting failed")
	tx := &coverageDetailTx{setError: want}
	if _, err := queryRoadCoverageEntityDetail(context.Background(), tx, account, session, selection, 7, "path", entity); !errors.Is(err, want) || tx.reads != 0 {
		t.Fatalf("failed local setting must prevent detail execution: reads=%d err=%v", tx.reads, err)
	}
}

func TestRoadCoverageEntityDetailSQLExecutionGateAndGeometryContract(t *testing.T) {
	if coverageEntityDetailSQL != `SELECT * FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,$5,$6)` {
		t.Fatal("detail must use the canonical stored reader, not a source-only API geometry copy")
	}
	// The server owns transaction lifetime; the helper must use that same scoped client.
	source, err := os.ReadFile("maps.go")
	if err != nil {
		t.Fatal(err)
	}
	_, handler, _ := strings.Cut(string(source), "func (s *Server) GetMapSelectionCoverageEntity(")
	handler, _, _ = strings.Cut(handler, "const coverageEntityDetailSQL")
	for _, required := range []string{
		"sessionAccountTransactionWithOptions(r.Context(), session, pgx.TxOptions{AccessMode: pgx.ReadOnly})",
		"defer tx.Rollback(r.Context())", "queryRoadCoverageEntityDetail(r.Context(), tx, *session.accountID, session.sessionID, selectionID, params.Generation, entityKind, entityID)",
		"if err != nil || tx.Commit(r.Context()) != nil", "writeMapUnavailable(w, r)",
	} {
		if !strings.Contains(handler, required) {
			t.Errorf("detail handler lost transaction/error contract: %q", required)
		}
	}
	if strings.Contains(handler, "QueryRow(") || strings.Contains(handler, "queryRoadCoverageEntityStats(") {
		t.Fatal("handler must not make a second metadata or legacy detail read")
	}
}

func TestRoadCoverageEntityDetailCanonicalStoredGeometryContract(t *testing.T) {
	source, err := migrations.Files.ReadFile("030_coverage_display.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, body, found := strings.Cut(strings.ToLower(string(source)), "function app.map_selection_coverage_entity_detail(")
	if !found {
		t.Fatal("030 canonical detail function missing")
	}
	_, body, found = strings.Cut(body, "as $function$")
	if !found {
		t.Fatal("030 canonical detail body missing")
	}
	body, _, _ = strings.Cut(body, "$function$;")
	for _, required := range []string{"map_selection_coverage_entity_stats(", "lateral", "offset 0", "source_kind", "source_id",
		"st_unaryunion", "st_collect", "st_transform", "3857", "500", "st_asgeojson"} {
		if !strings.Contains(body, required) {
			t.Errorf("canonical stored detail lost geometry/authorization contract: %q", required)
		}
	}
	for _, forbidden := range []string{"map_selection_coverage_entities(", "st_simplify", "st_intersection", "map_selection_workouts"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("canonical detail includes full-list, clipped, or checked-subset work: %q", forbidden)
		}
	}
}

func TestCreateMapSelectionPreparesDisplayContextWithinTransaction(t *testing.T) {
	source, err := os.ReadFile("maps.go")
	if err != nil {
		t.Fatal(err)
	}
	_, handler, _ := strings.Cut(string(source), "func (s *Server) CreateMapSelection(")
	handler, _, _ = strings.Cut(handler, "func containsUUID(")
	previous := -1
	for _, required := range []string{
		"IsoLevel: pgx.RepeatableRead", "insertMapSelectionWorkouts(", "if !selected {",
		"FROM app.prepare_coverage_display_context($1,$2,$3,$4)",
		"accountID, session.sessionID, selectionID, generation", "Scan(&contextID, &displayRevision, &policyVersion)",
		"response, err := readMapSelection(", "response.CoverageDisplayPolicy = policyVersion",
		"response.CoverageDisplayRevision = displayRevision", "tx.Commit(r.Context())", "writeJSON(w, http.StatusCreated, response)",
	} {
		index := strings.Index(handler, required)
		if index <= previous {
			t.Fatalf("selection preparation/response transaction order missing %q", required)
		}
		previous = index
	}
	document, err := generated.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["MapSelection"].Value
	for _, field := range []string{"coverageDisplayPolicy", "coverageDisplayRevision"} {
		if !strings.Contains(","+strings.Join(schema.Required, ",")+",", ","+field+",") {
			t.Errorf("selection metadata must be required: %s", field)
		}
	}
	if !schema.Properties["coverageDisplayPolicy"].Value.Type.Is("string") || schema.Properties["coverageDisplayRevision"].Value.Format != "int64" {
		t.Fatal("selection display metadata must retain string policy and int64 revision")
	}
}

func TestRoadCoverageEntityStatsSingleTargetedQueryWithoutGeometry(t *testing.T) {
	account, session, selection, entity := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	first, latest, allFirst, allLatest := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	date := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	for _, kind := range []string{"path", "park"} {
		t.Run(kind, func(t *testing.T) {
			broadClass := "road"
			if kind == "park" {
				broadClass = "park"
			}
			tx := &coverageStatsTx{row: activityDisplayRow{
				entity, kind, broadClass, nil, nil, "geofabrik:test", nil,
				int64(3), date, first, date.AddDate(0, 0, 1), latest,
				int64(7), date.AddDate(0, -1, 0), allFirst, date.AddDate(0, 1, 0), allLatest,
				-105.3, 39.8, -105.1, 40.1,
			}}
			result, err := queryRoadCoverageEntityStats(context.Background(), tx, account, session, selection, 7, kind, entity)
			if err != nil {
				t.Fatal(err)
			}
			if tx.reads != 1 || !strings.Contains(tx.query, "FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,$5,$6)") ||
				!reflect.DeepEqual(tx.arguments, []any{account, session, selection, int64(7), kind, entity}) {
				t.Fatalf("expected one correctly scoped targeted query: reads=%d sql=%s arguments=%v", tx.reads, tx.query, tx.arguments)
			}
			for _, forbidden := range []string{"map_selection_coverage_entities(", "detail(", "geometry", "fit_", "ST_", "EXISTS", "count("} {
				if strings.Contains(tx.query, forbidden) {
					t.Fatalf("statistics query includes unwanted work %q: %s", forbidden, tx.query)
				}
			}
			if result.EntityId != compactUUID(entity) || string(result.EntityKind) != kind || string(result.BroadClass) != broadClass ||
				result.RegionId != "geofabrik:test" || !result.Name.IsNull() || !result.LocalityName.IsNull() || !result.RegionName.IsNull() ||
				result.RangeWorkoutCount != 3 || result.AllTimeWorkoutCount != 7 ||
				result.RangeFirstWorkoutId != compactUUID(first) || result.RangeLatestWorkoutId != compactUUID(latest) ||
				result.AllTimeFirstWorkoutId != compactUUID(allFirst) || result.AllTimeLatestWorkoutId != compactUUID(allLatest) ||
				result.RangeFirstDate.String() != "2026-03-07" || result.RangeLatestDate.String() != "2026-03-08" ||
				result.AllTimeFirstDate.String() != "2026-02-07" || result.AllTimeLatestDate.String() != "2026-04-07" ||
				result.Bounds != (generated.RouteBounds{MinimumLongitude: -105.3, MinimumLatitude: 39.8, MaximumLongitude: -105.1, MaximumLatitude: 40.1}) {
				t.Fatalf("statistics were not preserved: %+v", result)
			}
			response := httptest.NewRecorder()
			writeJSON(response, http.StatusOK, result)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"geometry", "fitBounds"} {
				if _, present := fields[forbidden]; present {
					t.Fatalf("stats response contains %s", forbidden)
				}
			}
			validateRecordedResponse(t, http.MethodGet, "/api/map-selections/"+compactUUID(selection)+"/coverage/"+kind+"/"+compactUUID(entity)+"/stats?generation=7", response)
		})
	}
	for _, want := range []error{pgx.ErrNoRows, errors.New("statistics scan failed")} {
		tx := &coverageStatsTx{row: coverageStatsScanError{err: want}}
		if _, err := queryRoadCoverageEntityStats(context.Background(), tx, account, session, selection, 7, "path", entity); !errors.Is(err, want) || tx.reads != 1 {
			t.Fatalf("statistics lookup must preserve its single-query failure: reads=%d err=%v", tx.reads, err)
		}
	}
}

func TestRoadCoverageEntityStatsRequestContracts(t *testing.T) {
	handler := testHandler(t)
	selection, entity := "018F8E7D7A4C7C03A1C23D4E5F607182", "018F8E7D7A4C7C03A1C23D4E5F607183"
	base := "/api/map-selections/" + selection + "/coverage/"
	for _, test := range []struct {
		name, path string
		status     int
	}{
		{"anonymous path", base + "path/" + entity + "/stats?generation=7", http.StatusUnauthorized},
		{"anonymous park", base + "park/" + entity + "/stats?generation=7", http.StatusUnauthorized},
		{"dashed entity", base + "path/018f8e7d-7a4c-7c03-a1c2-3d4e5f607183/stats?generation=7", http.StatusUnauthorized},
		{"invalid selection", "/api/map-selections/invalid/coverage/path/" + entity + "/stats?generation=7", http.StatusBadRequest},
		{"invalid kind", base + "road/" + entity + "/stats?generation=7", http.StatusBadRequest},
		{"invalid entity", base + "path/invalid/stats?generation=7", http.StatusBadRequest},
		{"missing generation", base + "path/" + entity + "/stats", http.StatusBadRequest},
		{"empty generation", base + "path/" + entity + "/stats?generation=", http.StatusBadRequest},
		{"zero generation", base + "path/" + entity + "/stats?generation=0", http.StatusBadRequest},
		{"negative generation", base + "path/" + entity + "/stats?generation=-1", http.StatusBadRequest},
		{"nonnumeric generation", base + "path/" + entity + "/stats?generation=bad", http.StatusBadRequest},
		{"fractional generation", base + "path/" + entity + "/stats?generation=1.5", http.StatusBadRequest},
		{"overflow generation", base + "path/" + entity + "/stats?generation=9223372036854775808", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			validateRecordedResponse(t, http.MethodGet, test.path, response)
		})
	}
}

func TestRoadCoverageEntityIncludesDynamicRegionName(t *testing.T) {
	name := "Northern California"
	row := roadCoverageRow{
		entityID: uuid.New(), rangeFirstWorkoutID: uuid.New(), rangeLatestWorkoutID: uuid.New(),
		allTimeFirstWorkoutID: uuid.New(), allTimeLatestWorkoutID: uuid.New(), entityKind: "path",
		broadClass: "road", regionID: "geofabrik:norcal", regionName: &name, rangeCount: 1,
		allTimeCount: 1, rangeFirst: time.Now(), rangeLatest: time.Now(), allTimeFirst: time.Now(), allTimeLatest: time.Now(),
	}
	entity := row.entity()
	regionName, err := entity.RegionName.Get()
	if err != nil || regionName != name {
		t.Fatalf("regionName=%q err=%v", regionName, err)
	}
}

func TestNormalizedMapWorkoutIDsCanonicalizesAndRejectsDuplicates(t *testing.T) {
	first := generated.UUIDInput("018F1D0A4B2C7A5E8F90123456789ABC")
	second := generated.UUIDInput("018f1d0a-4b2c-7a5e-8f90-123456789abd")
	values := []generated.UUIDInput{first, second}
	ids, ok := normalizedMapWorkoutIDs(&values)
	if !ok || len(ids) != 2 || compactUUID(ids[0]) != string(first) || compactUUID(ids[1]) != "018F1D0A4B2C7A5E8F90123456789ABD" {
		t.Fatalf("unexpected normalized IDs: %v %v", ids, ok)
	}
	duplicate := []generated.UUIDInput{first, generated.UUIDInput("018f1d0a-4b2c-7a5e-8f90-123456789abc")}
	if _, ok := normalizedMapWorkoutIDs(&duplicate); ok {
		t.Fatal("alternate encodings of one UUID must be rejected as duplicates")
	}
	if ids, ok := normalizedMapWorkoutIDs(nil); !ok || ids != nil {
		t.Fatal("omitted IDs must mean the complete routed range")
	}
}

func TestContainsUUIDRequiresExplicitMembership(t *testing.T) {
	first := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abc")
	second := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abd")
	if !containsUUID([]uuid.UUID{first, second}, second) || containsUUID([]uuid.UUID{first}, second) {
		t.Fatal("focused workout subset membership was evaluated incorrectly")
	}
}

func TestMapTileUpstreamURLUsesOnlyValidatedScope(t *testing.T) {
	selectionID := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abc")
	accountID := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abd")
	sessionID := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abe")
	raw, err := mapTileUpstreamURL("http://workouts-explorer-tiles:3000/internal", selectionID, accountID, sessionID, 42, 12, 655, 1582)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "workouts-explorer-tiles:3000" || parsed.Path != "/internal/app.raw_route_mvt/12/655/1582" {
		t.Fatalf("unexpected upstream route: %s", raw)
	}
	want := map[string]string{
		"target_account_id": accountID.String(), "target_session_id": sessionID.String(),
		"target_selection_id": selectionID.String(), "target_generation": "42",
	}
	if len(parsed.Query()) != len(want) {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
	for key, value := range want {
		if values := parsed.Query()[key]; len(values) != 1 || values[0] != value {
			t.Errorf("%s=%v, want [%q]", key, values, value)
		}
	}
	coverageRaw, err := mapTileFunctionUpstreamURL("http://workouts-explorer-tiles:3000/internal", "app.coverage_mvt",
		selectionID, accountID, sessionID, 42, 12, 655, 1582)
	if err != nil {
		t.Fatal(err)
	}
	coverageURL, _ := url.Parse(coverageRaw)
	if coverageURL.Path != "/internal/app.coverage_mvt/12/655/1582" || coverageURL.RawQuery != parsed.RawQuery {
		t.Fatalf("unexpected upstream coverage route: %s", coverageRaw)
	}
	for _, source := range []string{"public.raw_route_mvt", "public.coverage_mvt", "app.other_route_mvt", "app.other_coverage_mvt", "app.hostile", ""} {
		if _, err := mapTileFunctionUpstreamURL("http://tiles.invalid", source, selectionID, accountID, sessionID, 1, 0, 0, 0); err == nil {
			t.Errorf("unapproved tile function %q was accepted", source)
		}
	}
	if _, err := mapTileUpstreamURL("/relative", selectionID, accountID, sessionID, 1, 0, 0, 0); err == nil {
		t.Fatal("relative internal tile service URL must be rejected")
	}
	for _, base := range []string{"file://tiles/private", "http://user:private@tiles.invalid", "http://tiles.invalid?private=scope", "http://tiles.invalid#private"} {
		if _, err := mapTileUpstreamURL(base, selectionID, accountID, sessionID, 1, 0, 0, 0); err == nil {
			t.Fatal("unsafe internal tile service URL was accepted")
		}
	}
}

func TestValidMVTContentType(t *testing.T) {
	for _, value := range []string{"application/vnd.mapbox-vector-tile", "application/x-protobuf", "application/octet-stream; charset=binary"} {
		if !validMVTContentType(value) {
			t.Errorf("expected %q to be accepted", value)
		}
	}
	for _, value := range []string{"", "text/html", "application/json", "application/vnd.mapbox-vector-tilex"} {
		if validMVTContentType(value) {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}

func TestValidMVTResponseAcceptsMartinEmptyTile(t *testing.T) {
	if !validMVTResponse(http.StatusNoContent, "") {
		t.Fatal("Martin empty tile response was rejected")
	}
	if validMVTResponse(http.StatusOK, "") || validMVTResponse(http.StatusBadGateway, "application/x-protobuf") {
		t.Fatal("invalid populated or upstream error response was accepted")
	}
}

func TestMapCoverageReadinessExposesIndependentStatuses(t *testing.T) {
	pointer := func(value string) *string { return &value }
	tests := []struct {
		state, processing       *string
		applied, appliedCurrent bool
		mapData                 generated.CoverageReadinessMapDataStatus
		processingStatus        generated.CoverageReadinessProcessingStatus
		resultStatus            generated.CoverageReadinessResultStatus
	}{
		{nil, nil, false, false, generated.CoverageReadinessMapDataStatusPending, generated.CoverageReadinessProcessingStatusUnprocessed, generated.CoverageReadinessResultStatusNone},
		{pointer("unresolved"), pointer("not_started"), false, false, generated.CoverageReadinessMapDataStatusPending, generated.CoverageReadinessProcessingStatusUnprocessed, generated.CoverageReadinessResultStatusNone},
		{pointer("pending"), pointer("not_started"), false, false, generated.CoverageReadinessMapDataStatusPending, generated.CoverageReadinessProcessingStatusUnprocessed, generated.CoverageReadinessResultStatusNone},
		{pointer("unavailable"), pointer("not_started"), false, false, generated.CoverageReadinessMapDataStatusUnavailable, generated.CoverageReadinessProcessingStatusUnprocessed, generated.CoverageReadinessResultStatusNone},
		{pointer("map_data_ready"), pointer("queued"), false, false, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusQueued, generated.CoverageReadinessResultStatusNone},
		{pointer("map_data_ready"), pointer("running"), true, false, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusRunning, generated.CoverageReadinessResultStatusStale},
		{pointer("map_data_ready"), pointer("current"), true, true, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusCurrent, generated.CoverageReadinessResultStatusCurrent},
		{pointer("map_data_ready"), pointer("failed"), false, false, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusFailed, generated.CoverageReadinessResultStatusNone},
		{pointer("map_data_ready"), pointer("failed"), true, true, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusFailed, generated.CoverageReadinessResultStatusCurrent},
		{pointer("map_data_ready"), pointer("failed"), true, false, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusFailed, generated.CoverageReadinessResultStatusStale},
		{pointer("map_data_ready"), pointer("stale"), true, false, generated.CoverageReadinessMapDataStatusReady, generated.CoverageReadinessProcessingStatusStale, generated.CoverageReadinessResultStatusStale},
	}
	for _, test := range tests {
		got := mapCoverageReadiness(test.state, test.processing, test.applied, test.appliedCurrent)
		if got.MapDataStatus != test.mapData || got.ProcessingStatus != test.processingStatus || got.ResultStatus != test.resultStatus {
			t.Fatalf("readiness=%+v want map=%s processing=%s result=%s", got, test.mapData, test.processingStatus, test.resultStatus)
		}
	}
}
