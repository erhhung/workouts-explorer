package routepipeline

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

// Reuse the coordinate-based synthetic topology, including oriented fraction
// clips and complete segment copies. No candidate depends on a batch ordinal.
type legSnapshot struct {
	*windowSnapshot
	queries          [][]osm.MatcherObservation
	generationCalls  int
	secondGeneration uuid.UUID
	badClip, badCopy bool
}

func (s *legSnapshot) Candidates(ctx context.Context, points []osm.MatcherObservation, limit int) ([]osm.MatcherCandidate, error) {
	s.queries = append(s.queries, append([]osm.MatcherObservation(nil), points...))
	candidates, err := s.windowSnapshot.Candidates(ctx, points, limit)
	for i := range candidates {
		if candidates[i].SegmentID == s.secondGeneration {
			candidates[i].GenerationID = 2
		}
	}
	return candidates, err
}

func (s *legSnapshot) IncidentEdges(ctx context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	edges, err := s.windowSnapshot.IncidentEdges(ctx, nodes, limit)
	for i := range edges {
		if edges[i].SegmentID == s.secondGeneration {
			edges[i].GenerationID = 2
		}
	}
	return edges, err
}

func (s *legSnapshot) Generations(context.Context) ([]osm.MatcherGeneration, error) {
	s.generationCalls++
	result := []osm.MatcherGeneration{{RegionID: "fixture:adaptive", GenerationID: 1}}
	if s.secondGeneration != uuid.Nil {
		result = append(result, osm.MatcherGeneration{RegionID: "fixture:adaptive", GenerationID: 2})
	}
	return result, nil
}

func (s *legSnapshot) ClipPortions(ctx context.Context, refs []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	normalized := append([]osm.MatcherPortionRef(nil), refs...)
	for i := range normalized {
		normalized[i].GenerationID = 1
	}
	clips, err := s.windowSnapshot.ClipPortions(ctx, normalized)
	copy(s.clipRefs[len(s.clipRefs)-len(refs):], refs)
	for i := range clips {
		clips[i].GenerationID = refs[i].GenerationID
	}
	if s.badClip && len(clips) > 0 {
		clips[len(clips)-1].Ordinal = len(clips)
	}
	return clips, err
}

func (s *legSnapshot) CopySegments(ctx context.Context, refs []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error) {
	normalized := append([]osm.MatcherSegmentRef(nil), refs...)
	for i := range normalized {
		normalized[i].GenerationID = 1
	}
	copies, err := s.windowSnapshot.CopySegments(ctx, normalized)
	copy(s.copyRefs[len(s.copyRefs)-len(refs):], refs)
	for i := range copies {
		copies[i].GenerationID = refs[i].GenerationID
	}
	if s.badCopy && len(copies) > 0 {
		copies[len(copies)-1].SegmentID = uuid.Nil
	}
	return copies, err
}

// Each span is a real decoded chunk, with a >30s temporal gap before the next.
func legPoints(spans ...[2]float64) []coverage.GeographicObservation {
	var points []coverage.GeographicObservation
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for leg, span := range spans {
		for j := 0; j < 4; j++ {
			points = append(points, coverage.GeographicObservation{Sequence: 100 + len(points)*7,
				Longitude: (span[0] + (span[1]-span[0])*float64(j)/3) / fixtureMetersPerDegree,
				Time:      start.Add(time.Duration(leg)*time.Hour + time.Duration(j)*time.Second), AccuracyMeters: 3})
		}
	}
	return points
}

