package coverage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestServiceTraversalRequiresDirectedMovementAndNoHardGaps(t *testing.T) {
	point := func(x, y float64, second int) GeographicObservation {
		return GeographicObservation{Longitude: x / 111320, Latitude: y / 111320, Time: time.Unix(int64(second), 0).UTC(), AccuracyMeters: 1.5}
	}
	geometry := func(coords ...[2]float64) json.RawMessage {
		line := make([][]float64, len(coords))
		for i, p := range coords {
			line[i] = []float64{p[0] / 111320, p[1] / 111320}
		}
		data, _ := json.Marshal(struct {
			Type        string      `json:"type"`
			Coordinates [][]float64 `json:"coordinates"`
		}{"LineString", line})
		return data
	}
	line := geometry([2]float64{0, 0}, [2]float64{100, 0})
	travelled := []GeographicObservation{}
	for i := 0; i <= 20; i++ {
		travelled = append(travelled, point(float64(i*5), 1, i))
	}
	crossing := []GeographicObservation{}
	parallel := []GeographicObservation{}
	stationary := []GeographicObservation{}
	for i := 0; i <= 20; i++ {
		crossing = append(crossing, point(50, float64(i*5-50), i))
		parallel = append(parallel, point(float64(i*5), 20, i))
		stationary = append(stationary, point(50+float64(i%3)/10, 0, i))
	}
	gap := append([]GeographicObservation{}, travelled...)
	for i := 10; i < len(gap); i++ {
		gap[i].Time = gap[i].Time.Add(time.Hour)
	}
	detour := []GeographicObservation{point(0, 0, 0), point(30, 0, 10), point(30, 30, 20), point(70, 30, 30), point(70, 0, 40), point(100, 0, 50)}
	for _, test := range []struct {
		name         string
		observations []GeographicObservation
		want         bool
	}{
		{"actually travelled", travelled, true}, {"crossed an entrance", crossing, false},
		{"parallel nearby road", parallel, false}, {"stationary jitter", stationary, false},
		{"hard temporal split", gap, false}, {"unobserved middle shortcut", detour, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ServiceTraversalSupported(context.Background(), line, test.observations, ExperimentalRules(), 8)
			if err != nil || got != test.want {
				t.Fatalf("supported=%t want=%t error=%v", got, test.want, err)
			}
		})
	}
	for _, partial := range []json.RawMessage{geometry([2]float64{0, 0}, [2]float64{30, 0}), geometry([2]float64{70, 0}, [2]float64{100, 0})} {
		if got, err := ServiceTraversalSupported(context.Background(), partial, detour, ExperimentalRules(), 8); err != nil || !got {
			t.Fatalf("actual disjoint piece was discarded: %t %v", got, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ServiceTraversalSupported(cancelled, line, travelled, ExperimentalRules(), 8); !errors.Is(err, context.Canceled) {
		t.Fatal("support check ignored cancellation")
	}
}

func TestResidentialContextOverridesNamesOnlyForNewPolicy(t *testing.T) {
	single := json.RawMessage(`{"name":"Named Private Drive","access":"private","service":"driveway","workouts:residential_context_version":"1","workouts:residential_access":"single_house"}`)
	shared := json.RawMessage(`{"service":"driveway","workouts:residential_context_version":"1","workouts:residential_access":"shared"}`)
	for _, mode := range []MovementMode{MovementFoot, MovementBicycle} {
		legacy := pathPolicyForVersion(mode, ExperimentalPathPolicyV83)
		current := pathPolicyForVersion(mode, ExperimentalPathPolicyV84)
		if !legacy.eligible("service", single) || legacy.eligible("service", shared) {
			t.Fatal("new context changed the queued v83 contract")
		}
		if current.eligible("service", single) || !current.eligible("service", shared) {
			t.Fatal("new policy did not follow residential evidence")
		}
		if DisplayClass(ExperimentalPathPolicyV83, mode, "service", single) != "" || DisplayClass(ExperimentalPathPolicyV84, mode, "service", single) != "connector" || DisplayClass(ExperimentalPathPolicyV84, mode, "service", shared) != "shared_road" {
			t.Fatal("display classification disagrees with matching")
		}
	}
}

func TestResidentialContextRequiresMatchingGenerationCapability(t *testing.T) {
	tags := json.RawMessage(`{"name":"Fixture","service":"driveway","workouts:residential_context_version":"1","workouts:residential_access":"shared"}`)
	options := DefaultOSMEvaluationOptions()
	options.ResidentialContextGenerations = map[int64]bool{2: true}
	legacy, err := options.residentialTags(tags, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pathPolicyForVersion(MovementFoot, ExperimentalPathPolicyV84).eligible("service", legacy) {
		t.Fatal("an old generation supplied untrusted context")
	}
	current, err := options.residentialTags(tags, 2)
	if err != nil || string(current) != string(tags) {
		t.Fatal("trusted public context was modified")
	}
	if !pathPolicyForVersion(MovementFoot, ExperimentalPathPolicyV84).eligible("service", current) {
		t.Fatal("context-bearing generation was ignored")
	}
}
