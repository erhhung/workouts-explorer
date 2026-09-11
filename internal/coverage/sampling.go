package coverage

import (
	"math"
	"time"
)

type SamplingVersion string

const ExperimentalSamplingV1 SamplingVersion = "coverage-sampling-experimental-v1"

type SamplingRules struct {
	Version               SamplingVersion
	MinimumDistanceMeters float64
	MaximumInterval       time.Duration
	TurnDegrees           float64
	MinimumTurnLegMeters  float64
}

func ExperimentalSamplingRules() SamplingRules {
	return SamplingRules{ExperimentalSamplingV1, 5, 5 * time.Second, 20, 2}
}

// SampleGeographicObservations deterministically reduces dense traces while
// preserving endpoints, gaps, turns, and material quality changes.
func SampleGeographicObservations(input []GeographicObservation, rules SamplingRules, matcherRules Rules) []GeographicObservation {
	if len(input) <= 2 {
		return append([]GeographicObservation(nil), input...)
	}
	result := make([]GeographicObservation, 0, len(input))
	result = append(result, input[0])
	lastKept := 0
	for i := 1; i < len(input)-1; i++ {
		keep := geographicDistance(input[lastKept], input[i]) >= rules.MinimumDistanceMeters
		if !keep && !input[lastKept].Time.IsZero() && !input[i].Time.IsZero() &&
			input[i].Time.Sub(input[lastKept].Time) >= rules.MaximumInterval {
			keep = true
		}
		if qualityBand(input[i-1].AccuracyMeters) != qualityBand(input[i].AccuracyMeters) ||
			headingQualityBand(input[i-1].HeadingAccuracy) != headingQualityBand(input[i].HeadingAccuracy) {
			keep = true
		}
		if geographicGap(input[i-1], input[i], matcherRules) || geographicGap(input[i], input[i+1], matcherRules) {
			keep = true
		}
		if geographicTurn(input[i-1], input[i], input[i+1], rules) {
			keep = true
		}
		if keep {
			result = append(result, input[i])
			lastKept = i
		}
	}
	result = append(result, input[len(input)-1])
	return result
}

func geographicDistance(a, b GeographicObservation) float64 {
	origin := a
	return distance(Point{}, localPoint(origin, b.Longitude, b.Latitude))
}

func geographicGap(a, b GeographicObservation, rules Rules) bool {
	if !a.Time.IsZero() && !b.Time.IsZero() && (b.Time.Before(a.Time) || b.Time.Sub(a.Time).Seconds() > rules.MaxTemporalGap) {
		return true
	}
	return geographicDistance(a, b) > rules.MaxSpatialGapMeters
}

func geographicTurn(a, b, c GeographicObservation, rules SamplingRules) bool {
	first, second := localPoint(b, a.Longitude, a.Latitude), localPoint(b, c.Longitude, c.Latitude)
	if math.Hypot(first.X, first.Y) < rules.MinimumTurnLegMeters || math.Hypot(second.X, second.Y) < rules.MinimumTurnLegMeters {
		return false
	}
	incoming := math.Atan2(-first.X, -first.Y) * 180 / math.Pi
	outgoing := math.Atan2(second.X, second.Y) * 180 / math.Pi
	return angularDifference(incoming, outgoing) >= rules.TurnDegrees
}

func qualityBand(accuracy float64) int {
	if !finite(accuracy) || accuracy <= 0 {
		return 0
	}
	if accuracy <= 5 {
		return 1
	}
	if accuracy <= 10 {
		return 2
	}
	return 3
}

func headingQualityBand(accuracy *float64) int {
	if accuracy == nil || !finite(*accuracy) || *accuracy < 0 {
		return 0
	}
	if *accuracy <= 20 {
		return 1
	}
	return 2
}
