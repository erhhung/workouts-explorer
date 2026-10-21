package coverage

import (
	"context"
	"encoding/binary"
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
	result, _ := MatchContext(context.Background(), observations, supplied, graph, rules)
	return result
}

// MatchContext cooperatively stops decoding and repair work on cancellation.
// Cancellation or exhausted repair tracking returns no partial coverage.
// Cancellation returns the context's Err, leaving cause classification to callers.
func MatchContext(ctx context.Context, observations []Observation, supplied [][]Candidate, graph Graph, rules Rules) (Result, error) {
	return matchContext(ctx, observations, supplied, graph, rules, maxParkingRepairStates, maxParkingRepairStateBytes)
}

func matchContext(ctx context.Context, observations []Observation, supplied [][]Candidate, graph Graph, rules Rules, parkingStateLimit, parkingStateByteLimit int) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result := Result{RulesVersion: rules.Version, MinimumTraversalMeters: rules.MinTraversalLengthMeters, Observations: make([]DecodedObservation, len(observations))}
	for i := range observations {
		if i&127 == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
		result.Observations[i] = DecodedObservation{ObservationIndex: i, Status: ObservationUnmatched}
	}
	if len(observations) == 0 || rules.Version == "" {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		return result, nil
	}
	g := compileGraphContext(ctx, graph)
	candidates := make([][]Candidate, len(observations))
	for i := range observations {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if i < len(supplied) {
			candidates[i] = acceptedCandidates(observations[i], supplied[i], g, rules)
		}
		if len(candidates[i]) > 0 {
			result.Observations[i].Status = ObservationRejected
		}
	}

	groupStart := -1
	for i := range observations {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
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
	if err := ctx.Err(); err != nil {
		return Result{}, err
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
	g.cancelTraversalBoundaryBacktracks(&result, rules.PositiveLengthEpsilonMeters)
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
	for range 2 {
		if err := repairRawSupportedParallelParkingRoutesWithLimits(&result, observations, g, 120, 20, 30, rules.PositiveLengthEpsilonMeters, parkingStateLimit, parkingStateByteLimit); err != nil {
			return Result{}, err
		}
	}
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
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func replaceShortUnsupportedRoadTangentsWithRawRoute(result *Result, observations []Observation, graph compiledGraph, maximumSelectedMeters, maximumRouteMeters float64, maximumRoutePortions int, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return
			}
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+3; end++ {
				if graph.canceled(&checks) {
					return
				}
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
				_, rawDistance, ok := graph.rawBetweenEndpoints(raw, from, to, maximumRawOffsetMeters)
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
				traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func roadPortionReachesGraphDeadEnd(portion TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	segment, exists := graph.segments[portion.SegmentID]
	if !exists {
		return false
	}
	for _, id := range graph.outgoing[segment.ToNode] {
		if graph.canceled(&checks) {
			return false
		}
		continuation := graph.segments[id]
		if continuation.ContinuityClass == "road" && !samePhysicalSegment(segment, continuation) {
			return false
		}
	}
	return true
}

func completeRawSupportedConnectedRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumMissingMeters, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
			before, after, ok := completedConnectedRoadTurn(traversal.Portions[i], traversal.Portions[i+1], raw, graph, maximumMissingMeters, maximumRawOffsetMeters, epsilon)
			if ok {
				traversal.Portions[i], traversal.Portions[i+1] = before, after
			}
		}
	}
}

func completeRawSupportedBoundaryConnectedRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumMissingMeters, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); i++ {
		if graph.canceled(&checks) {
			return
		}
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			if graph.canceled(&checks) {
				return
			}
			j++
		}
		if j == len(result.Traversals) || len(result.Traversals[i].Portions) == 0 ||
			result.Traversals[j].FirstObservation-result.Traversals[i].LastObservation > maximumObservations ||
			graph.hasHardSplit(result.Splits, result.Traversals[i].LastObservation, result.Traversals[j].FirstObservation) {
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
		!graph.endpointRawSupported(raw, beforeSegment.To, maximumRawOffsetMeters, 1) {
		return before, after, false
	}
	return graph.portion(before.SegmentID, before.FromMeter, graph.lengths[before.SegmentID]), graph.portion(after.SegmentID, 0, after.ToMeter), true
}

func removeUnsupportedIsolatedSingleRoadPortions(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 1; i+1 < len(result.Traversals); i++ {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[i]
		if len(traversal.Portions) != 1 || len(result.Traversals[i-1].Portions) == 0 || len(result.Traversals[i+1].Portions) == 0 ||
			result.Traversals[i+1].FirstObservation-result.Traversals[i-1].LastObservation > maximumObservations ||
			graph.hasHardSplit(result.Splits, result.Traversals[i-1].LastObservation, result.Traversals[i+1].FirstObservation) {
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
		if graph.rawPolylineSupportsDirectedSegmentWithin(raw, segment, maximumDegrees, maximumRawOffsetMeters) {
			continue
		}
		result.Traversals = append(result.Traversals[:i], result.Traversals[i+1:]...)
		i--
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+1 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
			before, middle, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1]
			segment, exists := graph.segments[middle.SegmentID]
			if !exists || segment.TransitionOnly || middle.ContinuityClass != "road" || (before.ContinuityClass != "road" && after.ContinuityClass != "road") ||
				middle.FromMeter > epsilon || graph.lengths[middle.SegmentID]-middle.ToMeter > epsilon || middle.ToMeter-middle.FromMeter > maximumMeters ||
				portionsConnected(before, middle, graph, epsilon) || portionsConnected(middle, after, graph, epsilon) ||
				graph.rawPolylineSupportsDirectedSegmentWithin(raw, segment, maximumDegrees, maximumRawOffsetMeters) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+1:]...)
			i--
		}
		for len(traversal.Portions) >= 2 {
			if graph.canceled(&checks) {
				return
			}
			last := len(traversal.Portions) - 1
			if !unsupportedDisconnectedWindowEdgeRoad(traversal.Portions[last], traversal.Portions[last-1], raw, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon) {
				break
			}
			traversal.Portions = traversal.Portions[:last]
		}
		for len(traversal.Portions) >= 2 && unsupportedDisconnectedWindowEdgeRoad(traversal.Portions[0], traversal.Portions[1], raw, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon) {
			if graph.canceled(&checks) {
				return
			}
			traversal.Portions = traversal.Portions[1:]
		}
	}
}

func unsupportedDisconnectedWindowEdgeRoad(candidate, neighbor TraversedPortion, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) bool {
	segment, exists := graph.segments[candidate.SegmentID]
	return exists && !segment.TransitionOnly && candidate.ContinuityClass == "road" && neighbor.ContinuityClass != "" &&
		candidate.FromMeter <= epsilon && graph.lengths[candidate.SegmentID]-candidate.ToMeter <= epsilon && candidate.ToMeter-candidate.FromMeter <= maximumMeters &&
		!portionsConnected(candidate, neighbor, graph, epsilon) && !portionsConnected(neighbor, candidate, graph, epsilon) &&
		!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters)
}

func removeUnsupportedSingleDrivewayOutAndBacks(result *Result, observations []Observation, graph compiledGraph, minimumOneWayMeters, maximumOneWayMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+2 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
			before, outbound, inbound, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1], traversal.Portions[i+2]
			outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
			inboundSegment, inboundExists := graph.segments[inbound.SegmentID]
			connected, known := roadPortionBoundaryConnected(before, after, graph, epsilon)
			if !outboundExists || !inboundExists || !known || !connected || !inversePortions(outbound, inbound, epsilon) ||
				!outboundSegment.DrivewayConnector || !inboundSegment.DrivewayConnector || outbound.ToMeter-outbound.FromMeter < minimumOneWayMeters || outbound.ToMeter-outbound.FromMeter > maximumOneWayMeters ||
				before.ContinuityClass != "road" || after.ContinuityClass != "road" || before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID {
				continue
			}
			outboundSupported := graph.rawPolylineSupportsDirectedSegmentWithin(raw, outboundSegment, maximumDegrees, maximumRawOffsetMeters)
			inboundSupported := graph.rawPolylineSupportsDirectedSegmentWithin(raw, inboundSegment, maximumDegrees, maximumRawOffsetMeters)
			if outboundSupported || inboundSupported {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+2:]...)
			i--
		}
	}
}

func repairBoundarySameRoadDetours(result *Result, observations []Observation, graph compiledGraph, maximumDetourMeters, minimumSavings float64, maximumMiddlePortions int, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			if graph.canceled(&checks) {
				return
			}
			j++
		}
		if j == len(result.Traversals) {
			break
		}
		first, second := &result.Traversals[i], result.Traversals[j]
		if len(first.Portions) == 0 || len(second.Portions) < 4 || second.FirstObservation-first.LastObservation > maximumObservations ||
			graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
			if graph.canceled(&checks) {
				return
			}
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
				if graph.canceled(&checks) {
					return
				}
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
				(!graph.rawSupportsSegmentByEndpointDistance(raw, connector, maximumRawOffsetMeters, 0.7, 1.5) &&
					!graph.rawPolylineSupportsDirectedSegmentWithin(raw, connector, 60, maximumRawOffsetMeters)) {
				continue
			}
			first.Portions = append(first.Portions, graph.portion(connector.ID, 0, connector.LengthMeters))
			first.Portions = append(first.Portions, second.Portions[end:]...)
			first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
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
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+2 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
				(!graph.rawSupportsSegmentByEndpointDistance(raw, afterSegment, maximumRawOffsetMeters, 0.7, 1.5) &&
					!graph.rawPolylineSupportsDirectedSegmentWithin(raw, afterSegment, 60, maximumRawOffsetMeters)) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i+1], traversal.Portions[i+2:]...)
			i = max(-1, i-1)
		}
	}
}

func bridgePlausibleParkingAisleTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations ||
			graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if !parkingAisleBoundaryClasses(before.ContinuityClass, after.ContinuityClass) {
			i++
			continue
		}
		connector, ok := parkingAisleBridgeRoute(before, after, graph, observations, second.FirstObservation, maximumRouteMeters, epsilon)
		rawDistance := graph.observationPolylineDistance(observations, first.LastObservation, second.FirstObservation)
		if !ok || rawDistance <= epsilon || connector.distance > rawDistance*1.5+20 {
			i++
			continue
		}
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, second.Portions...)
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func parkingAisleBridgeRoute(before, after TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumRouteMeters, epsilon float64) (networkRoute, bool) {
	if graph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		if graph.canceled(&checks) {
			return networkRoute{}, false
		}
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			if graph.canceled(&checks) {
				return networkRoute{}, false
			}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
	if graph.err() != nil {
		return nil
	}
	var checks uint

	result := make([]TraversedPortion, 0, len(portions))
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return nil
		}
		if graph.segments[portion.SegmentID].ParkingAisleConnector {
			result = append(result, portion)
		}
	}
	return result
}

func extendRawSupportedParkingAisles(result *Result, observations []Observation, graph compiledGraph, maximumMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[max(0, traversal.FirstObservation-64):min(len(observations), traversal.LastObservation+65)]
		for portionIndex := 0; portionIndex < len(traversal.Portions); portionIndex++ {
			if graph.canceled(&checks) {
				return
			}
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
	if graph.err() != nil {
		return TraversedPortion{}, false
	}
	var checks uint

	ids := append([]string(nil), graph.outgoing[node]...)
	sort.Strings(ids)
	best, bestLength := TraversedPortion{}, 0.0
	for _, id := range ids {
		if graph.canceled(&checks) {
			return TraversedPortion{}, false
		}
		segment := graph.segments[id]
		if !segment.ParkingAisleConnector || segment.LengthMeters > maximumMeters || present[segment.PhysicalID] || parkingAisleShadowedByRoad(segment, graph) {
			continue
		}
		fromMeter, toMeter, ok := graph.parkingAisleRawInterval(observations, segment, parkingAisleRawSupportOffsetMeters)
		if !ok || fromMeter > math.Max(5, segment.LengthMeters*0.2) || toMeter <= bestLength {
			continue
		}
		best, bestLength = graph.portion(id, 0, toMeter), toMeter
	}
	return best, bestLength > 0
}

func containsParkingAisleConnector(portions []TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		if graph.segments[portion.SegmentID].ParkingAisleConnector {
			return true
		}
	}
	return false
}

