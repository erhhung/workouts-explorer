package coverage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

const earthRadiusMeters = 6371008.8
const deadEndEndpointProjectionToleranceMeters = 1.0

type GeographicObservation struct {
	Sequence        int
	Longitude       float64
	Latitude        float64
	Time            time.Time
	AccuracyMeters  float64
	HeadingDegrees  *float64
	HeadingAccuracy *float64
}

type OSMEvaluationOptions struct {
	MaxCandidatesPerObservation int
	MaxExpansionLayers          int
	MaxGraphNodes               int
	MaxGraphEdges               int
	MovementMode                MovementMode
	Sampling                    SamplingRules
	RoadGeometry                RoadGeometryRules
}

func DefaultOSMEvaluationOptions() OSMEvaluationOptions {
	return OSMEvaluationOptions{
		MaxCandidatesPerObservation: 32,
		MaxExpansionLayers:          64,
		MaxGraphNodes:               2048,
		MaxGraphEdges:               4096,
		MovementMode:                MovementSharedPublic,
		Sampling:                    ExperimentalSamplingRules(),
		RoadGeometry:                DefaultRoadGeometryRules(),
	}
}

type OSMEvaluationStats struct {
	CandidateCount                 int
	GraphNodeCount                 int
	GraphEdgeCount                 int
	ExpansionLayers                int
	InputObservationCount          int
	SampledObservationCount        int
	SuppressedPedestrianCandidates int
}

type osmTopology interface {
	Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error)
	IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error)
}

