package routepipeline

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

func fixturePortion(road fixtureRoad, from, to float64, direction coverage.SegmentDirection, window, order int, points []coverage.GeographicObservation) matchedPortion {
	length := math.Hypot(road.b.x-road.a.x, road.b.y-road.a.y)
	start, end := from*length, to*length
	if direction == coverage.SegmentReverse {
		start, end = (1-to)*length, (1-from)*length
	}
	return matchedPortion{portion: coverage.TraversedPortion{PhysicalSegmentID: road.id, RegionID: "fixture:adaptive", GenerationID: 1,
		LogicalPathID: road.path.String(), ContinuityClass: "road", Direction: direction, SourceFromFraction: from, SourceToFraction: to,
		FromMeter: start, ToMeter: end}, window: window, routeOrder: order, firstTraversedAt: points[order].Time, class: "matched"}
}

func turnFixture(reverse bool, seam int) (*windowSnapshot, []coverage.GeographicObservation, []matchedPortion) {
	roads := fixtureRoads(fixturePoint{-200, 0}, fixturePoint{0, 0}, fixturePoint{0, -200})
	points := fixtureObservations(256)
	for i := range points {
		d := math.Max(-32, math.Min(30, float64(i-seam)*2))
		p := fixturePoint{d, 0}
		if d > 0 {
			p = fixturePoint{0, -d}
		}
		if reverse {
			p = fixturePoint{-p.y, p.x}
			p.x = -p.x
		}
		points[i].Longitude, points[i].Latitude = p.x/fixtureMetersPerDegree, p.y/fixtureMetersPerDegree
	}
	var portions []matchedPortion
	if !reverse {
		portions = []matchedPortion{fixturePortion(roads[0], .85, .96, coverage.SegmentForward, 0, seam-15, points), fixturePortion(roads[1], .04, .15, coverage.SegmentForward, 1, seam+4, points)}
	} else {
		portions = []matchedPortion{fixturePortion(roads[1], .04, .15, coverage.SegmentReverse, 0, seam-15, points), fixturePortion(roads[0], .85, .96, coverage.SegmentReverse, 1, seam+4, points)}
	}
	return &windowSnapshot{roads: roads}, points, portions
}

func tangentFixture(reverse bool, seam int) (*windowSnapshot, []coverage.GeographicObservation, []matchedPortion) {
	roads := fixtureRoads(fixturePoint{-200, 0}, fixturePoint{0, 0}, fixturePoint{20, 0}, fixturePoint{40, 0}, fixturePoint{240, 0})
	branchX, branchNode := 0.0, roads[0].to
	if reverse {
		branchX, branchNode = 40, roads[3].from
	}
	branch := fixtureRoad{a: fixturePoint{branchX, 0}, b: fixturePoint{branchX, 30}, id: fixtureID("tangent"), path: fixtureID("tangent path"), from: branchNode, to: fixtureID("tangent tip")}
	roads = append(roads, branch)
	points := fixtureObservations(256)
	for i := range points {
		x := float64(i-(seam-8)) * 5
		if reverse {
			x = 40 - x
		}
		x = math.Max(-50, math.Min(90, x))
		points[i].Longitude, points[i].Latitude = x/fixtureMetersPerDegree, 0
	}
	var before, after matchedPortion
	if !reverse {
		before = fixturePortion(roads[0], .85, 1, coverage.SegmentForward, 0, seam-14, points)
		after = fixturePortion(roads[3], 0, .15, coverage.SegmentForward, 1, seam+1, points)
	} else {
		before = fixturePortion(roads[3], 0, .15, coverage.SegmentReverse, 0, seam-14, points)
		after = fixturePortion(roads[0], .85, 1, coverage.SegmentReverse, 1, seam+1, points)
	}
	selected := fixturePortion(branch, 0, 1, coverage.SegmentForward, 0, seam-8, points)
	return &windowSnapshot{roads: roads}, points, []matchedPortion{before, selected, after}
}

func repairOptions() coverage.OSMEvaluationOptions {
	options := coverage.DefaultOSMEvaluationOptions()
	options.Sampling.MinimumDistanceMeters = 0
	options.MaxCandidatesPerObservation = 16
	options.MovementMode = coverage.MovementFoot
	return options
}

