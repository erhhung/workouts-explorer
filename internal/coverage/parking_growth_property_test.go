package coverage

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

const growthPropertyEpsilon = 1e-6

type growthPropertyFrame struct {
	current, alternative, replacement []TraversedPortion
	start, end                        int
	deterministic, oldPolicy          bool
}

// These helpers deliberately do not use production identity, normalization, or
// certificate helpers. The pure machine below has the old loop's end order,
// splice, and same-cursor restart, but a supplied local acceptance relation.
func growthPropertyEqual(a, b TraversedPortion) bool {
	ab := [4]uint64{math.Float64bits(a.FromMeter), math.Float64bits(a.ToMeter), math.Float64bits(a.SourceFromFraction), math.Float64bits(a.SourceToFraction)}
	bb := [4]uint64{math.Float64bits(b.FromMeter), math.Float64bits(b.ToMeter), math.Float64bits(b.SourceFromFraction), math.Float64bits(b.SourceToFraction)}
	a.FromMeter, a.ToMeter, a.SourceFromFraction, a.SourceToFraction = 0, 0, 0, 0
	b.FromMeter, b.ToMeter, b.SourceFromFraction, b.SourceToFraction = 0, 0, 0, 0
	return a == b && ab == bb
}

func growthPropertySequenceEqual(a, b []TraversedPortion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !growthPropertyEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func growthPropertyCanMerge(a, b TraversedPortion) bool {
	return a.SegmentID != "" && a.SegmentID == b.SegmentID && a.Direction == b.Direction &&
		!(math.Abs(a.ToMeter-b.FromMeter) > growthPropertyEpsilon)
}

func growthPropertyNormalize(input []TraversedPortion) []TraversedPortion {
	var output []TraversedPortion
	for _, portion := range input {
		if len(output) == 0 || !growthPropertyCanMerge(output[len(output)-1], portion) {
			output = append(output, portion)
			continue
		}
		previous := &output[len(output)-1]
		previous.ToMeter = portion.ToMeter
		previous.SourceFromFraction = math.Min(previous.SourceFromFraction, portion.SourceFromFraction)
		previous.SourceToFraction = math.Max(previous.SourceToFraction, portion.SourceToFraction)
	}
	return output
}

func growthPropertyJoin(spans ...[]TraversedPortion) []TraversedPortion {
	var joined []TraversedPortion
	for _, span := range spans {
		joined = append(joined, span...)
	}
	return joined
}

func growthPropertyRewrite(current, alternative []TraversedPortion, start, end int) []TraversedPortion {
	return growthPropertyNormalize(growthPropertyJoin(current[:start+1], alternative, current[end:]))
}

// This is a sufficient certificate, not an iff test for semantic divergence.
// Materialize every carried tail and every inserted block instead of reusing
// production's short metadata descriptor. Check the cross-product of replay
// boundaries, including boundaries not exposed in the observed two steps.
func growthPropertyReference(frames []growthPropertyFrame) bool {
	if len(frames) < 1 || len(frames) > 2 {
		return false
	}
	var locals, boundaries [][]TraversedPortion
	growth := 0
	for i, frame := range frames {
		s, e := frame.start, frame.end
		if !frame.deterministic || !frame.oldPolicy || s < 0 || s+5 > len(frame.current) || e < s+2 || e > s+4 {
			return false
		}
		if i > 0 && (s != frames[i-1].start || !growthPropertySequenceEqual(frames[i-1].replacement, frame.current)) {
			return false
		}
		left, head, tail := frame.current[:s], frame.current[s:s+5], frame.current[s+5:]
		if !growthPropertySequenceEqual(left, growthPropertyNormalize(left)) || !growthPropertySequenceEqual(tail, growthPropertyNormalize(tail)) {
			return false
		}
		local := growthPropertyNormalize(growthPropertyJoin(head[:1], frame.alternative, head[e-s:]))
		if len(local) < 5 || len(left) > 0 && growthPropertyCanMerge(left[len(left)-1], local[0]) {
			return false
		}
		if !growthPropertySequenceEqual(frame.replacement, growthPropertyJoin(left, local, tail)) ||
			!growthPropertySequenceEqual(frame.replacement, growthPropertyRewrite(frame.current, frame.alternative, s, e)) {
			return false
		}
		locals = append(locals, local)
		boundaries = append(boundaries, tail, local[5:])
		growth += len(local) - 5
	}
	first, last := frames[0], frames[len(frames)-1]
	if growth == 0 || !growthPropertySequenceEqual(first.current[first.start:first.start+5], last.replacement[last.start:last.start+5]) {
		return false
	}
	for _, local := range locals {
		for _, boundary := range boundaries {
			if len(boundary) > 0 && growthPropertyCanMerge(local[len(local)-1], boundary[0]) {
				return false
			}
		}
	}
	return true
}

func growthPropertyProduction(t *testing.T, frames []growthPropertyFrame) bool {
	t.Helper()
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	graph := compiledGraph{}
	if len(frames) > 0 && !frames[0].deterministic {
		graph, _, _ = ambiguousParkingEscapeFixture()
	}
	cursor, cycle := -1, false
	for _, frame := range frames {
		if frame.start != cursor {
			states.beginCursor(frame.start)
			cursor = frame.start
		}
		if !frame.oldPolicy {
			states.forgetContinuations()
			cycle = false
			continue
		}
		push, repeated, err := states.pushCycle(graph, frame.current, frame.replacement, frame.alternative, frame.start, frame.end, growthPropertyEpsilon)
		if err != nil {
			t.Fatal(err)
		}
		cycle = repeated
		if repeated {
			states.forgetContinuations()
		} else {
			states.rememberPush(push)
		}
	}
	return cycle
}

func growthPropertyCheck(t *testing.T, frames []growthPropertyFrame, want bool) {
	t.Helper()
	if got := growthPropertyReference(frames); got != want {
		t.Fatalf("independent sufficient certificate = %v, want %v", got, want)
	}
	if got := growthPropertyProduction(t, frames); got != want {
		t.Fatalf("production certificate = %v, independent reference = %v", got, want)
	}
}

type growthPropertyRule func(head []TraversedPortion, end, accepted int) ([]TraversedPortion, bool)

func growthPropertyOldLoop(initial []TraversedPortion, budget int, deterministic bool, rule growthPropertyRule) ([]TraversedPortion, []growthPropertyFrame, bool) {
	current := growthPropertyJoin(initial)
	var frames []growthPropertyFrame
	for start := 0; start+2 < len(current); start++ {
		for end := start + 2; end < len(current) && end <= start+4; end++ {
			alternative, ok := rule(current[start:min(start+5, len(current))], end-start, len(frames))
			if !ok {
				continue
			}
			if len(frames) == budget {
				return current, frames, false
			}
			replacement := growthPropertyRewrite(current, alternative, start, end)
			frames = append(frames, growthPropertyFrame{current, growthPropertyJoin(alternative), replacement, start, end, deterministic, true})
			current = replacement
			start = max(-1, start-1)
			break
		}
	}
	return current, frames, true
}

func growthPropertyPortion(id string) TraversedPortion {
	return TraversedPortion{SegmentID: id, FromMeter: 0, ToMeter: 10, RegionID: "synthetic-region", GenerationID: 27,
		DerivationVersion: 24, LogicalPathID: "synthetic-path", LocalityID: "synthetic-locality", SourceWayID: 31,
		SourceWayVersion: 2, Direction: SegmentForward, SourceFromFraction: 0, SourceToFraction: 1, ContinuityClass: "road"}
}

func growthPropertyFixture(prefix, tail int) ([]TraversedPortion, []TraversedPortion, []TraversedPortion, growthPropertyRule) {
	a := []TraversedPortion{growthPropertyPortion("parking"), growthPropertyPortion("a1"), growthPropertyPortion("a2"), growthPropertyPortion("a3"), growthPropertyPortion("forward")}
	b := []TraversedPortion{a[0], growthPropertyPortion("b1"), growthPropertyPortion("b2"), growthPropertyPortion("b3"), growthPropertyPortion("reverse")}
	b[4].Direction = SegmentReverse
	var left, right []TraversedPortion
	for i := range prefix {
		left = append(left, growthPropertyPortion(fmt.Sprintf("left-%d", i)))
	}
	for i := range tail {
		right = append(right, growthPropertyPortion(fmt.Sprintf("tail-%d", i)))
	}
	rule := func(head []TraversedPortion, end, _ int) ([]TraversedPortion, bool) {
		if end != 4 || len(head) != 5 {
			return nil, false
		}
		if growthPropertySequenceEqual(head, a) {
			return b[1:], true
		}
		if growthPropertySequenceEqual(head, b) {
			return a[1:], true
		}
		return nil, false
	}
	return growthPropertyJoin(left, a, right), a, b, rule
}

func TestParkingGrowthPropertyIndependent64StepPump(t *testing.T) {
	for _, prefix := range []int{0, 3} {
		for _, tail := range []int{0, 9} {
			initial, a, b, rule := growthPropertyFixture(prefix, tail)
			_, frames, terminated := growthPropertyOldLoop(initial, 64, true, rule)
			if terminated || len(frames) != 64 {
				t.Fatal("positive machine did not produce the requested 64 transitions")
			}
			for i, frame := range frames {
				head := a
				if i%2 != 0 {
					head = b
				}
				want := growthPropertyJoin(initial[:prefix], head)
				if i%2 != 0 {
					want = append(want, a[4])
				}
				for range i / 2 {
					want = append(want, b[4], a[4])
				}
				want = append(want, initial[prefix+5:]...)
				if frame.start != prefix || frame.end != prefix+4 || !growthPropertySequenceEqual(frame.current, want) || len(frame.replacement) != len(frame.current)+1 {
					t.Fatalf("symbolic push equations failed at step %d", i)
				}
				if i > 0 {
					growthPropertyCheck(t, frames[i-1:i+1], true)
				}
			}
			// The closed symbolic equations and replay barriers prove the pump;
			// the exhausted 64-step observation is not a nontermination oracle.
		}
	}
}

func TestParkingGrowthPropertyPeriodOne(t *testing.T) {
	p, q := growthPropertyPortion("p"), growthPropertyPortion("q")
	current := []TraversedPortion{p, q, q, q, q}
	alternative := []TraversedPortion{q, q}
	frame := growthPropertyFrame{current, alternative, growthPropertyRewrite(current, alternative, 0, 2), 0, 2, true, true}
	growthPropertyCheck(t, []growthPropertyFrame{frame}, true)
}

func TestParkingGrowthPropertyFiniteShrinkingTail(t *testing.T) {
	for _, count := range []int{1, 2, 8, 31} {
		p, a, b, c, f := growthPropertyPortion("p"), growthPropertyPortion("a"), growthPropertyPortion("b"), growthPropertyPortion("c"), growthPropertyPortion("f")
		x, y := growthPropertyPortion("x"), growthPropertyPortion("y")
		headA, headB := []TraversedPortion{p, a, b, c, f}, []TraversedPortion{p, x, y, f, c}
		initial := growthPropertyJoin(headA)
		for range count {
			initial = append(initial, c, f)
		}
		initial = append(initial, growthPropertyPortion("stop"))
		rule := func(head []TraversedPortion, end, _ int) ([]TraversedPortion, bool) {
			if end == 4 && growthPropertySequenceEqual(head, headA) {
				return []TraversedPortion{x, y}, true
			}
			if end == 4 && growthPropertySequenceEqual(head, headB) {
				return []TraversedPortion{a, b}, true
			}
			return nil, false
		}
		_, frames, terminated := growthPropertyOldLoop(initial, 2*count+2, true, rule)
		if !terminated || len(frames) != 2*count+1 || !growthPropertySequenceEqual(frames[0].current[:5], frames[1].replacement[:5]) {
			t.Fatal("finite counterexample did not repeat its head while consuming tail")
		}
		for i := 1; i < len(frames); i++ {
			growthPropertyCheck(t, frames[i-1:i+1], false)
		}
	}
}

func TestParkingGrowthPropertyGlobalRewindingMerge(t *testing.T) {
	epsilon := growthPropertyEpsilon
	piece := func(from, to float64) TraversedPortion {
		p := growthPropertyPortion("chain")
		p.FromMeter, p.ToMeter, p.SourceFromFraction, p.SourceToFraction = from, to, from/30, to/30
		return p
	}
	first, rewind, continuation := piece(0, 10), piece(10-0.75*epsilon, 10-0.25*epsilon), piece(10-0.25*epsilon, 20)
	if got := growthPropertyNormalize([]TraversedPortion{first, rewind}); len(got) != 1 || got[0].ToMeter >= first.ToMeter {
		t.Fatal("positive-length epsilon-adjacent portion did not rewind ToMeter")
	}
	chain := []TraversedPortion{first, rewind, continuation}
	if got := (compiledGraph{}).mergeConsecutiveTraversedPortions(chain, epsilon); !growthPropertySequenceEqual(got, growthPropertyNormalize(chain)) {
		t.Fatal("production merge differs from independent rewinding-chain semantics")
	}
	initial, _, _, _ := growthPropertyFixture(3, 0)
	initial[len(initial)-1] = first
	initial = append(initial, rewind, continuation, growthPropertyPortion("stop"))
	alt := []TraversedPortion{growthPropertyPortion("b1"), growthPropertyPortion("b2"), growthPropertyPortion("b3"), growthPropertyPortion("reverse")}
	frame := growthPropertyFrame{initial, alt, growthPropertyRewrite(initial, alt, 3, 7), 3, 7, true, true}
	if len(frame.replacement) != len(initial)-1 {
		t.Fatal("full-vector normalization did not absorb both tail portions across the head cut")
	}
	growthPropertyCheck(t, []growthPropertyFrame{frame}, false)
}

func TestParkingGrowthPropertyNonMonotonicAndNaNMeterMerges(t *testing.T) {
	for _, kind := range []string{"negative-interval", "zero-length", "nan-from", "nan-to"} {
		t.Run(kind, func(t *testing.T) {
			initial, a, b, _ := growthPropertyFixture(3, 0)
			first, second := a[4], a[4]
			first.FromMeter, first.ToMeter = 10, 12
			second.FromMeter, second.ToMeter = 12, 20
			switch kind {
			case "negative-interval":
				first.ToMeter, second.FromMeter = 2, 2
			case "zero-length":
				first.ToMeter, second.FromMeter = 10, 10
			case "nan-from":
				first.FromMeter = math.Float64frombits(0x7ff8000000000001)
			case "nan-to":
				first.ToMeter = math.Float64frombits(0x7ff8000000000002)
			}
			// Malformed synthetic intervals test the normalizer, not graph input
			// acceptance. In particular, abs(NaN) > epsilon is false, not true.
			chain := []TraversedPortion{a[4], first, second}
			want := growthPropertyNormalize(chain)
			if len(want) != 1 || want[0].ToMeter != 20 ||
				!growthPropertySequenceEqual(want, (compiledGraph{}).mergeConsecutiveTraversedPortions(chain, growthPropertyEpsilon)) {
				t.Fatal("independent and production non-monotonic merge semantics differ")
			}
			initial = append(initial, first, second, growthPropertyPortion("stop"))
			frame := growthPropertyFrame{initial, b[1:], growthPropertyRewrite(initial, b[1:], 3, 7), 3, 7, true, true}
			next := growthPropertyFrame{frame.replacement, a[1:], growthPropertyRewrite(frame.replacement, a[1:], 3, 7), 3, 7, true, true}
			if !growthPropertySequenceEqual(initial[3:8], next.replacement[3:8]) {
				t.Fatal("merge-sensitive negative did not close its local head")
			}
			growthPropertyCheck(t, []growthPropertyFrame{frame, next}, false)
		})
	}
	initial, a, b, _ := growthPropertyFixture(0, 1)
	a[4].ToMeter = -1
	initial[4] = a[4]
	first := growthPropertyFrame{initial, b[1:], growthPropertyRewrite(initial, b[1:], 0, 4), 0, 4, true, true}
	second := growthPropertyFrame{first.replacement, a[1:], growthPropertyRewrite(first.replacement, a[1:], 0, 4), 0, 4, true, true}
	// The sequence theorem needs exact replay barriers, not increasing meters.
	growthPropertyCheck(t, []growthPropertyFrame{first, second}, true)
}

func TestParkingGrowthPropertyDistantTailMergeEventuallyEscapes(t *testing.T) {
	p, a, b, c, f := growthPropertyPortion("p"), growthPropertyPortion("a"), growthPropertyPortion("b"), growthPropertyPortion("c"), growthPropertyPortion("f")
	x, y := growthPropertyPortion("x"), growthPropertyPortion("y")
	f.SourceToFraction = 0.5
	headA, headB := []TraversedPortion{p, a, b, c, f}, []TraversedPortion{p, x, y, f, c}
	initial := growthPropertyJoin(headA)
	const occurrences = 8
	for range occurrences {
		initial = append(initial, c, f)
	}
	cap := f
	cap.FromMeter, cap.ToMeter, cap.SourceFromFraction, cap.SourceToFraction = 10, 20, 0.5, 1
	initial = append(initial, cap, growthPropertyPortion("stop"))
	rule := func(head []TraversedPortion, end, _ int) ([]TraversedPortion, bool) {
		if end == 4 && growthPropertySequenceEqual(head, headA) {
			return []TraversedPortion{x, y}, true
		}
		if end == 4 && growthPropertySequenceEqual(head, headB) {
			return []TraversedPortion{a, b}, true
		}
		return nil, false
	}
	final, frames, terminated := growthPropertyOldLoop(initial, 32, true, rule)
	if !terminated || len(frames) != 2*occurrences || !growthPropertySequenceEqual(frames[0].current[:5], frames[1].replacement[:5]) {
		t.Fatal("distant-merge counterexample did not repeat a head before terminating")
	}
	if final[4].ToMeter != 20 || final[4].SourceToFraction != 1 || len(frames[0].replacement) != len(initial)-2 {
		t.Fatal("global merge did not propagate distant interval metadata into the eventual escape head")
	}
	for i := 1; i < len(frames); i++ {
		growthPropertyCheck(t, frames[i-1:i+1], false)
	}
}

func TestParkingGrowthPropertyFutureInsertionBarrier(t *testing.T) {
	initial, a, b, _ := growthPropertyFixture(0, 1)
	// All observed joins are sealed, but forward -> forward is not. The
	// sufficient theorem quantifies over every insertion-boundary combination.
	a[4].ToMeter = growthPropertyEpsilon / 2
	a[4].SourceToFraction = a[4].ToMeter / 10
	initial[4] = a[4]
	first := growthPropertyFrame{initial, b[1:], growthPropertyRewrite(initial, b[1:], 0, 4), 0, 4, true, true}
	second := growthPropertyFrame{first.replacement, a[1:], growthPropertyRewrite(first.replacement, a[1:], 0, 4), 0, 4, true, true}
	if !growthPropertySequenceEqual(initial[:5], second.replacement[:5]) || !growthPropertyCanMerge(a[4], a[4]) {
		t.Fatal("future-boundary counterexample is not closed and merge-sensitive")
	}
	growthPropertyCheck(t, []growthPropertyFrame{first, second}, false)
}

func TestParkingGrowthPropertyExactWholeTailAndHeadMetadata(t *testing.T) {
	initial, _, _, rule := growthPropertyFixture(3, 9)
	_, frames, _ := growthPropertyOldLoop(initial, 2, true, rule)
	for field := range reflect.TypeFor[TraversedPortion]().NumField() {
		t.Run(reflect.TypeFor[TraversedPortion]().Field(field).Name, func(t *testing.T) {
			changed := append([]growthPropertyFrame(nil), frames...)
			changed[1].replacement = growthPropertyJoin(changed[1].replacement)
			value := reflect.ValueOf(&changed[1].replacement[len(changed[1].replacement)-1]).Elem().Field(field)
			switch value.Kind() {
			case reflect.String:
				value.SetString(value.String() + "different")
			case reflect.Int, reflect.Int64:
				value.SetInt(value.Int() + 1)
			case reflect.Float64:
				value.SetFloat(math.Nextafter(value.Float(), math.Inf(1)))
			case reflect.Array:
				value.Index(0).SetUint(value.Index(0).Uint() ^ 1)
			default:
				t.Fatalf("unhandled portion field %s", value.Kind())
			}
			growthPropertyCheck(t, changed, false)
		})
	}
	for _, mutate := range []func(*TraversedPortion){
		func(p *TraversedPortion) { p.GenerationID++ },
		func(p *TraversedPortion) { p.FromMeter = math.Copysign(0, -1) },
		func(p *TraversedPortion) { p.SourceFromFraction = math.Float64frombits(0x7ff8000000000001) },
	} {
		changed := append([]growthPropertyFrame(nil), frames...)
		changed[1].alternative = growthPropertyJoin(changed[1].alternative)
		mutate(&changed[1].alternative[0])
		changed[1].replacement = growthPropertyRewrite(changed[1].current, changed[1].alternative, changed[1].start, changed[1].end)
		growthPropertyCheck(t, changed, false)
	}
}

func TestParkingGrowthPropertyIdenticalNaNBits(t *testing.T) {
	initial, a, b, _ := growthPropertyFixture(0, 1)
	a[1].SourceFromFraction = math.Float64frombits(0x7ff8000000000001)
	initial[1] = a[1]
	first := growthPropertyFrame{initial, b[1:], growthPropertyRewrite(initial, b[1:], 0, 4), 0, 4, true, true}
	second := growthPropertyFrame{first.replacement, a[1:], growthPropertyRewrite(first.replacement, a[1:], 0, 4), 0, 4, true, true}
	growthPropertyCheck(t, []growthPropertyFrame{first, second}, true)
	second.alternative = growthPropertyJoin(second.alternative)
	second.alternative[0].SourceFromFraction = math.Float64frombits(0x7ff8000000000002)
	second.replacement = growthPropertyRewrite(second.current, second.alternative, 0, 4)
	growthPropertyCheck(t, []growthPropertyFrame{first, second}, false)
}

func TestParkingGrowthPropertyRejectsUnattestedSpliceAndTail(t *testing.T) {
	initial, _, _, rule := growthPropertyFixture(3, 9)
	_, frames, _ := growthPropertyOldLoop(initial, 2, true, rule)
	for _, name := range []string{"hidden-alternative-suffix", "interior-tail-reordered", "frozen-prefix-changed", "different-end"} {
		t.Run(name, func(t *testing.T) {
			changed := append([]growthPropertyFrame(nil), frames...)
			last := &changed[1]
			last.replacement = growthPropertyJoin(last.replacement)
			switch name {
			case "hidden-alternative-suffix":
				last.alternative = append(growthPropertyJoin(last.alternative), growthPropertyPortion("hidden"))
			case "interior-tail-reordered":
				i := len(last.replacement) - 4
				last.replacement[i], last.replacement[i+1] = last.replacement[i+1], last.replacement[i]
			case "frozen-prefix-changed":
				last.replacement[0].GenerationID++
			case "different-end":
				last.end--
			}
			growthPropertyCheck(t, changed, false)
		})
	}
	current := initial[3:8]
	noGrowth := growthPropertyFrame{current, current[1:4], growthPropertyJoin(current), 0, 4, true, true}
	growthPropertyCheck(t, []growthPropertyFrame{noGrowth}, false)
}

func TestParkingGrowthPropertyAmbiguousRepeatCanEscape(t *testing.T) {
	initial, _, _, base := growthPropertyFixture(0, 1)
	rule := func(head []TraversedPortion, end, accepted int) ([]TraversedPortion, bool) {
		if accepted == 2 && end == 4 && len(head) == 5 && head[1].SegmentID == "a1" {
			return []TraversedPortion{growthPropertyPortion("escape")}, true
		}
		return base(head, end, accepted)
	}
	_, frames, terminated := growthPropertyOldLoop(initial, 4, false, rule)
	if !terminated || len(frames) != 3 || !growthPropertySequenceEqual(frames[0].current[:5], frames[1].replacement[:5]) {
		t.Fatal("ambiguous choice tape did not repeat its head and then escape")
	}
	growthPropertyCheck(t, frames[:2], false)
	graph, raw, original := ambiguousParkingEscapeFixture()
	tape := []string{"parking-r-dead", "parking-r-dead", "parking-r-good"}
	old, _, _, err := replayParkingAliasTape(t, graph, raw, original, tape, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	guarded, _, _, err := replayParkingAliasTape(t, graph, raw, original, tape, 3, true)
	if err != nil || !reflect.DeepEqual(old, guarded) {
		t.Fatalf("real-graph terminating old choice tape changed: %v", err)
	}
}

func TestParkingGrowthPropertySkipAndCursorBreakContiguity(t *testing.T) {
	initial, _, _, rule := growthPropertyFixture(3, 1)
	_, frames, _ := growthPropertyOldLoop(initial, 3, true, rule)
	states := newParkingRepairStates(maxParkingRepairStates, maxParkingRepairStateBytes)
	first := frames[0]
	push, cycle, err := states.pushCycle(compiledGraph{}, first.current, first.replacement, first.alternative, first.start, first.end, growthPropertyEpsilon)
	if err != nil || cycle || !push.valid {
		t.Fatalf("first accepted descriptor invalid: %v", err)
	}
	for _, reset := range []func(){states.forgetContinuations, func() { states.beginCursor(first.start + 1) }} {
		states.rememberPush(push)
		reset()
		if states.push.valid {
			t.Fatal("skip/cursor change retained an accepted descriptor")
		}
		second := frames[1]
		_, cycle, err := states.pushCycle(compiledGraph{}, second.current, second.replacement, second.alternative, second.start, second.end, growthPropertyEpsilon)
		if err != nil || cycle {
			t.Fatalf("next iteration reused pre-skip proof history: %v", err)
		}
	}
	frames[1].oldPolicy = false
	growthPropertyCheck(t, frames[:2], false)
	if growthPropertyProduction(t, frames) {
		t.Fatal("a later-end acceptance from an interrupted iteration seeded the next iteration's proof")
	}
}

func TestParkingGrowthPropertyTerminatingOriginalOccurrences(t *testing.T) {
	for _, count := range []int{1, 2, 8, 20} {
		graph, raw, original := supportedParkingMiddleGraph()
		base := append([]TraversedPortion(nil), original.Traversals[0].Portions...)
		original.Traversals[0].Portions = nil
		for occurrence := range count {
			for _, portion := range base {
				portion.GenerationID += int64(occurrence)
				portion.SourceWayVersion += occurrence
				original.Traversals[0].Portions = append(original.Traversals[0].Portions, portion)
			}
		}
		old := cloneParkingResult(original)
		if terminated, accepted := oldParkingRepair(&old, raw, graph, 120, 20, 15, 128); !terminated || accepted != count {
			t.Fatalf("old oracle did not finish finite original occurrences: %v/%d", terminated, accepted)
		}
		if err := repairRawSupportedParallelParkingRoutes(&original, raw, graph, 120, 20, 15, growthPropertyEpsilon); err != nil || !reflect.DeepEqual(original, old) {
			t.Fatalf("terminating old full-result oracle differs: %v", err)
		}
	}
}

func TestParkingGrowthPropertyGeneratedWitnesses(t *testing.T) {
	certified, declined := 0, 0
	for seed := range 512 {
		prefix, tail := 1+seed%4, 1+(seed/4)%12
		initial, a, b, _ := growthPropertyFixture(prefix, tail)
		switch seed % 8 {
		case 1:
			a[1].GenerationID += int64(seed + 1)
		case 2:
			a[4].SourceToFraction = 0.5
			initial[prefix+4] = a[4]
			initial[prefix+5] = a[4]
			initial[prefix+5].FromMeter, initial[prefix+5].ToMeter = 10, 20
			initial[prefix+5].SourceFromFraction, initial[prefix+5].SourceToFraction = 0.5, 1
		case 3:
			a[0].FromMeter, a[0].ToMeter, a[0].SourceFromFraction = 10, 20, 0.5
			initial[prefix] = a[0]
			initial[prefix-1] = a[0]
			initial[prefix-1].FromMeter, initial[prefix-1].ToMeter = 0, 10
			initial[prefix-1].SourceFromFraction, initial[prefix-1].SourceToFraction = 0, 0.5
		case 4:
			a[4].SourceToFraction = 0.5
			initial[prefix+4] = a[4]
			b[4] = a[4]
			b[4].FromMeter, b[4].ToMeter = 10, 20
			b[4].SourceFromFraction, b[4].SourceToFraction = 0.5, 1
		case 5:
			b[4] = a[4]
		case 7:
			a[1].SourceFromFraction = math.Float64frombits(0x7ff8000000000000 | uint64(seed+1))
			initial[prefix+1] = a[1]
		}
		first := growthPropertyFrame{initial, b[1:], growthPropertyRewrite(initial, b[1:], prefix, prefix+4), prefix, prefix + 4, true, true}
		second := growthPropertyFrame{first.replacement, a[1:], growthPropertyRewrite(first.replacement, a[1:], prefix, prefix+4), prefix, prefix + 4, true, true}
		if seed%8 == 6 {
			second.replacement[len(second.replacement)-1].SourceWayVersion++
		}
		frames := []growthPropertyFrame{first, second}
		want := growthPropertyReference(frames)
		if got := growthPropertyProduction(t, frames); got != want {
			t.Fatalf("generated witness %d: production=%v independent reference=%v", seed, got, want)
		}
		if want {
			certified++
		} else {
			declined++
		}
	}
	if certified < 128 || declined < 128 {
		t.Fatalf("vacuous generated corpus: certified=%d declined=%d", certified, declined)
	}
	t.Logf("independent certificate parity: certified=%d declined=%d", certified, declined)
}

func FuzzParkingGrowthPropertyExactCertificate(f *testing.F) {
	f.Add(uint8(3), uint8(9), uint8(0), uint64(27))
	f.Add(uint8(0), uint8(1), uint8(1), uint64(0x7ff8000000000001))
	f.Add(uint8(1), uint8(0), uint8(2), uint64(0x8000000000000000))
	f.Fuzz(func(t *testing.T, prefix, tail, mutation uint8, metadata uint64) {
		initial, _, _, rule := growthPropertyFixture(int(prefix%5), int(tail%12))
		_, frames, _ := growthPropertyOldLoop(initial, 2, true, rule)
		switch mutation % 5 {
		case 1:
			frames[1].alternative = growthPropertyJoin(frames[1].alternative)
			frames[1].alternative[0].GenerationID = int64(metadata)
			frames[1].replacement = growthPropertyRewrite(frames[1].current, frames[1].alternative, frames[1].start, frames[1].end)
		case 2:
			frames[1].alternative = growthPropertyJoin(frames[1].alternative)
			frames[1].alternative[0].SourceFromFraction = math.Float64frombits(metadata)
			frames[1].replacement = growthPropertyRewrite(frames[1].current, frames[1].alternative, frames[1].start, frames[1].end)
		case 3:
			frames[1].oldPolicy = false
		case 4:
			frames[0].deterministic, frames[1].deterministic = false, false
		}
		want := growthPropertyReference(frames)
		if got := growthPropertyProduction(t, frames); got != want {
			t.Fatalf("production=%v reference=%v prefix=%d tail=%d mutation=%d metadata=%x", got, want, prefix, tail, mutation, metadata)
		}
	})
}
