package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoverageWorkerLoopsBoundConcurrencyAndJoinCleanup(t *testing.T) {
	for _, n := range []int{0, 1, 2, 8} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			runner := NewCoverageRunnerWithOptions(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), CoverageRunnerOptions{JobConcurrency: n})
			want := n
			if want == 0 {
				want = 2
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{}, want+1)
			cleaning := make(chan struct{}, want)
			cleanup := make(chan struct{})
			defer close(cleanup)
			var active, calls atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- runner.runWorkers(ctx, func(ctx context.Context) (bool, error) {
					calls.Add(1)
					active.Add(1)
					defer active.Add(-1)
					entered <- struct{}{}
					<-ctx.Done()
					cleaning <- struct{}{}
					<-cleanup
					return true, ctx.Err()
				})
			}()
			for range want {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("fixed execution loops did not start")
				}
			}
			if got := active.Load(); got != int32(want) {
				t.Fatalf("active=%d, want %d", got, want)
			}
			cancel()
			for range want {
				select {
				case <-cleaning:
				case <-time.After(time.Second):
					t.Fatal("execution loop did not observe shutdown")
				}
			}
			select {
			case <-done:
				t.Fatal("runner returned before route cleanup finished")
			default:
			}
			for range want {
				cleanup <- struct{}{}
			}
			select {
			case err := <-done:
				if err != nil || active.Load() != 0 || calls.Load() != int32(want) {
					t.Fatalf("joined error=%v active=%d calls=%d", err, active.Load(), calls.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("runner did not join completed cleanup")
			}
		})
	}
}

func TestCoverageWorkerLoopsPollAfterIdleOrError(t *testing.T) {
	for _, cycleErr := range []error{nil, errors.New("fixture cycle failure")} {
		runner := NewCoverageRunnerWithOptions(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), CoverageRunnerOptions{
			JobConcurrency: 2, PollInterval: time.Millisecond,
		})
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		err := runner.runWorkers(ctx, func(context.Context) (bool, error) {
			if calls.Add(1) >= 4 {
				cancel()
			}
			return false, cycleErr
		})
		cancel()
		if err != nil || calls.Load() < 4 {
			t.Fatalf("polling error=%v calls=%d", err, calls.Load())
		}
	}
}

func TestCoverageInvalidConcurrencyFailsBeforeClaim(t *testing.T) {
	for _, n := range []int{-1, 9} {
		runner := NewCoverageRunnerWithOptions(nil, nil, slog.Default(), CoverageRunnerOptions{JobConcurrency: n})
		if err := runner.Run(context.Background()); err == nil {
			t.Fatal("Run accepted invalid concurrency")
		}
		if worked, err := runner.RunOnce(context.Background()); worked || err == nil {
			t.Fatalf("invalid options reached claim: worked=%v error=%v", worked, err)
		}
	}
}

func TestCoverageCancelledRunDoesNotClaim(t *testing.T) {
	runner := NewCoverageRunner(nil, nil, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
