package coverage

import (
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
)

type state struct {
	candidate Candidate
	cost      float64
	pathKey   string
	previous  int
	route     networkRoute
}

// Match decodes observations against caller-supplied candidates. Candidate
// discovery is intentionally outside this evaluation package.
func Match(observations []Observation, supplied [][]Candidate, graph Graph, rules Rules) Result {
	result := Result{RulesVersion: rules.Version, MinimumTraversalMeters: rules.MinTraversalLengthMeters, Observations: make([]DecodedObservation, len(observations))}
	for i := range observations {
		result.Observations[i] = DecodedObservation{ObservationIndex: i, Status: ObservationUnmatched}
	}
	if len(observations) == 0 || rules.Version == "" {
		return result
	}
	g := compileGraph(graph)
	candidates := make([][]Candidate, len(observations))
	for i := range observations {
		if i < len(supplied) {
			candidates[i] = acceptedCandidates(observations[i], supplied[i], g, rules)
		}
		if len(candidates[i]) > 0 {
			result.Observations[i].Status = ObservationRejected
		}
	}

	groupStart := -1
	for i := range observations {
		if len(candidates[i]) == 0 {
			if groupStart >= 0 {
				decodeGroup(groupStart, i, observations, candidates, g, rules, &result)
				groupStart = -1
			}
			result.Splits = append(result.Splits, Split{i, SplitNoCandidate})
			continue
		}
		if groupStart < 0 {
			groupStart = i
			continue
		}
		if reason := observationGap(observations[i-1], observations[i], rules); reason != "" {
			decodeGroup(groupStart, i, observations, candidates, g, rules, &result)
			result.Splits = append(result.Splits, Split{i, reason})
			groupStart = i
		}
	}
	if groupStart >= 0 {
		decodeGroup(groupStart, len(observations), observations, candidates, g, rules, &result)
	}
	repairRawSupportedAccessoryShortcuts(&result, observations, g, 50, 25, 0.5, 10, rules.PositiveLengthEpsilonMeters)
	bridgePlausibleAccessoryTraversals(&result, observations, g, 25, 32, rules.PositiveLengthEpsilonMeters)
	bridgePlausibleDrivewayTraversals(&result, observations, g, 40, 32, rules.PositiveLengthEpsilonMeters)
	bridgePlausibleParkingAisleTraversals(&result, observations, g, 200, 64, rules.PositiveLengthEpsilonMeters)
	extendRawSupportedParkingAisles(&result, observations, g, 200, rules.PositiveLengthEpsilonMeters)
	bridgeDisconnectedRoadPathParkingAisles(&result, observations, g, 200, rules.PositiveLengthEpsilonMeters)
	repairRawSupportedRoadShortcuts(&result, observations, g, 80, 60, 0.85, 10, 20, rules.PositiveLengthEpsilonMeters)
	bridgeRawSupportedRoadContinuity(&result, observations, g, 80, 60, 32, 0.85, 10, 20, rules.PositiveLengthEpsilonMeters)
	removeUnsupportedRoadOutAndBacks(&result, observations, g, 100, 15, rules.PositiveLengthEpsilonMeters)
	cancelTraversalBoundaryBacktracks(&result, rules.PositiveLengthEpsilonMeters)
	replaceShortExcursionsBetweenSameClass(&result, observations, g, 75, 500, 64, rules.PositiveLengthEpsilonMeters)
	replaceShortPortionExcursions(&result, g, 30, 250, rules.PositiveLengthEpsilonMeters)
	replaceBoundaryShortExcursions(&result, g, 30, 250, rules.PositiveLengthEpsilonMeters)
	replaceShortExcursionsBetweenSamePath(&result, observations, g, 30, 250, 64, rules.PositiveLengthEpsilonMeters)
	bridgeSamePathPortionGaps(&result, g, 250, rules.PositiveLengthEpsilonMeters)
	bridgePlausibleSamePathTraversals(&result, observations, g, 250, 64, rules.PositiveLengthEpsilonMeters)
	bridgeShortSamePathTraversals(&result, g, 30, 16, rules.PositiveLengthEpsilonMeters)
	completeRawSupportedPathEndpoints(&result, observations, g, 20, 5, rules.PositiveLengthEpsilonMeters)
	replaceBoundaryOverlappingRoadReversalWithPath(&result, observations, g, 150, 250, 60, 15, 64, rules.PositiveLengthEpsilonMeters)
	replaceOverlappingRoadReversalWithPath(&result, observations, g, 150, 100, 60, 15, rules.PositiveLengthEpsilonMeters)
	repairDisconnectedRoadPortionGaps(&result, observations, g, 40, rules.PositiveLengthEpsilonMeters)
	repairDisconnectedRoadExcursions(&result, observations, g, 40, 5, rules.PositiveLengthEpsilonMeters)
	repairRawSupportedParallelParkingRoutes(&result, observations, g, 120, 20, 30, rules.PositiveLengthEpsilonMeters)
	repairRawSupportedParallelParkingRoutes(&result, observations, g, 120, 20, 30, rules.PositiveLengthEpsilonMeters)
	replaceTraversalTailWithRawSupportedSameRoad(&result, observations, g, 150, 10, 15, rules.PositiveLengthEpsilonMeters)
	extendRawSupportedRoadDeadEnds(&result, observations, g, 150, 60, 15, rules.PositiveLengthEpsilonMeters)
	extendRawSupportedRoadOutAndBackChains(&result, observations, g, 150, 3, 15, rules.PositiveLengthEpsilonMeters)
	removeUnsupportedSingleRoadTurnStubs(&result, observations, g, 30, 45, 15, rules.PositiveLengthEpsilonMeters)
	extendRawSupportedDriveways(&result, observations, g, 40, 60, 15, rules.PositiveLengthEpsilonMeters)
	extendRawSupportedRoadOutAndBackChains(&result, observations, g, 150, 3, 15, rules.PositiveLengthEpsilonMeters)
	repairBoundaryIncompleteRoadTurns(&result, observations, g, 40, 30, 128, rules.PositiveLengthEpsilonMeters)
	completeRawSupportedConnectedRoadTurns(&result, observations, g, 35, 25, rules.PositiveLengthEpsilonMeters)
	completeRawSupportedBoundaryConnectedRoadTurns(&result, observations, g, 35, 25, 128, rules.PositiveLengthEpsilonMeters)
	repairRawSupportedSameRoadExcursions(&result, observations, g, 100, 20, 15, rules.PositiveLengthEpsilonMeters)
	repairBoundarySameRoadDetours(&result, observations, g, 100, 20, 6, 15, 128, rules.PositiveLengthEpsilonMeters)
	replaceShortUnsupportedRoadTangentsWithRawRoute(&result, observations, g, 60, 400, 3, 15, rules.PositiveLengthEpsilonMeters)
	removeUnsupportedSingleRoadTurnStubs(&result, observations, g, 30, 45, 15, rules.PositiveLengthEpsilonMeters)
	removeUnsupportedSingleDrivewayOutAndBacks(&result, observations, g, 12, 30, 45, 15, rules.PositiveLengthEpsilonMeters)
	removeUnsupportedIsolatedSingleRoadPortions(&result, observations, g, 20, 45, 15, 128, rules.PositiveLengthEpsilonMeters)
	removeParkingAislesBeforeSameRoadContinuations(&result, observations, g, 40, 15, rules.PositiveLengthEpsilonMeters)
	return result
}

func replaceShortUnsupportedRoadTangentsWithRawRoute(result *Result, observations []Observation, graph compiledGraph, maximumSelectedMeters, maximumRouteMeters float64, maximumRoutePortions int, maximumRawOffsetMeters, epsilon float64) {
	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+3; end++ {
				after, selected := traversal.Portions[end], traversal.Portions[start+1:end]
				selectedLength := portionsLength(selected)
				if before.ContinuityClass != "road" || after.ContinuityClass != "road" || selectedLength > maximumSelectedMeters ||
					!allRoadPortions(selected) || !roadSequenceDisconnected(before, selected, after, graph, epsilon) {
					continue
				}
				alternative, ok := roadRoute(before, after, roadGraph, maximumRouteMeters, epsilon)
				preserveTurnaround := len(selected) == 1 && selected[0].FromMeter <= epsilon && graph.lengths[selected[0].SegmentID]-selected[0].ToMeter <= epsilon &&
					roadPortionReachesGraphDeadEnd(selected[0], graph) && turnaroundRawSupported(selected[0], graph, raw, maximumRawOffsetMeters)
				if !ok || len(alternative.portions) < 2 || len(alternative.portions) > maximumRoutePortions ||
					(rawSupportedPortionLength(selected, graph, raw, maximumRawOffsetMeters) > selectedLength*0.2 && !preserveTurnaround) ||
					!roadTurnPortionsRawSupported(alternative.portions, graph, raw, 60, maximumRawOffsetMeters) {
					continue
				}
				beforeSegment, afterSegment := graph.segments[before.SegmentID], graph.segments[after.SegmentID]
				from := pointAlongSegment(beforeSegment, before.ToMeter, graph.lengths[before.SegmentID])
				to := pointAlongSegment(afterSegment, after.FromMeter, graph.lengths[after.SegmentID])
				_, rawDistance, ok := rawBetweenEndpoints(raw, from, to, maximumRawOffsetMeters)
				if !ok || alternative.distance > rawDistance*1.25+20 {
					continue
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				if preserveTurnaround {
					if reverseID, reverseOK := reverseDirectedSegmentID(graph.segments[selected[0].SegmentID], graph); reverseOK {
						replacement = append(replacement, selected[0], graph.portion(reverseID, 0, graph.lengths[reverseID]))
					}
				}
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func roadPortionReachesGraphDeadEnd(portion TraversedPortion, graph compiledGraph) bool {
	segment, exists := graph.segments[portion.SegmentID]
	if !exists {
		return false
	}
	for _, id := range graph.outgoing[segment.ToNode] {
		continuation := graph.segments[id]
		if continuation.ContinuityClass == "road" && !samePhysicalSegment(segment, continuation) {
			return false
		}
	}
	return true
}

func completeRawSupportedConnectedRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumMissingMeters, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			before, after, ok := completedConnectedRoadTurn(traversal.Portions[i], traversal.Portions[i+1], raw, graph, maximumMissingMeters, maximumRawOffsetMeters, epsilon)
			if ok {
				traversal.Portions[i], traversal.Portions[i+1] = before, after
			}
		}
	}
}

func completeRawSupportedBoundaryConnectedRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumMissingMeters, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); i++ {
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			j++
		}
		if j == len(result.Traversals) || len(result.Traversals[i].Portions) == 0 ||
			result.Traversals[j].FirstObservation-result.Traversals[i].LastObservation > maximumObservations ||
			hasHardSplit(result.Splits, result.Traversals[i].LastObservation, result.Traversals[j].FirstObservation) {
			continue
		}
		first, second := &result.Traversals[i], &result.Traversals[j]
		raw := observations[first.FirstObservation : second.LastObservation+1]
		before, after, ok := completedConnectedRoadTurn(first.Portions[len(first.Portions)-1], second.Portions[0], raw, graph, maximumMissingMeters, maximumRawOffsetMeters, epsilon)
		if ok {
			first.Portions[len(first.Portions)-1], second.Portions[0] = before, after
		}
	}
}

func completedConnectedRoadTurn(before, after TraversedPortion, raw []Observation, graph compiledGraph, maximumMissingMeters, maximumRawOffsetMeters, epsilon float64) (TraversedPortion, TraversedPortion, bool) {
	beforeSegment, beforeExists := graph.segments[before.SegmentID]
	afterSegment, afterExists := graph.segments[after.SegmentID]
	beforeMissing := graph.lengths[before.SegmentID] - before.ToMeter
	afterMissing := after.FromMeter
	if !beforeExists || !afterExists || beforeSegment.TransitionOnly || afterSegment.TransitionOnly ||
		before.ContinuityClass != "road" || after.ContinuityClass != "road" || before.LogicalPathID == "" || after.LogicalPathID == "" ||
		before.LogicalPathID == after.LogicalPathID || beforeSegment.ToNode != afterSegment.FromNode || beforeMissing <= epsilon || afterMissing <= epsilon ||
		before.ToMeter-before.FromMeter < 20 || after.ToMeter-after.FromMeter < 20 || beforeMissing+afterMissing > maximumMissingMeters ||
		!endpointRawSupported(raw, beforeSegment.To, maximumRawOffsetMeters, 1) {
		return before, after, false
	}
	return graph.portion(before.SegmentID, before.FromMeter, graph.lengths[before.SegmentID]), graph.portion(after.SegmentID, 0, after.ToMeter), true
}

func removeUnsupportedIsolatedSingleRoadPortions(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	for i := 1; i+1 < len(result.Traversals); i++ {
		traversal := &result.Traversals[i]
		if len(traversal.Portions) != 1 || len(result.Traversals[i-1].Portions) == 0 || len(result.Traversals[i+1].Portions) == 0 ||
			result.Traversals[i+1].FirstObservation-result.Traversals[i-1].LastObservation > maximumObservations ||
			hasHardSplit(result.Splits, result.Traversals[i-1].LastObservation, result.Traversals[i+1].FirstObservation) {
			continue
		}
		before := result.Traversals[i-1].Portions[len(result.Traversals[i-1].Portions)-1]
		middle := traversal.Portions[0]
		after := result.Traversals[i+1].Portions[0]
		segment, exists := graph.segments[middle.SegmentID]
		if !exists || segment.TransitionOnly || middle.ContinuityClass != "road" || (before.ContinuityClass != "road" && after.ContinuityClass != "road") ||
			middle.FromMeter > epsilon || graph.lengths[middle.SegmentID]-middle.ToMeter > epsilon || middle.ToMeter-middle.FromMeter > maximumMeters ||
			portionsConnected(before, middle, graph, epsilon) || portionsConnected(middle, after, graph, epsilon) {
			continue
		}
		raw := observations[result.Traversals[i-1].FirstObservation : result.Traversals[i+1].LastObservation+1]
		if rawPolylineSupportsDirectedSegmentWithin(raw, segment, maximumDegrees, maximumRawOffsetMeters) {
			continue
		}
		result.Traversals = append(result.Traversals[:i], result.Traversals[i+1:]...)
		i--
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+1 < len(traversal.Portions); i++ {
			before, middle, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1]
			segment, exists := graph.segments[middle.SegmentID]
			if !exists || segment.TransitionOnly || middle.ContinuityClass != "road" || (before.ContinuityClass != "road" && after.ContinuityClass != "road") ||
				middle.FromMeter > epsilon || graph.lengths[middle.SegmentID]-middle.ToMeter > epsilon || middle.ToMeter-middle.FromMeter > maximumMeters ||
				portionsConnected(before, middle, graph, epsilon) || portionsConnected(middle, after, graph, epsilon) ||
				rawPolylineSupportsDirectedSegmentWithin(raw, segment, maximumDegrees, maximumRawOffsetMeters) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+1:]...)
			i--
		}
		for len(traversal.Portions) >= 2 {
			last := len(traversal.Portions) - 1
			if !unsupportedDisconnectedWindowEdgeRoad(traversal.Portions[last], traversal.Portions[last-1], raw, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon) {
				break
			}
			traversal.Portions = traversal.Portions[:last]
		}
		for len(traversal.Portions) >= 2 && unsupportedDisconnectedWindowEdgeRoad(traversal.Portions[0], traversal.Portions[1], raw, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon) {
			traversal.Portions = traversal.Portions[1:]
		}
	}
}

func unsupportedDisconnectedWindowEdgeRoad(candidate, neighbor TraversedPortion, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) bool {
	segment, exists := graph.segments[candidate.SegmentID]
	return exists && !segment.TransitionOnly && candidate.ContinuityClass == "road" && neighbor.ContinuityClass != "" &&
		candidate.FromMeter <= epsilon && graph.lengths[candidate.SegmentID]-candidate.ToMeter <= epsilon && candidate.ToMeter-candidate.FromMeter <= maximumMeters &&
		!portionsConnected(candidate, neighbor, graph, epsilon) && !portionsConnected(neighbor, candidate, graph, epsilon) &&
		!rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters)
}

