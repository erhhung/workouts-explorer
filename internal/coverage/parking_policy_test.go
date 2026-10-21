package coverage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func cloneParkingResult(result Result) Result {
	result.Traversals = append([]DecodedTraversal(nil), result.Traversals...)
	for i := range result.Traversals {
		result.Traversals[i].Portions = append([]TraversedPortion(nil), result.Traversals[i].Portions...)
		result.Traversals[i].Observations = append([]DecodedObservation(nil), result.Traversals[i].Observations...)
	}
	return result
}

// An independent, bounded copy of the OLD repair policy. Hitting the test-only
// budget means the oracle has no final result, never an attested partial result.
func oldParkingRepair(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters, maximumRoadOffsetMeters float64, budget int, observers ...func(int, int, []TraversedPortion, []TraversedPortion)) (bool, int) {
	accepted := 0
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
				after, selected := traversal.Portions[end], traversal.Portions[start+1:end]
				selectedLength := portionsLength(selected)
				if selectedLength > maximumRouteMeters || !roadOrParkingBoundary(before, graph) || !roadOrParkingBoundary(after, graph) || portionsContainParking(selected, graph) {
					continue
				}
				filtered := materializedParkingFilter(graph, selected, before.SegmentID, after.SegmentID)
				alternative, ok := roadParkingRoute(before, after, filtered, graph, raw, maximumRouteMeters, maximumRoadOffsetMeters, 1e-6)
				if !ok || alternative.distance > selectedLength+maximumExtraMeters || !routeUsesSingleParkingPhysical(before, after, alternative.portions, graph) {
					continue
				}
				if accepted == budget {
					return false, accepted
				}
				for _, observe := range observers {
					observe(start, end, traversal.Portions, alternative.portions)
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, 1e-6)
				accepted++
				start = max(-1, start-1)
				break
			}
		}
	}
	return true, accepted
}

func supportedParkingMiddleGraph() (compiledGraph, []Observation, Result) {
	graph, raw, result := parkingBoundaryCycleGraph()
	var segments []DirectedSegment
	for _, segment := range graph.segments {
		if segment.ID == "parking" {
			segment.TransitionOnly, segment.ParkingAisleConnector, segment.ContinuityClass = false, false, "road"
		}
		if segment.ID == "second" {
			segment.TransitionOnly, segment.ParkingAisleConnector, segment.ContinuityClass = true, true, ""
		}
		segments = append(segments, segment)
	}
	graph = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	for i, portion := range result.Traversals[0].Portions {
		result.Traversals[0].Portions[i] = graph.portion(portion.SegmentID, portion.FromMeter, portion.ToMeter)
	}
	return graph, raw, result
}

func TestParkingOldTerminatingSupportedMiddleIsStillReplaced(t *testing.T) {
	graph, raw, result := supportedParkingMiddleGraph()
	if !roadParkingPortionsRawSupported(result.Traversals[0].Portions[1:2], graph, raw, 15) {
		t.Fatal("positive fixture does not have a raw-supported selected middle")
	}
	oracle := cloneParkingResult(result)
	if terminated, accepted := oldParkingRepair(&oracle, raw, graph, 120, 20, 15, 16); !terminated || accepted != 1 {
		t.Fatalf("old policy positive did not terminate after one change: %v/%d", terminated, accepted)
	}
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil || !reflect.DeepEqual(result, oracle) {
		t.Fatalf("supported-middle decision or full evidence differs from old policy: %v", err)
	}
}

func TestMatchParkingLimitReturnsNoPartialSuccess(t *testing.T) {
	graph, raw, decoded := supportedParkingMiddleGraph()
	var segments []DirectedSegment
	for _, segment := range graph.segments {
		segments = append(segments, segment)
	}
	supplied := make([][]Candidate, len(raw))
	for i, observation := range decoded.Traversals[0].Observations {
		supplied[i] = []Candidate{*observation.Candidate}
	}
	input := Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200}
	result, err := matchContext(context.Background(), raw, supplied, input, ExperimentalRules(), 0, maxParkingRepairStateBytes)
	var limit *ParkingRepairLimitError
	if !errors.As(err, &limit) || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("state exhaustion was not propagated as an error with no partial match: result=%+v err=%v", result, err)
	}
	var deterministic *OSMEvaluationLimitError
	if !errors.As(err, &deterministic) || deterministic.Field != "parking repair states" || deterministic.Limit != 0 {
		t.Fatal("tracking exhaustion must remain a typed manual-only processing limit, not a recoverable graph limit")
	}
	if _, err := MatchContext(context.Background(), raw, supplied, input, ExperimentalRules()); err != nil {
		t.Fatalf("same matching input failed with production tracking bounds: %v", err)
	}
}

