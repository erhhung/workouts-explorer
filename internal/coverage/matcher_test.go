package coverage

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

type syntheticFixture struct {
	name       string
	graph      Graph
	points     []Point
	times      []float64
	candidates func([]Observation, Graph) [][]Candidate
	expected   []string
	cross      []string
	turns      []ExpectedTurn
	split      SplitReason
}

func TestSyntheticMatcherEvaluation(t *testing.T) {
	fixtures := syntheticFixtures()
	evaluations := make([]EvaluationCase, 0, len(fixtures))
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			observations := syntheticObservations(fixture.points, fixture.times)
			candidates := candidatesNear(observations, fixture.graph)
			if fixture.candidates != nil {
				candidates = fixture.candidates(observations, fixture.graph)
			}
			started := time.Now()
			result := Match(observations, candidates, fixture.graph, ExperimentalRules())
			runtime := time.Since(started)
			if got := attributedSegments(result); !reflect.DeepEqual(got, stringSet(fixture.expected)) {
				t.Fatalf("segments=%v, want %v; result=%+v", got, fixture.expected, result)
			}
			if fixture.split != "" && !containsSplit(result.Splits, fixture.split) {
				t.Fatalf("splits=%v, want %q", result.Splits, fixture.split)
			}
			for _, traversal := range result.Traversals {
				for _, portion := range traversal.Portions {
					if portion.ToMeter-portion.FromMeter <= ExperimentalRules().PositiveLengthEpsilonMeters {
						t.Fatalf("non-positive portion: %+v", portion)
					}
				}
			}
			evaluations = append(evaluations, EvaluationCase{
				Name: fixture.name, Result: result, ExpectedSegmentIDs: fixture.expected,
				CrossStreetSegmentIDs: fixture.cross, ExpectedTurns: fixture.turns, Runtime: runtime,
			})
		})
	}
	metrics := Evaluate(evaluations)
	if metrics.SegmentPrecision != 1 || metrics.SegmentRecall != 1 || metrics.FalseCrossStreetAttribution != 0 || metrics.MissedTurns != 0 {
		t.Fatalf("evaluation metrics=%+v", metrics)
	}
	if metrics.UnmatchedObservations == 0 || metrics.RejectedObservations == 0 {
		t.Fatalf("evaluation corpus did not exercise unmatched and rejected observations: %+v", metrics)
	}
	t.Logf("synthetic metrics: precision=%.3f recall=%.3f false_cross_street=%d missed_turns=%d unmatched=%d ambiguous=%d rejected=%d matcher_runtime=%s",
		metrics.SegmentPrecision, metrics.SegmentRecall, metrics.FalseCrossStreetAttribution, metrics.MissedTurns,
		metrics.UnmatchedObservations, metrics.AmbiguousObservations, metrics.RejectedObservations, metrics.Runtime)
}

func TestCandidateOrderDoesNotAffectResult(t *testing.T) {
	fixture := syntheticFixtures()[0]
	observations := syntheticObservations(fixture.points, fixture.times)
	candidates := candidatesNear(observations, fixture.graph)
	want := Match(observations, candidates, fixture.graph, ExperimentalRules())
	shuffled := cloneCandidates(candidates)
	random := rand.New(rand.NewSource(7301))
	for i := range shuffled {
		random.Shuffle(len(shuffled[i]), func(a, b int) { shuffled[i][a], shuffled[i][b] = shuffled[i][b], shuffled[i][a] })
	}
	got := Match(observations, shuffled, fixture.graph, ExperimentalRules())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shuffled candidates changed result\n got: %+v\nwant: %+v", got, want)
	}
}

func TestReliableHeadingIsOptionalEvidence(t *testing.T) {
	east := segment("east", "a", "b", 0, 0, 100, 0, "road", "local")
	west := segment("west", "b", "a", 100, 0, 0, 0, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{east, west}})
	heading, reliable, unreliable := 90.0, 5.0, 60.0
	observations := []Observation{
		{Point: Point{20, 0}, AccuracyMeters: 5, HeadingDegrees: &heading, HeadingAccuracy: &reliable},
		{Point: Point{40, 0}, AccuracyMeters: 5},
	}
	eastCandidate := Candidate{SegmentID: "east", AlongMeters: 20}
	westCandidate := Candidate{SegmentID: "west", AlongMeters: 80}
	eastCost := emissionCost(0, observations, eastCandidate, graph, ExperimentalRules())
	westCost := emissionCost(0, observations, westCandidate, graph, ExperimentalRules())
	if eastCost >= westCost {
		t.Fatalf("reliable east heading costs east=%f west=%f", eastCost, westCost)
	}
	observations[0].HeadingAccuracy = &unreliable
	eastCost = emissionCost(0, observations, eastCandidate, graph, ExperimentalRules())
	westCost = emissionCost(0, observations, westCandidate, graph, ExperimentalRules())
	if eastCost >= westCost {
		t.Fatalf("adjacent motion did not replace unreliable heading: east=%f west=%f", eastCost, westCost)
	}
	observations[1].Point = observations[0].Point
	if got := emissionCost(0, observations, westCandidate, graph, ExperimentalRules()); got != 0 {
		t.Fatalf("stationary unreliable heading contributed cost %f", got)
	}
}

func TestSuppliedCandidateBoundsAndPointProximityInvariant(t *testing.T) {
	graph := Graph{Segments: []DirectedSegment{segment("road", "a", "b", 0, 0, 100, 0, "road", "local")}}
	observations := syntheticObservations([]Point{{20, 0}}, nil)
	candidates := [][]Candidate{{
		{SegmentID: "road", Projected: Point{20, 0}, AlongMeters: 20, DistanceMeters: 0},
		{SegmentID: "road", Projected: Point{20, 0}, AlongMeters: 20, DistanceMeters: 30},
		{SegmentID: "missing", Projected: Point{20, 0}, AlongMeters: 20, DistanceMeters: 0},
	}}
	result := Match(observations, candidates, graph, ExperimentalRules())
	if len(result.Traversals) != 0 || result.Observations[0].Status != ObservationRejected {
		t.Fatalf("a proximity-only point produced coverage: %+v", result)
	}
}

func TestTransitionOnlyEdgeConnectsStatesWithoutProducingCoverage(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	crossing := segment("crossing", "b", "c", 10, 0, 20, 0, "crossing", "local")
	crossing.TransitionOnly = true
	second := segment("second", "c", "d", 20, 0, 30, 0, "second", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, crossing, second}})
	route, ok := graph.route(
		Candidate{SegmentID: "first", AlongMeters: 5},
		Candidate{SegmentID: "second", AlongMeters: 5},
		100, 1e-6,
	)
	if !ok || route.distance != 20 || len(route.portions) != 2 ||
		route.portions[0].SegmentID != "first" || route.portions[1].SegmentID != "second" {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
}

func TestShortSelectedCrossingConnectorProducesContinuousCoverage(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	crossing := segment("crossing", "b", "c", 10, 0, 14, 0, "crossing", "local")
	crossing.TransitionOnly, crossing.ContinuityConnector = true, true
	second := segment("second", "c", "d", 14, 0, 24, 0, "second", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, crossing, second}, MaxContinuityConnectorMeters: 10})
	route, ok := graph.route(
		Candidate{SegmentID: "first", AlongMeters: 5},
		Candidate{SegmentID: "second", AlongMeters: 5},
		100, 1e-6,
	)
	if !ok || route.distance != 14 || len(route.portions) != 3 || route.portions[1].SegmentID != "crossing" {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
}

func TestRawSupportedAccessoryRunConnectsPedestrianPath(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 10, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass = 10, "path"
	sidewalk := segment("sidewalk", "b", "c", 10, 0, 21.4, 0, "sidewalk", "local")
	sidewalk.LengthMeters, sidewalk.TransitionOnly, sidewalk.AccessoryConnector = 11.4, true, true
	crossing := segment("crossing", "c", "d", 21.4, 0, 30, 0, "crossing", "local")
	crossing.LengthMeters, crossing.TransitionOnly, crossing.ContinuityConnector, crossing.AccessoryConnector = 8.6, true, true, true
	road := segment("road", "d", "e", 30, 0, 40, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass = 10, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{path, sidewalk, crossing, road},
		MaxContinuityConnectorMeters: 10, MaxAccessoryConnectorMeters: 25})
	route, ok := graph.route(Candidate{SegmentID: "path", AlongMeters: 5}, Candidate{SegmentID: "road", AlongMeters: 5}, 100, 1e-6)
	if !ok || len(route.portions) != 4 {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
	observations := []Observation{{Point: Point{5, 0}}, {Point: Point{11, 0}}, {Point: Point{20, 0}}, {Point: Point{29, 0}}, {Point: Point{35, 0}}}
	if !accessoryConnectorsSupported(route.portions, graph, observations, 4, 30) {
		t.Fatal("raw-supported accessory run was rejected")
	}
	for i := range observations {
		observations[i].Point.Y = float64(i * 5)
		observations[i].Point.X = 10
	}
	if accessoryConnectorsSupported(route.portions, graph, observations, 4, 30) {
		t.Fatal("unsupported accessory run was accepted")
	}
}

func TestAccessoryRunUsesCandidateClassesAtExactBoundaries(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 10, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass = 10, "path"
	sidewalk := segment("sidewalk", "b", "c", 10, 0, 21.4, 0, "sidewalk", "local")
	sidewalk.LengthMeters, sidewalk.TransitionOnly, sidewalk.AccessoryConnector = 11.4, true, true
	crossing := segment("crossing", "c", "d", 21.4, 0, 30, 0, "crossing", "local")
	crossing.LengthMeters, crossing.TransitionOnly, crossing.ContinuityConnector, crossing.AccessoryConnector = 8.6, true, true, true
	road := segment("road", "d", "e", 30, 0, 40, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass = 10, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{path, sidewalk, crossing, road},
		MaxContinuityConnectorMeters: 10, MaxAccessoryConnectorMeters: 25})
	route, ok := graph.route(
		Candidate{SegmentID: "path", AlongMeters: 10, ContinuityClass: "path"},
		Candidate{SegmentID: "road", AlongMeters: 0, ContinuityClass: "road"}, 100, 1e-6,
	)
	if !ok || len(route.portions) != 2 || !containsAccessoryConnector(route.portions, graph) {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
}

func TestAccessoryRunRequiresPedestrianPathBoundaryAndLengthCap(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	first.LengthMeters, first.ContinuityClass = 10, "road"
	sidewalk := segment("sidewalk", "b", "c", 10, 0, 30, 0, "sidewalk", "local")
	sidewalk.LengthMeters, sidewalk.TransitionOnly, sidewalk.AccessoryConnector = 20, true, true
	second := segment("second", "c", "d", 30, 0, 40, 0, "second", "local")
	second.LengthMeters, second.ContinuityClass = 10, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, sidewalk, second}, MaxAccessoryConnectorMeters: 25})
	route, ok := graph.route(Candidate{SegmentID: "first", AlongMeters: 5}, Candidate{SegmentID: "second", AlongMeters: 5}, 100, 1e-6)
	if !ok || len(route.portions) != 2 {
		t.Fatalf("road-only accessory route=%+v ok=%t", route, ok)
	}
	first.ContinuityClass, sidewalk.LengthMeters, sidewalk.To = "path", 26, Point{36, 0}
	second.From = Point{36, 0}
	graph = compileGraph(Graph{Segments: []DirectedSegment{first, sidewalk, second}, MaxAccessoryConnectorMeters: 25})
	route, ok = graph.route(Candidate{SegmentID: "first", AlongMeters: 5}, Candidate{SegmentID: "second", AlongMeters: 5}, 100, 1e-6)
	if !ok || len(route.portions) != 2 {
		t.Fatalf("over-limit accessory route=%+v ok=%t", route, ok)
	}
}

func TestRawSupportedAccessoryRunBridgesAdjacentTraversals(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	first.LengthMeters, first.ContinuityClass, first.PhysicalSegmentID = 10, "path", uuid.New()
	sidewalk := segment("sidewalk", "b", "c", 10, 0, 21.4, 0, "sidewalk", "local")
	sidewalk.LengthMeters, sidewalk.TransitionOnly, sidewalk.AccessoryConnector = 11.4, true, true
	crossing := segment("crossing", "c", "d", 21.4, 0, 30, 0, "crossing", "local")
	crossing.LengthMeters, crossing.TransitionOnly, crossing.ContinuityConnector, crossing.AccessoryConnector = 8.6, true, true, true
	second := segment("second", "d", "e", 30, 0, 40, 0, "second", "local")
	second.LengthMeters, second.ContinuityClass, second.PhysicalSegmentID = 10, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, sidewalk, crossing, second},
		MaxContinuityConnectorMeters: 10, MaxAccessoryConnectorMeters: 25})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 0, Portions: []TraversedPortion{graph.portion("first", 0, 10)}},
		{FirstObservation: 4, LastObservation: 4, Portions: []TraversedPortion{graph.portion("second", 0, 10)}},
	}}
	observations := []Observation{{Point: Point{10, 0}}, {Point: Point{11, 0}}, {Point: Point{20, 0}}, {Point: Point{29, 0}}, {Point: Point{30, 0}}}
	bridgePlausibleAccessoryTraversals(&result, observations, graph, 25, 32, 1e-6)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 4 || !containsAccessoryConnector(result.Traversals[0].Portions, graph) {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedAccessoryShortcutReplacesRoadDetour(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 5, 0, "before", "local")
	before.LengthMeters, before.TransitionOnly, before.AccessoryConnector, before.PhysicalSegmentID = 5, true, true, uuid.New()
	roadOne := segment("road-1", "b", "c", 5, 0, 5, 10, "road-1", "local")
	roadOne.LengthMeters, roadOne.ContinuityClass = 10, "road"
	roadTwo := segment("road-2", "c", "d", 5, 10, 15, 10, "road-2", "local")
	roadTwo.LengthMeters, roadTwo.ContinuityClass = 10, "road"
	roadThree := segment("road-3", "d", "e", 15, 10, 15, 0, "road-3", "local")
	roadThree.LengthMeters, roadThree.ContinuityClass = 10, "road"
	direct := segment("direct", "b", "e", 5, 0, 15, 0, "direct", "local")
	direct.LengthMeters, direct.TransitionOnly, direct.ContinuityConnector, direct.AccessoryConnector = 10, true, true, true
	after := segment("after", "e", "f", 15, 0, 20, 0, "after", "local")
	after.LengthMeters, after.TransitionOnly, after.AccessoryConnector, after.PhysicalSegmentID = 5, true, true, uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, roadOne, roadTwo, roadThree, direct, after},
		MaxContinuityConnectorMeters: 10, MaxAccessoryConnectorMeters: 25})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{
		graph.portion("before", 0, 5), graph.portion("road-1", 0, 10), graph.portion("road-2", 0, 10),
		graph.portion("road-3", 0, 10), graph.portion("after", 0, 5),
	}}}}
	directRaw := []Observation{{Point: Point{5, 0}}, {Point: Point{8, 0}}, {Point: Point{12, 0}}, {Point: Point{15, 0}}}
	repairRawSupportedAccessoryShortcuts(&result, directRaw, graph, 50, 25, 0.5, 10, 1e-6)
	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "direct" {
		t.Fatalf("direct result=%+v", result)
	}

	result.Traversals[0].Portions = []TraversedPortion{
		graph.portion("before", 0, 5), graph.portion("road-1", 0, 10), graph.portion("road-2", 0, 10),
		graph.portion("road-3", 0, 10), graph.portion("after", 0, 5),
	}
	detourRaw := []Observation{{Point: Point{5, 0}}, {Point: Point{5, 10}}, {Point: Point{15, 10}}, {Point: Point{15, 0}}}
	repairRawSupportedAccessoryShortcuts(&result, detourRaw, graph, 50, 25, 0.5, 10, 1e-6)
	if len(result.Traversals[0].Portions) != 5 {
		t.Fatalf("unsupported shortcut result=%+v", result)
	}
}