func removeUnsupportedSingleDrivewayOutAndBacks(result *Result, observations []Observation, graph compiledGraph, minimumOneWayMeters, maximumOneWayMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+2 < len(traversal.Portions); i++ {
			before, outbound, inbound, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1], traversal.Portions[i+2]
			outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
			inboundSegment, inboundExists := graph.segments[inbound.SegmentID]
			connected, known := roadPortionBoundaryConnected(before, after, graph, epsilon)
			if !outboundExists || !inboundExists || !known || !connected || !inversePortions(outbound, inbound, epsilon) ||
				!outboundSegment.DrivewayConnector || !inboundSegment.DrivewayConnector || outbound.ToMeter-outbound.FromMeter < minimumOneWayMeters || outbound.ToMeter-outbound.FromMeter > maximumOneWayMeters ||
				before.ContinuityClass != "road" || after.ContinuityClass != "road" || before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID {
				continue
			}
			outboundSupported := rawPolylineSupportsDirectedSegmentWithin(raw, outboundSegment, maximumDegrees, maximumRawOffsetMeters)
			inboundSupported := rawPolylineSupportsDirectedSegmentWithin(raw, inboundSegment, maximumDegrees, maximumRawOffsetMeters)
			if outboundSupported || inboundSupported {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+2:]...)
			i--
		}
	}
}

func repairBoundarySameRoadDetours(result *Result, observations []Observation, graph compiledGraph, maximumDetourMeters, minimumSavings float64, maximumMiddlePortions int, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			j++
		}
		if j == len(result.Traversals) {
			break
		}
		first, second := &result.Traversals[i], result.Traversals[j]
		if len(first.Portions) == 0 || len(second.Portions) < 4 || second.FirstObservation-first.LastObservation > maximumObservations ||
			hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before := first.Portions[len(first.Portions)-1]
		beforeSegment, beforeExists := graph.segments[before.SegmentID]
		if !beforeExists || beforeSegment.TransitionOnly || before.ContinuityClass != "road" || before.LogicalPathID == "" ||
			graph.lengths[before.SegmentID]-before.ToMeter > epsilon {
			i++
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		repaired := false
		for end := 3; end < len(second.Portions) && end <= maximumMiddlePortions; end++ {
			after := second.Portions[end]
			afterSegment, afterExists := graph.segments[after.SegmentID]
			selected := second.Portions[:end]
			selectedLength := portionsLength(selected)
			if !afterExists || afterSegment.TransitionOnly || after.ContinuityClass != "road" || after.FromMeter > epsilon ||
				after.LogicalPathID != before.LogicalPathID || afterSegment.SourceWayID != beforeSegment.SourceWayID ||
				selectedLength > maximumDetourMeters || slicesContainLogicalPath(selected, before.LogicalPathID) || !allRoadPortions(selected) ||
				rawSupportedPortionLength(selected, graph, raw, maximumRawOffsetMeters) > selectedLength*0.2 {
				continue
			}
			connectorIDs := make([]string, 0, 1)
			for _, id := range graph.outgoing[beforeSegment.ToNode] {
				segment := graph.segments[id]
				if !segment.TransitionOnly && segment.ContinuityClass == "road" && segment.ToNode == afterSegment.FromNode &&
					segment.LogicalPathID == before.LogicalPathID && segment.SourceWayID == beforeSegment.SourceWayID {
					connectorIDs = append(connectorIDs, id)
				}
			}
			if len(connectorIDs) != 1 {
				continue
			}
			connector := graph.segments[connectorIDs[0]]
			if connector.LengthMeters+minimumSavings > selectedLength ||
				(!rawSupportsSegmentByEndpointDistance(raw, connector, maximumRawOffsetMeters, 0.7, 1.5) &&
					!rawPolylineSupportsDirectedSegmentWithin(raw, connector, 60, maximumRawOffsetMeters)) {
				continue
			}
			first.Portions = append(first.Portions, graph.portion(connector.ID, 0, connector.LengthMeters))
			first.Portions = append(first.Portions, second.Portions[end:]...)
			first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
			first.Observations = append(first.Observations, second.Observations...)
			first.LastObservation = second.LastObservation
			first.Cost += second.Cost
			result.Traversals = append(result.Traversals[:i+1], result.Traversals[j+1:]...)
			repaired = true
			break
		}
		if !repaired {
			i++
		}
	}
}

func removeParkingAislesBeforeSameRoadContinuations(result *Result, observations []Observation, graph compiledGraph, minimumParkingMeters, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+2 < len(traversal.Portions); i++ {
			before, parking, after := traversal.Portions[i], traversal.Portions[i+1], traversal.Portions[i+2]
			beforeSegment, beforeExists := graph.segments[before.SegmentID]
			parkingSegment, parkingExists := graph.segments[parking.SegmentID]
			afterSegment, afterExists := graph.segments[after.SegmentID]
			if !beforeExists || !parkingExists || !afterExists || before.ContinuityClass != "road" || after.ContinuityClass != "road" ||
				!parkingSegment.ParkingAisleConnector || parking.ToMeter-parking.FromMeter < minimumParkingMeters ||
				before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID ||
				beforeSegment.SourceWayID == 0 || beforeSegment.SourceWayID != afterSegment.SourceWayID ||
				graph.lengths[before.SegmentID]-before.ToMeter > epsilon || after.FromMeter > epsilon || parking.FromMeter > epsilon ||
				beforeSegment.ToNode != afterSegment.FromNode || parkingSegment.FromNode != afterSegment.FromNode || parkingSegment.ToNode == afterSegment.FromNode ||
				(!rawSupportsSegmentByEndpointDistance(raw, afterSegment, maximumRawOffsetMeters, 0.7, 1.5) &&
					!rawPolylineSupportsDirectedSegmentWithin(raw, afterSegment, 60, maximumRawOffsetMeters)) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i+1], traversal.Portions[i+2:]...)
			i = max(-1, i-1)
		}
	}
}

func bridgePlausibleParkingAisleTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations ||
			hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if !parkingAisleBoundaryClasses(before.ContinuityClass, after.ContinuityClass) {
			i++
			continue
		}
		connector, ok := parkingAisleBridgeRoute(before, after, graph, observations, second.FirstObservation, maximumRouteMeters, epsilon)
		rawDistance := observationPolylineDistance(observations, first.LastObservation, second.FirstObservation)
		if !ok || rawDistance <= epsilon || connector.distance > rawDistance*1.5+20 {
			i++
			continue
		}
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func parkingAisleBridgeRoute(before, after TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumRouteMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			route, ok := graph.routeWithBoundaryClasses(from, to, maximumRouteMeters, epsilon, before.ContinuityClass, after.ContinuityClass)
			if !ok || route.distance <= epsilon || route.distance >= best.distance || !containsParkingAisleConnector(route.portions, graph) ||
				!onlyBoundaryAndParkingAislePortions(route.portions, before, after, graph) {
				continue
			}
			clipped, supported := parkingAisleEvidence(route.portions, graph, observations, transitionIndex)
			if !supported {
				continue
			}
			route.portions = parkingAislePortions(clipped, graph)
			best = route
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func bridgeDisconnectedRoadPathParkingAisles(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			before, after := traversal.Portions[i], traversal.Portions[i+1]
			if !parkingAisleBoundaryClasses(before.ContinuityClass, after.ContinuityClass) || portionsConnected(before, after, graph, epsilon) {
				continue
			}
			connector, ok := parkingAisleBridgeRoute(before, after, graph, raw, len(raw)/2, maximumRouteMeters, epsilon)
			if !ok {
				continue
			}
			insert := i + 1
			traversal.Portions = append(traversal.Portions, make([]TraversedPortion, len(connector.portions))...)
			copy(traversal.Portions[insert+len(connector.portions):], traversal.Portions[insert:len(traversal.Portions)-len(connector.portions)])
			copy(traversal.Portions[insert:], connector.portions)
			i += len(connector.portions)
		}
	}
}

func parkingAislePortions(portions []TraversedPortion, graph compiledGraph) []TraversedPortion {
	result := make([]TraversedPortion, 0, len(portions))
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].ParkingAisleConnector {
			result = append(result, portion)
		}
	}
	return result
}

func extendRawSupportedParkingAisles(result *Result, observations []Observation, graph compiledGraph, maximumMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[max(0, traversal.FirstObservation-64):min(len(observations), traversal.LastObservation+65)]
		for portionIndex := 0; portionIndex < len(traversal.Portions); portionIndex++ {
			portion := traversal.Portions[portionIndex]
			segment := graph.segments[portion.SegmentID]
			if segment.TransitionOnly || portion.ContinuityClass != "road" || graph.lengths[portion.SegmentID]-portion.ToMeter > epsilon {
				continue
			}
			addition, ok := bestRawSupportedOutgoingParkingAisle(segment.ToNode, raw, graph, maximumMeters, present)
			if !ok {
				continue
			}
			insert := portionIndex + 1
			traversal.Portions = append(traversal.Portions, TraversedPortion{})
			copy(traversal.Portions[insert+1:], traversal.Portions[insert:])
			traversal.Portions[insert] = addition
			present[graph.segments[addition.SegmentID].PhysicalID] = true
			portionIndex++
		}
	}
}

func bestRawSupportedOutgoingParkingAisle(node string, observations []Observation, graph compiledGraph, maximumMeters float64, present map[string]bool) (TraversedPortion, bool) {
	ids := append([]string(nil), graph.outgoing[node]...)
	sort.Strings(ids)
	best, bestLength := TraversedPortion{}, 0.0
	for _, id := range ids {
		segment := graph.segments[id]
		if !segment.ParkingAisleConnector || segment.LengthMeters > maximumMeters || present[segment.PhysicalID] || parkingAisleShadowedByRoad(segment, graph) {
			continue
		}
		fromMeter, toMeter, ok := parkingAisleRawInterval(observations, segment, parkingAisleRawSupportOffsetMeters)
		if !ok || fromMeter > math.Max(5, segment.LengthMeters*0.2) || toMeter <= bestLength {
			continue
		}
		best, bestLength = graph.portion(id, 0, toMeter), toMeter
	}
	return best, bestLength > 0
}

func containsParkingAisleConnector(portions []TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].ParkingAisleConnector {
			return true
		}
	}
	return false
}

func onlyBoundaryAndParkingAislePortions(portions []TraversedPortion, before, after TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		segment := graph.segments[portion.SegmentID]
		if !segment.ParkingAisleConnector && segment.PhysicalSegmentID != before.PhysicalSegmentID && segment.PhysicalSegmentID != after.PhysicalSegmentID {
			return false
		}
	}
	return true
}

func removeUnsupportedRoadOutAndBacks(result *Result, observations []Observation, graph compiledGraph, maximumOneWayMeters, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for center := 0; center+1 < len(traversal.Portions); center++ {
			if !inversePortions(traversal.Portions[center], traversal.Portions[center+1], epsilon) {
				continue
			}
			left, right := center, center+1
			for left > 0 && right+1 < len(traversal.Portions) && inversePortions(traversal.Portions[left-1], traversal.Portions[right+1], epsilon) {
				left--
				right++
			}
			outbound := traversal.Portions[left : center+1]
			if len(outbound) < 2 || portionsLength(outbound) > maximumOneWayMeters || !allRoadPortions(outbound) ||
				turnaroundRawSupported(traversal.Portions[center], graph, raw, maximumRawOffsetMeters) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:left], traversal.Portions[right+1:]...)
			center = max(-1, left-1)
		}
	}
}

func removeUnsupportedSingleRoadTurnStubs(result *Result, observations []Observation, graph compiledGraph, maximumOneWayMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+2 < len(traversal.Portions); i++ {
			before, outbound, inbound, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1], traversal.Portions[i+2]
			differentRoadTurn := before.LogicalPathID != after.LogicalPathID && (outbound.LogicalPathID == before.LogicalPathID || outbound.LogicalPathID == after.LogicalPathID)
			serviceDeadEnd := roadPortionEndsAtDeadEnd(outbound, graph)
			ordinaryDeadEnd := !graph.segments[outbound.SegmentID].RoadConnector && outbound.FromMeter <= epsilon && graph.lengths[outbound.SegmentID]-outbound.ToMeter <= epsilon &&
				inbound.FromMeter <= epsilon && graph.lengths[inbound.SegmentID]-inbound.ToMeter <= epsilon && roadPortionReachesGraphDeadEnd(outbound, graph)
			deadEnd := serviceDeadEnd || ordinaryDeadEnd
			sameRoadThrough := before.LogicalPathID == after.LogicalPathID && before.PhysicalSegmentID != after.PhysicalSegmentID &&
				outbound.LogicalPathID != before.LogicalPathID && (roadPortionHasNonPhysicalContinuation(outbound, graph) || serviceDeadEnd || ordinaryDeadEnd)
			if !inversePortions(outbound, inbound, epsilon) || portionsLength([]TraversedPortion{outbound}) > maximumOneWayMeters ||
				before.ContinuityClass != "road" || outbound.ContinuityClass != "road" || after.ContinuityClass != "road" ||
				before.LogicalPathID == "" || after.LogicalPathID == "" || (!differentRoadTurn && !sameRoadThrough) {
				continue
			}
			connected, known := roadPortionBoundaryConnected(before, after, graph, epsilon)
			outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
			inboundSegment, inboundExists := graph.segments[inbound.SegmentID]
			if !known || !connected || !outboundExists || !inboundExists || outboundSegment.TransitionOnly || inboundSegment.TransitionOnly {
				continue
			}
			outboundSupported := rawPolylineSupportsDirectedSegmentWithin(raw, outboundSegment, maximumDegrees, maximumRawOffsetMeters) ||
				(sameRoadThrough && ordinaryDeadEnd && turnaroundRawSupported(outbound, graph, raw, maximumRawOffsetMeters)) ||
				!deadEnd && rawExtendsPastDirectedEndpoint(raw, outboundSegment, maximumRawOffsetMeters, 5)
			inboundSupported := rawPolylineSupportsDirectedSegmentWithin(raw, inboundSegment, maximumDegrees, maximumRawOffsetMeters) ||
				!deadEnd && rawExtendsPastDirectedEndpoint(raw, inboundSegment, maximumRawOffsetMeters, 5)
			if outboundSupported && inboundSupported {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+2:]...)
			i--
		}
	}
}

func roadPortionEndsAtDeadEnd(portion TraversedPortion, graph compiledGraph) bool {
	segment, exists := graph.segments[portion.SegmentID]
	if !exists || !segment.RoadConnector {
		return false
	}
	for _, id := range graph.outgoing[segment.ToNode] {
		continuation := graph.segments[id]
		if !continuation.TransitionOnly && continuation.ContinuityClass == "road" && !samePhysicalSegment(segment, continuation) {
			return false
		}
	}
	return true
}

func roadPortionHasNonPhysicalContinuation(portion TraversedPortion, graph compiledGraph) bool {
	segment, exists := graph.segments[portion.SegmentID]
	if !exists {
		return false
	}
	for _, id := range graph.outgoing[segment.ToNode] {
		continuation := graph.segments[id]
		if !continuation.TransitionOnly && continuation.ContinuityClass == "road" && !samePhysicalSegment(segment, continuation) {
			return true
		}
	}
	return false
}

func removeDisconnectedSingleRoadStubs(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+1 < len(traversal.Portions); i++ {
			before, middle, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1]
			segment, exists := graph.segments[middle.SegmentID]
			leftConnected := portionsConnected(before, middle, graph, epsilon)
			rightConnected := portionsConnected(middle, after, graph, epsilon)
			if !exists || before.ContinuityClass != "road" || middle.ContinuityClass != "road" || after.ContinuityClass != "road" ||
				middle.ToMeter-middle.FromMeter > maximumMeters || !portionsConnected(before, after, graph, epsilon) || leftConnected == rightConnected ||
				rawPolylineSupportsDirectedSegmentWithin(raw, segment, 60, maximumRawOffsetMeters) || rawExtendsPastDirectedEndpoint(raw, segment, maximumRawOffsetMeters, 5) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+1:]...)
			i--
		}
	}
}

func repairDisconnectedRoadPortionGaps(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, epsilon float64) {
	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			before, after := traversal.Portions[i], traversal.Portions[i+1]
			if before.ContinuityClass != "road" || after.ContinuityClass != "road" || portionsConnected(before, after, graph, epsilon) {
				continue
			}
			connector, ok := roadRoute(before, after, roadGraph, maximumRouteMeters, epsilon)
			if !ok || connector.distance > maximumRouteMeters || !allRoadPortions(connector.portions) ||
				connector.distance > 20 && !roadTurnPortionsRawSupported(connector.portions, graph, raw, 60, 15) {
				continue
			}
			insert := i + 1
			traversal.Portions = append(traversal.Portions, make([]TraversedPortion, len(connector.portions))...)
			copy(traversal.Portions[insert+len(connector.portions):], traversal.Portions[insert:len(traversal.Portions)-len(connector.portions)])
			copy(traversal.Portions[insert:], connector.portions)
			i += len(connector.portions)
		}
	}
}

