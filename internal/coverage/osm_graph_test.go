package coverage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

type expansionTopology struct {
	edges    []osm.MatcherIncidentEdge
	requests map[uuid.UUID]int
	calls    int
	reverse  bool
}

func expansionID(namespace byte, value int) uuid.UUID {
	var id uuid.UUID
	id[0] = namespace
	binary.BigEndian.PutUint64(id[8:], uint64(value+1))
	return id
}

func expansionRoad(id int, first, second uuid.UUID, length float64) osm.MatcherIncidentEdge {
	return osm.MatcherIncidentEdge{SegmentID: expansionID(2, id), StartGraphNodeID: first,
		EndGraphNodeID: second, LengthMeters: length, Highway: "residential", Tags: json.RawMessage(`{}`),
		RegionID: "fixture:region", GenerationID: 1, SourceWayID: int64(id + 1), SourceWayVersion: 1,
		DerivationVersion: 1, LogicalPathID: testLogicalPath, MotorForwardAllowed: true, MotorReverseAllowed: true}
}

func (s *expansionTopology) Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error) {
	return nil, nil
}

func (s *expansionTopology) IncidentEdges(ctx context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.calls++
	if s.requests == nil {
		s.requests = make(map[uuid.UUID]int)
	}
	var result []osm.MatcherIncidentEdge
	for _, node := range nodes {
		s.requests[node]++
		for _, edge := range s.edges {
			if edge.StartGraphNodeID == node || edge.EndGraphNodeID == node {
				edge.RequestedNodeID = node
				result = append(result, edge)
			}
		}
	}
	if len(result) > limit {
		return nil, &osm.MatcherOverflowError{Operation: "incident edges", Limit: limit}
	}
	if s.reverse {
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}
	return result, nil
}

func competingExpansionPaths(count int) (*expansionTopology, map[uuid.UUID]physicalSegment, uuid.UUID, uuid.UUID) {
	root, otherSeed := expansionID(1, 1000), expansionID(1, 1001)
	seed := expansionRoad(0, root, otherSeed, 1)
	topology := &expansionTopology{edges: []osm.MatcherIncidentEdge{seed}}
	previous := root
	for i := 1; i <= count; i++ {
		// Reverse UUID order deliberately puts the long initial labels ahead of
		// the cheap chain. The old round-based relaxation failed after 64 rounds.
		node := expansionID(1, count-i)
		topology.edges = append(topology.edges, expansionRoad(2*i-1, root, node, 250), expansionRoad(2*i, previous, node, 0.4))
		previous = node
	}
	leaf, outside := expansionID(1, 2000), expansionID(1, 2001)
	topology.edges = append(topology.edges, expansionRoad(2*count+1, previous, leaf, 200), expansionRoad(2*count+2, leaf, outside, 60))
	return topology, map[uuid.UUID]physicalSegment{seed.SegmentID: physicalFromEdge(seed)}, leaf, outside
}

func TestOSMExpansionSettlesKnownShortChainsWithoutSpendingTopologyLayers(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward rows", true: "reversed rows"}[reverse], func(t *testing.T) {
			topology, segments, leaf, outside := competingExpansionPaths(128)
			topology.reverse = reverse
			options := DefaultOSMEvaluationOptions()
			stats := OSMEvaluationStats{}
			if err := expandOSMGraph(context.Background(), topology, segments, experimentalPathPolicy(MovementSharedPublic), options, 300, &stats); err != nil {
				t.Fatal(err)
			}
			if stats.ExpansionLayers != 3 || topology.calls != 3 || stats.GraphNodeCount != 131 || stats.GraphEdgeCount != 259 {
				t.Fatalf("known-edge relaxation consumed discovery budget or missed a newly reachable leaf: stats=%+v calls=%d", stats, topology.calls)
			}
			if topology.requests[leaf] != 1 || topology.requests[outside] != 0 {
				t.Fatal("shorter labels must discover the leaf without crossing the distance bound")
			}
			for node, calls := range topology.requests {
				if calls != 1 {
					t.Fatalf("queried known node %s %d times", node, calls)
				}
			}
		})
	}
}

func TestOSMExpansionAllowsDeepShortChainsWithinActualWorkBudgets(t *testing.T) {
	root, other := expansionID(1, 0), expansionID(1, 1)
	seed := expansionRoad(0, root, other, 1)
	topology := &expansionTopology{edges: []osm.MatcherIncidentEdge{seed}}
	previous := root
	for i := 0; i < 200; i++ {
		node := expansionID(1, i+2)
		topology.edges = append(topology.edges, expansionRoad(i+1, previous, node, 1))
		previous = node
	}
	options := DefaultOSMEvaluationOptions()
	stats := OSMEvaluationStats{}
	err := expandOSMGraph(context.Background(), topology, map[uuid.UUID]physicalSegment{seed.SegmentID: physicalFromEdge(seed)}, experimentalPathPolicy(MovementSharedPublic), options, 300, &stats)
	if err != nil || stats.GraphNodeCount != 202 || stats.GraphEdgeCount != 201 || topology.calls != 201 || topology.calls > options.MaxGraphNodes {
		t.Fatalf("bounded split-road chain did not complete: error=%v calls=%d stats=%+v", err, topology.calls, stats)
	}
	for node, calls := range topology.requests {
		if calls != 1 {
			t.Fatalf("queried chain node %s %d times", node, calls)
		}
	}
}

func TestOSMExpansionRetainsNodeAndIncidentReplyBounds(t *testing.T) {
	for _, nodes := range []bool{true, false} {
		topology, segments, _, _ := competingExpansionPaths(128)
		options := DefaultOSMEvaluationOptions()
		if nodes {
			options.MaxGraphNodes = 16
		} else {
			options.MaxGraphEdges = 16
		}
		err := expandOSMGraph(context.Background(), topology, segments, experimentalPathPolicy(MovementSharedPublic), options, 300, &OSMEvaluationStats{})
		if nodes {
			var limit *OSMEvaluationLimitError
			if !errors.As(err, &limit) || limit.Field != "graph node" || limit.Limit != 16 {
				t.Fatalf("node bound changed: %v", err)
			}
		} else {
			var overflow *osm.MatcherOverflowError
			if !errors.As(err, &overflow) || overflow.Limit != 15 {
				t.Fatalf("remaining incident-response bound changed: %v", err)
			}
		}
	}
}

// The oracle sees the complete fixture and relaxes every edge to a fixed point.
// It is independent of the production priority queue, lazy loading and cache.
func expectedExpansion(edges []osm.MatcherIncidentEdge, seeds map[uuid.UUID]physicalSegment, budget float64) (map[uuid.UUID]bool, int) {
	distances := make(map[uuid.UUID]float64)
	for _, edge := range edges {
		distances[edge.StartGraphNodeID], distances[edge.EndGraphNodeID] = math.Inf(1), math.Inf(1)
	}
	for _, seed := range seeds {
		distances[seed.startNode], distances[seed.endNode] = 0, 0
	}
	for pass := 0; pass < len(distances); pass++ {
		changed := false
		for _, edge := range edges {
			for _, pair := range [][2]uuid.UUID{{edge.StartGraphNodeID, edge.EndGraphNodeID}, {edge.EndGraphNodeID, edge.StartGraphNodeID}} {
				if candidate := distances[pair[0]] + edge.LengthMeters; candidate < distances[pair[1]] {
					distances[pair[1]], changed = candidate, true
				}
			}
		}
		if !changed {
			break
		}
	}
	result := make(map[uuid.UUID]bool)
	nodes := 0
	for _, distance := range distances {
		if distance <= budget {
			nodes++
		}
	}
	for id := range seeds {
		result[id] = true
	}
	for _, edge := range edges {
		if distances[edge.StartGraphNodeID] <= budget || distances[edge.EndGraphNodeID] <= budget {
			result[edge.SegmentID] = true
		}
	}
	return result, nodes
}

func TestOSMExpansionMatchesIndependentShortestDistanceOracle(t *testing.T) {
	random := rand.New(rand.NewSource(928314))
	for fixture := 0; fixture < 40; fixture++ {
		seed := expansionRoad(0, expansionID(1, 0), expansionID(1, 1), 1)
		edges := []osm.MatcherIncidentEdge{seed}
		for i := 1; i <= 80; i++ {
			first, second := random.Intn(30), random.Intn(30)
			edges = append(edges, expansionRoad(i, expansionID(1, first), expansionID(1, second), float64(1+random.Intn(60))))
		}
		for _, reverse := range []bool{false, true} {
			topology := &expansionTopology{edges: edges, reverse: reverse}
			segments := map[uuid.UUID]physicalSegment{seed.SegmentID: physicalFromEdge(seed)}
			want, nodeCount := expectedExpansion(edges, segments, 75)
			stats := OSMEvaluationStats{}
			if err := expandOSMGraph(context.Background(), topology, segments, experimentalPathPolicy(MovementSharedPublic), DefaultOSMEvaluationOptions(), 75, &stats); err != nil {
				t.Fatal(err)
			}
			got := make(map[uuid.UUID]bool)
			for id := range segments {
				got[id] = true
			}
			if !reflect.DeepEqual(got, want) || stats.GraphNodeCount != nodeCount {
				t.Fatalf("fixture %d reversed=%t: nodes=%d want=%d edges=%d want=%d", fixture, reverse, stats.GraphNodeCount, nodeCount, len(got), len(want))
			}
			for node, calls := range topology.requests {
				if calls != 1 {
					t.Fatalf("fixture %d queried %s %d times", fixture, node, calls)
				}
			}
		}
	}
}

type expansionCPUCancelContext struct {
	context.Context
	stats  *OSMEvaluationStats
	cancel context.CancelFunc
}

func (c expansionCPUCancelContext) Err() error {
	if c.stats.ExpansionLayers == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestOSMExpansionCancelsDuringCachedDistanceSettlement(t *testing.T) {
	topology, segments, _, _ := competingExpansionPaths(128)
	stats := OSMEvaluationStats{}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := expansionCPUCancelContext{Context: parent, stats: &stats, cancel: cancel}
	err := expandOSMGraph(ctx, topology, segments, experimentalPathPolicy(MovementSharedPublic), DefaultOSMEvaluationOptions(), 300, &stats)
	if !errors.Is(err, context.Canceled) || topology.calls != 2 {
		t.Fatalf("cached CPU work ignored cancellation: %v calls=%d", err, topology.calls)
	}
}

type expansionReplyTopology struct {
	fakeOSMTopology
	reply osm.MatcherIncidentEdge
}

func (s expansionReplyTopology) IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error) {
	return []osm.MatcherIncidentEdge{s.reply}, nil
}

func TestOSMExpansionRejectsInvalidLengthsAndForeignEndpoints(t *testing.T) {
	root, other := expansionID(1, 0), expansionID(1, 1)
	for _, fixture := range []struct {
		name      string
		configure func(*osm.MatcherIncidentEdge)
		message   string
	}{
		{"negative weight", func(e *osm.MatcherIncidentEdge) { e.LengthMeters = -1 }, "invalid length"},
		{"zero weight", func(e *osm.MatcherIncidentEdge) { e.LengthMeters = 0 }, "invalid length"},
		{"NaN weight", func(e *osm.MatcherIncidentEdge) { e.LengthMeters = math.NaN() }, "invalid length"},
		{"infinite weight", func(e *osm.MatcherIncidentEdge) { e.LengthMeters = math.Inf(1) }, "invalid length"},
		{"unrequested node", func(e *osm.MatcherIncidentEdge) { e.RequestedNodeID = expansionID(1, 8) }, "unrequested"},
		{"disconnected edge", func(e *osm.MatcherIncidentEdge) {
			e.StartGraphNodeID = expansionID(1, 8)
			e.EndGraphNodeID = expansionID(1, 9)
		}, "disconnected"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			seed := expansionRoad(0, root, other, 1)
			reply := expansionRoad(1, root, expansionID(1, 2), 1)
			reply.RequestedNodeID = root
			fixture.configure(&reply)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := expandOSMGraph(ctx, &expansionReplyTopology{reply: reply}, map[uuid.UUID]physicalSegment{seed.SegmentID: physicalFromEdge(seed)}, experimentalPathPolicy(MovementSharedPublic), DefaultOSMEvaluationOptions(), 300, &OSMEvaluationStats{})
			if err == nil || !strings.Contains(err.Error(), fixture.message) || ctx.Err() != nil {
				t.Fatalf("invalid topology did not fail closed: error=%v context=%v", err, ctx.Err())
			}
		})
	}
}