func TestParkingRepeatedPhysicalOccurrenceAndLimitRollback(t *testing.T) {
	graph, raw, result := supportedParkingMiddleGraph()
	traversal := &result.Traversals[0]
	traversal.Portions = append(traversal.Portions, traversal.Portions...)
	oracle := cloneParkingResult(result)
	if terminated, accepted := oldParkingRepair(&oracle, raw, graph, 120, 20, 15, 16); !terminated || accepted != 2 {
		t.Fatalf("repeated physical fixture did not terminate after two changes: %v/%d", terminated, accepted)
	}
	before := cloneParkingResult(result)
	err := repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, graph, 120, 20, 15, 1e-6, 3, maxParkingRepairStateBytes)
	var limit *ParkingRepairLimitError
	if !errors.As(err, &limit) || !reflect.DeepEqual(result, before) {
		t.Fatalf("later-position limit did not roll back all changes: %v", err)
	}
	if err := repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, graph, 120, 20, 15, 1e-6, 4, maxParkingRepairStateBytes); err != nil || !reflect.DeepEqual(result, oracle) {
		t.Fatalf("same physical segment at a later occurrence was incorrectly blocked: %v", err)
	}
}

func TestParkingStateBoundsAndCycleAtExactLimit(t *testing.T) {
	graph, raw, original := parkingBoundaryCycleGraph()
	expected := cloneParkingResult(original)
	if err := repairRawSupportedParallelParkingRoutes(&expected, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	statesForBytes := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	initial, err := statesForBytes.key(graph, original.Traversals[0].Portions, 0)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := statesForBytes.key(graph, expected.Traversals[0].Portions, 0)
	if err != nil {
		t.Fatal(err)
	}
	bytesBound := len(initial) + len(proposed)
	for _, maxStates := range []int{0, 1, 2, maxParkingRepairStates} {
		for _, maxBytes := range []int{0, bytesBound - 1, bytesBound, maxParkingRepairStateBytes} {
			t.Run(fmt.Sprintf("states=%d/bytes=%d", maxStates, maxBytes), func(t *testing.T) {
				graph, raw, result := parkingBoundaryCycleGraph()
				before := cloneParkingResult(result)
				err := repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, graph, 120, 20, 15, 1e-6, maxStates, maxBytes)
				var limit *ParkingRepairLimitError
				if maxStates < 2 || maxBytes < bytesBound {
					if !errors.As(err, &limit) || !reflect.DeepEqual(result, before) || limit.MaxStates != maxStates || limit.MaxStateBytes != maxBytes {
						t.Fatalf("tracking bound returned partial success or wrong error: %v", err)
					}
				} else if err != nil || len(result.Traversals[0].Portions) != 4 {
					t.Fatalf("cycle closer must be rejected even when state budget is exactly full: %v", err)
				}
			})
		}
	}
	graph, raw, result := supportedParkingMiddleGraph()
	result.Traversals = append(result.Traversals, cloneParkingResult(result).Traversals...)
	before := cloneParkingResult(result)
	err = repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, graph, 120, 20, 15, 1e-6, 1, maxParkingRepairStateBytes)
	var limit *ParkingRepairLimitError
	if !errors.As(err, &limit) || !reflect.DeepEqual(result, before) {
		t.Fatalf("limit must not commit any traversal: %v", err)
	}
	graph, raw, result = supportedParkingMiddleGraph()
	second := cloneParkingResult(result).Traversals[0]
	second.Portions = append(second.Portions, second.Portions...)
	result.Traversals = append(result.Traversals, second)
	before = cloneParkingResult(result)
	err = repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, graph, 120, 20, 15, 1e-6, 3, maxParkingRepairStateBytes)
	if !errors.As(err, &limit) || !reflect.DeepEqual(result, before) {
		t.Fatalf("a later traversal's limit must roll back earlier traversal repairs: %v", err)
	}
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	for i := range maxParkingRepairStates {
		if _, err := states.remember(fmt.Sprintf("state-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if repeated, err := states.remember("state-0"); !repeated || err != nil {
		t.Fatal("a repeated state must still be recognized at the exact count limit")
	}
	if _, err := states.remember("one-more"); !errors.As(err, &limit) {
		t.Fatal("maximum state count was not enforced")
	}
}

func TestParkingExactByteBoundAndLaterEndAfterCycle(t *testing.T) {
	graph, raw, original := parkingBoundaryCycleGraph()
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	initial, _ := states.key(graph, original.Traversals[0].Portions, 0)
	expected := cloneParkingResult(original)
	if err := repairRawSupportedParallelParkingRoutes(&expected, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	proposed, _ := states.key(graph, expected.Traversals[0].Portions, 0)
	for _, byteLimit := range []int{len(initial) + len(proposed) - 1, len(initial) + len(proposed)} {
		result := cloneParkingResult(original)
		err := repairRawSupportedParallelParkingRoutesWithLimits(&result, raw, graph, 120, 20, 15, 1e-6, 2, byteLimit)
		if byteLimit == len(initial)+len(proposed) {
			if err != nil || !reflect.DeepEqual(result, expected) {
				t.Fatalf("exact byte limit rejected an already-seen cycle closer: %v", err)
			}
		} else {
			var limit *ParkingRepairLimitError
			if !errors.As(err, &limit) || !reflect.DeepEqual(result, original) {
				t.Fatalf("byte exhaustion did not fail atomically: %v", err)
			}
		}
	}
	var segments []DirectedSegment
	for _, segment := range graph.segments {
		segments = append(segments, segment)
	}
	for _, spec := range []struct {
		id, from, to string
		x1, x2       float64
	}{
		{"detour", "c", "d", 20, 40}, {"tail", "d", "e", 40, 60},
	} {
		s := segment(spec.id, spec.from, spec.to, spec.x1, 0, spec.x2, 0, spec.id, "local")
		s.PhysicalID, s.LengthMeters, s.Direction, s.ContinuityClass = s.ID, 20, SegmentForward, "road"
		segments = append(segments, s)
	}
	graph = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	raw = append(raw, Observation{Point: Point{50, 0}})
	result := cloneParkingResult(original)
	result.Traversals[0].LastObservation = len(raw) - 1
	result.Traversals[0].Portions = append(result.Traversals[0].Portions, graph.portion("tail", 0, 20))
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, portion := range result.Traversals[0].Portions {
		found = found || portion.SegmentID == "detour"
	}
	if !found {
		t.Fatal("rejecting the cycle-closing end suppressed the later non-cyclic end")
	}
}

func TestParkingStateKeyIncludesEveryFieldAndCursor(t *testing.T) {
	graph, _, result := parkingBoundaryCycleGraph()
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	portions := result.Traversals[0].Portions
	initial, err := states.key(graph, portions, 0)
	if err != nil {
		t.Fatal(err)
	}
	otherCursor, err := states.key(graph, portions, 1)
	if err != nil || otherCursor == initial {
		t.Fatal("cursor is missing from the full state")
	}
	for field := range reflect.TypeFor[TraversedPortion]().NumField() {
		changed := append([]TraversedPortion(nil), portions...)
		value := reflect.ValueOf(&changed[1]).Elem().Field(field)
		switch value.Kind() {
		case reflect.String:
			value.SetString(value.String() + "\x00different")
		case reflect.Int, reflect.Int64:
			value.SetInt(value.Int() + 1)
		case reflect.Float64:
			value.SetFloat(math.Nextafter(value.Float(), math.Inf(1)))
		case reflect.Array:
			value.Index(0).SetUint(value.Index(0).Uint() ^ 1)
		default:
			t.Fatalf("unhandled portion field %s", reflect.TypeFor[TraversedPortion]().Field(field).Name)
		}
		key, err := states.key(graph, changed, 0)
		if err != nil || key == initial {
			t.Fatalf("portion field %s is missing from state identity: %v", reflect.TypeFor[TraversedPortion]().Field(field).Name, err)
		}
	}
	changed := append([]TraversedPortion(nil), portions...)
	changed[1].FromMeter = math.Copysign(0, -1)
	negativeZero, err := states.key(graph, changed, 0)
	if err != nil || initial == negativeZero {
		t.Fatal("state identity lost exact float bits")
	}
	// Length-prefixing must not confuse embedded delimiters between string fields.
	changed[1].SegmentID, changed[1].RegionID = "a\x00b", "c"
	a, _ := states.key(graph, changed, 0)
	changed[1].SegmentID, changed[1].RegionID = "a", "b\x00c"
	b, _ := states.key(graph, changed, 0)
	if a == b {
		t.Fatal("ambiguous string field encoding")
	}
}

func TestParkingStateSignatureCancellationIsAtomic(t *testing.T) {
	graph, raw, result := supportedParkingMiddleGraph()
	result.Traversals[0].Portions = append(result.Traversals[0].Portions, make([]TraversedPortion, 256)...)
	before := cloneParkingResult(result)
	ctx := newCPUCancelContext(t, ".(*parkingRepairStates).key", true, 1)
	graph.ctx = ctx
	err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, before) {
		t.Fatalf("signature cancellation returned partial success: %v", err)
	}
}

func checkParkingPolicyOracle(t *testing.T, data []byte) (bool, int) {
	t.Helper()
	if len(data) == 0 {
		return false, 0
	}
	value := func(i int) byte { return data[i%len(data)] }
	graph, raw, result := supportedParkingMiddleGraph()
	var segments []DirectedSegment
	for _, segment := range graph.segments {
		if segment.ID == "second" && value(0)&1 != 0 {
			segment.TransitionOnly, segment.ParkingAisleConnector, segment.ContinuityClass = false, false, "road"
		}
		if segment.ID == "parking" && value(1)&1 != 0 {
			segment.TransitionOnly, segment.ParkingAisleConnector, segment.ContinuityClass = true, true, ""
		}
		segment.RegionID, segment.GenerationID, segment.SourceWayID, segment.SourceWayVersion = "public", int64(value(2)), int64(value(3)), int(value(4))
		segment.PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(segment.ID))
		segment.PhysicalID = segment.PhysicalSegmentID.String()
		if value(5)&1 != 0 {
			segment.Direction, segment.From, segment.To, segment.FromNode, segment.ToNode = SegmentReverse, segment.To, segment.From, segment.ToNode, segment.FromNode
		}
		segments = append(segments, segment)
	}
	graph = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	for i, portion := range result.Traversals[0].Portions {
		result.Traversals[0].Portions[i] = graph.portion(portion.SegmentID, portion.FromMeter, portion.ToMeter)
	}
	if value(5)&1 != 0 {
		p := result.Traversals[0].Portions
		p[0], p[2] = p[2], p[0]
		for i, j := 0, len(raw)-1; i < j; i, j = i+1, j-1 {
			raw[i].Point, raw[j].Point = raw[j].Point, raw[i].Point
		}
	}
	for i := range raw {
		raw[i].Point.Y += float64(int(value(6)%41) - 20)
		raw[i].AccuracyMeters = float64(value(7) % 8)
	}
	for range int(value(8) % 9) {
		result.Traversals[0].Portions = append(result.Traversals[0].Portions, result.Traversals[0].Portions[:3]...)
	}
	maximumRoute, maximumExtra := float64(15+value(9)%106), float64(value(10)%31)
	oracle := cloneParkingResult(result)
	terminated, accepted := oldParkingRepair(&oracle, raw, graph, maximumRoute, maximumExtra, 15, 64)
	if !terminated {
		return false, accepted
	}
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, maximumRoute, maximumExtra, 15, 1e-6); err != nil || !reflect.DeepEqual(result, oracle) {
		t.Fatalf("terminating old policy differs for public input %x: %v", data, err)
	}
	return true, accepted
}

func TestParkingTerminatingPolicyProperty(t *testing.T) {
	terminated, changed := 0, 0
	for seed := range 256 {
		data := []byte{byte(seed), byte(seed >> 1), 27, byte(seed * 3), 24, byte(seed >> 2), byte(seed % 41), byte(seed % 8), byte(seed % 3), byte(seed % 106), byte(seed % 31)}
		if done, accepted := checkParkingPolicyOracle(t, data); done {
			terminated++
			if accepted > 0 {
				changed++
			}
		}
	}
	if terminated < 100 || changed < 10 {
		t.Fatalf("property corpus insufficient: terminating=%d changed=%d", terminated, changed)
	}
	t.Logf("old-oracle parity: terminating=%d changed=%d nonterminating-or-budget-exhausted=%d", terminated, changed, 256-terminated)
}

func FuzzParkingTerminatingPolicy(f *testing.F) {
	f.Add([]byte{0, 0, 27, 1, 24, 0, 20, 5, 0, 100, 20})
	f.Add([]byte{1, 1, 27, 1, 24, 0, 20, 5, 1, 100, 20})
	f.Fuzz(func(t *testing.T, data []byte) { checkParkingPolicyOracle(t, data) })
}
