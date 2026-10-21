package routepipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

const fixtureMetersPerDegree = 111195.0802335329

type fixturePoint struct{ x, y float64 }
type fixtureRoad struct {
	a, b               fixturePoint
	id, path, from, to uuid.UUID
}
type fixtureWindow struct{ start, size int }

// Entirely synthetic equatorial geometry. Projections depend on coordinates,
// never on batch ordinals, so retries and overlapping windows see identical OSM.
type windowSnapshot struct {
	emptySnapshot
	roads     []fixtureRoad
	calls     []fixtureWindow
	fail      func([]osm.MatcherObservation) error
	ctx       context.Context
	clipCalls int
	clipRefs  []osm.MatcherPortionRef
	copyCalls int
	copyRefs  []osm.MatcherSegmentRef
}

func fixtureID(name string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)) }

func fixtureRoads(vertices ...fixturePoint) []fixtureRoad {
	roads := make([]fixtureRoad, len(vertices)-1)
	for i := range roads {
		roads[i] = fixtureRoad{a: vertices[i], b: vertices[i+1], id: fixtureID(fmt.Sprint("road", i)),
			path: fixtureID(fmt.Sprint("path", i)), from: fixtureID(fmt.Sprint("node", i)), to: fixtureID(fmt.Sprint("node", i+1))}
	}
	return roads
}

func (s *windowSnapshot) Candidates(ctx context.Context, observations []osm.MatcherObservation, limit int) ([]osm.MatcherCandidate, error) {
	if s.ctx != nil && ctx != s.ctx {
		return nil, errors.New("context changed")
	}
	if limit != 256 {
		return nil, fmt.Errorf("candidate bound changed: %d", limit)
	}
	start := 0
	if len(observations) > 0 {
		start = int(math.Round((observations[0].Longitude*fixtureMetersPerDegree - 10) / 2))
	}
	s.calls = append(s.calls, fixtureWindow{start, len(observations)})
	if s.fail != nil {
		if err := s.fail(observations); err != nil {
			return nil, err
		}
	}
	var candidates []osm.MatcherCandidate
	for i, observation := range observations {
		p := fixturePoint{observation.Longitude * fixtureMetersPerDegree, observation.Latitude * fixtureMetersPerDegree}
		for _, road := range s.roads {
			dx, dy := road.b.x-road.a.x, road.b.y-road.a.y
			length := math.Hypot(dx, dy)
			fraction := math.Max(0, math.Min(1, ((p.x-road.a.x)*dx+(p.y-road.a.y)*dy)/(length*length)))
			projection := fixturePoint{road.a.x + fraction*dx, road.a.y + fraction*dy}
			distance := math.Hypot(p.x-projection.x, p.y-projection.y)
			if distance > observation.RadiusMeters {
				continue
			}
			tangent := math.Mod(math.Atan2(dx, dy)*180/math.Pi+360, 360)
			candidates = append(candidates, osm.MatcherCandidate{ObservationIndex: i, SegmentID: road.id,
				RegionID: "fixture:adaptive", GenerationID: 1, DerivationVersion: 1, SourceWayID: 1, SourceWayVersion: 1,
				StartGraphNodeID: road.from, EndGraphNodeID: road.to, LogicalPathID: road.path,
				Highway: "residential", BroadClass: "road", MotorForwardAllowed: true, MotorReverseAllowed: true,
				LengthMeters: length, Fraction: fraction, DistanceMeters: distance, TangentDegrees: &tangent,
				ProjectedLongitude: projection.x / fixtureMetersPerDegree, ProjectedLatitude: projection.y / fixtureMetersPerDegree,
				StartLongitude: road.a.x / fixtureMetersPerDegree, StartLatitude: road.a.y / fixtureMetersPerDegree,
				EndLongitude: road.b.x / fixtureMetersPerDegree, EndLatitude: road.b.y / fixtureMetersPerDegree})
		}
	}
	return candidates, nil
}