func TestAdaptiveSeamBoundsAndDuplicateContexts(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		initial, left, total int
		want                 []fixtureWindow
	}{
		{"centered", 128, 1, 2, []fixtureWindow{{63, 128}, {95, 64}, {111, 32}}},
		{"left biased", 192, 2, 3, []fixtureWindow{{0, 192}, {42, 128}, {85, 64}, {106, 32}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bounds := seamWindowBounds(0, 256, 127, tc.left, tc.total)
			index := 0
			for _, size := range []int{tc.initial, 128, 64, 32} {
				if tc.initial == 128 && index == 1 && size == 128 {
					continue
				}
				start, end := bounds(size)
				if got := (fixtureWindow{start, end - start}); got != tc.want[index] {
					t.Fatalf("bounds=%+v want=%+v", got, tc.want[index])
				}
				index++
			}
			for _, count := range []int{1, 20, 32} {
				snapshot := &windowSnapshot{fail: func([]osm.MatcherObservation) error {
					return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
				}}
				_, _, _, err := matchAdaptiveWindow(context.Background(), snapshot, fixtureObservations(count), coverage.ExperimentalRules(), repairOptions(), tc.initial, seamWindowBounds(0, count, count/2, tc.left, tc.total))
				if err == nil || len(snapshot.calls) != 1 {
					t.Fatalf("repeated actual context: count=%d calls=%v err=%v", count, snapshot.calls, err)
				}
			}
		})
	}
}

func TestSupplementalAdaptiveRepairs(t *testing.T) {
	for _, kind := range []string{"turn", "tangent"} {
		for _, reverse := range []bool{false, true} {
			for _, seam := range []int{31, 63, 127} {
				for _, cap := range []int{192, 128, 64, 32} {
					t.Run(fmt.Sprintf("%s/reverse=%v/seam=%d/cap=%d", kind, reverse, seam, cap), func(t *testing.T) {
						factory := turnFixture
						if kind == "tangent" {
							factory = tangentFixture
						}
						snapshot, points, portions := factory(reverse, seam)
						original := append([]matchedPortion(nil), portions...)
						snapshot.fail = func(points []osm.MatcherObservation) error {
							if len(points) > cap {
								return &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096}
							}
							return nil
						}
						repair := completeCrossWindowConnectedRoadTurns
						initial := 128
						if kind == "tangent" {
							repair = replaceCrossWindowRoadTangents
							initial = 192
						}
						got, stats, err := repair(context.Background(), snapshot, points, []matchingWindow{{0, 0, len(points)}, {seam, 0, len(points)}}, portions, coverage.ExperimentalRules(), repairOptions())
						if err != nil {
							t.Fatal(err)
						}
						var sizes []int
						for _, call := range snapshot.calls {
							sizes = append(sizes, call.size)
						}
						want := []int{initial}
						if initial > cap {
							if initial == 192 {
								want = append(want, 128)
							}
							if cap < 128 {
								want = append(want, 64)
							}
							if cap < 64 {
								want = append(want, 32)
							}
						}
						if !reflect.DeepEqual(sizes, want) || stats.windows != 1 {
							t.Fatalf("attempts=%v want=%v stats=%+v", sizes, want, stats)
						}
						if kind == "turn" {
							if len(got) != 2 || reflect.DeepEqual(got, original) {
								t.Fatalf("turn not completed: %+v", got)
							}
							for i := range got {
								if got[i].portion.ToMeter-got[i].portion.FromMeter <= original[i].portion.ToMeter-original[i].portion.FromMeter || got[i].routeOrder != original[i].routeOrder || got[i].firstTraversedAt != original[i].firstTraversedAt {
									t.Fatalf("turn extent/provenance: %+v", got)
								}
							}
						} else {
							if len(got) != 4 {
								t.Fatalf("tangent not replaced: %+v", got)
							}
							middle := []int{1, 2}
							if reverse {
								middle = []int{2, 1}
							}
							for i, road := range middle {
								if got[i+1].portion.PhysicalSegmentID != snapshot.roads[road].id || got[i+1].routeOrder != original[1].routeOrder || got[i+1].firstTraversedAt != original[1].firstTraversedAt || got[i+1].window != 1 {
									t.Fatalf("wrong replacement/provenance: %+v", got)
								}
							}
						}
						if snapshot.clipCalls != 0 {
							t.Fatal("supplemental matcher clipped prematurely")
						}
					})
				}
			}
		}
	}
}

