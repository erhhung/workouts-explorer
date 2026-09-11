package coverage

import (
	"reflect"
	"testing"
	"time"
)

func TestAdaptiveSamplingBoundsDenseStraightTrace(t *testing.T) {
	started := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	input := make([]GeographicObservation, 11)
	for i := range input {
		input[i] = GeographicObservation{Sequence: i, Longitude: metersLongitude(float64(i)), Time: started.Add(time.Duration(i) * time.Second), AccuracyMeters: 3}
	}
	got := SampleGeographicObservations(input, ExperimentalSamplingRules(), ExperimentalRules())
	if len(got) != 3 || got[0].Sequence != 0 || got[1].Sequence != 5 || got[2].Sequence != 10 {
		t.Fatalf("sampled sequences=%v", observationSequences(got))
	}
}

func TestAdaptiveSamplingPreservesTurnQualityChangeAndGap(t *testing.T) {
	started := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	good, poor := 5.0, 40.0
	input := []GeographicObservation{
		{Sequence: 0, Time: started, AccuracyMeters: 3, HeadingAccuracy: &good},
		{Sequence: 1, Longitude: metersLongitude(10), Time: started.Add(time.Second), AccuracyMeters: 3, HeadingAccuracy: &good},
		{Sequence: 2, Longitude: metersLongitude(10), Latitude: metersLatitude(10), Time: started.Add(2 * time.Second), AccuracyMeters: 12, HeadingAccuracy: &poor},
		{Sequence: 3, Longitude: metersLongitude(10), Latitude: metersLatitude(11), Time: started.Add(3 * time.Second), AccuracyMeters: 12, HeadingAccuracy: &poor},
		{Sequence: 4, Longitude: metersLongitude(10), Latitude: metersLatitude(500), Time: started.Add(60 * time.Second), AccuracyMeters: 12, HeadingAccuracy: &poor},
	}
	got := SampleGeographicObservations(input, ExperimentalSamplingRules(), ExperimentalRules())
	want := []int{0, 1, 2, 3, 4}
	if sequences := observationSequences(got); len(sequences) != len(want) {
		t.Fatalf("sampled sequences=%v", sequences)
	} else {
		for i := range want {
			if sequences[i] != want[i] {
				t.Fatalf("sampled sequences=%v", sequences)
			}
		}
	}
}

func TestAdaptiveSamplingIsDeterministic(t *testing.T) {
	input := []GeographicObservation{{Sequence: 1}, {Sequence: 2, Longitude: metersLongitude(6)}, {Sequence: 3, Longitude: metersLongitude(12)}}
	first := SampleGeographicObservations(input, ExperimentalSamplingRules(), ExperimentalRules())
	second := SampleGeographicObservations(input, ExperimentalSamplingRules(), ExperimentalRules())
	if got, want := observationSequences(first), observationSequences(second); !reflect.DeepEqual(got, want) {
		t.Fatalf("sampling changed: first=%v second=%v", got, want)
	}
}

func observationSequences(input []GeographicObservation) []int {
	result := make([]int, len(input))
	for i := range input {
		result[i] = input[i].Sequence
	}
	return result
}

func metersLongitude(meters float64) float64 {
	return meters / earthRadiusMeters * 180 / 3.141592653589793
}
func metersLatitude(meters float64) float64 { return metersLongitude(meters) }
