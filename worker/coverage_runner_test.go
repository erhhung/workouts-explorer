package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

func TestCoveragePersistencePayloadKeepsGeometryAsJSON(t *testing.T) {
	payload := coveragePersistencePayload([]routepipeline.PreparedMatch{{
		Segment: osm.MatcherSegmentCopy{SegmentID: uuid.New(), RegionID: "geofabrik:test", GenerationID: 1,
			LogicalPathID: uuid.New(), DerivationVersion: 1, SourceWayID: 42, SourceWayVersion: 1,
			Highway: "residential", BroadClass: "road", Tags: json.RawMessage(`{"surface":"paved"}`),
			GeoJSON: json.RawMessage(`{"type":"LineString","coordinates":[[-122,37],[-122.001,37.001]]}`), LengthMeters: 100},
		CoveredGeoJSON:   json.RawMessage(`{"type":"MultiLineString","coordinates":[[[-122,37],[-122.001,37.001]]]}`),
		FirstTraversedAt: time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC),
	}})
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded[0]["segmentGeometry"].(map[string]any); !ok {
		t.Fatalf("segment geometry was not embedded JSON: %s", encoded)
	}
	if _, ok := decoded[0]["coveredGeometry"].(map[string]any); !ok {
		t.Fatalf("covered geometry was not embedded JSON: %s", encoded)
	}
}

func TestClassifyCoverageFailure(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if failure := classifyCoverageFailure(ctx, coverageStageEvaluate, context.DeadlineExceeded); failure.code != "coverage-route-timeout" {
		t.Fatalf("timeout code=%s", failure.code)
	}
	if failure := classifyCoverageFailure(context.Background(), coverageStageEvaluate, &routepipeline.LimitError{Field: "segments", Limit: 1}); failure.code != "coverage-route-too-large" {
		t.Fatalf("limit code=%s", failure.code)
	}
}

func TestCoverageRouteTimeoutBackoff(t *testing.T) {
	for _, test := range []struct {
		retries int
		want    time.Duration
	}{{-1, 6 * time.Minute}, {0, 6 * time.Minute}, {1, 10 * time.Minute}, {2, 15 * time.Minute}, {3, 15 * time.Minute}, {16, 15 * time.Minute}, {int(^uint(0) >> 1), 15 * time.Minute}} {
		t.Run(strconv.Itoa(test.retries), func(t *testing.T) {
			if got := coverageRouteTimeout(6*time.Minute, test.retries); got != test.want {
				t.Errorf("coverageRouteTimeout(360s, %d) = %s, want %s", test.retries, got, test.want)
			}
		})
	}
}

func TestCoverageRouteTimeoutCustomInitial(t *testing.T) {
	for _, test := range []struct {
		base time.Duration
		want [3]time.Duration
	}{
		{2 * time.Second, [3]time.Duration{2 * time.Second, 10 * time.Minute, 15 * time.Minute}},
		{12 * time.Minute, [3]time.Duration{12 * time.Minute, 12 * time.Minute, 15 * time.Minute}},
		{15 * time.Minute, [3]time.Duration{15 * time.Minute, 15 * time.Minute, 15 * time.Minute}},
		{20 * time.Minute, [3]time.Duration{15 * time.Minute, 15 * time.Minute, 15 * time.Minute}},
		{time.Duration(1<<63 - 1), [3]time.Duration{15 * time.Minute, 15 * time.Minute, 15 * time.Minute}},
		{0, [3]time.Duration{0, 0, 0}},
		{-time.Second, [3]time.Duration{-time.Second, -time.Second, -time.Second}},
	} {
		t.Run(test.base.String(), func(t *testing.T) {
			// Some bases are helper guards only; LoadCoverageWorker rejects them.
			for retry, want := range test.want {
				if got := coverageRouteTimeout(test.base, retry); got != want {
					t.Errorf("coverageRouteTimeout(%s, %d) = %s, want %s", test.base, retry, got, want)
				}
			}
			if got := coverageRouteTimeout(test.base, -1); got != test.want[0] {
				t.Errorf("negative retry timeout = %s, want %s", got, test.want[0])
			}
		})
	}
}

func TestCoverageRunnerTimeoutDefaults(t *testing.T) {
	runner := NewCoverageRunner(nil, nil, slog.Default())
	if runner.routeTimeout != 6*time.Minute || runner.leaseDuration != 7*time.Minute || runner.heartbeatInterval != 20*time.Second {
		t.Fatalf("defaults: timeout=%s lease=%s heartbeat=%s", runner.routeTimeout, runner.leaseDuration, runner.heartbeatInterval)
	}
	if runner.routeTimeout+10*time.Second >= runner.leaseDuration {
		t.Fatal("default lease does not leave the required initial-timeout cleanup margin")
	}
}

func TestCoverageRunnerUsesSharedPointLimit(t *testing.T) {
	if coverageOriginalPointLimit != routepipeline.OriginalPointLimit {
		t.Fatal("worker raw-point bound differs from diagnostics")
	}
	if routepipeline.OriginalPointLimit != 50000 {
		t.Fatal("shared raw-point bound changed")
	}
	if limits := routepipeline.DefaultLimits(); limits != (routepipeline.Limits{SampledPoints: 12000, Portions: 10000, Segments: 4096, Generations: 256, UnavailableRegions: 64}) {
		t.Fatalf("shared matching-limit contract=%+v", limits)
	}
}

func TestCoverageRunnerAutoRetryOptions(t *testing.T) {
	if got := NewCoverageRunner(nil, nil, slog.Default()).autoRetryCount; got != 1 {
		t.Fatalf("default automatic retries=%d", got)
	}
	for _, count := range []int{0, 1, 20, -1, 21} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			runner := NewCoverageRunnerWithOptions(nil, nil, slog.Default(), CoverageRunnerOptions{AutoRetryCount: &count})
			if count < 0 || count > 20 {
				if runner.readerInitErr == nil || runner.Run(context.Background()) == nil {
					t.Fatal("invalid automatic retry count accepted")
				}
			} else if runner.autoRetryCount != count || runner.readerInitErr != nil {
				t.Fatalf("automatic retries=%d want=%d err=%v", runner.autoRetryCount, count, runner.readerInitErr)
			}
		})
	}
}