func onlyBoundaryAndParkingAislePortions(portions []TraversedPortion, before, after TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment := graph.segments[portion.SegmentID]
		if !segment.ParkingAisleConnector && segment.PhysicalSegmentID != before.PhysicalSegmentID && segment.PhysicalSegmentID != after.PhysicalSegmentID {
			return false
		}
	}
	return true
}

func removeUnsupportedRoadOutAndBacks(result *Result, observations []Observation, graph compiledGraph, maximumOneWayMeters, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for center := 0; center+1 < len(traversal.Portions); center++ {
			if graph.canceled(&checks) {
				return
			}
			if !inversePortions(traversal.Portions[center], traversal.Portions[center+1], epsilon) {
				continue
			}
			left, right := center, center+1
			for left > 0 && right+1 < len(traversal.Portions) && inversePortions(traversal.Portions[left-1], traversal.Portions[right+1], epsilon) {
				if graph.canceled(&checks) {
					return
				}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+2 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
			outboundSupported := graph.rawPolylineSupportsDirectedSegmentWithin(raw, outboundSegment, maximumDegrees, maximumRawOffsetMeters) ||
				(sameRoadThrough && ordinaryDeadEnd && turnaroundRawSupported(outbound, graph, raw, maximumRawOffsetMeters)) ||
				!deadEnd && graph.rawExtendsPastDirectedEndpoint(raw, outboundSegment, maximumRawOffsetMeters, 5)
			inboundSupported := graph.rawPolylineSupportsDirectedSegmentWithin(raw, inboundSegment, maximumDegrees, maximumRawOffsetMeters) ||
				!deadEnd && graph.rawExtendsPastDirectedEndpoint(raw, inboundSegment, maximumRawOffsetMeters, 5)
			if outboundSupported && inboundSupported {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+2:]...)
			i--
		}
	}
}

func roadPortionEndsAtDeadEnd(portion TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	segment, exists := graph.segments[portion.SegmentID]
	if !exists || !segment.RoadConnector {
		return false
	}
	for _, id := range graph.outgoing[segment.ToNode] {
		if graph.canceled(&checks) {
			return false
		}
		continuation := graph.segments[id]
		if !continuation.TransitionOnly && continuation.ContinuityClass == "road" && !samePhysicalSegment(segment, continuation) {
			return false
		}
	}
	return true
}

func roadPortionHasNonPhysicalContinuation(portion TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	segment, exists := graph.segments[portion.SegmentID]
	if !exists {
		return false
	}
	for _, id := range graph.outgoing[segment.ToNode] {
		if graph.canceled(&checks) {
			return false
		}
		continuation := graph.segments[id]
		if !continuation.TransitionOnly && continuation.ContinuityClass == "road" && !samePhysicalSegment(segment, continuation) {
			return true
		}
	}
	return false
}

func removeDisconnectedSingleRoadStubs(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+1 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
			before, middle, after := traversal.Portions[i-1], traversal.Portions[i], traversal.Portions[i+1]
			segment, exists := graph.segments[middle.SegmentID]
			leftConnected := portionsConnected(before, middle, graph, epsilon)
			rightConnected := portionsConnected(middle, after, graph, epsilon)
			if !exists || before.ContinuityClass != "road" || middle.ContinuityClass != "road" || after.ContinuityClass != "road" ||
				middle.ToMeter-middle.FromMeter > maximumMeters || !portionsConnected(before, after, graph, epsilon) || leftConnected == rightConnected ||
				graph.rawPolylineSupportsDirectedSegmentWithin(raw, segment, 60, maximumRawOffsetMeters) || graph.rawExtendsPastDirectedEndpoint(raw, segment, maximumRawOffsetMeters, 5) {
				continue
			}
			traversal.Portions = append(traversal.Portions[:i], traversal.Portions[i+1:]...)
			i--
		}
	}
}

func repairDisconnectedRoadPortionGaps(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return
			}
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
				if graph.canceled(&checks) {
					return
				}
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
				traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func roadTurnPortionsRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumDegrees, maximumRawOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment, exists := graph.segments[portion.SegmentID]
		if !exists || (!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) &&
			!graph.directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters)) {
			return false
		}
	}
	return len(portions) > 0
}

func roadSequenceDisconnected(before TraversedPortion, middle []TraversedPortion, after TraversedPortion, graph compiledGraph, epsilon float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	portions := make([]TraversedPortion, 0, len(middle)+2)
	portions = append(portions, before)
	portions = append(portions, middle...)
	portions = append(portions, after)
	for i := 0; i+1 < len(portions); i++ {
		if graph.canceled(&checks) {
			return false
		}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 0; i < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		for i := 0; i < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
				if graph.canceled(&checks) {
					return
				}
				present[graph.segments[portion.SegmentID].PhysicalID] = true
			}
			i += len(addition)
		}
	}
}

func extendRawSupportedBoundaryRoadOutAndBackChains(result *Result, observations []Observation, graph compiledGraph, maximumMeters float64, maximumSegments int, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for i := 0; i+1 < len(result.Traversals); i++ {
		if graph.canceled(&checks) {
			return
		}
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			if graph.canceled(&checks) {
				return
			}
			j++
		}
		if j == len(result.Traversals) {
			break
		}
		first, second := &result.Traversals[i], &result.Traversals[j]
		if len(first.Portions) == 0 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations ||
			graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
}