// MatchOSM bridges private observations to the read-only public OSM snapshot.
// It is an experimental evaluator: every physical edge is considered in both
// directions until ADR 0009 accepts mode/access policy.
func MatchOSM(ctx context.Context, topology osmTopology, input []GeographicObservation, rules Rules, options OSMEvaluationOptions) (Result, OSMEvaluationStats, error) {
	if topology == nil || len(input) == 0 {
		return Result{RulesVersion: rules.Version, MinimumTraversalMeters: rules.MinTraversalLengthMeters}, OSMEvaluationStats{}, nil
	}
	inputCount := len(input)
	input = SampleGeographicObservations(input, options.Sampling, rules)
	if options.MaxCandidatesPerObservation < 1 || options.MaxExpansionLayers < 0 ||
		options.MaxGraphNodes < 1 || options.MaxGraphEdges < 1 || !options.RoadGeometry.valid() {
		return Result{}, OSMEvaluationStats{}, fmt.Errorf("invalid OSM evaluation bounds")
	}
	origin := input[0]
	observations := make([]Observation, len(input))
	supplied := make([][]Candidate, len(input))
	physical := make(map[uuid.UUID]physicalSegment)
	policy := experimentalPathPolicy(options.MovementMode)
	stats := OSMEvaluationStats{InputObservationCount: inputCount, SampledObservationCount: len(input)}
	for i := range input {
		observations[i] = Observation{input[i].Sequence, localPoint(origin, input[i].Longitude, input[i].Latitude), input[i].Time,
			input[i].AccuracyMeters, input[i].HeadingDegrees, input[i].HeadingAccuracy}
	}

	for batchStart := 0; batchStart < len(input); batchStart += 256 {
		batchEnd := min(len(input), batchStart+256)
		query := make([]osm.MatcherObservation, batchEnd-batchStart)
		for i := batchStart; i < batchEnd; i++ {
			accuracy := effectiveAccuracy(input[i].AccuracyMeters, rules)
			radius := math.Min(rules.MaxCandidateDistanceMeters, rules.AccuracyRadiusMultiplier*accuracy)
			if (options.MovementMode == MovementFoot || options.MovementMode == MovementBicycle) && radius < footRoadDriftSearchRadiusMeters {
				radius = footRoadDriftSearchRadiusMeters
			}
			query[i-batchStart] = osm.MatcherObservation{
				Longitude: input[i].Longitude, Latitude: input[i].Latitude,
				RadiusMeters: radius,
			}
		}
		queryLimit := max(256, options.MaxCandidatesPerObservation*8)
		if queryLimit > 256 {
			queryLimit = 256
		}
		candidates, err := topology.Candidates(ctx, query, queryLimit)
		if err != nil {
			return Result{}, stats, err
		}
		stats.CandidateCount += len(candidates)
		preferred, sidewalkSupport, suppressed := attributionCandidates(options.MovementMode, policy, candidates, options.RoadGeometry)
		preferred = limitAttributionCandidates(preferred, sidewalkSupport, options.MaxCandidatesPerObservation)
		stats.SuppressedPedestrianCandidates += suppressed
		for _, candidate := range preferred {
			observationIndex := batchStart + candidate.ObservationIndex
			if observationIndex < batchStart || observationIndex >= batchEnd {
				return Result{}, stats, fmt.Errorf("OSM candidate returned an invalid observation index")
			}
			segment := physicalFromCandidate(candidate)
			if !policy.eligible(segment.highway, segment.tags) {
				continue
			}
			physical[segment.id] = segment
			projected := localPoint(origin, candidate.ProjectedLongitude, candidate.ProjectedLatitude)
			forwardTangent := candidate.TangentDegrees
			reverseTangent := reverseHeading(candidate.TangentDegrees)
			forward, reverse := policy.directions(segment.tags, segment.motorForward, segment.motorReverse)
			attributionOffset := roadAttributionOffset(options.MovementMode, candidate.Highway, candidate.Tags, options.RoadGeometry)
			if supportDistance, supported := sidewalkSupport[roadSupportKey(candidate)]; supported {
				attributionOffset = math.Max(attributionOffset, candidate.DistanceMeters-supportDistance+options.RoadGeometry.DirectionalDriftMeters)
			} else if candidate.DistanceMeters <= footRoadCandidateSearchRadiusMeters &&
				observationWindowAligned(observationIndex, observations, candidate.TangentDegrees, rules) {
				attributionOffset += options.RoadGeometry.DirectionalDriftMeters
			}
			pathSwitchCost := candidatePathSwitchCost(options.MovementMode, candidate)
			continuityClass, continuityClassCost := candidateContinuityClass(options.MovementMode, candidate)
			rawDistanceCostWeight := 0.0
			if options.MovementMode == MovementFoot || options.MovementMode == MovementBicycle {
				if drivableRoad(candidate.Highway, decodeTags(candidate.Tags)) {
					rawDistanceCostWeight = 0.01
				}
			}
			if forward {
				supplied[observationIndex] = append(supplied[observationIndex], Candidate{
					SegmentID: directedID(segment.id, true), Projected: projected,
					AlongMeters: candidate.Fraction * candidate.LengthMeters, DistanceMeters: candidate.DistanceMeters,
					AttributionOffsetMeters: attributionOffset, TangentDegrees: forwardTangent,
					PathGroupID: candidate.LogicalPathID.String(), PathSwitchCost: pathSwitchCost,
					PhysicalGroupID: candidate.SegmentID.String(), UTurnCost: 4,
					RawDistanceCostWeight: rawDistanceCostWeight,
					ContinuityClass:       continuityClass, ContinuityClassCost: continuityClassCost,
				})
			}
			if reverse {
				supplied[observationIndex] = append(supplied[observationIndex], Candidate{
					SegmentID: directedID(segment.id, false), Projected: projected,
					AlongMeters: (1 - candidate.Fraction) * candidate.LengthMeters, DistanceMeters: candidate.DistanceMeters,
					AttributionOffsetMeters: attributionOffset, TangentDegrees: reverseTangent,
					PathGroupID: candidate.LogicalPathID.String(), PathSwitchCost: pathSwitchCost,
					PhysicalGroupID: candidate.SegmentID.String(), UTurnCost: 4,
					RawDistanceCostWeight: rawDistanceCostWeight,
					ContinuityClass:       continuityClass, ContinuityClassCost: continuityClassCost,
				})
			}
		}
	}

	if err := expandOSMGraph(ctx, topology, physical, policy, options, rules.MaxNetworkDistanceMeters, &stats); err != nil {
		return Result{}, stats, err
	}
	applyDeadEndEndpointAllowance(options.MovementMode, supplied, physical, options.RoadGeometry)
	graph := Graph{Segments: make([]DirectedSegment, 0, len(physical)*2)}
	if options.MovementMode == MovementFoot || options.MovementMode == MovementBicycle {
		graph.MaxContinuityConnectorMeters = maxContinuityConnectorMeters
		graph.MaxDrivewayConnectorMeters = maxDrivewayConnectorMeters
		graph.MaxAccessoryConnectorMeters = maxAccessoryConnectorMeters
		graph.MaxParkingAisleConnectorMeters = maxParkingAisleConnectorMeters
	}
	ids := make([]uuid.UUID, 0, len(physical))
	for id := range physical {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		segment := physical[id]
		forward, reverse := policy.directions(segment.tags, segment.motorForward, segment.motorReverse)
		if forward {
			graph.Segments = append(graph.Segments, segment.directed(origin, true))
		}
		if reverse {
			graph.Segments = append(graph.Segments, segment.directed(origin, false))
		}
	}
	return Match(observations, supplied, graph, rules), stats, nil
}

