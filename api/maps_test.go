package api

import (
	"net/url"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
)

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

func TestMapTileUpstreamURLUsesOnlyValidatedScope(t *testing.T) {
	selectionID := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abc")
	accountID := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abd")
	sessionID := uuid.MustParse("018f1d0a-4b2c-7a5e-8f90-123456789abe")
	raw, err := mapTileUpstreamURL("http://pg-tileserv:7800/internal", selectionID, accountID, sessionID, 42, 12, 655, 1582)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "pg-tileserv:7800" || parsed.Path != "/internal/app.raw_route_mvt/12/655/1582.pbf" {
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