func bestRawSupportedRoadOutAndBackChain(current, next DirectedSegment, observations []Observation, graph compiledGraph, present map[string]bool, extendDriveway bool, maximumMeters float64, maximumSegments int, maximumRawOffsetMeters float64) ([]TraversedPortion, bool) {
	if graph.err() != nil {
		return nil, false
	}
	var checks uint

	type candidate struct {
		outbound []string
		inbound  []string
		distance float64
	}
	candidates := make([]candidate, 0)
	var visit func(string, []string, []string, map[string]bool, float64)
	visit = func(node string, outbound, inbound []string, used map[string]bool, traveled float64) {
		if graph.err() != nil {
			return
		}
		if len(outbound) >= maximumSegments {
			return
		}
		ids := append([]string(nil), graph.outgoing[node]...)
		sort.Strings(ids)
		for _, id := range ids {
			if graph.canceled(&checks) {
				return
			}
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
				if graph.canceled(&checks) {
					return
				}
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
		if graph.canceled(&checks) {
			return nil, false
		}
		if best.outbound[0] != other.outbound[0] {
			return nil, false
		}
	}
	result := make([]TraversedPortion, 0, len(best.outbound)+len(best.inbound))
	for _, id := range append(best.outbound, best.inbound...) {
		if graph.canceled(&checks) {
			return nil, false
		}
		segment := graph.segments[id]
		result = append(result, graph.portion(id, 0, segment.LengthMeters))
	}
	return result, true
}

func reverseDirectedSegmentID(segment DirectedSegment, graph compiledGraph) (string, bool) {
	if graph.err() != nil {
		return "", false
	}
	var checks uint

	for _, id := range graph.outgoing[segment.ToNode] {
		if graph.canceled(&checks) {
			return "", false
		}
		candidate := graph.segments[id]
		if candidate.ToNode == segment.FromNode && candidate.Direction != segment.Direction && samePhysicalSegment(segment, candidate) {
			return id, true
		}
	}
	return "", false
}

func rawSupportsOutAndBackSegmentChain(observations []Observation, outbound, inbound []string, graph compiledGraph, maximumRawOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	if len(outbound) == 0 || len(outbound) != len(inbound) {
		return false
	}
	points := make([]Point, 0, len(outbound)+len(inbound)+1)
	points = append(points, graph.segments[outbound[0]].From)
	for _, id := range outbound {
		if graph.canceled(&checks) {
			return false
		}
		points = append(points, graph.segments[id].To)
	}
	for _, id := range inbound {
		if graph.canceled(&checks) {
			return false
		}
		points = append(points, graph.segments[id].To)
	}
	for start := 0; start < len(observations); start++ {
		if graph.canceled(&checks) {
			return false
		}
		if observationPointOffset(observations[start], points[0]) > maximumRawOffsetMeters {
			continue
		}
		observationIndex := start
		supported := true
		for _, point := range points[1:] {
			if graph.canceled(&checks) {
				return false
			}
			found := false
			for observationIndex++; observationIndex < len(observations); observationIndex++ {
				if graph.canceled(&checks) {
					return false
				}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for currentIndex := max(0, len(traversal.Portions)-4); currentIndex+1 < len(traversal.Portions); currentIndex++ {
			if graph.canceled(&checks) {
				return
			}
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
				if graph.canceled(&checks) {
					return
				}
				segment := graph.segments[id]
				if segment.TransitionOnly || segment.ContinuityClass != "road" || segment.LogicalPathID != current.LogicalPathID ||
					segment.LengthMeters < 20 || segment.LengthMeters > maximumSegmentMeters || present[segment.PhysicalID] ||
					segment.LengthMeters+minimumSavings > portionsLength(suffix) || !graph.rawSupportsSegmentByEndpointDistance(raw, segment, maximumRawOffsetMeters, 0.7, 1.5) {
					continue
				}
				traversal.Portions = append(traversal.Portions[:currentIndex+1], graph.portion(id, 0, segment.LengthMeters))
				present[segment.PhysicalID] = true
				break
			}
		}
	}
}

const (
	// Also bounds accepted repair work when direction ambiguity forbids certificates.
	maxParkingRepairStates           = 4096
	maxParkingRepairStateBytes       = 512 * 1024
	maxParkingRepairInternedPortions = 16384
	maxParkingRepairInternBytes      = 8 * 1024 * 1024
	maxParkingRepairSuffixNodes      = 16384
	// Comparable key, map/allocator allowance, plus separately charged strings.
	parkingRepairInternEntryBytes = 384
	parkingRepairSuffixEntryBytes = 64
	// One inline push descriptor; its strings already belong to interned portions.
	parkingRepairPushEntryBytes = 4096
)

// ParkingRepairLimitError is a processing limit, not a successful partial match
// or evidence that the route is invalid. It does not authorize an automatic retry.
type ParkingRepairLimitError struct {
	// Accepted work is traversal-local and includes repeats; errors discard its result.
	AcceptedReplacements, ProjectedAcceptedReplacements, MaxAcceptedReplacements int
	CursorAcceptedReplacements                                                   int
	ContinuationDirectionsChecked, ContinuationDirectionsDeterministic           bool
	Field                                                                        string
	// States counts certificate keys per traversal, not work. Bytes/cursor states are cursor-local.
	States, StateBytes, MaxStates, MaxStateBytes int
	TraversalIndex                               int
	Cursor, CursorStates, ProjectedStates        int
	AttemptedKeyBytes, ProjectedStateBytes       int
	// InternBytes conservatively accounts all interning/continuation caches, not measured heap size.
	InternedPortions, InternBytes                     int
	MaxInternedPortions, MaxInternBytes               int
	ProjectedInternedPortions                         int
	AttemptedInternBytes, ProjectedInternBytes        int
	SuffixNodes, MaxSuffixNodes, ProjectedSuffixNodes int
	ContinuationStates                                int
}

func (err *ParkingRepairLimitError) Error() string {
	return fmt.Sprintf("parking repair tracking exhausted (%s; traversal %d states %d -> %d/%d; cursor %d states %d; retained key bytes %d + attempted %d = projected %d/%d; intern portions %d -> %d/%d; cache bytes %d + attempted %d = projected %d/%d; suffix nodes %d -> %d/%d; continuations %d; directions checked %t deterministic %t; accepted replacements %d -> %d/%d; cursor accepted %d)",
		err.Field, err.TraversalIndex, err.States, err.ProjectedStates, err.MaxStates, err.Cursor, err.CursorStates,
		err.StateBytes, err.AttemptedKeyBytes, err.ProjectedStateBytes, err.MaxStateBytes,
		err.InternedPortions, err.ProjectedInternedPortions, err.MaxInternedPortions,
		err.InternBytes, err.AttemptedInternBytes, err.ProjectedInternBytes, err.MaxInternBytes,
		err.SuffixNodes, err.ProjectedSuffixNodes, err.MaxSuffixNodes, err.ContinuationStates, err.ContinuationDirectionsChecked, err.ContinuationDirectionsDeterministic,
		err.AcceptedReplacements, err.ProjectedAcceptedReplacements, err.MaxAcceptedReplacements, err.CursorAcceptedReplacements)
}

func (err *ParkingRepairLimitError) Unwrap() error {
	// Preserve the existing deterministic-limit classification, but never use
	// graph node/edge fields: those authorize adaptive window recovery/retries.
	switch err.Field {
	case "parking repair accepted replacements":
		return &OSMEvaluationLimitError{Field: "parking repair accepted replacements", Limit: err.MaxAcceptedReplacements}
	case "parking repair states":
		return &OSMEvaluationLimitError{Field: "parking repair states", Limit: err.MaxStates}
	case "parking repair interned portions":
		return &OSMEvaluationLimitError{Field: "parking repair interned portions", Limit: err.MaxInternedPortions}
	case "parking repair intern bytes":
		return &OSMEvaluationLimitError{Field: "parking repair intern bytes", Limit: err.MaxInternBytes}
	case "parking repair suffix nodes":
		return &OSMEvaluationLimitError{Field: "parking repair suffix nodes", Limit: err.MaxSuffixNodes}
	default:
		return &OSMEvaluationLimitError{Field: "parking repair state bytes", Limit: err.MaxStateBytes}
	}
}

// Normalize float fields only in this copied map key; exact bits distinguish
// signed zeros and NaN payloads without Go's non-reflexive NaN equality.
type parkingRepairPortionKey struct {
	portion TraversedPortion
	bits    [4]uint64
}

func parkingRepairPortionIdentity(portion TraversedPortion) parkingRepairPortionKey {
	key := parkingRepairPortionKey{portion: portion, bits: [4]uint64{
		math.Float64bits(portion.FromMeter), math.Float64bits(portion.ToMeter),
		math.Float64bits(portion.SourceFromFraction), math.Float64bits(portion.SourceToFraction),
	}}
	key.portion.FromMeter, key.portion.ToMeter, key.portion.SourceFromFraction, key.portion.SourceToFraction = 0, 0, 0, 0
	return key
}

type parkingRepairPush struct {
	before, after                [5]parkingRepairPortionKey
	last, pushedFirst, tailFirst TraversedPortion
	cursor                       int
	valid, pushed, tail          bool
}

type parkingRepairStates struct {
	acceptedReplacements, cursorAcceptedReplacements int
	seen                                             map[string]struct{}
	traversalIndex                                   int
	count, bytes, maxCount, maxBytes                 int
	cursor, cursorStates                             int
	portions                                         map[parkingRepairPortionKey]uint32
	internBytes, maxInternCount, maxInternBytes      int
	suffixes                                         map[parkingRepairSuffixKey]uint32
	continuations                                    map[uint32]int
	maxSuffixNodes                                   int
	checkedDirections, deterministicDirections       bool
	push                                             parkingRepairPush
}

type parkingRepairSuffixKey struct {
	portion, next uint32
}

func newParkingRepairStates(maxCount, maxBytes int) parkingRepairStates {
	return parkingRepairStates{maxCount: maxCount, maxBytes: maxBytes,
		maxInternCount: maxParkingRepairInternedPortions, maxInternBytes: maxParkingRepairInternBytes,
		maxSuffixNodes: maxParkingRepairSuffixNodes}
}

func (states *parkingRepairStates) beginCursor(start int) {
	states.forgetPush()
	states.seen, states.bytes, states.cursorStates, states.cursor = nil, 0, 0, start
	states.cursorAcceptedReplacements = 0
}

func (states *parkingRepairStates) limitError(field string, keyBytes, internBytes int) *ParkingRepairLimitError {
	projectedPortions := len(states.portions)
	if internBytes > 0 {
		projectedPortions++
	}
	return &ParkingRepairLimitError{Field: field,
		AcceptedReplacements: states.acceptedReplacements, ProjectedAcceptedReplacements: states.acceptedReplacements + 1,
		MaxAcceptedReplacements: states.maxCount, CursorAcceptedReplacements: states.cursorAcceptedReplacements,
		States: states.count, StateBytes: states.bytes, MaxStates: states.maxCount, MaxStateBytes: states.maxBytes,
		TraversalIndex: states.traversalIndex,
		Cursor:         states.cursor, CursorStates: states.cursorStates, ProjectedStates: states.count + 1,
		AttemptedKeyBytes: keyBytes, ProjectedStateBytes: states.bytes + keyBytes,
		InternedPortions: len(states.portions), InternBytes: states.internBytes,
		MaxInternedPortions: states.maxInternCount, MaxInternBytes: states.maxInternBytes,
		ProjectedInternedPortions: projectedPortions, AttemptedInternBytes: internBytes, ProjectedInternBytes: states.internBytes + internBytes,
		SuffixNodes: len(states.suffixes), MaxSuffixNodes: states.maxSuffixNodes, ProjectedSuffixNodes: len(states.suffixes), ContinuationStates: len(states.continuations),
		ContinuationDirectionsChecked: states.checkedDirections, ContinuationDirectionsDeterministic: states.deterministicDirections}
}

func (states *parkingRepairStates) suffixLimitError(field string, newNode bool) *ParkingRepairLimitError {
	err := states.limitError(field, 0, parkingRepairSuffixEntryBytes)
	err.ProjectedInternedPortions = len(states.portions)
	if newNode {
		err.ProjectedSuffixNodes++
	}
	return err
}

// The boundary helper chooses the first opposite direction from a map. Certify
// determinism only when there is at most one such segment in the source graph.
func parkingRepairDirectionsDeterministic(graph compiledGraph) (bool, error) {
	type counts struct {
		total      int
		directions [3]int
	}
	index := func(direction SegmentDirection) int {
		switch direction {
		case SegmentForward:
			return 0
		case SegmentReverse:
			return 1
		case "":
			return 2
		default:
			return -1
		}
	}
	uuids := make(map[uuid.UUID]counts)
	physical, nilPhysical := make(map[string]counts), make(map[string]counts)
	var checks uint
	for _, s := range graph.segments {
		if graph.canceled(&checks) {
			return false, graph.err()
		}
		d := index(s.Direction)
		if d < 0 {
			return false, nil
		}
		if s.PhysicalSegmentID != uuid.Nil {
			c := uuids[s.PhysicalSegmentID]
			c.total++
			c.directions[d]++
			uuids[s.PhysicalSegmentID] = c
		}
		if s.PhysicalID != "" {
			c := physical[s.PhysicalID]
			c.total++
			c.directions[d]++
			physical[s.PhysicalID] = c
			if s.PhysicalSegmentID == uuid.Nil {
				c = nilPhysical[s.PhysicalID]
				c.total++
				c.directions[d]++
				nilPhysical[s.PhysicalID] = c
			}
		}
	}
	for _, s := range graph.segments {
		if graph.canceled(&checks) {
			return false, graph.err()
		}
		d, opposite := index(s.Direction), 0
		if s.PhysicalSegmentID != uuid.Nil {
			c := uuids[s.PhysicalSegmentID]
			opposite = c.total - c.directions[d]
			if s.PhysicalID != "" {
				c = nilPhysical[s.PhysicalID]
				opposite += c.total - c.directions[d]
			}
		} else if s.PhysicalID != "" {
			c := physical[s.PhysicalID]
			opposite = c.total - c.directions[d]
		}
		if opposite > 1 {
			return false, nil
		}
	}
	return true, graph.err()
}

// A sealed prefix cannot change under the full-vector merge: future rewrites
// retain its boundary's ID/direction/from-meter, the only left-merge inputs.
// Thus identical ENTIRE unread suffixes have identical future behavior up to
// cursor translation. Repeating one on an uninterrupted old-policy execution
// proves a cycle/pump, unlike repeating a physical segment or local window.
func (states *parkingRepairStates) repeatedContinuation(graph compiledGraph, portions []TraversedPortion, start int, epsilon float64) (bool, error) {
	deterministic, err := states.directionsDeterministic(graph)
	if err != nil || !deterministic {
		return false, err
	}
	var checks uint
	for i := 1; i <= start; i++ {
		if graph.canceled(&checks) {
			return false, graph.err()
		}
		previous := portions[i-1]
		if mergeConsecutivePortions(&previous, portions[i], epsilon) {
			return false, nil
		}
	}
	var root uint32
	for i := len(portions) - 1; i >= start; i-- {
		if graph.canceled(&checks) {
			return false, graph.err()
		}
		id, err := states.intern(graph, portions[i], 0)
		if err != nil {
			return false, err
		}
		key := parkingRepairSuffixKey{portion: id, next: root}
		if known, exists := states.suffixes[key]; exists {
			root = known
			continue
		}
		if len(states.suffixes) >= states.maxSuffixNodes {
			return false, states.suffixLimitError("parking repair suffix nodes", true)
		}
		if !states.internFits(parkingRepairSuffixEntryBytes) {
			return false, states.suffixLimitError("parking repair intern bytes", true)
		}
		if states.suffixes == nil {
			states.suffixes = make(map[parkingRepairSuffixKey]uint32)
		}
		root = uint32(len(states.suffixes) + 1)
		states.suffixes[key] = root
		states.internBytes += parkingRepairSuffixEntryBytes
	}
	if err := graph.err(); err != nil {
		return false, err
	}
	if previous, exists := states.continuations[root]; exists {
		return start >= previous, nil
	}
	if len(states.continuations) >= states.maxCount {
		return false, states.limitError("parking repair states", 0, 0)
	}
	if !states.internFits(parkingRepairSuffixEntryBytes) {
		return false, states.suffixLimitError("parking repair intern bytes", false)
	}
	if states.continuations == nil {
		states.continuations = make(map[uint32]int)
	}
	states.continuations[root] = start
	states.internBytes += parkingRepairSuffixEntryBytes
	return false, nil
}

func (states *parkingRepairStates) directionsDeterministic(graph compiledGraph) (bool, error) {
	if err := graph.err(); err != nil {
		return false, err
	}
	if !states.checkedDirections {
		var err error
		states.deterministicDirections, err = parkingRepairDirectionsDeterministic(graph)
		if err != nil {
			return false, err
		}
		states.checkedDirections = true
	}
	return states.deterministicDirections, nil
}

func (states *parkingRepairStates) forgetContinuations() {
	// A skipped old-policy candidate breaks the proof's uninterrupted execution.
	states.internBytes -= len(states.continuations) * parkingRepairSuffixEntryBytes
	states.continuations = nil
	states.forgetPush()
}

func (states *parkingRepairStates) forgetPush() {
	if states.push.valid {
		states.internBytes -= parkingRepairPushEntryBytes
		states.push = parkingRepairPush{}
	}
}

func (states *parkingRepairStates) rememberPush(push parkingRepairPush) {
	states.forgetPush()
	if push.valid && parkingRepairPushEntryBytes <= states.maxInternBytes-states.internBytes {
		states.push = push
		states.internBytes += parkingRepairPushEntryBytes
	}
}

func (states *parkingRepairStates) internFits(bytes int) bool {
	// Optional push history yields to the existing mandatory caches.
	if bytes > states.maxInternBytes-states.internBytes {
		states.forgetPush()
	}
	return bytes <= states.maxInternBytes-states.internBytes
}

// At a fixed cursor the old policy reads only H's five complete portions.
// Verify S=L+H+X -> L+R+X exactly, with R=N(H[:1]+alternative+H[d:]).
// A closed head cycle with positive pushes is infinite only if EVERY phase's
// last portion cannot merge with X0's first or any pushed block's first. Those
// replay barriers preserve all carried tails even when a merge could rewind.
func (states *parkingRepairStates) pushCycle(graph compiledGraph, current, replacement, alternative []TraversedPortion, start, end int, epsilon float64) (next parkingRepairPush, cycle bool, err error) {
	defer func() {
		if !next.valid {
			states.forgetPush()
		}
	}()
	deterministic, err := states.directionsDeterministic(graph)
	if err != nil || !deterministic || start < 0 || start+5 > len(current) || end < start+2 || end > start+4 {
		return next, false, err
	}
	head, tail := current[start:start+5], current[start+5:]
	local := append([]TraversedPortion{head[0]}, alternative...)
	local = append(local, head[end-start:]...)
	local = graph.mergeConsecutiveTraversedPortions(local, epsilon)
	if err := graph.err(); err != nil {
		return next, false, err
	}
	if len(local) < 5 || len(replacement) != start+len(local)+len(tail) {
		return next, false, nil
	}
	barrier := func(first, second TraversedPortion) bool {
		return !mergeConsecutivePortions(&first, second, epsilon)
	}
	var checks uint
	for _, span := range [][]TraversedPortion{current[:start], tail} {
		for i := 1; i < len(span); i++ {
			if graph.canceled(&checks) {
				return next, false, graph.err()
			}
			if !barrier(span[i-1], span[i]) {
				return next, false, nil
			}
		}
	}
	if start > 0 && !barrier(current[start-1], local[0]) {
		return next, false, nil
	}
	for i, portion := range replacement {
		if graph.canceled(&checks) {
			return next, false, graph.err()
		}
		var expected TraversedPortion
		switch {
		case i < start:
			expected = current[i]
		case i < start+len(local):
			expected = local[i-start]
		default:
			expected = tail[i-start-len(local)]
		}
		if parkingRepairPortionIdentity(portion) != parkingRepairPortionIdentity(expected) {
			return next, false, nil
		}
	}
	for i := range next.before {
		next.before[i] = parkingRepairPortionIdentity(head[i])
		next.after[i] = parkingRepairPortionIdentity(local[i])
	}
	next.cursor, next.last = start, local[len(local)-1]
	if len(local) > 5 {
		next.pushed, next.pushedFirst = true, local[5]
	}
	if len(tail) > 0 {
		next.tail, next.tailFirst = true, tail[0]
	}
	phases := [2]parkingRepairPush{next, {}}
	phaseCount := 1
	cycle = next.pushed && next.before == next.after
	previous := states.push
	if previous.valid && previous.cursor == start && previous.after == next.before && previous.before == next.after && (previous.pushed || next.pushed) {
		phases[1], phaseCount, cycle = previous, 2, true
	}
	for _, phase := range phases[:phaseCount] {
		for _, anchors := range phases[:phaseCount] {
			if anchors.tail && !barrier(phase.last, anchors.tailFirst) || anchors.pushed && !barrier(phase.last, anchors.pushedFirst) {
				return parkingRepairPush{}, false, nil
			}
		}
	}
	if err := graph.err(); err != nil {
		return parkingRepairPush{}, false, err
	}
	next.valid = true
	return next, cycle, nil
}

func (states *parkingRepairStates) intern(graph compiledGraph, portion TraversedPortion, keyBytes int) (uint32, error) {
	if err := graph.err(); err != nil {
		return 0, err
	}
	key := parkingRepairPortionIdentity(portion)
	if id, exists := states.portions[key]; exists {
		return id, nil
	}
	charge := parkingRepairInternEntryBytes
	for _, value := range [...]string{portion.SegmentID, portion.RegionID, portion.LogicalPathID, portion.LocalityID, string(portion.Direction), portion.ContinuityClass} {
		charge += len(value)
	}
	if len(states.portions) >= states.maxInternCount {
		return 0, states.limitError("parking repair interned portions", keyBytes, charge)
	}
	if !states.internFits(charge) {
		return 0, states.limitError("parking repair intern bytes", keyBytes, charge)
	}
	if states.portions == nil {
		states.portions = make(map[parkingRepairPortionKey]uint32)
	}
	id := uint32(len(states.portions) + 1)
	states.portions[key] = id
	states.internBytes += charge
	return id, nil
}

func (states *parkingRepairStates) remember(key string) (bool, error) {
	if _, exists := states.seen[key]; exists {
		return true, nil
	}
	if states.count >= states.maxCount {
		return false, states.limitError("parking repair states", len(key), 0)
	}
	if len(key) > states.maxBytes-states.bytes {
		return false, states.limitError("parking repair state bytes", len(key), 0)
	}
	if states.seen == nil {
		states.seen = make(map[string]struct{})
	}
	states.seen[key] = struct{}{}
	states.count++
	states.cursorStates++
	states.bytes += len(key)
	return false, nil
}

// Comparable full-portion interning and exact packed-ID equality are collision
// safe. IDs persist across cursor resets and are never reused in a traversal.
func (states *parkingRepairStates) key(graph compiledGraph, portions []TraversedPortion, start int) (string, error) {
	if err := graph.err(); err != nil {
		return "", err
	}
	// Two bytes per ID is only a bounded allocation hint: ID 16384 needs three.
	capacity := min(max(0, states.maxBytes), 2*binary.MaxVarintLen64+2*min(len(portions), maxParkingRepairStateBytes/2))
	key := make([]byte, 0, capacity)
	appendValue := func(value uint64) error {
		size := 1
		for remaining := value >> 7; remaining != 0; remaining >>= 7 {
			size++
		}
		if size > states.maxBytes-len(key) {
			return states.limitError("parking repair state bytes", len(key)+size, 0)
		}
		key = binary.AppendUvarint(key, value)
		return nil
	}
	if err := appendValue(uint64(start)); err != nil {
		return "", err
	}
	if err := appendValue(uint64(len(portions))); err != nil {
		return "", err
	}
	var checks uint
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return "", graph.err()
		}
		id, err := states.intern(graph, portion, len(key))
		if err != nil {
			return "", err
		}
		if err := appendValue(uint64(id)); err != nil {
			return "", err
		}
	}
	if err := graph.err(); err != nil {
		return "", err
	}
	return string(key), nil
}

