package routepipeline

import (
	"context"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

type preparationCancelContext struct {
	context.Context
	cancel         context.CancelFunc
	checks, stopAt int
	canceledAt     time.Time
}

func (ctx *preparationCancelContext) Err() error {
	ctx.checks++
	if ctx.checks == ctx.stopAt {
		ctx.canceledAt = time.Now()
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestPrepareStopsDuringIntervalMerge(t *testing.T) {
	const count = 10000
	id := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	result := Result{Evidence: make([]Evidence, count)}
	for i := range result.Evidence {
		result.Evidence[i] = Evidence{Portion: coverage.TraversedPortion{PhysicalSegmentID: id, RegionID: "fixture:region", GenerationID: 1,
			SourceFromFraction: float64(i) / count, SourceToFraction: float64(i+2) / count}, RouteOrder: i}
	}
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	base, cancel := context.WithCancel(deadline)
	defer cancel()
	// Entry, grouping, key collection, first key, earliest-order scan, then the
	// third interval-merge checkpoint. Cancellation happens after real merging.
	ctx := &preparationCancelContext{Context: base, cancel: cancel, stopAt: 1 + (count+127)/128 + 1 + 1 + (count+127)/128 + 3}
	snapshot := &preparationSnapshot{}
	prepared, err := Prepare(ctx, snapshot, result, time.Time{})
	if ctx.canceledAt.IsZero() || time.Since(ctx.canceledAt) >= time.Second {
		t.Fatal("interval merge did not cooperatively stop within one second")
	}
	if err != context.Canceled || prepared != nil || len(snapshot.refs) != 0 {
		t.Fatalf("preparation continued to snapshot work: prepared=%v err=%v refs=%d", prepared, err, len(snapshot.refs))
	}
}

type windowCancelSnapshot struct {
	emptySnapshot
	cancel context.CancelFunc
	calls  int
}

func (snapshot *windowCancelSnapshot) Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error) {
	snapshot.calls++
	if snapshot.calls == 2 {
		snapshot.cancel()
	}
	return nil, nil
}

func TestEvaluateStopsAcrossRouteWindows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshot := &windowCancelSnapshot{cancel: cancel}
	observations := make([]coverage.GeographicObservation, WindowSize*3)
	for i := range observations {
		observations[i] = coverage.GeographicObservation{Sequence: i, Longitude: float64(i) / 10000}
	}
	sampling := coverage.ExperimentalSamplingRules()
	sampling.MinimumDistanceMeters = 0
	started := time.Now()
	result, err := Evaluate(ctx, snapshot, observations, Config{Rules: coverage.ExperimentalRules(), Sampling: sampling,
		RoadGeometry: coverage.DefaultRoadGeometryRules(), Limits: DefaultLimits()})
	if err != context.Canceled || snapshot.calls != 2 || len(result.Evidence) != 0 || result.Counts.OriginalPoints != 0 {
		t.Fatalf("route pipeline did not discard partial evaluation: result=%+v err=%v calls=%d", result, err, snapshot.calls)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("route pipeline did not stop within one second")
	}
}

func TestRoutePipelineRejectsAlreadyStoppedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, nil, nil, Config{}); err != context.Canceled {
		t.Fatalf("Evaluate error=%v", err)
	}
	if _, err := Prepare(ctx, nil, Result{}, time.Time{}); err != context.Canceled {
		t.Fatalf("Prepare error=%v", err)
	}
}
