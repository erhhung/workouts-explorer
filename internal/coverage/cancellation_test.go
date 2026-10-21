package coverage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
)

// Cancel only at a checkpoint in the selected CPU function. The real decoder,
// heap search and repair loops run synchronously, without production test hooks.
type cpuCancelContext struct {
	context.Context
	cancel     context.CancelCauseFunc
	function   string
	viaCounter bool
	remaining  int
	canceledAt time.Time
}

func newCPUCancelContext(t *testing.T, function string, viaCounter bool, polls int) *cpuCancelContext {
	t.Helper()
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	ctx, cancel := context.WithCancelCause(deadline)
	t.Cleanup(func() { cancel(nil); stop() })
	return &cpuCancelContext{Context: ctx, cancel: cancel, function: function, viaCounter: viaCounter, remaining: polls}
}

func (ctx *cpuCancelContext) Err() error {
	if err := ctx.Context.Err(); err != nil {
		return err
	}
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	viaCounter := false
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".compiledGraph.canceled") {
			viaCounter = true
		} else if !strings.HasSuffix(frame.Function, ".compiledGraph.err") {
			if strings.HasSuffix(frame.Function, ctx.function) && viaCounter == ctx.viaCounter {
				ctx.remaining--
				if ctx.remaining == 0 {
					ctx.canceledAt = time.Now()
					ctx.cancel(errors.New("synthetic lease loss"))
				}
			}
			break
		}
		if !more {
			break
		}
	}
	return ctx.Context.Err()
}

func assertCPUStopped(t *testing.T, ctx *cpuCancelContext) {
	t.Helper()
	if ctx.canceledAt.IsZero() {
		t.Fatal("target CPU loop did not trigger cancellation")
	}
	if elapsed := time.Since(ctx.canceledAt); elapsed >= time.Second {
		t.Fatalf("CPU work continued for %s after cancellation", elapsed)
	}
	if ctx.Err() != context.Canceled || context.Cause(ctx.Context).Error() != "synthetic lease loss" {
		t.Fatal("context cancellation or its caller-owned cause was lost")
	}
}

func TestMatchContextUncanceledGolden(t *testing.T) {
	hash := sha256.New()
	cases := 0
	for _, fixture := range syntheticFixtures() {
		observations := syntheticObservations(fixture.points, fixture.times)
		candidates := candidatesNear(observations, fixture.graph)
		if fixture.candidates != nil {
			candidates = fixture.candidates(observations, fixture.graph)
		}
		for variant := range 17 {
			rules := ExperimentalRules().WithMinimumTraversalMeters(1 + float64(variant)*2.5)
			result, err := MatchContext(context.Background(), observations, candidates, fixture.graph, rules)
			if err != nil {
				t.Fatal(err)
			}
			if legacy := Match(observations, candidates, fixture.graph, rules); !reflect.DeepEqual(result, legacy) {
				t.Fatalf("legacy wrapper differs for %s variant %d", fixture.name, variant)
			}
			if err := json.NewEncoder(hash).Encode(result); err != nil {
				t.Fatal(err)
			}
			cases++
		}
	}
	// Captured from the original matcher, graph and OSM sources before this change.
	const golden = "0af466ae40c8b8f8cf7cd7c52daea94342e323ef8302f00a212122164afeb7d4"
	if got := fmt.Sprintf("%x", hash.Sum(nil)); cases != 204 || got != golden {
		t.Fatalf("uncanceled regression: cases=%d digest=%s", cases, got)
	}
}

func TestMatchContextAlreadyStopped(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{canceled, expired} {
		result, err := MatchContext(ctx, nil, nil, Graph{}, ExperimentalRules())
		if err != ctx.Err() || !reflect.DeepEqual(result, Result{}) {
			t.Fatalf("result=%+v err=%v, want %v and no partial result", result, err, ctx.Err())
		}
		topology := &fakeOSMTopology{}
		_, _, err = MatchOSM(ctx, topology, []GeographicObservation{{}}, ExperimentalRules(), DefaultOSMEvaluationOptions())
		if err != ctx.Err() || topology.candidateCalls != 0 || topology.edgeCalls != 0 {
			t.Fatalf("OSM work started with stopped context: err=%v topology=%+v", err, topology)
		}
	}
}