func (states *parkingRepairStates) trackReplacement(graph compiledGraph, current, replacement []TraversedPortion, start int) (bool, error) {
	deterministic, err := states.directionsDeterministic(graph)
	if err != nil {
		return false, err
	}
	// Repeated states are not a cycle certificate when map-selected boundary
	// aliases can differ. Preserve those choices and bound actual accepted work.
	if deterministic {
		if states.seen == nil {
			key, err := states.key(graph, current, start)
			if err != nil {
				return false, err
			}
			if _, err := states.remember(key); err != nil {
				return false, err
			}
		}
		key, err := states.key(graph, replacement, start)
		if err != nil {
			return false, err
		}
		cycle, err := states.remember(key)
		if err != nil || cycle {
			return cycle, err
		}
	}
	if states.acceptedReplacements >= states.maxCount {
		err := states.limitError("parking repair accepted replacements", 0, 0)
		err.ProjectedStates = states.count // No certificate key is attempted here.
		return false, err
	}
	states.acceptedReplacements++
	states.cursorAcceptedReplacements++
	return false, nil
}

func repairRawSupportedParallelParkingRoutes(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters, maximumRoadOffsetMeters, epsilon float64) error {
	return repairRawSupportedParallelParkingRoutesWithLimits(result, observations, graph, maximumRouteMeters, maximumExtraMeters, maximumRoadOffsetMeters, epsilon, maxParkingRepairStates, maxParkingRepairStateBytes)
}

func repairRawSupportedParallelParkingRoutesWithLimits(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters, maximumRoadOffsetMeters, epsilon float64, maxStates, maxStateBytes int) error {
	if err := graph.err(); err != nil {
		return err
	}
	var checks uint

	// Portion replacements allocate new slices. Commit the copied traversal
	// headers only on success, including when a later traversal hits the limit.
	traversals := append([]DecodedTraversal(nil), result.Traversals...)
	for traversalIndex := range traversals {
		if graph.canceled(&checks) {
			return graph.err()
		}
		traversal := &traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		states := newParkingRepairStates(maxStates, maxStateBytes)
		states.traversalIndex = traversalIndex
		stateStart := -1
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return graph.err()
			}
			if start != stateStart {
				// The old backtrack plus for increment resumes at the SAME start.
				// An advanced cursor cannot return here; release its state keys.
				states.beginCursor(start)
				stateStart = start
			}
			before := traversal.Portions[start]
			oldContinuation := true
			for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
				if graph.canceled(&checks) {
					return graph.err()
				}
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
				if oldContinuation {
					pump, err := states.repeatedContinuation(graph, traversal.Portions, start, epsilon)
					if err != nil {
						return err
					}
					if pump {
						states.forgetContinuations()
						oldContinuation = false
						continue
					}
				}
				replacement := append([]TraversedPortion(nil), traversal.Portions[:start+1]...)
				replacement = append(replacement, alternative.portions...)
				replacement = append(replacement, traversal.Portions[end:]...)
				replacement = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
				var push parkingRepairPush
				if oldContinuation {
					var pump bool
					var err error
					push, pump, err = states.pushCycle(graph, traversal.Portions, replacement, alternative.portions, start, end, epsilon)
					if err != nil {
						return err
					}
					if pump {
						states.forgetContinuations()
						oldContinuation = false
						continue
					}
				}
				cycle, err := states.trackReplacement(graph, traversal.Portions, replacement, start)
				if err != nil {
					return err
				}
				if cycle {
					states.forgetContinuations()
					oldContinuation = false
					continue // Reject only this candidate; try the remaining ends.
				}
				states.rememberPush(push)
				traversal.Portions = replacement
				start = max(-1, start-1)
				break
			}
		}
	}
	if err := graph.err(); err != nil {
		return err
	}
	if len(traversals) > 0 {
		result.Traversals = traversals
	}
	return nil
}

func repairRawSupportedSameRoadExcursions(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, minimumSavings, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return
			}
			before := traversal.Portions[start]
			beforeSegment := graph.segments[before.SegmentID]
			for end := start + 2; end < len(traversal.Portions) && end <= start+6; end++ {
				if graph.canceled(&checks) {
					return
				}
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
					if graph.canceled(&checks) {
						return
					}
					for _, to := range portionBoundaryCandidates(after, false, graph) {
						if graph.canceled(&checks) {
							return
						}
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
				traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func routeUsesSourceWay(portions []TraversedPortion, graph compiledGraph, sourceWayID int64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		if graph.segments[portion.SegmentID].SourceWayID != sourceWayID {
			return false
		}
	}
	return len(portions) > 0
}

func sameRoadRouteRawSupported(portions []TraversedPortion, graph compiledGraph, observations []Observation, maximumRawOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment := graph.segments[portion.SegmentID]
		if !graph.rawSupportsSegmentByEndpointDistance(observations, segment, maximumRawOffsetMeters, 0.7, 1.5) &&
			!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, 60, maximumRawOffsetMeters) {
			return false
		}
	}
	return len(portions) > 0
}

