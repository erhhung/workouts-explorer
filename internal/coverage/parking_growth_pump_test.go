package coverage

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// Two outgoing corridors reach opposite ends of a bidirectional bridge. With
// parking before the corridors, each old rewrite pushes a bridge at the SAME
// cursor, rather than advancing to an identical unread suffix.
func parkingSameCursorGrowthFixture() (compiledGraph, []Observation, Result) {
	var segments []DirectedSegment
	add := func(id, from, to string, x1, y1, x2, y2 float64) {
		s := segment(id, from, to, x1, y1, x2, y2, id, "public")
		s.LengthMeters, s.ContinuityClass, s.Direction = distance(s.From, s.To), "road", SegmentForward
		physical := id
		if id == "bridge-f" || id == "bridge-r" {
			physical = "bridge"
		}
		s.PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(physical))
		s.PhysicalID = s.PhysicalSegmentID.String()
		if id == "bridge-r" {
			s.Direction = SegmentReverse
		}
		if id == "parking" {
			s.TransitionOnly, s.ParkingAisleConnector, s.ContinuityClass = true, true, ""
		}
		segments = append(segments, s)
	}
	add("prefix-1", "prefix-a", "prefix-b", -30, -10, -20, -10)
	add("prefix-2", "prefix-b", "prefix-c", -20, -10, -10, -10)
	add("prefix-3", "prefix-c", "entry", -10, -10, 0, -10)
	add("parking", "entry", "exit", 0, -10, 0, 0)
	add("left-1", "exit", "left-a", 0, 0, 0, 10)
	add("left-2", "left-a", "left-b", 0, 10, 0, 20)
	add("left-3", "left-b", "bridge-a", 0, 20, 0, 30)
	add("right-1", "exit", "right-a", 0, 0, 10, 10)
	add("right-2", "right-a", "right-b", 10, 10, 10, 20)
	add("right-3", "right-b", "bridge-b", 10, 20, 10, 30)
	add("bridge-f", "bridge-a", "bridge-b", 0, 30, 10, 30)
	add("bridge-r", "bridge-b", "bridge-a", 10, 30, 0, 30)
	for i := range 9 {
		from := "bridge-b"
		if i > 0 {
			from = fmt.Sprintf("tail-node-%d", i-1)
		}
		add(fmt.Sprintf("tail-%d", i), from, fmt.Sprintf("tail-node-%d", i), float64(10+i*10), 30, float64(20+i*10), 30)
	}
	graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	points := []Point{{-30, -10}, {-20, -10}, {-10, -10}, {0, -10}, {0, 0}, {0, 10}, {0, 20}, {0, 30}, {10, 30}, {0, 30}, {0, 0}, {10, 10}, {10, 20}, {10, 30}}
	for i := range 9 {
		points = append(points, Point{float64(20 + i*10), 30})
	}
	raw := syntheticObservations(points, nil)
	traversal := DecodedTraversal{FirstObservation: 0, LastObservation: len(raw) - 1, Cost: 27}
	ids := []string{"prefix-1", "prefix-2", "prefix-3", "parking", "left-1", "left-2", "left-3", "bridge-f"}
	for i := range 9 {
		ids = append(ids, fmt.Sprintf("tail-%d", i))
	}
	for _, id := range ids {
		traversal.Portions = append(traversal.Portions, graph.portion(id, 0, graph.lengths[id]))
	}
	for i := range raw {
		candidate := candidatesNear(raw[i:i+1], Graph{Segments: segments})[0][0]
		traversal.Observations = append(traversal.Observations, DecodedObservation{ObservationIndex: i, Status: ObservationMatched, Candidate: &candidate})
	}
	return graph, raw, Result{Observations: append([]DecodedObservation(nil), traversal.Observations...), Traversals: []DecodedTraversal{traversal}}
}

