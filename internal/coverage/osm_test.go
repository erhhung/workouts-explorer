package coverage

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

var (
	testPhysicalSegment = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	testStartNode       = uuid.MustParse("10000000-0000-0000-0000-000000000001")
	testEndNode         = uuid.MustParse("10000000-0000-0000-0000-000000000002")
	testLogicalPath     = uuid.MustParse("20000000-0000-0000-0000-000000000001")
)

type fakeOSMTopology struct {
	candidateCalls int
	edgeCalls      int
	observations   []osm.MatcherObservation
	candidateLimit int
}

func (topology *fakeOSMTopology) Candidates(_ context.Context, observations []osm.MatcherObservation, limit int) ([]osm.MatcherCandidate, error) {
	// The bridge overfetches before applying movement policy and HMM bounds.
	topology.candidateLimit = max(topology.candidateLimit, limit)
	topology.candidateCalls++
	topology.observations = append(topology.observations, observations...)
	result := make([]osm.MatcherCandidate, len(observations))
	for i, observation := range observations {
		fraction := 0.2 + float64(i)*0.6
		longitude := fraction * 0.001
		tangent := 90.0
		result[i] = osm.MatcherCandidate{
			ObservationIndex: i, SegmentID: testPhysicalSegment, RegionID: "fixture:region", GenerationID: 1,
			SourceWayID: 10, SourceWayVersion: 1, DerivationVersion: 1,
			StartGraphNodeID: testStartNode, EndGraphNodeID: testEndNode, LogicalPathID: testLogicalPath,
			Highway: "residential", BroadClass: "road", MotorForwardAllowed: true, MotorReverseAllowed: true,
			LengthMeters: 111.2, Fraction: fraction, ProjectedLongitude: longitude,
			ProjectedLatitude: 0, TangentDegrees: &tangent, StartLongitude: 0, StartLatitude: 0,
			EndLongitude: 0.001, EndLatitude: 0,
		}
		_ = observation
	}
	return result, nil
}

func (topology *fakeOSMTopology) IncidentEdges(_ context.Context, nodes []uuid.UUID, _ int) ([]osm.MatcherIncidentEdge, error) {
	topology.edgeCalls++
	result := make([]osm.MatcherIncidentEdge, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, osm.MatcherIncidentEdge{
			RequestedNodeID: node, SegmentID: testPhysicalSegment, RegionID: "fixture:region", GenerationID: 1,
			SourceWayID: 10, SourceWayVersion: 1, DerivationVersion: 1,
			StartGraphNodeID: testStartNode, EndGraphNodeID: testEndNode, LogicalPathID: testLogicalPath,
			Highway: "residential", BroadClass: "road", MotorForwardAllowed: true, MotorReverseAllowed: true,
			LengthMeters: 111.2, StartLongitude: 0, StartLatitude: 0, EndLongitude: 0.001, EndLatitude: 0,
		})
	}
	return result, nil
}

func TestMatchOSMBridgesCandidatesAndPositiveTraversal(t *testing.T) {
	topology := &fakeOSMTopology{}
	heading, headingAccuracy := 90.0, 5.0
	started := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	input := []GeographicObservation{
		{1, 0.0002, 0, started, 3, &heading, &headingAccuracy},
		{2, 0.0008, 0, started.Add(10 * time.Second), 3, &heading, &headingAccuracy},
	}
	result, stats, err := MatchOSM(context.Background(), topology, input, ExperimentalRules(), DefaultOSMEvaluationOptions())
	if err != nil {
		t.Fatal(err)
	}
	if topology.candidateCalls != 1 || topology.edgeCalls != 1 {
		t.Fatalf("candidate calls=%d edge calls=%d", topology.candidateCalls, topology.edgeCalls)
	}
	if topology.candidateLimit != 256 {
		t.Fatalf("raw candidate query limit=%d, want 256", topology.candidateLimit)
	}
	if stats.CandidateCount != 2 || stats.GraphEdgeCount != 1 || stats.GraphNodeCount != 2 {
		t.Fatalf("stats=%+v", stats)
	}
	if len(result.Traversals) != 1 || len(result.Traversals[0].Portions) != 1 {
		t.Fatalf("result=%+v", result)
	}
	portion := result.Traversals[0].Portions[0]
	if portion.SegmentID != directedID(testPhysicalSegment, true) || math.Abs((portion.ToMeter-portion.FromMeter)-66.72) > 0.1 {
		t.Fatalf("portion=%+v", portion)
	}
	if portion.PhysicalSegmentID != testPhysicalSegment || portion.RegionID != "fixture:region" ||
		portion.GenerationID != 1 || portion.DerivationVersion != 1 ||
		portion.LogicalPathID != testLogicalPath.String() || portion.SourceWayID != 10 ||
		portion.SourceWayVersion != 1 || portion.Direction != SegmentForward ||
		math.Abs(portion.SourceFromFraction-0.2) > 1e-9 || math.Abs(portion.SourceToFraction-0.8) > 1e-9 {
		t.Fatalf("portion provenance=%+v", portion)
	}
}

