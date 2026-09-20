package routepipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

type emptySnapshot struct {
	unavailable []osm.MatcherUnavailableRegion
}

type preparationSnapshot struct {
	emptySnapshot
	refs []osm.MatcherPortionRef
	copy osm.MatcherSegmentCopy
}

func (s *preparationSnapshot) ClipPortions(_ context.Context, refs []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	s.refs = append(s.refs, refs...)
	result := make([]osm.MatcherClippedPortion, len(refs))
	for i, ref := range refs {
		result[i] = osm.MatcherClippedPortion{Ordinal: i, SegmentID: ref.SegmentID, RegionID: ref.RegionID,
			GenerationID: ref.GenerationID, LengthMeters: 50,
			GeoJSON: json.RawMessage(`{"type":"LineString","coordinates":[[-122,37],[-122.001,37.001]]}`)}
	}
	return result, nil
}

func (s *preparationSnapshot) CopySegments(context.Context, []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error) {
	return []osm.MatcherSegmentCopy{s.copy}, nil
}

func (emptySnapshot) Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error) {
	return nil, nil
}

func (emptySnapshot) IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error) {
	return nil, nil
}

func (emptySnapshot) ClipPortions(context.Context, []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	return nil, nil
}

func (emptySnapshot) Generations(context.Context) ([]osm.MatcherGeneration, error) { return nil, nil }

func (s emptySnapshot) UnavailableRegions(context.Context, []osm.MatcherObservation) ([]osm.MatcherUnavailableRegion, error) {
	return s.unavailable, nil
}

func TestMovementMode(t *testing.T) {
	for input, want := range map[string]coverage.MovementMode{
		"Outdoor Cycling": coverage.MovementBicycle,
		"Trail Running":   coverage.MovementFoot,
		"Other":           coverage.MovementSharedPublic,
	} {
		if got := MovementMode("", input); got != want {
			t.Errorf("mode(%q)=%q want %q", input, got, want)
		}
	}
}

func TestEvaluateEmptyRouteReportsUnavailableRegions(t *testing.T) {
	want := osm.MatcherUnavailableRegion{RegionID: "geofabrik:new-york", DisplayName: "New York"}
	result, err := Evaluate(context.Background(), emptySnapshot{unavailable: []osm.MatcherUnavailableRegion{want}}, nil, Config{
		Rules: coverage.ExperimentalRules(), Sampling: coverage.ExperimentalSamplingRules(), Limits: DiagnosticLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.OriginalPoints != 0 || result.Counts.SampledPoints != 0 || len(result.Evidence) != 0 ||
		len(result.UnavailableRegions) != 1 || result.UnavailableRegions[0] != want {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestValidClip(t *testing.T) {
	valid := osm.MatcherClippedPortion{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122.0001,37.0001]]}`)}
	if !ValidClip(valid) {
		t.Fatal("valid line rejected")
	}
	for _, clip := range []osm.MatcherClippedPortion{
		{LengthMeters: 0, GeoJSON: valid.GeoJSON},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122,37]]}`)},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"Point","coordinates":[-122,37]}`)},
	} {
		if ValidClip(clip) {
			t.Fatalf("degenerate clip accepted: %+v", clip)
		}
	}
}

func TestPrepareMergesRepeatedSegmentIntervals(t *testing.T) {
	segmentID, pathID := uuid.New(), uuid.New()
	snapshot := &preparationSnapshot{copy: osm.MatcherSegmentCopy{SegmentID: segmentID, RegionID: "geofabrik:test",
		GenerationID: 1, LogicalPathID: pathID}}
	first := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	result, err := Prepare(context.Background(), snapshot, Result{Evidence: []Evidence{
		{Portion: coverage.TraversedPortion{PhysicalSegmentID: segmentID, RegionID: "geofabrik:test", GenerationID: 1,
			SourceFromFraction: 0.1, SourceToFraction: 0.6}, RouteOrder: 10, FirstTraversedAt: first},
		{Portion: coverage.TraversedPortion{PhysicalSegmentID: segmentID, RegionID: "geofabrik:test", GenerationID: 1,
			SourceFromFraction: 0.4, SourceToFraction: 0.9}, RouteOrder: 20, FirstTraversedAt: first.Add(time.Minute)},
	}}, first.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || len(snapshot.refs) != 1 || snapshot.refs[0].SourceFromFraction != 0.1 ||
		snapshot.refs[0].SourceToFraction != 0.9 || result[0].FirstTraversedAt != first || result[0].CoveredMeters != 50 {
		t.Fatalf("unexpected prepared result: %+v refs=%+v", result, snapshot.refs)
	}
}