func TestParkingSameCursorPeriodTwoGrowthTerminates(t *testing.T) {
	graph, raw, original := parkingSameCursorGrowthFixture()
	old := cloneParkingResult(original)
	var firstAccepted []TraversedPortion
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	states.beginCursor(3)
	steps := 0
	terminated, accepted := oldParkingRepair(&old, raw, graph, 120, 20, 15, 64, func(start, end int, before, alternative []TraversedPortion) {
		if start != 3 || end != 7 || len(alternative) != 4 || len(before) != 17+steps {
			t.Fatalf("not the same-cursor three-to-four pump: step=%d start=%d end=%d current=%d alternative=%d", steps, start, end, len(before), len(alternative))
		}
		if !roadParkingPortionsRawSupported(before[start+1:end], graph, raw, 15) || !roadParkingPortionsRawSupported(alternative, graph, raw, 15) {
			t.Fatal("corridors or bridge do not have actual raw support")
		}
		if steps == 1 {
			firstAccepted = append([]TraversedPortion(nil), before...)
		}
		if steps < 2 {
			if pump, err := states.repeatedContinuation(graph, before, start, 1e-6); pump || err != nil {
				t.Fatalf("full suffix unexpectedly repeated: pump=%v err=%v", pump, err)
			}
			replacement := growthTestSplice(graph, before, alternative, start, end)
			next, pump, err := states.pushCycle(graph, before, replacement, alternative, start, end, 1e-6)
			if err != nil || pump != (steps == 1) {
				t.Fatalf("push cycle did not close on the second candidate: pump=%v err=%v", pump, err)
			}
			if !pump {
				if cycle, err := states.trackReplacement(graph, before, replacement, start); cycle || err != nil {
					t.Fatal(err)
				}
				states.rememberPush(next)
			}
		}
		steps++
	})
	if terminated || accepted != 64 || steps != 64 || len(old.Traversals[0].Portions) != 81 {
		t.Fatalf("bounded old oracle did not reproduce unbounded growth: terminated=%v accepted=%d", terminated, accepted)
	}
	result := cloneParkingResult(original)
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil {
		t.Fatal(err)
	}
	expected := cloneParkingResult(original)
	expected.Traversals[0].Portions = firstAccepted
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("only the closing candidate should be rejected: portions=%d want=%d", len(result.Traversals[0].Portions), len(firstAccepted))
	}
	if states.count != 2 || states.bytes != 39 || states.acceptedReplacements != 1 || len(states.portions) != 21 || len(states.suffixes) != 19 {
		t.Fatal("same-cursor proof grew tracking beyond the first accepted transition")
	}
	t.Logf("SYNTH same-cursor pump: accepted=%d key states=%d retained key bytes=%d interned portions=%d suffix nodes=%d charged cache bytes=%d", states.acceptedReplacements, states.count, states.bytes, len(states.portions), len(states.suffixes), states.internBytes)
}