func TestRawSupportedAccessoryShortcutMayUseRoadBoundaries(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 5, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 5, "road", uuid.New()
	detour := segment("detour", "b", "c", 5, 0, 5, 20, "detour", "local")
	detour.LengthMeters, detour.ContinuityClass = 20, "road"
	direct := segment("direct", "b", "d", 5, 0, 15, 0, "direct", "local")
	direct.LengthMeters, direct.TransitionOnly, direct.ContinuityConnector, direct.AccessoryConnector = 10, true, true, true
	after := segment("after", "d", "c", 15, 0, 5, 20, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 22.36, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, detour, direct, after},
		MaxContinuityConnectorMeters: 10, MaxAccessoryConnectorMeters: 25})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{
		graph.portion("before", 0, 5), graph.portion("detour", 0, 20), graph.portion("after", 0, 22.36),
	}}}}
	raw := []Observation{{Point: Point{5, 0}}, {Point: Point{8, 0}}, {Point: Point{12, 0}}, {Point: Point{15, 0}}}
	repairRawSupportedAccessoryShortcuts(&result, raw, graph, 50, 25, 0.5, 10, 1e-6)
	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "direct" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedRoadShortcutReplacesLongerDetour(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 5, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 5, "road", uuid.New()
	detourOne := segment("detour-1", "b", "c", 5, 0, 5, 20, "detour-1", "local")
	detourOne.LengthMeters, detourOne.ContinuityClass, detourOne.RoadConnector = 20, "road", true
	detourTwo := segment("detour-2", "c", "d", 5, 20, 30, 20, "detour-2", "local")
	detourTwo.LengthMeters, detourTwo.ContinuityClass = 25, "road"
	direct := segment("direct", "b", "d", 5, 0, 30, 0, "direct", "local")
	direct.LengthMeters, direct.ContinuityClass = 25, "road"
	after := segment("after", "d", "e", 30, 0, 35, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 5, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, detourOne, detourTwo, direct, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 5), graph.portion("detour-1", 0, 20), graph.portion("detour-2", 0, 25), graph.portion("after", 0, 5),
	}}}}
	raw := []Observation{{Point: Point{5, 0}}, {Point: Point{10, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}, {Point: Point{35, 0}}}
	repairRawSupportedRoadShortcuts(&result, raw, graph, 80, 60, 0.85, 10, 20, 1e-6)
	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "direct" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedRoadShortcutRepairsDisconnectedOrdinaryTurn(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 30, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass, before.LogicalPathID = 30, "road", "fremont"
	direct := segment("direct", "b", "c", 30, 0, 41, 0, "direct", "local")
	direct.LengthMeters, direct.ContinuityClass, direct.LogicalPathID = 11, "road", "fremont"
	spur := segment("spur", "d", "c", 41, -7, 41, 0, "spur", "local")
	spur.LengthMeters, spur.ContinuityClass, spur.LogicalPathID = 7, "road", "bernardo"
	after := segment("after", "c", "e", 41, 0, 41, 18, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.LogicalPathID = 18, "road", "bernardo"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, direct, spur, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{
		graph.portion("before", 0, 30), graph.portion("spur", 0, 7), graph.portion("after", 0, 18),
	}}}}
	raw := []Observation{{Point: Point{0, -6}}, {Point: Point{20, -6}}, {Point: Point{29, -6}}, {Point: Point{30, 0}}, {Point: Point{30, 8}}, {Point: Point{30, 18}}}

	repairRawSupportedRoadShortcuts(&result, raw, graph, 80, 60, 0.85, 10, 20, 1e-6)

	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "direct" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRoadShortcutKeepsDisconnectedTurnWithoutDirectRawSupport(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 30, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass, before.LogicalPathID = 30, "road", "fremont"
	direct := segment("direct", "b", "c", 30, 0, 41, 0, "direct", "local")
	direct.LengthMeters, direct.ContinuityClass, direct.LogicalPathID = 11, "road", "fremont"
	spur := segment("spur", "d", "c", 41, -7, 41, 0, "spur", "local")
	spur.LengthMeters, spur.ContinuityClass, spur.LogicalPathID = 7, "road", "bernardo"
	after := segment("after", "c", "e", 41, 0, 41, 18, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.LogicalPathID = 18, "road", "bernardo"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, direct, spur, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{
		graph.portion("before", 0, 30), graph.portion("spur", 0, 7), graph.portion("after", 0, 18),
	}}}}
	raw := []Observation{{Point: Point{41, -12}}, {Point: Point{41, -7}}, {Point: Point{41, 5}}, {Point: Point{41, 15}}}

	repairRawSupportedRoadShortcuts(&result, raw, graph, 80, 60, 0.85, 10, 20, 1e-6)

	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "spur" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRoadShortcutKeepsDisconnectedOutAndBackSpur(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 30, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass, before.LogicalPathID = 30, "road", "fremont"
	direct := segment("direct", "b", "c", 30, 0, 41, 0, "direct", "local")
	direct.LengthMeters, direct.ContinuityClass, direct.LogicalPathID = 11, "road", "fremont"
	spur := segment("spur", "d", "c", 41, -7, 41, 0, "spur", "local")
	spur.LengthMeters, spur.ContinuityClass, spur.LogicalPathID, spur.PhysicalID = 7, "road", "bernardo", "spur"
	spurBack := segment("spur-back", "c", "d", 41, 0, 41, -7, "spur", "local")
	spurBack.LengthMeters, spurBack.ContinuityClass, spurBack.LogicalPathID, spurBack.PhysicalID, spurBack.Direction = 7, "road", "bernardo", "spur", SegmentReverse
	after := segment("after", "c", "e", 41, 0, 41, 18, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.LogicalPathID = 18, "road", "bernardo"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, direct, spur, spurBack, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{
		graph.portion("before", 0, 30), graph.portion("spur", 0, 7), graph.portion("after", 0, 18), graph.portion("spur-back", 0, 7),
	}}}}
	raw := []Observation{{Point: Point{0, -6}}, {Point: Point{20, -6}}, {Point: Point{29, -6}}, {Point: Point{30, 0}}, {Point: Point{30, 8}}, {Point: Point{30, 18}}}

	repairRawSupportedRoadShortcuts(&result, raw, graph, 80, 60, 0.85, 10, 20, 1e-6)

	if result.Traversals[0].Portions[1].SegmentID != "spur" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRoadContinuityBridgesNetworkGapOnBoundaryPath(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 5, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass, before.LogicalPathID, before.PhysicalSegmentID = 5, "road", "main", uuid.New()
	badTail := segment("bad-tail", "b", "x", 5, 0, 10, 0, "bad-tail", "local")
	badTail.LengthMeters, badTail.ContinuityClass, badTail.RoadConnector = 15, "road", true
	directOne := segment("direct-1", "b", "c", 5, 0, 25, 0, "direct-1", "local")
	directOne.LengthMeters, directOne.ContinuityClass, directOne.LogicalPathID = 20, "road", "main"
	directTwo := segment("direct-2", "c", "d", 25, 0, 45, 0, "direct-2", "local")
	directTwo.LengthMeters, directTwo.ContinuityClass, directTwo.LogicalPathID = 20, "road", "main"
	badHead := segment("bad-head", "y", "d", 40, 5, 45, 0, "bad-head", "local")
	badHead.LengthMeters, badHead.ContinuityClass = 10, "road"
	after := segment("after", "d", "e", 45, 0, 50, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 5, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, badTail, directOne, directTwo, badHead, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 5), graph.portion("bad-tail", 0, 15)}},
		{FirstObservation: 2, LastObservation: 4, Portions: []TraversedPortion{graph.portion("bad-head", 0, 10), graph.portion("after", 0, 5)}},
	}}
	raw := []Observation{{Point: Point{5, 0}}, {Point: Point{15, 0}}, {Point: Point{25, 0}}, {Point: Point{35, 0}}, {Point: Point{45, 0}}}
	bridgeRawSupportedRoadContinuity(&result, raw, graph, 80, 60, 32, 0.85, 10, 20, 1e-6)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 4 ||
		result.Traversals[0].Portions[1].SegmentID != "direct-1" || result.Traversals[0].Portions[2].SegmentID != "direct-2" {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnsupportedMultiEdgeRoadOutAndBackIsRemoved(t *testing.T) {
	stem := segment("stem-f", "a", "b", 0, 0, 10, 0, "stem", "local")
	stem.LengthMeters, stem.ContinuityClass, stem.PhysicalID, stem.PhysicalSegmentID = 10, "road", "stem", uuid.New()
	terminal := segment("terminal-f", "b", "c", 10, 0, 30, 0, "terminal", "local")
	terminal.LengthMeters, terminal.ContinuityClass, terminal.PhysicalID, terminal.PhysicalSegmentID = 20, "road", "terminal", uuid.New()
	terminalReverse := segment("terminal-r", "c", "b", 30, 0, 10, 0, "terminal", "local")
	terminalReverse.LengthMeters, terminalReverse.ContinuityClass, terminalReverse.PhysicalID, terminalReverse.PhysicalSegmentID, terminalReverse.Direction = 20, "road", "terminal", terminal.PhysicalSegmentID, SegmentReverse
	stemReverse := segment("stem-r", "b", "a", 10, 0, 0, 0, "stem", "local")
	stemReverse.LengthMeters, stemReverse.ContinuityClass, stemReverse.PhysicalID, stemReverse.PhysicalSegmentID, stemReverse.Direction = 10, "road", "stem", stem.PhysicalSegmentID, SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{stem, terminal, terminalReverse, stemReverse}})
	portions := []TraversedPortion{
		graph.portion("stem-f", 0, 10), graph.portion("terminal-f", 0, 20),
		graph.portion("terminal-r", 0, 20), graph.portion("stem-r", 0, 10),
	}
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 2, Portions: portions}}}
	unsupported := []Observation{{Point: Point{0, 20}}, {Point: Point{10, 20}}, {Point: Point{20, 20}}}
	removeUnsupportedRoadOutAndBacks(&result, unsupported, graph, 100, 15, 1e-6)
	if len(result.Traversals[0].Portions) != 0 {
		t.Fatalf("unsupported excursion retained: %+v", result)
	}

	result.Traversals[0].Portions = portions
	supported := []Observation{{Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{30, 0}}, {Point: Point{10, 0}}, {Point: Point{0, 0}}}
	result.Traversals[0].LastObservation = len(supported) - 1
	removeUnsupportedRoadOutAndBacks(&result, supported, graph, 100, 15, 1e-6)
	if len(result.Traversals[0].Portions) != 4 {
		t.Fatalf("supported excursion removed: %+v", result)
	}
}

func TestUnsupportedSingleRoadTurnStubIsRemoved(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "whisman", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-r", "junction", "tip", 0, 0, -10, 10, "dana", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.Direction = 14, "road", "stub", physical, SegmentReverse
	inbound := segment("stub-f", "tip", "junction", -10, 10, 0, 0, "dana", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalID, inbound.PhysicalSegmentID = 14, "road", "stub", physical
	after := segment("after", "junction", "b", 0, 0, 10, -20, "dana", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 22, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("stub-r", 0, 14), graph.portion("stub-f", 0, 14), graph.portion("after", 0, 22),
	}}}}
	raw := []Observation{{Point: Point{-15, 12}}, {Point: Point{-10, 10}}, {Point: Point{-5, 5}}, {Point: Point{0, 0}}, {Point: Point{8, -16}}}

	removeUnsupportedSingleRoadTurnStubs(&result, raw, graph, 30, 45, 15, 1e-6)

	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[0].SegmentID != "before" || result.Traversals[0].Portions[1].SegmentID != "after" {
		t.Fatalf("result=%+v", result)
	}
}

func TestSupportedSingleRoadOutAndBackAtTurnIsRetained(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "whisman", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-r", "junction", "tip", 0, 0, -10, 10, "dana", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.Direction = 14, "road", "stub", physical, SegmentReverse
	inbound := segment("stub-f", "tip", "junction", -10, 10, 0, 0, "dana", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalID, inbound.PhysicalSegmentID = 14, "road", "stub", physical
	after := segment("after", "junction", "b", 0, 0, 10, -20, "dana", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 22, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}})
	portions := []TraversedPortion{graph.portion("before", 0, 20), graph.portion("stub-r", 0, 14), graph.portion("stub-f", 0, 14), graph.portion("after", 0, 22)}
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: portions}}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{-10, 10}}, {Point: Point{0, 0}}, {Point: Point{5, -10}}, {Point: Point{10, -20}}}

	removeUnsupportedSingleRoadTurnStubs(&result, raw, graph, 30, 45, 15, 1e-6)

	if len(result.Traversals[0].Portions) != len(portions) {
		t.Fatalf("supported out-and-back removed: %+v", result)
	}
}

func TestUnsupportedSingleRoadStubDuringSameRoadTravelIsRemoved(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-r", "junction", "tip", 0, 0, 0, 10, "side", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalSegmentID, outbound.Direction = 10, "road", physical, SegmentReverse
	inbound := segment("stub-f", "tip", "junction", 0, 10, 0, 0, "side", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalSegmentID = 10, "road", physical
	continuation := segment("stub-next", "tip", "elsewhere", 0, 10, 0, 30, "side", "local")
	continuation.LengthMeters, continuation.ContinuityClass, continuation.PhysicalSegmentID = 20, "road", uuid.New()
	after := segment("after", "junction", "b", 0, 0, 20, 0, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 20, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, continuation, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("stub-r", 0, 10), graph.portion("stub-f", 0, 10), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{-5, 0}}, {Point: Point{5, 0}}, {Point: Point{15, 0}}}

	removeUnsupportedSingleRoadTurnStubs(&result, raw, graph, 30, 45, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnsupportedSingleDeadEndStubDuringSameRoadTravelIsRemoved(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-f", "junction", "tip", 0, 0, 20, 0, "side", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.RoadConnector = 20, "road", "stub", physical, true
	inbound := segment("stub-r", "tip", "junction", 20, 0, 0, 0, "side", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalID, inbound.PhysicalSegmentID, inbound.Direction, inbound.RoadConnector = 20, "road", "stub", physical, SegmentReverse, true
	after := segment("after", "junction", "b", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 20, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("stub-f", 0, 20), graph.portion("stub-r", 0, 20), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{0, -15}}, {Point: Point{0, -5}}, {Point: Point{0, 5}}, {Point: Point{0, 15}}}

	removeUnsupportedSingleRoadTurnStubs(&result, raw, graph, 30, 45, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnsupportedOrdinaryDeadEndStubDuringSameRoadTravelIsRemoved(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "south", "junction", 0, -20, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-f", "junction", "tip", 0, 0, 25, 0, "side", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalID, outbound.PhysicalSegmentID = 25, "road", "stub", physical
	inbound := segment("stub-r", "tip", "junction", 25, 0, 0, 0, "side", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalID, inbound.PhysicalSegmentID, inbound.Direction = 25, "road", "stub", physical, SegmentReverse
	after := segment("after", "junction", "north", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 20, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("stub-f", 0, 25), graph.portion("stub-r", 0, 25), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{0, -15}}, {Point: Point{0, -5}}, {Point: Point{0, 5}}, {Point: Point{0, 10}}, {Point: Point{0, 18}}}

	removeUnsupportedSingleRoadTurnStubs(&result, raw, graph, 30, 45, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestEndpointSupportedOrdinaryDeadEndStubIsRetained(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "south", "junction", 0, -20, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-f", "junction", "tip", 0, 0, 25, 0, "side", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalID, outbound.PhysicalSegmentID = 25, "road", "stub", physical
	inbound := segment("stub-r", "tip", "junction", 25, 0, 0, 0, "side", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalID, inbound.PhysicalSegmentID, inbound.Direction = 25, "road", "stub", physical, SegmentReverse
	after := segment("after", "junction", "north", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 20, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}})
	portions := []TraversedPortion{graph.portion("before", 0, 20), graph.portion("stub-f", 0, 25), graph.portion("stub-r", 0, 25), graph.portion("after", 0, 20)}
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: portions}}}
	raw := []Observation{{Point: Point{0, -10}}, {Point: Point{0, 0}}, {Point: Point{25, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 10}}}

	removeUnsupportedSingleRoadTurnStubs(&result, raw, graph, 30, 45, 5, 1e-6)

	if len(result.Traversals[0].Portions) != len(portions) {
		t.Fatalf("supported dead end removed: %+v", result)
	}
}

func TestPerpendicularSingleDrivewayOutAndBackIsRemoved(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "south", "junction", 0, -20, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("driveway-r", "junction", "opposite", 0, 0, -16, 0, "driveway", "local")
	outbound.LengthMeters, outbound.TransitionOnly, outbound.DrivewayConnector, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.Direction = 16, true, true, "driveway", physical, SegmentReverse
	inbound := segment("driveway-f", "opposite", "junction", -16, 0, 0, 0, "driveway", "local")
	inbound.LengthMeters, inbound.TransitionOnly, inbound.DrivewayConnector, inbound.PhysicalID, inbound.PhysicalSegmentID = 16, true, true, "driveway", physical
	after := segment("after", "junction", "north", 0, 0, 0, 80, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 80, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("driveway-r", 0, 16), graph.portion("driveway-f", 0, 16), graph.portion("after", 0, 80),
	}}}}
	raw := []Observation{{Point: Point{8, -15}}, {Point: Point{8, -5}}, {Point: Point{8, 5}}, {Point: Point{8, 30}}, {Point: Point{8, 70}}}

	removeUnsupportedSingleDrivewayOutAndBacks(&result, raw, graph, 12, 30, 45, 15, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDirectionallySupportedSingleDrivewayOutAndBackIsRetained(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "south", "junction", 0, -20, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("driveway-r", "junction", "tip", 0, 0, -16, 0, "driveway", "local")
	outbound.LengthMeters, outbound.TransitionOnly, outbound.DrivewayConnector, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.Direction = 16, true, true, "driveway", physical, SegmentReverse
	inbound := segment("driveway-f", "tip", "junction", -16, 0, 0, 0, "driveway", "local")
	inbound.LengthMeters, inbound.TransitionOnly, inbound.DrivewayConnector, inbound.PhysicalID, inbound.PhysicalSegmentID = 16, true, true, "driveway", physical
	after := segment("after", "junction", "north", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 20, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}, MaxDrivewayConnectorMeters: 40})
	portions := []TraversedPortion{graph.portion("before", 0, 20), graph.portion("driveway-r", 0, 16), graph.portion("driveway-f", 0, 16), graph.portion("after", 0, 20)}
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: portions}}}
	raw := []Observation{{Point: Point{0, -15}}, {Point: Point{0, 0}}, {Point: Point{-16, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 5}}, {Point: Point{0, 15}}}

	removeUnsupportedSingleDrivewayOutAndBacks(&result, raw, graph, 12, 30, 45, 15, 1e-6)

	if len(result.Traversals[0].Portions) != len(portions) {
		t.Fatalf("supported driveway removed: %+v", result)
	}
}