func applyDeadEndEndpointAllowance(mode MovementMode, candidates [][]Candidate, physical map[uuid.UUID]physicalSegment, rules RoadGeometryRules) {
	if (mode != MovementFoot && mode != MovementBicycle) || rules.DeadEndEndpointAllowanceMeters <= 0 {
		return
	}
	roadDegrees := make(map[uuid.UUID]int)
	for _, segment := range physical {
		if !drivableRoad(segment.highway, decodeTags(segment.tags)) {
			continue
		}
		roadDegrees[segment.startNode]++
		roadDegrees[segment.endNode]++
	}
	for observationIndex := range candidates {
		for candidateIndex := range candidates[observationIndex] {
			candidate := &candidates[observationIndex][candidateIndex]
			physicalID, err := uuid.Parse(candidate.PhysicalGroupID)
			if err != nil {
				continue
			}
			segment, ok := physical[physicalID]
			if !ok || !drivableRoad(segment.highway, decodeTags(segment.tags)) || segment.length <= 0 {
				continue
			}
			fraction := candidate.AlongMeters / segment.length
			if candidate.SegmentID == directedID(physicalID, false) {
				fraction = 1 - fraction
			}
			nearStart := fraction*segment.length <= deadEndEndpointProjectionToleranceMeters
			nearEnd := (1-fraction)*segment.length <= deadEndEndpointProjectionToleranceMeters
			if (nearStart && roadDegrees[segment.startNode] == 1) || (nearEnd && roadDegrees[segment.endNode] == 1) {
				candidate.AttributionOffsetMeters += rules.DeadEndEndpointAllowanceMeters
			}
		}
	}
}

func candidateContinuityClass(mode MovementMode, candidate osm.MatcherCandidate) (string, float64) {
	if mode != MovementFoot && mode != MovementBicycle {
		return "", 0
	}
	if class := continuityClassForWay(candidate.Highway, candidate.Tags); class != "" {
		return class, 8
	}
	return "", 0
}

func continuityClassForWay(highway string, rawTags json.RawMessage) string {
	if pedestrianOnlyPath(highway, rawTags) {
		return "path"
	}
	if drivableRoad(highway, decodeTags(rawTags)) {
		return "road"
	}
	return ""
}

func candidatePathSwitchCost(mode MovementMode, candidate osm.MatcherCandidate) float64 {
	if mode != MovementFoot && mode != MovementBicycle {
		return 0
	}
	if independentPedestrianPath(candidate) {
		return 2
	}
	return 0.75
}

func limitAttributionCandidates(candidates []osm.MatcherCandidate, sidewalkSupport map[string]float64, limit int) []osm.MatcherCandidate {
	candidates = append([]osm.MatcherCandidate(nil), candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		first, firstSupported := sidewalkSupport[roadSupportKey(candidates[i])]
		second, secondSupported := sidewalkSupport[roadSupportKey(candidates[j])]
		if firstSupported != secondSupported {
			return firstSupported
		}
		if firstSupported && first != second {
			return first < second
		}
		return candidates[i].DistanceMeters < candidates[j].DistanceMeters
	})
	counts := make(map[int]int)
	result := make([]osm.MatcherCandidate, 0, min(len(candidates), limit))
	for _, candidate := range candidates {
		if counts[candidate.ObservationIndex] >= limit {
			continue
		}
		counts[candidate.ObservationIndex]++
		result = append(result, candidate)
	}
	return result
}