func TestTraversedPortionConvertsForwardAndReverseSourceFractions(t *testing.T) {
	base := DirectedSegment{
		PhysicalID: testPhysicalSegment.String(), PhysicalSegmentID: testPhysicalSegment,
		RegionID: "fixture:region", GenerationID: 7,
		DerivationVersion: 3, LogicalPathID: testLogicalPath.String(), LocalityID: "42",
		SourceWayID: 99, SourceWayVersion: 4, LengthMeters: 100,
	}
	forward := base
	forward.ID, forward.FromNode, forward.ToNode, forward.Direction = "forward", "a", "b", SegmentForward
	reverse := base
	reverse.ID, reverse.FromNode, reverse.ToNode, reverse.Direction = "reverse", "b", "a", SegmentReverse
	graph := compileGraph(Graph{Segments: []DirectedSegment{forward, reverse}})

	forwardPortion := graph.portion("forward", 20, 80)
	reversePortion := graph.portion("reverse", 20, 80)
	if forwardPortion.SourceFromFraction != 0.2 || forwardPortion.SourceToFraction != 0.8 {
		t.Fatalf("forward portion=%+v", forwardPortion)
	}
	if math.Abs(reversePortion.SourceFromFraction-0.2) > 1e-9 || math.Abs(reversePortion.SourceToFraction-0.8) > 1e-9 ||
		reversePortion.Direction != SegmentReverse || reversePortion.LocalityID != "42" {
		t.Fatalf("reverse portion=%+v", reversePortion)
	}
}

func TestLocalPointWrapsAntimeridian(t *testing.T) {
	origin := GeographicObservation{Longitude: 179.999, Latitude: 10}
	point := localPoint(origin, -179.999, 10)
	if point.X < 200 || point.X > 230 || math.Abs(point.Y) > 1e-9 {
		t.Fatalf("point=%+v", point)
	}
}

func TestMatchOSMRejectsInvalidBounds(t *testing.T) {
	_, _, err := MatchOSM(context.Background(), &fakeOSMTopology{}, []GeographicObservation{{}}, ExperimentalRules(), OSMEvaluationOptions{})
	if err == nil {
		t.Fatal("invalid OSM evaluation bounds were accepted")
	}
}

func TestFootAttributionPrefersNearbyDrivableRoadOverPedestrianPath(t *testing.T) {
	road := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "residential", DistanceMeters: 4}
	sidewalk := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "footway", Tags: []byte(`{"footway":"sidewalk"}`), DistanceMeters: 0.2}
	path := osm.MatcherCandidate{ObservationIndex: 1, SegmentID: uuid.New(), Highway: "path", DistanceMeters: 0.1}
	policy := experimentalPathPolicy(MovementFoot)
	got, _, suppressed := attributionCandidates(MovementFoot, policy, []osm.MatcherCandidate{sidewalk, road, path}, DefaultRoadGeometryRules())
	if policy.version != ExperimentalPathPolicyV82 {
		t.Fatalf("policy version=%q", policy.version)
	}
	if suppressed != 1 || len(got) != 2 || got[0].SegmentID != road.SegmentID || got[1].SegmentID != path.SegmentID {
		t.Fatalf("candidates=%+v suppressed=%d", got, suppressed)
	}
}

