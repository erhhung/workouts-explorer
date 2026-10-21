package coverage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// Fabricated geometry: a bidirectional bridge joins two three-road corridors
// ending at the same parking entry. Each corridor can be replaced by traversing
// the bridge backwards and taking the other corridor. The boundary retained by
// the splice is outside route-local backtrack cancellation.
func parkingGrowthFixture() (compiledGraph, []Observation, Result) {
	var segments []DirectedSegment
	for _, spec := range []struct {
		id, from, to   string
		x1, y1, x2, y2 float64
	}{
		{"bridge-f", "a", "b", 0, 0, 10, 0},
		{"bridge-r", "b", "a", 10, 0, 0, 0},
		{"left-1", "b", "l1", 10, 0, 10, 10},
		{"left-2", "l1", "l2", 10, 10, 10, 20},
		{"left-3", "l2", "p", 10, 20, 5, 30},
		{"right-1", "a", "r1", 0, 0, 0, 10},
		{"right-2", "r1", "r2", 0, 10, 0, 20},
		{"right-3", "r2", "p", 0, 20, 5, 30},
		{"parking", "p", "end", 5, 30, 5, 50},
	} {
		s := segment(spec.id, spec.from, spec.to, spec.x1, spec.y1, spec.x2, spec.y2, spec.id, "public")
		s.LengthMeters, s.ContinuityClass, s.Direction = distance(s.From, s.To), "road", SegmentForward
		physical := s.ID
		if s.ID == "bridge-f" || s.ID == "bridge-r" {
			physical = "bridge"
		}
		s.PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(physical))
		s.PhysicalID = s.PhysicalSegmentID.String()
		if s.ID == "bridge-r" {
			s.Direction = SegmentReverse
		}
		if s.ID == "parking" {
			s.TransitionOnly, s.ParkingAisleConnector, s.ContinuityClass = true, true, ""
		}
		segments = append(segments, s)
	}
	graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	raw := syntheticObservations([]Point{{0, 0}, {10, 0}, {10, 10}, {10, 20}, {5, 30}, {5, 50}, {5, 30}, {10, 20}, {10, 10}, {10, 0}, {0, 0}, {0, 10}, {0, 20}, {5, 30}, {5, 50}}, nil)
	traversal := DecodedTraversal{FirstObservation: 0, LastObservation: len(raw) - 1, Cost: 27}
	for _, id := range []string{"bridge-f", "left-1", "left-2", "left-3", "parking"} {
		traversal.Portions = append(traversal.Portions, graph.portion(id, 0, graph.lengths[id]))
	}
	for i := range raw {
		candidates := candidatesNear(raw[i:i+1], Graph{Segments: segments})[0]
		best := candidates[0]
		for _, candidate := range candidates {
			if candidate.DistanceMeters < best.DistanceMeters {
				best = candidate
			}
		}
		traversal.Observations = append(traversal.Observations, DecodedObservation{ObservationIndex: i, Status: ObservationMatched, Candidate: &best})
	}
	return graph, raw, Result{Observations: append([]DecodedObservation(nil), traversal.Observations...), Traversals: []DecodedTraversal{traversal}}
}