func TestLegAggregationUnionAndFirstVisit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		second             [2]float64
		intervals          [][2]float64
		backwards, missing bool
	}{
		{name: "identical", second: [2]float64{10, 40}, intervals: [][2]float64{{10, 40}}},
		{name: "overlap", second: [2]float64{30, 60}, intervals: [][2]float64{{10, 60}}},
		{name: "opposite", second: [2]float64{40, 10}, intervals: [][2]float64{{10, 40}}},
		{name: "hole", second: [2]float64{70, 90}, intervals: [][2]float64{{10, 40}, {70, 90}}},
		{name: "backwards clock", second: [2]float64{30, 60}, intervals: [][2]float64{{10, 60}}, backwards: true},
		{name: "missing first time", second: [2]float64{30, 60}, intervals: [][2]float64{{10, 60}}, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			points := legPoints([2]float64{10, 40}, tc.second)
			if tc.backwards {
				for i := 4; i < len(points); i++ {
					points[i].Time = points[i].Time.Add(-2 * time.Hour)
				}
			}
			if tc.missing {
				points[0].Time = time.Time{}
			}
			snapshot := &legSnapshot{windowSnapshot: &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{100, 0})}}
			result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.queries) != 2 || len(snapshot.queries[0]) != 4 || len(snapshot.queries[1]) != 4 {
				t.Errorf("candidate batches crossed legs: %+v", snapshot.queries)
			}
			if len(result.Evidence) != 2 || result.Counts.Portions != 2 || result.Counts.UniqueSegments != 1 ||
				result.Counts.SampledPoints != 8 || result.Counts.MatchedPoints+result.Counts.AmbiguousPoints != 8 ||
				snapshot.generationCalls != 1 || len(result.Generations) != 1 {
				t.Fatalf("lost/global accounting: result=%+v generation calls=%d", result, snapshot.generationCalls)
			}
			for i, evidence := range result.Evidence {
				if evidence.RouteOrder != i*4 || evidence.FirstTraversedAt != points[i*4].Time || !ValidClip(evidence.Clipped) {
					t.Fatalf("non-global sampled order/time or invalid clip: %+v", evidence)
				}
			}
			wantRefs := 2
			if tc.name == "identical" {
				wantRefs = 1
			}
			if snapshot.clipCalls != 1 || len(snapshot.clipRefs) != wantRefs {
				t.Fatalf("evaluation did not globally deduplicate refs: %+v", snapshot.clipRefs)
			}
			fallback := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
			prepared, err := Prepare(context.Background(), snapshot, result, fallback)
			if err != nil {
				t.Fatal(err)
			}
			if len(prepared) != 1 || snapshot.copyCalls != 1 || len(snapshot.copyRefs) != 1 || snapshot.clipCalls != 2 || len(snapshot.clipRefs) != wantRefs+len(tc.intervals) {
				t.Fatalf("not one global dissolve/copy: prepared=%+v refs=%+v copies=%+v", prepared, snapshot.clipRefs, snapshot.copyRefs)
			}
			item := prepared[0]
			first := points[0].Time
			if tc.missing {
				first = fallback
			}
			if item.FirstRouteOrder != 0 || item.FirstTraversedAt != first || item.Segment.SourceWayID != 1 || item.Segment.Highway != "residential" || len(item.Segment.GeoJSON) == 0 {
				t.Fatalf("earliest route visit/copy lost: %+v", item)
			}
			var geometry struct {
				Type        string
				Coordinates [][][]float64
			}
			if err := json.Unmarshal(item.CoveredGeoJSON, &geometry); err != nil {
				t.Fatal(err)
			}
			if geometry.Type != "MultiLineString" || len(geometry.Coordinates) != len(tc.intervals) {
				t.Fatalf("hole bridged: %s", item.CoveredGeoJSON)
			}
			meters := 0.0
			for i, interval := range tc.intervals {
				line := geometry.Coordinates[i]
				if len(line) != 2 {
					t.Fatalf("unexpected line: %v", line)
				}
				for j, x := range interval {
					if len(line[j]) != 2 || math.Abs(line[j][0]*fixtureMetersPerDegree-x) > 1e-6 || line[j][1] != 0 {
						t.Fatalf("wrong interval/orientation: %s", item.CoveredGeoJSON)
					}
				}
				meters += interval[1] - interval[0]
			}
			if math.Abs(item.CoveredMeters-meters) > 1e-6 {
				t.Fatalf("double-counted union: got=%f want=%f", item.CoveredMeters, meters)
			}
		})
	}
}

