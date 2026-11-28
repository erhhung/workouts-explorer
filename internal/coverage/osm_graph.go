package coverage

import (
	"bytes"
	"container/heap"
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

type osmExpansionNode struct {
	id       uuid.UUID
	distance float64
}

type osmExpansionQueue []osmExpansionNode

func (q osmExpansionQueue) Len() int { return len(q) }
func (q osmExpansionQueue) Less(i, j int) bool {
	if q[i].distance != q[j].distance {
		return q[i].distance < q[j].distance
	}
	return bytes.Compare(q[i].id[:], q[j].id[:]) < 0
}
func (q osmExpansionQueue) Swap(i, j int)   { q[i], q[j] = q[j], q[i] }
func (q *osmExpansionQueue) Push(value any) { *q = append(*q, value.(osmExpansionNode)) }
func (q *osmExpansionQueue) Pop() any {
	last := len(*q) - 1
	value := (*q)[last]
	*q = (*q)[:last]
	return value
}

type osmExpansionEdge struct {
	other  uuid.UUID
	length float64
}

func expandOSMGraph(ctx context.Context, topology osmTopology, segments map[uuid.UUID]physicalSegment, policy pathPolicy, options OSMEvaluationOptions, maxDistance float64, stats *OSMEvaluationStats) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var checks uint
	g := compiledGraph{ctx: ctx}
	bestDistance := make(map[uuid.UUID]float64)
	adjacency := make(map[uuid.UUID][]osmExpansionEdge)
	queue := &osmExpansionQueue{}
	for _, segment := range segments {
		if g.canceled(&checks) {
			return ctx.Err()
		}
		for _, node := range []uuid.UUID{segment.startNode, segment.endNode} {
			if _, seen := bestDistance[node]; !seen {
				bestDistance[node] = 0
				heap.Push(queue, osmExpansionNode{node, 0})
			}
		}
	}
	layers := 0
	for {
		if len(bestDistance) > options.MaxGraphNodes {
			return &OSMEvaluationLimitError{Field: "graph node", Limit: options.MaxGraphNodes}
		}
		unfetched := make(map[uuid.UUID]struct{})
		// Settle distance improvements through loaded edges before discovering more
		// topology. Re-querying those nodes consumed the layer budget on short chains.
		for queue.Len() > 0 {
			if g.canceled(&checks) {
				return ctx.Err()
			}
			node := heap.Pop(queue).(osmExpansionNode)
			if node.distance != bestDistance[node.id] {
				continue
			}
			edges, loaded := adjacency[node.id]
			if !loaded {
				unfetched[node.id] = struct{}{}
				continue
			}
			for _, edge := range edges {
				if g.canceled(&checks) {
					return ctx.Err()
				}
				distance := node.distance + edge.length
				prior, seen := bestDistance[edge.other]
				if distance > maxDistance || seen && distance >= prior {
					continue
				}
				if !seen && len(bestDistance) >= options.MaxGraphNodes {
					return &OSMEvaluationLimitError{Field: "graph node", Limit: options.MaxGraphNodes}
				}
				bestDistance[edge.other] = distance
				heap.Push(queue, osmExpansionNode{edge.other, distance})
			}
		}
		if len(unfetched) == 0 {
			break
		}
		// Each node is fetched at most once. The node budget therefore also
		// bounds database calls, without a hop limit sensitive to road splitting.
		frontier := make([]uuid.UUID, 0, len(unfetched))
		for node := range unfetched {
			frontier = append(frontier, node)
		}
		sort.Slice(frontier, func(i, j int) bool { return bytes.Compare(frontier[i][:], frontier[j][:]) < 0 })
		for start := 0; start < len(frontier); start += 256 {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(len(frontier), start+256)
			remaining := options.MaxGraphEdges - len(segments)
			if remaining < 1 {
				return &OSMEvaluationLimitError{Field: "graph edge", Limit: options.MaxGraphEdges}
			}
			batch := make(map[uuid.UUID]struct{}, end-start)
			for _, node := range frontier[start:end] {
				batch[node] = struct{}{}
				adjacency[node] = nil
			}
			edges, err := topology.IncidentEdges(ctx, frontier[start:end], remaining)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, edge := range edges {
				if g.canceled(&checks) {
					return ctx.Err()
				}
				if _, requested := batch[edge.RequestedNodeID]; !requested {
					return fmt.Errorf("OSM incident edge returned an unrequested graph node")
				}
				segment := physicalFromEdge(edge)
				segment.policy = policy
				if policy.sharedAccessPolicy() {
					segment.tags, err = options.residentialTags(segment.tags, segment.generation)
					if err != nil {
						return err
					}
				}
				if !policy.transitionEligible(segment.highway, segment.tags) {
					continue
				}
				other := segment.startNode
				if other == edge.RequestedNodeID {
					other = segment.endNode
				} else if segment.endNode != edge.RequestedNodeID {
					return fmt.Errorf("OSM incident edge is disconnected from its requested graph node")
				}
				if !finite(segment.length) || segment.length <= 0 {
					return fmt.Errorf("OSM incident edge has an invalid length")
				}
				segment.transitionOnly = !policy.eligible(segment.highway, segment.tags)
				tags := decodeTags(segment.tags)
				segment.continuityConnector = segment.transitionOnly && pedestrianContinuityConnector(segment.highway, tags)
				segment.drivewayConnector = segment.transitionOnly && pedestrianDriveway(segment.highway, tags)
				segment.accessoryConnector = segment.transitionOnly && pedestrianAccessoryConnector(segment.highway, tags)
				segment.parkingAisleConnector = segment.transitionOnly && pedestrianParkingAisle(segment.highway, tags)
				segments[segment.id] = segment
				if len(segments) > options.MaxGraphEdges {
					return &OSMEvaluationLimitError{Field: "graph edge", Limit: options.MaxGraphEdges}
				}
				adjacency[edge.RequestedNodeID] = append(adjacency[edge.RequestedNodeID], osmExpansionEdge{other, segment.length})
			}
			for _, node := range frontier[start:end] {
				heap.Push(queue, osmExpansionNode{node, bestDistance[node]})
			}
		}
		layers++
		stats.ExpansionLayers = layers
	}
	stats.GraphNodeCount, stats.GraphEdgeCount = len(bestDistance), len(segments)
	return ctx.Err()
}