func repairDisconnectedRoadExcursions(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, minimumSavings, epsilon float64) {
	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
				after, selected := traversal.Portions[end], traversal.Portions[start+1:end]
				selectedLength := portionsLength(selected)
				if selectedLength > maximumExcursionMeters || before.ContinuityClass != "road" || after.ContinuityClass != "road" || !allRoadPortions(selected) ||
					!roadSequenceDisconnected(before, selected, after, graph, epsilon) {
					continue
				}
				alternative, ok := roadRoute(before, after, roadGraph, maximumExcursionMeters, epsilon)
				if !ok || alternative.distance+minimumSavings > selectedLength || !allRoadPortions(alternative.portions) || !roadTurnPortionsRawSupported(alternative.portions, graph, raw, 60, 15) {
					continue
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func roadTurnPortionsRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumDegrees, maximumRawOffsetMeters float64) bool {
	for _, portion := range portions {
		segment, exists := graph.segments[portion.SegmentID]
		if !exists || (!rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) &&
			!directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters)) {
			return false
		}
	}
	return len(portions) > 0
}

func roadSequenceDisconnected(before TraversedPortion, middle []TraversedPortion, after TraversedPortion, graph compiledGraph, epsilon float64) bool {
	portions := make([]TraversedPortion, 0, len(middle)+2)
	portions = append(portions, before)
	portions = append(portions, middle...)
	portions = append(portions, after)
	for i := 0; i+1 < len(portions); i++ {
		if !portionsConnected(portions[i], portions[i+1], graph, epsilon) {
			return true
		}
	}
	return false
}

func portionsConnected(before, after TraversedPortion, graph compiledGraph, epsilon float64) bool {
	beforeSegment, beforeExists := graph.segments[before.SegmentID]
	afterSegment, afterExists := graph.segments[after.SegmentID]
	if !beforeExists || !afterExists {
		return true
	}
	beforePoint := pointAlongSegment(beforeSegment, before.ToMeter, graph.lengths[before.SegmentID])
	afterPoint := pointAlongSegment(afterSegment, after.FromMeter, graph.lengths[after.SegmentID])
	return distance(beforePoint, afterPoint) <= epsilon
}

func extendRawSupportedRoadDeadEnds(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i < len(traversal.Portions); i++ {
			current := traversal.Portions[i]
			segment := graph.segments[current.SegmentID]
			if segment.TransitionOnly || current.ContinuityClass != "road" || graph.lengths[current.SegmentID]-current.ToMeter > epsilon {
				continue
			}
			addition, ok := bestRawSupportedRoadDeadEnd(segment.ToNode, current, nextPortion(traversal.Portions, i), raw, graph, present, maximumMeters, maximumDegrees, maximumRawOffsetMeters)
			if !ok {
				continue
			}
			insert := i + 1
			traversal.Portions = append(traversal.Portions, TraversedPortion{}, TraversedPortion{})
			copy(traversal.Portions[insert+2:], traversal.Portions[insert:len(traversal.Portions)-2])
			copy(traversal.Portions[insert:], addition)
			present[graph.segments[addition[0].SegmentID].PhysicalID] = true
			i += 2
		}
	}
}

func extendRawSupportedRoadOutAndBackChains(result *Result, observations []Observation, graph compiledGraph, maximumMeters float64, maximumSegments int, maximumRawOffsetMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		for i := 0; i < len(traversal.Portions); i++ {
			current := traversal.Portions[i]
			currentSegment, exists := graph.segments[current.SegmentID]
			if !exists || current.ContinuityClass != "road" && !currentSegment.DrivewayConnector || graph.lengths[current.SegmentID]-current.ToMeter > epsilon {
				continue
			}
			next := nextPortion(traversal.Portions, i)
			nextSegment, nextExists := graph.segments[next.SegmentID]
			extendDriveway := nextExists && samePhysicalSegment(currentSegment, nextSegment) && current.Direction != next.Direction
			branchAtJunction := nextExists && next.ContinuityClass == "road" && next.FromMeter <= epsilon && currentSegment.ToNode == nextSegment.FromNode &&
				current.LogicalPathID != "" && current.LogicalPathID == next.LogicalPathID
			if !extendDriveway && !branchAtJunction {
				continue
			}
			raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
			if extendDriveway {
				raw = observations[max(0, traversal.FirstObservation-64):min(len(observations), traversal.LastObservation+65)]
			}
			addition, ok := bestRawSupportedRoadOutAndBackChain(currentSegment, nextSegment, raw, graph, present, extendDriveway, maximumMeters, maximumSegments, maximumRawOffsetMeters)
			if !ok {
				continue
			}
			insert := i + 1
			traversal.Portions = append(traversal.Portions, make([]TraversedPortion, len(addition))...)
			copy(traversal.Portions[insert+len(addition):], traversal.Portions[insert:len(traversal.Portions)-len(addition)])
			copy(traversal.Portions[insert:], addition)
			for _, portion := range addition {
				present[graph.segments[portion.SegmentID].PhysicalID] = true
			}
			i += len(addition)
		}
	}
}

func extendRawSupportedBoundaryRoadOutAndBackChains(result *Result, observations []Observation, graph compiledGraph, maximumMeters float64, maximumSegments int, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for i := 0; i+1 < len(result.Traversals); i++ {
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			j++
		}
		if j == len(result.Traversals) {
			break
		}
		first, second := &result.Traversals[i], &result.Traversals[j]
		if len(first.Portions) == 0 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations ||
			hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		current := first.Portions[len(first.Portions)-1]
		next := second.Portions[0]
		currentSegment, currentExists := graph.segments[current.SegmentID]
		nextSegment, nextExists := graph.segments[next.SegmentID]
		if !currentExists || !nextExists || !currentSegment.DrivewayConnector || !nextSegment.DrivewayConnector ||
			!samePhysicalSegment(currentSegment, nextSegment) || current.Direction == next.Direction || graph.lengths[current.SegmentID]-current.ToMeter > epsilon {
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		addition, ok := bestRawSupportedRoadOutAndBackChain(currentSegment, nextSegment, raw, graph, present, true, maximumMeters, maximumSegments, maximumRawOffsetMeters)
		if !ok {
			continue
		}
		first.Portions = append(first.Portions, addition...)
		for _, portion := range addition {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
}

func bestRawSupportedRoadOutAndBackChain(current, next DirectedSegment, observations []Observation, graph compiledGraph, present map[string]bool, extendDriveway bool, maximumMeters float64, maximumSegments int, maximumRawOffsetMeters float64) ([]TraversedPortion, bool) {
	type candidate struct {
		outbound []string
		inbound  []string
		distance float64
	}
	candidates := make([]candidate, 0)
	var visit func(string, []string, []string, map[string]bool, float64)
	visit = func(node string, outbound, inbound []string, used map[string]bool, traveled float64) {
		if len(outbound) >= maximumSegments {
			return
		}
		ids := append([]string(nil), graph.outgoing[node]...)
		sort.Strings(ids)
		for _, id := range ids {
			segment := graph.segments[id]
			eligible := !segment.TransitionOnly && segment.ContinuityClass == "road" && segment.LogicalPathID != "" && segment.SourceWayID != 0 && segment.LengthMeters >= 20
			if extendDriveway {
				eligible = segment.DrivewayConnector && segment.SourceWayID == current.SourceWayID
			} else if len(outbound) > 0 {
				first := graph.segments[outbound[0]]
				eligible = eligible && segment.LogicalPathID == first.LogicalPathID && segment.SourceWayID == first.SourceWayID
			}
			if !eligible || used[segment.PhysicalID] || present[segment.PhysicalID] || samePhysicalSegment(segment, current) ||
				next.ID != "" && samePhysicalSegment(segment, next) || traveled+segment.LengthMeters > maximumMeters {
				continue
			}
			returnID, ok := reverseDirectedSegmentID(segment, graph)
			if !ok {
				continue
			}
			newOutbound := append(append([]string(nil), outbound...), id)
			newInbound := append([]string{returnID}, inbound...)
			if (extendDriveway || len(newOutbound) >= 2 && traveled+segment.LengthMeters >= 60) &&
				rawSupportsOutAndBackSegmentChain(observations, newOutbound, newInbound, graph, maximumRawOffsetMeters) {
				candidates = append(candidates, candidate{newOutbound, newInbound, traveled + segment.LengthMeters})
			}
			newUsed := make(map[string]bool, len(used)+1)
			for physicalID := range used {
				newUsed[physicalID] = true
			}
			newUsed[segment.PhysicalID] = true
			visit(segment.ToNode, newOutbound, newInbound, newUsed, traveled+segment.LengthMeters)
		}
	}
	visit(current.ToNode, nil, nil, map[string]bool{}, 0)
	if len(candidates) == 0 {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].distance > candidates[j].distance })
	best := candidates[0]
	for _, other := range candidates[1:] {
		if best.outbound[0] != other.outbound[0] {
			return nil, false
		}
	}
	result := make([]TraversedPortion, 0, len(best.outbound)+len(best.inbound))
	for _, id := range append(best.outbound, best.inbound...) {
		segment := graph.segments[id]
		result = append(result, graph.portion(id, 0, segment.LengthMeters))
	}
	return result, true
}

func reverseDirectedSegmentID(segment DirectedSegment, graph compiledGraph) (string, bool) {
	for _, id := range graph.outgoing[segment.ToNode] {
		candidate := graph.segments[id]
		if candidate.ToNode == segment.FromNode && candidate.Direction != segment.Direction && samePhysicalSegment(segment, candidate) {
			return id, true
		}
	}
	return "", false
}

func rawSupportsOutAndBackSegmentChain(observations []Observation, outbound, inbound []string, graph compiledGraph, maximumRawOffsetMeters float64) bool {
	if len(outbound) == 0 || len(outbound) != len(inbound) {
		return false
	}
	points := make([]Point, 0, len(outbound)+len(inbound)+1)
	points = append(points, graph.segments[outbound[0]].From)
	for _, id := range outbound {
		points = append(points, graph.segments[id].To)
	}
	for _, id := range inbound {
		points = append(points, graph.segments[id].To)
	}
	for start := 0; start < len(observations); start++ {
		if observationPointOffset(observations[start], points[0]) > maximumRawOffsetMeters {
			continue
		}
		observationIndex := start
		supported := true
		for _, point := range points[1:] {
			found := false
			for observationIndex++; observationIndex < len(observations); observationIndex++ {
				if observationPointOffset(observations[observationIndex], point) <= maximumRawOffsetMeters {
					found = true
					break
				}
			}
			if !found {
				supported = false
				break
			}
		}
		if supported {
			return true
		}
	}
	return false
}

func observationPointOffset(observation Observation, point Point) float64 {
	return math.Max(0, distance(observation.Point, point)-observation.AccuracyMeters)
}

func replaceTraversalTailWithRawSupportedSameRoad(result *Result, observations []Observation, graph compiledGraph, maximumSegmentMeters, minimumSavings, maximumRawOffsetMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for currentIndex := max(0, len(traversal.Portions)-4); currentIndex+1 < len(traversal.Portions); currentIndex++ {
			current := traversal.Portions[currentIndex]
			currentSegment := graph.segments[current.SegmentID]
			suffix := traversal.Portions[currentIndex+1:]
			if current.ContinuityClass != "road" || current.LogicalPathID == "" || graph.lengths[current.SegmentID]-current.ToMeter > epsilon ||
				len(suffix) > 3 || slicesContainLogicalPath(suffix, current.LogicalPathID) {
				continue
			}
			ids := append([]string(nil), graph.outgoing[currentSegment.ToNode]...)
			sort.Strings(ids)
			for _, id := range ids {
				segment := graph.segments[id]
				if segment.TransitionOnly || segment.ContinuityClass != "road" || segment.LogicalPathID != current.LogicalPathID ||
					segment.LengthMeters < 20 || segment.LengthMeters > maximumSegmentMeters || present[segment.PhysicalID] ||
					segment.LengthMeters+minimumSavings > portionsLength(suffix) || !rawSupportsSegmentByEndpointDistance(raw, segment, maximumRawOffsetMeters, 0.7, 1.5) {
					continue
				}
				traversal.Portions = append(traversal.Portions[:currentIndex+1], graph.portion(id, 0, segment.LengthMeters))
				present[segment.PhysicalID] = true
				break
			}
		}
	}
}

