package coverage

import (
	"container/heap"
	"math"
	"sort"

	"github.com/google/uuid"
)

type compiledGraph struct {
	segments                       map[string]DirectedSegment
	lengths                        map[string]float64
	outgoing                       map[string][]string
	maxContinuityConnectorMeters   float64
	maxDrivewayConnectorMeters     float64
	maxAccessoryConnectorMeters    float64
	maxParkingAisleConnectorMeters float64
}

func compileGraph(graph Graph) compiledGraph {
	result := compiledGraph{
		segments: map[string]DirectedSegment{}, lengths: map[string]float64{}, outgoing: map[string][]string{},
		maxContinuityConnectorMeters:   graph.MaxContinuityConnectorMeters,
		maxDrivewayConnectorMeters:     graph.MaxDrivewayConnectorMeters,
		maxAccessoryConnectorMeters:    graph.MaxAccessoryConnectorMeters,
		maxParkingAisleConnectorMeters: graph.MaxParkingAisleConnectorMeters,
	}
	for _, segment := range graph.Segments {
		if segment.ID == "" || segment.FromNode == "" || segment.ToNode == "" {
			continue
		}
		length := segment.LengthMeters
		if length <= 0 {
			length = distance(segment.From, segment.To)
		}
		if length <= 0 {
			continue
		}
		if _, exists := result.segments[segment.ID]; exists {
			continue
		}
		result.segments[segment.ID] = segment
		result.lengths[segment.ID] = length
		result.outgoing[segment.FromNode] = append(result.outgoing[segment.FromNode], segment.ID)
	}
	for node := range result.outgoing {
		sort.Strings(result.outgoing[node])
	}
	return result
}

type networkRoute struct {
	distance float64
	portions []TraversedPortion
}

func (g compiledGraph) route(from, to Candidate, limit, epsilon float64) (networkRoute, bool) {
	return g.routeWithBoundaryClasses(from, to, limit, epsilon, from.ContinuityClass, to.ContinuityClass)
}

func (g compiledGraph) routeWithBoundaryClasses(from, to Candidate, limit, epsilon float64, beforeClass, afterClass string) (networkRoute, bool) {
	fromLength, fromOK := g.lengths[from.SegmentID]
	toLength, toOK := g.lengths[to.SegmentID]
	if !fromOK || !toOK {
		return networkRoute{}, false
	}
	if from.SegmentID != to.SegmentID && samePhysicalSegment(g.segments[from.SegmentID], g.segments[to.SegmentID]) {
		startOnTarget := from.AlongMeters
		if g.segments[from.SegmentID].Direction != g.segments[to.SegmentID].Direction {
			startOnTarget = toLength - from.AlongMeters
		}
		length := to.AlongMeters - startOnTarget
		if length >= -epsilon {
			length = math.Max(0, length)
			result := networkRoute{distance: length}
			if length > epsilon && !g.segments[to.SegmentID].TransitionOnly {
				result.portions = []TraversedPortion{g.portion(to.SegmentID, startOnTarget, to.AlongMeters)}
			}
			return result, length <= limit
		}
	}
	if from.SegmentID == to.SegmentID && to.AlongMeters+epsilon >= from.AlongMeters {
		length := math.Max(0, to.AlongMeters-from.AlongMeters)
		result := networkRoute{distance: length}
		if length > epsilon && !g.segments[from.SegmentID].TransitionOnly {
			result.portions = []TraversedPortion{g.portion(from.SegmentID, from.AlongMeters, to.AlongMeters)}
		}
		return result, length <= limit
	}

	startSegment, endSegment := g.segments[from.SegmentID], g.segments[to.SegmentID]
	startDistance := math.Max(0, fromLength-from.AlongMeters)
	endDistance := math.Max(0, math.Min(toLength, to.AlongMeters))
	budget := limit - startDistance - endDistance
	if budget < 0 {
		return networkRoute{}, false
	}
	middle, middleDistance, ok := g.shortest(startSegment.ToNode, endSegment.FromNode, budget)
	if !ok {
		return networkRoute{}, false
	}
	portions := make([]TraversedPortion, 0, len(middle)+2)
	if startDistance > epsilon {
		portions = append(portions, g.portion(from.SegmentID, from.AlongMeters, fromLength))
	}
	for _, id := range middle {
		portions = append(portions, g.portion(id, 0, g.lengths[id]))
	}
	if endDistance > epsilon {
		portions = append(portions, g.portion(to.SegmentID, 0, to.AlongMeters))
	}
	if !g.drivewayRunsEligible(portions, epsilon, beforeClass, afterClass) {
		return networkRoute{}, false
	}
	if !g.parkingAisleRunsEligible(portions, epsilon, beforeClass, afterClass) {
		return networkRoute{}, false
	}
	portions = g.emittedPortions(portions, epsilon, beforeClass, afterClass)
	return networkRoute{startDistance + middleDistance + endDistance, cancelImmediateBacktracks(portions, epsilon)}, true
}