func TestDeadEndEndpointAllowanceIsRoadModeAndTopologyScoped(t *testing.T) {
	neighborID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	sharedNode := uuid.MustParse("10000000-0000-0000-0000-000000000003")
	physical := map[uuid.UUID]physicalSegment{
		testPhysicalSegment: {
			id: testPhysicalSegment, startNode: testStartNode, endNode: testEndNode,
			length: 100, highway: "residential", tags: []byte(`{"lanes":"2"}`),
		},
		neighborID: {
			id: neighborID, startNode: sharedNode, endNode: testStartNode,
			length: 50, highway: "residential", tags: []byte(`{"lanes":"2"}`),
		},
	}
	candidates := [][]Candidate{{
		{SegmentID: directedID(testPhysicalSegment, true), PhysicalGroupID: testPhysicalSegment.String(), AlongMeters: 100, AttributionOffsetMeters: 6},
		{SegmentID: directedID(testPhysicalSegment, false), PhysicalGroupID: testPhysicalSegment.String(), AlongMeters: 0, AttributionOffsetMeters: 6},
		{SegmentID: directedID(testPhysicalSegment, true), PhysicalGroupID: testPhysicalSegment.String(), AlongMeters: 98.9, AttributionOffsetMeters: 6},
		{SegmentID: directedID(testPhysicalSegment, true), PhysicalGroupID: testPhysicalSegment.String(), AlongMeters: 0, AttributionOffsetMeters: 6},
	}}
	applyDeadEndEndpointAllowance(MovementFoot, candidates, physical, DefaultRoadGeometryRules())
	if candidates[0][0].AttributionOffsetMeters != 9 || candidates[0][1].AttributionOffsetMeters != 9 {
		t.Fatalf("terminal offsets=%f/%f", candidates[0][0].AttributionOffsetMeters, candidates[0][1].AttributionOffsetMeters)
	}
	if candidates[0][2].AttributionOffsetMeters != 6 || candidates[0][3].AttributionOffsetMeters != 6 {
		t.Fatalf("non-terminal offsets=%f/%f", candidates[0][2].AttributionOffsetMeters, candidates[0][3].AttributionOffsetMeters)
	}
	graph := compileGraph(Graph{Segments: []DirectedSegment{{
		ID: directedID(testPhysicalSegment, true), FromNode: "start", ToNode: "end", LengthMeters: 100,
	}}})
	terminal, nonTerminal := candidates[0][0], candidates[0][2]
	terminal.DistanceMeters, nonTerminal.DistanceMeters = 14.01, 14.01
	acceptedTerminal := acceptedCandidates(Observation{AccuracyMeters: 1}, []Candidate{terminal}, graph, ExperimentalRules())
	acceptedNonTerminal := acceptedCandidates(Observation{AccuracyMeters: 1}, []Candidate{nonTerminal}, graph, ExperimentalRules())
	if len(acceptedTerminal) != 1 || len(acceptedNonTerminal) != 0 {
		t.Fatalf("terminal=%+v non-terminal=%+v", acceptedTerminal, acceptedNonTerminal)
	}
	applyDeadEndEndpointAllowance(MovementSharedPublic, candidates, physical, DefaultRoadGeometryRules())
	if candidates[0][0].AttributionOffsetMeters != 9 {
		t.Fatal("shared-public mode changed endpoint attribution")
	}
}

func TestOnlyCrossingsAndTrafficIslandsQualifyAsContinuityConnectors(t *testing.T) {
	for _, tags := range []map[string]string{{"footway": "crossing"}, {"footway": "traffic_island"}} {
		if !pedestrianContinuityConnector("footway", tags) {
			t.Errorf("connector tags=%v rejected", tags)
		}
	}
	for _, test := range []struct {
		highway string
		tags    map[string]string
	}{{"footway", map[string]string{"footway": "sidewalk"}}, {"footway", map[string]string{"footway": "link"}}, {"path", map[string]string{"footway": "crossing"}}} {
		if pedestrianContinuityConnector(test.highway, test.tags) {
			t.Errorf("non-connector highway=%s tags=%v accepted", test.highway, test.tags)
		}
	}
}

