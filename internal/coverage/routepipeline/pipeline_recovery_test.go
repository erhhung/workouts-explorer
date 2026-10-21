package routepipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

func TestEvaluateMixedAdaptiveSeamsThroughPrepare(t *testing.T) {
	roads := fixtureRoads(fixturePoint{0, 0}, fixturePoint{136, 0}, fixturePoint{136, 62}, fixturePoint{390, 62},
		fixturePoint{390, 188}, fixturePoint{452, 188}, fixturePoint{452, 558})
	points := fixtureObservations(501)
	roadStarts := []int{0, 63, 94, 221, 284, 315}
	indexes := make(map[[2]float64]int, len(points))
	for i := range points {
		roadIndex := 0
		for roadIndex+1 < len(roads) && i >= roadStarts[roadIndex+1] {
			roadIndex++
		}
		road := roads[roadIndex]
		length := math.Hypot(road.b.x-road.a.x, road.b.y-road.a.y)
		along := float64(i-roadStarts[roadIndex]) * 2
		if roadIndex == 0 {
			along += 10
		}
		points[i].Longitude = (road.a.x + along*(road.b.x-road.a.x)/length) / fixtureMetersPerDegree
		points[i].Latitude = (road.a.y + along*(road.b.y-road.a.y)/length) / fixtureMetersPerDegree
		indexes[[2]float64{points[i].Longitude, points[i].Latitude}] = i
	}
	var attempts []fixtureWindow
	snapshot := &windowSnapshot{roads: roads}
	snapshot.fail = func(query []osm.MatcherObservation) error {
		start, ok := indexes[[2]float64{query[0].Longitude, query[0].Latitude}]
		if !ok {
			return errors.New("query is not an absolute route slice")
		}
		attempts = append(attempts, fixtureWindow{start, len(query)})
		cap := 128
		switch start {
		case 0, 221:
			cap = 64
		case 63, 284:
			cap = 32
		}
		if len(query) > cap {
			return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
		}
		return nil
	}
	result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	wantAttempts := []fixtureWindow{{0, 128}, {0, 64}, {63, 128}, {63, 64}, {63, 32}, {94, 128}, {221, 128}, {221, 64},
		{284, 128}, {284, 64}, {284, 32}, {315, 128}, {442, 59}}
	if !reflect.DeepEqual(attempts, wantAttempts) {
		t.Fatalf("attempts=%v want=%v", attempts, wantAttempts)
	}
	starts := []int{0, 63, 94, 221, 284, 315, 442}
	if result.Stats.Windows != len(starts) || len(result.Evidence) != len(starts) || result.Counts.UniqueSegments != len(roads) ||
		result.Counts.SampledPoints != len(points) || result.Counts.MatchedPoints+result.Counts.AmbiguousPoints != len(points) ||
		result.Counts.RejectedPoints+result.Counts.UnmatchedPoints != 0 {
		t.Fatalf("lost/duplicated seam evidence: counts=%+v stats=%+v evidence=%+v", result.Counts, result.Stats, result.Evidence)
	}
	if want := ([]osm.MatcherGeneration{{RegionID: "fixture:adaptive", GenerationID: 1}}); !reflect.DeepEqual(result.Generations, want) {
		t.Fatalf("generation provenance=%+v want=%+v", result.Generations, want)
	}
	total := 0.0
	for i, evidence := range result.Evidence {
		roadIndex := min(i, len(roads)-1)
		if evidence.Portion.PhysicalSegmentID != roads[roadIndex].id || evidence.Portion.RegionID != "fixture:adaptive" ||
			evidence.Portion.GenerationID != 1 || evidence.Portion.LogicalPathID != roads[roadIndex].path.String() ||
			evidence.RouteOrder != starts[i] || evidence.FirstTraversedAt != points[starts[i]].Time {
			t.Fatalf("wrong absolute seam provenance: %+v", evidence)
		}
		if i == len(starts)-1 && math.Abs(evidence.Portion.SourceFromFraction-result.Evidence[i-1].Portion.SourceToFraction) > 1e-9 {
			t.Fatal("overlapping windows left a hole or duplicated a positive-length interval")
		}
		total += evidence.Clipped.LengthMeters
	}
	if math.Abs(total-1000) > 1e-6 {
		t.Fatalf("raw evidence inflated/clipped: %f meters", total)
	}
	prepared, err := Prepare(context.Background(), snapshot, result, points[0].Time.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != len(roads) || snapshot.clipCalls != 2 || snapshot.copyCalls != 1 || len(snapshot.copyRefs) != len(roads) ||
		len(snapshot.clipRefs) != len(starts)+len(roads) {
		t.Fatalf("wrong dissolve/copy batches: prepared=%d clips=%v copies=%v", len(prepared), snapshot.clipRefs, snapshot.copyRefs)
	}
	total = 0
	seen := make(map[int]bool)
	for i, item := range prepared {
		roadIndex := -1
		for j, road := range roads {
			if item.Segment.SegmentID == road.id {
				roadIndex = j
				break
			}
		}
		if roadIndex < 0 || seen[roadIndex] {
			t.Fatalf("unknown/duplicate prepared segment: %+v", item)
		}
		seen[roadIndex] = true
		road := roads[roadIndex]
		ref := snapshot.clipRefs[len(starts)+i]
		from := 0.0
		wantStart := road.a
		if roadIndex == 0 {
			from = 10.0 / 136
			wantStart.x = 10
		}
		if ref.SegmentID != road.id || ref.RegionID != "fixture:adaptive" || ref.GenerationID != 1 || ref.Direction != osm.MatcherForward ||
			math.Abs(ref.SourceFromFraction-from) > 1e-9 || math.Abs(ref.SourceToFraction-1) > 1e-9 ||
			item.Segment.RegionID != ref.RegionID || item.Segment.GenerationID != ref.GenerationID || item.Segment.LogicalPathID != road.path ||
			item.FirstRouteOrder != roadStarts[roadIndex] || item.FirstTraversedAt != points[roadStarts[roadIndex]].Time {
			t.Fatalf("wrong dissolved interval/provenance: ref=%+v prepared=%+v", ref, item)
		}
		var geometry struct {
			Type        string        `json:"type"`
			Coordinates [][][]float64 `json:"coordinates"`
		}
		if err := json.Unmarshal(item.CoveredGeoJSON, &geometry); err != nil {
			t.Fatal(err)
		}
		if geometry.Type != "MultiLineString" || len(geometry.Coordinates) != 1 || len(geometry.Coordinates[0]) != 2 {
			t.Fatalf("noncontiguous dissolved geometry: %s", item.CoveredGeoJSON)
		}
		for j, want := range []fixturePoint{wantStart, road.b} {
			got := geometry.Coordinates[0][j]
			if len(got) != 2 || math.Abs(got[0]*fixtureMetersPerDegree-want.x) > 1e-6 || math.Abs(got[1]*fixtureMetersPerDegree-want.y) > 1e-6 {
				t.Fatalf("clipped/doubled seam geometry: %s", item.CoveredGeoJSON)
			}
		}
		length := math.Hypot(road.b.x-wantStart.x, road.b.y-wantStart.y)
		if math.Abs(item.CoveredMeters-length) > 1e-6 {
			t.Fatalf("dissolved meters=%f want=%f", item.CoveredMeters, length)
		}
		total += item.CoveredMeters
	}
	if math.Abs(total-1000) > 1e-6 {
		t.Fatalf("prepared coverage inflated/clipped: %f meters", total)
	}
}

// Hard gaps deliberately retain clipped turn anchors or an actually visited
// tangent. A successful supplemental rematch must not manufacture continuity.
func supplementalFailureRoute(kind string) (*windowSnapshot, []coverage.GeographicObservation) {
	snapshot, points, _ := turnFixture(false, 128)
	if kind == "turn" {
		for i := range points {
			if i < 128 {
				points[i].Longitude = math.Max(-40, float64(i-127)*2-8) / fixtureMetersPerDegree
				points[i].Latitude = 0
			} else {
				points[i].Longitude = 0
				points[i].Latitude = -math.Min(40, float64(i-128)*2+8) / fixtureMetersPerDegree
			}
		}
	} else {
		snapshot, points, _ = tangentFixture(false, 128)
		for i := range points {
			points[i].Longitude, points[i].Latitude = 0, 0
			switch {
			case i < 108:
				points[i].Longitude = math.Min(0, -40+float64(i)*2) / fixtureMetersPerDegree
			case i < 128:
				points[i].Latitude = float64(i-108) * 30 / 19 / fixtureMetersPerDegree
			default:
				points[i].Longitude = math.Min(90, 40+float64(i-128)*2) / fixtureMetersPerDegree
			}
		}
	}
	for i := 128; i < len(points); i++ {
		points[i].Time = points[i].Time.Add(time.Hour)
	}
	return snapshot, points
}

func TestEvaluateSupplementalFailureDiscardsPrimaryEvidence(t *testing.T) {
	for _, kind := range []string{"turn", "tangent"} {
		t.Run(kind, func(t *testing.T) {
			baseline, points := supplementalFailureRoute(kind)
			result, err := Evaluate(context.Background(), baseline, points, fixtureConfig())
			initial := 128
			if kind == "tangent" {
				initial = 192
			}
			if err != nil || len(result.Evidence) < 2 || len(baseline.calls) != 4 || baseline.calls[3].size != initial || result.Stats.Windows != 4 {
				t.Fatalf("fixture did not reach supplemental %s after three accepted primary windows: counts=%+v stats=%+v calls=%v err=%v", kind, result.Counts, result.Stats, baseline.calls, err)
			}
			meters := 0.0
			for _, evidence := range result.Evidence {
				meters += evidence.Clipped.LengthMeters
			}
			wantMeters := 64.0
			if kind == "tangent" {
				wantMeters = 120
			}
			if math.Abs(meters-wantMeters) > 1e-6 {
				t.Fatalf("successful supplemental rematch bridged a hard gap: meters=%f want=%f", meters, wantMeters)
			}
			for _, cancelAttempt := range []int{0, 1, 2} {
				t.Run(fmt.Sprintf("cancelAttempt=%d", cancelAttempt), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					snapshot, points := supplementalFailureRoute(kind)
					snapshot.ctx = ctx
					limit := &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096}
					snapshot.fail = func([]osm.MatcherObservation) error {
						attempt := len(snapshot.calls) - 3
						if attempt <= 0 {
							return nil
						}
						if attempt == cancelAttempt {
							cancel()
						}
						return limit
					}
					failed, err := Evaluate(ctx, snapshot, points, fixtureConfig())
					if !reflect.DeepEqual(failed, Result{}) {
						t.Fatalf("partial result after supplemental failure: %+v", failed)
					}
					wantSizes := []int{128, 64, 32}
					if kind == "tangent" {
						wantSizes = []int{192, 128, 64, 32}
					}
					if cancelAttempt > 0 {
						wantSizes = wantSizes[:cancelAttempt]
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("cancellation error=%v", err)
						}
					} else if err != limit {
						t.Fatalf("exhaustion error=%v want original graph limit", err)
					}
					if len(snapshot.calls) != 3+len(wantSizes) || !reflect.DeepEqual(snapshot.calls[:3], baseline.calls[:3]) {
						t.Fatalf("primary windows retried or supplemental attempts escaped bounds: %v", snapshot.calls)
					}
					for i, size := range wantSizes {
						if snapshot.calls[3+i].size != size {
							t.Fatalf("wrong supplemental fallback: %v", snapshot.calls)
						}
					}
					// Even a caller mistakenly preparing the failed result has nothing
					// to clip or copy, using a fresh context to avoid masking evidence.
					prepared, prepareErr := Prepare(context.Background(), snapshot, failed, time.Time{})
					if prepareErr != nil || prepared != nil || snapshot.clipCalls != 0 || snapshot.copyCalls != 0 || len(snapshot.clipRefs)+len(snapshot.copyRefs) != 0 {
						t.Fatalf("failed evaluation reached downstream work: prepared=%+v err=%v clips=%v copies=%v", prepared, prepareErr, snapshot.clipRefs, snapshot.copyRefs)
					}
				})
			}
		})
	}
}