func (s *windowSnapshot) IncidentEdges(ctx context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	if s.ctx != nil && ctx != s.ctx {
		return nil, errors.New("context changed")
	}
	if limit > 4096 || len(nodes) > 256 {
		return nil, errors.New("graph query bound changed")
	}
	var edges []osm.MatcherIncidentEdge
	for _, node := range nodes {
		for _, road := range s.roads {
			if node != road.from && node != road.to {
				continue
			}
			edges = append(edges, osm.MatcherIncidentEdge{RequestedNodeID: node, SegmentID: road.id,
				RegionID: "fixture:adaptive", GenerationID: 1, DerivationVersion: 1, SourceWayID: 1, SourceWayVersion: 1,
				StartGraphNodeID: road.from, EndGraphNodeID: road.to, LogicalPathID: road.path,
				Highway: "residential", BroadClass: "road", MotorForwardAllowed: true, MotorReverseAllowed: true,
				LengthMeters:   math.Hypot(road.b.x-road.a.x, road.b.y-road.a.y),
				StartLongitude: road.a.x / fixtureMetersPerDegree, StartLatitude: road.a.y / fixtureMetersPerDegree,
				EndLongitude: road.b.x / fixtureMetersPerDegree, EndLatitude: road.b.y / fixtureMetersPerDegree})
		}
	}
	return edges, nil
}

func (s *windowSnapshot) Generations(context.Context) ([]osm.MatcherGeneration, error) {
	return []osm.MatcherGeneration{{RegionID: "fixture:adaptive", GenerationID: 1}}, nil
}

func (s *windowSnapshot) ClipPortions(_ context.Context, refs []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	s.clipCalls++
	s.clipRefs = append(s.clipRefs, refs...)
	clips := make([]osm.MatcherClippedPortion, len(refs))
	for i, ref := range refs {
		if ref.SourceFromFraction < 0 || ref.SourceToFraction > 1 || ref.SourceFromFraction >= ref.SourceToFraction || ref.RegionID != "fixture:adaptive" || ref.GenerationID != 1 {
			return nil, fmt.Errorf("invalid synthetic clip ref: %+v", ref)
		}
		found := false
		for _, road := range s.roads {
			if road.id != ref.SegmentID {
				continue
			}
			found = true
			coordinates := make([][]float64, 2)
			for j, fraction := range []float64{ref.SourceFromFraction, ref.SourceToFraction} {
				coordinates[j] = []float64{(road.a.x + fraction*(road.b.x-road.a.x)) / fixtureMetersPerDegree, (road.a.y + fraction*(road.b.y-road.a.y)) / fixtureMetersPerDegree}
			}
			if ref.Direction == osm.MatcherReverse {
				coordinates[0], coordinates[1] = coordinates[1], coordinates[0]
			}
			geometry, _ := json.Marshal(struct {
				Type        string      `json:"type"`
				Coordinates [][]float64 `json:"coordinates"`
			}{"LineString", coordinates})
			clips[i] = osm.MatcherClippedPortion{Ordinal: i, SegmentID: ref.SegmentID, RegionID: ref.RegionID, GenerationID: ref.GenerationID,
				Direction: ref.Direction, SourceFromFraction: ref.SourceFromFraction, SourceToFraction: ref.SourceToFraction,
				LengthMeters: (ref.SourceToFraction - ref.SourceFromFraction) * math.Hypot(road.b.x-road.a.x, road.b.y-road.a.y), GeoJSON: geometry}
		}
		if !found {
			return nil, errors.New("unknown synthetic segment")
		}
	}
	return clips, nil
}