func attributionCandidates(mode MovementMode, policy pathPolicy, candidates []osm.MatcherCandidate, geometry RoadGeometryRules) ([]osm.MatcherCandidate, map[string]float64, int) {
	roadNearby := make(map[int]bool)
	sidewalkSupport := make(map[string]float64)
	nearestCrossing := make(map[int]osm.MatcherCandidate)
	if mode == MovementFoot || mode == MovementBicycle {
		for _, candidate := range candidates {
			if candidate.DistanceMeters <= footRoadAttributionDistanceMeters && drivableRoad(candidate.Highway, decodeTags(candidate.Tags)) && policy.eligible(candidate.Highway, candidate.Tags) {
				roadNearby[candidate.ObservationIndex] = true
			}
			if candidate.DistanceMeters <= 10 && pedestrianCrossing(candidate) {
				prior, exists := nearestCrossing[candidate.ObservationIndex]
				if !exists || candidate.DistanceMeters < prior.DistanceMeters {
					nearestCrossing[candidate.ObservationIndex] = candidate
				}
			}
		}
		for _, road := range candidates {
			if !drivableRoad(road.Highway, decodeTags(road.Tags)) || !policy.eligible(road.Highway, road.Tags) || road.DistanceMeters > footRoadDriftSearchRadiusMeters {
				continue
			}
			for _, sidewalk := range candidates {
				if sidewalk.ObservationIndex != road.ObservationIndex || sidewalk.DistanceMeters > footRoadCandidateSearchRadiusMeters ||
					!sidewalkRoadSupport(sidewalk) || !parallelTangents(road.TangentDegrees, sidewalk.TangentDegrees, 25) ||
					road.DistanceMeters > sidewalk.DistanceMeters+roadAttributionOffset(mode, road.Highway, road.Tags, geometry)+5 {
					continue
				}
				key := roadSupportKey(road)
				if prior, exists := sidewalkSupport[key]; !exists || sidewalk.DistanceMeters < prior {
					sidewalkSupport[key] = sidewalk.DistanceMeters
				}
			}
			if crossing, exists := nearestCrossing[road.ObservationIndex]; exists && parallelTangents(road.TangentDegrees, crossing.TangentDegrees, 30) {
				key := roadSupportKey(road)
				if prior, supported := sidewalkSupport[key]; !supported || crossing.DistanceMeters < prior {
					sidewalkSupport[key] = crossing.DistanceMeters
				}
			}
		}
	}
	result := make([]osm.MatcherCandidate, 0, len(candidates))
	suppressed := 0
	for _, candidate := range candidates {
		if !policy.eligible(candidate.Highway, candidate.Tags) {
			if (mode == MovementFoot || mode == MovementBicycle) && pedestrianRoadAccessory(candidate.Highway, decodeTags(candidate.Tags)) {
				suppressed++
			}
			continue
		}
		if roadNearby[candidate.ObservationIndex] && pedestrianOnlyPath(candidate.Highway, candidate.Tags) && !independentPedestrianPath(candidate) {
			suppressed++
			continue
		}
		if crossing, exists := nearestCrossing[candidate.ObservationIndex]; exists &&
			dividedMajorCarriageway(candidate) && transverseTangents(candidate.TangentDegrees, crossing.TangentDegrees, 35) {
			suppressed++
			continue
		}
		result = append(result, candidate)
	}
	return result, sidewalkSupport, suppressed
}

func independentPedestrianPath(candidate osm.MatcherCandidate) bool {
	return independentPedestrianWay(candidate.Highway, candidate.Tags)
}

func independentPedestrianWay(highway string, rawTags json.RawMessage) bool {
	tags := decodeTags(rawTags)
	return pedestrianOnlyPath(highway, rawTags) && (tags["name"] != "" || tags["bridge"] == "yes")
}

func dividedMajorCarriageway(candidate osm.MatcherCandidate) bool {
	if candidate.Highway != "primary" && candidate.Highway != "secondary" {
		return false
	}
	tags := decodeTags(candidate.Tags)
	return tags["oneway"] == "yes" || tags["oneway"] == "1" || tags["oneway"] == "true"
}

func pedestrianCrossing(candidate osm.MatcherCandidate) bool {
	tags := decodeTags(candidate.Tags)
	return candidate.Highway == "footway" && (tags["footway"] == "crossing" || tags["footway"] == "traffic_island")
}

func transverseTangents(first, second *float64, parallelTolerance float64) bool {
	return first != nil && second != nil && finite(*first) && finite(*second) && !parallelTangents(first, second, parallelTolerance)
}

func observationWindowAligned(index int, observations []Observation, tangent *float64, rules Rules) bool {
	for i := max(0, index-2); i <= min(len(observations)-1, index+2); i++ {
		if heading, ok := observationHeading(i, observations, rules); ok && parallelTangents(tangent, &heading, 25) {
			return true
		}
	}
	return false
}

