package routepipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

func syntheticLegObservations(lengths ...int) []coverage.GeographicObservation {
	count := 0
	for _, length := range lengths {
		count += length
	}
	points := fixtureObservations(count)
	index := 0
	for leg, length := range lengths {
		for range length {
			points[index].Time = time.Unix(1, 0).UTC().Add(time.Duration(index+leg*60) * time.Second)
			index++
		}
	}
	return points
}

func TestPrimaryWindowsStayWithinLegs(t *testing.T) {
	lengths := [][]int{{0}, {1, 1}, {2, 2}, {31, 31}, {32, 32}, {64, 64}, {128, 128}, {129, 129}, {127, 173}, {128, 172}, {129, 171}}
	for _, lengths := range lengths {
		for _, cap := range []int{128, 64, 32} {
			t.Run(fmt.Sprintf("lengths=%v/cap=%d", lengths, cap), func(t *testing.T) {
				points := syntheticLegObservations(lengths...)
				snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{2000, 0})}
				snapshot.fail = func(query []osm.MatcherObservation) error {
					if len(query) > cap {
						return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
					}
					return nil
				}
				result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
				if err != nil {
					t.Fatal(err)
				}
				var want []fixtureWindow
				var evidenceStarts []int
				accepted, legStart := 0, 0
				for _, length := range lengths {
					legEnd := legStart + length
					for start := legStart; start < legEnd; {
						end, previousSize := 0, -1
						for _, size := range []int{128, 64, 32} {
							end = min(start+size, legEnd)
							if end-start == previousSize {
								continue
							}
							previousSize = end - start
							want = append(want, fixtureWindow{start, end - start})
							if end-start <= cap {
								break
							}
						}
						accepted++
						if end-start >= 4 {
							evidenceStarts = append(evidenceStarts, start)
						}
						if end == legEnd {
							break
						}
						start = end - 1
					}
					legStart = legEnd
				}
				if !reflect.DeepEqual(snapshot.calls, want) || result.Stats.Windows != accepted {
					t.Fatalf("attempts=%v want=%v stats=%+v", snapshot.calls, want, result.Stats)
				}
				counts := result.Counts
				if counts.OriginalPoints != len(points) || counts.SampledPoints != len(points) ||
					counts.MatchedPoints+counts.AmbiguousPoints+counts.UnmatchedPoints+counts.RejectedPoints != len(points) {
					t.Fatalf("dropped/duplicated points: %+v", counts)
				}
				if wantSplits := max(0, len(lengths)-1); result.Stats.Splits[coverage.SplitTemporal] != wantSplits {
					t.Fatalf("boundary splits=%v want=%d (including rejected singleton endpoints)", result.Stats.Splits, wantSplits)
				}
				if len(result.Evidence) != len(evidenceStarts) {
					t.Fatalf("evidence=%d want=%d starts=%v", len(result.Evidence), len(evidenceStarts), evidenceStarts)
				}
				for i, evidence := range result.Evidence {
					if evidence.RouteOrder != evidenceStarts[i] || evidence.FirstTraversedAt != points[evidenceStarts[i]].Time {
						t.Fatalf("relative provenance: %+v", evidence)
					}
				}
			})
		}
	}
}

func TestSpatialGapsNearPrimaryWindowBoundary(t *testing.T) {
	for _, gap := range []int{127, 128, 129} {
		for _, cap := range []int{128, 64, 32} {
			t.Run(fmt.Sprintf("gap=%d/cap=%d", gap, cap), func(t *testing.T) {
				points := fixtureObservations(300)
				for i := gap; i < len(points); i++ {
					points[i].Latitude = 1000 / fixtureMetersPerDegree
				}
				snapshot := &windowSnapshot{fail: func(query []osm.MatcherObservation) error {
					for _, observation := range query {
						if observation.Latitude != query[0].Latitude {
							t.Fatal("primary or failed adaptive attempt crossed a spatial gap")
						}
					}
					if len(query) > cap {
						return &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096}
					}
					return nil
				}}
				result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
				if err != nil || result.Counts.SampledPoints != len(points) || result.Counts.UnmatchedPoints != len(points) {
					t.Fatalf("counts=%+v err=%v", result.Counts, err)
				}
				found := false
				for _, call := range snapshot.calls {
					found = found || call.start == gap
				}
				if !found {
					t.Fatal("first point of spatial leg dropped or overlapped")
				}
			})
		}
	}
}

func TestLegBoundaryCandidatePrecedence(t *testing.T) {
	for _, kind := range []string{"temporal", "spatial", "both"} {
		for _, candidates := range []string{"both", "before", "after", "neither"} {
			t.Run(kind+"/"+candidates, func(t *testing.T) {
				points := syntheticLegObservations(1, 1)
				if kind != "temporal" {
					points[1].Longitude = 500 / fixtureMetersPerDegree
				} else {
					points[1].Longitude = 100 / fixtureMetersPerDegree
				}
				if kind == "spatial" {
					points[1].Time = points[0].Time.Add(time.Second)
				}
				snapshot := &windowSnapshot{}
				for i, point := range points {
					if candidates == "neither" || candidates == "before" && i == 1 || candidates == "after" && i == 0 {
						continue
					}
					x := point.Longitude * fixtureMetersPerDegree
					snapshot.roads = append(snapshot.roads, fixtureRoad{a: fixturePoint{x, -10}, b: fixturePoint{x, 10},
						id: fixtureID(fmt.Sprint("boundary road", i)), path: fixtureID(fmt.Sprint("boundary path", i)),
						from: fixtureID(fmt.Sprint("boundary start", i)), to: fixtureID(fmt.Sprint("boundary end", i))})
				}
				result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
				if err != nil {
					t.Fatal(err)
				}
				wantRejected := 1
				if candidates == "both" {
					wantRejected = 2
				} else if candidates == "neither" {
					wantRejected = 0
				}
				want := map[coverage.SplitReason]int{}
				if wantRejected < 2 {
					want[coverage.SplitNoCandidate] = 2 - wantRejected
				} else if kind == "spatial" {
					want[coverage.SplitSpatial] = 1
				} else {
					want[coverage.SplitTemporal] = 1
				}
				if !reflect.DeepEqual(result.Stats.Splits, want) || result.Counts.RejectedPoints != wantRejected || result.Counts.UnmatchedPoints != 2-wantRejected {
					t.Fatalf("candidate precedence: splits=%v want=%v counts=%+v", result.Stats.Splits, want, result.Counts)
				}
			})
		}
	}
}

func TestSevenSyntheticLegsKeepEverySample(t *testing.T) {
	points := syntheticLegObservations(128, 129, 32, 64, 1000, 2000, 744)
	snapshot := &windowSnapshot{fail: func(query []osm.MatcherObservation) error {
		for i := 1; i < len(query); i++ {
			// Longitude encodes the absolute index; timestamps remain on input.
			a := int(math.Round((query[i-1].Longitude*fixtureMetersPerDegree - 10) / 2))
			b := int(math.Round((query[i].Longitude*fixtureMetersPerDegree - 10) / 2))
			if b != a+1 || coverage.GeographicGapReason(points[a], points[b], coverage.ExperimentalRules()) != "" {
				t.Fatalf("candidate attempt crosses legs: %d..%d", a, b)
			}
		}
		if len(query) > 32 {
			return &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096}
		}
		return nil
	}}
	result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
	if err != nil || result.Counts.OriginalPoints != len(points) || result.Counts.SampledPoints != len(points) ||
		result.Counts.MatchedPoints+result.Counts.AmbiguousPoints+result.Counts.RejectedPoints+result.Counts.UnmatchedPoints != len(points) {
		t.Fatalf("counts=%+v err=%v", result.Counts, err)
	}
}

func TestLaterLegFailureIsAtomic(t *testing.T) {
	for _, failure := range []string{"exhausted", "permanent", "cancel", "deadline"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{2000, 0}), ctx: ctx}
			limit := &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
			var want error = limit
			if failure == "permanent" {
				want = errors.New("synthetic later-leg failure")
			} else if failure == "cancel" {
				want = context.Canceled
			} else if failure == "deadline" {
				want = context.DeadlineExceeded
			}
			snapshot.fail = func(query []osm.MatcherObservation) error {
				if query[0].Longitude*fixtureMetersPerDegree < 266 {
					return nil
				}
				if failure == "cancel" {
					cancel()
					return limit
				}
				return want
			}
			failed, err := Evaluate(ctx, snapshot, syntheticLegObservations(128, 129), fixtureConfig())
			wantCalls := 2
			if failure == "exhausted" {
				wantCalls = 4
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(failed, Result{}) || len(snapshot.calls) != wantCalls || snapshot.clipCalls != 0 {
				t.Fatalf("partial/retried failure: result=%+v err=%v calls=%v clips=%d", failed, err, snapshot.calls, snapshot.clipCalls)
			}
			prepared, err := Prepare(context.Background(), snapshot, failed, time.Time{})
			if err != nil || prepared != nil || snapshot.clipCalls+snapshot.copyCalls != 0 {
				t.Fatalf("partial preparation: prepared=%v err=%v", prepared, err)
			}
		})
	}
}