type boundedEdgeSnapshot struct{ *windowSnapshot }

func (s boundedEdgeSnapshot) IncidentEdges(ctx context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	edges, err := s.windowSnapshot.IncidentEdges(ctx, nodes, limit)
	if err != nil {
		return nil, err
	}
	// Match the production reader's max+1 contract, rather than returning an
	// oversized batch that would manufacture a recoverable graph-edge limit.
	if len(edges) > limit {
		return nil, &osm.MatcherOverflowError{Operation: "incident edges", Limit: limit}
	}
	return edges, nil
}

func TestEvaluateDenseIncidentEdgeOverflowDoesNotRecover(t *testing.T) {
	roads := fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0})
	seed := roads[0]
	for i := 0; i < 4096; i++ {
		name := fmt.Sprintf("dense incident branch %d", i)
		roads = append(roads, fixtureRoad{a: seed.a, b: fixturePoint{0, 10}, id: fixtureID(name), path: seed.path, from: seed.from, to: fixtureID(name + " node")})
	}
	points := fixtureObservations(128)
	for i := range points {
		points[i].Longitude = (500 + 2*float64(i)) / fixtureMetersPerDegree
	}
	snapshot := boundedEdgeSnapshot{&windowSnapshot{roads: roads}}
	result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
	var overflow *osm.MatcherOverflowError
	if !errors.As(err, &overflow) || overflow.Operation != "incident edges" || overflow.Limit != 4095 ||
		len(snapshot.calls) != 1 || snapshot.calls[0].size != 128 || !reflect.DeepEqual(result, Result{}) || snapshot.clipCalls+snapshot.copyCalls != 0 {
		t.Fatalf("production-shaped edge overflow was retried or retained evidence: result=%+v calls=%v err=%v", result, snapshot.calls, err)
	}
}
