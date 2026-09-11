package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

func TestDisabledCoverageDiagnosticsReturnsNotFoundWithoutOSM(t *testing.T) {
	called := false
	server := &Server{diagnostics: &coverageDiagnosticService{begin: func(context.Context) (coverageDiagnosticSnapshot, error) {
		called = true
		return nil, nil
	}}}
	request := httptest.NewRequest(http.MethodPost, "/api/workouts/00000000000000000000000000000000/coverage-diagnostic-runs", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.CreateCoverageDiagnosticRun(response, request, generated.WorkoutID("00000000000000000000000000000000"), generated.CreateCoverageDiagnosticRunParams{})
	if response.Code != http.StatusNotFound || called {
		t.Fatalf("status=%d osm_called=%t", response.Code, called)
	}
}

func TestDiagnosticMovementMode(t *testing.T) {
	for input, want := range map[string]string{"Outdoor Cycling": "bicycle", "Trail Running": "foot", "Other": "shared_public"} {
		if got := string(diagnosticMovementMode("", input)); got != want {
			t.Errorf("mode(%q)=%q want %q", input, got, want)
		}
	}
}

func TestDiagnosticResponseReportsCurrentPathPolicy(t *testing.T) {
	response := diagnosticResponse(diagnosticResult{unavailableRegions: []osm.MatcherUnavailableRegion{{RegionID: "geofabrik:new-york", DisplayName: "New York"}}})
	if response.PathPolicyVersion != generated.CoveragePathPolicyExperimentalV82 {
		t.Fatalf("path policy version=%q", response.PathPolicyVersion)
	}
	if len(response.UnavailableRegions) != 1 || response.UnavailableRegions[0].RegionId != "geofabrik:new-york" || response.UnavailableRegions[0].DisplayName != "New York" {
		t.Fatalf("unavailable regions=%+v", response.UnavailableRegions)
	}
}

func TestValidDiagnosticClipRejectsDegenerateGeometry(t *testing.T) {
	valid := osm.MatcherClippedPortion{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122.0001,37.0001]]}`)}
	if !validDiagnosticClip(valid) {
		t.Fatal("valid line rejected")
	}
	for _, clip := range []osm.MatcherClippedPortion{
		{LengthMeters: 0, GeoJSON: valid.GeoJSON},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122,37]]}`)},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"Point","coordinates":[-122,37]}`)},
	} {
		if validDiagnosticClip(clip) {
			t.Fatalf("degenerate clip accepted: %+v", clip)
		}
	}
}