func parkingLaterEndEscapeFixture() (compiledGraph, []Observation, Result) {
	var segments []DirectedSegment
	for _, spec := range []struct {
		id, from, to   string
		x1, y1, x2, y2 float64
	}{
		{"parking", "entry", "exit", 0, -10, 0, 0},
		{"left-1", "exit", "left", 0, 0, 0, 10},
		{"left-2", "left", "bridge-a", 0, 10, 0, 20},
		{"right-1", "exit", "right", 0, 0, 8, 10},
		{"right-2", "right", "bridge-b", 8, 10, 8, 20},
		{"forward-1", "bridge-a", "forward", 0, 20, 0, 24},
		{"forward-2", "forward", "bridge-b", 0, 24, 8, 20},
		{"return-1", "bridge-b", "return", 8, 20, 8, 24},
		{"return-2", "return", "bridge-a", 8, 24, 0, 20},
		{"escape", "exit", "return", 0, 0, 8, 24},
		{"tail", "bridge-b", "end", 8, 20, 18, 20},
	} {
		s := segment(spec.id, spec.from, spec.to, spec.x1, spec.y1, spec.x2, spec.y2, spec.id, "public")
		s.LengthMeters, s.ContinuityClass, s.Direction = distance(s.From, s.To), "road", SegmentForward
		physical := s.ID
		if s.ID == "parking" || s.ID == "escape" {
			physical = "parking-physical"
			s.TransitionOnly, s.ParkingAisleConnector, s.ContinuityClass = true, true, ""
		}
		if s.ID == "escape" {
			// Longer than the right corridor plus return-1, so earlier ends
			// prefer the growing corridor rewrite. Still fits the later end.
			s.LengthMeters = math.Hypot(8, 10) + 24
		}
		s.PhysicalSegmentID = uuid.NewSHA1(uuid.Nil, []byte(physical))
		s.PhysicalID = s.PhysicalSegmentID.String()
		segments = append(segments, s)
	}
	graph := compileGraph(Graph{Segments: segments, MaxParkingAisleConnectorMeters: 200})
	raw := syntheticObservations([]Point{{0, -10}, {0, 0}, {0, 10}, {0, 20}, {0, 24}, {8, 20}, {8, 24}, {0, 20}, {0, 0}, {8, 10}, {8, 20}, {0, 0}, {4, 12}, {8, 24}, {8, 20}, {18, 20}}, nil)
	traversal := DecodedTraversal{FirstObservation: 0, LastObservation: len(raw) - 1, Cost: 27}
	for _, id := range []string{"parking", "left-1", "left-2", "forward-1", "forward-2", "tail"} {
		traversal.Portions = append(traversal.Portions, graph.portion(id, 0, graph.lengths[id]))
	}
	for i := range raw {
		candidate := candidatesNear(raw[i:i+1], Graph{Segments: segments})[0][0]
		traversal.Observations = append(traversal.Observations, DecodedObservation{ObservationIndex: i, Status: ObservationMatched, Candidate: &candidate})
	}
	return graph, raw, Result{Observations: append([]DecodedObservation(nil), traversal.Observations...), Traversals: []DecodedTraversal{traversal}}
}

func TestParkingPushRejectionStillTriesLaterEnd(t *testing.T) {
	graph, raw, original := parkingLaterEndEscapeFixture()
	old := cloneParkingResult(original)
	observed := 0
	terminated, accepted := oldParkingRepair(&old, raw, graph, 120, 20, 15, 4, func(start, end int, before, alternative []TraversedPortion) {
		if start != 0 || end != 3 || len(before) != 6+2*observed || len(alternative) != 4 {
			t.Fatal("later-end fixture does not grow at end=cursor+3")
		}
		observed++
	})
	if terminated || accepted != 4 {
		t.Fatalf("bounded old trace did not exhibit the growing rewrites: terminated=%v accepted=%d", terminated, accepted)
	}
	// The exhausted trace above is not a final-result oracle. This explicit
	// escape state is independently attested terminal by the old policy below.
	expected := cloneParkingResult(original)
	expected.Traversals[0].Portions = nil
	for _, id := range []string{"parking", "escape", "return-2", "forward-1", "forward-2", "tail"} {
		expected.Traversals[0].Portions = append(expected.Traversals[0].Portions, graph.portion(id, 0, graph.lengths[id]))
	}
	terminal := cloneParkingResult(expected)
	if terminated, accepted := oldParkingRepair(&terminal, raw, graph, 120, 20, 15, 16); !terminated || accepted != 0 || !reflect.DeepEqual(terminal, expected) {
		t.Fatalf("expected escape state is not an attested old-policy terminal result: terminated=%v accepted=%d", terminated, accepted)
	}
	result := cloneParkingResult(original)
	if err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6); err != nil || !reflect.DeepEqual(result, terminal) {
		t.Fatalf("rejecting the end=3 pump skipped the end=4 escape or changed full evidence: %v", err)
	}
}

func TestParkingPushLaterEndKeyCancellationIsAtomic(t *testing.T) {
	graph, raw, result := parkingLaterEndEscapeFixture()
	for range 512 {
		result.Traversals[0].Portions = append(result.Traversals[0].Portions, growthTestPortion("large-tail"))
	}
	before := cloneParkingResult(result)
	// Each of the first two keys has 518..520 portions, guaranteeing four
	// 128-portion checkpoints. Poll nine is inside the later escape's key,
	// after one accepted replacement and the rejected push-cycle closer.
	ctx := newCPUCancelContext(t, ".(*parkingRepairStates).key", true, 9)
	graph.ctx = ctx
	err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, before) {
		t.Fatalf("later-end key cancellation did not roll back the earlier replacement: %v", err)
	}
}