func (s *windowSnapshot) CopySegments(_ context.Context, refs []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error) {
	s.copyCalls++
	s.copyRefs = append(s.copyRefs, refs...)
	copies := make([]osm.MatcherSegmentCopy, len(refs))
	for i, ref := range refs {
		found := false
		for _, road := range s.roads {
			if road.id != ref.SegmentID || ref.RegionID != "fixture:adaptive" || ref.GenerationID != 1 {
				continue
			}
			found = true
			geometry, _ := json.Marshal(struct {
				Type        string      `json:"type"`
				Coordinates [][]float64 `json:"coordinates"`
			}{"LineString", [][]float64{{road.a.x / fixtureMetersPerDegree, road.a.y / fixtureMetersPerDegree}, {road.b.x / fixtureMetersPerDegree, road.b.y / fixtureMetersPerDegree}}})
			copies[i] = osm.MatcherSegmentCopy{Ordinal: i, SegmentID: ref.SegmentID, RegionID: ref.RegionID, GenerationID: ref.GenerationID,
				LogicalPathID: road.path, DerivationVersion: 1, SourceWayID: 1, SourceWayVersion: 1, Highway: "residential", BroadClass: "road",
				LengthMeters: math.Hypot(road.b.x-road.a.x, road.b.y-road.a.y), GeoJSON: geometry}
		}
		if !found {
			return nil, errors.New("unknown synthetic segment copy")
		}
	}
	return copies, nil
}

func fixtureConfig() Config {
	sampling := coverage.ExperimentalSamplingRules()
	sampling.MinimumDistanceMeters = 0
	return Config{Rules: coverage.ExperimentalRules(), Sampling: sampling, RoadGeometry: coverage.DefaultRoadGeometryRules(),
		MovementMode: coverage.MovementFoot, Limits: DefaultLimits()}
}

func fixtureObservations(count int) []coverage.GeographicObservation {
	points := make([]coverage.GeographicObservation, count)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range points {
		points[i] = coverage.GeographicObservation{Sequence: i, Longitude: (10 + 2*float64(i)) / fixtureMetersPerDegree, Time: start.Add(time.Duration(i) * time.Second), AccuracyMeters: 3}
	}
	return points
}

func TestAdaptivePrimaryWindows(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  func(int) int
		want []fixtureWindow
	}{
		{"normal128", func(int) int { return 128 }, []fixtureWindow{{0, 128}, {127, 128}, {254, 67}}},
		{"64", func(int) int { return 64 }, nil},
		{"32", func(int) int { return 32 }, nil},
		{"mixed", func(start int) int {
			if start == 0 {
				return 64
			}
			if start == 63 {
				return 32
			}
			return 128
		}, []fixtureWindow{{0, 128}, {0, 64}, {63, 128}, {63, 64}, {63, 32}, {94, 128}, {221, 100}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0}), ctx: ctx}
			snapshot.fail = func(points []osm.MatcherObservation) error {
				start := int(math.Round((points[0].Longitude*fixtureMetersPerDegree - 10) / 2))
				if len(points) > tc.cap(start) {
					return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
				}
				return nil
			}
			points := fixtureObservations(321)
			result, err := Evaluate(ctx, snapshot, points, fixtureConfig())
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != nil && !reflect.DeepEqual(snapshot.calls, tc.want) {
				t.Fatalf("calls=%v want=%v", snapshot.calls, tc.want)
			}
			if result.Counts.MatchedPoints+result.Counts.AmbiguousPoints != len(points) || result.Counts.UnmatchedPoints+result.Counts.RejectedPoints != 0 {
				t.Fatalf("counts=%+v", result.Counts)
			}
			accepted, totalCandidates, totalMeters := 0, 0, 0.0
			for _, call := range snapshot.calls {
				if call.size <= tc.cap(call.start) {
					accepted++
					totalCandidates += call.size
				}
			}
			if result.Stats.Windows != accepted || result.Stats.Candidates != totalCandidates || result.Stats.GraphNodes != accepted*2 || result.Stats.GraphEdges != accepted {
				t.Fatalf("failed attempts inflated stats: %+v", result.Stats)
			}
			var acceptedStarts []int
			for _, call := range snapshot.calls {
				if call.size <= tc.cap(call.start) {
					acceptedStarts = append(acceptedStarts, call.start)
				}
			}
			for i, evidence := range result.Evidence {
				if evidence.RouteOrder != acceptedStarts[i] {
					t.Fatalf("wrong absolute order: %+v starts=%v", evidence, acceptedStarts)
				}
				if evidence.FirstTraversedAt != points[evidence.RouteOrder].Time {
					t.Fatalf("wrong absolute time/order: %+v", evidence)
				}
				totalMeters += evidence.Clipped.LengthMeters
			}
			if math.Abs(totalMeters-640) > 1e-6 || len(result.Evidence) != accepted {
				t.Fatalf("inflated/missing coverage: meters=%f evidence=%d accepted=%d", totalMeters, len(result.Evidence), accepted)
			}
		})
	}
}

