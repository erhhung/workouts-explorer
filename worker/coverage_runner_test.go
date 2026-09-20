package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

func TestCoveragePersistencePayloadKeepsGeometryAsJSON(t *testing.T) {
	payload := coveragePersistencePayload([]routepipeline.PreparedMatch{{
		Segment: osm.MatcherSegmentCopy{SegmentID: uuid.New(), RegionID: "geofabrik:test", GenerationID: 1,
			LogicalPathID: uuid.New(), DerivationVersion: 1, SourceWayID: 42, SourceWayVersion: 1,
			Highway: "residential", BroadClass: "road", Tags: json.RawMessage(`{"surface":"paved"}`),
			GeoJSON: json.RawMessage(`{"type":"LineString","coordinates":[[-122,37],[-122.001,37.001]]}`), LengthMeters: 100},
		CoveredGeoJSON:   json.RawMessage(`{"type":"MultiLineString","coordinates":[[[-122,37],[-122.001,37.001]]]}`),
		FirstTraversedAt: time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC),
	}})
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded[0]["segmentGeometry"].(map[string]any); !ok {
		t.Fatalf("segment geometry was not embedded JSON: %s", encoded)
	}
	if _, ok := decoded[0]["coveredGeometry"].(map[string]any); !ok {
		t.Fatalf("covered geometry was not embedded JSON: %s", encoded)
	}
}

func TestClassifyCoverageFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if code, _ := classifyCoverageFailure(ctx, context.DeadlineExceeded); code != "coverage-route-timeout" {
		t.Fatalf("timeout code=%s", code)
	}
	if code, _ := classifyCoverageFailure(context.Background(), &routepipeline.LimitError{Field: "segments", Limit: 1}); code != "coverage-route-too-large" {
		t.Fatalf("limit code=%s", code)
	}
}

func TestCoverageRouteTimeoutBackoff(t *testing.T) {
	for _, test := range []struct {
		retries int
		want    time.Duration
	}{{0, 3 * time.Minute}, {1, 6 * time.Minute}, {2, 10 * time.Minute}, {3, 10 * time.Minute}, {16, 10 * time.Minute}} {
		if got := coverageRouteTimeout(3*time.Minute, test.retries); got != test.want {
			t.Errorf("coverageRouteTimeout(180s, %d) = %s, want %s", test.retries, got, test.want)
		}
	}
}