func TestShortUnsupportedSingleDrivewayOutAndBackIsRetained(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "south", "junction", 0, -20, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("driveway-r", "junction", "tip", 0, 0, -8, 0, "driveway", "local")
	outbound.LengthMeters, outbound.TransitionOnly, outbound.DrivewayConnector, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.Direction = 8, true, true, "driveway", physical, SegmentReverse
	inbound := segment("driveway-f", "tip", "junction", -8, 0, 0, 0, "driveway", "local")
	inbound.LengthMeters, inbound.TransitionOnly, inbound.DrivewayConnector, inbound.PhysicalID, inbound.PhysicalSegmentID = 8, true, true, "driveway", physical
	after := segment("after", "junction", "north", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 20, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, after}, MaxDrivewayConnectorMeters: 40})
	portions := []TraversedPortion{graph.portion("before", 0, 20), graph.portion("driveway-r", 0, 8), graph.portion("driveway-f", 0, 8), graph.portion("after", 0, 20)}
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: portions}}}
	raw := []Observation{{Point: Point{8, -15}}, {Point: Point{8, -5}}, {Point: Point{8, 5}}, {Point: Point{8, 15}}}

	removeUnsupportedSingleDrivewayOutAndBacks(&result, raw, graph, 12, 30, 45, 15, 1e-6)

	if len(result.Traversals[0].Portions) != len(portions) {
		t.Fatalf("short driveway removed: %+v", result)
	}
}

func TestUnsupportedIsolatedSingleRoadTraversalIsRemoved(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 20, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	middle := segment("middle", "x", "y", 30, 20, 45, 20, "middle", "local")
	middle.LengthMeters, middle.ContinuityClass = 15, "road"
	after := segment("after", "c", "d", 50, 0, 70, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass = 20, "path"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, middle, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 20)}},
		{FirstObservation: 2, LastObservation: 2, Portions: []TraversedPortion{graph.portion("middle", 0, 15)}},
		{FirstObservation: 3, LastObservation: 4, Portions: []TraversedPortion{graph.portion("after", 0, 20)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{35, 0}}, {Point: Point{50, 0}}, {Point: Point{70, 0}}}

	removeUnsupportedIsolatedSingleRoadPortions(&result, raw, graph, 20, 45, 5, 128, 1e-6)

	if len(result.Traversals) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDirectionallySupportedIsolatedRoadTraversalIsRetained(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 20, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	middle := segment("middle", "x", "y", 30, 20, 45, 20, "middle", "local")
	middle.LengthMeters, middle.ContinuityClass = 15, "road"
	after := segment("after", "c", "d", 50, 0, 70, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, middle, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 20)}},
		{FirstObservation: 2, LastObservation: 3, Portions: []TraversedPortion{graph.portion("middle", 0, 15)}},
		{FirstObservation: 4, LastObservation: 5, Portions: []TraversedPortion{graph.portion("after", 0, 20)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 20}}, {Point: Point{45, 20}}, {Point: Point{50, 0}}, {Point: Point{70, 0}}}

	removeUnsupportedIsolatedSingleRoadPortions(&result, raw, graph, 20, 45, 5, 128, 1e-6)

	if len(result.Traversals) != 3 {
		t.Fatalf("supported isolated traversal removed: %+v", result)
	}
}

func TestUnsupportedDisconnectedRoadAtWindowEdgeIsRemoved(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 20, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	isolated := segment("isolated", "x", "y", 30, 20, 45, 20, "isolated", "local")
	isolated.LengthMeters, isolated.ContinuityClass = 15, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, isolated}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("isolated", 0, 15),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{35, 0}}}

	removeUnsupportedIsolatedSingleRoadPortions(&result, raw, graph, 20, 45, 5, 128, 1e-6)

	if len(result.Traversals[0].Portions) != 1 || result.Traversals[0].Portions[0].SegmentID != "before" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDisconnectedSingleRoadStubBetweenConnectedRoadsIsRemoved(t *testing.T) {
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	stub := segment("stub", "tip", "junction", 0, 20, 0, 0, "side", "local")
	stub.LengthMeters, stub.ContinuityClass = 20, "road"
	after := segment("after", "junction", "b", 0, 0, 20, 0, "main", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, stub, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("stub", 0, 20), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{-5, 0}}, {Point: Point{5, 0}}, {Point: Point{15, 0}}}

	removeDisconnectedSingleRoadStubs(&result, raw, graph, 30, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedRoadDeadEndIsAddedOutAndBack(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
	after := segment("after", "junction", "b", 0, 0, 20, 0, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalID = 20, "road", "after"
	outbound := segment("court-r", "junction", "tip", 0, 0, 0, 40, "court", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalID, outbound.PhysicalSegmentID, outbound.Direction = 40, "road", "court", physical, SegmentReverse
	inbound := segment("court-f", "tip", "junction", 0, 40, 0, 0, "court", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalID, inbound.PhysicalSegmentID = 40, "road", "court", physical
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, after, outbound, inbound}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 6, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 20}}, {Point: Point{0, 40}}, {Point: Point{0, 20}}, {Point: Point{0, 0}}, {Point: Point{15, 0}}}

	extendRawSupportedRoadDeadEnds(&result, raw, graph, 150, 60, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "court-r" || got[2].SegmentID != "court-f" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedRoadBranchChainIsAddedOutAndBack(t *testing.T) {
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
	after := segment("after", "junction", "b", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalID = 20, "road", "after"
	first := segment("court-1", "junction", "middle", 0, 0, 40, 0, "court", "local")
	first.LengthMeters, first.ContinuityClass, first.PhysicalID, first.SourceWayID = 40, "road", "court-1", 7
	firstBack := segment("court-1-back", "middle", "junction", 40, 0, 0, 0, "court", "local")
	firstBack.LengthMeters, firstBack.ContinuityClass, firstBack.PhysicalID, firstBack.SourceWayID, firstBack.Direction = 40, "road", "court-1", 7, SegmentReverse
	second := segment("court-2", "middle", "end", 40, 0, 80, 0, "court", "local")
	second.LengthMeters, second.ContinuityClass, second.PhysicalID, second.SourceWayID = 40, "road", "court-2", 7
	secondBack := segment("court-2-back", "end", "middle", 80, 0, 40, 0, "court", "local")
	secondBack.LengthMeters, secondBack.ContinuityClass, secondBack.PhysicalID, secondBack.SourceWayID, secondBack.Direction = 40, "road", "court-2", 7, SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, after, first, firstBack, second, secondBack}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 6, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{40, 0}}, {Point: Point{80, 0}}, {Point: Point{40, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 10}}}

	extendRawSupportedRoadOutAndBackChains(&result, raw, graph, 150, 3, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 6 || got[1].SegmentID != "court-1" || got[2].SegmentID != "court-2" || got[3].SegmentID != "court-2-back" || got[4].SegmentID != "court-1-back" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRoadBranchChainRequiresOrderedReturn(t *testing.T) {
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
	after := segment("after", "junction", "b", 0, 0, 0, 20, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalID = 20, "road", "after"
	branch := segment("branch", "junction", "end", 0, 0, 40, 0, "branch", "local")
	branch.LengthMeters, branch.ContinuityClass, branch.PhysicalID = 40, "road", "branch"
	branchBack := segment("branch-back", "end", "junction", 40, 0, 0, 0, "branch", "local")
	branchBack.LengthMeters, branchBack.ContinuityClass, branchBack.PhysicalID, branchBack.Direction = 40, "road", "branch", SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, after, branch, branchBack}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{40, 0}}, {Point: Point{0, 10}}}

	extendRawSupportedRoadOutAndBackChains(&result, raw, graph, 150, 3, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("one-way branch inferred as out-and-back: %+v", result)
	}
}

func TestDrivewayChainExtendsAcrossSoftTraversalBoundary(t *testing.T) {
	firstPhysical, secondPhysical := uuid.New(), uuid.New()
	first := segment("first", "road", "middle", 0, 0, 10, 0, "driveway", "local")
	first.LengthMeters, first.TransitionOnly, first.DrivewayConnector, first.PhysicalID, first.PhysicalSegmentID, first.SourceWayID = 10, true, true, "first", firstPhysical, 7
	firstBack := segment("first-back", "middle", "road", 10, 0, 0, 0, "driveway", "local")
	firstBack.LengthMeters, firstBack.TransitionOnly, firstBack.DrivewayConnector, firstBack.PhysicalID, firstBack.PhysicalSegmentID, firstBack.SourceWayID, firstBack.Direction = 10, true, true, "first", firstPhysical, 7, SegmentReverse
	second := segment("second", "middle", "end", 10, 0, 70, 0, "driveway", "local")
	second.LengthMeters, second.TransitionOnly, second.DrivewayConnector, second.PhysicalID, second.PhysicalSegmentID, second.SourceWayID = 60, true, true, "second", secondPhysical, 7
	secondBack := segment("second-back", "end", "middle", 70, 0, 10, 0, "driveway", "local")
	secondBack.LengthMeters, secondBack.TransitionOnly, secondBack.DrivewayConnector, secondBack.PhysicalID, secondBack.PhysicalSegmentID, secondBack.SourceWayID, secondBack.Direction = 60, true, true, "second", secondPhysical, 7, SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, firstBack, second, secondBack}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("first", 0, 10)}},
		{FirstObservation: 2, LastObservation: 2},
		{FirstObservation: 3, LastObservation: 4, Portions: []TraversedPortion{graph.portion("first-back", 0, 10)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{70, 0}}, {Point: Point{10, 0}}, {Point: Point{0, 0}}}

	extendRawSupportedBoundaryRoadOutAndBackChains(&result, raw, graph, 150, 3, 5, 64, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 3 || got[1].SegmentID != "second" || got[2].SegmentID != "second-back" {
		t.Fatalf("result=%+v", result)
	}
}

func TestShortOrdinaryRoadDeadEndIsNotInferred(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
	short := segment("short-r", "junction", "tip", 0, 0, 0, 6, "side", "local")
	short.LengthMeters, short.ContinuityClass, short.PhysicalID, short.PhysicalSegmentID, short.Direction = 6, "road", "short", physical, SegmentReverse
	shortBack := segment("short-f", "tip", "junction", 0, 6, 0, 0, "side", "local")
	shortBack.LengthMeters, shortBack.ContinuityClass, shortBack.PhysicalID, shortBack.PhysicalSegmentID = 6, "road", "short", physical
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, short, shortBack}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{graph.portion("before", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 6}}, {Point: Point{0, 0}}, {Point: Point{-10, 0}}}

	extendRawSupportedRoadDeadEnds(&result, raw, graph, 150, 60, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 1 {
		t.Fatalf("short road dead end inferred: %+v", result)
	}
}

func TestDisconnectedRoadGapGetsSupportedConnector(t *testing.T) {
	before := segment("before", "a", "b", -20, 0, 0, 0, "first", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	connector := segment("connector", "b", "c", 0, 0, 10, 0, "turn", "local")
	connector.LengthMeters, connector.ContinuityClass = 10, "road"
	after := segment("after", "c", "d", 10, 0, 30, 0, "second", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, connector, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{5, 0}}, {Point: Point{10, 0}}, {Point: Point{25, 0}}}

	repairDisconnectedRoadPortionGaps(&result, raw, graph, 40, 1e-6)

	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "connector" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDisconnectedLongRoadGapRequiresRawSupport(t *testing.T) {
	before := segment("before", "a", "b", -20, 0, 0, 0, "first", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	connector := segment("connector", "b", "c", 0, 0, 30, 0, "turn", "local")
	connector.LengthMeters, connector.ContinuityClass = 30, "road"
	after := segment("after", "c", "d", 30, 0, 50, 0, "second", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, connector, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 50}}, {Point: Point{10, 50}}, {Point: Point{40, 50}}}

	repairDisconnectedRoadPortionGaps(&result, raw, graph, 40, 1e-6)

	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("unsupported connector added: %+v", result)
	}
}

func TestDisconnectedRoadExcursionUsesShorterSupportedTurn(t *testing.T) {
	before := segment("before", "a", "b", -20, 0, 0, 0, "first", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	direct := segment("direct", "b", "c", 0, 0, 10, 0, "first", "local")
	direct.LengthMeters, direct.ContinuityClass = 10, "road"
	wrongFirst := segment("wrong-1", "x", "y", 0, 10, 8, 10, "wrong", "local")
	wrongFirst.LengthMeters, wrongFirst.ContinuityClass = 8, "road"
	wrongSecond := segment("wrong-2", "y", "c", 8, 10, 10, 0, "wrong", "local")
	wrongSecond.LengthMeters, wrongSecond.ContinuityClass = 8, "road"
	after := segment("after", "c", "d", 10, 0, 30, 0, "second", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, direct, wrongFirst, wrongSecond, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("wrong-1", 0, 8), graph.portion("wrong-2", 0, 8), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{5, 0}}, {Point: Point{10, 0}}, {Point: Point{25, 0}}}

	repairDisconnectedRoadExcursions(&result, raw, graph, 40, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "direct" {
		t.Fatalf("result=%+v", result)
	}
}

func TestIncompleteRoadTurnCompletesBoundaryAndReplacesTangent(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 30, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass = 30, "road"
	connector := segment("connector", "b", "c", 30, 0, 45, 0, "main", "local")
	connector.LengthMeters, connector.ContinuityClass = 15, "road"
	wrong := segment("wrong", "x", "c", 35, 15, 45, 0, "wrong", "local")
	wrong.LengthMeters, wrong.ContinuityClass = 10, "road"
	after := segment("after", "c", "d", 45, 0, 65, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, connector, wrong, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("wrong", 0, 10), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}, {Point: Point{45, 0}}, {Point: Point{60, 0}}}

	repairIncompleteRoadTurns(&result, raw, graph, 60, 30, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 3 || got[0].ToMeter != 30 || got[1].SegmentID != "connector" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBoundaryIncompleteRoadTurnCompletesAndMerges(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 30, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass = 30, "road"
	connector := segment("connector", "b", "c", 30, 0, 45, 0, "main", "local")
	connector.LengthMeters, connector.ContinuityClass = 15, "road"
	wrong := segment("wrong", "x", "c", 35, 15, 45, 0, "wrong", "local")
	wrong.LengthMeters, wrong.ContinuityClass = 10, "road"
	after := segment("after", "c", "d", 45, 0, 65, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, connector, wrong, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 20)}},
		{FirstObservation: 2, LastObservation: 2},
		{FirstObservation: 2, LastObservation: 4, Portions: []TraversedPortion{graph.portion("wrong", 5, 10), graph.portion("after", 0, 20)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}, {Point: Point{45, 0}}, {Point: Point{60, 0}}}

	repairBoundaryIncompleteRoadTurns(&result, raw, graph, 60, 30, 64, 1e-6)

	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[0].ToMeter != 30 || result.Traversals[0].Portions[1].SegmentID != "connector" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBoundaryIncompleteRoadTurnDoesNotReplaceSameRoadReversal(t *testing.T) {
	before := segment("before", "a", "b", 0, 0, 30, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass = 30, "road"
	connector := segment("connector", "b", "c", 30, 0, 45, 0, "main", "local")
	connector.LengthMeters, connector.ContinuityClass = 15, "road"
	wrong := segment("wrong", "x", "c", 35, 15, 45, 0, "wrong", "local")
	wrong.LengthMeters, wrong.ContinuityClass = 10, "road"
	after := segment("after", "c", "d", 45, 0, 65, 0, "main", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, connector, wrong, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 20)}},
		{FirstObservation: 2, LastObservation: 4, Portions: []TraversedPortion{graph.portion("wrong", 0, 10), graph.portion("after", 0, 20)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}, {Point: Point{45, 0}}, {Point: Point{60, 0}}}

	repairBoundaryIncompleteRoadTurns(&result, raw, graph, 40, 30, 64, 1e-6)

	if len(result.Traversals) != 2 || result.Traversals[1].Portions[0].SegmentID != "wrong" {
		t.Fatalf("same-road reversal was replaced: %+v", result)
	}
}