func TestSidewalkCrossingAndTrafficIslandQualifyAsAccessoryConnectors(t *testing.T) {
	for _, tags := range []map[string]string{{"footway": "sidewalk"}, {"footway": "crossing"}, {"footway": "traffic_island"}, {"sidewalk": "yes"}} {
		if !pedestrianAccessoryConnector("footway", tags) {
			t.Errorf("accessory tags=%v rejected", tags)
		}
	}
	if pedestrianAccessoryConnector("footway", map[string]string{"footway": "link"}) ||
		pedestrianAccessoryConnector("path", map[string]string{}) {
		t.Fatal("generic link or path accepted as accessory connector")
	}
}

func TestFootAndBicycleCandidateRadiusIncludesOffsetRoadCenterlines(t *testing.T) {
	for _, mode := range []MovementMode{MovementFoot, MovementBicycle} {
		topology := &fakeOSMTopology{}
		options := DefaultOSMEvaluationOptions()
		options.MovementMode = mode
		_, _, err := MatchOSM(context.Background(), topology, []GeographicObservation{{Sequence: 1, AccuracyMeters: 1}}, ExperimentalRules(), options)
		if err != nil {
			t.Fatal(err)
		}
		if len(topology.observations) != 1 || topology.observations[0].RadiusMeters != footRoadDriftSearchRadiusMeters {
			t.Errorf("mode=%s observations=%+v", mode, topology.observations)
		}
	}
	shared := &fakeOSMTopology{}
	options := DefaultOSMEvaluationOptions()
	options.MovementMode = MovementSharedPublic
	_, _, err := MatchOSM(context.Background(), shared, []GeographicObservation{{Sequence: 1, AccuracyMeters: 1}}, ExperimentalRules(), options)
	if err != nil {
		t.Fatal(err)
	}
	if shared.observations[0].RadiusMeters >= footRoadDriftSearchRadiusMeters {
		t.Fatalf("shared-public radius unexpectedly expanded: %+v", shared.observations)
	}
}

func TestFootAndBicycleRejectRoadAccessorySegmentsFromCandidatesAndGraph(t *testing.T) {
	for _, mode := range []MovementMode{MovementFoot, MovementBicycle} {
		policy := experimentalPathPolicy(mode)
		for _, tags := range []string{`{"footway":"sidewalk"}`, `{"footway":"crossing"}`, `{"sidewalk":"both"}`} {
			if policy.eligible("footway", []byte(tags)) {
				t.Errorf("mode=%s accepted road accessory tags=%s", mode, tags)
			}
		}
		if !policy.eligible("footway", []byte(`{"surface":"paved"}`)) {
			t.Errorf("mode=%s rejected unassociated park footway", mode)
		}
	}
}

func TestFootAttributionFiveMeterThresholdIsInclusive(t *testing.T) {
	policy := experimentalPathPolicy(MovementFoot)
	for _, test := range []struct {
		distance       float64
		wantCandidates int
		wantSuppressed int
	}{{5, 1, 1}, {5.0001, 2, 0}} {
		road := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "service", DistanceMeters: test.distance}
		path := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "footway", DistanceMeters: 0.1}
		got, _, suppressed := attributionCandidates(MovementFoot, policy, []osm.MatcherCandidate{path, road}, DefaultRoadGeometryRules())
		if len(got) != test.wantCandidates || suppressed != test.wantSuppressed {
			t.Errorf("distance=%f candidates=%d suppressed=%d", test.distance, len(got), suppressed)
		}
	}
}

func TestSidewalkSuppressionAppliesToBicycleMode(t *testing.T) {
	road := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "residential", DistanceMeters: 4}
	path := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "cycleway", DistanceMeters: 0.1}
	got, _, suppressed := attributionCandidates(MovementBicycle, experimentalPathPolicy(MovementBicycle), []osm.MatcherCandidate{path, road}, DefaultRoadGeometryRules())
	if len(got) != 1 || got[0].SegmentID != road.SegmentID || suppressed != 1 {
		t.Fatalf("candidates=%+v suppressed=%d", got, suppressed)
	}
}

func TestNamedTrailRemainsCandidateNearRoad(t *testing.T) {
	road := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "residential", DistanceMeters: 4}
	trail := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "cycleway", Tags: []byte(`{"name":"Stevens Creek Trail","bridge":"yes"}`), DistanceMeters: 0.1}
	got, _, suppressed := attributionCandidates(MovementFoot, experimentalPathPolicy(MovementFoot), []osm.MatcherCandidate{trail, road}, DefaultRoadGeometryRules())
	if len(got) != 2 || suppressed != 0 {
		t.Fatalf("candidates=%+v suppressed=%d", got, suppressed)
	}
}

