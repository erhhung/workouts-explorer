package coverage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// Public synthetic fixture from the independent review: opposite parking
// aliases have different connectivity, so map choice can escape an exact cycle.
func ambiguousParkingEscapeFixture() (compiledGraph, []Observation, Result) {
	g, _, original := parkingBoundaryCycleGraph()
	var segments []DirectedSegment
	for _, s := range g.segments {
		switch s.ID {
		case "first":
			s.LengthMeters = 80
		case "second", "second-tail":
			s.LengthMeters = 40
		}
		segments = append(segments, s)
	}
	for _, id := range []string{"parking-r-good", "parking-r-dead"} {
		s := g.segments["parking"]
		s.ID, s.Direction = id, SegmentReverse
		s.FromNode, s.ToNode, s.From, s.To = s.ToNode, s.FromNode, s.To, s.From
		s.FromNode = "other-b"
		if id == "parking-r-dead" {
			s.ToNode = "dead"
		}
		segments = append(segments, s)
	}
	s := segment("escape", "a", "c", -20, 0, 20, 0, "escape", "local")
	s.PhysicalID, s.Direction, s.ContinuityClass, s.LengthMeters = "escape", SegmentForward, "road", 40
	segments = append(segments, s)
	g = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	raw := syntheticObservations([]Point{{-20, 0}, {-10, 0}, {0, 0}, {10, 0}, {20, 0}, {30, 0}, {0, 0}, {-10, 0}, {-20, 0}, {0, 0}, {20, 0}, {30, 0}}, nil)
	original.Traversals[0].LastObservation = len(raw) - 1
	for i, p := range original.Traversals[0].Portions {
		original.Traversals[0].Portions[i] = g.portion(p.SegmentID, 0, g.lengths[p.SegmentID])
	}
	return g, raw, original
}

// Restrict only the boundary-enumeration choice in the test view. Neither alias
// is reachable by shortest-path expansion (their from-node has no incoming
// edge), so removing the unchosen alias models a permitted map iteration exactly.
// No production chooser hook, graph mutation, sorting, or route preference.
func parkingAliasChoiceView(t *testing.T, graph compiledGraph, alias string) compiledGraph {
	t.Helper()
	for _, s := range graph.segments {
		if s.ToNode == "other-b" {
			t.Fatal("choice tape would change reachable connectivity")
		}
	}
	segments := make(map[string]DirectedSegment, len(graph.segments))
	for id, s := range graph.segments {
		if (id == "parking-r-good" || id == "parking-r-dead") && id != alias {
			continue
		}
		segments[id] = s
	}
	graph.segments = segments
	return graph
}

func replayParkingAliasTape(t *testing.T, graph compiledGraph, raw []Observation, original Result, aliases []string, maximum int, guarded bool) (Result, [][]TraversedPortion, *parkingRepairStates, error) {
	t.Helper()
	result := cloneParkingResult(original)
	states := newParkingRepairStates(maximum, 0) // Ambiguity must not allocate state keys.
	var trace [][]TraversedPortion
	trace = append(trace, append([]TraversedPortion(nil), result.Traversals[0].Portions...))
	traversal := &result.Traversals[0]
	accepted, stateStart := 0, -1
	for start := 0; start+2 < len(traversal.Portions); start++ {
		if start != stateStart {
			states.beginCursor(start)
			stateStart = start
		}
		before := traversal.Portions[start]
		for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
			after, selected := traversal.Portions[end], traversal.Portions[start+1:end]
			if portionsLength(selected) > 120 || !roadOrParkingBoundary(before, graph) || !roadOrParkingBoundary(after, graph) || portionsContainParking(selected, graph) {
				continue
			}
			view := graph.withoutPhysicalPortions(selected, before.SegmentID, after.SegmentID)
			view = parkingAliasChoiceView(t, view, aliases[min(accepted, len(aliases)-1)])
			alternative, ok := roadParkingRoute(before, after, view, graph, raw, 120, 15, 1e-6)
			if !ok || alternative.distance > portionsLength(selected)+20 || !routeUsesSingleParkingPhysical(before, after, alternative.portions, graph) {
				continue
			}
			if guarded {
				if cycle, err := states.repeatedContinuation(graph, traversal.Portions, start, 1e-6); err != nil || cycle {
					t.Fatalf("ambiguous suffix was certified: %v", err)
				}
			}
			replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
			replacement = append(replacement, alternative.portions...)
			replacement = append(replacement, traversal.Portions[end:]...)
			replacement = graph.mergeConsecutiveTraversedPortions(replacement, 1e-6)
			if guarded {
				cycle, err := states.trackReplacement(graph, traversal.Portions, replacement, start)
				if err != nil {
					return Result{}, trace, &states, err
				}
				if cycle {
					t.Fatal("ambiguous full state was certified")
				}
			} else if accepted >= maximum {
				t.Fatal("bounded old choice-tape oracle did not terminate")
			}
			traversal.Portions = replacement
			accepted++
			trace = append(trace, append([]TraversedPortion(nil), replacement...))
			start = max(-1, start-1)
			break
		}
	}
	return result, trace, &states, nil
}

