package coverage

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// This graph uses real geometry and projections, including raw support for both
// parallel roads. A parking boundary satisfies routeUsesSingleParkingPhysical
// even when neither the selected middle nor its replacement contains parking.
func parkingBoundaryCycleGraph() (compiledGraph, []Observation, Result) {
	parking := segment("parking", "a", "b", -20, 0, 0, 0, "parking", "local")
	parking.TransitionOnly, parking.ParkingAisleConnector = true, true
	first := segment("first", "b", "c", 0, 0, 20, 0, "first", "local")
	second := segment("second", "b", "branch", 0, 0, 10, 1, "second", "local")
	secondTail := segment("second-tail", "branch", "c", 10, 1, 20, 0, "second", "local")
	after := segment("after", "c", "d", 20, 0, 40, 0, "after", "local")
	segments := []DirectedSegment{parking, first, second, secondTail, after}
	for i := range segments {
		segments[i].PhysicalID = segments[i].ID
		segments[i].LengthMeters = distance(segments[i].From, segments[i].To)
		segments[i].Direction = SegmentForward
		if !segments[i].TransitionOnly {
			segments[i].ContinuityClass = "road"
		}
	}
	graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	raw := syntheticObservations([]Point{{-20, 0}, {-10, 0}, {0, 0}, {10, 0}, {20, 0}, {30, 0}}, nil)
	portions := []TraversedPortion{graph.portion("parking", 0, 20), graph.portion("first", 0, 20), graph.portion("after", 0, 20)}
	decoded := make([]DecodedObservation, len(raw))
	for i := range raw {
		id := "first"
		if i < 2 {
			id = "parking"
		} else if i > 3 {
			id = "after"
		}
		candidate := project(raw[i].Point, graph.segments[id])
		decoded[i] = DecodedObservation{ObservationIndex: i, Status: ObservationMatched, Candidate: &candidate}
	}
	return graph, raw, Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: len(raw) - 1, Observations: decoded, Portions: portions}}}
}

func TestParallelParkingBoundaryCycleReproduction(t *testing.T) {
	graph, raw, result := parkingBoundaryCycleGraph()
	want := result.Traversals[0]
	portions := append([]TraversedPortion(nil), want.Portions...)
	for _, expected := range [][]string{{"second", "second-tail"}, {"first"}} {
		before, selected, after := portions[0], portions[1:len(portions)-1], portions[len(portions)-1]
		filtered := materializedParkingFilter(graph, selected, before.SegmentID, after.SegmentID)
		alternative, ok := roadParkingRoute(before, after, filtered, graph, raw, 120, 15, 1e-6)
		var gotIDs []string
		for _, portion := range alternative.portions {
			gotIDs = append(gotIDs, portion.SegmentID)
		}
		if !ok || alternative.distance > portionsLength(selected)+20 || !routeUsesSingleParkingPhysical(before, after, alternative.portions, graph) ||
			!reflect.DeepEqual(gotIDs, expected) {
			t.Fatalf("original acceptance predicates did not reproduce cycle: %+v, ok=%v", alternative, ok)
		}
		portions = graph.mergeConsecutiveTraversedPortions(append(append([]TraversedPortion{before}, alternative.portions...), after), 1e-6)
	}
	if !reflect.DeepEqual(portions, want.Portions) {
		t.Fatal("two original accepted replacements did not return to the initial state")
	}
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	expected := []TraversedPortion{want.Portions[0], graph.portion("second", 0, graph.lengths["second"]), graph.portion("second-tail", 0, graph.lengths["second-tail"]), want.Portions[2]}
	if !reflect.DeepEqual(result.Traversals[0].Portions, expected) || !reflect.DeepEqual(result.Traversals[0].Observations, want.Observations) {
		t.Fatal("cycle-closing candidate was not rejected, or observation evidence changed")
	}
}