func TestConnectedRawSupportedRoadTurnCompletesBothPortions(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 40, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 40, "road"
	after := segment("after", "junction", "b", 40, 0, 40, -40, "after", "local")
	after.LengthMeters, after.ContinuityClass = 40, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{
		graph.portion("before", 0, 34), graph.portion("after", 8, 40),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{40, 0}}, {Point: Point{40, -10}}, {Point: Point{40, -25}}, {Point: Point{40, -40}}}

	completeRawSupportedConnectedRoadTurns(&result, raw, graph, 30, 5, 1e-6)

	got := result.Traversals[0].Portions
	if got[0].ToMeter != 40 || got[1].FromMeter != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestConnectedRoadTurnRequiresDirectionalRawSupport(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 40, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 40, "road"
	after := segment("after", "junction", "b", 40, 0, 40, -40, "after", "local")
	after.LengthMeters, after.ContinuityClass = 40, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{
		graph.portion("before", 0, 34), graph.portion("after", 8, 40),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{34, 0}}, {Point: Point{50, 0}}}

	completeRawSupportedConnectedRoadTurns(&result, raw, graph, 30, 5, 1e-6)

	got := result.Traversals[0].Portions
	if got[0].ToMeter != 34 || got[1].FromMeter != 8 {
		t.Fatalf("unsupported turn completed: %+v", result)
	}
}

func TestConnectedRawSupportedRoadTurnCompletesAcrossSoftBoundary(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 40, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 40, "road"
	after := segment("after", "junction", "b", 40, 0, 40, -40, "after", "local")
	after.LengthMeters, after.ContinuityClass = 40, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("before", 0, 34)}},
		{FirstObservation: 3, LastObservation: 5, Portions: []TraversedPortion{graph.portion("after", 8, 40)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{40, 0}}, {Point: Point{40, -10}}, {Point: Point{40, -25}}, {Point: Point{40, -40}}}

	completeRawSupportedBoundaryConnectedRoadTurns(&result, raw, graph, 30, 5, 128, 1e-6)

	if result.Traversals[0].Portions[0].ToMeter != 40 || result.Traversals[1].Portions[0].FromMeter != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestBoundarySameRoadDetourUsesUniqueRawSupportedConnector(t *testing.T) {
	before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.SourceWayID = 20, "road", 7
	direct := segment("direct", "junction", "return", 0, 0, 40, 0, "main", "local")
	direct.LengthMeters, direct.ContinuityClass, direct.SourceWayID = 40, "road", 7
	detourOne := segment("detour-1", "junction", "x", 0, 0, 0, 20, "detour", "local")
	detourOne.LengthMeters, detourOne.ContinuityClass = 20, "road"
	detourTwo := segment("detour-2", "x", "y", 0, 20, 40, 20, "detour", "local")
	detourTwo.LengthMeters, detourTwo.ContinuityClass = 40, "road"
	detourThree := segment("detour-3", "y", "return", 40, 20, 40, 0, "detour", "local")
	detourThree.LengthMeters, detourThree.ContinuityClass = 20, "road"
	after := segment("after", "return", "b", 40, 0, 60, 0, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.SourceWayID = 20, "road", 7
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, direct, detourOne, detourTwo, detourThree, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 20)}},
		{FirstObservation: 2, LastObservation: 2},
		{FirstObservation: 3, LastObservation: 5, Portions: []TraversedPortion{graph.portion("detour-1", 0, 20), graph.portion("detour-2", 0, 40), graph.portion("detour-3", 0, 20), graph.portion("after", 0, 20)}},
	}}
	raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{25, 0}}, {Point: Point{40, 0}}, {Point: Point{55, 0}}}

	repairBoundarySameRoadDetours(&result, raw, graph, 100, 20, 6, 5, 128, 1e-6)

	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "direct" {
		t.Fatalf("result=%+v", result)
	}
}

func TestSameRoadExcursionUsesTwoRawSupportedSubdivisions(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 20, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.SourceWayID = 20, "road", 7
	directOne := segment("direct-1", "junction", "middle", 20, 0, 30, 0, "main", "local")
	directOne.LengthMeters, directOne.ContinuityClass, directOne.SourceWayID = 10, "road", 7
	directTwo := segment("direct-2", "middle", "return", 30, 0, 40, 0, "main", "local")
	directTwo.LengthMeters, directTwo.ContinuityClass, directTwo.SourceWayID = 10, "road", 7
	detourOne := segment("detour-1", "junction", "x", 20, 0, 20, 20, "detour-1", "local")
	detourOne.LengthMeters, detourOne.ContinuityClass = 20, "road"
	detourTwo := segment("detour-2", "x", "y", 20, 20, 40, 20, "detour-2", "local")
	detourTwo.LengthMeters, detourTwo.ContinuityClass = 20, "road"
	detourThree := segment("detour-3", "y", "return", 40, 20, 40, 0, "detour-3", "local")
	detourThree.LengthMeters, detourThree.ContinuityClass = 20, "road"
	after := segment("after", "return", "b", 40, 0, 60, 0, "main", "local")
	after.LengthMeters, after.ContinuityClass, after.SourceWayID = 20, "road", 7
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, directOne, directTwo, detourOne, detourTwo, detourThree, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("detour-1", 0, 20), graph.portion("detour-2", 0, 20), graph.portion("detour-3", 0, 20), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{25, 0}}, {Point: Point{35, 0}}, {Point: Point{40, 0}}, {Point: Point{60, 0}}}

	repairRawSupportedSameRoadExcursions(&result, raw, graph, 100, 20, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "direct-1" || got[2].SegmentID != "direct-2" {
		t.Fatalf("result=%+v", result)
	}
}

func TestShortUnsupportedTangentUsesLongerRawSupportedRoadRoute(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 20, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	tangent := segment("tangent", "junction", "wrong", 20, 0, 20, 30, "tangent", "local")
	tangent.LengthMeters, tangent.ContinuityClass = 30, "road"
	directOne := segment("direct-1", "junction", "middle", 20, 0, 100, 0, "direct-1", "local")
	directOne.LengthMeters, directOne.ContinuityClass = 80, "road"
	directTwo := segment("direct-2", "middle", "return", 100, 0, 180, 0, "direct-2", "local")
	directTwo.LengthMeters, directTwo.ContinuityClass = 80, "road"
	after := segment("after", "return", "b", 180, 0, 200, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, tangent, directOne, directTwo, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("tangent", 0, 30), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{60, 0}}, {Point: Point{100, 0}}, {Point: Point{140, 0}}, {Point: Point{180, 0}}}

	replaceShortUnsupportedRoadTangentsWithRawRoute(&result, raw, graph, 60, 400, 3, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "direct-1" || got[2].SegmentID != "direct-2" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedDeadEndTangentIsPreservedOutAndBackBeforeLongerRoadRoute(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", 0, 0, 20, 0, "before", "local")
	before.LengthMeters, before.ContinuityClass = 20, "road"
	tangent := segment("tangent", "junction", "tip", 20, 0, 20, 30, "tangent", "local")
	tangent.LengthMeters, tangent.ContinuityClass, tangent.PhysicalID, tangent.PhysicalSegmentID = 30, "road", "tangent", physical
	tangentBack := segment("tangent-back", "tip", "junction", 20, 30, 20, 0, "tangent", "local")
	tangentBack.LengthMeters, tangentBack.ContinuityClass, tangentBack.PhysicalID, tangentBack.PhysicalSegmentID, tangentBack.Direction = 30, "road", "tangent", physical, SegmentReverse
	directOne := segment("direct-1", "junction", "middle", 20, 0, 100, 0, "direct-1", "local")
	directOne.LengthMeters, directOne.ContinuityClass = 80, "road"
	directTwo := segment("direct-2", "middle", "return", 100, 0, 180, 0, "direct-2", "local")
	directTwo.LengthMeters, directTwo.ContinuityClass = 80, "road"
	after := segment("after", "return", "b", 180, 0, 200, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass = 20, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, tangent, tangentBack, directOne, directTwo, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 8, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("tangent", 0, 30), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{20, 15}}, {Point: Point{20, 30}}, {Point: Point{20, 0}}, {Point: Point{60, 0}}, {Point: Point{100, 0}}, {Point: Point{140, 0}}, {Point: Point{180, 0}}}

	replaceShortUnsupportedRoadTangentsWithRawRoute(&result, raw, graph, 60, 400, 3, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 6 || got[1].SegmentID != "tangent" || got[2].SegmentID != "tangent-back" || got[3].SegmentID != "direct-1" || got[4].SegmentID != "direct-2" {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnsupportedBoundaryNetworkPrefixIsRemoved(t *testing.T) {
	before := segment("before", "a", "b", 0, 16, 0, 20, "before", "local")
	before.LengthMeters, before.ContinuityClass = 4, "path"
	wrongOne := segment("wrong-1", "x", "y", 20, 20, 40, 20, "wrong", "local")
	wrongOne.LengthMeters, wrongOne.ContinuityClass = 20, "road"
	wrongTwo := segment("wrong-2", "y", "z", 40, 20, 40, 40, "wrong", "local")
	wrongTwo.LengthMeters, wrongTwo.ContinuityClass = 20, "road"
	wrongThree := segment("wrong-3", "z", "q", 40, 40, 20, 40, "wrong", "local")
	wrongThree.LengthMeters, wrongThree.ContinuityClass = 20, "road"
	after := segment("after", "b", "c", 0, 20, 0, 60, "after", "local")
	after.LengthMeters, after.ContinuityClass = 40, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, wrongOne, wrongTwo, wrongThree, after}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("before", 0, 4)}},
		{FirstObservation: 2, LastObservation: 5, Portions: []TraversedPortion{graph.portion("wrong-1", 0, 20), graph.portion("wrong-2", 0, 20), graph.portion("wrong-3", 0, 20), graph.portion("after", 0, 40)}},
	}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{0, 20}}, {Point: Point{0, 25}}, {Point: Point{0, 35}}, {Point: Point{0, 45}}, {Point: Point{0, 60}}}

	removeUnsupportedBoundaryNetworkDetours(&result, raw, graph, 15, 64, 1e-6)

	if len(result.Traversals[1].Portions) != 1 || result.Traversals[1].Portions[0].SegmentID != "after" {
		t.Fatalf("result=%+v", result)
	}
}

func TestTraversalTailUsesFullySupportedSameRoadContinuation(t *testing.T) {
	current := segment("current", "a", "junction", -40, 0, 0, 0, "arboleda", "local")
	current.LengthMeters, current.ContinuityClass, current.PhysicalID = 40, "road", "current"
	continuation := segment("continuation", "junction", "end", 0, 0, 60, 60, "arboleda", "local")
	continuation.LengthMeters, continuation.ContinuityClass, continuation.PhysicalID = 100, "road", "continuation"
	wrongOne := segment("wrong-1", "junction", "x", 0, 0, 0, -50, "wrong", "local")
	wrongOne.LengthMeters, wrongOne.ContinuityClass, wrongOne.PhysicalID = 50, "path", "wrong-1"
	wrongTwo := segment("wrong-2", "x", "y", 0, -50, 60, -50, "wrong", "local")
	wrongTwo.LengthMeters, wrongTwo.ContinuityClass, wrongTwo.PhysicalID = 60, "road", "wrong-2"
	wrongThree := segment("wrong-3", "y", "z", 60, -50, 60, 0, "wrong", "local")
	wrongThree.LengthMeters, wrongThree.ContinuityClass, wrongThree.PhysicalID = 50, "road", "wrong-3"
	graph := compileGraph(Graph{Segments: []DirectedSegment{current, continuation, wrongOne, wrongTwo, wrongThree}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("current", 0, 40), graph.portion("wrong-1", 0, 50), graph.portion("wrong-2", 0, 60), graph.portion("wrong-3", 0, 50),
	}}}}
	raw := []Observation{{Point: Point{-20, 0}}, {Point: Point{0, 0}}, {Point: Point{60, 0}}, {Point: Point{60, 30}}, {Point: Point{60, 60}}}

	replaceTraversalTailWithRawSupportedSameRoad(&result, raw, graph, 150, 10, 10, 1e-6)

	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[1].SegmentID != "continuation" {
		t.Fatalf("result=%+v", result)
	}
}

func TestTraversalTailRejectsSameEndpointDetour(t *testing.T) {
	current := segment("current", "a", "junction", -40, 0, 0, 0, "arboleda", "local")
	current.LengthMeters, current.ContinuityClass, current.PhysicalID = 40, "road", "current"
	continuation := segment("continuation", "junction", "end", 0, 0, 60, 60, "arboleda", "local")
	continuation.LengthMeters, continuation.ContinuityClass, continuation.PhysicalID = 100, "road", "continuation"
	wrong := segment("wrong", "junction", "x", 0, 0, 0, -120, "wrong", "local")
	wrong.LengthMeters, wrong.ContinuityClass, wrong.PhysicalID = 160, "road", "wrong"
	graph := compileGraph(Graph{Segments: []DirectedSegment{current, continuation, wrong}})
	portions := []TraversedPortion{graph.portion("current", 0, 40), graph.portion("wrong", 0, 160)}
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: portions}}}
	raw := []Observation{{Point: Point{-20, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 100}}, {Point: Point{100, 100}}, {Point: Point{60, 60}}}

	replaceTraversalTailWithRawSupportedSameRoad(&result, raw, graph, 150, 10, 10, 1e-6)

	if len(result.Traversals[0].Portions) != len(portions) {
		t.Fatalf("detour accepted: %+v", result)
	}
}

func TestUnsupportedParallelRoadRouteUsesSupportedParkingAlternative(t *testing.T) {
	before := segment("before", "a", "b", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
	wrongOne := segment("wrong-1", "b", "x", 0, 0, 0, 30, "wrong", "local")
	wrongOne.LengthMeters, wrongOne.ContinuityClass, wrongOne.PhysicalID = 30, "road", "wrong-1"
	wrongTwo := segment("wrong-2", "x", "c", 0, 30, 50, 10, "wrong", "local")
	wrongTwo.LengthMeters, wrongTwo.ContinuityClass, wrongTwo.PhysicalID = 40, "road", "wrong-2"
	alley := segment("alley", "b", "d", 0, 0, 10, 0, "alley", "local")
	alley.LengthMeters, alley.ContinuityClass, alley.PhysicalID = 10, "road", "alley"
	parking := segment("parking", "d", "c", 10, 0, 50, 0, "parking", "local")
	parking.LengthMeters, parking.TransitionOnly, parking.ParkingAisleConnector, parking.PhysicalID = 40, true, true, "parking"
	after := segment("after", "c", "e", 50, 0, 70, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalID = 20, "road", "after"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, wrongOne, wrongTwo, alley, parking, after}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("wrong-1", 0, 30), graph.portion("wrong-2", 0, 40), graph.portion("after", 0, 20),
	}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{30, 0}}, {Point: Point{50, 0}}, {Point: Point{65, 0}}}

	repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "alley" || got[2].SegmentID != "parking" {
		t.Fatalf("result=%+v", result)
	}
}

func TestParallelRepairDoesNotReplaceSelectedParkingRoute(t *testing.T) {
	before := segment("before", "a", "b", -20, 0, 0, 0, "main", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
	wrong := segment("wrong", "b", "c", 0, 20, 50, 20, "wrong", "local")
	wrong.LengthMeters, wrong.TransitionOnly, wrong.ParkingAisleConnector, wrong.PhysicalID = 70, true, true, "wrong"
	parking := segment("parking", "b", "c", 0, 0, 50, 0, "parking", "local")
	parking.LengthMeters, parking.TransitionOnly, parking.ParkingAisleConnector, parking.PhysicalID = 50, true, true, "parking"
	after := segment("after", "c", "d", 50, 0, 70, 0, "after", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalID = 20, "road", "after"
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, wrong, parking, after}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("wrong", 0, 70), graph.portion("after", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{25, 0}}, {Point: Point{50, 0}}, {Point: Point{65, 0}}}

	repairRawSupportedParallelParkingRoutes(&result, raw, graph, 120, 20, 15, 1e-6)

	if result.Traversals[0].Portions[1].SegmentID != "wrong" {
		t.Fatalf("selected parking route replaced: %+v", result)
	}
}

