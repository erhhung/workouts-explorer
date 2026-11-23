package routepipeline

import (
	"context"
	"net"

	"github.com/erhhung/workouts-explorer/internal/coverage"
)

// Only explicit local matcher bounds qualify. Database, cancellation, transport,
// malformed-input and persistence limits must never become successful gaps.
func recoverableProcessingLimit(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(net.Error); ok {
		return false
	}
	switch value := err.(type) {
	case *coverage.ParkingRepairLimitError:
		if value == nil {
			return false
		}
		switch value.Field {
		case "parking repair accepted replacements", "parking repair states", "parking repair state bytes", "parking repair interned portions", "parking repair intern bytes", "parking repair suffix nodes":
			limit, ok := value.Unwrap().(*coverage.OSMEvaluationLimitError)
			return ok && limit.Limit > 0
		}
		return false
	case *coverage.OSMEvaluationLimitError:
		return value != nil && value.Limit > 0 && (value.Field == "graph node" || value.Field == "graph edge")
	case interface{ Unwrap() []error }:
		children := value.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !recoverableProcessingLimit(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return recoverableProcessingLimit(value.Unwrap())
	default:
		return false
	}
}

type exhaustedWindow struct {
	start, end, attempts int
	cause                error
}

func (failure *exhaustedWindow) Error() string { return failure.cause.Error() }
func (failure *exhaustedWindow) Unwrap() error { return failure.cause }

func matchPipelineWindow(ctx context.Context, snapshot Snapshot, points []coverage.GeographicObservation, rules coverage.Rules, options coverage.OSMEvaluationOptions, initialSize int, bounds func(int) (int, int), recoverLimits bool) (coverage.Result, coverage.OSMEvaluationStats, int, error) {
	if !recoverLimits {
		return matchAdaptiveWindow(ctx, snapshot, points, rules, options, initialSize, bounds)
	}
	previousStart, previousEnd, attempts := -1, -1, 0
	var last error
	for _, size := range []int{initialSize, WindowSize, WindowSize / 2, WindowSize / 4, WindowSize / 8, WindowSize / 16, WindowSize / 32, 2} {
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
			stats.WindowLimitRetries = attempts
			return matched, stats, end, nil
		}
		if !recoverableProcessingLimit(err) {
			return coverage.Result{}, coverage.OSMEvaluationStats{}, 0, err
		}
		attempts++
		last = err
	}
	return coverage.Result{}, coverage.OSMEvaluationStats{}, 0, &exhaustedWindow{start: previousStart, end: previousEnd, attempts: attempts, cause: last}
}