func TestPrivatePedestrianPathRemainsCandidateNearRoad(t *testing.T) {
	road := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "residential", DistanceMeters: 10}
	path := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), Highway: "footway", Tags: []byte(`{"access":"private"}`), DistanceMeters: 0.1}
	got, _, suppressed := attributionCandidates(MovementFoot, experimentalPathPolicy(MovementFoot), []osm.MatcherCandidate{path, road}, DefaultRoadGeometryRules())
	if len(got) != 2 || suppressed != 0 || got[0].SegmentID != path.SegmentID || got[1].SegmentID != road.SegmentID {
		t.Fatalf("candidates=%+v suppressed=%d", got, suppressed)
	}
}

func TestNamedTrailUsesStrongerPathContinuityCost(t *testing.T) {
	trail := osm.MatcherCandidate{Highway: "cycleway", Tags: []byte(`{"name":"Stevens Creek Trail","bridge":"yes"}`)}
	road := osm.MatcherCandidate{Highway: "residential", Tags: []byte(`{"name":"Dale Avenue"}`)}
	if got := candidatePathSwitchCost(MovementFoot, trail); got != 2 {
		t.Fatalf("trail switch cost=%f", got)
	}
	if got := candidatePathSwitchCost(MovementFoot, road); got != 0.75 {
		t.Fatalf("road switch cost=%f", got)
	}
	if got := candidatePathSwitchCost(MovementSharedPublic, trail); got != 0 {
		t.Fatalf("shared-public switch cost=%f", got)
	}
}

func TestPedestrianContinuityClassSeparatesPathsFromRoads(t *testing.T) {
	pathClass, pathCost := candidateContinuityClass(MovementFoot, osm.MatcherCandidate{Highway: "cycleway"})
	roadClass, roadCost := candidateContinuityClass(MovementFoot, osm.MatcherCandidate{Highway: "tertiary"})
	if pathClass != "path" || roadClass != "road" || pathCost != 8 || roadCost != 8 {
		t.Fatalf("path=%s/%f road=%s/%f", pathClass, pathCost, roadClass, roadCost)
	}
	if class, cost := candidateContinuityClass(MovementSharedPublic, osm.MatcherCandidate{Highway: "cycleway"}); class != "" || cost != 0 {
		t.Fatalf("shared-public class=%s/%f", class, cost)
	}
}

func TestParallelMappedSidewalkSupportsDriftedRoadWithoutBecomingCandidate(t *testing.T) {
	roadTangent, sidewalkTangent := 112.0, 292.0
	road := osm.MatcherCandidate{
		ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1,
		Highway: "residential", Tags: []byte(`{"lanes":"2"}`), DistanceMeters: 12, TangentDegrees: &roadTangent,
	}
	sidewalk := osm.MatcherCandidate{
		ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1,
		Highway: "footway", Tags: []byte(`{"footway":"sidewalk"}`), DistanceMeters: 2, TangentDegrees: &sidewalkTangent,
	}
	got, support, suppressed := attributionCandidates(MovementFoot, experimentalPathPolicy(MovementFoot), []osm.MatcherCandidate{sidewalk, road}, DefaultRoadGeometryRules())
	if len(got) != 1 || got[0].SegmentID != road.SegmentID || suppressed != 1 || support[roadSupportKey(road)] != 2 {
		t.Fatalf("candidates=%+v support=%+v suppressed=%d", got, support, suppressed)
	}
	perpendicular := 22.0
	sidewalk.TangentDegrees = &perpendicular
	_, support, _ = attributionCandidates(MovementFoot, experimentalPathPolicy(MovementFoot), []osm.MatcherCandidate{sidewalk, road}, DefaultRoadGeometryRules())
	if len(support) != 0 {
		t.Fatalf("perpendicular sidewalk supported road: %+v", support)
	}
	sidewalk.TangentDegrees = &sidewalkTangent
	road.DistanceMeters = 25
	_, support, _ = attributionCandidates(MovementFoot, experimentalPathPolicy(MovementFoot), []osm.MatcherCandidate{sidewalk, road}, DefaultRoadGeometryRules())
	if len(support) != 0 {
		t.Fatalf("distant parallel carriageway supported: %+v", support)
	}
}