func growthTestPortion(id string) TraversedPortion {
	return TraversedPortion{SegmentID: id, Direction: SegmentForward, ToMeter: 1, SourceToFraction: 1}
}

func growthTestSplice(graph compiledGraph, current, alternative []TraversedPortion, start, end int) []TraversedPortion {
	replacement := append([]TraversedPortion(nil), current[:start+1]...)
	replacement = append(replacement, alternative...)
	replacement = append(replacement, current[end:]...)
	return graph.mergeConsecutiveTraversedPortions(replacement, 1e-6)
}

func TestParkingPushCertificatePeriodsAndBudget(t *testing.T) {
	if reflect.TypeFor[parkingRepairPush]().Size()+128 > parkingRepairPushEntryBytes {
		t.Fatal("push descriptor outgrew its conservative fixed cache charge")
	}
	for _, period := range []int{1, 2} {
		t.Run(fmt.Sprint(period), func(t *testing.T) {
			graph := compiledGraph{}
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			current := []TraversedPortion{growthTestPortion("boundary"), growthTestPortion("a"), growthTestPortion("b"), growthTestPortion("c"), growthTestPortion("bridge"), growthTestPortion("tail")}
			for phase := range period {
				alternative := []TraversedPortion{current[1], current[2], current[3], growthTestPortion("push")}
				if period == 2 && phase == 0 {
					alternative[0], alternative[1], alternative[2] = growthTestPortion("d"), growthTestPortion("e"), growthTestPortion("f")
				}
				if period == 2 && phase == 1 {
					alternative[0], alternative[1], alternative[2], alternative[3] = growthTestPortion("a"), growthTestPortion("b"), growthTestPortion("c"), growthTestPortion("bridge")
				} else if period == 1 {
					alternative[3] = current[4]
				}
				replacement := growthTestSplice(graph, current, alternative, 0, 4)
				next, pump, err := states.pushCycle(graph, current, replacement, alternative, 0, 4, 1e-6)
				if err != nil || !next.valid || pump != (phase == period-1) {
					t.Fatalf("exact period-%d proof failed at phase %d: valid=%v pump=%v err=%v", period, phase, next.valid, pump, err)
				}
				// Production stores descriptors only after the accepted vector is interned.
				if _, err := states.key(graph, replacement, 0); err != nil {
					t.Fatal(err)
				}
				before := states.internBytes
				if states.push.valid {
					before -= parkingRepairPushEntryBytes
				}
				states.rememberPush(next)
				if !states.push.valid || states.internBytes != before+parkingRepairPushEntryBytes {
					t.Fatal("descriptor was not charged exactly once")
				}
				states.forgetPush()
				states.maxInternBytes = before + parkingRepairPushEntryBytes - 1
				states.rememberPush(next)
				if states.push.valid || states.internBytes != before {
					t.Fatal("optional descriptor did not decline when its charge could not fit")
				}
				states.maxInternBytes = maxParkingRepairInternBytes
				states.rememberPush(next)
				current = replacement
			}
			states.beginCursor(1)
			if states.push.valid {
				t.Fatal("cursor advance retained push history")
			}
			t.Logf("inline descriptor=%d bytes; charged cache=%d bytes", reflect.TypeFor[parkingRepairPush]().Size(), parkingRepairPushEntryBytes)
		})
	}
}