func TestAdaptiveErrorEligibility(t *testing.T) {
	node := &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
	edge := &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"node", node, true}, {"edge", edge, true}, {"wrapped", fmt.Errorf("wrapped: %w", node), true},
		{"joined graph", errors.Join(node, edge), true}, {"nested graph", fmt.Errorf("outer: %w", errors.Join(node, fmt.Errorf("inner: %w", edge))), true},
		{"layer", &coverage.OSMEvaluationLimitError{Field: "graph layer", Limit: 64}, false},
		{"unknown field", &coverage.OSMEvaluationLimitError{Field: "nodes", Limit: 2048}, false},
		{"overflow", &osm.MatcherOverflowError{Operation: "incident edges", Limit: 4096}, false},
		{"bounds", &osm.MatcherBoundsError{Field: "nodes", Limit: 256}, false},
		{"text", errors.New(node.Error()), false}, {"canceled", context.Canceled, false}, {"deadline", context.DeadlineExceeded, false},
		{"network", &net.OpError{Op: "read", Net: "tcp", Err: node}, false},
		{"joined permanent", errors.Join(node, errors.New("permanent")), false},
		{"joined cancel", errors.Join(node, context.Canceled), false}, {"joined timeout", errors.Join(node, context.DeadlineExceeded), false},
		{"nested permanent", fmt.Errorf("outer: %w", errors.Join(node, fmt.Errorf("inner: %w", errors.New("permanent")))), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoverableGraphLimit(tc.err); got != tc.want {
				t.Fatalf("eligible=%v want=%v", got, tc.want)
			}
			snapshot := &windowSnapshot{fail: func([]osm.MatcherObservation) error { return tc.err }}
			result, err := Evaluate(context.Background(), snapshot, fixtureObservations(128), fixtureConfig())
			wantCalls := 1
			if tc.want {
				wantCalls = 3
			}
			if err != tc.err || len(snapshot.calls) != wantCalls || !reflect.DeepEqual(result, Result{}) || snapshot.clipCalls != 0 {
				t.Fatalf("result=%+v err=%v calls=%v clips=%d", result, err, snapshot.calls, snapshot.clipCalls)
			}
		})
	}
}

func TestAdaptiveShortTailAndEmptyInput(t *testing.T) {
	for _, count := range []int{0, 1, 20, 32, 33, 64, 65, 128, 129} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			snapshot := &windowSnapshot{}
			result, err := Evaluate(context.Background(), snapshot, fixtureObservations(count), fixtureConfig())
			if err != nil || result.Counts.UnmatchedPoints != count {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			snapshot = &windowSnapshot{fail: func([]osm.MatcherObservation) error {
				return &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096}
			}}
			result, err = Evaluate(context.Background(), snapshot, fixtureObservations(count), fixtureConfig())
			if count == 0 {
				if err != nil || len(snapshot.calls) != 0 {
					t.Fatal("empty route matched")
				}
				return
			}
			want := 1
			if count > 32 {
				want++
			}
			if count > 64 {
				want++
			}
			if err == nil || len(snapshot.calls) != want || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("duplicate short-tail attempt: calls=%v result=%+v err=%v", snapshot.calls, result, err)
			}
		})
	}
}