func TestLegAggregationGlobalLimitsAndAtomicFailure(t *testing.T) {
	for _, field := range []string{"sampled points", "portions", "segments", "generations", "later leg", "evaluate clip", "prepare clip", "prepare copy"} {
		t.Run(field, func(t *testing.T) {
			roads := append(fixtureRoads(fixturePoint{0, 0}, fixturePoint{100, 0}), fixtureRoad{
				a: fixturePoint{0, 45 * fixtureMetersPerDegree}, b: fixturePoint{0, 45*fixtureMetersPerDegree + 100},
				id: fixtureID("northern leg"), path: fixtureID("northern path"), from: fixtureID("north start"), to: fixtureID("north end")})
			points := legPoints([2]float64{10, 40}, [2]float64{160, 190})
			for i := 4; i < len(points); i++ {
				points[i].Latitude, points[i].Longitude = 45+points[i].Longitude-150/fixtureMetersPerDegree, 0
			}
			snapshot := &legSnapshot{windowSnapshot: &windowSnapshot{roads: roads}, secondGeneration: roads[1].id}
			cfg := fixtureConfig()
			sentinel := errors.New("synthetic later leg failure")
			switch field {
			case "sampled points":
				cfg.Limits.SampledPoints = 4
			case "portions":
				cfg.Limits.Portions = 1
			case "segments":
				cfg.Limits.Segments = 1
			case "generations":
				cfg.Limits.Generations = 1
			case "later leg":
				snapshot.fail = func(query []osm.MatcherObservation) error {
					for _, point := range query {
						if point.Latitude > 1 {
							return sentinel
						}
					}
					return nil
				}
			case "evaluate clip":
				snapshot.badClip = true
			}
			result, err := Evaluate(context.Background(), snapshot, points, cfg)
			for _, query := range snapshot.queries {
				for _, point := range query {
					if (point.Latitude > 1) != (query[0].Latitude > 1) {
						t.Fatal("candidate batch mixed distant geographic legs")
					}
				}
			}
			if field == "prepare clip" || field == "prepare copy" {
				if err != nil || len(result.Evidence) != 2 || len(result.Generations) != 2 || snapshot.generationCalls != 1 {
					t.Fatalf("baseline: result=%+v err=%v", result, err)
				}
				snapshot.badClip, snapshot.badCopy = field == "prepare clip", field == "prepare copy"
				prepared, prepareErr := Prepare(context.Background(), snapshot, result, points[0].Time)
				if prepareErr == nil || prepared != nil {
					t.Fatalf("partial preparation: %+v err=%v", prepared, prepareErr)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("partial evaluation: %+v err=%v", result, err)
			}
			if field == "later leg" {
				if !errors.Is(err, sentinel) || len(snapshot.queries) != 2 || len(snapshot.queries[0]) != 4 {
					t.Fatalf("failure was not after an accepted leg: queries=%+v err=%v", snapshot.queries, err)
				}
			} else if field != "evaluate clip" {
				var limit *LimitError
				if !errors.As(err, &limit) || limit.Field != field {
					t.Fatalf("wrong limit: %v", err)
				}
			}
			beforeClips := snapshot.clipCalls
			prepared, prepareErr := Prepare(context.Background(), snapshot, result, time.Time{})
			if prepareErr != nil || prepared != nil || snapshot.clipCalls != beforeClips || snapshot.copyCalls != 0 {
				t.Fatal("failed result retained downstream work")
			}
		})
	}
}

func TestLegAggregationThresholdAndEmptyLegCounts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		spans     [][2]float64
		roads     bool
		threshold float64
		evidence  int
	}{
		{name: "empty", threshold: 5},
		{name: "singletons and no candidates", spans: [][2]float64{{10, 10}, {60, 90}}, threshold: 5},
		{name: "three meters each cannot combine", spans: [][2]float64{{10, 13}, {30, 33}}, roads: true, threshold: 5},
		{name: "configured threshold unchanged", spans: [][2]float64{{10, 13}, {30, 33}}, roads: true, threshold: 2, evidence: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			points := legPoints(tc.spans...)
			if tc.name == "singletons and no candidates" {
				points = append(points[:1:1], points[4:]...)
			}
			snapshot := &legSnapshot{windowSnapshot: &windowSnapshot{}}
			if tc.roads {
				snapshot.roads = fixtureRoads(fixturePoint{0, 0}, fixturePoint{100, 0})
			}
			cfg := fixtureConfig()
			cfg.Rules = cfg.Rules.WithMinimumTraversalMeters(tc.threshold)
			result, err := Evaluate(context.Background(), snapshot, points, cfg)
			if err != nil {
				t.Fatal(err)
			}
			counts := result.Counts
			if len(result.Evidence) != tc.evidence || counts.OriginalPoints != len(points) || counts.SampledPoints != len(points) ||
				counts.MatchedPoints+counts.AmbiguousPoints+counts.UnmatchedPoints+counts.RejectedPoints != counts.SampledPoints {
				t.Fatalf("per-chunk threshold or status accounting changed: %+v", result)
			}
			if !tc.roads && counts.UnmatchedPoints != len(points) {
				t.Fatalf("no-candidate statuses: %+v", counts)
			}
		})
	}
}

