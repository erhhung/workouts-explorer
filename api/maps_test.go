package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
)

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
		if parsed.Query().Get(key) != value {
			t.Errorf("%s=%q, want %q", key, parsed.Query().Get(key), value)
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
	if _, err := mapTileFunctionUpstreamURL("http://tiles.invalid", "app.hostile", selectionID, accountID, sessionID, 1, 0, 0, 0); err == nil {
		t.Fatal("unknown tile function was accepted")
	}
	if _, err := mapTileUpstreamURL("/relative", selectionID, accountID, sessionID, 1, 0, 0, 0); err == nil {
		t.Fatal("relative internal tile service URL must be rejected")
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

func TestMapCoverageReadinessUsesConservativePublicEnums(t *testing.T) {
	pointer := func(value string) *string { return &value }
	tests := []struct {
		state, processing, reason *string
		want                      generated.CoverageReadinessState
		wantReason                *generated.CoverageReadinessReason
	}{
		{nil, nil, nil, generated.CoverageReadinessStatePending, nil},
		{pointer("unresolved"), pointer("not_started"), nil, generated.CoverageReadinessStatePending, nil},
		{pointer("pending"), pointer("not_started"), pointer("region_not_active"), generated.CoverageReadinessStatePending, reasonPointer(generated.RegionNotActive)},
		{pointer("map_data_ready"), pointer("not_started"), nil, generated.CoverageReadinessStateNotProcessed, nil},
		{pointer("unavailable"), pointer("not_started"), pointer("no_provider_region"), generated.CoverageReadinessStateUnavailable, reasonPointer(generated.NoProviderRegion)},
	}
	for _, test := range tests {
		got := mapCoverageReadiness(test.state, test.processing, test.reason)
		if got.State != test.want || (got.Reason == nil) != (test.wantReason == nil) || (got.Reason != nil && *got.Reason != *test.wantReason) {
			t.Fatalf("readiness=%+v want=%s reason=%v", got, test.want, test.wantReason)
		}
	}
}

func reasonPointer(value generated.CoverageReadinessReason) *generated.CoverageReadinessReason {
	return &value
}
