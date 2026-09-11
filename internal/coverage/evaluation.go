package coverage

import "time"

type ExpectedTurn struct {
	FromSegmentID string
	ToSegmentID   string
}

type EvaluationCase struct {
	Name                  string
	Result                Result
	ExpectedSegmentIDs    []string
	CrossStreetSegmentIDs []string
	ExpectedTurns         []ExpectedTurn
	Runtime               time.Duration
}

type Metrics struct {
	SegmentPrecision            float64
	SegmentRecall               float64
	FalseCrossStreetAttribution int
	MissedTurns                 int
	UnmatchedObservations       int
	AmbiguousObservations       int
	RejectedObservations        int
	Runtime                     time.Duration
}

// Evaluate computes set-based segment precision/recall and explicit failure
// counters over labeled synthetic cases.
func Evaluate(cases []EvaluationCase) Metrics {
	metrics := Metrics{}
	truePositive, attributed, expected := 0, 0, 0
	for _, evaluation := range cases {
		actual := attributedSegments(evaluation.Result)
		wanted, cross := stringSet(evaluation.ExpectedSegmentIDs), stringSet(evaluation.CrossStreetSegmentIDs)
		expected += len(wanted)
		attributed += len(actual)
		for id := range actual {
			if wanted[id] {
				truePositive++
			}
			if cross[id] {
				metrics.FalseCrossStreetAttribution++
			}
		}
		for _, turn := range evaluation.ExpectedTurns {
			if !hasTurn(evaluation.Result, turn) {
				metrics.MissedTurns++
			}
		}
		for _, observation := range evaluation.Result.Observations {
			switch observation.Status {
			case ObservationUnmatched:
				metrics.UnmatchedObservations++
			case ObservationAmbiguous:
				metrics.AmbiguousObservations++
			case ObservationRejected:
				metrics.RejectedObservations++
			}
		}
		metrics.Runtime += evaluation.Runtime
	}
	if attributed > 0 {
		metrics.SegmentPrecision = float64(truePositive) / float64(attributed)
	} else if expected == 0 {
		metrics.SegmentPrecision = 1
	}
	if expected > 0 {
		metrics.SegmentRecall = float64(truePositive) / float64(expected)
	} else {
		metrics.SegmentRecall = 1
	}
	return metrics
}

func attributedSegments(result Result) map[string]bool {
	segments := make(map[string]bool)
	for _, traversal := range result.Traversals {
		for _, portion := range traversal.Portions {
			segments[portion.SegmentID] = true
		}
	}
	return segments
}

func hasTurn(result Result, expected ExpectedTurn) bool {
	for _, traversal := range result.Traversals {
		for i := 1; i < len(traversal.Portions); i++ {
			if traversal.Portions[i-1].SegmentID == expected.FromSegmentID && traversal.Portions[i].SegmentID == expected.ToSegmentID {
				return true
			}
		}
	}
	return false
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