func removeUnsupportedDirectNetworkDetours(result *Result, observations []Observation, graph compiledGraph, maximumMiddlePortions int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return
			}
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+maximumMiddlePortions+1; end++ {
				if graph.canceled(&checks) {
					return
				}
				after, middle := traversal.Portions[end], traversal.Portions[start+1:end]
				beforeSegment, beforeExists := graph.segments[before.SegmentID]
				afterSegment, afterExists := graph.segments[after.SegmentID]
				if !beforeExists || !afterExists {
					continue
				}
				from := pointAlongSegment(beforeSegment, before.ToMeter, graph.lengths[before.SegmentID])
				to := pointAlongSegment(afterSegment, after.FromMeter, graph.lengths[after.SegmentID])
				localRaw, rawDistance, ok := graph.rawBetweenEndpoints(raw, from, to, 15)
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
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); i++ {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], &result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) < 2 || second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		before := first.Portions[len(first.Portions)-1]
		beforeSegment, beforeExists := graph.segments[before.SegmentID]
		if !beforeExists || before.ToMeter-before.FromMeter > 5 {
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		for end := 1; end < len(second.Portions) && end <= maximumMiddlePortions; end++ {
			if graph.canceled(&checks) {
				return
			}
			after := second.Portions[end]
			afterSegment, afterExists := graph.segments[after.SegmentID]
			if !afterExists {
				continue
			}
			from := pointAlongSegment(beforeSegment, before.ToMeter, graph.lengths[before.SegmentID])
			to := pointAlongSegment(afterSegment, after.FromMeter, graph.lengths[after.SegmentID])
			localRaw, rawDistance, ok := graph.rawBetweenEndpoints(raw, from, to, 15)
			middle := second.Portions[:end]
			afterLength := after.ToMeter - after.FromMeter
			afterSupported := graph.rawPolylineSupportsDirectedSegmentWithin(raw, afterSegment, 60, 15) || afterLength >= 20 && graph.directedSegmentEndpointsRawSupported(raw, afterSegment, 15)
			if !afterSupported && afterLength < 20 && end+1 < len(second.Portions) && second.Portions[end+1].LogicalPathID == after.LogicalPathID {
				nextSegment := graph.segments[second.Portions[end+1].SegmentID]
				afterSupported = graph.rawPolylineSupportsDirectedSegmentWithin(raw, nextSegment, 60, 15)
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
	if graph.err() != nil {
		return
	}
	var checks uint

	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return
			}
			before := traversal.Portions[start]
			for end := start + 2; end < len(traversal.Portions) && end <= start+3; end++ {
				if graph.canceled(&checks) {
					return
				}
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
				traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
				start = max(-1, start-1)
				break
			}
		}
	}
}

func repairBoundaryIncompleteRoadTurns(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters, maximumExtraMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		j := i + 1
		for j < len(result.Traversals) && len(result.Traversals[j].Portions) == 0 {
			if graph.canceled(&checks) {
				return
			}
			j++
		}
		if j == len(result.Traversals) {
			break
		}
		first, second := &result.Traversals[i], result.Traversals[j]
		if len(first.Portions) == 0 || len(second.Portions) < 2 || second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
			if graph.canceled(&checks) {
				return
			}
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
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[j+1:]...)
	}
}

func removeBoundaryDisconnectedSingleRoadStubs(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); i++ {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], &result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) < 2 || second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		before, middle, after := first.Portions[len(first.Portions)-1], second.Portions[0], second.Portions[1]
		segment, exists := graph.segments[middle.SegmentID]
		if !exists || before.ContinuityClass != "road" || middle.ContinuityClass != "road" || after.ContinuityClass != "road" || middle.ToMeter-middle.FromMeter > maximumMeters ||
			!portionsConnected(before, after, graph, epsilon) || portionsConnected(before, middle, graph, epsilon) == portionsConnected(middle, after, graph, epsilon) {
			continue
		}
		raw := observations[first.FirstObservation : second.LastObservation+1]
		if graph.rawPolylineSupportsDirectedSegmentWithin(raw, segment, 60, maximumRawOffsetMeters) || graph.rawExtendsPastDirectedEndpoint(raw, segment, maximumRawOffsetMeters, 5) {
			continue
		}
		second.Portions = append([]TraversedPortion(nil), second.Portions[1:]...)
	}
}

func removeUnsupportedTraversalHeadRoadStubs(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumRawOffsetMeters float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
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
			firstSegment.ToNode != secondSegment.FromNode || graph.rawPolylineSupportsDirectedSegmentWithin(raw, firstSegment, 60, maximumRawOffsetMeters) ||
			graph.rawExtendsPastDirectedEndpoint(raw, firstSegment, maximumRawOffsetMeters, 5) || graph.endpointRawSupported(raw, firstStart, maximumRawOffsetMeters, 1) ||
			!graph.rawPolylineSupportsDirectedSegmentWithin(raw, secondSegment, 60, maximumRawOffsetMeters) {
			continue
		}
		traversal.Portions = append([]TraversedPortion(nil), traversal.Portions[1:]...)
	}
}

func (graph compiledGraph) rawBetweenEndpoints(observations []Observation, from, to Point, maximumOffsetMeters float64) ([]Observation, float64, bool) {
	if graph.err() != nil {
		return nil, 0, false
	}
	var checks uint

	for start := 0; start+1 < len(observations); start++ {
		if graph.canceled(&checks) {
			return nil, 0, false
		}
		if math.Max(0, distance(observations[start].Point, from)-observations[start].AccuracyMeters) > maximumOffsetMeters {
			continue
		}
		distanceMeters := 0.0
		for end := start + 1; end < len(observations); end++ {
			if graph.canceled(&checks) {
				return nil, 0, false
			}
			distanceMeters += distance(observations[end-1].Point, observations[end].Point)
			if math.Max(0, distance(observations[end].Point, to)-observations[end].AccuracyMeters) <= maximumOffsetMeters {
				return observations[start : end+1], distanceMeters, true
			}
		}
	}
	return nil, 0, false
}

func removeUnsupportedTraversalHeadDetours(result *Result, observations []Observation, graph compiledGraph, maximumMiddlePortions int) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 4 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for end := 3; end < len(traversal.Portions) && end <= maximumMiddlePortions; end++ {
			if graph.canceled(&checks) {
				return
			}
			after := traversal.Portions[end]
			afterSegment, exists := graph.segments[after.SegmentID]
			if !exists {
				continue
			}
			afterLength := after.ToMeter - after.FromMeter
			afterSupported := graph.rawPolylineSupportsDirectedSegmentWithin(raw, afterSegment, 60, 15)
			if !afterSupported && afterLength < 20 && end+1 < len(traversal.Portions) && traversal.Portions[end+1].LogicalPathID == after.LogicalPathID {
				afterSupported = graph.rawPolylineSupportsDirectedSegmentWithin(raw, graph.segments[traversal.Portions[end+1].SegmentID], 60, 15)
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
	if graph.err() != nil {
		return 0
	}
	var checks uint

	total := 0.0
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return 0
		}
		if segment, exists := graph.segments[portion.SegmentID]; exists && graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, 60, maximumOffsetMeters) {
			total += portion.ToMeter - portion.FromMeter
		}
	}
	return total
}

func routeUsesSingleParkingPhysical(before, after TraversedPortion, portions []TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	physical := make(map[string]bool)
	for _, portion := range append(append([]TraversedPortion{before}, portions...), after) {
		if graph.canceled(&checks) {
			return false
		}
		segment, exists := graph.segments[portion.SegmentID]
		if exists && segment.ParkingAisleConnector {
			physical[segment.PhysicalID] = true
		}
	}
	return len(physical) == 1
}

func portionsContainParking(portions []TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
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
	if graph.err() != nil {
		return compiledGraph{ctx: graph.ctx}
	}
	var checks uint

	// This is a routing view: metadata remains available, but route endpoints,
	// boundary candidates and shortest-path expansion all enforce the filter.
	// No graph-sized copies, compilation or cache survive a pair query.
	excludedPhysical := make(map[string]bool, len(excluded))
	if graph.parkingFilter != nil {
		for physical := range graph.parkingFilter.excludedPhysical {
			if graph.canceled(&checks) {
				return compiledGraph{ctx: graph.ctx}
			}
			excludedPhysical[physical] = true
		}
	}
	for _, portion := range excluded {
		if graph.canceled(&checks) {
			return compiledGraph{ctx: graph.ctx}
		}
		excludedPhysical[graph.segments[portion.SegmentID].PhysicalID] = true
	}
	boundaries := make(map[string]bool, len(boundaryIDs))
	for _, id := range boundaryIDs {
		if graph.canceled(&checks) {
			return compiledGraph{ctx: graph.ctx}
		}
		if graph.allowsSegment(id) {
			boundaries[id] = true
		}
	}
	graph.parkingFilter = &parkingRouteFilter{excludedPhysical: excludedPhysical, boundaryIDs: boundaries}
	return graph
}

func roadParkingRoute(before, after TraversedPortion, routeGraph, sourceGraph compiledGraph, observations []Observation, maximumRouteMeters, maximumRoadOffsetMeters, epsilon float64) (networkRoute, bool) {
	if routeGraph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, routeGraph) {
		if routeGraph.canceled(&checks) {
			return networkRoute{}, false
		}

		for _, to := range portionBoundaryCandidates(after, false, routeGraph) {
			if routeGraph.canceled(&checks) {
				return networkRoute{}, false
			}

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
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment, exists := graph.segments[portion.SegmentID]
		if !exists {
			return false
		}
		if segment.ParkingAisleConnector {
			fromMeter, toMeter, ok := graph.parkingAisleRawInterval(observations, segment, parkingAisleRawSupportOffsetMeters)
			if !ok || math.Min(portion.ToMeter, toMeter)-math.Max(portion.FromMeter, fromMeter) < math.Max(5, (portion.ToMeter-portion.FromMeter)*0.5) {
				return false
			}
			continue
		}
		if segment.TransitionOnly || portion.ContinuityClass != "road" ||
			!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, 60, maximumRoadOffsetMeters) &&
				!graph.directedSegmentEndpointsRawSupported(observations, segment, maximumRoadOffsetMeters) {
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

func (graph compiledGraph) rawSupportsSegmentByEndpointDistance(observations []Observation, segment DirectedSegment, maximumRawOffsetMeters, minimumRatio, maximumRatio float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	started, traveled := false, 0.0
	var previous Point
	for _, observation := range observations {
		if graph.canceled(&checks) {
			return false
		}
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
	if graph.err() != nil {
		return nil, false
	}
	var checks uint

	ids := append([]string(nil), graph.outgoing[node]...)
	sort.Strings(ids)
	for _, id := range ids {
		if graph.canceled(&checks) {
			return nil, false
		}
		segment := graph.segments[id]
		if segment.TransitionOnly || segment.ContinuityClass != "road" || segment.LengthMeters < 20 || segment.LengthMeters > maximumMeters || present[segment.PhysicalID] ||
			samePhysicalSegment(segment, graph.segments[current.SegmentID]) || next.SegmentID != "" && samePhysicalSegment(segment, graph.segments[next.SegmentID]) ||
			!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) &&
				!graph.directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters) {
			continue
		}
		for _, returnID := range graph.outgoing[segment.ToNode] {
			if graph.canceled(&checks) {
				return nil, false
			}
			returnSegment := graph.segments[returnID]
			if returnSegment.ToNode != segment.FromNode || returnSegment.Direction == segment.Direction || !samePhysicalSegment(segment, returnSegment) ||
				!graph.rawPolylineSupportsDirectedSegmentWithin(observations, returnSegment, maximumDegrees, maximumRawOffsetMeters) &&
					!graph.directedSegmentEndpointsRawSupported(observations, returnSegment, maximumRawOffsetMeters) {
				continue
			}
			deadEnd := true
			for _, continuationID := range graph.outgoing[segment.ToNode] {
				if graph.canceled(&checks) {
					return nil, false
				}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for i := 1; i+2 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
			traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
			i--
		}
	}
}

func replaceBoundaryOverlappingRoadReversalWithPath(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, maximumDegrees, maximumRawOffsetMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) < 3 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations ||
			graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
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
	if graph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	pathGraph := graph.onlyContinuityClassWithBoundaries("path", before.SegmentID, after.SegmentID)
	for _, from := range portionBoundaryCandidates(before, true, pathGraph) {
		if graph.canceled(&checks) {
			return networkRoute{}, false
		}
		for _, to := range portionBoundaryCandidates(after, false, pathGraph) {
			if graph.canceled(&checks) {
				return networkRoute{}, false
			}
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
	if graph.err() != nil {
		return compiledGraph{ctx: graph.ctx}
	}
	var checks uint

	boundaries := make(map[string]bool, len(boundaryIDs))
	for _, id := range boundaryIDs {
		if graph.canceled(&checks) {
			return compiledGraph{ctx: graph.ctx}
		}
		boundaries[id] = true
		if segment, exists := graph.segments[id]; exists {
			for oppositeID, opposite := range graph.segments {
				if graph.canceled(&checks) {
					return compiledGraph{ctx: graph.ctx}
				}
				if samePhysicalSegment(segment, opposite) {
					boundaries[oppositeID] = true
				}
			}
		}
	}
	filtered := Graph{MaxContinuityConnectorMeters: graph.maxContinuityConnectorMeters, MaxDrivewayConnectorMeters: graph.maxDrivewayConnectorMeters,
		MaxAccessoryConnectorMeters: graph.maxAccessoryConnectorMeters, MaxParkingAisleConnectorMeters: graph.maxParkingAisleConnectorMeters}
	for id, segment := range graph.segments {
		if graph.canceled(&checks) {
			return compiledGraph{ctx: graph.ctx}
		}
		if (graph.parkingFilter == nil || graph.allowsSegment(id)) && (segment.ContinuityClass == continuityClass || boundaries[id]) {
			filtered.Segments = append(filtered.Segments, segment)
		}
	}
	return compileGraphContext(graph.ctx, filtered)
}

func pathTurnConnectorSupported(portions []TraversedPortion, before, after TraversedPortion, graph compiledGraph, observations []Observation, maximumDegrees, maximumRawOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	supported := false
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
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
		edgeSupported := graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) ||
			graph.directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters)
		supported = supported || edgeSupported
		if portion.ToMeter-portion.FromMeter > 5 && !edgeSupported {
			return false
		}
	}
	return supported
}