func TestOverlappingRoadReversalIsReplacedBySupportedPathTurn(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "before-road", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-r", "junction", "tip", 0, 0, 0, 80, "stub-road", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalSegmentID, outbound.Direction = 80, "road", physical, SegmentReverse
	inbound := segment("stub-f", "tip", "junction", 0, 80, 0, 0, "stub-road", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalSegmentID = 80, "road", physical
	connector := segment("connector", "junction", "path-start", 0, 0, 10, 0, "trail", "local")
	connector.LengthMeters, connector.ContinuityClass, connector.PhysicalSegmentID = 10, "path", uuid.New()
	after := segment("after", "path-start", "b", 10, 0, 40, 0, "trail", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 30, "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, connector, after}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("stub-r", 0, 60), graph.portion("stub-f", 0, 44), graph.portion("after", 10, 30),
	}}}}
	result.Traversals[0].Portions[1].SourceFromFraction, result.Traversals[0].Portions[1].SourceToFraction = 0.25, 1
	result.Traversals[0].Portions[2].SourceFromFraction, result.Traversals[0].Portions[2].SourceToFraction = 0.25, 0.8
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{20, 0}}, {Point: Point{35, 0}}}

	replaceOverlappingRoadReversalWithPath(&result, raw, graph, 150, 100, 60, 15, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 3 || got[0].SegmentID != "before" || got[1].SegmentID != "connector" || got[2].SegmentID != "after" || got[2].FromMeter != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestOverlappingRoadReversalIsRetainedWithoutPathSupport(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "before-road", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-r", "junction", "tip", 0, 0, 0, 80, "stub-road", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalSegmentID, outbound.Direction = 80, "road", physical, SegmentReverse
	inbound := segment("stub-f", "tip", "junction", 0, 80, 0, 0, "stub-road", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalSegmentID = 80, "road", physical
	connector := segment("connector", "junction", "path-start", 0, 0, 10, 0, "trail", "local")
	connector.LengthMeters, connector.ContinuityClass, connector.PhysicalSegmentID = 10, "path", uuid.New()
	after := segment("after", "path-start", "b", 10, 0, 40, 0, "trail", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 30, "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, connector, after}})
	portions := []TraversedPortion{graph.portion("before", 0, 20), graph.portion("stub-r", 0, 60), graph.portion("stub-f", 0, 44), graph.portion("after", 10, 30)}
	portions[1].SourceFromFraction, portions[1].SourceToFraction = 0.25, 1
	portions[2].SourceFromFraction, portions[2].SourceToFraction = 0.25, 0.8
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: portions}}}
	raw := []Observation{{Point: Point{0, 20}}, {Point: Point{0, 40}}, {Point: Point{0, 60}}, {Point: Point{0, 70}}}

	replaceOverlappingRoadReversalWithPath(&result, raw, graph, 150, 100, 60, 15, 1e-6)

	if len(result.Traversals[0].Portions) != len(portions) {
		t.Fatalf("unsupported replacement applied: %+v", result)
	}
}

func TestBoundaryOverlappingRoadReversalIsReplacedBySupportedPathTurn(t *testing.T) {
	physical := uuid.New()
	before := segment("before", "a", "junction", -20, 0, 0, 0, "before-road", "local")
	before.LengthMeters, before.ContinuityClass, before.PhysicalSegmentID = 20, "road", uuid.New()
	outbound := segment("stub-r", "junction", "tip", 0, 0, 0, 80, "stub-road", "local")
	outbound.LengthMeters, outbound.ContinuityClass, outbound.PhysicalSegmentID, outbound.Direction = 80, "road", physical, SegmentReverse
	inbound := segment("stub-f", "tip", "junction", 0, 80, 0, 0, "stub-road", "local")
	inbound.LengthMeters, inbound.ContinuityClass, inbound.PhysicalSegmentID = 80, "road", physical
	connector := segment("connector", "junction", "path-start", 0, 0, 10, 0, "trail", "local")
	connector.LengthMeters, connector.ContinuityClass, connector.PhysicalSegmentID = 10, "path", uuid.New()
	after := segment("after", "path-start", "b", 10, 0, 100, 0, "trail", "local")
	after.LengthMeters, after.ContinuityClass, after.PhysicalSegmentID = 90, "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, outbound, inbound, connector, after}})
	firstPortions := []TraversedPortion{graph.portion("before", 0, 20), graph.portion("stub-r", 0, 60), graph.portion("stub-f", 0, 44)}
	firstPortions[1].SourceFromFraction, firstPortions[1].SourceToFraction = 0.25, 1
	firstPortions[2].SourceFromFraction, firstPortions[2].SourceToFraction = 0.25, 0.8
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: firstPortions},
		{FirstObservation: 2, LastObservation: 4, Portions: []TraversedPortion{graph.portion("after", 40, 90)}},
	}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{50, 0}}, {Point: Point{90, 0}}}

	replaceBoundaryOverlappingRoadReversalWithPath(&result, raw, graph, 150, 250, 60, 15, 64, 1e-6)

	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "connector" || result.Traversals[0].Portions[2].FromMeter != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedPathEndpointCompletionIsCapped(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 100, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass, path.PhysicalID, path.PhysicalSegmentID = 100, "path", "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{path}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3,
		Portions: []TraversedPortion{graph.portion("path", 10, 85)}}}}
	observations := []Observation{{Point: Point{0, 0}}, {Point: Point{5, 0}}, {Point: Point{95, 0}}, {Point: Point{100, 0}}}
	completeRawSupportedPathEndpoints(&result, observations, graph, 20, 5, 1e-6)
	portion := result.Traversals[0].Portions[0]
	if portion.FromMeter != 0 || portion.ToMeter != 100 {
		t.Fatalf("completed portion=%+v", portion)
	}

	result.Traversals[0].Portions = []TraversedPortion{graph.portion("path", 25, 75)}
	completeRawSupportedPathEndpoints(&result, observations, graph, 20, 5, 1e-6)
	portion = result.Traversals[0].Portions[0]
	if portion.FromMeter != 25 || portion.ToMeter != 75 {
		t.Fatalf("over-limit tails completed: %+v", portion)
	}
}

func TestPathEndpointCompletionRejectsSubthresholdJitterSpan(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 100, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass, path.PhysicalID, path.PhysicalSegmentID = 100, "path", "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{path}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3,
		Portions: []TraversedPortion{graph.portion("path", 79, 80)}}}}
	observations := []Observation{{Point: Point{80, 0}}, {Point: Point{90, 0}}, {Point: Point{98, 0}}, {Point: Point{100, 0}}}
	completeRawSupportedPathEndpoints(&result, observations, graph, 20, 5, 1e-6)
	portion := result.Traversals[0].Portions[0]
	if portion.FromMeter != 79 || portion.ToMeter != 80 {
		t.Fatalf("subthreshold jitter span completed: %+v", portion)
	}
}

func TestPathEndpointCompletionDoesNotCrossHardSplit(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 100, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass, path.PhysicalID, path.PhysicalSegmentID = 100, "path", "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{path}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 1, LastObservation: 2,
		Portions: []TraversedPortion{graph.portion("path", 10, 100)}}}, Splits: []Split{{BeforeObservation: 1, Reason: SplitTemporal}}}
	observations := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}}
	completeRawSupportedPathEndpoints(&result, observations, graph, 20, 5, 1e-6)
	if result.Traversals[0].Portions[0].FromMeter != 10 {
		t.Fatalf("hard-split tail completed: %+v", result)
	}
}

func TestEndpointSupportUsesLocalApproachNotWholeSegmentChord(t *testing.T) {
	endpoint := Point{10, 0}
	observations := []Observation{{Point: Point{4, -1}}, {Point: Point{6, -1}}, {Point: Point{8, -1}}, {Point: Point{10, -1}}}
	if !endpointRawSupported(observations, endpoint, 5, 1) {
		t.Fatal("curved-segment endpoint approach was rejected")
	}
	passing := []Observation{{Point: Point{9, 4}}, {Point: Point{9, 4.2}}, {Point: Point{9, 4.4}}}
	if endpointRawSupported(passing, endpoint, 5, 1) {
		t.Fatal("nearby motion without material endpoint approach was accepted")
	}
}

func TestEndpointSupportAccountsForReportedAccuracy(t *testing.T) {
	endpoint := Point{10, 0}
	observations := []Observation{
		{Point: Point{2, 0}, AccuracyMeters: 1.5},
		{Point: Point{4, 0}, AccuracyMeters: 1.5},
	}
	if !endpointRawSupported(observations, endpoint, 5, 1) {
		t.Fatal("accuracy-supported endpoint approach was rejected")
	}
	observations[0].AccuracyMeters, observations[1].AccuracyMeters = 0, 0
	if endpointRawSupported(observations, endpoint, 5, 1) {
		t.Fatal("endpoint approach outside the unadjusted corridor was accepted")
	}
}

func TestRawSupportedShortPathNeighborIsAbsorbedOnce(t *testing.T) {
	neighbor := segment("neighbor", "x", "a", -4, 0, 0, 0, "neighbor", "local")
	neighbor.LengthMeters, neighbor.ContinuityClass, neighbor.PhysicalID, neighbor.PhysicalSegmentID = 4, "path", "neighbor", uuid.New()
	path := segment("path", "a", "b", 0, 0, 10, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass, path.PhysicalID, path.PhysicalSegmentID = 10, "path", "path", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{neighbor, path}})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3,
		Portions: []TraversedPortion{graph.portion("path", 0, 10)}}}}
	observations := []Observation{{Point: Point{-4, 0}}, {Point: Point{0, 0}}, {Point: Point{5, 0}}, {Point: Point{10, 0}}}
	completeRawSupportedPathEndpoints(&result, observations, graph, 20, 5, 1e-6)
	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[0].SegmentID != "neighbor" {
		t.Fatalf("result=%+v", result)
	}
	completeRawSupportedPathEndpoints(&result, observations, graph, 20, 5, 1e-6)
	if len(result.Traversals[0].Portions) != 2 {
		t.Fatalf("neighbor duplicated: %+v", result)
	}
}

func TestTransitionConnectorPromotionRequiresShortEligibleBoundedRun(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	second := segment("second", "d", "e", 25, 0, 35, 0, "second", "local")
	connectorOne := segment("connector-1", "b", "c", 10, 0, 16, 0, "connector-1", "local")
	connectorTwo := segment("connector-2", "c", "d", 16, 0, 25, 0, "connector-2", "local")
	connectorOne.TransitionOnly, connectorOne.ContinuityConnector = true, true
	connectorTwo.TransitionOnly, connectorTwo.ContinuityConnector = true, true
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, connectorOne, connectorTwo, second}, MaxContinuityConnectorMeters: 10})
	route, ok := graph.route(Candidate{SegmentID: "first", AlongMeters: 5}, Candidate{SegmentID: "second", AlongMeters: 5}, 100, 1e-6)
	if !ok || len(route.portions) != 2 || route.portions[0].SegmentID != "first" || route.portions[1].SegmentID != "second" {
		t.Fatalf("over-limit route=%+v ok=%t", route, ok)
	}

	connectorTwo.LengthMeters = 3
	connectorTwo.To = Point{19, 0}
	connectorTwo.ContinuityConnector = false
	second.From = Point{19, 0}
	graph = compileGraph(Graph{Segments: []DirectedSegment{first, connectorOne, connectorTwo, second}, MaxContinuityConnectorMeters: 10})
	route, ok = graph.route(Candidate{SegmentID: "first", AlongMeters: 5}, Candidate{SegmentID: "second", AlongMeters: 5}, 100, 1e-6)
	if !ok || len(route.portions) != 2 {
		t.Fatalf("mixed connector route=%+v ok=%t", route, ok)
	}
}

func TestAlignedDrivewayRunBridgesPathToRoadAndProducesCoverage(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 10, 0, "path", "local")
	path.ContinuityClass = "path"
	driveway := segment("driveway", "b", "c", 10, 0, 30, 0, "driveway", "local")
	driveway.TransitionOnly, driveway.DrivewayConnector, driveway.LengthMeters = true, true, 20
	road := segment("road", "c", "d", 30, 0, 40, 0, "road", "local")
	road.ContinuityClass = "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{path, driveway, road}, MaxDrivewayConnectorMeters: 40})
	route, ok := graph.route(Candidate{SegmentID: "path", AlongMeters: 5}, Candidate{SegmentID: "road", AlongMeters: 5}, 100, 1e-6)
	if !ok || len(route.portions) != 3 || route.portions[1].SegmentID != "driveway" {
		t.Fatalf("route=%+v ok=%t", route, ok)
	}
	if !drivewayConnectorsSupported(route.portions, graph, []Observation{{Point: Point{5, 0}}, {Point: Point{12, 0}}, {Point: Point{28, 0}}, {Point: Point{35, 0}}}, 3, 30, "", "") {
		t.Fatal("aligned driveway connector was rejected")
	}
	if drivewayConnectorsSupported(route.portions, graph, []Observation{{Point: Point{10, 0}}, {Point: Point{10, 5}}, {Point: Point{10, 15}}, {Point: Point{10, 20}}}, 3, 30, "", "") {
		t.Fatal("perpendicular driveway connector was accepted")
	}
}

func TestDrivewayRunRequiresPathRoadBoundaryAndLengthCap(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	first.ContinuityClass = "road"
	driveway := segment("driveway", "b", "c", 10, 0, 30, 0, "driveway", "local")
	driveway.TransitionOnly, driveway.DrivewayConnector, driveway.LengthMeters = true, true, 20
	second := segment("second", "c", "d", 30, 0, 40, 0, "second", "local")
	second.ContinuityClass = "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, driveway, second}, MaxDrivewayConnectorMeters: 40})
	if route, ok := graph.route(Candidate{SegmentID: "first", AlongMeters: 5}, Candidate{SegmentID: "second", AlongMeters: 5}, 100, 1e-6); ok {
		t.Fatalf("road-to-road driveway route accepted: %+v", route)
	}
	first.ContinuityClass = "path"
	driveway.LengthMeters, driveway.To = 41, Point{51, 0}
	second.From = Point{51, 0}
	graph = compileGraph(Graph{Segments: []DirectedSegment{first, driveway, second}, MaxDrivewayConnectorMeters: 40})
	if route, ok := graph.route(Candidate{SegmentID: "first", AlongMeters: 5}, Candidate{SegmentID: "second", AlongMeters: 5}, 100, 1e-6); ok {
		t.Fatalf("over-limit driveway route accepted: %+v", route)
	}
}

func TestDrivewayAlignmentAllowsOnlyShortRoadAdjacentJoin(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 10, 0, "path", "local")
	path.ContinuityClass = "path"
	driveway := segment("driveway", "b", "c", 10, 0, 30, 0, "driveway", "local")
	driveway.TransitionOnly, driveway.DrivewayConnector, driveway.LengthMeters = true, true, 20
	roadJoin := segment("road-join", "c", "d", 30, 0, 30, 7, "road-join", "local")
	roadJoin.TransitionOnly, roadJoin.DrivewayConnector, roadJoin.LengthMeters = true, true, 7
	road := segment("road", "d", "e", 30, 7, 40, 7, "road", "local")
	road.ContinuityClass = "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{path, driveway, roadJoin, road}, MaxDrivewayConnectorMeters: 40})
	route, ok := graph.route(Candidate{SegmentID: "path", AlongMeters: 5}, Candidate{SegmentID: "road", AlongMeters: 5}, 100, 1e-6)
	observations := []Observation{{Point: Point{5, 0}}, {Point: Point{12, 0}}, {Point: Point{28, 0}}, {Point: Point{35, 7}}}
	if !ok || !drivewayConnectorsSupported(route.portions, graph, observations, 3, 30, "", "") {
		t.Fatalf("short road join rejected: route=%+v ok=%t", route, ok)
	}
	roadJoin.LengthMeters = 11
	graph = compileGraph(Graph{Segments: []DirectedSegment{path, driveway, roadJoin, road}, MaxDrivewayConnectorMeters: 40})
	route, ok = graph.route(Candidate{SegmentID: "path", AlongMeters: 5}, Candidate{SegmentID: "road", AlongMeters: 5}, 100, 1e-6)
	if !ok || drivewayConnectorsSupported(route.portions, graph, observations, 3, 30, "", "") {
		t.Fatalf("long perpendicular road join accepted: route=%+v ok=%t", route, ok)
	}
}

