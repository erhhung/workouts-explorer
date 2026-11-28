package coverage

import (
	"math"
	"testing"
)

func TestShortSharedDeadEndNeedsOrderedMotionAndBetterFitThanBoundaryRoad(t *testing.T) {
	for _, test := range []struct {
		name                       string
		shared, travelled, shallow bool
		want                       bool
	}{
		{"legacy ordinary short street", false, true, false, false},
		{"residential shared turning arm", true, true, false, true},
		{"passed shared entrance", true, false, false, false},
		{"near-parallel shared stub", true, false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := segment("before", "a", "junction", -20, 0, 0, 0, "main", "local")
			before.LengthMeters, before.ContinuityClass, before.PhysicalID = 20, "road", "before"
			after := segment("after", "junction", "b", 0, 0, 20, 0, "main", "local")
			after.LengthMeters, after.ContinuityClass, after.PhysicalID = 20, "road", "after"
			x, y := 0.0, 12.0
			if test.shallow {
				x, y = 12*math.Cos(20*math.Pi/180), 12*math.Sin(20*math.Pi/180)
			}
			out := segment("out", "junction", "end", 0, 0, x, y, "court", "local")
			back := segment("back", "end", "junction", x, y, 0, 0, "court", "local")
			out.LengthMeters, out.ContinuityClass, out.PhysicalID, out.Direction = 12, "road", "court", SegmentForward
			back.LengthMeters, back.ContinuityClass, back.PhysicalID, back.Direction = 12, "road", "court", SegmentReverse
			if test.shared {
				out.DisplayClass, back.DisplayClass = "shared_road", "shared_road"
			}
			graph := compileGraph(Graph{Segments: []DirectedSegment{before, after, out, back}})
			raw := []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{5, 0}}, {Point: Point{10, 0}}, {Point: Point{15, 0}}, {Point: Point{20, 0}}, {Point: Point{15, 0}}, {Point: Point{10, 0}}, {Point: Point{5, 0}}, {Point: Point{0, 0}}, {Point: Point{-15, 0}}}
			if test.travelled {
				raw = []Observation{{Point: Point{-15, 0}}, {Point: Point{0, 0}}, {Point: Point{0, 6}}, {Point: Point{0, 12}}, {Point: Point{0, 6}}, {Point: Point{0, 0}}, {Point: Point{15, 0}}}
			}
			result := Result{Traversals: []DecodedTraversal{{FirstObservation: 0, LastObservation: len(raw) - 1, Portions: []TraversedPortion{graph.portion("before", 0, 20), graph.portion("after", 0, 20)}}}}
			extendRawSupportedRoadDeadEnds(&result, raw, graph, 150, 60, 15, 1e-6)
			if got := len(result.Traversals[0].Portions) == 4; got != test.want {
				t.Fatalf("inferred short shared arm=%t want=%t", got, test.want)
			}
		})
	}
}
