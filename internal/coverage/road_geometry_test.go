package coverage

import (
	"encoding/json"
	"math"
	"testing"
)

func TestRoadAttributionOffsetUsesWidthThenLaneEstimate(t *testing.T) {
	rules := DefaultRoadGeometryRules()
	tests := []struct {
		name string
		tags string
		want float64
	}{
		{"explicit metric width", `{"width":"12 m","lanes":"8"}`, 9},
		{"explicit imperial width", `{"width":"40 ft","lanes":"8"}`, 9.096},
		{"four motor lanes", `{"lanes":"4"}`, 9},
		{"bike lanes", `{"lanes":"4","cycleway:both":"lane"}`, 10.5},
		{"parking lanes", `{"lanes":"2","parking:lane:both":"parallel"}`, 8.1},
		{"fallback lanes", `{}`, 6},
		{"invalid width falls back", `{"width":"many","lanes":"4"}`, 9},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := roadAttributionOffset(MovementFoot, "residential", json.RawMessage(test.tags), rules)
			if math.Abs(got-test.want) > 0.001 {
				t.Fatalf("offset=%f want=%f", got, test.want)
			}
		})
	}
}

func TestRoadAttributionOffsetIsModeAndClassScopedAndCapped(t *testing.T) {
	rules := DefaultRoadGeometryRules()
	if got := roadAttributionOffset(MovementSharedPublic, "residential", []byte(`{"lanes":"4"}`), rules); got != 0 {
		t.Fatalf("shared-public offset=%f", got)
	}
	if got := roadAttributionOffset(MovementFoot, "path", []byte(`{"lanes":"4"}`), rules); got != 0 {
		t.Fatalf("path offset=%f", got)
	}
	if got := roadAttributionOffset(MovementFoot, "primary", []byte(`{"width":"40"}`), rules); got != 23 {
		t.Fatalf("capped offset=%f", got)
	}
}

func TestAttributionOffsetControlsAcceptanceAndEmissionWithoutChangingRawDistance(t *testing.T) {
	rules := ExperimentalRules()
	graph := compileGraph(Graph{Segments: []DirectedSegment{segment("road", "a", "b", 0, 0, 100, 0, "road", "local")}})
	observation := Observation{AccuracyMeters: 1}
	candidate := Candidate{SegmentID: "road", AlongMeters: 20, DistanceMeters: 12, AttributionOffsetMeters: 9}
	accepted := acceptedCandidates(observation, []Candidate{candidate}, graph, rules)
	if len(accepted) != 1 || accepted[0].DistanceMeters != 12 {
		t.Fatalf("accepted=%+v", accepted)
	}
	if got := emissionCost(0, []Observation{observation}, candidate, graph, rules); math.Abs(got-1) > 1e-9 {
		t.Fatalf("emission cost=%f want=1", got)
	}
	candidate.AttributionOffsetMeters = 0
	if got := acceptedCandidates(observation, []Candidate{candidate}, graph, rules); len(got) != 0 {
		t.Fatalf("unadjusted candidate accepted: %+v", got)
	}
}