func TestRawSupportedDrivewayBridgesAdjacentPathAndRoadTraversals(t *testing.T) {
	path := segment("path", "a", "b", 0, 0, 10, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass = 10, "path"
	driveway := segment("driveway", "b", "c", 10, 0, 30, 0, "driveway", "local")
	driveway.LengthMeters, driveway.TransitionOnly, driveway.DrivewayConnector = 20, true, true
	road := segment("road", "c", "d", 30, 0, 40, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass = 10, "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{path, driveway, road}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 0, Portions: []TraversedPortion{graph.portion("path", 0, 10)}},
		{FirstObservation: 3, LastObservation: 3, Portions: []TraversedPortion{graph.portion("road", 0, 10)}},
	}}
	observations := []Observation{{Point: Point{10, 0}}, {Point: Point{12, 0}}, {Point: Point{28, 0}}, {Point: Point{30, 0}}}
	bridgePlausibleDrivewayTraversals(&result, observations, graph, 40, 32, 1e-6)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "driveway" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRawSupportedParkingAisleBridgesRoadTraversals(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	first.LengthMeters, first.ContinuityClass, first.PhysicalSegmentID = 10, "road", uuid.New()
	aisle := segment("aisle", "b", "c", 10, 0, 90, 0, "aisle", "local")
	aisle.LengthMeters, aisle.TransitionOnly, aisle.ParkingAisleConnector = 80, true, true
	second := segment("second", "c", "d", 90, 0, 100, 0, "second", "local")
	second.LengthMeters, second.ContinuityClass, second.PhysicalSegmentID = 10, "road", uuid.New()
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, aisle, second}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 0, Portions: []TraversedPortion{graph.portion("first", 0, 10)}},
		{FirstObservation: 4, LastObservation: 4, Portions: []TraversedPortion{graph.portion("second", 0, 10)}},
	}}
	observations := []Observation{{Point: Point{10, 0}}, {Point: Point{30, 0}}, {Point: Point{60, 0}}, {Point: Point{85, 0}}, {Point: Point{90, 0}}}
	bridgePlausibleParkingAisleTraversals(&result, observations, graph, 200, 64, 1e-6)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "aisle" {
		t.Fatalf("result=%+v", result)
	}

	result.Traversals = []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 0, Portions: []TraversedPortion{graph.portion("first", 0, 10)}},
		{FirstObservation: 4, LastObservation: 4, Portions: []TraversedPortion{graph.portion("second", 0, 10)}},
	}
	unsupported := []Observation{{Point: Point{10, 25}}, {Point: Point{30, 25}}, {Point: Point{60, 25}}, {Point: Point{85, 25}}, {Point: Point{90, 25}}}
	bridgePlausibleParkingAisleTraversals(&result, unsupported, graph, 200, 64, 1e-6)
	if len(result.Traversals) != 2 {
		t.Fatalf("unsupported aisle bridged: %+v", result)
	}
}

func TestParkingAisleRunRequiresRoadBoundariesAndCap(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	first.LengthMeters, first.ContinuityClass = 10, "path"
	aisle := segment("aisle", "b", "c", 10, 0, 90, 0, "aisle", "local")
	aisle.LengthMeters, aisle.TransitionOnly, aisle.ParkingAisleConnector = 80, true, true
	second := segment("second", "c", "d", 90, 0, 100, 0, "second", "local")
	second.LengthMeters, second.ContinuityClass = 10, "path"
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, aisle, second}, MaxParkingAisleConnectorMeters: 200})
	if route, ok := graph.route(Candidate{SegmentID: "first", AlongMeters: 5, ContinuityClass: "path"}, Candidate{SegmentID: "second", AlongMeters: 5, ContinuityClass: "path"}, 250, 1e-6); ok {
		t.Fatalf("path-to-path parking aisle accepted: %+v", route)
	}
	first.ContinuityClass, second.ContinuityClass, aisle.LengthMeters, aisle.To = "road", "road", 201, Point{211, 0}
	second.From = Point{211, 0}
	graph = compileGraph(Graph{Segments: []DirectedSegment{first, aisle, second}, MaxParkingAisleConnectorMeters: 200})
	if route, ok := graph.route(Candidate{SegmentID: "first", AlongMeters: 5, ContinuityClass: "road"}, Candidate{SegmentID: "second", AlongMeters: 5, ContinuityClass: "road"}, 250, 1e-6); ok {
		t.Fatalf("over-limit parking aisle accepted: %+v", route)
	}
}

func TestRoadToPathParkingAislesBridgeDisconnectedPortions(t *testing.T) {
	road := segment("road", "a", "b", -20, 0, 0, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass = 20, "road"
	first := segment("aisle-1", "b", "c", 0, 0, 20, 0, "aisle", "local")
	first.LengthMeters, first.TransitionOnly, first.ParkingAisleConnector = 20, true, true
	second := segment("aisle-2", "c", "d", 20, 0, 50, 0, "aisle", "local")
	second.LengthMeters, second.TransitionOnly, second.ParkingAisleConnector = 30, true, true
	path := segment("path", "d", "e", 50, 0, 70, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass = 20, "path"
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, first, second, path}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 5, Portions: []TraversedPortion{graph.portion("road", 0, 20), graph.portion("path", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{15, 0}}, {Point: Point{30, 0}}, {Point: Point{50, 0}}, {Point: Point{65, 0}}}

	bridgeDisconnectedRoadPathParkingAisles(&result, raw, graph, 200, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "aisle-1" || got[2].SegmentID != "aisle-2" {
		t.Fatalf("result=%+v", result)
	}
}

func TestShortParkingEntryUsesDirectedPastEndpointSupport(t *testing.T) {
	short := segment("short", "a", "b", 0, 0, 6, 0, "aisle", "local")
	short.LengthMeters, short.TransitionOnly, short.ParkingAisleConnector = 6, true, true
	long := segment("long", "b", "c", 6, 0, 58, 0, "aisle", "local")
	long.LengthMeters, long.TransitionOnly, long.ParkingAisleConnector = 52, true, true
	graph := compileGraph(Graph{Segments: []DirectedSegment{short, long}, MaxParkingAisleConnectorMeters: 200})
	portions := []TraversedPortion{graph.portion("short", 0, 6), graph.portion("long", 0, 52)}
	raw := []Observation{{Point: Point{4, 0}}, {Point: Point{12, 0}}, {Point: Point{30, 0}}, {Point: Point{58, 0}}}

	got, ok := parkingAisleEvidence(portions, graph, raw, 2)

	if !ok || len(got) != 2 || got[0].FromMeter != 0 || got[0].ToMeter != 6 {
		t.Fatalf("short entry not retained: portions=%+v ok=%t", got, ok)
	}
}

func TestRawSupportedParkingAisleExtendsFromRoadEndpoint(t *testing.T) {
	road := segment("road", "a", "b", 0, 0, 10, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass, road.PhysicalID, road.PhysicalSegmentID = 10, "road", "road", uuid.New()
	aisle := segment("aisle", "b", "c", 10, 0, 90, 0, "aisle", "local")
	aisle.LengthMeters, aisle.TransitionOnly, aisle.ParkingAisleConnector, aisle.PhysicalID = 80, true, true, "aisle"
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, aisle}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4,
		Portions: []TraversedPortion{graph.portion("road", 0, 10)}}}}
	observations := []Observation{{Point: Point{10, 0}}, {Point: Point{30, 0}}, {Point: Point{50, 0}}, {Point: Point{65, 0}}, {Point: Point{70, 0}}}
	extendRawSupportedParkingAisles(&result, observations, graph, 200, 1e-6)
	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[1].SegmentID != "aisle" ||
		result.Traversals[0].Portions[1].FromMeter != 0 || result.Traversals[0].Portions[1].ToMeter != 60 {
		t.Fatalf("result=%+v", result)
	}

	result.Traversals[0].Portions = []TraversedPortion{graph.portion("road", 0, 9)}
	extendRawSupportedParkingAisles(&result, observations, graph, 200, 1e-6)
	if len(result.Traversals[0].Portions) != 1 {
		t.Fatalf("aisle extended from incomplete road: %+v", result)
	}
}

func TestParkingAisleShadowedByDirectRoadIsNotExtended(t *testing.T) {
	road := segment("road", "a", "b", 0, 0, 10, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass, road.PhysicalID = 10, "road", "road"
	first := segment("direct-1", "b", "middle", 10, 0, 20, 0, "road", "local")
	first.LengthMeters, first.ContinuityClass, first.PhysicalID = 10, "road", "direct-1"
	second := segment("direct-2", "middle", "c", 20, 0, 30, 0, "road", "local")
	second.LengthMeters, second.ContinuityClass, second.PhysicalID = 10, "road", "direct-2"
	aisle := segment("aisle", "b", "c", 10, 0, 30, 0, "aisle", "local")
	aisle.LengthMeters, aisle.TransitionOnly, aisle.ParkingAisleConnector, aisle.PhysicalID = 80, true, true, "aisle"
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, first, second, aisle}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{graph.portion("road", 0, 10)}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}}

	extendRawSupportedParkingAisles(&result, raw, graph, 200, 1e-6)

	if len(result.Traversals[0].Portions) != 1 {
		t.Fatalf("shadowed parking aisle added: %+v", result)
	}
}

func TestParkingAisleBeforeSameRoadContinuationIsRemoved(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 20, 0, "road", "local")
	before.LengthMeters, before.ContinuityClass, before.SourceWayID = 20, "road", 7
	parking := segment("parking", "junction", "lot", 20, 0, 20, 100, "parking", "local")
	parking.LengthMeters, parking.TransitionOnly, parking.ParkingAisleConnector = 100, true, true
	after := segment("after", "junction", "b", 20, 0, 20, -80, "road", "local")
	after.LengthMeters, after.ContinuityClass, after.SourceWayID = 80, "road", 7
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, parking, after}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("parking", 0, 95), graph.portion("after", 0, 80),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{20, -20}}, {Point: Point{20, -50}}, {Point: Point{20, -80}}}

	removeParkingAislesBeforeSameRoadContinuations(&result, raw, graph, 40, 15, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 2 || got[0].SegmentID != "before" || got[1].SegmentID != "after" {
		t.Fatalf("result=%+v", result)
	}
}

func TestParkingAisleBeforeDifferentRoadIsRetained(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 20, 0, "road-a", "local")
	before.LengthMeters, before.ContinuityClass, before.SourceWayID = 20, "road", 7
	parking := segment("parking", "junction", "lot", 20, 0, 20, 100, "parking", "local")
	parking.LengthMeters, parking.TransitionOnly, parking.ParkingAisleConnector = 100, true, true
	after := segment("after", "junction", "b", 20, 0, 20, -80, "road-b", "local")
	after.LengthMeters, after.ContinuityClass, after.SourceWayID = 80, "road", 8
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, parking, after}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("parking", 0, 95), graph.portion("after", 0, 80),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{20, -20}}, {Point: Point{20, -50}}, {Point: Point{20, -80}}}

	removeParkingAislesBeforeSameRoadContinuations(&result, raw, graph, 40, 15, 1e-6)

	if len(result.Traversals[0].Portions) != 3 {
		t.Fatalf("different-road parking connector removed: %+v", result)
	}
}

func TestShortParkingAisleBeforeSameRoadContinuationIsRetained(t *testing.T) {
	before := segment("before", "a", "junction", 0, 0, 20, 0, "road", "local")
	before.LengthMeters, before.ContinuityClass, before.SourceWayID = 20, "road", 7
	parking := segment("parking", "junction", "lot", 20, 0, 20, 10, "parking", "local")
	parking.LengthMeters, parking.TransitionOnly, parking.ParkingAisleConnector = 10, true, true
	after := segment("after", "junction", "b", 20, 0, 20, -80, "road", "local")
	after.LengthMeters, after.ContinuityClass, after.SourceWayID = 80, "road", 7
	graph := compileGraph(Graph{Segments: []DirectedSegment{before, parking, after}, MaxParkingAisleConnectorMeters: 200})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{
		graph.portion("before", 0, 20), graph.portion("parking", 0, 10), graph.portion("after", 0, 80),
	}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{20, 0}}, {Point: Point{20, -20}}, {Point: Point{20, -50}}, {Point: Point{20, -80}}}

	removeParkingAislesBeforeSameRoadContinuations(&result, raw, graph, 40, 15, 1e-6)

	if len(result.Traversals[0].Portions) != 3 {
		t.Fatalf("short parking connector removed: %+v", result)
	}
}

func TestRawSupportedDrivewayExtendsFromRoadEndpoint(t *testing.T) {
	road := segment("road", "a", "b", 0, 0, 10, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass, road.PhysicalID = 10, "road", "road"
	driveway := segment("driveway", "b", "c", 10, 0, 30, 0, "driveway", "local")
	driveway.LengthMeters, driveway.TransitionOnly, driveway.DrivewayConnector, driveway.PhysicalID = 20, true, true, "driveway"
	drivewayBack := segment("driveway-back", "c", "b", 30, 0, 10, 0, "driveway", "local")
	drivewayBack.LengthMeters, drivewayBack.TransitionOnly, drivewayBack.DrivewayConnector, drivewayBack.PhysicalID, drivewayBack.Direction = 20, true, true, "driveway", SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, driveway, drivewayBack}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{graph.portion("road", 0, 10)}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{20, 0}}, {Point: Point{30, 0}}}

	extendRawSupportedDriveways(&result, raw, graph, 40, 60, 15, 1e-6)

	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "driveway" || result.Traversals[0].Portions[2].SegmentID != "driveway-back" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDrivewayEndpointExtensionRequiresDirectedRawSupport(t *testing.T) {
	road := segment("road", "a", "b", 0, 0, 10, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass, road.PhysicalID = 10, "road", "road"
	driveway := segment("driveway", "b", "c", 10, 0, 30, 0, "driveway", "local")
	driveway.LengthMeters, driveway.TransitionOnly, driveway.DrivewayConnector, driveway.PhysicalID = 20, true, true, "driveway"
	drivewayBack := segment("driveway-back", "c", "b", 30, 0, 10, 0, "driveway", "local")
	drivewayBack.LengthMeters, drivewayBack.TransitionOnly, drivewayBack.DrivewayConnector, drivewayBack.PhysicalID, drivewayBack.Direction = 20, true, true, "driveway", SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, driveway, drivewayBack}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("road", 0, 10)}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{10, 10}}, {Point: Point{30, 10}}}

	extendRawSupportedDriveways(&result, raw, graph, 40, 60, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 1 {
		t.Fatalf("unsupported driveway added: %+v", result)
	}
}