func repairRawSupportedParallelParkingRoutes(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters, maximumRoadOffsetMeters, epsilon float64) {
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
				alternativeGraph := graph.withoutPhysicalPortions(selected, before.SegmentID, after.SegmentID)
				alternative, ok := roadParkingRoute(before, after, alternativeGraph, graph, raw, maximumRouteMeters, maximumRoadOffsetMeters, epsilon)
				if !ok || alternative.distance > selectedLength+maximumExtraMeters || !routeUsesSingleParkingPhysical(before, after, alternative.portions, graph) {
					continue
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func repairRawSupportedSameRoadExcursions(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, minimumSavings, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			beforeSegment := graph.segments[before.SegmentID]
			for end := start + 2; end < len(traversal.Portions) && end <= start+6; end++ {
				after, selected := traversal.Portions[end], traversal.Portions[start+1:end]
				afterSegment := graph.segments[after.SegmentID]
				selectedLength := portionsLength(selected)
				if before.ContinuityClass != "road" || after.ContinuityClass != "road" || before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID ||
					beforeSegment.SourceWayID == 0 || beforeSegment.SourceWayID != afterSegment.SourceWayID || selectedLength > maximumExcursionMeters ||
					slicesContainLogicalPath(selected, before.LogicalPathID) || !allRoadPortions(selected) {
					continue
				}
				best := networkRoute{distance: math.Inf(1)}
				for _, from := range portionBoundaryCandidates(before, true, graph) {
					for _, to := range portionBoundaryCandidates(after, false, graph) {
						route, ok := graph.routeOnLogicalPath(from, to, before.LogicalPathID, maximumExcursionMeters, epsilon)
						if ok && route.distance < best.distance {
							best = route
						}
					}
				}
				if len(selected) < 3 || len(best.portions) == 0 || len(best.portions) > 2 || math.IsInf(best.distance, 1) || best.distance*1.5 > selectedLength ||
					best.distance+minimumSavings > selectedLength || !routeUsesSourceWay(best.portions, graph, beforeSegment.SourceWayID) ||
					!sameRoadRouteRawSupported(best.portions, graph, raw, maximumRawOffsetMeters) {
					continue
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, best.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func routeUsesSourceWay(portions []TraversedPortion, graph compiledGraph, sourceWayID int64) bool {
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].SourceWayID != sourceWayID {
			return false
		}
	}
	return len(portions) > 0
}

func sameRoadRouteRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumRawOffsetMeters float64) bool {
	for _, portion := range portions {
		segment := graph.segments[portion.SegmentID]
		if !rawSupportsSegmentByEndpointDistance(observations, segment, maximumRawOffsetMeters, 0.7, 1.5) &&
			!rawPolylineSupportsDirectedSegmentWithin(observations, segment, 60, maximumRawOffsetMeters) {
			return false
		}
	}
	return len(portions) > 0
}

func removeUnsupportedDirectNetworkDetours(result *Result, observations []Observation, graph compiledGraph, maximumMiddlePortions int, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+maximumMiddlePortions+1; end++ {
				after, middle := traversal.Portions[end], traversal.Portions[start+1:end]
				beforeSegment, beforeExists := graph.segments[before.SegmentID]
				afterSegment, afterExists := graph.segments[after.SegmentID]
				if !beforeExists || !afterExists {
					continue
				}
				from := pointAlongSegment(beforeSegment, before.ToMeter, graph.lengths[before.SegmentID])
				to := pointAlongSegment(afterSegment, after.FromMeter, graph.lengths[after.SegmentID])
				localRaw, rawDistance, ok := rawBetweenEndpoints(raw, from, to, 15)
				if !ok || rawDistance > distance(from, to)*1.25+10 || rawSupportedPortionLength(middle, graph, localRaw, 15) > portionsLength(middle)*0.2 {
					continue
				}
				traversal.Portions = append(append([]TraversedPortion(nil), traversal.Portions[:start+1]...), traversal.Portions[end:]...)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func removeUnsupportedBoundaryNetworkDetours(result *Result, observations []Observation, graph compiledGraph, maximumMiddlePortions, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); i++ {
		first, second := &result.Traversals[i], &result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) < 2 || second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		before := first.Portions[len(first.Portions)-1]
		beforeSegment, beforeExists := graph.segments[before.SegmentID]
		if !beforeExists || before.ToMeter-before.FromMeter > 5 {
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		for end := 1; end < len(second.Portions) && end <= maximumMiddlePortions; end++ {
			after := second.Portions[end]
			afterSegment, afterExists := graph.segments[after.SegmentID]
			if !afterExists {
				continue
			}
			from := pointAlongSegment(beforeSegment, before.ToMeter, graph.lengths[before.SegmentID])
			to := pointAlongSegment(afterSegment, after.FromMeter, graph.lengths[after.SegmentID])
			localRaw, rawDistance, ok := rawBetweenEndpoints(raw, from, to, 15)
			middle := second.Portions[:end]
			afterLength := after.ToMeter - after.FromMeter
			afterSupported := rawPolylineSupportsDirectedSegmentWithin(raw, afterSegment, 60, 15) || afterLength >= 20 && directedSegmentEndpointsRawSupported(raw, afterSegment, 15)
			if !afterSupported && afterLength < 20 && end+1 < len(second.Portions) && second.Portions[end+1].LogicalPathID == after.LogicalPathID {
				nextSegment := graph.segments[second.Portions[end+1].SegmentID]
				afterSupported = rawPolylineSupportsDirectedSegmentWithin(raw, nextSegment, 60, 15)
			}
			if !ok || end < 3 || rawDistance > 200 || !afterSupported || rawSupportedPortionLength(middle, graph, localRaw, 15) > portionsLength(middle)*0.2 {
				continue
			}
			second.Portions = append([]TraversedPortion(nil), second.Portions[end:]...)
			break
		}
	}
}

func repairIncompleteRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters, epsilon float64) {
	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+3; end++ {
				after, selected := traversal.Portions[end], traversal.Portions[start+1:end]
				if before.ContinuityClass != "road" || after.ContinuityClass != "road" || graph.lengths[before.SegmentID]-before.ToMeter <= epsilon ||
					!roadSequenceDisconnected(before, selected, after, graph, epsilon) {
					continue
				}
				alternative, ok := roadRoute(before, after, roadGraph, maximumRouteMeters, epsilon)
				if !ok || alternative.distance > portionsLength(selected)+maximumExtraMeters || !roadRouteStrictlyContinuesBoundaryPath(alternative.portions, before, after) ||
					roadRouteStrictlyContinuesBoundaryPath(selected, before, after) || !roadTurnPortionsRawSupported(alternative.portions, graph, raw, 60, 15) {
					continue
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func repairBoundaryIncompleteRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			j++
		}
		if j == len(result.Traversals) {
			break
		}
		first, second := &result.Traversals[i], result.Traversals[j]
		if len(first.Portions) == 0 || len(second.Portions) < 2 || second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before, selected, after := first.Portions[len(first.Portions)-1], second.Portions[:1], second.Portions[1]
		stub := selected[0]
		beforeSegment, beforeExists := graph.segments[before.SegmentID]
		stubSegment, stubExists := graph.segments[stub.SegmentID]
		afterSegment, afterExists := graph.segments[after.SegmentID]
		remaining := graph.lengths[before.SegmentID] - before.ToMeter
		if !beforeExists || !stubExists || !afterExists || beforeSegment.TransitionOnly || stubSegment.TransitionOnly || afterSegment.TransitionOnly ||
			before.ContinuityClass != "road" || stub.ContinuityClass != "road" || after.ContinuityClass != "road" || remaining <= epsilon ||
			graph.lengths[stub.SegmentID]-stub.ToMeter > epsilon || after.FromMeter > epsilon ||
			beforeSegment.ToNode == stubSegment.FromNode || stubSegment.ToNode != afterSegment.FromNode ||
			before.LogicalPathID == "" || stub.LogicalPathID == "" || after.LogicalPathID == "" ||
			before.LogicalPathID == stub.LogicalPathID || before.LogicalPathID == after.LogicalPathID || stub.LogicalPathID == after.LogicalPathID {
			i++
			continue
		}
		connectorIDs := make([]string, 0, 1)
		for _, id := range graph.outgoing[beforeSegment.ToNode] {
			segment := graph.segments[id]
			if !segment.TransitionOnly && segment.ContinuityClass == "road" && segment.ToNode == afterSegment.FromNode &&
				segment.LogicalPathID == before.LogicalPathID && !samePhysicalSegment(segment, stubSegment) && !samePhysicalSegment(segment, afterSegment) {
				connectorIDs = append(connectorIDs, id)
			}
		}
		if len(connectorIDs) != 1 {
			i++
			continue
		}
		connector := graph.segments[connectorIDs[0]]
		alternativeDistance := remaining + connector.LengthMeters
		if alternativeDistance > maximumRouteMeters+epsilon || alternativeDistance > stub.ToMeter-stub.FromMeter+maximumExtraMeters {
			i++
			continue
		}
		alternative := []TraversedPortion{
			graph.portion(before.SegmentID, before.ToMeter, graph.lengths[before.SegmentID]),
			graph.portion(connector.ID, 0, connector.LengthMeters),
		}
		first.Portions = append(first.Portions, alternative...)
		first.Portions = append(first.Portions, second.Portions[1:]...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[j+1:]...)
	}
}

func removeBoundaryDisconnectedSingleRoadStubs(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); i++ {
		first, second := &result.Traversals[i], &result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) < 2 || second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		before, middle, after := first.Portions[len(first.Portions)-1], second.Portions[0], second.Portions[1]
		segment, exists := graph.segments[middle.SegmentID]
		if !exists || before.ContinuityClass != "road" || middle.ContinuityClass != "road" || after.ContinuityClass != "road" || middle.ToMeter-middle.FromMeter > maximumMeters ||
			!portionsConnected(before, after, graph, epsilon) || portionsConnected(before, middle, graph, epsilon) == portionsConnected(middle, after, graph, epsilon) {
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		if rawPolylineSupportsDirectedSegmentWithin(raw, segment, 60, maximumRawOffsetMeters) || rawExtendsPastDirectedEndpoint(raw, segment, maximumRawOffsetMeters, 5) {
			continue
		}
		second.Portions = append([]TraversedPortion(nil), second.Portions[1:]...)
	}
}

func removeUnsupportedTraversalHeadRoadStubs(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumRawOffsetMeters float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 2 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		first, second := traversal.Portions[0], traversal.Portions[1]
		firstSegment, firstExists := graph.segments[first.SegmentID]
		secondSegment, secondExists := graph.segments[second.SegmentID]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		if !firstExists || !secondExists {
			continue
		}
		firstStart := pointAlongSegment(firstSegment, first.FromMeter, graph.lengths[first.SegmentID])
		if first.ContinuityClass != "road" || second.ContinuityClass != "road" || first.ToMeter-first.FromMeter > maximumMeters ||
			firstSegment.ToNode != secondSegment.FromNode || rawPolylineSupportsDirectedSegmentWithin(raw, firstSegment, 60, maximumRawOffsetMeters) ||
			rawExtendsPastDirectedEndpoint(raw, firstSegment, maximumRawOffsetMeters, 5) || endpointRawSupported(raw, firstStart, maximumRawOffsetMeters, 1) ||
			!rawPolylineSupportsDirectedSegmentWithin(raw, secondSegment, 60, maximumRawOffsetMeters) {
			continue
		}
		traversal.Portions = append([]TraversedPortion(nil), traversal.Portions[1:]...)
	}
}

func rawBetweenEndpoints(observations []Observation, from, to Point, maximumOffsetMeters float64) ([]Observation, float64, bool) {
	for start := 0; start+1 < len(observations); start++ {
		if math.Max(0, distance(observations[start].Point, from)-observations[start].AccuracyMeters) > maximumOffsetMeters {
			continue
		}
		distanceMeters := 0.0
		for end := start + 1; end < len(observations); end++ {
			distanceMeters += distance(observations[end-1].Point, observations[end].Point)
			if math.Max(0, distance(observations[end].Point, to)-observations[end].AccuracyMeters) <= maximumOffsetMeters {
				return observations[start : end+1], distanceMeters, true
			}
		}
	}
	return nil, 0, false
}

func removeUnsupportedTraversalHeadDetours(result *Result, observations []Observation, graph compiledGraph, maximumMiddlePortions int) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 4 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for end := 3; end < len(traversal.Portions) && end <= maximumMiddlePortions; end++ {
			after := traversal.Portions[end]
			afterSegment, exists := graph.segments[after.SegmentID]
			if !exists {
				continue
			}
			afterLength := after.ToMeter - after.FromMeter
			afterSupported := rawPolylineSupportsDirectedSegmentWithin(raw, afterSegment, 60, 15)
			if !afterSupported && afterLength < 20 && end+1 < len(traversal.Portions) && traversal.Portions[end+1].LogicalPathID == after.LogicalPathID {
				afterSupported = rawPolylineSupportsDirectedSegmentWithin(raw, graph.segments[traversal.Portions[end+1].SegmentID], 60, 15)
			}
			middle := traversal.Portions[:end]
			if !afterSupported || rawSupportedPortionLength(middle, graph, raw, 15) > portionsLength(middle)*0.2 {
				continue
			}
			traversal.Portions = append([]TraversedPortion(nil), traversal.Portions[end:]...)
			break
		}
	}
}

func rawSupportedPortionLength(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumOffsetMeters float64) float64 {
	total := 0.0
	for _, portion := range portions {
		if segment, exists := graph.segments[portion.SegmentID]; exists && rawPolylineSupportsDirectedSegmentWithin(observations, segment, 60, maximumOffsetMeters) {
			total += portion.ToMeter - portion.FromMeter
		}
	}
	return total
}

func routeUsesSingleParkingPhysical(before, after TraversedPortion, portions []TraversedPortion, graph compiledGraph) bool {
	physical := make(map[string]bool)
	for _, portion := range append(append([]TraversedPortion{before}, portions...), after) {
		segment, exists := graph.segments[portion.SegmentID]
		if exists && segment.ParkingAisleConnector {
			physical[segment.PhysicalID] = true
		}
	}
	return len(physical) == 1
}

func portionsContainParking(portions []TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].ParkingAisleConnector {
			return true
		}
	}
	return false
}

func roadOrParkingBoundary(portion TraversedPortion, graph compiledGraph) bool {
	segment, exists := graph.segments[portion.SegmentID]
	return exists && (portion.ContinuityClass == "road" || segment.ParkingAisleConnector)
}

func (graph compiledGraph) withoutPhysicalPortions(excluded []TraversedPortion, boundaryIDs ...string) compiledGraph {
	excludedPhysical := make(map[string]bool, len(excluded))
	for _, portion := range excluded {
		excludedPhysical[graph.segments[portion.SegmentID].PhysicalID] = true
	}
	boundaries := make(map[string]bool, len(boundaryIDs))
	for _, id := range boundaryIDs {
		boundaries[id] = true
	}
	filtered := Graph{MaxContinuityConnectorMeters: graph.maxContinuityConnectorMeters, MaxDrivewayConnectorMeters: graph.maxDrivewayConnectorMeters,
		MaxAccessoryConnectorMeters: graph.maxAccessoryConnectorMeters, MaxParkingAisleConnectorMeters: graph.maxParkingAisleConnectorMeters}
	for id, segment := range graph.segments {
		if excludedPhysical[segment.PhysicalID] || segment.TransitionOnly && !segment.ParkingAisleConnector && !boundaries[id] ||
			segment.ContinuityClass != "road" && !segment.ParkingAisleConnector && !boundaries[id] {
			continue
		}
		filtered.Segments = append(filtered.Segments, segment)
	}
	return compileGraph(filtered)
}

func roadParkingRoute(before, after TraversedPortion, routeGraph, sourceGraph compiledGraph, observations []Observation, maximumRouteMeters, maximumRoadOffsetMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, routeGraph) {
		for _, to := range portionBoundaryCandidates(after, false, routeGraph) {
			route, ok := routeGraph.routeWithBoundaryClasses(from, to, maximumRouteMeters, epsilon, "road", "road")
			if !ok || route.distance <= epsilon || route.distance >= best.distance || !roadParkingPortionsRawSupported(route.portions, sourceGraph, observations, maximumRoadOffsetMeters) {
				continue
			}
			best = route
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func roadParkingPortionsRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumRoadOffsetMeters float64) bool {
	for _, portion := range portions {
		segment, exists := graph.segments[portion.SegmentID]
		if !exists {
			return false
		}
		if segment.ParkingAisleConnector {
			fromMeter, toMeter, ok := parkingAisleRawInterval(observations, segment, parkingAisleRawSupportOffsetMeters)
			if !ok || math.Min(portion.ToMeter, toMeter)-math.Max(portion.FromMeter, fromMeter) < math.Max(5, (portion.ToMeter-portion.FromMeter)*0.5) {
				return false
			}
			continue
		}
		if segment.TransitionOnly || portion.ContinuityClass != "road" ||
			!rawPolylineSupportsDirectedSegmentWithin(observations, segment, 60, maximumRoadOffsetMeters) &&
				!directedSegmentEndpointsRawSupported(observations, segment, maximumRoadOffsetMeters) {
			return false
		}
	}
	return len(portions) > 0
}

func slicesContainLogicalPath(portions []TraversedPortion, logicalPathID string) bool {
	for _, portion := range portions {
		if portion.LogicalPathID == logicalPathID {
			return true
		}
	}
	return false
}

func rawSupportsSegmentByEndpointDistance(observations []Observation, segment DirectedSegment, maximumRawOffsetMeters, minimumRatio, maximumRatio float64) bool {
	started, traveled := false, 0.0
	var previous Point
	for _, observation := range observations {
		if started {
			traveled += distance(previous, observation.Point)
		}
		nearStart := math.Max(0, distance(observation.Point, segment.From)-observation.AccuracyMeters) <= maximumRawOffsetMeters
		if !started && nearStart {
			started, traveled = true, 0
		}
		if started && math.Max(0, distance(observation.Point, segment.To)-observation.AccuracyMeters) <= maximumRawOffsetMeters &&
			traveled >= segment.LengthMeters*minimumRatio && traveled <= segment.LengthMeters*maximumRatio {
			return true
		}
		if started && traveled > segment.LengthMeters*maximumRatio {
			started, traveled = nearStart, 0
		}
		previous = observation.Point
	}
	return false
}

func nextPortion(portions []TraversedPortion, index int) TraversedPortion {
	if index+1 < len(portions) {
		return portions[index+1]
	}
	return TraversedPortion{}
}

func bestRawSupportedRoadDeadEnd(node string, current, next TraversedPortion, observations []Observation, graph compiledGraph, present map[string]bool, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64) ([]TraversedPortion, bool) {
	ids := append([]string(nil), graph.outgoing[node]...)
	sort.Strings(ids)
	for _, id := range ids {
		segment := graph.segments[id]
		if segment.TransitionOnly || segment.ContinuityClass != "road" || segment.LengthMeters < 20 || segment.LengthMeters > maximumMeters || present[segment.PhysicalID] ||
			samePhysicalSegment(segment, graph.segments[current.SegmentID]) || next.SegmentID != "" && samePhysicalSegment(segment, graph.segments[next.SegmentID]) ||
			!rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) &&
				!directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters) {
			continue
		}
		for _, returnID := range graph.outgoing[segment.ToNode] {
			returnSegment := graph.segments[returnID]
			if returnSegment.ToNode != segment.FromNode || returnSegment.Direction == segment.Direction || !samePhysicalSegment(segment, returnSegment) ||
				!rawPolylineSupportsDirectedSegmentWithin(observations, returnSegment, maximumDegrees, maximumRawOffsetMeters) &&
					!directedSegmentEndpointsRawSupported(observations, returnSegment, maximumRawOffsetMeters) {
				continue
			}
			deadEnd := true
			for _, continuationID := range graph.outgoing[segment.ToNode] {
				continuation := graph.segments[continuationID]
				if !samePhysicalSegment(segment, continuation) && !continuation.TransitionOnly {
					deadEnd = false
				}
			}
			if deadEnd {
				return []TraversedPortion{graph.portion(id, 0, segment.LengthMeters), graph.portion(returnID, 0, returnSegment.LengthMeters)}, true
			}
		}
	}
	return nil, false
}

