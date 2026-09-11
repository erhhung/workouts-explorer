package evaluator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

const (
	privateAccountID  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	privateWorkoutID  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	privateLabel      = "Private Midnight Ride"
	privateSegment    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	privateCoordinate = "12.345678"
)

type fakeSource struct {
	pointsErr error
}

func (fakeSource) ResolveAccount(context.Context, string) (uuid.UUID, error) {
	return uuid.MustParse(privateAccountID), nil
}

func (fakeSource) Workouts(context.Context, uuid.UUID, int) ([]Workout, error) {
	return []Workout{{ID: uuid.MustParse(privateWorkoutID), TypeKey: "cycling-private", ProviderLabel: privateLabel}}, nil
}

func (s fakeSource) Points(context.Context, uuid.UUID, uuid.UUID) ([]coverage.GeographicObservation, error) {
	if s.pointsErr != nil {
		return nil, s.pointsErr
	}
	points := make([]coverage.GeographicObservation, 130)
	for i := range points {
		points[i] = coverage.GeographicObservation{
			Sequence: i, Longitude: 12.345678 + float64(i)*0.0001, Latitude: 45.123456,
			Time: time.Date(2026, 8, 31, 1, 2, i%60, 0, time.UTC), AccuracyMeters: 3,
		}
	}
	return points, nil
}

type fakeSnapshot struct{ closes int }

func (*fakeSnapshot) Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error) {
	return nil, nil
}

func (*fakeSnapshot) IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error) {
	return nil, nil
}

func (s *fakeSnapshot) Close(context.Context) error {
	s.closes++
	return nil
}

type fakeFactory struct{ snapshot *fakeSnapshot }

func (f fakeFactory) Begin(context.Context) (Snapshot, error) { return f.snapshot, nil }

type fakeWindows struct {
	sizes []int
	err   error
}

func (w *fakeWindows) Evaluate(_ context.Context, _ Topology, observations []coverage.GeographicObservation, _ coverage.Rules, options coverage.OSMEvaluationOptions) (coverage.Result, coverage.OSMEvaluationStats, error) {
	w.sizes = append(w.sizes, len(observations))
	if w.err != nil {
		return coverage.Result{}, coverage.OSMEvaluationStats{}, w.err
	}
	if options.MovementMode != coverage.MovementBicycle || options.Sampling.MinimumDistanceMeters != 0 {
		return coverage.Result{}, coverage.OSMEvaluationStats{}, errors.New("private evaluator detail")
	}
	decoded := make([]coverage.DecodedObservation, len(observations))
	for i := range decoded {
		status := coverage.ObservationMatched
		if i%4 == 1 {
			status = coverage.ObservationAmbiguous
		} else if i%4 == 2 {
			status = coverage.ObservationRejected
		} else if i%4 == 3 {
			status = coverage.ObservationUnmatched
		}
		decoded[i].Status = status
	}
	result := coverage.Result{Observations: decoded, Traversals: []coverage.DecodedTraversal{{
		Portions: []coverage.TraversedPortion{{SegmentID: privateSegment + "/f"}},
	}}}
	return result, coverage.OSMEvaluationStats{CandidateCount: 9, GraphNodeCount: 7, GraphEdgeCount: 8, ExpansionLayers: 3}, nil
}

func TestRunAggregatesOverlappingWindowsWithoutPrivateOutput(t *testing.T) {
	snapshot := &fakeSnapshot{}
	windows := &fakeWindows{}
	runner := Runner{Source: fakeSource{}, Snapshots: fakeFactory{snapshot}, Windows: windows}
	report, err := runner.Run(context.Background(), "private@example.invalid", Config{Limit: 20, MinimumTraversalMeters: 5})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(windows.sizes) != "[128 3]" {
		t.Fatalf("window sizes = %v", windows.sizes)
	}
	if snapshot.closes != 1 {
		t.Fatalf("snapshot closes = %d", snapshot.closes)
	}
	if report.Observations.Original != 130 || report.Observations.Sampled != 130 {
		t.Fatalf("observation counts = %+v", report.Observations)
	}
	statusTotal := report.Observations.Matched + report.Observations.Ambiguous + report.Observations.Unmatched + report.Observations.Rejected
	if statusTotal != 130 {
		t.Fatalf("aggregated statuses = %d", statusTotal)
	}
	if report.Matching.Candidates != (Measure{Total: 18, Max: 9}) || report.Matching.Traversals != 2 || report.Matching.Portions != 2 || report.Matching.UniqueSegments != 1 {
		t.Fatalf("matching counts = %+v", report.Matching)
	}
	if report.Workouts.Total != 1 || report.Workouts.ByOutcome["evaluated"] != 1 || report.Workouts.ByMode["bicycle"] != 1 || report.Workouts.ByOutcomeAndMode["evaluated/bicycle"] != 1 {
		t.Fatalf("workout counts = %+v", report.Workouts)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, private := range []string{privateAccountID, privateWorkoutID, privateLabel, privateSegment, privateCoordinate, "private@example.invalid", "2026-08-31"} {
		if strings.Contains(output, private) {
			t.Fatalf("report contains private fixture value %q: %s", private, output)
		}
	}
}

func TestRunCategorizesDatabaseErrorWithoutLeakingIt(t *testing.T) {
	privateError := errors.New("database failed near 12.345678 for " + privateWorkoutID)
	runner := Runner{Source: fakeSource{pointsErr: privateError}, Snapshots: fakeFactory{&fakeSnapshot{}}, Windows: &fakeWindows{}}
	report, err := runner.Run(context.Background(), "private@example.invalid", Config{Limit: 1, MinimumTraversalMeters: 5})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), privateError.Error()) || report.Errors["application_database"] != 1 || report.Workouts.ByOutcomeAndMode["error/bicycle"] != 1 {
		t.Fatalf("unsafe or incorrect error report: %s", encoded)
	}
}

func TestRunClosesSnapshotAndCategorizesWindowError(t *testing.T) {
	snapshot := &fakeSnapshot{}
	privateError := errors.New("OSM failure at 12.345678 on " + privateSegment)
	runner := Runner{Source: fakeSource{}, Snapshots: fakeFactory{snapshot}, Windows: &fakeWindows{err: privateError}}
	report, err := runner.Run(context.Background(), "private@example.invalid", Config{Limit: 1, MinimumTraversalMeters: 5})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.closes != 1 || report.Errors["osm_database"] != 1 || strings.Contains(string(encoded), privateError.Error()) {
		t.Fatalf("unsafe error or snapshot leak: closes=%d report=%s", snapshot.closes, encoded)
	}
}