func (graph compiledGraph) directedSegmentEndpointsRawSupported(observations []Observation, segment DirectedSegment, maximumRawOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	started := false
	for _, observation := range observations {
		if graph.canceled(&checks) {
			return false
		}
		started = started || math.Max(0, distance(observation.Point, segment.From)-observation.AccuracyMeters) <= maximumRawOffsetMeters
		if started && math.Max(0, distance(observation.Point, segment.To)-observation.AccuracyMeters) <= maximumRawOffsetMeters {
			return true
		}
	}
	return false
}

func extendRawSupportedDriveways(result *Result, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[max(0, traversal.FirstObservation-32):min(len(observations), traversal.LastObservation+33)]
		for portionIndex := 0; portionIndex < len(traversal.Portions); portionIndex++ {
			if graph.canceled(&checks) {
				return
			}
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
				if graph.canceled(&checks) {
					return
				}
				present[graph.segments[added.SegmentID].PhysicalID] = true
			}
			portionIndex += len(addition)
		}
	}
}

func bestRawSupportedOutgoingDriveway(current DirectedSegment, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64, present map[string]bool) ([]TraversedPortion, bool) {
	if graph.err() != nil {
		return nil, false
	}
	var checks uint

	if addition, ok := rawSupportedDrivewayAtNode(current.ToNode, observations, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, present); ok {
		return addition, true
	}
	ids := append([]string(nil), graph.outgoing[current.ToNode]...)
	sort.Strings(ids)
	for _, id := range ids {
		if graph.canceled(&checks) {
			return nil, false
		}
		segment := graph.segments[id]
		if segment.TransitionOnly || segment.ContinuityClass != "road" || segment.LengthMeters > 5 || segment.LogicalPathID != current.LogicalPathID ||
			present[segment.PhysicalID] || !graph.directedSegmentEndpointsRawSupported(observations, segment, maximumRawOffsetMeters) {
			continue
		}
		if driveway, ok := rawSupportedDrivewayAtNode(segment.ToNode, observations, graph, maximumMeters, maximumDegrees, maximumRawOffsetMeters, present); ok {
			return append([]TraversedPortion{graph.portion(id, 0, segment.LengthMeters)}, driveway...), true
		}
	}
	return nil, false
}

func rawSupportedDrivewayAtNode(node string, observations []Observation, graph compiledGraph, maximumMeters, maximumDegrees, maximumRawOffsetMeters float64, present map[string]bool) ([]TraversedPortion, bool) {
	if graph.err() != nil {
		return nil, false
	}
	var checks uint

	ids := append([]string(nil), graph.outgoing[node]...)
	sort.Strings(ids)
	for _, id := range ids {
		if graph.canceled(&checks) {
			return nil, false
		}
		segment := graph.segments[id]
		if !segment.DrivewayConnector || segment.LengthMeters > maximumMeters || present[segment.PhysicalID] ||
			(!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, maximumRawOffsetMeters) &&
				!graph.rawExtendsPastDirectedEndpoint(observations, segment, maximumRawOffsetMeters, 5)) {
			continue
		}
		for _, returnID := range graph.outgoing[segment.ToNode] {
			if graph.canceled(&checks) {
				return nil, false
			}
			returnSegment := graph.segments[returnID]
			if returnSegment.ToNode == segment.FromNode && returnSegment.Direction != segment.Direction && samePhysicalSegment(segment, returnSegment) {
				return []TraversedPortion{graph.portion(id, 0, segment.LengthMeters), graph.portion(returnID, 0, returnSegment.LengthMeters)}, true
			}
		}
	}
	return nil, false
}