func parkingRepairWorkload(edges, portions int) (compiledGraph, []Observation, Result) {
	segments := make([]DirectedSegment, 0, edges)
	raw := make([]Observation, portions+1)
	for i := range edges {
		x, y := float64(i*10), 25.0
		from, to := fmt.Sprintf("n-%04d", i), fmt.Sprintf("n-%04d", i+1)
		if i >= portions {
			x, y = float64(i*10), 1000
			from, to = fmt.Sprintf("unused-%04d", i), fmt.Sprintf("unused-%04d", i+1)
		}
		s := segment(fmt.Sprintf("road-%04d", i), from, to, x, y, x+10, y, "road", "local")
		s.LengthMeters, s.PhysicalID, s.ContinuityClass, s.Direction = 10, s.ID, "road", SegmentForward
		segments = append(segments, s)
	}
	graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	traversal := DecodedTraversal{FirstObservation: 0, LastObservation: portions}
	for i := range raw {
		raw[i] = Observation{Point: Point{float64(i * 10), 0}}
		candidate := project(raw[i].Point, segments[min(i, portions-1)])
		traversal.Observations = append(traversal.Observations, DecodedObservation{ObservationIndex: i, Status: ObservationMatched, Candidate: &candidate})
		if i < portions {
			traversal.Portions = append(traversal.Portions, graph.portion(segments[i].ID, 0, 10))
		}
	}
	return graph, raw, Result{Traversals: []DecodedTraversal{traversal}}
}

func TestParallelParkingHeavyWorkloadPreservesEvidence(t *testing.T) {
	graph, raw, result := parkingRepairWorkload(2000, 96)
	want := result.Traversals[0]
	repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	if !reflect.DeepEqual(result.Traversals[0], want) {
		t.Fatal("unsuccessful alternatives changed portions or projected observation evidence")
	}
	// The original recompilation allocated over a million objects per call on
	// this fixture. Leave ample headroom without a wall-clock timing assertion.
	if allocations := testing.AllocsPerRun(2, func() {
		repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	}); allocations > 10000 {
		t.Fatalf("graph-sized allocation regression: %.0f allocations", allocations)
	}
}

func BenchmarkParallelParkingRepair(b *testing.B) {
	graph, raw, result := parkingRepairWorkload(2000, 96)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	}
}

func TestParkingRepairWorkloadContext(t *testing.T) {
	graph, raw, result := parkingRepairWorkload(2000, 96)
	ctx, cancel := context.WithCancel(context.Background())
	graph.ctx = ctx
	cancel()
	want := result.Traversals[0]
	repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	if !reflect.DeepEqual(result.Traversals[0], want) {
		t.Fatal("already-canceled repair changed evidence")
	}
}