func TestParkingGrowingBoundarySpliceHasExactContinuationPump(t *testing.T) {
	graph, raw, original := parkingGrowthFixture()
	old := cloneParkingResult(original)
	keys := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	seen := make(map[string]int)
	proved := false
	terminated, accepted := oldParkingRepair(&old, raw, graph, 120, 20, 15, 64, func(start, end int, before, alternative []TraversedPortion) {
		if end-start != 4 || len(alternative) != 4 || len(before) != len(original.Traversals[0].Portions)+start {
			t.Fatalf("public graph does not reproduce the three-to-four, advancing boundary-reversal splice: start=%d end=%d portions=%d alternative=%+v", start, end, len(before), alternative)
		}
		if !samePhysicalSegment(graph.segments[before[start].SegmentID], graph.segments[alternative[0].SegmentID]) || before[start].SegmentID == alternative[0].SegmentID {
			t.Fatal("growth did not prepend the opposite physical boundary")
		}
		if !roadParkingPortionsRawSupported(before[start+1:end], graph, raw, 15) || !roadParkingPortionsRawSupported(alternative, graph, raw, 15) {
			t.Fatal("growth proof must retain the original raw-support policy")
		}
		key, err := keys.key(graph, before[start:], 0)
		if err != nil {
			t.Fatal(err)
		}
		if previous, exists := seen[key]; exists && start == previous+2 {
			proved = true
		}
		seen[key] = start
	})
	if terminated || accepted != 64 || len(old.Traversals[0].Portions) != len(original.Traversals[0].Portions)+64 || !proved {
		t.Fatalf("bounded old execution did not exhibit the exact suffix pump: terminated=%v accepted=%d proved=%v", terminated, accepted, proved)
	}
	result := cloneParkingResult(original)
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	want := append([]TraversedPortion{graph.portion("bridge-f", 0, 10), graph.portion("bridge-r", 0, 10)}, original.Traversals[0].Portions...)
	if !reflect.DeepEqual(result.Traversals[0].Portions, want) || !reflect.DeepEqual(result.Observations, original.Observations) || !reflect.DeepEqual(result.Traversals[0].Observations, original.Traversals[0].Observations) || result.Traversals[0].Cost != original.Traversals[0].Cost {
		t.Fatal("proven pumping candidate was not rejected alone, or observation evidence changed")
	}
}

func TestParkingContinuationAllowsSeparateOriginalOccurrences(t *testing.T) {
	for _, count := range []int{2, 8, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			graph, raw, result := supportedParkingMiddleGraph()
			portion := append([]TraversedPortion(nil), result.Traversals[0].Portions...)
			for range count - 1 {
				result.Traversals[0].Portions = append(result.Traversals[0].Portions, portion...)
			}
			old := cloneParkingResult(result)
			if terminated, accepted := oldParkingRepair(&old, raw, graph, 120, 20, 15, 128); !terminated || accepted != count {
				t.Fatalf("old finite repeated occurrence fixture failed: %v/%d", terminated, accepted)
			}
			if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil || !reflect.DeepEqual(result, old) {
				t.Fatalf("distinct original occurrences were incorrectly treated as a pump: %v", err)
			}
		})
	}
}

func TestParkingContinuationRequiresSealedPrefixAndDeterminism(t *testing.T) {
	graph, _, result := parkingBoundaryCycleGraph()
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	suffix := []TraversedPortion{graph.portion("first", 10, 20), graph.portion("after", 0, 20), graph.portion("parking", 0, 20)}
	sealed := append([]TraversedPortion{graph.portion("parking", 0, 20)}, suffix...)
	unsafePrefix := append([]TraversedPortion{graph.portion("first", 0, 10)}, suffix...)
	if repeated, err := states.repeatedContinuation(graph, sealed, 1, 1e-6); repeated || err != nil {
		t.Fatal(err)
	}
	if repeated, err := states.repeatedContinuation(graph, unsafePrefix, 1, 1e-6); repeated || err != nil {
		t.Fatalf("merge-sensitive prefix produced a false certificate: %v", err)
	}
	states.forgetContinuations()
	if repeated, err := states.repeatedContinuation(graph, sealed, 1, 1e-6); repeated || err != nil {
		t.Fatalf("a skipped old candidate did not reset proof history: %v", err)
	}
	var segments []DirectedSegment
	for _, s := range graph.segments {
		segments = append(segments, s)
	}
	for _, id := range []string{"opposite-a", "opposite-b"} {
		s := graph.segments["first"]
		s.ID, s.Direction = id, SegmentReverse
		s.FromNode, s.ToNode, s.From, s.To = s.ToNode, s.FromNode, s.To, s.From
		segments = append(segments, s)
	}
	ambiguous := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	states = newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	for range 2 {
		if repeated, err := states.repeatedContinuation(ambiguous, result.Traversals[0].Portions, 0, 1e-6); repeated || err != nil {
			t.Fatalf("ambiguous map-order boundary choice was assumed deterministic: %v", err)
		}
	}
	if len(states.continuations) != 0 {
		t.Fatal("ambiguous directions retained a proof certificate")
	}
}