func TestAdaptiveFailureDiscardsAcceptedWindows(t *testing.T) {
	for _, cancelAfterFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelAfterFailure), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0}), ctx: ctx}
			snapshot.fail = func(points []osm.MatcherObservation) error {
				if points[0].Longitude*fixtureMetersPerDegree > 200 {
					if cancelAfterFailure {
						cancel()
					}
					return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
				}
				return nil
			}
			result, err := Evaluate(ctx, snapshot, fixtureObservations(300), fixtureConfig())
			wantCalls := 4
			if cancelAfterFailure {
				wantCalls = 2
			}
			if err == nil || !reflect.DeepEqual(result, Result{}) || snapshot.clipCalls != 0 || len(snapshot.calls) != wantCalls {
				t.Fatalf("partial success: %+v err=%v calls=%v clips=%d", result, err, snapshot.calls, snapshot.clipCalls)
			}
			if cancelAfterFailure && !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDefaultLimitsContract(t *testing.T) {
	want := Limits{SampledPoints: 12000, Portions: 10000, Segments: 4096, Generations: 256, UnavailableRegions: 64}
	if DefaultLimits() != want || OriginalPointLimit != 50000 {
		t.Fatalf("limits=%+v original=%d", DefaultLimits(), OriginalPointLimit)
	}
	for _, count := range []int{12000, 12001} {
		snapshot := &windowSnapshot{}
		result, err := Evaluate(context.Background(), snapshot, fixtureObservations(count), fixtureConfig())
		if count == 12000 {
			if err != nil || result.Counts.SampledPoints != count || result.Counts.UnmatchedPoints != count {
				t.Fatalf("12000 rejected: result=%+v err=%v", result, err)
			}
		} else {
			var limit *LimitError
			if !errors.As(err, &limit) || limit.Field != "sampled points" || limit.Limit != 12000 || len(snapshot.calls) != 0 || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("12001 accepted: result=%+v err=%v", result, err)
			}
		}
	}
}

func TestAdaptiveRecoversActualGraphExpansionLimits(t *testing.T) {
	for _, cap := range []int{64, 32} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			points := fixtureObservations(128)
			snapshot := &windowSnapshot{}
			for i := range points {
				x := 10 + float64(i)*1000
				points[i].Longitude = x / fixtureMetersPerDegree
				for j := 0; j < 16; j++ {
					name := fmt.Sprintf("dense/%d/%d", i, j)
					xj := x + float64(j)*.01
					road := fixtureRoad{a: fixturePoint{xj, -500}, b: fixturePoint{xj, 500}, id: fixtureID(name), path: fixtureID(name + " path"), from: fixtureID(name + " from"), to: fixtureID(name + " to")}
					snapshot.roads = append(snapshot.roads, road)
					if cap == 32 {
						for leaf := 0; leaf < 2; leaf++ {
							leafName := fmt.Sprintf("%s leaf %d", name, leaf)
							snapshot.roads = append(snapshot.roads, fixtureRoad{a: road.a, b: fixturePoint{xj, -510 - float64(leaf)*10}, id: fixtureID(leafName), path: road.path, from: road.from, to: fixtureID(leafName + " node")})
						}
					}
				}
			}
			result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
			if err != nil {
				t.Fatal(err)
			}
			wantPrefix := []int{128, 64}
			if cap == 32 {
				wantPrefix = append(wantPrefix, 32)
			}
			for i, size := range wantPrefix {
				if snapshot.calls[i].size != size {
					t.Fatalf("not real adaptive recovery: calls=%v", snapshot.calls)
				}
			}
			if result.Counts.SampledPoints != len(points) || len(result.Evidence) != 0 {
				t.Fatalf("spatially separated seeds gained coverage: %+v", result)
			}
			wantWindows := (len(points)-2)/(cap-1) + 1
			if result.Stats.Windows != wantWindows {
				t.Fatalf("windows=%d want=%d calls=%v", result.Stats.Windows, wantWindows, snapshot.calls)
			}
		})
	}
}

func TestAdaptiveFullSampleBudgetIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot := &windowSnapshot{ctx: ctx, fail: func(points []osm.MatcherObservation) error {
		if len(points) > 32 {
			return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
		}
		return nil
	}}
	count := DefaultLimits().SampledPoints
	result, err := Evaluate(ctx, snapshot, fixtureObservations(count), fixtureConfig())
	windows := (count-2)/31 + 1
	if err != nil || result.Counts.UnmatchedPoints != count || result.Stats.Windows != windows || len(snapshot.calls) > 3*windows {
		t.Fatalf("unbounded/incorrect full-budget recovery: counts=%+v windows=%d calls=%d err=%v", result.Counts, result.Stats.Windows, len(snapshot.calls), err)
	}
}