func sidewalkRoadSupport(candidate osm.MatcherCandidate) bool {
	tags := decodeTags(candidate.Tags)
	return candidate.Highway == "footway" && tags["footway"] == "sidewalk"
}

func parallelTangents(first, second *float64, maximumDifference float64) bool {
	if first == nil || second == nil || !finite(*first) || !finite(*second) {
		return false
	}
	difference := math.Mod(math.Abs(*first-*second), 180)
	return math.Min(difference, 180-difference) <= maximumDifference
}

func roadSupportKey(candidate osm.MatcherCandidate) string {
	return fmt.Sprintf("%d/%s/%s/%d", candidate.ObservationIndex, candidate.SegmentID, candidate.RegionID, candidate.GenerationID)
}

type physicalSegment struct {
	id                    uuid.UUID
	region                string
	generation            int64
	derivationVersion     int
	sourceWay             int64
	sourceWayVersion      int
	startNode, endNode    uuid.UUID
	logicalPath           uuid.UUID
	locality              *int64
	length                float64
	highway               string
	tags                  json.RawMessage
	motorForward          bool
	motorReverse          bool
	startLongitude        float64
	startLatitude         float64
	endLongitude          float64
	endLatitude           float64
	transitionOnly        bool
	continuityConnector   bool
	drivewayConnector     bool
	accessoryConnector    bool
	parkingAisleConnector bool
}

func physicalFromCandidate(candidate osm.MatcherCandidate) physicalSegment {
	return physicalSegment{candidate.SegmentID, candidate.RegionID, candidate.GenerationID,
		candidate.DerivationVersion, candidate.SourceWayID, candidate.SourceWayVersion,
		candidate.StartGraphNodeID, candidate.EndGraphNodeID,
		candidate.LogicalPathID, candidate.LocalityRelationID, candidate.LengthMeters,
		candidate.Highway, candidate.Tags, candidate.MotorForwardAllowed, candidate.MotorReverseAllowed,
		candidate.StartLongitude, candidate.StartLatitude, candidate.EndLongitude, candidate.EndLatitude, false, false, false, false, false}
}

func physicalFromEdge(edge osm.MatcherIncidentEdge) physicalSegment {
	return physicalSegment{edge.SegmentID, edge.RegionID, edge.GenerationID,
		edge.DerivationVersion, edge.SourceWayID, edge.SourceWayVersion,
		edge.StartGraphNodeID, edge.EndGraphNodeID,
		edge.LogicalPathID, edge.LocalityRelationID, edge.LengthMeters,
		edge.Highway, edge.Tags, edge.MotorForwardAllowed, edge.MotorReverseAllowed,
		edge.StartLongitude, edge.StartLatitude, edge.EndLongitude, edge.EndLatitude, false, false, false, false, false}
}

func (segment physicalSegment) directed(origin GeographicObservation, forward bool) DirectedSegment {
	fromNode, toNode := segment.startNode, segment.endNode
	from := localPoint(origin, segment.startLongitude, segment.startLatitude)
	to := localPoint(origin, segment.endLongitude, segment.endLatitude)
	if !forward {
		fromNode, toNode, from, to = toNode, fromNode, to, from
	}
	locality := ""
	if segment.locality != nil {
		locality = fmt.Sprintf("%d", *segment.locality)
	}
	direction := SegmentForward
	if !forward {
		direction = SegmentReverse
	}
	return DirectedSegment{
		ID: directedID(segment.id, forward), PhysicalID: segment.id.String(), PhysicalSegmentID: segment.id,
		FromNode: fromNode.String(), ToNode: toNode.String(), From: from, To: to,
		LengthMeters: segment.length, LogicalPathID: segment.logicalPath.String(), LocalityID: locality,
		RegionID: segment.region, GenerationID: segment.generation,
		DerivationVersion: segment.derivationVersion, SourceWayID: segment.sourceWay,
		SourceWayVersion: segment.sourceWayVersion, Direction: direction, TransitionOnly: segment.transitionOnly,
		ContinuityConnector:   segment.continuityConnector,
		DrivewayConnector:     segment.drivewayConnector,
		AccessoryConnector:    segment.accessoryConnector,
		ParkingAisleConnector: segment.parkingAisleConnector,
		RoadConnector: segment.highway == "service" && !namedPrivateNeighborhoodRoad(decodeTags(segment.tags)) &&
			!pedestrianParkingAisle(segment.highway, decodeTags(segment.tags)) || strings.HasSuffix(segment.highway, "_link"),
		IndependentPath: independentPedestrianWay(segment.highway, segment.tags),
		ContinuityClass: continuityClassForWay(segment.highway, segment.tags),
	}
}