func TestParallelParkingRepairsMakeFiniteProgress(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			const blocks = 8
			var segments []DirectedSegment
			var points []Point
			var selectedIDs, expectedIDs []string
			for block := range blocks {
				x := float64(block * 200)
				prefix := fmt.Sprintf("block-%d-", block)
				for _, spec := range []struct {
					id, from, to   string
					x1, y1, x2, y2 float64
				}{
					{"before", "a", "b", -20, 0, 0, 0},
					{"wrong-1", "b", "x", 0, 0, 0, 40},
					{"wrong-2", "x", "c", 0, 40, 50, 0},
					{"alley", "b", "d", 0, 0, 10, 0},
					{"parking", "d", "c", 10, 0, 50, 0},
					{"after", "c", "e", 50, 0, 70, 0},
				} {
					s := segment(prefix+spec.id, prefix+spec.from, prefix+spec.to, x+spec.x1, spec.y1, x+spec.x2, spec.y2, spec.id, "local")
					s.LengthMeters, s.ContinuityClass, s.Direction = distance(s.From, s.To), "road", SegmentForward
					s.PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(s.ID))
					s.PhysicalID = s.PhysicalSegmentID.String()
					s.RegionID, s.GenerationID, s.SourceWayID, s.SourceWayVersion = "region", 27, int64(block+1), 2
					if spec.id == "parking" {
						s.ContinuityClass, s.TransitionOnly, s.ParkingAisleConnector = "", true, true
					}
					if reverse {
						s.From, s.To, s.FromNode, s.ToNode, s.Direction = s.To, s.From, s.ToNode, s.FromNode, SegmentReverse
					}
					segments = append(segments, s)
				}
				for _, id := range []string{"before", "wrong-1", "wrong-2", "after"} {
					selectedIDs = append(selectedIDs, prefix+id)
				}
				for _, id := range []string{"before", "alley", "parking", "after"} {
					expectedIDs = append(expectedIDs, prefix+id)
				}
				for _, offset := range []float64{-20, -10, 0, 10, 30, 50, 60, 70} {
					points = append(points, Point{x + offset, 0})
				}
			}
			if reverse {
				for i, j := 0, len(selectedIDs)-1; i < j; i, j = i+1, j-1 {
					selectedIDs[i], selectedIDs[j] = selectedIDs[j], selectedIDs[i]
					expectedIDs[i], expectedIDs[j] = expectedIDs[j], expectedIDs[i]
				}
				for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
					points[i], points[j] = points[j], points[i]
				}
			}
			graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
			raw := syntheticObservations(points, nil)
			traversal := DecodedTraversal{FirstObservation: 0, LastObservation: len(raw) - 1, Cost: 42}
			for _, id := range selectedIDs {
				traversal.Portions = append(traversal.Portions, graph.portion(id, 0, graph.lengths[id]))
			}
			for i := range raw {
				candidates := candidatesNear(raw[i:i+1], Graph{Segments: segments})[0]
				if len(candidates) == 0 {
					t.Fatal("synthetic raw observation has no real projection")
				}
				traversal.Observations = append(traversal.Observations, DecodedObservation{ObservationIndex: i, Status: ObservationMatched, Candidate: &candidates[0]})
			}
			result := Result{Traversals: []DecodedTraversal{traversal}}
			oracle := cloneParkingResult(result)
			terminated, replacements := oldParkingRepair(&oracle, raw, graph, 120, 20, 15, 128)
			if !terminated || replacements != blocks {
				t.Fatalf("old oracle did not finish the expected repairs: terminated=%v replacements=%d", terminated, replacements)
			}
			if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result, oracle) {
				t.Fatal("new repair differs from terminating old policy")
			}
			var expected []TraversedPortion
			for _, id := range expectedIDs {
				expected = append(expected, graph.portion(id, 0, graph.lengths[id]))
			}
			got := result.Traversals[0]
			if !reflect.DeepEqual(got.Portions, expected) || !reflect.DeepEqual(got.Observations, traversal.Observations) || got.Cost != traversal.Cost ||
				got.FirstObservation != traversal.FirstObservation || got.LastObservation != traversal.LastObservation {
				t.Fatalf("repair changed expected direction, metadata or observation evidence: %+v", got)
			}
			if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Traversals[0], got) {
				t.Fatal("repair did not reach a stable supported result")
			}
		})
	}
}

func TestParallelParkingRepairStopsDuringRawSupport(t *testing.T) {
	graph, raw, result := parkingBoundaryCycleGraph()
	raw = make([]Observation, 2048)
	for i := range raw {
		raw[i].Point = Point{1000, 1000}
	}
	result.Traversals[0].LastObservation = len(raw) - 1
	want := result.Traversals[0]
	ctx := newCPUCancelContext(t, ".compiledGraph.rawPolylineSupportsDirectedSegmentWithin", true, 2)
	graph.ctx = ctx
	repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if !reflect.DeepEqual(result.Traversals[0], want) {
		t.Fatal("canceled support check accepted a replacement")
	}
}