func TestParkingAmbiguousExactStateRepeatCanEscape(t *testing.T) {
	g, raw, original := ambiguousParkingEscapeFixture()
	if deterministic, err := parkingRepairDirectionsDeterministic(g); deterministic || err != nil {
		t.Fatalf("fixture must be ambiguous: %v", err)
	}
	tape := []string{"parking-r-dead", "parking-r-dead", "parking-r-good"}
	old, oldTrace, _, err := replayParkingAliasTape(t, g, raw, original, tape, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	current, trace, states, err := replayParkingAliasTape(t, g, raw, original, tape, 3, true)
	want := []TraversedPortion{g.portion("parking", 0, 20), g.portion("parking-r-good", 0, 20), g.portion("escape", 0, 40), g.portion("after", 0, 20)}
	if err != nil || !reflect.DeepEqual(current, old) || !reflect.DeepEqual(trace, oldTrace) || len(trace) != 4 || !reflect.DeepEqual(trace[0], trace[2]) || !reflect.DeepEqual(current.Traversals[0].Portions, want) {
		t.Fatalf("A -> B -> A -> escape did not retain the old complete result: %v", err)
	}
	if states.acceptedReplacements != 3 || states.count != 0 || states.bytes != 0 || states.internBytes != 0 || len(states.seen) != 0 || len(states.continuations) != 0 {
		t.Fatal("ambiguous work was incorrectly deduplicated or cached")
	}
	// Exercise the real repair loop as well; its unconstrained alias choices may
	// escape immediately or after repeats, but its only terminal state is escape.
	for range 16 {
		result := cloneParkingResult(original)
		if err := repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, g, 120, 20, 15, 1e-6, maxParkingRepairStates, 0); err != nil || !reflect.DeepEqual(result, old) {
			t.Fatalf("production ambiguous loop returned a non-escape result: %v", err)
		}
	}
}

func TestParkingAmbiguousRepeatedWorkIsBoundedAndAtomic(t *testing.T) {
	g, raw, original := ambiguousParkingEscapeFixture()
	var segments []DirectedSegment
	for id, s := range g.segments {
		if id != "escape" {
			segments = append(segments, s)
		}
	}
	g = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	for _, maximum := range []int{0, 1, 2, 4, maxParkingRepairStates} {
		t.Run(fmt.Sprint(maximum), func(t *testing.T) {
			result := cloneParkingResult(original)
			err := repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, g, 120, 20, 15, 1e-6, maximum, 0)
			var limit *ParkingRepairLimitError
			var typed *OSMEvaluationLimitError
			if !errors.As(err, &limit) || !errors.As(err, &typed) || typed.Field != "parking repair accepted replacements" || typed.Limit != maximum ||
				limit.AcceptedReplacements != maximum || limit.ProjectedAcceptedReplacements != maximum+1 || limit.MaxAcceptedReplacements != maximum || limit.CursorAcceptedReplacements != maximum ||
				limit.States != 0 || limit.ProjectedStates != 0 || limit.StateBytes != 0 || limit.InternBytes != 0 || limit.ContinuationDirectionsDeterministic || !limit.ContinuationDirectionsChecked || !reflect.DeepEqual(result, original) {
				t.Fatalf("repeated accepted work escaped its manual-only bound or returned partial success: %v", err)
			}
		})
	}
	result := cloneParkingResult(original)
	ctx := newCPUCancelContext(t, ".(*parkingRepairStates).directionsDeterministic", false, 7)
	g.ctx = ctx
	err := repairRawSupportedParallelParkingRoutes(&result, raw, g, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, original) {
		t.Fatalf("ambiguous work ignored cancellation or committed a partial match: %v", err)
	}
}

func FuzzParkingAmbiguousTerminatingChoiceTape(f *testing.F) {
	f.Add(uint8(2))
	f.Add(uint8(3))
	f.Add(uint8(31))
	f.Fuzz(func(t *testing.T, repeats uint8) {
		g, raw, original := ambiguousParkingEscapeFixture()
		count := int(repeats % 32)
		tape := make([]string, count+1)
		for i := range count {
			tape[i] = "parking-r-dead"
		}
		tape[count] = "parking-r-good"
		old, oldTrace, _, err := replayParkingAliasTape(t, g, raw, original, tape, count+1, false)
		if err != nil {
			t.Fatal(err)
		}
		result, trace, states, err := replayParkingAliasTape(t, g, raw, original, tape, count+1, true)
		if err != nil || !reflect.DeepEqual(result, old) || !reflect.DeepEqual(trace, oldTrace) || states.acceptedReplacements != count+1 {
			t.Fatalf("terminating ambiguous choice tape changed: %v", err)
		}
	})
}