func TestParkingPushFactorizationAndIdentityNegatives(t *testing.T) {
	for _, kind := range []string{"consumed-tail", "tail-merge", "rewind", "zero-length", "left-merge", "metadata", "fraction", "nan", "signed-zero", "shrinking"} {
		t.Run(kind, func(t *testing.T) {
			graph := compiledGraph{}
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			current := []TraversedPortion{growthTestPortion("left"), growthTestPortion("boundary"), growthTestPortion("a"), growthTestPortion("b"), growthTestPortion("c"), growthTestPortion("bridge"), growthTestPortion("tail")}
			alternative := []TraversedPortion{current[2], current[3], current[4], current[5]}
			switch kind {
			case "tail-merge":
				current[6] = current[5]
				current[6].FromMeter, current[6].ToMeter = 1, 2
			case "rewind":
				current[6] = current[5]
				current[6].FromMeter, current[6].ToMeter = 1, 0
			case "zero-length":
				current[6] = current[5]
				current[6].FromMeter = 1
			case "left-merge":
				current[0] = current[1]
				current[0].ToMeter = 0
			case "shrinking":
				alternative = alternative[:2]
			}
			replacement := growthTestSplice(graph, current, alternative, 1, 5)
			switch kind {
			case "consumed-tail":
				replacement = replacement[:len(replacement)-1]
			case "metadata":
				replacement[2].SourceWayVersion++
			case "fraction":
				replacement[2].SourceToFraction = math.Nextafter(replacement[2].SourceToFraction, 2)
			case "nan":
				replacement[2].FromMeter = math.Float64frombits(0x7ff8000000000001)
			case "signed-zero":
				replacement[2].FromMeter = math.Copysign(0, -1)
			}
			states.push.valid = true
			states.internBytes = parkingRepairPushEntryBytes
			next, pump, err := states.pushCycle(graph, current, replacement, alternative, 1, 5, 1e-6)
			if err != nil || pump || next.valid || states.push.valid || states.internBytes != 0 {
				t.Fatalf("unsafe factorization retained a certificate: valid=%v pump=%v err=%v", next.valid, pump, err)
			}
		})
	}
}

func TestParkingPushCacheYieldsToMandatoryCaches(t *testing.T) {
	for _, kind := range []string{"portion", "suffix"} {
		t.Run(kind, func(t *testing.T) {
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			portion := growthTestPortion("public")
			base := 0
			if kind == "suffix" {
				if _, err := states.intern(compiledGraph{}, portion, 0); err != nil {
					t.Fatal(err)
				}
				base = states.internBytes
			}
			states.maxInternBytes = base + parkingRepairPushEntryBytes
			states.rememberPush(parkingRepairPush{valid: true})
			if !states.push.valid || states.internBytes != states.maxInternBytes {
				t.Fatal("fixture did not fill the budget with an optional descriptor")
			}
			if kind == "portion" {
				if _, err := states.intern(compiledGraph{}, portion, 0); err != nil {
					t.Fatalf("optional certificate caused an interning limit: %v", err)
				}
			} else {
				if pump, err := states.repeatedContinuation(compiledGraph{}, []TraversedPortion{portion}, 0, 1e-6); pump || err != nil {
					t.Fatalf("optional certificate caused a suffix-cache limit: %v", err)
				}
				if states.internBytes != base+2*parkingRepairSuffixEntryBytes {
					t.Fatal("suffix or continuation entry was not charged exactly")
				}
			}
			if states.push.valid || len(states.portions) != 1 || states.internBytes >= states.maxInternBytes {
				t.Fatal("mandatory cache insertion did not release optional history")
			}
		})
	}
}

func TestParkingPushNormalizedHeadCutIsNotAPush(t *testing.T) {
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	current := []TraversedPortion{growthTestPortion("boundary"), growthTestPortion("a"), growthTestPortion("b"), growthTestPortion("c"), growthTestPortion("bridge"), growthTestPortion("tail")}
	alternative := append([]TraversedPortion(nil), current[1:5]...)
	alternative[3].ToMeter, alternative[3].SourceToFraction = 0, 0
	replacement := growthTestSplice(compiledGraph{}, current, alternative, 0, 4)
	if !reflect.DeepEqual(replacement, current) {
		t.Fatal("zero-length head-cut fixture did not normalize to its original vector")
	}
	next, pump, err := states.pushCycle(compiledGraph{}, current, replacement, alternative, 0, 4, 1e-6)
	if err != nil || pump || !next.valid || next.pushed {
		t.Fatalf("unnormalized splice length was mistaken for positive growth: %+v %v", next, err)
	}
}

