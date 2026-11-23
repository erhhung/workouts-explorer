package routepipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

func parkingLimit() error {
	return &coverage.ParkingRepairLimitError{Field: "parking repair state bytes", MaxStateBytes: 512 * 1024}
}

func recoveryConfig() Config {
	config := fixtureConfig()
	config.RecoverProcessingLimits = true
	return config
}

func observationIndex(point osm.MatcherObservation) int {
	return int(math.Round((point.Longitude*fixtureMetersPerDegree - 10) / 2))
}

func TestProcessingLimitRecoveryRejectsOtherFailureCauses(t *testing.T) {
	limit := parkingLimit()
	for _, test := range []struct {
		err error
		ok  bool
	}{
		{limit, true}, {fmt.Errorf("wrapped: %w", limit), true},
		{errors.Join(limit, &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}), true},
		{errors.Join(limit, errors.New("permission denied")), false},
		{errors.Join(limit, context.Canceled), false}, {errors.Join(limit, context.DeadlineExceeded), false},
		{&net.OpError{Op: "read", Net: "tcp", Err: limit}, false},
		{&coverage.ParkingRepairLimitError{Field: "unknown", MaxStateBytes: 512}, false},
		{&coverage.ParkingRepairLimitError{Field: "parking repair state bytes"}, false},
		{(*coverage.ParkingRepairLimitError)(nil), false},
		{&osm.MatcherOverflowError{Operation: "candidates", Limit: 32}, false},
		{&LimitError{Field: "portions", Limit: 10000}, false},
	} {
		if got := recoverableProcessingLimit(test.err); got != test.ok {
			t.Fatalf("%T: recoverable=%t, want %t", test.err, got, test.ok)
		}
	}
}

func TestParkingLimitRecoversWithSmallerWindowsWithoutDroppingPoints(t *testing.T) {
	snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0})}
	snapshot.fail = func(points []osm.MatcherObservation) error {
		if len(points) > 16 {
			for _, point := range points {
				if observationIndex(point) == 95 {
					return parkingLimit()
				}
			}
		}
		return nil
	}
	result, err := Evaluate(context.Background(), snapshot, fixtureObservations(321), recoveryConfig())
	if err != nil || result.Counts.MatchedPoints+result.Counts.AmbiguousPoints != 321 || result.Counts.UnmatchedPoints != 0 || result.Stats.LimitSkippedPoints != 0 || result.Stats.WindowLimitRetries == 0 {
		t.Fatalf("recovery discarded otherwise matchable points: counts=%+v stats=%+v err=%v", result.Counts, result.Stats, err)
	}
	total := 0.0
	for _, evidence := range result.Evidence {
		total += evidence.Clipped.LengthMeters
	}
	if math.Abs(total-640) > 1e-6 {
		t.Fatalf("recovery changed complete-route coverage: %f", total)
	}
}

func TestIrreducibleLimitsExcludeOnlyOnePointAndDoNotBridgeIt(t *testing.T) {
	for _, test := range []struct {
		name     string
		poison   int
		pairOnly bool
		skip     int
	}{
		{"interior point", 95, false, 95}, {"first point", 0, false, 0},
		{"interior pair", 95, true, 96}, {"first pair", 0, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0})}
			snapshot.fail = func(points []osm.MatcherObservation) error {
				first, second := false, false
				for _, point := range points {
					first = first || observationIndex(point) == test.poison
					second = second || observationIndex(point) == test.poison+1
				}
				if first && (!test.pairOnly || second) {
					return parkingLimit()
				}
				return nil
			}
			result, err := Evaluate(context.Background(), snapshot, fixtureObservations(321), recoveryConfig())
			if err != nil || result.Counts.MatchedPoints+result.Counts.AmbiguousPoints+result.Counts.RejectedPoints != 320 || result.Counts.UnmatchedPoints != 1 || result.Stats.LimitSkippedPoints != 1 || result.Stats.Splits[coverage.SplitProcessingLimit] != 1 {
				t.Fatalf("not a one-point exclusion: counts=%+v stats=%+v err=%v", result.Counts, result.Stats, err)
			}
			for _, evidence := range result.Evidence {
				var line struct{ Coordinates [][]float64 }
				if err := json.Unmarshal(evidence.Clipped.GeoJSON, &line); err != nil {
					t.Fatal(err)
				}
				minX, maxX := math.Inf(1), math.Inf(-1)
				for _, point := range line.Coordinates {
					minX, maxX = math.Min(minX, point[0]*fixtureMetersPerDegree), math.Max(maxX, point[0]*fixtureMetersPerDegree)
				}
				gap := 10 + 2*float64(test.skip)
				if minX < gap-1e-6 && maxX > gap+1e-6 {
					t.Fatalf("Coverage bridged excluded point %d: [%f,%f]", test.skip, minX, maxX)
				}
			}
			if len(snapshot.calls) > 8*321 {
				t.Fatal("recovery did not make bounded forward progress", len(snapshot.calls))
			}
		})
	}
}

func TestEntirelyLimitedRouteIsNotFalselyReportedAsNoEvidence(t *testing.T) {
	limit := parkingLimit()
	snapshot := &windowSnapshot{fail: func([]osm.MatcherObservation) error { return limit }}
	result, err := Evaluate(context.Background(), snapshot, fixtureObservations(20), recoveryConfig())
	if err != limit || !reflect.DeepEqual(result, Result{}) || snapshot.clipCalls != 0 || len(snapshot.calls) > 8*20 {
		t.Fatalf("limited route became success or did not terminate: result=%+v err=%v calls=%d", result, err, len(snapshot.calls))
	}
}

func TestRecoveryDoesNotHidePermanentFailureAfterAcceptedWindows(t *testing.T) {
	failure := errors.Join(parkingLimit(), errors.New("permanent database failure"))
	snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0})}
	snapshot.fail = func(points []osm.MatcherObservation) error {
		if observationIndex(points[0]) > 0 {
			return failure
		}
		return nil
	}
	result, err := Evaluate(context.Background(), snapshot, fixtureObservations(321), recoveryConfig())
	if err != failure || !reflect.DeepEqual(result, Result{}) || snapshot.clipCalls != 0 {
		t.Fatalf("permanent error became partial success: result=%+v err=%v", result, err)
	}
}
