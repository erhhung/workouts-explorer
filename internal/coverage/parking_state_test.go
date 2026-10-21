package coverage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func largeParkingStateFixture(padding int) (compiledGraph, []Observation, Result) {
	graph, raw, result := parkingBoundaryCycleGraph()
	segments := make([]DirectedSegment, 0, len(graph.segments)+padding)
	for _, segment := range graph.segments {
		segments = append(segments, segment)
	}
	for i := range padding {
		from := "d"
		if i > 0 {
			from = fmt.Sprintf("tail-node-%d", i-1)
		}
		x := 40 + float64(i*10)
		s := segment(fmt.Sprintf("tail-segment-%d", i), from, fmt.Sprintf("tail-node-%d", i), x, 0, x+10, 0, "public-path-"+strings.Repeat("p", 64), "public-locality")
		s.RegionID, s.GenerationID, s.DerivationVersion = "public-region-"+strings.Repeat("r", 64), 27, 24
		s.LengthMeters, s.ContinuityClass, s.Direction = 10, "path", SegmentForward
		s.SourceWayID, s.SourceWayVersion = int64(i+1), 2
		s.PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(s.ID))
		s.PhysicalID = s.PhysicalSegmentID.String()
		segments = append(segments, s)
	}
	graph = compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	for _, s := range segments[len(segments)-padding:] {
		result.Traversals[0].Portions = append(result.Traversals[0].Portions, graph.portion(s.ID, 0, s.LengthMeters))
	}
	return graph, raw, result
}

func TestParkingLargeTraversalCycleUsesCompactExactStates(t *testing.T) {
	for _, padding := range []int{1000, 2000} {
		t.Run(fmt.Sprintf("portions=%d", padding+3), func(t *testing.T) {
			graph, raw, result := largeParkingStateFixture(padding)
			before := cloneParkingResult(result)
			fullBytes := 16
			for _, p := range before.Traversals[0].Portions {
				fullBytes += 128 + len(p.SegmentID) + len(p.RegionID) + len(p.LogicalPathID) + len(p.LocalityID) + len(p.Direction) + len(p.ContinuityClass)
			}
			if 2*fullBytes <= maxParkingRepairStateBytes {
				t.Fatal("fixture does not reproduce the old duplicated-metadata byte exhaustion")
			}
			if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
				t.Fatal(err)
			}
			got := result.Traversals[0]
			if len(got.Portions) != padding+4 || got.Portions[1].SegmentID != "second" || got.Portions[2].SegmentID != "second-tail" ||
				!reflect.DeepEqual(got.Portions[3:], before.Traversals[0].Portions[2:]) || !reflect.DeepEqual(got.Observations, before.Traversals[0].Observations) {
				t.Fatal("large cycle changed evidence or rejected more than its cycle-closing candidate")
			}
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			states.beginCursor(0)
			for _, portions := range [][]TraversedPortion{before.Traversals[0].Portions, got.Portions} {
				key, err := states.key(graph, portions, 0)
				if err != nil {
					t.Fatal(err)
				}
				assertParkingVarintKey(t, key, 0, portions, &states)
				if repeated, err := states.remember(key); repeated || err != nil {
					t.Fatalf("first two states were not distinct: %v", err)
				}
			}
			key, err := states.key(graph, before.Traversals[0].Portions, 0)
			if err != nil {
				t.Fatal(err)
			}
			if repeated, err := states.remember(key); !repeated || err != nil || states.count != 2 || states.bytes >= 17*1024 {
				t.Fatalf("period-two state was not recognized compactly: repeated=%v bytes=%d err=%v", repeated, states.bytes, err)
			}
			t.Logf("old full key=%d bytes; two compact keys=%d bytes; interned portions=%d charged bytes=%d", fullBytes, states.bytes, len(states.portions), states.internBytes)
		})
	}
}