func (graph compiledGraph) rawExtendsPastDirectedEndpoint(observations []Observation, segment DirectedSegment, maximumOffsetMeters, minimumBeyondMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	dx, dy := segment.To.X-segment.From.X, segment.To.Y-segment.From.Y
	geometryLength := math.Hypot(dx, dy)
	if geometryLength <= 0 || segment.LengthMeters <= 0 {
		return false
	}
	started := false
	for _, observation := range observations {
		if graph.canceled(&checks) {
			return false
		}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		raw := observations[max(0, traversal.FirstObservation-64):min(len(observations), traversal.LastObservation+65)]
		for i := 0; i+1 < len(traversal.Portions); i++ {
			if graph.canceled(&checks) {
				return
			}
			outbound, inbound := traversal.Portions[i], traversal.Portions[i+1]
			outboundSegment, outboundExists := graph.segments[outbound.SegmentID]
			_, inboundExists := graph.segments[inbound.SegmentID]
			if !outboundExists || !inboundExists || !outboundSegment.DrivewayConnector || !inversePortions(outbound, inbound, epsilon) {
				continue
			}
			ids := append([]string(nil), graph.outgoing[outboundSegment.ToNode]...)
			sort.Strings(ids)
			for _, id := range ids {
				if graph.canceled(&checks) {
					return
				}
				segment := graph.segments[id]
				if !segment.DrivewayConnector || segment.SourceWayID != outboundSegment.SourceWayID || segment.LengthMeters+outboundSegment.LengthMeters > maximumChainMeters || present[segment.PhysicalID] ||
					(!graph.rawPolylineSupportsDirectedSegmentWithin(raw, segment, maximumDegrees, maximumRawOffsetMeters) && !graph.rawSupportsSegmentByEndpointDistance(raw, segment, maximumRawOffsetMeters, 0.7, 1.5) && !graph.directedSegmentEndpointsRawSupported(raw, segment, maximumRawOffsetMeters)) {
					continue
				}
				for _, returnID := range graph.outgoing[segment.ToNode] {
					if graph.canceled(&checks) {
						return
					}
					returnSegment := graph.segments[returnID]
					if !samePhysicalSegment(segment, returnSegment) || returnSegment.ToNode != segment.FromNode ||
						(!graph.rawPolylineSupportsDirectedSegmentWithin(raw, returnSegment, maximumDegrees, maximumRawOffsetMeters) && !graph.rawSupportsSegmentByEndpointDistance(raw, returnSegment, maximumRawOffsetMeters, 0.7, 1.5) && !graph.directedSegmentEndpointsRawSupported(raw, returnSegment, maximumRawOffsetMeters)) {
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
	if graph.err() != nil {
		return false
	}
	var checks uint

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
		if graph.canceled(&checks) {
			return false
		}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	roadGraph := graph.onlyContinuityClass("road")
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 3 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for start := 0; start+2 < len(traversal.Portions); start++ {
			if graph.canceled(&checks) {
				return
			}
			before := traversal.Portions[start]
			if before.ContinuityClass != "road" {
				continue
			}
			excursionLength, hasRoadConnector := 0.0, false
			for end := start + 2; end < len(traversal.Portions) && end <= start+4; end++ {
				if graph.canceled(&checks) {
					return
				}
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
				traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
				end = start + 1
				excursionLength = 0
			}
		}
	}
}

func physicalPortionOccurrences(portions []TraversedPortion, target TraversedPortion, graph compiledGraph) int {
	if graph.err() != nil {
		return 0
	}
	var checks uint

	targetSegment, exists := graph.segments[target.SegmentID]
	if !exists {
		return 0
	}
	count := 0
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return 0
		}
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
		graph.rawPolylineSupportsDirectedSegmentWithin(observations, beforeSegment, 45, 15) &&
		graph.rawPolylineSupportsDirectedSegmentWithin(observations, afterSegment, 45, 15)
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
	return graph.endpointRawSupported(observations, start, maximumMeters, 1) && graph.endpointRawSupported(observations, end, maximumMeters, 1)
}

func pointAlongSegment(segment DirectedSegment, along, length float64) Point {
	if length <= 0 {
		return segment.From
	}
	fraction := math.Max(0, math.Min(1, along/length))
	return Point{segment.From.X + fraction*(segment.To.X-segment.From.X), segment.From.Y + fraction*(segment.To.Y-segment.From.Y)}
}

func bridgeRawSupportedRoadContinuity(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters float64, maximumObservations int, maximumRatio, minimumSavings, maximumContinuityDetour, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	if !resultContainsRoadConnector(result, graph) {
		return
	}
	roadGraph := graph.onlyContinuityClass("road")
	for i := 0; i+1 < len(result.Traversals); i++ {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
			continue
		}
		windowStart, windowEnd := max(0, first.LastObservation-32), min(len(observations), second.FirstObservation+33)
		raw := observations[windowStart:windowEnd]
		bestBefore, bestAfter, bestImprovement := -1, -1, -math.Inf(1)
		best := networkRoute{}
		for beforeIndex := max(0, len(first.Portions)-3); beforeIndex < len(first.Portions); beforeIndex++ {
			if graph.canceled(&checks) {
				return
			}
			before := first.Portions[beforeIndex]
			if before.ContinuityClass != "road" {
				continue
			}
			for afterIndex := 0; afterIndex < min(3, len(second.Portions)); afterIndex++ {
				if graph.canceled(&checks) {
					return
				}
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
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
		i--
	}
}

func resultContainsRoadConnector(result *Result, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return false
		}
		if containsRoadConnector(traversal.Portions, graph) {
			return true
		}
	}
	return false
}

func containsRoadConnector(portions []TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		if graph.segments[portion.SegmentID].RoadConnector {
			return true
		}
	}
	return false
}

func roadRoute(before, after TraversedPortion, graph compiledGraph, maximumMeters, epsilon float64) (networkRoute, bool) {
	if graph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		if graph.canceled(&checks) {
			return networkRoute{}, false
		}
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			if graph.canceled(&checks) {
				return networkRoute{}, false
			}
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
	if graph.err() != nil {
		return false
	}
	var checks uint

	supported := false
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment := graph.segments[portion.SegmentID]
		if portion.ContinuityClass != "road" {
			return false
		}
		if portion.ToMeter-portion.FromMeter <= 5 {
			continue
		}
		if !graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, 45, maximumOffsetMeters) {
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
	if graph.err() != nil {
		return
	}
	var checks uint

	present := make(map[string]bool)
	for _, traversal := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range traversal.Portions {
			if graph.canceled(&checks) {
				return
			}
			present[graph.segments[portion.SegmentID].PhysicalID] = true
		}
	}
	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		windowStart, windowEnd := completionRawWindow(*traversal, result.Splits, len(observations), 16)
		raw := observations[windowStart:windowEnd]
		for portionIndex := 0; portionIndex < len(traversal.Portions); portionIndex++ {
			if graph.canceled(&checks) {
				return
			}
			portion := traversal.Portions[portionIndex]
			segment, exists := graph.segments[portion.SegmentID]
			if !exists || segment.TransitionOnly || portion.ContinuityClass != "path" {
				continue
			}
			changed := false
			selectedLength := portion.ToMeter - portion.FromMeter
			if selectedLength >= 5 && portion.FromMeter > epsilon && portion.FromMeter <= maximumTailMeters &&
				graph.endpointRawSupported(raw, segment.From, 5, 1) {
				portion.FromMeter = 0
				changed = true
			}
			remaining := graph.lengths[portion.SegmentID] - portion.ToMeter
			if selectedLength >= 5 && remaining > epsilon && remaining <= maximumTailMeters &&
				graph.endpointRawSupported(raw, segment.To, 5, 1) {
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
				if graph.canceled(&checks) {
					return
				}
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
		traversal.Portions = graph.mergeConsecutiveTraversedPortions(traversal.Portions, epsilon)
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

func (graph compiledGraph) endpointRawSupported(observations []Observation, endpoint Point, maximumOffsetMeters, minimumApproachMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for i := 1; i < len(observations); i++ {
		if graph.canceled(&checks) {
			return false
		}
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
	if graph.err() != nil {
		return "", false, false
	}
	var checks uint

	ids := make([]string, 0)
	for id, segment := range graph.segments {
		if graph.canceled(&checks) {
			return "", false, false
		}
		if segment.FromNode == node || segment.ToNode == node {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if graph.canceled(&checks) {
			return "", false, false
		}
		segment := graph.segments[id]
		if segment.PhysicalID == currentPhysicalID || present[segment.PhysicalID] || segment.TransitionOnly ||
			segment.ContinuityClass != "path" || graph.lengths[id] > maximumMeters ||
			!graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, drivewayConnectorHeadingDegrees, 5) {
			continue
		}
		return id, segment.ToNode == node, true
	}
	return "", false, false
}

func repairRawSupportedAccessoryShortcuts(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumShortcutMeters, maximumRatio, minimumSavings, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		traversal := &result.Traversals[traversalIndex]
		if len(traversal.Portions) < 3 || traversal.FirstObservation < 0 || traversal.LastObservation >= len(observations) {
			continue
		}
		raw := observations[traversal.FirstObservation : traversal.LastObservation+1]
		for {
			if graph.canceled(&checks) {
				return
			}
			bestStart, bestEnd, bestSavings := -1, -1, 0.0
			best := networkRoute{}
			for start := 0; start+2 < len(traversal.Portions); start++ {
				if graph.canceled(&checks) {
					return
				}
				before := traversal.Portions[start]
				if !accessoryShortcutBoundary(before, graph) {
					continue
				}
				excursionLength, hasRoad := 0.0, false
				for end := start + 2; end < len(traversal.Portions); end++ {
					if graph.canceled(&checks) {
						return
					}
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
			traversal.Portions = graph.mergeConsecutiveTraversedPortions(replacement, epsilon)
		}
	}
}

func accessoryShortcutBoundary(portion TraversedPortion, graph compiledGraph) bool {
	segment, exists := graph.segments[portion.SegmentID]
	return exists && (segment.AccessoryConnector || portion.ContinuityClass == "path" || portion.ContinuityClass == "road")
}

func accessoryOnlyRoute(before, after TraversedPortion, graph compiledGraph, maximumMeters, epsilon float64) (networkRoute, bool) {
	if graph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		if graph.canceled(&checks) {
			return networkRoute{}, false
		}
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			if graph.canceled(&checks) {
				return networkRoute{}, false
			}
			route, ok := graph.routeWithBoundaryClasses(from, to, maximumMeters, epsilon, "path", "path")
			if !ok || route.distance <= epsilon || route.distance >= best.distance || !containsAccessoryConnector(route.portions, graph) {
				continue
			}
			accessoryOnly := true
			for _, portion := range route.portions {
				if graph.canceled(&checks) {
					return networkRoute{}, false
				}
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
	if graph.err() != nil {
		return false
	}
	var checks uint

	supported := false
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment := graph.segments[portion.SegmentID]
		if !segment.AccessoryConnector {
			return false
		}
		edgeSupported := graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, 5)
		supported = supported || edgeSupported
		if portion.ToMeter-portion.FromMeter > accessoryRawSupportLengthMeters && !edgeSupported {
			return false
		}
	}
	return supported
}

func bridgePlausibleAccessoryTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func accessoryBridgeRoute(before, after TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumRouteMeters, epsilon float64) (networkRoute, bool) {
	if graph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		if graph.canceled(&checks) {
			return networkRoute{}, false
		}
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			if graph.canceled(&checks) {
				return networkRoute{}, false
			}
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
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		if graph.segments[portion.SegmentID].AccessoryConnector {
			return true
		}
	}
	return false
}

func onlyBoundaryAndAccessoryPortions(portions []TraversedPortion, before, after TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		segment := graph.segments[portion.SegmentID]
		if !segment.AccessoryConnector && segment.PhysicalSegmentID != before.PhysicalSegmentID && segment.PhysicalSegmentID != after.PhysicalSegmentID {
			return false
		}
	}
	return true
}

func bridgePlausibleDrivewayTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func drivewayBridgeRoute(before, after TraversedPortion, graph compiledGraph, observations []Observation, transitionIndex int, maximumRouteMeters, epsilon float64) (networkRoute, bool) {
	if graph.err() != nil {
		return networkRoute{}, false
	}
	var checks uint

	best := networkRoute{distance: math.Inf(1)}
	for _, from := range portionBoundaryCandidates(before, true, graph) {
		if graph.canceled(&checks) {
			return networkRoute{}, false
		}
		for _, to := range portionBoundaryCandidates(after, false, graph) {
			if graph.canceled(&checks) {
				return networkRoute{}, false
			}
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
	if graph.err() != nil || graph.parkingFilter != nil && !graph.allowsSegment(portion.SegmentID) {
		return nil
	}
	var checks uint

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
		if graph.canceled(&checks) {
			return nil
		}
		if id == portion.SegmentID || !samePhysicalSegment(segment, opposite) || opposite.Direction == segment.Direction ||
			graph.parkingFilter != nil && !graph.allowsSegment(id) {
			continue
		}
		result = append(result, Candidate{SegmentID: id, AlongMeters: graph.lengths[id] - along})
		break
	}
	return result
}

func containsDrivewayConnector(portions []TraversedPortion, graph compiledGraph) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
		if graph.segments[portion.SegmentID].DrivewayConnector {
			return true
		}
	}
	return false
}

func replaceShortExcursionsBetweenSameClass(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+2 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, excursion, third := &result.Traversals[i], &result.Traversals[i+1], result.Traversals[i+2]
		if len(first.Portions) == 0 || len(excursion.Portions) == 0 || len(third.Portions) == 0 ||
			third.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, third.FirstObservation) {
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
		rawDistance := graph.observationPolylineDistance(observations, first.LastObservation, third.FirstObservation)
		if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters || rawDistance <= epsilon ||
			connector.distance < math.Max(0, rawDistance/1.5-20) || connector.distance > rawDistance*1.5+20 ||
			!singleContinuityClass(connector.portions, before.ContinuityClass) {
			i++
			continue
		}
		graph.markTraversalRejected(result, i+1)
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, third.Portions...)
		first.Portions = graph.cancelImmediateBacktracks(first.Portions, epsilon)
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
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		portions := result.Traversals[traversalIndex].Portions
		for i := 0; i+1 < len(portions); i++ {
			if graph.canceled(&checks) {
				return
			}
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
			portions = graph.cancelImmediateBacktracks(replacement, epsilon)
			i = 0
		}
		result.Traversals[traversalIndex].Portions = portions
	}
}

func bridgePlausibleSamePathTraversals(result *Result, observations []Observation, graph compiledGraph, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			second.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
		rawDistance := graph.observationPolylineDistance(observations, first.LastObservation, second.FirstObservation)
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
		first.Portions = graph.cancelImmediateBacktracks(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func replaceBoundaryShortExcursions(result *Result, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) < 2 || len(second.Portions) == 0 {
			i++
			continue
		}
		after := second.Portions[0]
		beforeIndex, excursionLength := len(first.Portions)-1, 0.0
		for beforeIndex >= 0 && first.Portions[beforeIndex].LogicalPathID != after.LogicalPathID {
			if graph.canceled(&checks) {
				return
			}
			excursionLength += first.Portions[beforeIndex].ToMeter - first.Portions[beforeIndex].FromMeter
			beforeIndex--
		}
		if beforeIndex < 0 || beforeIndex == len(first.Portions)-1 || after.LogicalPathID == "" ||
			excursionLength > maximumExcursionMeters || graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
		first.Portions = graph.mergeConsecutiveTraversedPortions(first.Portions, epsilon)
		first.Observations = append(first.Observations, second.Observations...)
		first.LastObservation = second.LastObservation
		first.Cost += second.Cost
		result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
	}
}

