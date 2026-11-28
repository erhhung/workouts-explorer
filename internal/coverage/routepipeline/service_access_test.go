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

type serviceSnapshot struct {
	*windowSnapshot
	tags map[uuid.UUID]json.RawMessage
}

func (s *serviceSnapshot) Candidates(ctx context.Context, points []osm.MatcherObservation, limit int) ([]osm.MatcherCandidate, error) {
	result, err := s.windowSnapshot.Candidates(ctx, points, limit)
	for i := range result {
		if tags, ok := s.tags[result[i].SegmentID]; ok {
			result[i].Highway = "service"
			result[i].Tags = tags
		}
	}
	return result, err
}
func (s *serviceSnapshot) IncidentEdges(ctx context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	result, err := s.windowSnapshot.IncidentEdges(ctx, nodes, limit)
	for i := range result {
		if tags, ok := s.tags[result[i].SegmentID]; ok {
			result[i].Highway = "service"
			result[i].Tags = tags
		}
	}
	return result, err
}
func (s *serviceSnapshot) CopySegments(ctx context.Context, refs []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error) {
	result, err := s.windowSnapshot.CopySegments(ctx, refs)
	for i := range result {
		if tags, ok := s.tags[result[i].SegmentID]; ok {
			result[i].Highway = "service"
			result[i].Tags = tags
		}
	}
	return result, err
}
func (s *serviceSnapshot) Generations(context.Context) ([]osm.MatcherGeneration, error) {
	return []osm.MatcherGeneration{{RegionID: "fixture:adaptive", GenerationID: 1, ImporterVersion: 4}}, nil
}

func TestServiceAccessFullPipelineAndLegacySemantics(t *testing.T) {
	for _, test := range []struct {
		name, tags, class string
		want              bool
	}{
		{"unnamed neighborhood", `{"service":"driveway","workouts:residential_context_version":"1","workouts:residential_access":"shared"}`, "shared_road", true},
		{"single house", `{"service":"driveway","name":"Named Private Drive","access":"private","workouts:residential_context_version":"1","workouts:residential_access":"single_house"}`, "", false},
		{"parking aisle", `{"service":"parking_aisle"}`, "parking_aisle", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			roads := fixtureRoads(fixturePoint{0, 0}, fixturePoint{100, 0})
			snapshot := &serviceSnapshot{windowSnapshot: &windowSnapshot{roads: roads}, tags: map[uuid.UUID]json.RawMessage{roads[0].id: json.RawMessage(test.tags)}}
			points := make([]coverage.GeographicObservation, 51)
			for i := range points {
				points[i] = coverage.GeographicObservation{Sequence: i, Longitude: float64(i*2) / fixtureMetersPerDegree, Time: time.Unix(int64(i), 0).UTC(), AccuracyMeters: 1.5}
			}
			cfg := fixtureConfig()
			cfg.PathPolicyVersion = coverage.ExperimentalPathPolicyV84
			result, err := Evaluate(context.Background(), snapshot, points, cfg)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := Prepare(context.Background(), snapshot, result, time.Unix(1, 0))
			if err != nil {
				t.Fatal(err)
			}
			if !test.want {
				if len(prepared) != 0 {
					t.Fatal("individual house became coverage")
				}
				return
			}
			if len(prepared) != 1 || prepared[0].CoveredMeters < 90 || prepared[0].DisplayClass != test.class {
				t.Fatalf("missing supported service road: %+v", prepared)
			}
			cfg.PathPolicyVersion = coverage.ExperimentalPathPolicyV83
			legacy, err := Evaluate(context.Background(), snapshot, points, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(legacy.Evidence) != 0 {
				t.Fatal("new service eligibility leaked into a queued v83 job")
			}
		})
	}
}

func TestParkingEntranceAndNearbyParallelRoadAreNotCoverage(t *testing.T) {
	for _, test := range []struct {
		name     string
		from, to fixturePoint
	}{
		{"crossed entrance", fixturePoint{50, 0}, fixturePoint{50, 80}},
		{"nearby parallel aisle", fixturePoint{0, 15}, fixturePoint{100, 15}},
	} {
		t.Run(test.name, func(t *testing.T) {
			roads := fixtureRoads(fixturePoint{0, 0}, fixturePoint{100, 0})
			target := fixtureRoad{a: test.from, b: test.to, id: fixtureID("parking-negative"), path: fixtureID("parking-path"), from: fixtureID("parking-start"), to: fixtureID("parking-end")}
			snapshot := &serviceSnapshot{windowSnapshot: &windowSnapshot{roads: append(roads, target)}, tags: map[uuid.UUID]json.RawMessage{target.id: json.RawMessage(`{"service":"parking_aisle"}`)}}
			points := make([]coverage.GeographicObservation, 51)
			for i := range points {
				points[i] = coverage.GeographicObservation{Sequence: i, Longitude: float64(i*2) / fixtureMetersPerDegree, Time: time.Unix(int64(i), 0).UTC(), AccuracyMeters: 1.5}
			}
			result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range result.Evidence {
				if e.Portion.PhysicalSegmentID == target.id {
					t.Fatal("passing an aisle entrance granted Coverage")
				}
			}
			if len(result.Evidence) == 0 {
				t.Fatal("negative control suppressed the real public-road traversal")
			}
		})
	}
}