func TestShortDrivewayEndpointExtensionAcceptsTravelPastMappedEndpoint(t *testing.T) {
	road := segment("road", "a", "b", -20, 0, 0, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass, road.PhysicalID = 20, "road", "road"
	driveway := segment("driveway", "b", "c", 0, 0, 6, 0, "driveway", "local")
	driveway.LengthMeters, driveway.TransitionOnly, driveway.DrivewayConnector, driveway.PhysicalID = 6, true, true, "driveway"
	drivewayBack := segment("driveway-back", "c", "b", 6, 0, 0, 0, "driveway", "local")
	drivewayBack.LengthMeters, drivewayBack.TransitionOnly, drivewayBack.DrivewayConnector, drivewayBack.PhysicalID, drivewayBack.Direction = 6, true, true, "driveway", SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, driveway, drivewayBack}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 3, Portions: []TraversedPortion{graph.portion("road", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{1, 2}}, {Point: Point{8, 2}}, {Point: Point{20, 2}}}

	extendRawSupportedDriveways(&result, raw, graph, 40, 30, 5, 1e-6)

	if len(result.Traversals[0].Portions) != 3 || result.Traversals[0].Portions[1].SegmentID != "driveway" || result.Traversals[0].Portions[2].SegmentID != "driveway-back" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDrivewayChainExtendsExistingOutAndBack(t *testing.T) {
	firstPhysical, secondPhysical := uuid.New(), uuid.New()
	first := segment("first", "road", "middle", 0, 0, 10, 0, "driveway", "local")
	first.LengthMeters, first.TransitionOnly, first.DrivewayConnector, first.PhysicalID, first.PhysicalSegmentID, first.SourceWayID = 10, true, true, "first", firstPhysical, 7
	firstBack := segment("first-back", "middle", "road", 10, 0, 0, 0, "driveway", "local")
	firstBack.LengthMeters, firstBack.TransitionOnly, firstBack.DrivewayConnector, firstBack.PhysicalID, firstBack.PhysicalSegmentID, firstBack.SourceWayID, firstBack.Direction = 10, true, true, "first", firstPhysical, 7, SegmentReverse
	second := segment("second", "middle", "end", 10, 0, 70, 0, "driveway", "local")
	second.LengthMeters, second.TransitionOnly, second.DrivewayConnector, second.PhysicalID, second.PhysicalSegmentID, second.SourceWayID = 60, true, true, "second", secondPhysical, 7
	secondBack := segment("second-back", "end", "middle", 70, 0, 10, 0, "driveway", "local")
	secondBack.LengthMeters, secondBack.TransitionOnly, secondBack.DrivewayConnector, secondBack.PhysicalID, secondBack.PhysicalSegmentID, secondBack.SourceWayID, secondBack.Direction = 60, true, true, "second", secondPhysical, 7, SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, firstBack, second, secondBack}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{graph.portion("first", 0, 10), graph.portion("first-back", 0, 10)}}}}
	raw := []Observation{{Point: Point{0, 0}}, {Point: Point{10, 0}}, {Point: Point{70, 0}}, {Point: Point{10, 0}}, {Point: Point{0, 0}}}

	extendRawSupportedRoadOutAndBackChains(&result, raw, graph, 150, 3, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "second" || got[2].SegmentID != "second-back" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDrivewayEndpointExtensionMayCrossOneShortSameRoadEdge(t *testing.T) {
	road := segment("road", "a", "b", -20, 0, 0, 0, "road", "local")
	road.LengthMeters, road.ContinuityClass, road.PhysicalID = 20, "road", "road"
	short := segment("short", "b", "c", 0, 0, 4, 0, "road", "local")
	short.LengthMeters, short.ContinuityClass, short.PhysicalID = 4, "road", "short"
	driveway := segment("driveway", "c", "d", 4, 0, 10, 0, "driveway", "local")
	driveway.LengthMeters, driveway.TransitionOnly, driveway.DrivewayConnector, driveway.PhysicalID = 6, true, true, "driveway"
	drivewayBack := segment("driveway-back", "d", "c", 10, 0, 4, 0, "driveway", "local")
	drivewayBack.LengthMeters, drivewayBack.TransitionOnly, drivewayBack.DrivewayConnector, drivewayBack.PhysicalID, drivewayBack.Direction = 6, true, true, "driveway", SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{road, short, driveway, drivewayBack}, MaxDrivewayConnectorMeters: 40})
	result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: 4, Portions: []TraversedPortion{graph.portion("road", 0, 20)}}}}
	raw := []Observation{{Point: Point{-10, 0}}, {Point: Point{0, 0}}, {Point: Point{4, 0}}, {Point: Point{11, 0}}, {Point: Point{20, 0}}}

	extendRawSupportedDriveways(&result, raw, graph, 40, 30, 5, 1e-6)

	got := result.Traversals[0].Portions
	if len(got) != 4 || got[1].SegmentID != "short" || got[2].SegmentID != "driveway" || got[3].SegmentID != "driveway-back" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDrivewayBridgeMayReversePhysicalRoadAtBoundary(t *testing.T) {
	roadForward := segment("road-f", "c", "d", 30, 0, 40, 0, "road", "local")
	roadForward.PhysicalID, roadForward.LengthMeters, roadForward.Direction, roadForward.ContinuityClass = "road", 10, SegmentForward, "road"
	roadReverse := segment("road-r", "d", "c", 40, 0, 30, 0, "road", "local")
	roadReverse.PhysicalID, roadReverse.LengthMeters, roadReverse.Direction, roadReverse.ContinuityClass = "road", 10, SegmentReverse, "road"
	driveway := segment("driveway", "c", "b", 30, 0, 10, 0, "driveway", "local")
	driveway.LengthMeters, driveway.TransitionOnly, driveway.DrivewayConnector = 20, true, true
	path := segment("path", "b", "a", 10, 0, 0, 0, "path", "local")
	path.LengthMeters, path.ContinuityClass = 10, "path"
	graph := compileGraph(Graph{Segments: []DirectedSegment{roadForward, roadReverse, driveway, path}, MaxDrivewayConnectorMeters: 40})
	before, after := graph.portion("road-f", 0, 5), graph.portion("path", 0, 10)
	observations := []Observation{{Point: Point{35, 0}}, {Point: Point{28, 0}}, {Point: Point{12, 0}}, {Point: Point{10, 0}}}
	connector, ok := drivewayBridgeRoute(before, after, graph, observations, 3, 40, 1e-6)
	if !ok || len(connector.portions) < 2 || connector.portions[0].SegmentID != "road-r" || !containsDrivewayConnector(connector.portions, graph) {
		t.Fatalf("connector=%+v ok=%t", connector, ok)
	}
}

func TestDirectionChangeUsesSamePhysicalPositionInsteadOfEndpointLoop(t *testing.T) {
	forward := segment("road/f", "a", "b", 0, 0, 100, 0, "road", "local")
	reverse := segment("road/r", "b", "a", 100, 0, 0, 0, "road", "local")
	forward.PhysicalID, reverse.PhysicalID = "road", "road"
	forward.Direction, reverse.Direction = SegmentForward, SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{forward, reverse}})
	route, ok := graph.route(
		Candidate{SegmentID: "road/f", AlongMeters: 80},
		Candidate{SegmentID: "road/r", AlongMeters: 30}, 100, 1e-9,
	)
	if !ok || route.distance != 10 || len(route.portions) != 1 || route.portions[0].SegmentID != "road/r" ||
		route.portions[0].FromMeter != 20 || route.portions[0].ToMeter != 30 {
		t.Fatalf("u-turn route=%+v ok=%t", route, ok)
	}
}

func TestLogicalPathSwitchCostPenalizesOnlyPathChanges(t *testing.T) {
	road := segment("road", "a", "b", 0, 0, 100, 0, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{road}})
	from := Candidate{SegmentID: "road", AlongMeters: 10, PathGroupID: "same", PathSwitchCost: 0.75}
	to := Candidate{SegmentID: "road", AlongMeters: 20, PathGroupID: "same", PathSwitchCost: 0.75}
	observations := []Observation{{Point: Point{10, 0}}, {Point: Point{20, 0}}}
	_, sameCost, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules())
	if !ok || sameCost != 0 {
		t.Fatalf("same path cost=%f ok=%t", sameCost, ok)
	}
	to.PathGroupID = "other"
	_, switchedCost, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules())
	if !ok || math.Abs(switchedCost-0.75) > 1e-9 {
		t.Fatalf("switched path cost=%f ok=%t", switchedCost, ok)
	}
	reverse := segment("road-reverse", "b", "a", 100, 0, 0, 0, "road", "local")
	graph = compileGraph(Graph{Segments: []DirectedSegment{road, reverse}})
	from.AlongMeters, from.PhysicalGroupID = 95, "physical"
	to.AlongMeters, to.PathGroupID, to.PhysicalGroupID, to.SegmentID, to.UTurnCost = 5, "same", "physical", "road-reverse", 4
	_, uTurnCost, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules())
	if !ok || math.Abs(uTurnCost-4) > 1e-9 {
		t.Fatalf("u-turn cost=%f ok=%t", uTurnCost, ok)
	}
}

func TestContinuityClassPenalizesPathToRoadButNotPathToPath(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 10, 0, "first", "local")
	second := segment("second", "b", "c", 10, 0, 20, 0, "second", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second}})
	from := Candidate{SegmentID: "first", AlongMeters: 5, ContinuityClass: "path", ContinuityClassCost: 2}
	to := Candidate{SegmentID: "second", AlongMeters: 5, ContinuityClass: "path", ContinuityClassCost: 2}
	observations := []Observation{{Point: Point{5, 0}}, {Point: Point{15, 0}}}
	_, sameCost, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules())
	if !ok || sameCost != 0 {
		t.Fatalf("same-class cost=%f ok=%t", sameCost, ok)
	}
	to.ContinuityClass = "road"
	_, switchedCost, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules())
	if !ok || math.Abs(switchedCost-2) > 1e-9 {
		t.Fatalf("class-switch cost=%f ok=%t", switchedCost, ok)
	}
}

func TestRoadAttributionOffsetsProvideBoundedTransitionSlack(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 30, 0, "first", "local")
	second := segment("second", "b", "c", 30, 0, 130, 0, "second", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second}})
	from := Candidate{SegmentID: "first", AlongMeters: 10}
	to := Candidate{SegmentID: "second", AlongMeters: 20}
	observations := []Observation{{Point: Point{10, 10}}, {Point: Point{15, 10}}}
	if _, _, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules()); ok {
		t.Fatal("corner-cut transition passed without projection slack")
	}
	from.AttributionOffsetMeters, to.AttributionOffsetMeters = 15, 15
	if _, _, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules()); !ok {
		t.Fatal("corner-cut transition failed with bounded projection slack")
	}
	from.AttributionOffsetMeters, to.AttributionOffsetMeters = 100, 100
	to.AlongMeters = 50
	if _, _, ok := transition(observations[0], observations[1], from, to, graph, ExperimentalRules()); ok {
		t.Fatal("projection slack exceeded its per-candidate cap")
	}
}

func TestShortSameDirectedSegmentGapBridgesAdjacentTraversals(t *testing.T) {
	road := segment("road", "a", "b", 0, 0, 100, 0, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{road}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("road", 0, 20)}},
		{FirstObservation: 5, LastObservation: 7, Portions: []TraversedPortion{graph.portion("road", 30, 50)}},
	}}
	bridgeShortSamePathTraversals(&result, graph, 15, 4, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 1 ||
		result.Traversals[0].Portions[0].FromMeter != 0 || result.Traversals[0].Portions[0].ToMeter != 50 {
		t.Fatalf("bridged result=%+v", result.Traversals)
	}
	result = Result{Splits: []Split{{BeforeObservation: 3, Reason: SplitTemporal}}, Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("road", 0, 20)}},
		{FirstObservation: 5, LastObservation: 7, Portions: []TraversedPortion{graph.portion("road", 30, 50)}},
	}}
	bridgeShortSamePathTraversals(&result, graph, 15, 4, 1e-9)
	if len(result.Traversals) != 2 {
		t.Fatalf("bridged across temporal split: %+v", result.Traversals)
	}
}

func TestShortSameLogicalPathGapBridgesConnectedPhysicalSegments(t *testing.T) {
	first := segment("first", "a", "b", 0, 0, 20, 0, "road", "local")
	second := segment("second", "b", "c", 20, 0, 40, 0, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("first", 0, 15)}},
		{FirstObservation: 6, LastObservation: 8, Portions: []TraversedPortion{graph.portion("second", 5, 20)}},
	}}
	bridgeShortSamePathTraversals(&result, graph, 15, 8, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 2 ||
		result.Traversals[0].Portions[0].ToMeter != 20 || result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("bridged result=%+v", result.Traversals)
	}
}

func TestTraversalBoundaryBacktracksCancelNestedExcursion(t *testing.T) {
	firstID, secondID := uuid.New(), uuid.New()
	portion := func(id uuid.UUID, segment string, direction SegmentDirection) TraversedPortion {
		return TraversedPortion{SegmentID: segment, PhysicalSegmentID: id, Direction: direction, SourceFromFraction: 0, SourceToFraction: 1}
	}
	result := Result{Observations: make([]DecodedObservation, 4), Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 0, Observations: []DecodedObservation{{ObservationIndex: 0, Status: ObservationMatched}}, Portions: []TraversedPortion{portion(firstID, "first/r", SegmentReverse)}},
		{FirstObservation: 1, LastObservation: 1, Observations: []DecodedObservation{{ObservationIndex: 1, Status: ObservationMatched}}, Portions: []TraversedPortion{portion(secondID, "second/f", SegmentForward)}},
		{FirstObservation: 2, LastObservation: 2, Observations: []DecodedObservation{{ObservationIndex: 2, Status: ObservationMatched}}, Portions: []TraversedPortion{portion(secondID, "second/r", SegmentReverse)}},
		{FirstObservation: 3, LastObservation: 3, Observations: []DecodedObservation{{ObservationIndex: 3, Status: ObservationMatched}}, Portions: []TraversedPortion{portion(firstID, "first/f", SegmentForward)}},
	}}
	cancelTraversalBoundaryBacktracks(&result, 1e-9)
	if len(result.Traversals) != 0 {
		t.Fatalf("nested excursion retained: %+v", result.Traversals)
	}
	for i, observation := range result.Observations {
		if observation.Status != ObservationRejected {
			t.Fatalf("observation %d status=%s", i, observation.Status)
		}
	}
}

func TestRawDistanceTieBreakPrefersCloserCandidateInsideEnvelope(t *testing.T) {
	rules := ExperimentalRules()
	graph := compileGraph(Graph{Segments: []DirectedSegment{segment("road", "a", "b", 0, 0, 100, 0, "road", "local")}})
	observation := Observation{AccuracyMeters: 1}
	near := Candidate{SegmentID: "road", DistanceMeters: 1, AttributionOffsetMeters: 10, RawDistanceCostWeight: 0.01}
	far := Candidate{SegmentID: "road", DistanceMeters: 10, AttributionOffsetMeters: 10, RawDistanceCostWeight: 0.01}
	nearCost := emissionCost(0, []Observation{observation}, near, graph, rules)
	farCost := emissionCost(0, []Observation{observation}, far, graph, rules)
	if nearCost >= farCost || math.Abs(nearCost-0.01) > 1e-9 || math.Abs(farCost-1) > 1e-9 {
		t.Fatalf("near=%f far=%f", nearCost, farCost)
	}
}

func TestShortExcursionIsReplacedByPlausibleSurroundingPath(t *testing.T) {
	first := segment("trail-1", "a", "b", 0, 0, 20, 0, "trail", "local")
	second := segment("trail-2", "b", "c", 20, 0, 120, 0, "trail", "local")
	road := segment("road", "x", "y", 0, 10, 5, 10, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second, road}})
	observations := make([]Observation, 13)
	for i := range observations {
		observations[i].Point = Point{X: float64(i * 10)}
	}
	result := Result{Observations: make([]DecodedObservation, len(observations)), Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("trail-1", 0, 10)}},
		{FirstObservation: 2, LastObservation: 2, Observations: []DecodedObservation{{ObservationIndex: 2, Status: ObservationMatched}}, Portions: []TraversedPortion{graph.portion("road", 0, 5)}},
		{FirstObservation: 12, LastObservation: 12, Portions: []TraversedPortion{graph.portion("trail-2", 90, 100)}},
	}}
	replaceShortExcursionsBetweenSamePath(&result, observations, graph, 10, 250, 64, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 2 ||
		result.Traversals[0].Portions[0].SegmentID != "trail-1" || result.Traversals[0].Portions[1].SegmentID != "trail-2" {
		t.Fatalf("replacement=%+v", result.Traversals)
	}
	if result.Observations[2].Status != ObservationRejected {
		t.Fatalf("excursion observation status=%s", result.Observations[2].Status)
	}
	result = Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("trail-1", 0, 10)}},
		{FirstObservation: 2, LastObservation: 2, Portions: []TraversedPortion{graph.portion("road", 0, 5)}},
		{FirstObservation: 3, LastObservation: 3, Portions: []TraversedPortion{graph.portion("trail-2", 90, 100)}},
	}}
	replaceShortExcursionsBetweenSamePath(&result, observations, graph, 10, 250, 64, 1e-9)
	if len(result.Traversals) != 3 {
		t.Fatalf("implausible route replaced: %+v", result.Traversals)
	}
}

func TestShortPortionExcursionUsesSurroundingLogicalPath(t *testing.T) {
	first := segment("trail-1", "a", "b", 0, 0, 20, 0, "trail", "local")
	second := segment("trail-2", "b", "c", 20, 0, 120, 0, "trail", "local")
	road := segment("road", "x", "y", 0, 10, 5, 10, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second, road}})
	result := Result{Traversals: []DecodedTraversal{{Portions: []TraversedPortion{
		graph.portion("trail-1", 0, 10), graph.portion("road", 0, 5), graph.portion("trail-2", 90, 100),
	}}}}
	replaceShortPortionExcursions(&result, graph, 10, 250, 1e-9)
	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[0].SegmentID != "trail-1" ||
		result.Traversals[0].Portions[0].ToMeter != 20 || result.Traversals[0].Portions[1].SegmentID != "trail-2" ||
		result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("replacement=%+v", result.Traversals[0].Portions)
	}
}

func TestMultiPortionExcursionUsesSurroundingLogicalPath(t *testing.T) {
	first := segment("road-1", "a", "b", 0, 0, 20, 0, "road", "local")
	second := segment("road-2", "b", "c", 20, 0, 40, 0, "road", "local")
	excursionOne := segment("other-1", "x", "y", 0, 10, 10, 10, "other", "local")
	excursionTwo := segment("other-2", "y", "z", 10, 10, 20, 10, "other", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second, excursionOne, excursionTwo}})
	result := Result{Traversals: []DecodedTraversal{{Portions: []TraversedPortion{
		graph.portion("road-1", 0, 15), graph.portion("other-1", 0, 10),
		graph.portion("other-2", 0, 10), graph.portion("road-2", 5, 20),
	}}}}
	replaceShortPortionExcursions(&result, graph, 30, 250, 1e-9)
	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[0].ToMeter != 20 ||
		result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("replacement=%+v", result.Traversals[0].Portions)
	}
}

func TestBoundaryShortExcursionUsesResumedLogicalPath(t *testing.T) {
	first := segment("trail-1", "a", "b", 0, 0, 20, 0, "trail", "local")
	second := segment("trail-2", "b", "c", 20, 0, 120, 0, "trail", "local")
	road := segment("road", "x", "y", 0, 10, 5, 10, "road", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second, road}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{graph.portion("trail-1", 0, 10), graph.portion("road", 0, 5)}},
		{FirstObservation: 3, LastObservation: 5, Portions: []TraversedPortion{graph.portion("trail-2", 90, 100)}},
	}}
	replaceBoundaryShortExcursions(&result, graph, 10, 250, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 2 ||
		result.Traversals[0].Portions[0].SegmentID != "trail-1" || result.Traversals[0].Portions[0].ToMeter != 20 ||
		result.Traversals[0].Portions[1].SegmentID != "trail-2" || result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("replacement=%+v", result.Traversals)
	}
}