func (g compiledGraph) emittedPortions(portions []TraversedPortion, epsilon float64, beforeClass, afterClass string) []TraversedPortion {
	result := make([]TraversedPortion, 0, len(portions))
	for i := 0; i < len(portions); {
		if !g.segments[portions[i].SegmentID].TransitionOnly {
			result = append(result, portions[i])
			i++
			continue
		}
		j, length, crossing, driveway, accessory, parkingAisle := i, 0.0, true, true, true, true
		for j < len(portions) && g.segments[portions[j].SegmentID].TransitionOnly {
			segment := g.segments[portions[j].SegmentID]
			crossing = crossing && segment.ContinuityConnector
			driveway = driveway && segment.DrivewayConnector
			accessory = accessory && segment.AccessoryConnector
			parkingAisle = parkingAisle && segment.ParkingAisleConnector
			length += portions[j].ToMeter - portions[j].FromMeter
			j++
		}
		leftClass, rightClass := transitionBoundaryClasses(portions, i, j, beforeClass, afterClass)
		shortCrossing := i > 0 && j < len(portions) && crossing && g.maxContinuityConnectorMeters > 0 && length <= g.maxContinuityConnectorMeters+epsilon
		qualifiedDriveway := leftClass != "" && rightClass != "" && driveway && g.maxDrivewayConnectorMeters > 0 && length <= g.maxDrivewayConnectorMeters+epsilon &&
			pathRoadClasses(leftClass, rightClass)
		qualifiedAccessory := leftClass != "" && rightClass != "" && accessory && g.maxAccessoryConnectorMeters > 0 &&
			length <= g.maxAccessoryConnectorMeters+epsilon && (leftClass == "path" || rightClass == "path")
		qualifiedParkingAisle := parkingAisleBoundaryClasses(leftClass, rightClass) && parkingAisle &&
			g.maxParkingAisleConnectorMeters > 0 && length <= g.maxParkingAisleConnectorMeters+epsilon
		if shortCrossing || qualifiedDriveway || qualifiedAccessory || qualifiedParkingAisle {
			result = append(result, portions[i:j]...)
		}
		i = j
	}
	return result
}

func (g compiledGraph) parkingAisleRunsEligible(portions []TraversedPortion, epsilon float64, beforeClass, afterClass string) bool {
	for i := 0; i < len(portions); {
		if !g.segments[portions[i].SegmentID].ParkingAisleConnector {
			i++
			continue
		}
		start, end := i, i
		for start > 0 && g.segments[portions[start-1].SegmentID].TransitionOnly {
			start--
		}
		for end < len(portions) && g.segments[portions[end].SegmentID].TransitionOnly {
			end++
		}
		length, onlyParkingAisles := 0.0, true
		for k := start; k < end; k++ {
			onlyParkingAisles = onlyParkingAisles && g.segments[portions[k].SegmentID].ParkingAisleConnector
			length += portions[k].ToMeter - portions[k].FromMeter
		}
		leftClass, rightClass := transitionBoundaryClasses(portions, start, end, beforeClass, afterClass)
		if !onlyParkingAisles || !parkingAisleBoundaryClasses(leftClass, rightClass) ||
			g.maxParkingAisleConnectorMeters <= 0 || length > g.maxParkingAisleConnectorMeters+epsilon {
			return false
		}
		i = end
	}
	return true
}

func parkingAisleBoundaryClasses(left, right string) bool {
	return left == "road" && (right == "road" || right == "path") || right == "road" && left == "path"
}

func (g compiledGraph) drivewayRunsEligible(portions []TraversedPortion, epsilon float64, beforeClass, afterClass string) bool {
	for i := 0; i < len(portions); {
		if !g.segments[portions[i].SegmentID].DrivewayConnector {
			i++
			continue
		}
		j, length := i, 0.0
		for j < len(portions) && g.segments[portions[j].SegmentID].DrivewayConnector {
			length += portions[j].ToMeter - portions[j].FromMeter
			j++
		}
		leftClass, rightClass := transitionBoundaryClasses(portions, i, j, beforeClass, afterClass)
		if leftClass == "" || rightClass == "" || g.maxDrivewayConnectorMeters <= 0 ||
			length > g.maxDrivewayConnectorMeters+epsilon || !pathRoadClasses(leftClass, rightClass) {
			return false
		}
		i = j
	}
	return true
}