func TestMatchContextStopsDuringParkingRepair(t *testing.T) {
	graph := Graph{Segments: []DirectedSegment{segment("road", "a", "b", 0, 0, 100, 0, "road", "local")}}
	var raw []Observation
	var candidates [][]Candidate
	for i := range 256 {
		for j, x := range []float64{20, 40} {
			raw = append(raw, Observation{Point: Point{x, 0}, Time: time.Unix(int64(i*120+j*10), 0)})
			candidate := project(raw[len(raw)-1].Point, graph.Segments[0])
			candidates = append(candidates, []Candidate{candidate})
		}
	}
	ctx := newCPUCancelContext(t, ".repairRawSupportedParallelParkingRoutesWithLimits", true, 2)
	result, err := MatchContext(ctx, raw, candidates, graph, ExperimentalRules())
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("parking repair returned partial success: result=%+v err=%v", result, err)
	}
}

func TestParkingCrossBoundaryEvidenceAndHardGaps(t *testing.T) {
	for _, test := range []struct {
		name       string
		reason     SplitReason
		reverseRaw bool
		wantBridge bool
	}{
		{"soft-network-gap", SplitNetwork, false, true},
		{"temporal-gap", SplitTemporal, false, false},
		{"spatial-gap", SplitSpatial, false, false},
		{"wrong-raw-direction", SplitNetwork, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
			aisle := segment("aisle", "b", "c", 10, 0, 90, 0, "aisle", "local")
			second := segment("second", "c", "d", 90, 0, 100, 0, "second", "local")
			segments := []DirectedSegment{first, aisle, second}
			for i := range segments {
				segments[i].LengthMeters, segments[i].Direction, segments[i].ContinuityClass = distance(segments[i].From, segments[i].To), SegmentForward, "road"
				segments[i].PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(segments[i].ID))
				segments[i].PhysicalID = segments[i].PhysicalSegmentID.String()
			}
			segments[1].TransitionOnly, segments[1].ParkingAisleConnector, segments[1].ContinuityClass = true, true, ""
			graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
			raw := syntheticObservations([]Point{{10, 0}, {30, 0}, {60, 0}, {85, 0}, {90, 0}}, nil)
			if test.reverseRaw {
				for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
					raw[i].Point, raw[j].Point = raw[j].Point, raw[i].Point
				}
			}
			var decoded []DecodedObservation
			for i, id := range []string{"first", "second"} {
				index := i * (len(raw) - 1)
				candidate := project(raw[index].Point, graph.segments[id])
				decoded = append(decoded, DecodedObservation{ObservationIndex: index, Status: ObservationMatched, Candidate: &candidate})
			}
			result := Result{Observations: decoded, Splits: []Split{{BeforeObservation: 4, Reason: test.reason}}, Traversals: []DecodedTraversal{
				{FirstObservation: 0, LastObservation: 0, Observations: decoded[:1], Portions: []TraversedPortion{graph.portion("first", 0, 10)}},
				{FirstObservation: 4, LastObservation: 4, Observations: decoded[1:], Portions: []TraversedPortion{graph.portion("second", 0, 10)}},
			}}
			before := Result{Observations: append([]DecodedObservation(nil), decoded...), Splits: append([]Split(nil), result.Splits...), Traversals: append([]DecodedTraversal(nil), result.Traversals...)}
			bridgePlausibleParkingAisleTraversals(&result, raw, graph, 200, 64, 1e-6)
			if !test.wantBridge {
				if !reflect.DeepEqual(result, before) {
					t.Fatal("hard gap or unsupported direction changed traversal evidence")
				}
				return
			}
			if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1] != graph.portion("aisle", 0, 80) ||
				!reflect.DeepEqual(result.Traversals[0].Observations, decoded) || !reflect.DeepEqual(result.Observations, before.Observations) || !reflect.DeepEqual(result.Splits, before.Splits) {
				t.Fatal("soft parking bridge changed direction, metadata, raw evidence or split diagnostics")
			}
		})
	}
}