func TestParkingPushRejectsFutureInsertedBlockSeam(t *testing.T) {
	for _, to := range []float64{0, 1, 2} {
		t.Run(fmt.Sprint(to), func(t *testing.T) {
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			current := []TraversedPortion{growthTestPortion("boundary"), growthTestPortion("a"), growthTestPortion("b"), growthTestPortion("c"), growthTestPortion("bridge"), growthTestPortion("tail")}
			seam := current[4]
			seam.FromMeter, seam.ToMeter = 1, to
			alternative := []TraversedPortion{growthTestPortion("d"), growthTestPortion("e"), growthTestPortion("f"), growthTestPortion("g"), seam, growthTestPortion("separator")}
			replacement := growthTestSplice(compiledGraph{}, current, alternative, 0, 4)
			if len(replacement) != 9 {
				t.Fatal("fixture merged at an actual seam rather than a future replay seam")
			}
			// D starts with seam but ends with the old bridge, separated in this
			// actual vector. Replaying the push could merge across D's copies,
			// including a rewind or a zero-length merge.
			next, pump, err := states.pushCycle(compiledGraph{}, current, replacement, alternative, 0, 4, 1e-6)
			if err != nil || pump || next.valid {
				t.Fatalf("future inserted-block merge was assumed safe: valid=%v pump=%v err=%v", next.valid, pump, err)
			}
		})
	}
}

func TestParkingPushRequiresExactHeadsAndDeterminism(t *testing.T) {
	for _, kind := range []string{"metadata", "fraction", "nan-payload", "signed-zero", "ambiguous"} {
		t.Run(kind, func(t *testing.T) {
			states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
			current := []TraversedPortion{growthTestPortion("boundary"), growthTestPortion("a"), growthTestPortion("b"), growthTestPortion("c"), growthTestPortion("bridge"), growthTestPortion("tail")}
			alternative := append([]TraversedPortion(nil), current[1:5]...)
			graph := compiledGraph{}
			switch kind {
			case "metadata":
				alternative[0].SourceWayVersion++
			case "fraction":
				alternative[0].SourceFromFraction = math.Nextafter(0, 1)
			case "nan-payload":
				current[1].SourceFromFraction = math.Float64frombits(0x7ff8000000000001)
				alternative[0].SourceFromFraction = math.Float64frombits(0x7ff8000000000002)
			case "signed-zero":
				alternative[0].SourceFromFraction = math.Copysign(0, -1)
			case "ambiguous":
				graph.segments = map[string]DirectedSegment{
					"forward":   {PhysicalID: "shared", Direction: SegmentForward},
					"reverse-a": {PhysicalID: "shared", Direction: SegmentReverse},
					"reverse-b": {PhysicalID: "shared", Direction: SegmentReverse},
				}
			}
			replacement := growthTestSplice(graph, current, alternative, 0, 4)
			next, pump, err := states.pushCycle(graph, current, replacement, alternative, 0, 4, 1e-6)
			if err != nil || pump || next.valid == (kind == "ambiguous") {
				t.Fatalf("inexact or nondeterministic head was certified: valid=%v pump=%v err=%v", next.valid, pump, err)
			}
		})
	}
}

func TestParkingPushTailComparisonCancellationIsAtomic(t *testing.T) {
	graph, raw, result := parkingSameCursorGrowthFixture()
	for range 512 {
		result.Traversals[0].Portions = append(result.Traversals[0].Portions, growthTestPortion("extra-tail"))
	}
	before := cloneParkingResult(result)
	ctx := newCPUCancelContext(t, ".(*parkingRepairStates).pushCycle", true, 2)
	graph.ctx = ctx
	err := repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, before) {
		t.Fatalf("push comparison cancellation returned partial coverage: %v", err)
	}
}