func TestParkingManyCursorStatesRetainStableInternIDs(t *testing.T) {
	graph, _, result := largeParkingStateFixture(1000)
	portions := result.Traversals[0].Portions
	other := append([]TraversedPortion(nil), portions...)
	other[1].SourceWayVersion++
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	for cursor := range 1086 {
		states.beginCursor(cursor)
		for variant, p := range [][]TraversedPortion{portions, other} {
			if cursor == 1085 && variant == 1 {
				break
			}
			key, err := states.key(graph, p, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if repeated, err := states.remember(key); repeated || err != nil {
				t.Fatalf("advanced cursor or different full traversal falsely repeated: %v", err)
			}
		}
	}
	lastKey, err := states.key(graph, portions, 1085)
	if err != nil {
		t.Fatal(err)
	}
	assertParkingVarintKey(t, lastKey, 1085, portions, &states)
	if states.count != 2171 || states.cursorStates != 1 || states.cursor != 1085 || states.bytes != len(lastKey) || len(states.portions) != len(portions)+1 {
		t.Fatalf("cursor-local payload or traversal-wide dictionary/count drifted: %+v", states.limitError("parking repair state bytes", 0, 0))
	}
	count, bytes := len(states.portions), states.internBytes
	states.beginCursor(2000)
	id, err := states.intern(graph, portions[0], 0)
	if err != nil || id != 1 || len(states.portions) != count || states.internBytes != bytes {
		t.Fatalf("dictionary IDs were reused or dictionary contents were discarded on cursor advance: %d/%v", id, err)
	}
}

func TestParkingInternLimitsAreExplicitAndAccounted(t *testing.T) {
	graph, _, result := parkingBoundaryCycleGraph()
	p := result.Traversals[0].Portions[0]
	if reflect.TypeFor[parkingRepairPortionKey]().Size()+128 > parkingRepairInternEntryBytes {
		t.Fatal("comparable key grew beyond the conservative dictionary-entry accounting allowance")
	}
	for _, field := range []string{"parking repair interned portions", "parking repair intern bytes"} {
		t.Run(field, func(t *testing.T) {
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			id, err := states.intern(graph, p, 20)
			if err != nil || id != 1 {
				t.Fatal(err)
			}
			if field == "parking repair interned portions" {
				states.maxInternCount = 1
			} else {
				states.maxInternBytes = states.internBytes
			}
			if again, err := states.intern(graph, p, 20); again != id || err != nil {
				t.Fatalf("existing exact portion was charged again at the limit: %v", err)
			}
			p.SourceWayVersion++
			_, err = states.intern(graph, p, 20)
			var limit *ParkingRepairLimitError
			var typed *OSMEvaluationLimitError
			if !errors.As(err, &limit) || !errors.As(err, &typed) || limit.Field != field || typed.Field != field ||
				limit.InternedPortions != 1 || limit.ProjectedInternedPortions != 2 || limit.AttemptedInternBytes < parkingRepairInternEntryBytes ||
				limit.ProjectedInternBytes != limit.InternBytes+limit.AttemptedInternBytes || len(states.portions) != 1 {
				t.Fatalf("dictionary limit counters or manual-only typed field are incorrect: %v", err)
			}
		})
	}
}

func TestParkingInternExhaustionRollsBackAndReportsProjection(t *testing.T) {
	graph, raw, result := largeParkingStateFixture(2000)
	region := strings.Repeat("public-region", 1000)
	for i := range result.Traversals[0].Portions {
		result.Traversals[0].Portions[i].RegionID = region
	}
	before := cloneParkingResult(result)
	err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	var limit *ParkingRepairLimitError
	if !errors.As(err, &limit) || limit.Field != "parking repair intern bytes" || limit.InternBytes >= limit.MaxInternBytes ||
		limit.ProjectedInternBytes <= limit.MaxInternBytes || limit.AttemptedInternBytes <= 0 || limit.SuffixNodes == 0 || limit.AttemptedKeyBytes != 0 || !reflect.DeepEqual(result, before) {
		t.Fatalf("dictionary exhaustion did not report its attempted insertion and roll back: %v", err)
	}
}

func TestParkingStateByteErrorExplainsBelowLimitCounters(t *testing.T) {
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	states.beginCursor(7)
	states.count = 2170
	if _, err := states.remember(strings.Repeat("a", 262208)); err != nil {
		t.Fatal(err)
	}
	_, err := states.remember(strings.Repeat("b", 262081))
	var limit *ParkingRepairLimitError
	if !errors.As(err, &limit) || limit.Field != "parking repair state bytes" || limit.States != 2171 || limit.Cursor != 7 || limit.CursorStates != 1 ||
		limit.ProjectedStates != 2172 || limit.StateBytes != 262208 || limit.AttemptedKeyBytes != 262081 || limit.ProjectedStateBytes != 524289 ||
		!strings.Contains(err.Error(), "projected 524289/524288") {
		t.Fatalf("below-limit current counters hide the actual projected-byte exhaustion: %v", err)
	}
}

func TestParkingInterningPreservesNaNPayloadsAndSignedZero(t *testing.T) {
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	p := TraversedPortion{SegmentID: "public", FromMeter: math.Float64frombits(0x7ff8000000000001)}
	first, err := states.key(compiledGraph{}, []TraversedPortion{p}, 0)
	if err != nil {
		t.Fatal(err)
	}
	again, err := states.key(compiledGraph{}, []TraversedPortion{p}, 0)
	if err != nil || first != again || len(states.portions) != 1 {
		t.Fatal("identical NaN bits did not intern reflexively")
	}
	p.FromMeter = math.Float64frombits(0x7ff8000000000002)
	other, err := states.key(compiledGraph{}, []TraversedPortion{p}, 0)
	if err != nil || other == first {
		t.Fatal("distinct NaN payloads collapsed")
	}
	p.FromMeter = 0
	positive, _ := states.key(compiledGraph{}, []TraversedPortion{p}, 0)
	p.FromMeter = math.Copysign(0, -1)
	negative, _ := states.key(compiledGraph{}, []TraversedPortion{p}, 0)
	if positive == negative {
		t.Fatal("signed zero collapsed during interning")
	}
}

func TestParkingCancellationDuringInternLookupIsAtomic(t *testing.T) {
	graph, raw, result := largeParkingStateFixture(1000)
	before := cloneParkingResult(result)
	ctx := newCPUCancelContext(t, ".(*parkingRepairStates).intern", false, 4)
	graph.ctx = ctx
	err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, before) {
		t.Fatalf("intern cancellation returned a partial match: %v", err)
	}
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	graph.ctx = context.Background()
	if _, err := states.key(graph, before.Traversals[0].Portions, 0); err != nil {
		t.Fatal(err)
	}
	count, bytes := len(states.portions), states.internBytes
	ctx = newCPUCancelContext(t, ".(*parkingRepairStates).intern", false, 4)
	graph.ctx = ctx
	_, err = states.key(graph, before.Traversals[0].Portions, 0)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || len(states.portions) != count || states.internBytes != bytes {
		t.Fatalf("cached intern lookup ignored cancellation or mutated its dictionary: %v", err)
	}
}

func TestParkingConcurrentInternDictionariesAreIndependent(t *testing.T) {
	graph, raw, original := largeParkingStateFixture(1000)
	expected := cloneParkingResult(original)
	if err := repairRawSupportedParallelParkingRoutes(&expected, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	segment := graph.segments["tail-segment-0"]
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 3 {
				result := cloneParkingResult(original)
				if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil || !reflect.DeepEqual(result, expected) {
					t.Errorf("local dictionaries changed another evaluation's result: %v", err)
				}
			}
		})
	}
	workers.Wait()
	if graph.segments[segment.ID] != segment || original.Traversals[0].Portions[1].SegmentID != "first" {
		t.Fatal("interning mutated source metadata or traversal portions")
	}
}

func BenchmarkParkingCompactLargeCycle(b *testing.B) {
	graph, raw, original := largeParkingStateFixture(2000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		result := cloneParkingResult(original)
		if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
			b.Fatal(err)
		}
	}
}