func TestLegAggregationRawEqualsSampledWithoutMutation(t *testing.T) {
	points := legPoints([2]float64{10, 19}, [2]float64{50, 59})
	before := append([]coverage.GeographicObservation(nil), points...)
	for i := range points {
		// Distinct heading fields in one quality band still permit real thinning.
		heading, accuracy := 90+float64(i), 10+float64(i)/10
		points[i].HeadingDegrees, points[i].HeadingAccuracy = &heading, &accuracy
		h, a := heading, accuracy
		before[i] = points[i]
		before[i].HeadingDegrees, before[i].HeadingAccuracy = &h, &a
	}
	cfg := fixtureConfig()
	cfg.Sampling = coverage.ExperimentalSamplingRules()
	sampled := coverage.SampleGeographicObservations(points, cfg.Sampling, cfg.Rules)
	if len(sampled) >= len(points) {
		t.Fatal("fixture did not exercise sampling")
	}
	a := &legSnapshot{windowSnapshot: &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{100, 0})}}
	b := &legSnapshot{windowSnapshot: &windowSnapshot{roads: a.roads}}
	raw, err := Evaluate(context.Background(), a, points, cfg)
	if err != nil {
		t.Fatal(err)
	}
	thin, err := Evaluate(context.Background(), b, sampled, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Counts.OriginalPoints != len(points) || thin.Counts.OriginalPoints != len(sampled) {
		t.Fatal("original counts changed")
	}
	raw.Counts.OriginalPoints = thin.Counts.OriginalPoints
	if !reflect.DeepEqual(raw, thin) || !reflect.DeepEqual(a.queries, b.queries) || !reflect.DeepEqual(points, before) {
		t.Fatal("raw/sampled results differ or input mutated")
	}
	if len(raw.Evidence) != 2 || raw.Evidence[0].RouteOrder != 0 || raw.Evidence[1].RouteOrder != 3 {
		t.Fatalf("route order is not the global sampled index: %+v", raw.Evidence)
	}
	preparedRaw, rawErr := Prepare(context.Background(), a, raw, points[0].Time)
	preparedThin, thinErr := Prepare(context.Background(), b, thin, points[0].Time)
	if rawErr != nil || thinErr != nil || !reflect.DeepEqual(preparedRaw, preparedThin) || !reflect.DeepEqual(points, before) {
		t.Fatalf("raw/sampled preparation differs: raw=%+v sampled=%+v errors=%v/%v", preparedRaw, preparedThin, rawErr, thinErr)
	}
	var got [][2]float64
	for _, query := range a.queries {
		for _, point := range query {
			got = append(got, [2]float64{point.Longitude, point.Latitude})
		}
	}
	var want [][2]float64
	for _, point := range sampled {
		want = append(want, [2]float64{point.Longitude, point.Latitude})
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("not exactly global sampled observations: got=%v want=%v", got, want)
	}
}