func replaceOverlappingRoadReversalWithPath(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+2 < len(traversal.Portions); i++ {
			before, outbound, inbound, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1], traversal.Portions[i+2]
			outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
			inboundSegment, inboundExists := graph.segments[inbound.SegmentID]
			if !outboundExists || !inboundExists || !samePhysicalSegment(outboundSegment, inboundSegment) || outbound.Direction == inbound.Direction ||
				outbound.ContinuityClass != "road" || inbound.ContinuityClass != "road" || before.ContinuityClass != "road" || after.ContinuityClass != "path" ||
				portionsLength([]TraversedPortion{outbound, inbound}) > maximumExcursionMeters || physicalPortionOverlapMeters(outbound, inbound, outboundSegment.LengthMeters) < 5 {
				continue
			}
			connector, ok := pathTurnConnector(before, after, graph, raw, maximumRouteMeters, maximumDegrees, maximumRawOffsetMeters, epsilon)
			if !ok || connector.distance >= portionsLength([]TraversedPortion{outbound, inbound})-epsilon {
				continue
			}
			replacement := append([]TraversedPortion(nil), traversal.Portions[:i]...)
			replacement = append(replacement, connector.portions...)
			replacement = append(replacement, traversal.Portions[i+2:]...)
			traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
			i--
		}
	}
}

func replaceBoundaryOverlappingRoadReversalWithPath(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, maximumDegrees, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) < 3 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations ||
			hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before := first.Portions[len(first.Portions)-3]
		outbound := first.Portions[len(first.Portions)-2]
		inbound := first.Portions[len(first.Portions)-1]
		after := second.Portions[0]
		outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
		inboundSegment, inboundExists := graph.segments[inbound.SegmentID]
		if !outboundExists || !inboundExists || !samePhysicalSegment(outboundSegment, inboundSegment) || outbound.Direction == inbound.Direction ||
			outbound.ContinuityClass != "road" || inbound.ContinuityClass != "road" || before.ContinuityClass != "road" || after.ContinuityClass != "path" ||
			portionsLength([]TraversedPortion{outbound, inbound}) > maximumExcursionMeters || physicalPortionOverlapMeters(outbound, inbound, outboundSegment.LengthMeters) < 5 {
			i++
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		connector, ok := pathTurnConnector(before, after, graph, raw, maximumRouteMeters, maximumDegrees, maximumRawOffsetMeters, epsilon)
		if !ok {
			i++
			continue
		}
		first.Portions = append(first.Portions[:len(first.Portions)-2], connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func physicalPortionOverlapMeters(first, second TraversedPortion, physicalLength float64) float64 {
	from := math.Max(math.Min(first.SourceFromFraction, first.SourceToFraction), math.Min(second.SourceFromFraction, second.SourceToFraction))
	to := math.Min(math.Max(first.SourceFromFraction, first.SourceToFraction), math.Max(second.SourceFromFraction, second.SourceToFraction))
	return math.Max(0, to-from) * physicalLength
}

func pathTurnConnector(before, after TraversedPortion, graph compiledGraph, observations []Observation, maximumRouteMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	pathGraph := graph.onlyContinuityClassWithBoundaries("path", before.SegmentID, after.SegmentID)
	for _, from := range portionBoundaryCandidates(before, true, pathGraph) {
		for _, to := range portionBoundaryCandidates(after, false, pathGraph) {
			route, ok := pathGraph.routeWithBoundaryClasses(from, to, maximumRouteMeters, epsilon, "road", "path")
			if !ok || route.distance <= epsilon || route.distance >= best.distance || !pathTurnConnectorSupported(route.portions, before, after, graph, observations, maximumDegrees, maximumRawOffsetMeters) {
				continue
			}
			best = route
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func (graph compiledGraph) onlyContinuityClassWithBoundaries(continuityClass string, boundaryIDs ...string) compiledGraph {
	boundaries := make(map[string]bool, len(boundaryIDs))
	for _, id := range boundaryIDs {
		boundaries[id] = true
		if segment, exists := graph.segments[id]; exists {
			for oppositeID, opposite := range graph.segments {
				if samePhysicalSegment(segment, opposite) {
					boundaries[oppositeID] = true
				}
			}
		}
	}
	filtered := Graph{MaxContinuityConnectorMeters: graph.maxContinuityConnectorMeters, MaxDrivewayConnectorMeters: graph.maxDrivewayConnectorMeters,
		MaxAccessoryConnectorMeters: graph.maxAccessoryConnectorMeters, MaxParkingAisleConnectorMeters: graph.maxParkingAisleConnectorMeters}
	for id, segment := range graph.segments {
		if segment.ContinuityClass == continuityClass || boundaries[id] {
			filtered.Segments = append(filtered.Segments, segment)
		}
	}
	return compileGraph(filtered)
}

func pathTurnConnectorSupported(portions []TraversedPortion, before, after TraversedPortion, graph compiledGraph, observations []Observation, maximumDegrees, maximumRawOffsetMeters float64) bool {
	supported := false
	for _, portion := range portions {
		segment, exists := graph.segments[portion.SegmentID]
		if !exists {
			return false
		}
		if samePhysicalSegment(segment, graph.segments[before.SegmentID]) || samePhysicalSegment(segment, graph.segments[after.SegmentID]) {
			continue
		}
		if portion.ContinuityClass != "path" || portion.LogicalPathID != after.LogicalPathID {
			return false
		}
		edgeSupported := rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) ||
			directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters)
		supported = supported || edgeSupported
		if portion.ToMeter-portion.FromMeter > 5 && !edgeSupported {
			return false
		}
	}
	return supported
}

func directedSegmentEndpointsRawSupported(observations []Observation, segment DirectedSegment, maximumRawOffsetMeters float64) bool {
	started := false
	for _, observation := range observations {
		started = started || math.Max(0, distance(observation.Point, segment.From)-observation.AccuracyMeters) <= maximumRawOffsetMeters
		if started && math.Max(0, distance(observation.Point, segment.To)-observation.AccuracyMeters) <= maximumRawOffsetMeters {
			return true
		}
	}
	return false
}

func extendRawSupportedDriveways(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[max(0, traversal.FirstObservation-32):min(len(observations), traversal.LastObservation+33)]
		for portionIndex := 0; portionIndex < len(traversal.Portions); portionIndex++ {
			portion := traversal.Portions[portionIndex]
			segment := graph.segments[portion.SegmentID]
			if segment.TransitionOnly || portion.ContinuityClass != "road" || graph.lengths[portion.SegmentID]-portion.ToMeter > epsilon {
				continue
			}
			addition, ok := bestRawSupportedOutgoingDriveway(segment, raw, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, present)
			if !ok {
				continue
			}
			insert := portionIndex + 1
			traversal.Portions = append(traversal.Portions, make([]TraversedPortion, len(addition))...)
			copy(traversal.Portions[insert+len(addition):], traversal.Portions[insert:len(traversal.Portions)-len(addition)])
			copy(traversal.Portions[insert:], addition)
			for _, added := range addition {
				present[graph.segments[added.SegmentID].PhysicalID] = true
			}
			portionIndex += len(addition)
		}
	}
}

func bestRawSupportedOutgoingDriveway(current DirectedSegment, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64, present map[string]bool) ([]TraversedPortion, bool) {
	if addition, ok := rawSupportedDrivewayAtNode(current.ToNode, observations, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, present); ok {
		return addition, true
	}
	ids := append([]string(nil), graph.outgoing[current.ToNode]...)
	sort.Strings(ids)
	for _, id := range ids {
		segment := graph.segments[id]
		if segment.TransitionOnly || segment.ContinuityClass != "road" || segment.LengthMeters > 5 || segment.LogicalPathID != current.LogicalPathID ||
			present[segment.PhysicalID] || !directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters) {
			continue
		}
		if driveway, ok := rawSupportedDrivewayAtNode(segment.ToNode, observations, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, present); ok {
			return append([]TraversedPortion{graph.portion(id, 0, segment.LengthMeters)}, driveway...), true
		}
	}
	return nil, false
}

func rawSupportedDrivewayAtNode(node string, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64, present map[string]bool) ([]TraversedPortion, bool) {
	ids := append([]string(nil), graph.outgoing[node]...)
	sort.Strings(ids)
	for _, id := range ids {
		segment := graph.segments[id]
		if !segment.DrivewayConnector || segment.LengthMeters > maximumMeters || present[segment.PhysicalID] ||
			(!rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) &&
				!rawExtendsPastDirectedEndpoint(observations, segment, maximumRawOffsetMeters, 5)) {
			continue
		}
		for _, returnID := range graph.outgoing[segment.ToNode] {
			returnSegment := graph.segments[returnID]
			if returnSegment.ToNode == segment.FromNode && returnSegment.Direction != segment.Direction && samePhysicalSegment(segment, returnSegment) {
				return []TraversedPortion{graph.portion(id, 0, segment.LengthMeters), graph.portion(returnID, 0, returnSegment.LengthMeters)}, true
			}
		}
	}
	return nil, false
}

func rawExtendsPastDirectedEndpoint(observations []Observation, segment DirectedSegment, maximumOffsetMeters, minimumBeyondMeters float64) bool {
	dx, dy := segment.To.X-segment.From.X, segment.To.Y-segment.From.Y
	geometryLength := math.Hypot(dx, dy)
	if geometryLength <= 0 || segment.LengthMeters <= 0 {
		return false
	}
	started := false
	for _, observation := range observations {
		alongGeometry := ((observation.Point.X-segment.From.X)*dx + (observation.Point.Y-segment.From.Y)*dy) / geometryLength
		projected := Point{segment.From.X + alongGeometry*dx/geometryLength, segment.From.Y + alongGeometry*dy/geometryLength}
		if math.Max(0, distance(observation.Point, projected)-observation.AccuracyMeters) > maximumOffsetMeters {
			continue
		}
		along := alongGeometry * segment.LengthMeters / geometryLength
		started = started || along >= -maximumOffsetMeters && along <= maximumOffsetMeters
		if started && along >= segment.LengthMeters+minimumBeyondMeters {
			return true
		}
	}
	return false
}

func extendRawSupportedDrivewayChains(result *Result, observations []Observation, graph compiledGraph, maximumChainMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		raw := observations[max(0, traversal.FirstObservation-64):min(len(observations), traversal.LastObservation+65)]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			outbound, inbound := traversal.Portions[i], traversal.Portions[i+1]
			outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
			_, inboundExists := graph.segments[inbound.SegmentID]
			if !outboundExists || !inboundExists || !outboundSegment.DrivewayConnector || !inversePortions(outbound, inbound, epsilon) {
				continue
			}
			ids := append([]string(nil), graph.outgoing[outboundSegment.ToNode]...)
			sort.Strings(ids)
			for _, id := range ids {
				segment := graph.segments[id]
				if !segment.DrivewayConnector || segment.SourceWayID != outboundSegment.SourceWayID || segment.LengthMeters+outboundSegment.LengthMeters > maximumChainMeters || present[segment.PhysicalID] ||
					(!rawPolylineSupportsDirectedSegmentWithin(raw, segment, maximumDegrees, maximumRawOffsetMeters) && !rawSupportsSegmentByEndpointDistance(raw, segment, maximumRawOffsetMeters, 0.7, 1.5) && !directedSegmentEndpointsRawSupported(raw, segment, maximumRawOffsetMeters)) {
					continue
				}
				for _, returnID := range graph.outgoing[segment.ToNode] {
					returnSegment := graph.segments[returnID]
					if !samePhysicalSegment(segment, returnSegment) || returnSegment.ToNode != segment.FromNode ||
						(!rawPolylineSupportsDirectedSegmentWithin(raw, returnSegment, maximumDegrees, maximumRawOffsetMeters) && !rawSupportsSegmentByEndpointDistance(raw, returnSegment, maximumRawOffsetMeters, 0.7, 1.5) && !directedSegmentEndpointsRawSupported(raw, returnSegment, maximumRawOffsetMeters)) {
						continue
					}
					addition := []TraversedPortion{graph.portion(id, 0, segment.LengthMeters), graph.portion(returnID, 0, returnSegment.LengthMeters)}
					traversal.Portions = append(traversal.Portions, TraversedPortion{}, TraversedPortion{})
					copy(traversal.Portions[i+3:], traversal.Portions[i+1:len(traversal.Portions)-2])
					copy(traversal.Portions[i+1:], addition)
					present[segment.PhysicalID] = true
					i += 2
					break
				}
			}
		}
	}
}

func turnaroundRawSupported(portion TraversedPortion, graph compiledGraph, observations []Observation, maximumOffsetMeters float64) bool {
	segment, exists := graph.segments[portion.SegmentID]
	if !exists || segment.LengthMeters <= 0 {
		return false
	}
	fraction := math.Max(0, math.Min(1, portion.ToMeter/segment.LengthMeters))
	turnaround := Point{
		X: segment.From.X + fraction*(segment.To.X-segment.From.X),
		Y: segment.From.Y + fraction*(segment.To.Y-segment.From.Y),
	}
	for _, observation := range observations {
		if math.Max(0, distance(observation.Point, turnaround)-observation.AccuracyMeters) <= maximumOffsetMeters {
			return true
		}
	}
	return false
}

func allRoadPortions(portions []TraversedPortion) bool {
	for _, portion := range portions {
		if portion.ContinuityClass != "road" {
			return false
		}
	}
	return true
}

func repairRawSupportedRoadShortcuts(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, maximumRatio, minimumSavings, maximumContinuityDetour, epsilon float64) {
	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 3 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			before := traversal.Portions[start]
			if before.ContinuityClass != "road" {
				continue
			}
			excursionLength, hasRoadConnector := 0.0, false
			for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
				middle := traversal.Portions[end-1]
				excursionLength += middle.ToMeter - middle.FromMeter
				hasRoadConnector = hasRoadConnector || graph.segments[middle.SegmentID].RoadConnector
				if excursionLength > maximumExcursionMeters {
					break
				}
				after := traversal.Portions[end]
				if after.ContinuityClass != "road" {
					continue
				}
				alternative, ok := roadRoute(before, after, roadGraph, maximumRouteMeters, epsilon)
				if !ok {
					continue
				}
				shorter := hasRoadConnector && alternative.distance <= excursionLength*maximumRatio && excursionLength-alternative.distance >= minimumSavings &&
					roadPortionsRawSupported(alternative.portions, graph, raw)
				selected := traversal.Portions[start+1 : end]
				continuous := hasRoadConnector && roadRouteStrictlyContinuesBoundaryPath(alternative.portions, before, after) &&
					!roadRouteStrictlyContinuesBoundaryPath(selected, before, after) && alternative.distance <= excursionLength+maximumContinuityDetour
				directTurn := !hasRoadConnector && physicalPortionOccurrences(traversal.Portions, selected[0], graph) == 1 &&
					ordinaryRoadTurnSpur(before, selected, after, alternative, graph, 15, epsilon) &&
					alternative.distance <= excursionLength+maximumContinuityDetour &&
					roadTurnBoundariesRawSupported(before, after, graph, raw) &&
					roadTurnConnectorRawSupported(alternative, graph, raw, 15, epsilon)
				if !shorter && !continuous && !directTurn {
					continue
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
				end = start + 1
				excursionLength = 0
			}
		}
	}
}

func physicalPortionOccurrences(portions []TraversedPortion, target TraversedPortion, graph compiledGraph) int {
	targetSegment, exists := graph.segments[target.SegmentID]
	if !exists {
		return 0
	}
	count := 0
	for _, portion := range portions {
		segment, ok := graph.segments[portion.SegmentID]
		if ok && (samePhysicalSegment(segment, targetSegment) ||
			(targetSegment.PhysicalSegmentID == uuid.Nil && targetSegment.PhysicalID == "" && portion.SegmentID == target.SegmentID)) {
			count++
		}
	}
	return count
}

func ordinaryRoadTurnSpur(before TraversedPortion, selected []TraversedPortion, after TraversedPortion, alternative networkRoute, graph compiledGraph, maximumMeters, epsilon float64) bool {
	if len(selected) != 1 || len(alternative.portions) != 1 || portionsLength(selected) > maximumMeters+epsilon || alternative.distance > maximumMeters+epsilon {
		return false
	}
	spur, connector := selected[0], alternative.portions[0]
	beforeSegment, beforeExists := graph.segments[before.SegmentID]
	spurSegment, spurExists := graph.segments[spur.SegmentID]
	afterSegment, afterExists := graph.segments[after.SegmentID]
	connectorSegment, connectorExists := graph.segments[connector.SegmentID]
	if !beforeExists || !spurExists || !afterExists || !connectorExists ||
		before.ContinuityClass != "road" || spur.ContinuityClass != "road" || after.ContinuityClass != "road" || connector.ContinuityClass != "road" ||
		beforeSegment.TransitionOnly || spurSegment.TransitionOnly || afterSegment.TransitionOnly || connectorSegment.TransitionOnly {
		return false
	}
	leftConnected, leftKnown := roadPortionBoundaryConnected(before, spur, graph, epsilon)
	rightConnected, rightKnown := roadPortionBoundaryConnected(spur, after, graph, epsilon)
	if !leftKnown || !rightKnown || leftConnected == rightConnected {
		return false
	}
	if !leftConnected {
		return rightConnected && spur.LogicalPathID != "" && spur.LogicalPathID == after.LogicalPathID &&
			connector.LogicalPathID != "" && connector.LogicalPathID == before.LogicalPathID
	}
	return before.LogicalPathID != "" && spur.LogicalPathID == before.LogicalPathID &&
		connector.LogicalPathID != "" && connector.LogicalPathID == after.LogicalPathID
}