func TestBoundaryMultiPortionExcursionUsesResumedLogicalPath(t *testing.T) {
	first := segment("road-1", "a", "b", 0, 0, 20, 0, "road", "local")
	second := segment("road-2", "b", "c", 20, 0, 40, 0, "road", "local")
	excursionOne := segment("other-1", "x", "y", 0, 10, 10, 10, "other", "local")
	excursionTwo := segment("other-2", "y", "z", 10, 10, 20, 10, "other", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second, excursionOne, excursionTwo}})
	result := Result{Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 2, Portions: []TraversedPortion{
			graph.portion("road-1", 0, 15), graph.portion("other-1", 0, 10), graph.portion("other-2", 0, 10),
		}},
		{FirstObservation: 3, LastObservation: 5, Portions: []TraversedPortion{graph.portion("road-2", 5, 20)}},
	}}
	replaceBoundaryShortExcursions(&result, graph, 30, 250, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 2 ||
		result.Traversals[0].Portions[0].ToMeter != 20 || result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("replacement=%+v", result.Traversals)
	}
}

func TestPlausibleSamePathBoundaryUsesRawDistanceAgreement(t *testing.T) {
	first := segment("trail-1", "a", "b", 0, 0, 20, 0, "trail", "local")
	second := segment("trail-2", "b", "c", 20, 0, 170, 0, "trail", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second}})
	observations := make([]Observation, 15)
	for i := range observations {
		observations[i].Point = Point{X: float64(i * 10)}
	}
	makeResult := func() Result {
		return Result{Traversals: []DecodedTraversal{
			{FirstObservation: 0, LastObservation: 0, Portions: []TraversedPortion{graph.portion("trail-1", 0, 10)}},
			{FirstObservation: 14, LastObservation: 14, Portions: []TraversedPortion{graph.portion("trail-2", 130, 150)}},
		}}
	}
	result := makeResult()
	bridgePlausibleSamePathTraversals(&result, observations, graph, 250, 64, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 2 ||
		result.Traversals[0].Portions[0].ToMeter != 20 || result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("plausible bridge=%+v", result.Traversals)
	}
	for i := range observations {
		observations[i].Point = Point{X: float64(i * 30)}
	}
	result = makeResult()
	bridgePlausibleSamePathTraversals(&result, observations, graph, 250, 64, 1e-9)
	if len(result.Traversals) != 2 {
		t.Fatalf("off-path detour bridged: %+v", result.Traversals)
	}
	first.IndependentPath, second.IndependentPath = true, true
	graph = compileGraph(Graph{Segments: []DirectedSegment{first, second}})
	result = makeResult()
	bridgePlausibleSamePathTraversals(&result, observations, graph, 250, 64, 1e-9)
	if len(result.Traversals) != 1 {
		t.Fatalf("independent path continuity was not preserved: %+v", result.Traversals)
	}
	for i := range observations {
		observations[i].Point = Point{X: float64(i * 10)}
	}
	result = makeResult()
	result.Splits = []Split{{BeforeObservation: 7, Reason: SplitSpatial}}
	bridgePlausibleSamePathTraversals(&result, observations, graph, 250, 64, 1e-9)
	if len(result.Traversals) != 2 {
		t.Fatalf("spatial split bridged: %+v", result.Traversals)
	}
}

func TestSamePathGapInsideAcceptedTraversalUsesConstrainedGraph(t *testing.T) {
	first := segment("trail-1", "a", "b", 0, 0, 20, 0, "trail", "local")
	second := segment("trail-2", "b", "c", 20, 0, 170, 0, "trail", "local")
	graph := compileGraph(Graph{Segments: []DirectedSegment{first, second}})
	result := Result{Traversals: []DecodedTraversal{{Portions: []TraversedPortion{
		graph.portion("trail-1", 0, 10), graph.portion("trail-2", 130, 150),
	}}}}
	bridgeSamePathPortionGaps(&result, graph, 250, 1e-9)
	if len(result.Traversals[0].Portions) != 2 || result.Traversals[0].Portions[0].ToMeter != 20 ||
		result.Traversals[0].Portions[1].FromMeter != 0 {
		t.Fatalf("bridged portions=%+v", result.Traversals[0].Portions)
	}
}

func TestShortRoadTraversalBetweenPathTraversalsUsesPathConnector(t *testing.T) {
	pathOne := segment("path-1", "a", "b", 0, 0, 20, 0, "path-one", "local")
	pathTwo := segment("path-2", "b", "c", 20, 0, 120, 0, "path-two", "local")
	road := segment("road", "x", "y", 0, 10, 60, 10, "road", "local")
	pathOne.ContinuityClass, pathTwo.ContinuityClass, road.ContinuityClass = "path", "path", "road"
	graph := compileGraph(Graph{Segments: []DirectedSegment{pathOne, pathTwo, road}})
	observations := make([]Observation, 12)
	for i := range observations {
		observations[i].Point = Point{X: float64(i * 10)}
	}
	result := Result{Observations: make([]DecodedObservation, len(observations)), Traversals: []DecodedTraversal{
		{FirstObservation: 0, LastObservation: 1, Portions: []TraversedPortion{graph.portion("path-1", 0, 10)}},
		{FirstObservation: 2, LastObservation: 3, Observations: []DecodedObservation{{ObservationIndex: 2, Status: ObservationMatched}}, Portions: []TraversedPortion{graph.portion("road", 0, 60)}},
		{FirstObservation: 10, LastObservation: 11, Portions: []TraversedPortion{graph.portion("path-2", 90, 100)}},
	}}
	replaceShortExcursionsBetweenSameClass(&result, observations, graph, 75, 500, 64, 1e-9)
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 2 ||
		result.Traversals[0].Portions[0].SegmentID != "path-1" || result.Traversals[0].Portions[1].SegmentID != "path-2" {
		t.Fatalf("replacement=%+v", result.Traversals)
	}
	if result.Observations[2].Status != ObservationRejected {
		t.Fatalf("excursion status=%s", result.Observations[2].Status)
	}
}

func TestExactImmediatePhysicalBacktrackDoesNotProduceCoverage(t *testing.T) {
	physicalID := uuid.New()
	first := TraversedPortion{PhysicalSegmentID: physicalID, Direction: SegmentForward, SourceFromFraction: 0.2, SourceToFraction: 0.8}
	reverse := TraversedPortion{PhysicalSegmentID: physicalID, Direction: SegmentReverse, SourceFromFraction: 0.2, SourceToFraction: 0.8}
	kept := TraversedPortion{PhysicalSegmentID: uuid.New(), Direction: SegmentForward, SourceFromFraction: 0, SourceToFraction: 1}
	got := cancelImmediateBacktracks([]TraversedPortion{kept, first, reverse}, 1e-9)
	if len(got) != 1 || got[0].PhysicalSegmentID != kept.PhysicalSegmentID {
		t.Fatalf("simplified portions=%+v", got)
	}
	reverse.SourceToFraction = 0.7
	if got := cancelImmediateBacktracks([]TraversedPortion{first, reverse}, 1e-9); len(got) != 2 {
		t.Fatalf("non-exact backtrack removed: %+v", got)
	}
	reverse.SourceToFraction = 0.8
	if got := mergeConsecutiveTraversedPortions([]TraversedPortion{first, reverse}, 1e-9); len(got) != 2 {
		t.Fatalf("same-direction coalescing removed reversal: %+v", got)
	}
	splitOne := TraversedPortion{SegmentID: "physical/r", PhysicalSegmentID: physicalID, Direction: SegmentReverse, FromMeter: 0, ToMeter: 5, SourceFromFraction: 0.5, SourceToFraction: 0.8}
	splitTwo := TraversedPortion{SegmentID: "physical/r", PhysicalSegmentID: physicalID, Direction: SegmentReverse, FromMeter: 5, ToMeter: 10, SourceFromFraction: 0.2, SourceToFraction: 0.5}
	forward := TraversedPortion{SegmentID: "physical/f", PhysicalSegmentID: physicalID, Direction: SegmentForward, FromMeter: 0, ToMeter: 10, SourceFromFraction: 0.2, SourceToFraction: 0.8}
	if got := cancelImmediateBacktracks([]TraversedPortion{splitOne, splitTwo, forward}, 1e-9); len(got) != 0 {
		t.Fatalf("split immediate backtrack retained: %+v", got)
	}
}

func TestUnresolvedLowConfidenceSequenceIsRejected(t *testing.T) {
	graph := Graph{Segments: []DirectedSegment{
		segment("equal-a", "a", "b", 0, 0, 100, 0, "a", "local"),
		segment("equal-b", "a", "b", 0, 0, 100, 0, "b", "local"),
	}}
	observations := syntheticObservations([]Point{{10, 0}, {30, 0}, {50, 0}}, nil)
	result := Match(observations, candidatesNear(observations, graph), graph, ExperimentalRules())
	if len(result.Traversals) != 0 {
		t.Fatalf("unresolved equal-cost paths were accepted: %+v", result)
	}
	for _, observation := range result.Observations {
		if observation.Status != ObservationRejected {
			t.Fatalf("status=%q, want rejected", observation.Status)
		}
	}
}

func TestMinimumTraversalSettingIsRetainedInDiagnostics(t *testing.T) {
	rules := ExperimentalRules().WithMinimumTraversalMeters(7.5)
	result := Match(nil, nil, Graph{}, rules)
	if rules.MinTraversalLengthMeters != 7.5 || result.MinimumTraversalMeters != 7.5 {
		t.Fatalf("rules=%+v result=%+v", rules, result)
	}
}

func syntheticFixtures() []syntheticFixture {
	intersection := Graph{Segments: []DirectedSegment{
		segment("main-west", "w", "i", 0, 0, 50, 0, "main", "local"),
		segment("main-east", "i", "e", 50, 0, 100, 0, "main", "local"),
		segment("cross-north", "i", "n", 50, 0, 50, 60, "cross", "local"),
	}}
	straight := syntheticFixture{
		name: "straight intersection without cross-street attribution", graph: intersection,
		points:   []Point{{10, 0}, {35, 0}, {50, 0}, {65, 0}, {90, 0}},
		expected: []string{"main-east", "main-west"}, cross: []string{"cross-north"},
	}
	return []syntheticFixture{
		straight,
		{
			name: "true turn", graph: intersection,
			points:   []Point{{10, 0}, {40, 0}, {50, 0}, {50, 15}, {50, 40}},
			expected: []string{"cross-north", "main-west"}, turns: []ExpectedTurn{{"main-west", "cross-north"}},
		},
		{
			name: "single-point cross-street excursion", graph: Graph{Segments: []DirectedSegment{
				segment("main", "w", "e", 0, 0, 100, 0, "main", "local"),
				segment("cross", "i", "n", 50, 0, 50, 60, "cross", "local"),
			}}, points: []Point{{10, 0}, {35, 0}, {50, 8}, {65, 0}, {90, 0}},
			expected: []string{"main"}, cross: []string{"cross"},
		},
		{
			name: "grade-separated crossing", graph: Graph{Segments: []DirectedSegment{
				segment("bridge", "bw", "be", 0, 0, 100, 0, "bridge", "local"),
				segment("underpass", "us", "un", 50, -50, 50, 50, "underpass", "local"),
			}}, points: []Point{{10, 0}, {40, 0}, {50, 0}, {60, 0}, {90, 0}},
			expected: []string{"bridge"}, cross: []string{"underpass"},
		},
		{
			name: "divided and parallel roads", graph: Graph{Segments: []DirectedSegment{
				segment("carriageway-a", "aw", "ae", 0, 0, 100, 0, "divided-a", "local"),
				segment("carriageway-b", "bw", "be", 0, 8, 100, 8, "divided-b", "local"),
			}}, points: []Point{{10, 2}, {35, 2}, {60, 4}, {85, 2}},
			expected: []string{"carriageway-a"}, cross: []string{"carriageway-b"},
		},
		{
			name: "stationary jitter", graph: Graph{Segments: []DirectedSegment{
				segment("nearby", "a", "b", 0, 0, 100, 0, "nearby", "local"),
			}}, points: []Point{{49, 0.2}, {49.3, -0.2}, {49.6, 0.1}, {49.8, 0}},
			expected: nil,
		},
		{
			name: "long temporal gap", graph: Graph{Segments: []DirectedSegment{
				segment("long-road", "a", "b", 0, 0, 120, 0, "long", "local"),
			}}, points: []Point{{10, 0}, {20, 0}, {70, 0}, {80, 0}}, times: []float64{0, 5, 100, 105},
			expected: []string{"long-road"}, split: SplitTemporal,
		},
		{
			name: "long spatial gap", graph: Graph{Segments: []DirectedSegment{
				segment("very-long-road", "a", "b", 0, 0, 400, 0, "long", "local"),
			}}, points: []Point{{10, 0}, {20, 0}, {280, 0}, {290, 0}},
			expected: []string{"very-long-road"}, split: SplitSpatial,
		},
		{
			name: "sparse trace", graph: Graph{Segments: []DirectedSegment{
				segment("sparse-road", "a", "b", 0, 0, 120, 0, "sparse", "local"),
			}}, points: []Point{{10, 0}, {100, 0}}, times: []float64{0, 10},
			expected: []string{"sparse-road"},
		},
		{
			name: "same-name disconnected locality paths", graph: Graph{Segments: []DirectedSegment{
				segment("main-locality-a", "a0", "a1", 0, 0, 40, 0, "main", "locality-a"),
				segment("main-locality-b", "b0", "b1", 60, 0, 100, 0, "main", "locality-b"),
			}}, points: []Point{{10, 0}, {30, 0}, {70, 0}, {90, 0}},
			expected: []string{"main-locality-a", "main-locality-b"}, split: SplitNetwork,
		},
		{
			name: "deliberate no-match", graph: Graph{Segments: []DirectedSegment{
				segment("far-road", "a", "b", 0, 0, 100, 0, "far", "local"),
			}}, points: []Point{{20, 30}, {40, 30}}, expected: nil,
			candidates: func(observations []Observation, graph Graph) [][]Candidate {
				return [][]Candidate{
					{{SegmentID: "far-road", Projected: Point{20, 0}, AlongMeters: 20, DistanceMeters: 30}},
					{{SegmentID: "far-road", Projected: Point{40, 0}, AlongMeters: 40, DistanceMeters: 30}},
				}
			}, split: SplitNoCandidate,
		},
		{
			name: "duplicate stationary observations", graph: Graph{Segments: []DirectedSegment{
				segment("stationary-road", "a", "b", 0, 0, 100, 0, "stationary", "local"),
			}}, points: []Point{{20, 0}, {20, 0}, {20, 0}, {20, 0}}, expected: nil,
		},
	}
}

func segment(id, fromNode, toNode string, x1, y1, x2, y2 float64, path, locality string) DirectedSegment {
	return DirectedSegment{ID: id, FromNode: fromNode, ToNode: toNode, From: Point{x1, y1}, To: Point{x2, y2}, LogicalPathID: path, LocalityID: locality}
}

func syntheticObservations(points []Point, elapsed []float64) []Observation {
	start := time.Unix(1_700_000_000, 0)
	result := make([]Observation, len(points))
	for i, point := range points {
		seconds := float64(i * 5)
		if len(elapsed) == len(points) {
			seconds = elapsed[i]
		}
		result[i] = Observation{Sequence: i, Point: point, Time: start.Add(time.Duration(seconds * float64(time.Second))), AccuracyMeters: 5}
	}
	return result
}

func candidatesNear(observations []Observation, graph Graph) [][]Candidate {
	result := make([][]Candidate, len(observations))
	for i, observation := range observations {
		for _, segment := range graph.Segments {
			candidate := project(observation.Point, segment)
			if candidate.DistanceMeters <= 30 {
				result[i] = append(result[i], candidate)
			}
		}
	}
	return result
}

func project(point Point, segment DirectedSegment) Candidate {
	dx, dy := segment.To.X-segment.From.X, segment.To.Y-segment.From.Y
	lengthSquared := dx*dx + dy*dy
	t := ((point.X-segment.From.X)*dx + (point.Y-segment.From.Y)*dy) / lengthSquared
	t = math.Max(0, math.Min(1, t))
	projected := Point{segment.From.X + t*dx, segment.From.Y + t*dy}
	return Candidate{SegmentID: segment.ID, Projected: projected, AlongMeters: t * math.Sqrt(lengthSquared), DistanceMeters: distance(point, projected)}
}

func containsSplit(splits []Split, reason SplitReason) bool {
	for _, split := range splits {
		if split.Reason == reason {
			return true
		}
	}
	return false
}

func cloneCandidates(source [][]Candidate) [][]Candidate {
	result := make([][]Candidate, len(source))
	for i := range source {
		result[i] = append([]Candidate(nil), source[i]...)
	}
	return result
}