func expandOSMGraph(ctx context.Context, topology osmTopology, segments map[uuid.UUID]physicalSegment, policy pathPolicy, options OSMEvaluationOptions, maxDistance float64, stats *OSMEvaluationStats) error {
	bestDistance := make(map[uuid.UUID]float64)
	frontier := make([]uuid.UUID, 0, len(segments)*2)
	for _, segment := range segments {
		for _, node := range []uuid.UUID{segment.startNode, segment.endNode} {
			if _, exists := bestDistance[node]; !exists {
				bestDistance[node] = 0
				frontier = append(frontier, node)
			}
		}
	}
	for layer := 0; layer < options.MaxExpansionLayers && len(frontier) > 0; layer++ {
		if len(bestDistance) > options.MaxGraphNodes {
			return fmt.Errorf("OSM evaluation graph node bound exceeded")
		}
		sort.Slice(frontier, func(i, j int) bool { return frontier[i].String() < frontier[j].String() })
		next := make([]uuid.UUID, 0)
		for start := 0; start < len(frontier); start += 256 {
			end := min(len(frontier), start+256)
			remaining := options.MaxGraphEdges - len(segments)
			if remaining < 1 {
				return fmt.Errorf("OSM evaluation graph edge bound exceeded")
			}
			edges, err := topology.IncidentEdges(ctx, frontier[start:end], remaining)
			if err != nil {
				return err
			}
			for _, edge := range edges {
				segment := physicalFromEdge(edge)
				if !policy.transitionEligible(segment.highway, segment.tags) {
					continue
				}
				segment.transitionOnly = !policy.eligible(segment.highway, segment.tags)
				segment.continuityConnector = segment.transitionOnly && pedestrianContinuityConnector(segment.highway, decodeTags(segment.tags))
				segment.drivewayConnector = segment.transitionOnly && pedestrianDriveway(segment.highway, decodeTags(segment.tags))
				segment.accessoryConnector = segment.transitionOnly && pedestrianAccessoryConnector(segment.highway, decodeTags(segment.tags))
				segment.parkingAisleConnector = segment.transitionOnly && pedestrianParkingAisle(segment.highway, decodeTags(segment.tags))
				segments[segment.id] = segment
				distance, exists := bestDistance[edge.RequestedNodeID]
				if !exists {
					return fmt.Errorf("OSM incident edge returned an unrequested graph node")
				}
				other := segment.startNode
				if other == edge.RequestedNodeID {
					other = segment.endNode
				} else if segment.endNode != edge.RequestedNodeID {
					return fmt.Errorf("OSM incident edge is disconnected from its requested graph node")
				}
				candidateDistance := distance + segment.length
				prior, seen := bestDistance[other]
				if candidateDistance <= maxDistance && (!seen || candidateDistance < prior) {
					bestDistance[other] = candidateDistance
					next = append(next, other)
				}
			}
		}
		stats.ExpansionLayers = layer + 1
		frontier = next
		if len(segments) > options.MaxGraphEdges {
			return fmt.Errorf("OSM evaluation graph edge bound exceeded")
		}
	}
	if len(frontier) > 0 {
		return fmt.Errorf("OSM evaluation graph layer bound exceeded")
	}
	stats.GraphNodeCount, stats.GraphEdgeCount = len(bestDistance), len(segments)
	return nil
}

func directedID(id uuid.UUID, forward bool) string {
	if forward {
		return id.String() + "/f"
	}
	return id.String() + "/r"
}

func reverseHeading(heading *float64) *float64 {
	if heading == nil {
		return nil
	}
	value := math.Mod(*heading+180, 360)
	return &value
}

func localPoint(origin GeographicObservation, longitude, latitude float64) Point {
	lat0 := origin.Latitude * math.Pi / 180
	dLon := (longitude - origin.Longitude) * math.Pi / 180
	if dLon > math.Pi {
		dLon -= 2 * math.Pi
	} else if dLon < -math.Pi {
		dLon += 2 * math.Pi
	}
	return Point{earthRadiusMeters * math.Cos(lat0) * dLon, earthRadiusMeters * (latitude - origin.Latitude) * math.Pi / 180}
}