func TestParkingContinuationCachesAreBoundedAndCancellable(t *testing.T) {
	graph, _, result := parkingGrowthFixture()
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	states.maxSuffixNodes = 0
	_, err := states.repeatedContinuation(graph, result.Traversals[0].Portions, 0, 1e-6)
	var limit *ParkingRepairLimitError
	var typed *OSMEvaluationLimitError
	if !errors.As(err, &limit) || !errors.As(err, &typed) || typed.Field != "parking repair suffix nodes" || limit.SuffixNodes != 0 || limit.ProjectedSuffixNodes != 1 {
		t.Fatalf("suffix-cache bound is not explicit/manual-only: %v", err)
	}
	graph, raw, result := largeParkingStateFixture(1000)
	before := cloneParkingResult(result)
	ctx := newCPUCancelContext(t, ".(*parkingRepairStates).repeatedContinuation", true, 2)
	graph.ctx = ctx
	err = repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, before) {
		t.Fatalf("continuation cancellation committed partial coverage: %v", err)
	}
}

func FuzzParkingRepeatedOriginalOccurrences(f *testing.F) {
	f.Add(uint8(2), false, false, true)
	f.Add(uint8(20), true, true, true)
	f.Add(uint8(31), false, true, false)
	f.Fuzz(func(t *testing.T, repetitions uint8, reverse, splitPrefix, variedMetadata bool) {
		graph, raw, result := supportedParkingMiddleGraph()
		if reverse {
			var segments []DirectedSegment
			for _, s := range graph.segments {
				s.From, s.To, s.FromNode, s.ToNode, s.Direction = s.To, s.From, s.ToNode, s.FromNode, SegmentReverse
				segments = append(segments, s)
			}
			graph = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
			p := result.Traversals[0].Portions
			p[0], p[2] = p[2], p[0]
			for i, portion := range p {
				p[i] = graph.portion(portion.SegmentID, portion.FromMeter, portion.ToMeter)
			}
			for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
				raw[i].Point, raw[j].Point = raw[j].Point, raw[i].Point
			}
			for i := range raw {
				var best Candidate
				found := false
				for _, s := range graph.segments {
					candidate := project(raw[i].Point, s)
					if !found || candidate.DistanceMeters < best.DistanceMeters {
						best, found = candidate, true
					}
				}
				result.Traversals[0].Observations[i].Candidate = &best
			}
		}
		base := append([]TraversedPortion(nil), result.Traversals[0].Portions...)
		result.Traversals[0].Portions = nil
		if splitPrefix {
			id := base[2].SegmentID
			length := graph.lengths[id]
			result.Traversals[0].Portions = append(result.Traversals[0].Portions, graph.portion(id, 0, length/2), graph.portion(id, length/2, length))
		}
		for occurrence := range 1 + int(repetitions%32) {
			for _, portion := range base {
				if variedMetadata {
					portion.SourceWayVersion += occurrence
				}
				result.Traversals[0].Portions = append(result.Traversals[0].Portions, portion)
			}
		}
		old := cloneParkingResult(result)
		if terminated, accepted := oldParkingRepair(&old, raw, graph, 120, 20, 15, 256); !terminated {
			t.Fatalf("finite-occurrence oracle did not terminate: %d", accepted)
		}
		if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil || !reflect.DeepEqual(result, old) {
			t.Fatalf("original occurrence/order/metadata or outside merge context was lost: %v", err)
		}
	})
}
