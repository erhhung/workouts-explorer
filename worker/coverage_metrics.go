package worker

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const coverageMeterName = "github.com/erhhung/workouts-explorer/worker/coverage"

type coverageMetrics struct {
	queueAge, matcherDuration, routeDuration, admissionWait metric.Float64Histogram
	outcomes, controls, retries, stale                      metric.Int64Counter
}

func newCoverageMetrics(meter metric.Meter) coverageMetrics {
	histogram := func(name, description string) metric.Float64Histogram {
		instrument, err := meter.Float64Histogram(name, metric.WithDescription(description), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.01, .1, .5, 1, 5, 10, 30, 60, 180, 300, 600, 1800, 3600))
		if err != nil {
			otel.Handle(err)
		}
		return instrument
	}
	counter := func(name, description string) metric.Int64Counter {
		instrument, err := meter.Int64Counter(name, metric.WithDescription(description), metric.WithUnit("{event}"))
		if err != nil {
			otel.Handle(err)
		}
		return instrument
	}
	return coverageMetrics{
		queueAge:        histogram("coverage.queue.age", "Job creation to first claim; excludes recoveries."),
		matcherDuration: histogram("coverage.matcher.duration", "Evaluate call duration, excluding snapshot setup, preparation and persistence."),
		routeDuration:   histogram("coverage.route.duration", "Postclaim attempt duration, including admission and cleanup."),
		admissionWait:   histogram("coverage.admission.wait", "Admission loop duration, including unsuccessful exits."),
		outcomes:        counter("coverage.route.outcomes", "Confirmed durable terminal route transitions."),
		controls:        counter("coverage.control.calls", "Lease and admission control results."),
		retries:         counter("coverage.retry.attempts", "Observed recovery, explicit retry and timeout-backoff attempts."),
		stale:           counter("coverage.stale.rejections", "Terminal transitions rejected by ownership fencing."),
	}
}

func (m coverageMetrics) loaded(ctx context.Context, claimedAt, createdAt time.Time, attempts int, retry bool, timeoutRetries int) {
	if attempts == 1 {
		m.queueAge.Record(ctx, max(0, claimedAt.Sub(createdAt).Seconds()))
	} else if attempts > 1 {
		m.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "recovery")))
	}
	if retry {
		m.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "retry")))
	}
	if timeoutRetries > 0 {
		m.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "timeout_backoff")))
	}
}

func (m coverageMetrics) terminal(ctx context.Context, outcome, code string) {
	switch outcome {
	case "applied", "no_evidence", "superseded", "failed", "cancelled":
	default:
		return
	}
	category := "none"
	if outcome == "failed" {
		switch code {
		case "coverage-route-timeout":
			category = "timeout"
		case "coverage-route-too-large":
			category = "limit"
		case "coverage-route-invalid":
			category = "invalid"
		case "coverage-route-read-failed":
			category = "route_read"
		case "coverage-persist-failed":
			category = "persist"
		case "coverage-osm-unavailable":
			category = "osm"
		default:
			category = "other"
		}
	}
	m.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome), attribute.String("failure", category)))
}

func (m coverageMetrics) control(ctx context.Context, operation, result string, err error) {
	if err != nil {
		result = "error"
		if errors.Is(err, context.DeadlineExceeded) {
			result = "timeout"
		} else if errors.Is(err, context.Canceled) {
			result = "cancelled"
		}
	}
	m.controls.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", operation), attribute.String("result", result)))
}

func (m coverageMetrics) reject(ctx context.Context, operation string) {
	m.stale.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", operation)))
}