func TestAdaptiveSeamRouteEvidence(t *testing.T) {
	for _, kind := range []string{"turn", "tangent"} {
		for _, reverse := range []bool{false, true} {
			for _, cap := range []int{128, 64, 32} {
				t.Run(fmt.Sprintf("%s/reverse=%v/cap=%d", kind, reverse, cap), func(t *testing.T) {
					factory := turnFixture
					wantMeters := 62.0
					if kind == "tangent" {
						factory = tangentFixture
						wantMeters = 140
					}
					snapshot, points, _ := factory(reverse, cap-1)
					snapshot.fail = func(points []osm.MatcherObservation) error {
						if len(points) > cap {
							return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
						}
						return nil
					}
					result, err := Evaluate(context.Background(), snapshot, points, fixtureConfig())
					if err != nil {
						t.Fatal(err)
					}
					meters := 0.0
					for _, evidence := range result.Evidence {
						if kind == "tangent" && evidence.Portion.PhysicalSegmentID == fixtureID("tangent") {
							t.Fatal("unsupported tangent gained coverage")
						}
						if evidence.RouteOrder < 0 || evidence.RouteOrder >= len(points) || evidence.FirstTraversedAt != points[evidence.RouteOrder].Time {
							t.Fatalf("invalid absolute provenance: %+v", evidence)
						}
						meters += evidence.Clipped.LengthMeters
					}
					counts := result.Counts
					if counts.MatchedPoints+counts.AmbiguousPoints+counts.RejectedPoints+counts.UnmatchedPoints != len(points) {
						t.Fatalf("overlap counted twice: %+v", counts)
					}
					if math.Abs(meters-wantMeters) > 1e-6 {
						t.Fatalf("seam coverage=%f want=%f evidence=%+v", meters, wantMeters, result.Evidence)
					}
				})
			}
		}
	}
}

func TestSupplementalAdaptiveNoFalseContinuity(t *testing.T) {
	for _, kind := range []string{"turn", "tangent"} {
		for _, reverse := range []bool{false, true} {
			for _, failure := range []string{"temporal", "spatial", "missing", "disconnected", "anchors", "exhausted"} {
				t.Run(fmt.Sprintf("%s/reverse=%v/%s", kind, reverse, failure), func(t *testing.T) {
					const seam = 128
					factory := turnFixture
					if kind == "tangent" {
						factory = tangentFixture
					}
					snapshot, points, portions := factory(reverse, seam)
					original := append([]matchedPortion(nil), portions...)
					gap := seam
					if kind == "tangent" {
						gap = seam - 4
					}
					switch failure {
					case "temporal":
						for i := gap; i < len(points); i++ {
							points[i].Time = points[i].Time.Add(time.Hour)
						}
					case "spatial":
						points[gap].Latitude += .01
					case "missing":
						for i := gap - 1; i <= gap+1; i++ {
							points[i].Latitude += .001
						}
					case "disconnected":
						index := 1
						if kind == "tangent" {
							index = 2
						}
						snapshot.roads[index].from = fixtureID("disconnected")
					case "anchors":
						for i := range points {
							points[i].Longitude, points[i].Latitude = 0, 0
						}
					}
					snapshot.fail = func(points []osm.MatcherObservation) error {
						if len(points) > 32 || failure == "exhausted" {
							return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
						}
						return nil
					}
					repair := completeCrossWindowConnectedRoadTurns
					if kind == "tangent" {
						repair = replaceCrossWindowRoadTangents
					}
					windows := []matchingWindow{{0, 0, len(points)}, {seam, 0, len(points)}}
					if failure == "temporal" || failure == "spatial" {
						windows[0].legEnd = gap
						windows[1].legStart = gap
					}
					got, stats, err := repair(context.Background(), snapshot, points, windows, portions, coverage.ExperimentalRules(), repairOptions())
					if failure == "exhausted" {
						if err == nil || got != nil || stats.windows != 0 {
							t.Fatalf("exhaustion swallowed: got=%+v stats=%+v err=%v", got, stats, err)
						}
						return
					}
					if err != nil || !reflect.DeepEqual(got, original) {
						t.Fatalf("false continuity: got=%+v want=%+v stats=%+v err=%v", got, original, stats, err)
					}
					if (failure == "temporal" || failure == "spatial") && (len(snapshot.calls) != 0 || stats.windows != 0) {
						t.Fatalf("cross-leg supplemental query: calls=%v stats=%+v", snapshot.calls, stats)
					}
				})
			}
		}
	}
}