func transitionBoundaryClasses(portions []TraversedPortion, start, end int, beforeClass, afterClass string) (string, string) {
	if start > 0 {
		beforeClass = portions[start-1].ContinuityClass
	}
	if end < len(portions) {
		afterClass = portions[end].ContinuityClass
	}
	return beforeClass, afterClass
}

func pathRoadClasses(first, second string) bool {
	return first == "path" && second == "road" || first == "road" && second == "path"
}

func samePhysicalSegment(first, second DirectedSegment) bool {
	if first.PhysicalSegmentID != uuid.Nil && second.PhysicalSegmentID != uuid.Nil {
		return first.PhysicalSegmentID == second.PhysicalSegmentID
	}
	return first.PhysicalID != "" && first.PhysicalID == second.PhysicalID
}

func (g compiledGraph) routeOnLogicalPath(from, to Candidate, logicalPathID string, limit, epsilon float64) (networkRoute, bool) {
	filtered := Graph{Segments: make([]DirectedSegment, 0), MaxContinuityConnectorMeters: g.maxContinuityConnectorMeters,
		MaxDrivewayConnectorMeters: g.maxDrivewayConnectorMeters, MaxAccessoryConnectorMeters: g.maxAccessoryConnectorMeters,
		MaxParkingAisleConnectorMeters: g.maxParkingAisleConnectorMeters}
	for _, segment := range g.segments {
		if segment.LogicalPathID == logicalPathID {
			filtered.Segments = append(filtered.Segments, segment)
		}
	}
	return compileGraph(filtered).route(from, to, limit, epsilon)
}

func (g compiledGraph) routeOnContinuityClass(from, to Candidate, continuityClass string, limit, epsilon float64) (networkRoute, bool) {
	return g.onlyContinuityClass(continuityClass).route(from, to, limit, epsilon)
}

func (g compiledGraph) onlyContinuityClass(continuityClass string) compiledGraph {
	filtered := Graph{Segments: make([]DirectedSegment, 0), MaxContinuityConnectorMeters: g.maxContinuityConnectorMeters,
		MaxDrivewayConnectorMeters: g.maxDrivewayConnectorMeters, MaxAccessoryConnectorMeters: g.maxAccessoryConnectorMeters,
		MaxParkingAisleConnectorMeters: g.maxParkingAisleConnectorMeters}
	for _, segment := range g.segments {
		if segment.ContinuityClass == continuityClass {
			filtered.Segments = append(filtered.Segments, segment)
		}
	}
	return compileGraph(filtered)
}

func cancelImmediateBacktracks(portions []TraversedPortion, epsilon float64) []TraversedPortion {
	result := make([]TraversedPortion, 0, len(portions))
	for _, portion := range portions {
		if len(result) > 0 && mergeConsecutivePortions(&result[len(result)-1], portion, epsilon) {
			continue
		}
		if len(result) > 0 && inversePortions(result[len(result)-1], portion, epsilon) {
			result = result[:len(result)-1]
			continue
		}
		result = append(result, portion)
	}
	return result
}

func mergeConsecutiveTraversedPortions(portions []TraversedPortion, epsilon float64) []TraversedPortion {
	result := make([]TraversedPortion, 0, len(portions))
	for _, portion := range portions {
		if len(result) > 0 && mergeConsecutivePortions(&result[len(result)-1], portion, epsilon) {
			continue
		}
		result = append(result, portion)
	}
	return result
}

func mergeConsecutivePortions(first *TraversedPortion, second TraversedPortion, epsilon float64) bool {
	if first.SegmentID == "" || first.SegmentID != second.SegmentID || first.Direction != second.Direction ||
		math.Abs(first.ToMeter-second.FromMeter) > epsilon {
		return false
	}
	first.ToMeter = second.ToMeter
	first.SourceFromFraction = math.Min(first.SourceFromFraction, second.SourceFromFraction)
	first.SourceToFraction = math.Max(first.SourceToFraction, second.SourceToFraction)
	return true
}