func roadPortionBoundaryConnected(first, second TraversedPortion, graph compiledGraph, epsilon float64) (bool, bool) {
	firstSegment, firstExists := graph.segments[first.SegmentID]
	secondSegment, secondExists := graph.segments[second.SegmentID]
	if !firstExists || !secondExists || graph.lengths[first.SegmentID]-first.ToMeter > epsilon || second.FromMeter > epsilon {
		return false, false
	}
	return firstSegment.ToNode == secondSegment.FromNode, true
}

func roadTurnBoundariesRawSupported(before, after TraversedPortion, graph compiledGraph, observations []Observation) bool {
	beforeSegment, beforeExists := graph.segments[before.SegmentID]
	afterSegment, afterExists := graph.segments[after.SegmentID]
	return beforeExists && afterExists &&
		rawPolylineSupportsDirectedSegmentWithin(observations, beforeSegment, 45, 15) &&
		rawPolylineSupportsDirectedSegmentWithin(observations, afterSegment, 45, 15)
}

func roadTurnConnectorRawSupported(route networkRoute, graph compiledGraph, observations []Observation, maximumMeters, epsilon float64) bool {
	if roadPortionsRawSupported(route.portions, graph, observations) {
		return true
	}
	if len(route.portions) == 0 || route.distance > maximumMeters+epsilon {
		return false
	}
	first, last := route.portions[0], route.portions[len(route.portions)-1]
	firstSegment, firstExists := graph.segments[first.SegmentID]
	lastSegment, lastExists := graph.segments[last.SegmentID]
	if !firstExists || !lastExists {
		return false
	}
	start := pointAlongSegment(firstSegment, first.FromMeter, graph.lengths[first.SegmentID])
	end := pointAlongSegment(lastSegment, last.ToMeter, graph.lengths[last.SegmentID])
	return endpointRawSupported(observations, start, maximumMeters, 1) && endpointRawSupported(observations, end, maximumMeters, 1)
}

func pointAlongSegment(segment DirectedSegment, along, length float64) Point {
	if length <= 0 {
		return segment.From
	}
	fraction := math.Max(0, math.Min(1, along/length))
	return Point{segment.From.X + fraction*(segment.To.X-segment.From.X), segment.From.Y + fraction*(segment.To.Y-segment.From.Y)}
}

func bridgeRawSupportedRoadContinuity(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters float64, maximumObservations int, maximumRatio, minimumSavings, maximumContinuityDetour, epsilon float64) {
	if !resultContainsRoadConnector(result, graph) {
		return
	}
	roadGraph := graph.onlyContinuityClass("road")
	for i := 0; i+1 < len(result.Traversals); i++ {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		windowStart, windowEnd := max(0, first.LastObservation-32), min(len(observations), second.FirstObservation+33)
		raw := observations[windowStart:windowEnd]
		bestBefore, bestAfter, bestImprovement := -1, -1, -math.Inf(1)
		best := networkRoute{}
		for beforeIndex := max(0, len(first.Portions)-3); beforeIndex < len(first.Portions); beforeIndex++ {
			before := first.Portions[beforeIndex]
			if before.ContinuityClass != "road" {
				continue
			}
			for afterIndex := 0; afterIndex < min(3, len(second.Portions)); afterIndex++ {
				after := second.Portions[afterIndex]
				if after.ContinuityClass != "road" {
					continue
				}
				excursion := append(append([]TraversedPortion(nil), first.Portions[beforeIndex+1:]...), second.Portions[:afterIndex]...)
				excursionLength := portionsLength(excursion)
				if excursionLength > maximumExcursionMeters || !containsRoadConnector(excursion, graph) {
					continue
				}
				alternative, ok := roadRoute(before, after, roadGraph, maximumRouteMeters, epsilon)
				if !ok {
					continue
				}
				savings := excursionLength - alternative.distance
				shorter := alternative.distance <= excursionLength*maximumRatio && savings >= minimumSavings &&
					roadPortionsRawSupported(alternative.portions, graph, raw)
				continuous := roadRouteStrictlyContinuesBoundaryPath(alternative.portions, before, after) &&
					!roadRouteStrictlyContinuesBoundaryPath(excursion, before, after) && alternative.distance <= excursionLength+maximumContinuityDetour
				if (!shorter && !continuous) || savings <= bestImprovement {
					continue
				}
				bestBefore, bestAfter, bestImprovement, best = beforeIndex, afterIndex, savings, alternative
			}
		}
		if bestBefore < 0 {
			continue
		}
		first.Portions = append(first.Portions[:bestBefore+1], best.portions...)
		first.Portions = append(first.Portions, second.Portions[bestAfter:]...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
		i--
	}
}

func resultContainsRoadConnector(result *Result, graph compiledGraph) bool {
	for _, traversal := range result.Traversals {
		if containsRoadConnector(traversal.Portions, graph) {
			return true
		}
	}
	return false
}

func containsRoadConnector(portions []TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].RoadConnector {
			return true
		}
	}
	return false
}

func roadRoute(before, after TraversedPortion, graph compiledGraph, maximumMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			route, ok := graph.route(from, to, maximumMeters, epsilon)
			if !ok || route.distance <= epsilon || route.distance >= best.distance {
				continue
			}
			best = route
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func roadPortionsRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation) bool {
	return roadPortionsRawSupportedWithin(portions, graph, observations, 15)
}

func roadPortionsRawSupportedWithin(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumOffsetMeters float64) bool {
	supported := false
	for _, portion := range portions {
		segment := graph.segments[portion.SegmentID]
		if portion.ContinuityClass != "road" {
			return false
		}
		if portion.ToMeter-portion.FromMeter <= 5 {
			continue
		}
		if !rawPolylineSupportsDirectedSegmentWithin(observations, segment, 45, maximumOffsetMeters) {
			return false
		}
		supported = true
	}
	return supported
}

func roadRouteContinuesBoundaryPath(portions []TraversedPortion, before, after TraversedPortion) bool {
	for _, portion := range portions {
		if portion.LogicalPathID != "" && (portion.LogicalPathID == before.LogicalPathID || portion.LogicalPathID == after.LogicalPathID) {
			return true
		}
	}
	return false
}

func roadRouteStrictlyContinuesBoundaryPath(portions []TraversedPortion, before, after TraversedPortion) bool {
	for _, logicalPathID := range []string{before.LogicalPathID, after.LogicalPathID} {
		if logicalPathID == "" || len(portions) == 0 {
			continue
		}
		matches := true
		for _, portion := range portions {
			matches = matches && portion.LogicalPathID == logicalPathID
		}
		if matches {
			return true
		}
	}
	return false
}

func portionsLength(portions []TraversedPortion) float64 {
	total := 0.0
	for _, portion := range portions {
		total += portion.ToMeter - portion.FromMeter
	}
	return total
}

func completeRawSupportedPathEndpoints(result *Result, observations []Observation, graph compiledGraph, maximumTailMeters, maximumNeighborMeters, epsilon float64) {
	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		windowStart, windowEnd := completionRawWindow(*traversal, result.Splits, len(observations), 16)
		raw := observations[windowStart:windowEnd]
		for portionIndex := 0; portionIndex < len(traversal.Portions); portionIndex++ {
			portion := traversal.Portions[portionIndex]
			segment, exists := graph.segments[portion.SegmentID]
			if !exists || segment.TransitionOnly || portion.ContinuityClass != "path" {
				continue
			}
			changed := false
			selectedLength := portion.ToMeter - portion.FromMeter
			if selectedLength >= 5 && portion.FromMeter > epsilon && portion.FromMeter <= maximumTailMeters &&
				endpointRawSupported(raw, segment.From, 5, 1) {
				portion.FromMeter = 0
				changed = true
			}
			remaining := graph.lengths[portion.SegmentID] - portion.ToMeter
			if selectedLength >= 5 && remaining > epsilon && remaining <= maximumTailMeters &&
				endpointRawSupported(raw, segment.To, 5, 1) {
				portion.ToMeter = graph.lengths[portion.SegmentID]
				changed = true
			}
			if changed {
				traversal.Portions[portionIndex] = graph.portion(portion.SegmentID, portion.FromMeter, portion.ToMeter)
			}
			for _, endpoint := range []struct {
				node    string
				reached bool
			}{{segment.FromNode, portion.FromMeter <= epsilon}, {segment.ToNode, graph.lengths[portion.SegmentID]-portion.ToMeter <= epsilon}} {
				if !endpoint.reached {
					continue
				}
				neighbor, insertBefore, ok := rawSupportedShortPathNeighbor(endpoint.node, segment.PhysicalID, raw, graph, maximumNeighborMeters, present)
				if !ok {
					continue
				}
				insert := portionIndex + 1
				if insertBefore {
					insert = portionIndex
					portionIndex++
				}
				addition := graph.portion(neighbor, 0, graph.lengths[neighbor])
				traversal.Portions = append(traversal.Portions, TraversedPortion{})
				copy(traversal.Portions[insert+1:], traversal.Portions[insert:])
				traversal.Portions[insert] = addition
				present[graph.segments[neighbor].PhysicalID] = true
			}
		}
		traversal.Portions = mergeConsecutiveTraversedPortions(traversal.Portions, epsilon)
	}
}

func completionRawWindow(traversal DecodedTraversal, splits []Split, observationCount, context int) (int, int) {
	start, end := max(0, traversal.FirstObservation-context), min(observationCount, traversal.LastObservation+context+1)
	for _, split := range splits {
		if split.Reason != SplitTemporal && split.Reason != SplitSpatial {
			continue
		}
		if split.BeforeObservation > start && split.BeforeObservation <= traversal.FirstObservation {
			start = split.BeforeObservation
		}
		if split.BeforeObservation > traversal.LastObservation && split.BeforeObservation < end {
			end = split.BeforeObservation
		}
	}
	return start, end
}

func endpointRawSupported(observations []Observation, endpoint Point, maximumOffsetMeters, minimumApproachMeters float64) bool {
	for i := 1; i < len(observations); i++ {
		from, to := observations[i-1].Point, observations[i].Point
		fromDistance, toDistance := distance(from, endpoint), distance(to, endpoint)
		fromEffective := math.Max(0, fromDistance-observations[i-1].AccuracyMeters)
		toEffective := math.Max(0, toDistance-observations[i].AccuracyMeters)
		if math.Min(fromEffective, toEffective) > maximumOffsetMeters || distance(from, to) < 2 {
			continue
		}
		if math.Abs(fromDistance-toDistance) >= minimumApproachMeters {
			return true
		}
	}
	return false
}

func rawSupportedShortPathNeighbor(node, currentPhysicalID string, observations []Observation, graph compiledGraph, maximumMeters float64, present map[string]bool) (string, bool, bool) {
	ids := make([]string, 0)
	for id, segment := range graph.segments {
		if segment.FromNode == node || segment.ToNode == node {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		segment := graph.segments[id]
		if segment.PhysicalID == currentPhysicalID || present[segment.PhysicalID] || segment.TransitionOnly ||
			segment.ContinuityClass != "path" || graph.lengths[id] > maximumMeters ||
			!rawPolylineSupportsDirectedSegmentWithin(observations, segment, drivewayConnectorHeadingDegrees, 5) {
			continue
		}
		return id, segment.ToNode == node, true
	}
	return "", false, false
}

func repairRawSupportedAccessoryShortcuts(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumShortcutMeters, maximumRatio, minimumSavings, epsilon float64) {
	for traversalIndex := range result.Traversals {
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 3 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for {
			bestStart, bestEnd, bestSavings := -1, -1, 0.0
			best := networkRoute{}
			for start := 0; start+2 < len(traversal.Portions); start++ {
				before := traversal.Portions[start]
				if !accessoryShortcutBoundary(before, graph) {
					continue
				}
				excursionLength, hasRoad := 0.0, false
				for end := start + 2; end < len(traversal.Portions); end++ {
					middle := traversal.Portions[end-1]
					excursionLength += middle.ToMeter - middle.FromMeter
					middleSegment := graph.segments[middle.SegmentID]
					hasRoad = hasRoad || (!middleSegment.TransitionOnly && middle.ContinuityClass == "road")
					if excursionLength > maximumExcursionMeters {
						break
					}
					after := traversal.Portions[end]
					if !hasRoad || !accessoryShortcutBoundary(after, graph) {
						continue
					}
					shortcut, ok := accessoryOnlyRoute(before, after, graph, maximumShortcutMeters, epsilon)
					savings := excursionLength - shortcut.distance
					if !ok || shortcut.distance > excursionLength*maximumRatio || savings < minimumSavings || savings <= bestSavings ||
						!accessoryPortionsRawSupported(shortcut.portions, graph, raw, drivewayConnectorHeadingDegrees) {
						continue
					}
					bestStart, bestEnd, bestSavings, best = start, end, savings, shortcut
				}
			}
			if bestStart < 0 {
				break
			}
			replacement := make([]TraversedPortion, 0, len(traversal.Portions)-(bestEnd-bestStart-1)+len(best.portions))
			replacement = append(replacement, traversal.Portions[:bestStart+1]...)
			replacement = append(replacement, best.portions...)
			replacement = append(replacement, traversal.Portions[bestEnd:]...)
			traversal.Portions = mergeConsecutiveTraversedPortions(replacement, epsilon)
		}
	}
}

func accessoryShortcutBoundary(portion TraversedPortion, graph compiledGraph) bool {
	segment, exists := graph.segments[portion.SegmentID]
	return exists && (segment.AccessoryConnector || portion.ContinuityClass == "path" || portion.ContinuityClass == "road")
}

func accessoryOnlyRoute(before, after TraversedPortion, graph compiledGraph, maximumMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			route, ok := graph.routeWithBoundaryClasses(from, to, maximumMeters, epsilon, "path", "path")
			if !ok || route.distance <= epsilon || route.distance >= best.distance || !containsAccessoryConnector(route.portions, graph) {
				continue
			}
			accessoryOnly := true
			for _, portion := range route.portions {
				if !graph.segments[portion.SegmentID].AccessoryConnector {
					accessoryOnly = false
					break
				}
			}
			if accessoryOnly {
				best = route
			}
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func accessoryPortionsRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumDegrees float64) bool {
	supported := false
	for _, portion := range portions {
		segment := graph.segments[portion.SegmentID]
		if !segment.AccessoryConnector {
			return false
		}
		edgeSupported := rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, 5)
		supported = supported || edgeSupported
		if portion.ToMeter-portion.FromMeter > accessoryRawSupportLengthMeters && !edgeSupported {
			return false
		}
	}
	return supported
}

func bridgePlausibleAccessoryTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if before.ContinuityClass != "path" && after.ContinuityClass != "path" {
			i++
			continue
		}
		connector, ok := accessoryBridgeRoute(before, after, graph, observations, second.FirstObservation, maximumRouteMeters, epsilon)
		if !ok {
			i++
			continue
		}
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func accessoryBridgeRoute(before, after TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumRouteMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			connector, ok := graph.routeWithBoundaryClasses(from, to, maximumRouteMeters, epsilon, before.ContinuityClass, after.ContinuityClass)
			if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || connector.distance >= best.distance ||
				!containsAccessoryConnector(connector.portions, graph) || !onlyBoundaryAndAccessoryPortions(connector.portions, before, after, graph) ||
				!accessoryConnectorsSupported(connector.portions, graph, observations, transitionIndex, drivewayConnectorHeadingDegrees) {
				continue
			}
			best = connector
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func containsAccessoryConnector(portions []TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].AccessoryConnector {
			return true
		}
	}
	return false
}

func onlyBoundaryAndAccessoryPortions(portions []TraversedPortion, before, after TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		segment := graph.segments[portion.SegmentID]
		if !segment.AccessoryConnector && segment.PhysicalSegmentID != before.PhysicalSegmentID && segment.PhysicalSegmentID != after.PhysicalSegmentID {
			return false
		}
	}
	return true
}

func bridgePlausibleDrivewayTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if !pathRoadClasses(before.ContinuityClass, after.ContinuityClass) {
			i++
			continue
		}
		connector, ok := drivewayBridgeRoute(before, after, graph, observations, second.FirstObservation, maximumRouteMeters, epsilon)
		if !ok {
			i++
			continue
		}
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func drivewayBridgeRoute(before, after TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumRouteMeters, epsilon float64) (networkRoute, bool) {
	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			connector, ok := graph.routeWithBoundaryClasses(from, to, maximumRouteMeters, epsilon, before.ContinuityClass, after.ContinuityClass)
			if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || connector.distance >= best.distance ||
				!containsDrivewayConnector(connector.portions, graph) ||
				!drivewayConnectorsSupported(connector.portions, graph, observations, transitionIndex,
					drivewayConnectorHeadingDegrees, before.ContinuityClass, after.ContinuityClass) {
				continue
			}
			best = connector
		}
	}
	return best, !math.IsInf(best.distance, 1)
}

func portionBoundaryCandidates(portion TraversedPortion, atEnd bool, graph compiledGraph) []Candidate {
	along := portion.FromMeter
	if atEnd {
		along = portion.ToMeter
	}
	result := []Candidate{{SegmentID: portion.SegmentID, AlongMeters: along}}
	segment, exists := graph.segments[portion.SegmentID]
	if !exists {
		return result
	}
	for id, opposite := range graph.segments {
		if id == portion.SegmentID || !samePhysicalSegment(segment, opposite) || opposite.Direction == segment.Direction {
			continue
		}
		result = append(result, Candidate{SegmentID: id, AlongMeters: graph.lengths[id] - along})
		break
	}
	return result
}

func containsDrivewayConnector(portions []TraversedPortion, graph compiledGraph) bool {
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].DrivewayConnector {
			return true
		}
	}
	return false
}

func replaceShortExcursionsBetweenSameClass(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+2 < len(result.Traversals); {
		first, excursion, third := &result.Traversals[i], &result.Traversals[i+1], result.Traversals[i+2]
		if len(first.Portions) == 0 || len(excursion.Portions) == 0 || len(third.Portions) == 0 ||
			third.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, third.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], third.Portions[0]
		excursionClass, uniform := traversalContinuityClass(*excursion)
		if before.ContinuityClass != "path" || after.ContinuityClass != before.ContinuityClass || !uniform ||
			excursionClass == before.ContinuityClass || traversalLength(*excursion) > maximumExcursionMeters {
			i++
			continue
		}
		connector, ok := graph.routeOnContinuityClass(
			Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
			Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.ContinuityClass, maximumRouteMeters, epsilon,
		)
		rawDistance := observationPolylineDistance(observations, first.LastObservation, third.FirstObservation)
		if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || rawDistance <= epsilon ||
			connector.distance < math.Max(0, rawDistance/1.5-20) || connector.distance > rawDistance*1.5+20 ||
			!singleContinuityClass(connector.portions, before.ContinuityClass) {
			i++
			continue
		}
		markTraversalRejected(result, i+1)
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, third.Portions...)
		first.Portions = cancelImmediateBacktracks(first.Portions, epsilon)
		first.Observations = append(first.Observations, third.Observations...)
		first.LastObservation = third.LastObservation
		first.Cost += third.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+3:]...)
	}
}

func traversalContinuityClass(traversal DecodedTraversal) (string, bool) {
	class := ""
	for _, portion := range traversal.Portions {
		if portion.ContinuityClass == "" {
			return "", false
		}
		if class == "" {
			class = portion.ContinuityClass
		} else if class != portion.ContinuityClass {
			return "", false
		}
	}
	return class, class != ""
}

func singleContinuityClass(portions []TraversedPortion, continuityClass string) bool {
	for _, portion := range portions {
		if portion.ContinuityClass != continuityClass {
			return false
		}
	}
	return len(portions) > 0
}

func bridgeSamePathPortionGaps(result *Result, graph compiledGraph, maximumRouteMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		portions := result.Traversals[traversalIndex].Portions
		for i := 0; i+1 < len(portions); i++ {
			before, after := portions[i], portions[i+1]
			if before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID {
				continue
			}
			connector, ok := graph.routeOnLogicalPath(
				Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
				Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.LogicalPathID, maximumRouteMeters, epsilon,
			)
			if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || !singleLogicalPath(connector.portions, before.LogicalPathID) {
				continue
			}
			replacement := append([]TraversedPortion(nil), portions[:i+1]...)
			replacement = append(replacement, connector.portions...)
			replacement = append(replacement, portions[i+1:]...)
			portions = cancelImmediateBacktracks(replacement, epsilon)
			i = 0
		}
		result.Traversals[traversalIndex].Portions = portions
	}
}

func bridgePlausibleSamePathTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID {
			i++
			continue
		}
		connector, ok := graph.routeOnLogicalPath(
			Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
			Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.LogicalPathID, maximumRouteMeters, epsilon,
		)
		rawDistance := observationPolylineDistance(observations, first.LastObservation, second.FirstObservation)
		minimumPlausible := math.Max(0, rawDistance/1.5-20)
		maximumPlausible := rawDistance*1.5 + 20
		independentPath := graph.segments[before.SegmentID].IndependentPath && graph.segments[after.SegmentID].IndependentPath
		if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || rawDistance <= epsilon ||
			(!independentPath && (connector.distance < minimumPlausible || connector.distance > maximumPlausible)) ||
			!singleLogicalPath(connector.portions, before.LogicalPathID) {
			i++
			continue
		}
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = cancelImmediateBacktracks(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func replaceBoundaryShortExcursions(result *Result, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) < 2 || len(second.Portions) == 0 {
			i++
			continue
		}
		after := second.Portions[0]
		beforeIndex, excursionLength := len(first.Portions)-1, 0.0
		for beforeIndex >= 0 && first.Portions[beforeIndex].LogicalPathID != after.LogicalPathID {
			excursionLength += first.Portions[beforeIndex].ToMeter - first.Portions[beforeIndex].FromMeter
			beforeIndex--
		}
		if beforeIndex < 0 || beforeIndex == len(first.Portions)-1 || after.LogicalPathID == "" ||
			excursionLength > maximumExcursionMeters || hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before := first.Portions[beforeIndex]
		connector, ok := graph.routeOnLogicalPath(
			Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
			Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.LogicalPathID, maximumRouteMeters, epsilon,
		)
		if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || !singleLogicalPath(connector.portions, before.LogicalPathID) {
			i++
			continue
		}
		first.Portions = first.Portions[:beforeIndex+1]
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func replaceShortPortionExcursions(result *Result, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, epsilon float64) {
	for traversalIndex := range result.Traversals {
		portions := result.Traversals[traversalIndex].Portions
		for i := 0; i+2 < len(portions); i++ {
			before := portions[i]
			if before.LogicalPathID == "" {
				continue
			}
			j, excursionLength := i+1, 0.0
			for j < len(portions) && portions[j].LogicalPathID != before.LogicalPathID {
				excursionLength += portions[j].ToMeter - portions[j].FromMeter
				j++
			}
			if j >= len(portions) || j == i+1 || excursionLength > maximumExcursionMeters {
				continue
			}
			after := portions[j]
			connector, ok := graph.routeOnLogicalPath(
				Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
				Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.LogicalPathID, maximumRouteMeters, epsilon,
			)
			if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters ||
				!singleLogicalPath(connector.portions, before.LogicalPathID) {
				continue
			}
			replacement := append([]TraversedPortion(nil), portions[:i+1]...)
			replacement = append(replacement, connector.portions...)
			replacement = append(replacement, portions[j:]...)
			portions = cancelImmediateBacktracks(replacement, epsilon)
			i = 0
		}
		result.Traversals[traversalIndex].Portions = portions
	}
}

func replaceShortExcursionsBetweenSamePath(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+2 < len(result.Traversals); {
		first, excursion, third := &result.Traversals[i], &result.Traversals[i+1], result.Traversals[i+2]
		if len(first.Portions) == 0 || len(excursion.Portions) == 0 || len(third.Portions) == 0 ||
			third.FirstObservation-first.LastObservation > maximumObservations || hasHardSplit(result.Splits, first.LastObservation, third.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], third.Portions[0]
		if before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID ||
			traversalLength(*excursion) > maximumExcursionMeters {
			i++
			continue
		}
		connector, ok := graph.routeOnLogicalPath(
			Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
			Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.LogicalPathID, maximumRouteMeters, epsilon,
		)
		rawDistance := observationPolylineDistance(observations, first.LastObservation, third.FirstObservation)
		if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters ||
			!singleLogicalPath(connector.portions, before.LogicalPathID) || rawDistance <= epsilon || connector.distance > rawDistance*1.5+20 {
			i++
			continue
		}
		markTraversalRejected(result, i+1)
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, third.Portions...)
		first.Portions = cancelImmediateBacktracks(first.Portions, epsilon)
		first.Observations = append(first.Observations, third.Observations...)
		first.LastObservation = third.LastObservation
		first.Cost += third.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+3:]...)
	}
}

func traversalLength(traversal DecodedTraversal) float64 {
	total := 0.0
	for _, portion := range traversal.Portions {
		total += portion.ToMeter - portion.FromMeter
	}
	return total
}

func observationPolylineDistance(observations []Observation, first, last int) float64 {
	if first < 0 || last >= len(observations) || first >= last {
		return 0
	}
	total := 0.0
	for i := first + 1; i <= last; i++ {
		total += distance(observations[i-1].Point, observations[i].Point)
	}
	return total
}

func bridgeShortSamePathTraversals(result *Result, graph compiledGraph, maximumMeters float64, maximumObservations int, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID ||
			hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		connector, ok := graph.routeOnLogicalPath(
			Candidate{SegmentID: before.SegmentID, AlongMeters: before.ToMeter},
			Candidate{SegmentID: after.SegmentID, AlongMeters: after.FromMeter}, before.LogicalPathID, maximumMeters, epsilon,
		)
		if !ok || connector.distance <= epsilon || connector.distance > maximumMeters || !singleLogicalPath(connector.portions, before.LogicalPathID) {
			i++
			continue
		}
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = cancelImmediateBacktracks(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func singleLogicalPath(portions []TraversedPortion, logicalPathID string) bool {
	for _, portion := range portions {
		if portion.LogicalPathID != logicalPathID {
			return false
		}
	}
	return len(portions) > 0
}

func cancelTraversalBoundaryBacktracks(result *Result, epsilon float64) {
	for i := 0; i+1 < len(result.Traversals); {
		first, second := &result.Traversals[i], &result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			!inversePortions(first.Portions[len(first.Portions)-1], second.Portions[0], epsilon) {
			i++
			continue
		}
		first.Portions = first.Portions[:len(first.Portions)-1]
		second.Portions = second.Portions[1:]
		if len(first.Portions) == 0 && len(second.Portions) == 0 {
			markTraversalRejected(result, i)
			markTraversalRejected(result, i+1)
			result.Traversals = append(result.Traversals[:i], result.Traversals[i+2:]...)
			if i > 0 {
				i--
			}
			continue
		}
		if len(first.Portions) == 0 {
			markTraversalRejected(result, i)
			result.Traversals = append(result.Traversals[:i], result.Traversals[i+1:]...)
			if i > 0 {
				i--
			}
			continue
		}
		if len(second.Portions) == 0 {
			markTraversalRejected(result, i+1)
			result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
			if i > 0 {
				i--
			}
			continue
		}
	}
}

func markTraversalRejected(result *Result, index int) {
	for i := range result.Traversals[index].Observations {
		observation := &result.Traversals[index].Observations[i]
		observation.Status = ObservationRejected
		if observation.ObservationIndex >= 0 && observation.ObservationIndex < len(result.Observations) {
			result.Observations[observation.ObservationIndex].Status = ObservationRejected
		}
	}
}

func hasHardSplit(splits []Split, after, before int) bool {
	for _, split := range splits {
		if split.BeforeObservation > after && split.BeforeObservation <= before &&
			(split.Reason == SplitTemporal || split.Reason == SplitSpatial) {
			return true
		}
	}
	return false
}

func acceptedCandidates(observation Observation, supplied []Candidate, graph compiledGraph, rules Rules) []Candidate {
	accuracy := effectiveAccuracy(observation.AccuracyMeters, rules)
	radius := math.Min(rules.MaxCandidateDistanceMeters, rules.AccuracyRadiusMultiplier*accuracy)
	seen := make(map[string]bool)
	result := make([]Candidate, 0, len(supplied))
	for _, candidate := range supplied {
		length, ok := graph.lengths[candidate.SegmentID]
		if !ok || !finite(candidate.DistanceMeters) || candidate.DistanceMeters < 0 ||
			!finite(candidate.AttributionOffsetMeters) || candidate.AttributionOffsetMeters < 0 || candidateEffectiveDistance(candidate) > radius ||
			!finite(candidate.PathSwitchCost) || candidate.PathSwitchCost < 0 ||
			!finite(candidate.UTurnCost) || candidate.UTurnCost < 0 ||
			!finite(candidate.RawDistanceCostWeight) || candidate.RawDistanceCostWeight < 0 ||
			!finite(candidate.ContinuityClassCost) || candidate.ContinuityClassCost < 0 ||
			!finite(candidate.AlongMeters) || candidate.AlongMeters < 0 || candidate.AlongMeters > length {
			continue
		}
		key := candidateKey(candidate)
		if !seen[key] {
			seen[key] = true
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(i, j int) bool { return candidateKey(result[i]) < candidateKey(result[j]) })
	return result
}

func decodeGroup(start, end int, observations []Observation, candidates [][]Candidate, graph compiledGraph, rules Rules, result *Result) {
	chunkStart := start
	for chunkStart < end {
		layers := [][]state{initialStates(chunkStart, observations, candidates, graph, rules)}
		i := chunkStart + 1
		for ; i < end; i++ {
			next := transitionStates(i, observations, candidates[i], layers[len(layers)-1], graph, rules)
			if len(next) == 0 {
				break
			}
			layers = append(layers, next)
		}
		finalizeChunk(chunkStart, i, layers, rules, result)
		if i < end {
			result.Splits = append(result.Splits, Split{i, SplitNetwork})
		}
		chunkStart = i
	}
}

func initialStates(index int, observations []Observation, candidates [][]Candidate, graph compiledGraph, rules Rules) []state {
	states := make([]state, len(candidates[index]))
	for i, candidate := range candidates[index] {
		key := candidateKey(candidate)
		states[i] = state{candidate: candidate, cost: emissionCost(index, observations, candidate, graph, rules), pathKey: key, previous: -1}
	}
	return states
}

func transitionStates(index int, observations []Observation, candidates []Candidate, previous []state, graph compiledGraph, rules Rules) []state {
	result := make([]state, 0, len(candidates))
	for _, candidate := range candidates {
		best := state{cost: math.Inf(1)}
		for previousIndex, prior := range previous {
			route, transitionCost, ok := transition(observations[index-1], observations[index], prior.candidate, candidate, graph, rules)
			if !ok || !drivewayConnectorsSupported(route.portions, graph, observations, index, drivewayConnectorHeadingDegrees, "", "") {
				continue
			}
			parkingEvidence, supported := parkingAisleEvidence(route.portions, graph, observations, index)
			if !supported {
				continue
			}
			route.portions = parkingEvidence
			if !accessoryConnectorsSupported(route.portions, graph, observations, index, drivewayConnectorHeadingDegrees) {
				route.portions = withoutAccessoryConnectors(route.portions, graph)
			}
			cost := prior.cost + transitionCost + emissionCost(index, observations, candidate, graph, rules)
			pathKey := prior.pathKey + "\x01" + candidateKey(candidate)
			if cost < best.cost || (cost == best.cost && pathKey < best.pathKey) {
				best = state{candidate, cost, pathKey, previousIndex, route}
			}
		}
		if !math.IsInf(best.cost, 1) {
			result = append(result, best)
		}
	}
	return result
}

func parkingAisleEvidence(portions []TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int) ([]TraversedPortion, bool) {
	hasParkingAisle := false
	for _, portion := range portions {
		hasParkingAisle = hasParkingAisle || graph.segments[portion.SegmentID].ParkingAisleConnector
	}
	if !hasParkingAisle {
		return portions, true
	}
	if transitionIndex < 1 || transitionIndex >= len(observations) {
		return nil, false
	}
	windowStart, windowEnd := max(0, transitionIndex-48), min(len(observations), transitionIndex+49)
	result := append([]TraversedPortion(nil), portions...)
	for index, portion := range portions {
		segment := graph.segments[portion.SegmentID]
		if !segment.ParkingAisleConnector {
			continue
		}
		if parkingAisleShadowedByRoad(segment, graph) {
			return nil, false
		}
		fromMeter, toMeter, ok := parkingAisleRawInterval(observations[windowStart:windowEnd], segment, parkingAisleRawSupportOffsetMeters)
		fromMeter, toMeter = math.Max(portion.FromMeter, fromMeter), math.Min(portion.ToMeter, toMeter)
		required := math.Max(5, (portion.ToMeter-portion.FromMeter)*0.5)
		shortEndpointSupport := segment.LengthMeters <= 10 && rawExtendsPastDirectedEndpoint(observations[windowStart:windowEnd], segment, parkingAisleRawSupportOffsetMeters, 5)
		if (!ok || toMeter-fromMeter < required) && !shortEndpointSupport {
			return nil, false
		}
		if shortEndpointSupport {
			fromMeter, toMeter = portion.FromMeter, portion.ToMeter
		}
		result[index] = graph.portion(portion.SegmentID, fromMeter, toMeter)
	}
	return result, true
}

func parkingAisleShadowedByRoad(parking DirectedSegment, graph compiledGraph) bool {
	return graph.hasShorterOrdinaryRoadPath(parking.FromNode, parking.ToNode, parking.LengthMeters-1e-6) ||
		graph.hasShorterOrdinaryRoadPath(parking.ToNode, parking.FromNode, parking.LengthMeters-1e-6)
}

func parkingAisleRawInterval(observations []Observation, segment DirectedSegment, maximumOffsetMeters float64) (float64, float64, bool) {
	geometryLength := distance(segment.From, segment.To)
	if geometryLength <= 0 || segment.LengthMeters <= 0 {
		return 0, 0, false
	}
	bestFrom, bestTo, runFrom, runTo, previous := 0.0, 0.0, 0.0, 0.0, 0.0
	active := false
	for _, observation := range observations {
		along, offset := segmentProjection(observation.Point, segment, geometryLength)
		if math.Max(0, offset-observation.AccuracyMeters) > maximumOffsetMeters {
			active = false
			continue
		}
		if !active || along+2 < previous {
			runFrom, runTo, previous, active = along, along, along, true
		} else {
			runTo = math.Max(runTo, along)
			previous = math.Max(previous, along)
		}
		if runTo-runFrom > bestTo-bestFrom {
			bestFrom, bestTo = runFrom, runTo
		}
	}
	return bestFrom, bestTo, bestTo-bestFrom >= math.Max(5, segment.LengthMeters*0.5)
}

func accessoryConnectorsSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumDegrees float64) bool {
	for i := 0; i < len(portions); {
		if !graph.segments[portions[i].SegmentID].AccessoryConnector {
			i++
			continue
		}
		j, length, crossingOnly := i, 0.0, true
		for j < len(portions) && graph.segments[portions[j].SegmentID].AccessoryConnector {
			segment := graph.segments[portions[j].SegmentID]
			length += portions[j].ToMeter - portions[j].FromMeter
			crossingOnly = crossingOnly && segment.ContinuityConnector
			j++
		}
		if crossingOnly && length <= graph.maxContinuityConnectorMeters {
			i = j
			continue
		}
		if transitionIndex < 1 || transitionIndex >= len(observations) {
			return false
		}
		windowStart, windowEnd, supported := max(0, transitionIndex-16), min(len(observations), transitionIndex+17), false
		for k := i; k < j; k++ {
			segment := graph.segments[portions[k].SegmentID]
			edgeSupported := rawPolylineSupportsDirectedSegment(observations[windowStart:windowEnd], segment, maximumDegrees)
			supported = supported || edgeSupported
			if portions[k].ToMeter-portions[k].FromMeter > accessoryRawSupportLengthMeters && !edgeSupported {
				return false
			}
		}
		if !supported {
			return false
		}
		i = j
	}
	return true
}

func withoutAccessoryConnectors(portions []TraversedPortion, graph compiledGraph) []TraversedPortion {
	result := make([]TraversedPortion, 0, len(portions))
	for _, portion := range portions {
		if !graph.segments[portion.SegmentID].AccessoryConnector {
			result = append(result, portion)
		}
	}
	return result
}

func finalizeChunk(start, end int, layers [][]state, rules Rules, result *Result) {
	if len(layers) == 0 || len(layers[len(layers)-1]) == 0 {
		return
	}
	last := layers[len(layers)-1]
	best, second := 0, math.Inf(1)
	for i := range last {
		if last[i].cost < last[best].cost || (last[i].cost == last[best].cost && last[i].pathKey < last[best].pathKey) {
			second = last[best].cost
			best = i
		} else if i != best && last[i].cost < second {
			second = last[i].cost
		}
	}
	indices := make([]int, len(layers))
	indices[len(indices)-1] = best
	for i := len(indices) - 1; i > 0; i-- {
		indices[i-1] = layers[i][indices[i]].previous
	}
	portions := make([]TraversedPortion, 0)
	for i := 1; i < len(layers); i++ {
		for _, portion := range layers[i][indices[i]].route.portions {
			length := portion.ToMeter - portion.FromMeter
			if length > rules.PositiveLengthEpsilonMeters {
				portions = append(portions, portion)
			}
		}
	}
	portions = cancelImmediateBacktracks(portions, rules.PositiveLengthEpsilonMeters)
	totalLength := 0.0
	for _, portion := range portions {
		totalLength += portion.ToMeter - portion.FromMeter
	}
	meanCost := last[best].cost / float64(len(layers))
	finalAmbiguous := second-last[best].cost < rules.AmbiguousCostDelta
	ambiguousCount := 0
	for i, layer := range layers {
		chosenCost := layer[indices[i]].cost
		ambiguous := false
		for j := range layer {
			if j != indices[i] && math.Abs(layer[j].cost-chosenCost) < rules.AmbiguousCostDelta {
				ambiguous = true
				break
			}
		}
		if ambiguous {
			ambiguousCount++
		}
	}
	if totalLength < rules.MinTraversalLengthMeters || meanCost > rules.MaxMeanCost ||
		(finalAmbiguous && float64(ambiguousCount)/float64(len(layers)) >= rules.RejectAmbiguousFraction) {
		return
	}
	decoded := make([]DecodedObservation, len(layers))
	for i, layer := range layers {
		candidate := layer[indices[i]].candidate
		status := ObservationMatched
		if observationAmbiguous(layer, indices[i], rules) {
			status = ObservationAmbiguous
		}
		decoded[i] = DecodedObservation{start + i, status, &candidate}
		result.Observations[start+i] = decoded[i]
	}
	result.Traversals = append(result.Traversals, DecodedTraversal{start, end - 1, decoded, portions, last[best].cost})
}

func transition(fromObservation, toObservation Observation, from, to Candidate, graph compiledGraph, rules Rules) (networkRoute, float64, bool) {
	observed := distance(fromObservation.Point, toObservation.Point)
	route, ok := graph.route(from, to, rules.MaxNetworkDistanceMeters, rules.PositiveLengthEpsilonMeters)
	projectionSlack := math.Min(15, from.AttributionOffsetMeters) + math.Min(15, to.AttributionOffsetMeters)
	if !ok || route.distance > observed*rules.MaxNetworkToObservedRatio+rules.NetworkDistanceSlackMeters+projectionSlack {
		return networkRoute{}, 0, false
	}
	dt := elapsedSeconds(fromObservation, toObservation)
	if dt > 0 && route.distance/dt > rules.MaxTransitionSpeedMPS {
		return networkRoute{}, 0, false
	}
	sigma := rules.TransitionSigmaMeters + effectiveAccuracy(fromObservation.AccuracyMeters, rules) + effectiveAccuracy(toObservation.AccuracyMeters, rules)
	delta := (route.distance - observed) / sigma
	cost := delta * delta
	if from.PathGroupID != "" && to.PathGroupID != "" && from.PathGroupID != to.PathGroupID {
		cost += math.Max(from.PathSwitchCost, to.PathSwitchCost)
	}
	if from.ContinuityClass != "" && to.ContinuityClass != "" && from.ContinuityClass != to.ContinuityClass {
		cost += math.Max(from.ContinuityClassCost, to.ContinuityClassCost)
	}
	if from.PhysicalGroupID != "" && from.PhysicalGroupID == to.PhysicalGroupID && from.SegmentID != to.SegmentID {
		cost += math.Max(from.UTurnCost, to.UTurnCost)
	}
	return route, cost, true
}

func drivewayConnectorsSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumDegrees float64, beforeClass, afterClass string) bool {
	hasDriveway := false
	for _, portion := range portions {
		if graph.segments[portion.SegmentID].DrivewayConnector {
			hasDriveway = true
			break
		}
	}
	if !hasDriveway {
		return true
	}
	if transitionIndex < 1 || transitionIndex >= len(observations) {
		return false
	}
	windowStart, windowEnd := max(0, transitionIndex-16), min(len(observations), transitionIndex+17)
	for i := 0; i < len(portions); {
		if !graph.segments[portions[i].SegmentID].DrivewayConnector {
			i++
			continue
		}
		j := i
		for j < len(portions) && graph.segments[portions[j].SegmentID].DrivewayConnector {
			j++
		}
		leftClass, rightClass := transitionBoundaryClasses(portions, i, j, beforeClass, afterClass)
		roadJoin := -1
		if leftClass == "road" {
			roadJoin = i
		} else if rightClass == "road" {
			roadJoin = j - 1
		}
		for k := i; k < j; k++ {
			segment := graph.segments[portions[k].SegmentID]
			length := portions[k].ToMeter - portions[k].FromMeter
			if k == roadJoin && length <= maxDrivewayRoadJoinMeters {
				continue
			}
			if !rawPolylineSupportsDirectedSegment(observations[windowStart:windowEnd], segment, maximumDegrees) {
				return false
			}
		}
		i = j
	}
	return true
}

func rawPolylineSupportsDirectedSegment(observations []Observation, segment DirectedSegment, maximumDegrees float64) bool {
	return rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, 10)
}

func rawPolylineSupportsDirectedSegmentWithin(observations []Observation, segment DirectedSegment, maximumDegrees, maximumOffsetMeters float64) bool {
	geometryLength := distance(segment.From, segment.To)
	if geometryLength <= 0 || segment.LengthMeters <= 0 {
		return false
	}
	startLimit, endLimit := segment.LengthMeters*0.2, segment.LengthMeters*0.8
	segmentHeading := math.Atan2(segment.To.X-segment.From.X, segment.To.Y-segment.From.Y) * 180 / math.Pi
	for i := 0; i+1 < len(observations); i++ {
		startAlong, startDistance := segmentProjection(observations[i].Point, segment, geometryLength)
		if startDistance > maximumOffsetMeters || startAlong > startLimit {
			continue
		}
		for j := i + 1; j < len(observations); j++ {
			endAlong, endDistance := segmentProjection(observations[j].Point, segment, geometryLength)
			if endDistance > maximumOffsetMeters || endAlong < endLimit || endAlong-startAlong < segment.LengthMeters*0.6 {
				continue
			}
			rawHeading := math.Atan2(observations[j].Point.X-observations[i].Point.X, observations[j].Point.Y-observations[i].Point.Y) * 180 / math.Pi
			if angularDifference(rawHeading, segmentHeading) <= maximumDegrees &&
				rawSpanFollowsDirectedSegment(observations[i:j+1], segment, geometryLength, maximumOffsetMeters) {
				return true
			}
		}
	}
	return false
}

func rawSpanFollowsDirectedSegment(observations []Observation, segment DirectedSegment, geometryLength, maximumOffsetMeters float64) bool {
	previousAlong := -math.Inf(1)
	for _, observation := range observations {
		along, offset := segmentProjection(observation.Point, segment, geometryLength)
		if offset > maximumOffsetMeters || along+2 < previousAlong {
			return false
		}
		previousAlong = math.Max(previousAlong, along)
	}
	return true
}

func segmentProjection(point Point, segment DirectedSegment, geometryLength float64) (float64, float64) {
	dx, dy := segment.To.X-segment.From.X, segment.To.Y-segment.From.Y
	fraction := ((point.X-segment.From.X)*dx + (point.Y-segment.From.Y)*dy) / (geometryLength * geometryLength)
	fraction = math.Max(0, math.Min(1, fraction))
	projected := Point{segment.From.X + fraction*dx, segment.From.Y + fraction*dy}
	return fraction * segment.LengthMeters, distance(point, projected)
}

func emissionCost(index int, observations []Observation, candidate Candidate, graph compiledGraph, rules Rules) float64 {
	sigma := effectiveAccuracy(observations[index].AccuracyMeters, rules)
	normalized := candidateEffectiveDistance(candidate) / sigma
	cost := normalized * normalized
	cost += candidate.RawDistanceCostWeight * candidate.DistanceMeters * candidate.DistanceMeters
	heading, ok := observationHeading(index, observations, rules)
	if !ok {
		return cost
	}
	segment := graph.segments[candidate.SegmentID]
	segmentHeading := math.Atan2(segment.To.X-segment.From.X, segment.To.Y-segment.From.Y) * 180 / math.Pi
	if candidate.TangentDegrees != nil {
		segmentHeading = *candidate.TangentDegrees
	}
	delta := angularDifference(heading, segmentHeading) / rules.HeadingSigmaDegrees
	return cost + delta*delta
}

func candidateEffectiveDistance(candidate Candidate) float64 {
	return math.Max(0, candidate.DistanceMeters-candidate.AttributionOffsetMeters)
}

func observationHeading(index int, observations []Observation, rules Rules) (float64, bool) {
	observation := observations[index]
	previousAvailable := index > 0 && observationGap(observations[index-1], observation, rules) == ""
	nextAvailable := index+1 < len(observations) && observationGap(observation, observations[index+1], rules) == ""
	motion := 0.0
	if previousAvailable {
		motion = math.Max(motion, distance(observations[index-1].Point, observation.Point))
	}
	if nextAvailable {
		motion = math.Max(motion, distance(observation.Point, observations[index+1].Point))
	}
	if motion < rules.MinHeadingMotionMeters {
		return 0, false
	}
	if observation.HeadingDegrees != nil && observation.HeadingAccuracy != nil &&
		*observation.HeadingAccuracy <= rules.ReliableHeadingAccuracyDeg {
		return *observation.HeadingDegrees, true
	}
	from, to := observation.Point, observation.Point
	if previousAvailable {
		from = observations[index-1].Point
	}
	if nextAvailable {
		to = observations[index+1].Point
	}
	if distance(from, to) < rules.MinHeadingMotionMeters {
		return 0, false
	}
	return math.Atan2(to.X-from.X, to.Y-from.Y) * 180 / math.Pi, true
}

func observationAmbiguous(states []state, selected int, rules Rules) bool {
	for i := range states {
		if i != selected && math.Abs(states[i].cost-states[selected].cost) < rules.AmbiguousCostDelta {
			return true
		}
	}
	return false
}

func observationGap(a, b Observation, rules Rules) SplitReason {
	if !a.Time.IsZero() && !b.Time.IsZero() && b.Time.Before(a.Time) {
		return SplitTemporal
	}
	if dt := elapsedSeconds(a, b); dt > rules.MaxTemporalGap {
		return SplitTemporal
	}
	if distance(a.Point, b.Point) > rules.MaxSpatialGapMeters {
		return SplitSpatial
	}
	return ""
}

func elapsedSeconds(a, b Observation) float64 {
	if a.Time.IsZero() || b.Time.IsZero() {
		return 0
	}
	return b.Time.Sub(a.Time).Seconds()
}

func effectiveAccuracy(accuracy float64, rules Rules) float64 {
	if !finite(accuracy) || accuracy <= 0 {
		return rules.MaxEffectiveAccuracyMeters
	}
	return math.Max(rules.MinEffectiveAccuracyMeters, math.Min(rules.MaxEffectiveAccuracyMeters, accuracy))
}

func angularDifference(a, b float64) float64 {
	difference := math.Mod(math.Abs(a-b), 360)
	return math.Min(difference, 360-difference)
}

func candidateKey(candidate Candidate) string {
	return fmt.Sprintf("%s/%024.9f/%024.9f/%024.9f/%024.9f", candidate.SegmentID, candidate.AlongMeters, candidate.DistanceMeters, candidate.Projected.X, candidate.Projected.Y)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