func TestRoadAccessoriesAreTransitionEligibleButNotCandidateEligible(t *testing.T) {
	policy := experimentalPathPolicy(MovementFoot)
	tags := []byte(`{"footway":"crossing"}`)
	if policy.eligible("footway", tags) || !policy.transitionEligible("footway", tags) {
		t.Fatalf("crossing eligibility candidate=%t transition=%t", policy.eligible("footway", tags), policy.transitionEligible("footway", tags))
	}
	if !policy.transitionEligible("footway", []byte(`{"footway":"crossing","access":"private"}`)) {
		t.Fatal("private crossing rejected for transition")
	}
	if policy.transitionEligible("footway", []byte(`{"footway":"crossing","access":"no"}`)) {
		t.Fatal("forbidden crossing accepted for transition")
	}
}

func TestFootAndBicycleExcludeParkingAndDrivewayServiceRoads(t *testing.T) {
	for _, mode := range []MovementMode{MovementFoot, MovementBicycle} {
		policy := experimentalPathPolicy(mode)
		for _, service := range []string{"parking_aisle", "driveway", "drive-through", "emergency_access"} {
			tags := []byte(`{"service":"` + service + `"}`)
			transitionExpected := service == "driveway" || service == "parking_aisle"
			if policy.eligible("service", tags) || policy.transitionEligible("service", tags) != transitionExpected {
				t.Fatalf("mode=%s service=%s was eligible", mode, service)
			}
		}
		if policy.transitionEligible("service", []byte(`{"service":"driveway","access":"private"}`)) {
			t.Fatalf("mode=%s private driveway was transition eligible", mode)
		}
		if !policy.transitionEligible("service", []byte(`{"service":"parking_aisle","access":"private"}`)) ||
			policy.transitionEligible("service", []byte(`{"service":"parking_aisle","access":"no"}`)) {
			t.Fatalf("mode=%s parking aisle access policy is incorrect", mode)
		}
		if !policy.eligible("service", []byte(`{"service":"alley"}`)) {
			t.Fatalf("mode=%s public alley was excluded", mode)
		}
	}
}

func TestCandidateLimitRetainsDistantSidewalkSupportedRoad(t *testing.T) {
	near := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1, DistanceMeters: 2}
	supported := osm.MatcherCandidate{ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1, DistanceMeters: 32}
	got := limitAttributionCandidates([]osm.MatcherCandidate{near, supported}, map[string]float64{roadSupportKey(supported): 1}, 1)
	if len(got) != 1 || got[0].SegmentID != supported.SegmentID {
		t.Fatalf("limited candidates=%+v", got)
	}
}

func TestNearbyCrossingPromotesContinuationAndSuppressesCrossedRoad(t *testing.T) {
	crossingTangent, continuationTangent, crossedTangent := 5.0, 185.0, 95.0
	crossing := osm.MatcherCandidate{
		ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1,
		Highway: "footway", Tags: []byte(`{"footway":"crossing"}`), DistanceMeters: 1, TangentDegrees: &crossingTangent,
	}
	continuation := osm.MatcherCandidate{
		ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1,
		Highway: "residential", Tags: []byte(`{"lanes":"2"}`), DistanceMeters: 8, TangentDegrees: &continuationTangent,
	}
	crossed := osm.MatcherCandidate{
		ObservationIndex: 0, SegmentID: uuid.New(), RegionID: "fixture", GenerationID: 1,
		Highway: "primary", Tags: []byte(`{"lanes":"5","oneway":"yes"}`), DistanceMeters: 2, TangentDegrees: &crossedTangent,
	}
	got, support, suppressed := attributionCandidates(MovementFoot, experimentalPathPolicy(MovementFoot), []osm.MatcherCandidate{crossing, crossed, continuation}, DefaultRoadGeometryRules())
	if len(got) != 1 || got[0].SegmentID != continuation.SegmentID || support[roadSupportKey(continuation)] != 1 || suppressed != 2 {
		t.Fatalf("candidates=%+v support=%+v suppressed=%d", got, support, suppressed)
	}
}