func inversePortions(first, second TraversedPortion, epsilon float64) bool {
	return first.PhysicalSegmentID != uuid.Nil && first.PhysicalSegmentID == second.PhysicalSegmentID &&
		first.Direction != second.Direction &&
		math.Abs(first.SourceFromFraction-second.SourceFromFraction) <= epsilon &&
		math.Abs(first.SourceToFraction-second.SourceToFraction) <= epsilon
}

func (g compiledGraph) portion(id string, fromMeter, toMeter float64) TraversedPortion {
	segment := g.segments[id]
	portion := TraversedPortion{
		SegmentID: id, FromMeter: fromMeter, ToMeter: toMeter,
		RegionID: segment.RegionID, GenerationID: segment.GenerationID,
		DerivationVersion: segment.DerivationVersion, LogicalPathID: segment.LogicalPathID,
		LocalityID: segment.LocalityID, SourceWayID: segment.SourceWayID,
		SourceWayVersion: segment.SourceWayVersion, Direction: segment.Direction,
		ContinuityClass: segment.ContinuityClass,
	}
	portion.PhysicalSegmentID = segment.PhysicalSegmentID
	if portion.PhysicalSegmentID == uuid.Nil && segment.PhysicalID != "" {
		portion.PhysicalSegmentID, _ = uuid.Parse(segment.PhysicalID)
	}
	if portion.PhysicalSegmentID == uuid.Nil {
		return portion
	}
	length := g.lengths[id]
	if segment.Direction == SegmentReverse {
		portion.SourceFromFraction = 1 - toMeter/length
		portion.SourceToFraction = 1 - fromMeter/length
	} else {
		portion.SourceFromFraction = fromMeter / length
		portion.SourceToFraction = toMeter / length
	}
	return portion
}

type queueItem struct {
	node string
	dist float64
	path string
	ids  []string
}
type routeQueue []queueItem

func (q routeQueue) Len() int { return len(q) }
func (q routeQueue) Less(i, j int) bool {
	if q[i].dist != q[j].dist {
		return q[i].dist < q[j].dist
	}
	return q[i].path < q[j].path
}
func (q routeQueue) Swap(i, j int)   { q[i], q[j] = q[j], q[i] }
func (q *routeQueue) Push(value any) { *q = append(*q, value.(queueItem)) }
func (q *routeQueue) Pop() any {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}

func (g compiledGraph) shortest(start, end string, limit float64) ([]string, float64, bool) {
	if start == end {
		return nil, 0, true
	}
	queue := &routeQueue{{node: start}}
	heap.Init(queue)
	bestDistance := map[string]float64{start: 0}
	bestPath := map[string]string{start: ""}
	for queue.Len() > 0 {
		item := heap.Pop(queue).(queueItem)
		if item.dist > limit || item.dist > bestDistance[item.node] || (item.dist == bestDistance[item.node] && item.path != bestPath[item.node]) {
			continue
		}
		if item.node == end {
			return item.ids, item.dist, true
		}
		for _, id := range g.outgoing[item.node] {
			segment := g.segments[id]
			d := item.dist + g.lengths[id]
			path := item.path + "\x00" + id
			old, seen := bestDistance[segment.ToNode]
			if d > limit || (seen && (d > old || (d == old && path >= bestPath[segment.ToNode]))) {
				continue
			}
			bestDistance[segment.ToNode], bestPath[segment.ToNode] = d, path
			ids := append(append([]string(nil), item.ids...), id)
			heap.Push(queue, queueItem{segment.ToNode, d, path, ids})
		}
	}
	return nil, 0, false
}

func (g compiledGraph) hasShorterOrdinaryRoadPath(start, end string, limit float64) bool {
	if start == end || limit <= 0 {
		return false
	}
	queue := &routeQueue{{node: start}}
	heap.Init(queue)
	bestDistance := map[string]float64{start: 0}
	for queue.Len() > 0 {
		item := heap.Pop(queue).(queueItem)
		if item.dist >= limit || item.dist > bestDistance[item.node] {
			continue
		}
		if item.node == end {
			return true
		}
		for _, id := range g.outgoing[item.node] {
			segment := g.segments[id]
			if segment.TransitionOnly || segment.ContinuityClass != "road" {
				continue
			}
			distance := item.dist + g.lengths[id]
			old, seen := bestDistance[segment.ToNode]
			if distance >= limit || seen && distance >= old {
				continue
			}
			bestDistance[segment.ToNode] = distance
			heap.Push(queue, queueItem{node: segment.ToNode, dist: distance})
		}
	}
	return false
}

func distance(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }
