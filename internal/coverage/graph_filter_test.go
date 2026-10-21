package coverage

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// The original materialized filter is an independent routing oracle. In
// particular, exclusions use PhysicalID strings, not PhysicalSegmentID UUIDs.
func materializedParkingFilter(graph compiledGraph, excluded []TraversedPortion, boundaryIDs ...string) compiledGraph {
	excludedPhysical := make(map[string]bool)
	for _, portion := range excluded {
		excludedPhysical[graph.segments[portion.SegmentID].PhysicalID] = true
	}
	boundaries := make(map[string]bool)
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
	return compileGraphContext(graph.ctx, filtered)
}

func TestParkingRoutingViewMatchesMaterializedFilter(t *testing.T) {
	var segments []DirectedSegment
	for _, spec := range []struct {
		id, from, to, class string
		transition, parking bool
	}{
		{"before", "a", "b", "road", false, false},
		{"selected", "b", "c", "road", false, false},
		{"a-tie", "b", "c", "road", false, false},
		{"z-tie", "b", "c", "road", false, false},
		{"parking", "c", "d", "", true, true},
		{"after", "d", "e", "road", false, false},
		{"path-boundary", "e", "f", "path", false, false},
		{"accessory", "c", "e", "path", true, false},
		{"driveway", "b", "d", "", true, false},
		{"crossing", "d", "f", "", true, false},
		{"empty-physical", "e", "g", "road", false, false},
	} {
		forward := segment(spec.id, spec.from, spec.to, 0, 0, 10, 0, spec.class, "local")
		forward.ContinuityClass, forward.TransitionOnly, forward.ParkingAisleConnector = spec.class, spec.transition, spec.parking
		forward.PhysicalID, forward.PhysicalSegmentID = spec.id, uuid.NewSHA1(uuid.Nil, []byte(spec.id))
		forward.Direction, forward.LengthMeters = SegmentForward, 10
		forward.RegionID, forward.GenerationID, forward.DerivationVersion = "region", 27, 24
		forward.SourceWayID, forward.SourceWayVersion = 123, 4
		forward.AccessoryConnector, forward.DrivewayConnector, forward.ContinuityConnector = spec.id == "accessory", spec.id == "driveway", spec.id == "crossing"
		if spec.id == "empty-physical" {
			forward.PhysicalID = ""
		}
		if spec.id == "a-tie" {
			// compileGraph computes the routing length but retains raw metadata.
			forward.LengthMeters = 0
		}
		reverse := forward
		reverse.ID += "-r"
		reverse.FromNode, reverse.ToNode, reverse.From, reverse.To, reverse.Direction = forward.ToNode, forward.FromNode, forward.To, forward.From, SegmentReverse
		segments = append(segments, forward, reverse)
	}
	graph := compileGraph(Graph{Segments: segments, MaxContinuityConnectorMeters: 20, MaxDrivewayConnectorMeters: 30,
		MaxAccessoryConnectorMeters: 40, MaxParkingAisleConnectorMeters: 200})
	for _, excludedIDs := range [][]string{nil, {"selected"}, {"selected-r", "empty-physical"}, {"missing"}, {"parking"}, {"path-boundary"}} {
		for _, boundaries := range [][]string{nil, {"before", "after"}, {"path-boundary", "accessory"}, {"selected", "parking"}} {
			var excluded []TraversedPortion
			for _, id := range excludedIDs {
				excluded = append(excluded, TraversedPortion{SegmentID: id})
			}
			view := graph.withoutPhysicalPortions(excluded, boundaries...)
			oracle := materializedParkingFilter(graph, excluded, boundaries...)
			for _, class := range []string{"road", "path", ""} {
				if got, want := view.onlyContinuityClass(class), oracle.onlyContinuityClass(class); !reflect.DeepEqual(got.segments, want.segments) || !reflect.DeepEqual(got.outgoing, want.outgoing) {
					t.Fatal("continuity filter reintroduced an excluded segment")
				}
				if got, want := view.onlyContinuityClassWithBoundaries(class, "path-boundary", "selected"), oracle.onlyContinuityClassWithBoundaries(class, "path-boundary", "selected"); !reflect.DeepEqual(got.segments, want.segments) || !reflect.DeepEqual(got.outgoing, want.outgoing) {
					t.Fatal("boundary continuity filter reintroduced an excluded segment")
				}
				got, gotOK := view.routeOnLogicalPath(Candidate{SegmentID: "before"}, Candidate{SegmentID: "after", AlongMeters: 10}, class, 100, 1e-6)
				want, wantOK := oracle.routeOnLogicalPath(Candidate{SegmentID: "before"}, Candidate{SegmentID: "after", AlongMeters: 10}, class, 100, 1e-6)
				if gotOK != wantOK || !reflect.DeepEqual(got, want) {
					t.Fatal("logical path filter reintroduced an excluded segment")
				}
			}
			for id := range graph.segments {
				_, wantAllowed := oracle.segments[id]
				if view.allowsSegment(id) != wantAllowed {
					t.Fatalf("membership mismatch for %s exclusions=%v boundaries=%v", id, excludedIDs, boundaries)
				}
				if wantAllowed && (!reflect.DeepEqual(view.segments[id], oracle.segments[id]) || view.lengths[id] != oracle.lengths[id] ||
					view.portion(id, 2, 8) != oracle.portion(id, 2, 8)) {
					t.Fatalf("metadata or source fractions changed for %s", id)
				}
				portion := graph.portion(id, 2, 8)
				for _, atEnd := range []bool{false, true} {
					got := portionBoundaryCandidates(portion, atEnd, view)
					want := portionBoundaryCandidates(portion, atEnd, oracle)
					if !wantAllowed {
						want = nil // The old helper rejected these at route entry.
					}
					sort.Slice(got, func(i, j int) bool { return got[i].SegmentID < got[j].SegmentID })
					sort.Slice(want, func(i, j int) bool { return want[i].SegmentID < want[j].SegmentID })
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("boundary candidates changed for %s: got=%+v want=%+v", id, got, want)
					}
				}
				for toID := range graph.segments {
					from, to := Candidate{SegmentID: id, AlongMeters: 3}, Candidate{SegmentID: toID, AlongMeters: 7}
					got, gotOK := view.routeWithBoundaryClasses(from, to, 100, 1e-6, "road", "road")
					want, wantOK := oracle.routeWithBoundaryClasses(from, to, 100, 1e-6, "road", "road")
					if gotOK != wantOK || !reflect.DeepEqual(got, want) {
						t.Fatalf("route %s -> %s changed: got=%+v/%v want=%+v/%v", id, toID, got, gotOK, want, wantOK)
					}
				}
			}
			for _, from := range []string{"a", "b", "c", "d", "e", "f", "g"} {
				for _, to := range []string{"a", "b", "c", "d", "e", "f", "g"} {
					for _, limit := range []float64{0, 10, 25, 100} {
						gotIDs, gotDistance, gotOK := view.shortest(from, to, limit)
						wantIDs, wantDistance, wantOK := oracle.shortest(from, to, limit)
						if gotOK != wantOK || gotDistance != wantDistance || !reflect.DeepEqual(gotIDs, wantIDs) ||
							view.hasShorterOrdinaryRoadPath(from, to, limit) != oracle.hasShorterOrdinaryRoadPath(from, to, limit) {
							t.Fatalf("shortest path or tie changed: %s -> %s limit=%g", from, to, limit)
						}
					}
				}
			}
		}
	}
}

func TestParkingRoutingViewsAreLocalAndImmutable(t *testing.T) {
	graph, _, _ := parkingBoundaryCycleGraph()
	want := materializedParkingFilter(graph, nil)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 20 {
				first := graph.withoutPhysicalPortions([]TraversedPortion{{SegmentID: "first"}}, "parking", "after")
				second := first.withoutPhysicalPortions([]TraversedPortion{{SegmentID: "second"}}, "parking", "after")
				if first.allowsSegment("first") || !first.allowsSegment("second") || second.allowsSegment("first") || second.allowsSegment("second") || !graph.allowsSegment("first") {
					t.Error("an exclusion leaked between views or into the source graph")
				}
				_, _, _ = first.shortest("b", "d", 100)
			}
		})
	}
	workers.Wait()
	if !reflect.DeepEqual(graph.segments, want.segments) || !reflect.DeepEqual(graph.lengths, want.lengths) || !reflect.DeepEqual(graph.outgoing, want.outgoing) {
		t.Fatal("source graph maps changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	graph.ctx = ctx
	view := graph.withoutPhysicalPortions(nil)
	cancel()
	if _, _, ok := view.shortest("a", "d", 100); ok || view.err() != context.Canceled {
		t.Fatal("view lost cooperative cancellation")
	}
}