func TestMatchContextStopsDuringViterbi(t *testing.T) {
	graph := Graph{Segments: []DirectedSegment{segment("road", "a", "b", 0, 0, 100, 0, "road", "local")}}
	observations := syntheticObservations([]Point{{20, 0}, {40, 0}}, nil)
	supplied := make([][]Candidate, len(observations))
	for i := range supplied {
		for j := range 256 {
			supplied[i] = append(supplied[i], Candidate{SegmentID: "road", AlongMeters: observations[i].Point.X + float64(j)/1000})
		}
	}
	ctx := newCPUCancelContext(t, ".transitionStates", false, 3)
	result, err := MatchContext(ctx, observations, supplied, graph, ExperimentalRules())
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("decoder returned partial success: result=%+v err=%v", result, err)
	}
}

func TestGraphStopsDuringHeapSearch(t *testing.T) {
	segments := make([]DirectedSegment, 1024)
	for i := range segments {
		segments[i] = segment(fmt.Sprintf("edge-%04d", i), fmt.Sprintf("node-%04d", i), fmt.Sprintf("node-%04d", i+1), float64(i), 0, float64(i+1), 0, "road", "local")
		segments[i].ContinuityClass = "road"
	}
	for _, function := range []string{".compiledGraph.shortest", ".compiledGraph.hasShorterOrdinaryRoadPath"} {
		t.Run(function, func(t *testing.T) {
			ctx := newCPUCancelContext(t, function, false, 4)
			graph := compileGraphContext(ctx, Graph{Segments: segments})
			var ok bool
			if strings.HasSuffix(function, ".shortest") {
				_, _, ok = graph.shortest("node-0000", "node-1024", math.Inf(1))
			} else {
				ok = graph.hasShorterOrdinaryRoadPath("node-0000", "node-1024", math.Inf(1))
			}
			assertCPUStopped(t, ctx)
			if ok {
				t.Fatal("canceled search returned a successful path")
			}
		})
	}
	t.Run("logical-path-filter", func(t *testing.T) {
		ctx := newCPUCancelContext(t, ".compiledGraph.shortest", false, 4)
		graph := compileGraphContext(ctx, Graph{Segments: segments})
		_, ok := graph.routeOnLogicalPath(Candidate{SegmentID: "edge-0000"}, Candidate{SegmentID: "edge-1023", AlongMeters: 1}, "road", math.Inf(1), 1e-6)
		assertCPUStopped(t, ctx)
		if ok {
			t.Fatal("logical-path graph lost cancellation during search")
		}
	})
}

func TestGraphStopsDuringHighDegreeAdjacencyScan(t *testing.T) {
	segments := make([]DirectedSegment, 4096)
	for i := range segments {
		segments[i] = segment(fmt.Sprintf("edge-%04d", i), "root", fmt.Sprintf("leaf-%04d", i), 0, 0, 1, 0, "road", "local")
	}
	ctx := newCPUCancelContext(t, ".compiledGraph.shortest", true, 2)
	graph := compileGraphContext(ctx, Graph{Segments: segments})
	_, _, ok := graph.shortest("root", "unreachable", 100)
	assertCPUStopped(t, ctx)
	if ok {
		t.Fatal("canceled high-degree search returned success")
	}
}

func TestMatchContextStopsDuringRepairPass(t *testing.T) {
	graph := Graph{Segments: []DirectedSegment{segment("road", "a", "b", 0, 0, 100, 0, "road", "local")}}
	var observations []Observation
	var candidates [][]Candidate
	for i := range 256 {
		for j, x := range []float64{20, 40} {
			observations = append(observations, Observation{Point: Point{x, 0}, Time: time.Unix(int64(i*120+j*10), 0)})
			candidates = append(candidates, []Candidate{{SegmentID: "road", AlongMeters: x}})
		}
	}
	ctx := newCPUCancelContext(t, ".repairRawSupportedAccessoryShortcuts", true, 2)
	result, err := MatchContext(ctx, observations, candidates, graph, ExperimentalRules())
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("repair returned partial success: result=%+v err=%v", result, err)
	}
}

func TestRepairStopsDuringQuadraticRawEvidenceScan(t *testing.T) {
	before := segment("before", "a", "b", -20, 0, 0, 0, "road", "local")
	out := segment("out", "b", "c", 0, 0, 20, 0, "driveway", "local")
	back := segment("back", "c", "b", 20, 0, 0, 0, "driveway", "local")
	after := segment("after", "b", "d", 0, 0, 40, 0, "road", "local")
	before.ContinuityClass, after.ContinuityClass = "road", "road"
	out.DrivewayConnector, back.DrivewayConnector = true, true
	out.LengthMeters, back.LengthMeters = 20, 20
	out.PhysicalSegmentID, out.Direction = testPhysicalSegment, SegmentForward
	back.PhysicalID, back.PhysicalSegmentID, back.Direction = out.PhysicalID, out.PhysicalSegmentID, SegmentReverse
	ctx := newCPUCancelContext(t, ".compiledGraph.rawPolylineSupportsDirectedSegmentWithin", true, 2)
	graph := compileGraphContext(ctx, Graph{Segments: []DirectedSegment{before, out, back, after}})
	observations := make([]Observation, 10000)
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: len(observations) - 1, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("out", 0, 20), graph.portion("back", 0, 20), graph.portion("after", 0, 40),
	}}}}
	removeUnsupportedSingleDrivewayOutAndBacks(&result, observations, graph, 12, 30, 45, 15, 1e-6)
	assertCPUStopped(t, ctx)
}

func TestFilteredGraphsPreserveEvaluationContext(t *testing.T) {
	base := segment("road", "a", "b", 0, 0, 100, 0, "road", "local")
	base.ContinuityClass = "road"
	ctx, cancel := context.WithCancel(context.Background())
	graph := compileGraphContext(ctx, Graph{Segments: []DirectedSegment{base}})
	filtered := []compiledGraph{graph.onlyContinuityClass("road"), graph.onlyContinuityClassWithBoundaries("road"), graph.withoutPhysicalPortions(nil)}
	cancel()
	for _, candidate := range filtered {
		if candidate.ctx != ctx || candidate.err() != context.Canceled {
			t.Fatal("filtered graph lost evaluation context")
		}
	}
	if _, ok := graph.routeOnLogicalPath(Candidate{SegmentID: "road"}, Candidate{SegmentID: "road", AlongMeters: 20}, "road", 100, 1e-6); ok {
		t.Fatal("logical-path filtering lost cancellation")
	}
	// A new evaluation using the same source graph must not inherit cancellation.
	if _, _, ok := compileGraph(Graph{Segments: []DirectedSegment{base}}).shortest("a", "b", 100); !ok {
		t.Fatal("cancellation leaked into another evaluation")
	}
}

func TestConcurrentMatchContextsAreIndependent(t *testing.T) {
	fixture := syntheticFixtures()[0]
	observations := syntheticObservations(fixture.points, fixture.times)
	candidates := candidatesNear(observations, fixture.graph)
	want := Match(observations, candidates, fixture.graph, ExperimentalRules())
	ctx := newCPUCancelContext(t, ".transitionStates", false, 1)
	type outcome struct {
		result Result
		err    error
	}
	completed := make(chan outcome, 2)
	for _, evaluationCtx := range []context.Context{ctx, context.Background()} {
		go func() {
			result, err := MatchContext(evaluationCtx, observations, candidates, fixture.graph, ExperimentalRules())
			completed <- outcome{result, err}
		}()
	}
	var canceled, successful int
	for range 2 {
		out := <-completed
		if out.err == context.Canceled {
			canceled++
		} else if out.err == nil && reflect.DeepEqual(out.result, want) {
			successful++
		} else {
			t.Fatalf("unexpected concurrent result: %+v", out)
		}
	}
	assertCPUStopped(t, ctx)
	if canceled != 1 || successful != 1 {
		t.Fatalf("canceled=%d successful=%d", canceled, successful)
	}
}

func TestOSMAttributionStopsDuringCandidatePairs(t *testing.T) {
	candidates := make([]osm.MatcherCandidate, 4096)
	for i := range candidates {
		candidates[i] = osm.MatcherCandidate{Highway: "residential", DistanceMeters: 1, ObservationIndex: i / 16}
	}
	ctx := newCPUCancelContext(t, ".attributionCandidatesContext", true, 40)
	_, _, _, err := attributionCandidatesContext(ctx, MovementFoot, experimentalPathPolicy(MovementFoot), candidates, DefaultRoadGeometryRules())
	assertCPUStopped(t, ctx)
	if err != context.Canceled {
		t.Fatalf("attribution error=%v", err)
	}
}

func TestMatchOSMPropagatesContextIntoDecoder(t *testing.T) {
	ctx := newCPUCancelContext(t, ".transitionStates", false, 1)
	topology := &fakeOSMTopology{}
	input := []GeographicObservation{{Sequence: 1, Longitude: 0.0002}, {Sequence: 2, Longitude: 0.0008}}
	result, stats, err := MatchOSM(ctx, topology, input, ExperimentalRules(), DefaultOSMEvaluationOptions())
	assertCPUStopped(t, ctx)
	if err != context.Canceled || !reflect.DeepEqual(result, Result{}) || stats.CandidateCount != 2 || topology.edgeCalls == 0 {
		t.Fatalf("OSM context not propagated into CPU work: result=%+v stats=%+v err=%v", result, stats, err)
	}
}
