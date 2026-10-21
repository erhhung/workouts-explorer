package routepipeline

import (
	"context"
	"errors"
	"net"

	"github.com/erhhung/workouts-explorer/internal/coverage"
)

// Only deterministic node/edge exhaustion is recoverable. Every leaf of a
// joined error must qualify; errors.As alone could hide a permanent failure.
func recoverableGraphLimit(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := err.(net.Error); ok {
		return false
	}
	switch e := err.(type) {
	case *coverage.OSMEvaluationLimitError:
		return e != nil && (e.Field == "graph node" || e.Field == "graph edge")
	case interface{ Unwrap() []error }:
		children := e.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !recoverableGraphLimit(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return recoverableGraphLimit(e.Unwrap())
	default:
		return false
	}
}

// All attempts share the caller's context, snapshot, and unchanged graph bounds.
// A successful rematch is indivisible: smaller contexts are never concatenated.
func matchAdaptiveWindow(ctx context.Context, snapshot Snapshot, points []coverage.GeographicObservation, rules coverage.Rules, options coverage.OSMEvaluationOptions, initialSize int, bounds func(int) (int, int)) (coverage.Result, coverage.OSMEvaluationStats, int, error) {
	previousStart, previousEnd := -1, -1
	var lastErr error
	for _, size := range []int{initialSize, WindowSize, WindowSize / 2, WindowSize / 4} {
		if err := ctx.Err(); err != nil {
			return coverage.Result{}, coverage.OSMEvaluationStats{}, 0, err
		}
		start, end := bounds(size)
		if start == previousStart && end == previousEnd {
			continue
		}
		previousStart, previousEnd = start, end
		matched, stats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], rules, options)
		if stopped := ctx.Err(); stopped != nil {
			return coverage.Result{}, coverage.OSMEvaluationStats{}, 0, stopped
		}
		if err == nil {
			return matched, stats, end, nil
		}
		if !recoverableGraphLimit(err) {
			return coverage.Result{}, coverage.OSMEvaluationStats{}, 0, err
		}
		lastErr = err
	}
	return coverage.Result{}, coverage.OSMEvaluationStats{}, 0, lastErr
}

func seamWindowBounds(count, seam, left, total int) func(int) (int, int) {
	return func(size int) (int, int) {
		start := max(0, seam-size*left/total)
		end := min(count, start+size)
		return max(0, end-size), end
	}
}