func replaceShortPortionExcursions(result *Result, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for traversalIndex := range result.Traversals {
		if graph.canceled(&checks) {
			return
		}
		portions := result.Traversals[traversalIndex].Portions
		for i := 0; i+2 < len(portions); i++ {
			if graph.canceled(&checks) {
				return
			}
			before := portions[i]
			if before.LogicalPathID == "" {
				continue
			}
			j, excursionLength := i+1, 0.0
			for j < len(portions) && portions[j].LogicalPathID != before.LogicalPathID {
				if graph.canceled(&checks) {
					return
				}
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
			portions = graph.cancelImmediateBacktracks(replacement, epsilon)
			i = 0
		}
		result.Traversals[traversalIndex].Portions = portions
	}
}

func replaceShortExcursionsBetweenSamePath(result *Result, observations []Observation, graph compiledGraph, maximumExcursionMeters, maximumRouteMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+2 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, excursion, third := &result.Traversals[i], &result.Traversals[i+1], result.Traversals[i+2]
		if len(first.Portions) == 0 || len(excursion.Portions) == 0 || len(third.Portions) == 0 ||
			third.FirstObservation-first.LastObservation > maximumObservations || graph.hasHardSplit(result.Splits, first.LastObservation, third.FirstObservation) {
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
		rawDistance := graph.observationPolylineDistance(observations, first.LastObservation, third.FirstObservation)
		if !ok || connector.distance <= epsilon || connector.distance > maximumRouteMeters ||
			!singleLogicalPath(connector.portions, before.LogicalPathID) || rawDistance <= epsilon || connector.distance > rawDistance*1.5+20 {
			i++
			continue
		}
		graph.markTraversalRejected(result, i+1)
		first.Portions = append(first.Portions, connector.portions...)
		first.Portions = append(first.Portions, third.Portions...)
		first.Portions = graph.cancelImmediateBacktracks(first.Portions, epsilon)
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

func (graph compiledGraph) observationPolylineDistance(observations []Observation, first, last int) float64 {
	if graph.err() != nil {
		return 0
	}
	var checks uint

	if first < 0 || last >= len(observations) || first >= last {
		return 0
	}
	total := 0.0
	for i := first + 1; i <= last; i++ {
		if graph.canceled(&checks) {
			return 0
		}
		total += distance(observations[i-1].Point, observations[i].Point)
	}
	return total
}

func bridgeShortSamePathTraversals(result *Result, graph compiledGraph, maximumMeters float64, maximumObservations int, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 || second.FirstObservation-first.LastObservation > maximumObservations {
			i++
			continue
		}
		before, after := first.Portions[len(first.Portions)-1], second.Portions[0]
		if before.LogicalPathID == "" || before.LogicalPathID != after.LogicalPathID ||
			graph.hasHardSplit(result.Splits, first.LastObservation, second.FirstObservation) {
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
		first.Portions = graph.cancelImmediateBacktracks(first.Portions, epsilon)
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

func (graph compiledGraph) cancelTraversalBoundaryBacktracks(result *Result, epsilon float64) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := 0; i+1 < len(result.Traversals); {
		if graph.canceled(&checks) {
			return
		}
		first, second := &result.Traversals[i], &result.Traversals[i+1]
		if len(first.Portions) == 0 || len(second.Portions) == 0 ||
			!inversePortions(first.Portions[len(first.Portions)-1], second.Portions[0], epsilon) {
			i++
			continue
		}
		first.Portions = first.Portions[:len(first.Portions)-1]
		second.Portions = second.Portions[1:]
		if len(first.Portions) == 0 && len(second.Portions) == 0 {
			graph.markTraversalRejected(result, i)
			graph.markTraversalRejected(result, i+1)
			result.Traversals = append(result.Traversals[:i], result.Traversals[i+2:]...)
			if i > 0 {
				i--
			}
			continue
		}
		if len(first.Portions) == 0 {
			graph.markTraversalRejected(result, i)
			result.Traversals = append(result.Traversals[:i], result.Traversals[i+1:]...)
			if i > 0 {
				i--
			}
			continue
		}
		if len(second.Portions) == 0 {
			graph.markTraversalRejected(result, i+1)
			result.Traversals = append(result.Traversals[:i+1], result.Traversals[i+2:]...)
			if i > 0 {
				i--
			}
			continue
		}
	}
}

func (graph compiledGraph) markTraversalRejected(result *Result, index int) {
	if graph.err() != nil {
		return
	}
	var checks uint

	for i := range result.Traversals[index].Observations {
		if graph.canceled(&checks) {
			return
		}
		observation := &result.Traversals[index].Observations[i]
		observation.Status = ObservationRejected
		if observation.ObservationIndex >= 0 && observation.ObservationIndex < len(result.Observations) {
			result.Observations[observation.ObservationIndex].Status = ObservationRejected
		}
	}
}

func (graph compiledGraph) hasHardSplit(splits []Split, after, before int) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	for _, split := range splits {
		if graph.canceled(&checks) {
			return false
		}
		if split.BeforeObservation > after && split.BeforeObservation <= before &&
			(split.Reason == SplitTemporal || split.Reason == SplitSpatial) {
			return true
		}
	}
	return false
}

func acceptedCandidates(observation Observation, supplied []Candidate, graph compiledGraph, rules Rules) []Candidate {
	if graph.err() != nil {
		return nil
	}
	var checks uint

	accuracy := effectiveAccuracy(observation.AccuracyMeters, rules)
	radius := math.Min(rules.MaxCandidateDistanceMeters, rules.AccuracyRadiusMultiplier*accuracy)
	seen := make(map[string]bool)
	result := make([]Candidate, 0, len(supplied))
	for _, candidate := range supplied {
		if graph.canceled(&checks) {
			return nil
		}
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
	if graph.err() != nil {
		return
	}
	var checks uint

	chunkStart := start
	for chunkStart < end {
		if graph.canceled(&checks) {
			return
		}
		layers := [][]state{initialStates(chunkStart, observations, candidates, graph, rules)}
		i := chunkStart + 1
		for ; i < end; i++ {
			if graph.canceled(&checks) {
				return
			}
			next := transitionStates(i, observations, candidates[i], layers[len(layers)-1], graph, rules)
			if len(next) == 0 {
				break
			}
			layers = append(layers, next)
		}
		graph.finalizeChunk(chunkStart, i, layers, rules, result)
		if i < end {
			result.Splits = append(result.Splits, Split{i, SplitNetwork})
		}
		chunkStart = i
	}
}

func initialStates(index int, observations []Observation, candidates [][]Candidate, graph compiledGraph, rules Rules) []state {
	if graph.err() != nil {
		return nil
	}
	var checks uint

	states := make([]state, len(candidates[index]))
	for i, candidate := range candidates[index] {
		if graph.canceled(&checks) {
			return nil
		}
		key := candidateKey(candidate)
		states[i] = state{candidate: candidate, cost: emissionCost(index, observations, candidate, graph, rules), pathKey: key, previous: -1}
	}
	return states
}

func transitionStates(index int, observations []Observation, candidates []Candidate, previous []state, graph compiledGraph, rules Rules) []state {
	result := make([]state, 0, len(candidates))
	pairs := 0
	for _, candidate := range candidates {
		best := state{cost: math.Inf(1)}
		for previousIndex, prior := range previous {
			if pairs&127 == 0 && graph.err() != nil {
				return nil
			}
			pairs++
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
	if graph.err() != nil {
		return nil, false
	}
	var checks uint

	hasParkingAisle := false
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return nil, false
		}
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
		if graph.canceled(&checks) {
			return nil, false
		}
		segment := graph.segments[portion.SegmentID]
		if !segment.ParkingAisleConnector {
			continue
		}
		if parkingAisleShadowedByRoad(segment, graph) {
			return nil, false
		}
		fromMeter, toMeter, ok := graph.parkingAisleRawInterval(observations[windowStart:windowEnd], segment, parkingAisleRawSupportOffsetMeters)
		fromMeter, toMeter = math.Max(portion.FromMeter, fromMeter), math.Min(portion.ToMeter, toMeter)
		required := math.Max(5, (portion.ToMeter-portion.FromMeter)*0.5)
		shortEndpointSupport := segment.LengthMeters <= 10 && graph.rawExtendsPastDirectedEndpoint(observations[windowStart:windowEnd], segment, parkingAisleRawSupportOffsetMeters, 5)
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

func (graph compiledGraph) parkingAisleRawInterval(observations []Observation, segment DirectedSegment, maximumOffsetMeters float64) (float64, float64, bool) {
	if graph.err() != nil {
		return 0, 0, false
	}
	var checks uint

	geometryLength := distance(segment.From, segment.To)
	if geometryLength <= 0 || segment.LengthMeters <= 0 {
		return 0, 0, false
	}
	bestFrom, bestTo, runFrom, runTo, previous := 0.0, 0.0, 0.0, 0.0, 0.0
	active := false
	for _, observation := range observations {
		if graph.canceled(&checks) {
			return 0, 0, false
		}
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
	if graph.err() != nil {
		return false
	}
	var checks uint

	for i := 0; i < len(portions); {
		if graph.canceled(&checks) {
			return false
		}
		if !graph.segments[portions[i].SegmentID].AccessoryConnector {
			i++
			continue
		}
		j, length, crossingOnly := i, 0.0, true
		for j < len(portions) && graph.segments[portions[j].SegmentID].AccessoryConnector {
			if graph.canceled(&checks) {
				return false
			}
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
			if graph.canceled(&checks) {
				return false
			}
			segment := graph.segments[portions[k].SegmentID]
			edgeSupported := graph.rawPolylineSupportsDirectedSegment(observations[windowStart:windowEnd], segment, maximumDegrees)
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
	if graph.err() != nil {
		return nil
	}
	var checks uint

	result := make([]TraversedPortion, 0, len(portions))
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return nil
		}
		if !graph.segments[portion.SegmentID].AccessoryConnector {
			result = append(result, portion)
		}
	}
	return result
}

func (graph compiledGraph) finalizeChunk(start, end int, layers [][]state, rules Rules, result *Result) {
	if graph.err() != nil {
		return
	}
	var checks uint

	if len(layers) == 0 || len(layers[len(layers)-1]) == 0 {
		return
	}
	last := layers[len(layers)-1]
	best, second := 0, math.Inf(1)
	for i := range last {
		if graph.canceled(&checks) {
			return
		}
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
		if graph.canceled(&checks) {
			return
		}
		indices[i-1] = layers[i][indices[i]].previous
	}
	portions := make([]TraversedPortion, 0)
	for i := 1; i < len(layers); i++ {
		if graph.canceled(&checks) {
			return
		}
		for _, portion := range layers[i][indices[i]].route.portions {
			if graph.canceled(&checks) {
				return
			}
			length := portion.ToMeter - portion.FromMeter
			if length > rules.PositiveLengthEpsilonMeters {
				portions = append(portions, portion)
			}
		}
	}
	portions = graph.cancelImmediateBacktracks(portions, rules.PositiveLengthEpsilonMeters)
	totalLength := 0.0
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return
		}
		totalLength += portion.ToMeter - portion.FromMeter
	}
	meanCost := last[best].cost / float64(len(layers))
	finalAmbiguous := second-last[best].cost < rules.AmbiguousCostDelta
	ambiguousCount := 0
	for i, layer := range layers {
		if graph.canceled(&checks) {
			return
		}
		chosenCost := layer[indices[i]].cost
		ambiguous := false
		for j := range layer {
			if graph.canceled(&checks) {
				return
			}
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
		if graph.canceled(&checks) {
			return
		}
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
	if graph.err() != nil {
		return false
	}
	var checks uint

	hasDriveway := false
	for _, portion := range portions {
		if graph.canceled(&checks) {
			return false
		}
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
		if graph.canceled(&checks) {
			return false
		}
		if !graph.segments[portions[i].SegmentID].DrivewayConnector {
			i++
			continue
		}
		j := i
		for j < len(portions) && graph.segments[portions[j].SegmentID].DrivewayConnector {
			if graph.canceled(&checks) {
				return false
			}
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
			if graph.canceled(&checks) {
				return false
			}
			segment := graph.segments[portions[k].SegmentID]
			length := portions[k].ToMeter - portions[k].FromMeter
			if k == roadJoin && length <= maxDrivewayRoadJoinMeters {
				continue
			}
			if !graph.rawPolylineSupportsDirectedSegment(observations[windowStart:windowEnd], segment, maximumDegrees) {
				return false
			}
		}
		i = j
	}
	return true
}

func (graph compiledGraph) rawPolylineSupportsDirectedSegment(observations []Observation, segment DirectedSegment, maximumDegrees float64) bool {
	return graph.rawPolylineSupportsDirectedSegmentWithin(observations, segment, maximumDegrees, 10)
}

func (graph compiledGraph) rawPolylineSupportsDirectedSegmentWithin(observations []Observation, segment DirectedSegment, maximumDegrees, maximumOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	geometryLength := distance(segment.From, segment.To)
	if geometryLength <= 0 || segment.LengthMeters <= 0 {
		return false
	}
	startLimit, endLimit := segment.LengthMeters*0.2, segment.LengthMeters*0.8
	segmentHeading := math.Atan2(segment.To.X-segment.From.X, segment.To.Y-segment.From.Y) * 180 / math.Pi
	for i := 0; i+1 < len(observations); i++ {
		if graph.canceled(&checks) {
			return false
		}
		startAlong, startDistance := segmentProjection(observations[i].Point, segment, geometryLength)
		if startDistance > maximumOffsetMeters || startAlong > startLimit {
			continue
		}
		for j := i + 1; j < len(observations); j++ {
			if graph.canceled(&checks) {
				return false
			}
			endAlong, endDistance := segmentProjection(observations[j].Point, segment, geometryLength)
			if endDistance > maximumOffsetMeters || endAlong < endLimit || endAlong-startAlong < segment.LengthMeters*0.6 {
				continue
			}
			rawHeading := math.Atan2(observations[j].Point.X-observations[i].Point.X, observations[j].Point.Y-observations[i].Point.Y) * 180 / math.Pi
			if angularDifference(rawHeading, segmentHeading) <= maximumDegrees &&
				graph.rawSpanFollowsDirectedSegment(observations[i:j+1], segment, geometryLength, maximumOffsetMeters) {
				return true
			}
		}
	}
	return false
}

func (graph compiledGraph) rawSpanFollowsDirectedSegment(observations []Observation, segment DirectedSegment, geometryLength, maximumOffsetMeters float64) bool {
	if graph.err() != nil {
		return false
	}
	var checks uint

	previousAlong := -math.Inf(1)
	for _, observation := range observations {
		if graph.canceled(&checks) {
			return false
		}
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

func endpointRawSupported(observations []Observation, endpoint Point, maximumOffsetMeters, minimumApproachMeters float64) bool {
	return (compiledGraph{}).endpointRawSupported(observations, endpoint, maximumOffsetMeters, minimumApproachMeters)
}

func cancelTraversalBoundaryBacktracks(result *Result, epsilon float64) {
	(compiledGraph{}).cancelTraversalBoundaryBacktracks(result, epsilon)
}
