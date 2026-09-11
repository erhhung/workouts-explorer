package coverage

import "math"

type RulesVersion string

const ExperimentalRulesV1 RulesVersion = "coverage-experimental-v1"

// Rules centralizes every tuning constant. ExperimentalRules returns the only
// currently evaluated version; it is not a production policy.
type Rules struct {
	Version                     RulesVersion
	MaxCandidateDistanceMeters  float64
	MinEffectiveAccuracyMeters  float64
	MaxEffectiveAccuracyMeters  float64
	AccuracyRadiusMultiplier    float64
	ReliableHeadingAccuracyDeg  float64
	MinHeadingMotionMeters      float64
	HeadingSigmaDegrees         float64
	MaxTemporalGap              float64
	MaxSpatialGapMeters         float64
	MaxNetworkDistanceMeters    float64
	MaxNetworkToObservedRatio   float64
	NetworkDistanceSlackMeters  float64
	TransitionSigmaMeters       float64
	MaxTransitionSpeedMPS       float64
	MinTraversalLengthMeters    float64
	MaxMeanCost                 float64
	AmbiguousCostDelta          float64
	RejectAmbiguousFraction     float64
	PositiveLengthEpsilonMeters float64
}

func ExperimentalRules() Rules {
	return Rules{
		Version:                     ExperimentalRulesV1,
		MaxCandidateDistanceMeters:  25,
		MinEffectiveAccuracyMeters:  3,
		MaxEffectiveAccuracyMeters:  15,
		AccuracyRadiusMultiplier:    2.5,
		ReliableHeadingAccuracyDeg:  20,
		MinHeadingMotionMeters:      2,
		HeadingSigmaDegrees:         30,
		MaxTemporalGap:              30,
		MaxSpatialGapMeters:         200,
		MaxNetworkDistanceMeters:    300,
		MaxNetworkToObservedRatio:   2.5,
		NetworkDistanceSlackMeters:  20,
		TransitionSigmaMeters:       8,
		MaxTransitionSpeedMPS:       15,
		MinTraversalLengthMeters:    5,
		MaxMeanCost:                 12,
		AmbiguousCostDelta:          0.35,
		RejectAmbiguousFraction:     0.75,
		PositiveLengthEpsilonMeters: 1e-6,
	}
}

func (rules Rules) WithMinimumTraversalMeters(value float64) Rules {
	if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
		rules.MinTraversalLengthMeters = value
	}
	return rules
}