func TestSupplementalFallbacksStayInsideSameLeg(t *testing.T) {
	for _, kind := range []string{"turn", "tangent"} {
		for _, seam := range []int{63, 193} {
			t.Run(fmt.Sprintf("%s/seam=%d", kind, seam), func(t *testing.T) {
				factory := turnFixture
				repair := completeCrossWindowConnectedRoadTurns
				if kind == "tangent" {
					factory, repair = tangentFixture, replaceCrossWindowRoadTangents
				}
				snapshot, points, portions := factory(false, seam)
				const legStart, legEnd = 40, 216
				indexes := make(map[[2]float64]int, len(points))
				for i := range points {
					// Distinguish stationary synthetic samples without changing support.
					points[i].Latitude += float64(i) * 1e-9 / fixtureMetersPerDegree
					indexes[[2]float64{points[i].Longitude, points[i].Latitude}] = i
				}
				for i := range portions {
					portions[i].window++
				}
				original := append([]matchedPortion(nil), portions...)
				var attempts []fixtureWindow
				snapshot.fail = func(query []osm.MatcherObservation) error {
					start, ok := indexes[[2]float64{query[0].Longitude, query[0].Latitude}]
					if !ok || start < legStart || start+len(query) > legEnd {
						t.Fatalf("supplemental escaped leg: start=%d size=%d", start, len(query))
					}
					for i, observation := range query {
						if index := indexes[[2]float64{observation.Longitude, observation.Latitude}]; index != start+i {
							t.Fatalf("noncontiguous supplemental context: start=%d offset=%d index=%d", start, i, index)
						}
					}
					attempts = append(attempts, fixtureWindow{start, len(query)})
					if len(query) > 32 {
						return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
					}
					return nil
				}
				windows := []matchingWindow{{0, 0, legStart}, {legStart, legStart, legEnd}, {seam, legStart, legEnd}, {legEnd, legEnd, len(points)}}
				got, stats, err := repair(context.Background(), snapshot, points, windows, portions, coverage.ExperimentalRules(), repairOptions())
				want := []fixtureWindow{{40, 128}, {40, 64}, {47, 32}}
				if seam == 193 {
					want = []fixtureWindow{{88, 128}, {152, 64}, {177, 32}}
				}
				if kind == "tangent" {
					want = []fixtureWindow{{40, 176}, {40, 128}, {40, 64}, {42, 32}}
					if seam == 193 {
						want = []fixtureWindow{{40, 176}, {88, 128}, {151, 64}, {172, 32}}
					}
				}
				if err != nil || stats.windows != 1 || !reflect.DeepEqual(attempts, want) || reflect.DeepEqual(got, original) {
					t.Fatalf("same-leg repair: got=%+v stats=%+v attempts=%v want=%v err=%v", got, stats, attempts, want, err)
				}
				for _, portion := range got {
					if portion.window < 1 || portion.window > 2 || portion.firstTraversedAt != points[portion.routeOrder].Time {
						t.Fatalf("lost global window/order provenance: %+v", portion)
					}
				}
			})
		}
	}
}

func TestTangentPresentSegmentsAreLegScoped(t *testing.T) {
	for _, otherLeg := range []bool{false, true} {
		t.Run(fmt.Sprint(otherLeg), func(t *testing.T) {
			const seam = 127
			snapshot, points, portions := tangentFixture(false, seam)
			for i := range portions {
				portions[i].window++
			}
			// An earlier visit to one of the supported middle segments may only
			// suppress replacement when it belongs to this same leg.
			window, order := 1, 40
			if otherLeg {
				window, order = 0, 5
			}
			earlier := fixturePortion(snapshot.roads[1], 0, 1, coverage.SegmentForward, window, order, points)
			portions = append([]matchedPortion{earlier}, portions...)
			original := append([]matchedPortion(nil), portions...)
			windows := []matchingWindow{{0, 0, 32}, {32, 32, len(points)}, {seam, 32, len(points)}}
			got, stats, err := replaceCrossWindowRoadTangents(context.Background(), snapshot, points, windows, portions, coverage.ExperimentalRules(), repairOptions())
			wantLength := len(original)
			if otherLeg {
				wantLength++
			}
			if err != nil || len(got) != wantLength || stats.windows != 1 {
				t.Fatalf("leg-scoped presence: got=%+v stats=%+v err=%v", got, stats, err)
			}
			if !otherLeg && !reflect.DeepEqual(got, original) {
				t.Fatal("same-leg present segment no longer suppresses repair")
			}
			if otherLeg {
				if got[0] != earlier {
					t.Fatal("repair altered earlier-leg evidence")
				}
				for _, replacement := range got[2:4] {
					if replacement.window != 2 || replacement.routeOrder != original[2].routeOrder || replacement.firstTraversedAt != original[2].firstTraversedAt {
						t.Fatalf("replacement reset global provenance: %+v", replacement)
					}
				}
			}
		})
	}
}
