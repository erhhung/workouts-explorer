package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func coverageTestMetrics(t *testing.T) (coverageMetrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return newCoverageMetrics(provider.Meter(coverageMeterName)), reader
}

func collectCoverageMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]metricdata.Metrics)
	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Name != coverageMeterName {
			t.Fatalf("unexpected scope %q", scope.Scope.Name)
		}
		for _, instrument := range scope.Metrics {
			result[instrument.Name] = instrument
		}
	}
	return result
}

func TestCoverageMetricsExport(t *testing.T) {
	m, reader := coverageTestMetrics(t)
	ctx := context.Background()
	claimedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	m.loaded(ctx, claimedAt, claimedAt.Add(-30*time.Second), 1, false, 0)
	m.loaded(ctx, claimedAt, claimedAt.Add(-time.Hour), 2, true, 1)
	m.matcherDuration.Record(ctx, 2)
	m.routeDuration.Record(ctx, 12)
	m.admissionWait.Record(ctx, 3)
	for _, outcome := range []string{"applied", "no_evidence", "superseded", "cancelled"} {
		m.terminal(ctx, outcome, "")
	}
	for _, code := range []string{"coverage-route-timeout", "coverage-route-too-large", "coverage-route-invalid",
		"coverage-route-read-failed", "coverage-persist-failed", "coverage-osm-unavailable", "private-error-marker"} {
		m.terminal(ctx, "failed", code)
	}
	m.terminal(ctx, "private-outcome-marker", "")
	for _, call := range []struct{ operation, result string }{
		{"heartbeat", "renewed"}, {"heartbeat", "lost"}, {"admission", "denied"},
		{"admission", "acquired"}, {"release", "released"}, {"release", "not_found"},
	} {
		m.control(ctx, call.operation, call.result, nil)
	}
	m.control(ctx, "heartbeat", "", context.DeadlineExceeded)
	m.control(ctx, "heartbeat", "", context.Canceled)
	m.control(ctx, "release", "", errors.New("private-error-marker"))
	for _, operation := range []string{"persist", "fail", "supersede", "cancel"} {
		m.reject(ctx, operation)
	}
	metrics := collectCoverageMetrics(t, reader)
	if len(metrics) != 8 {
		t.Fatalf("exported %d instruments, want 8", len(metrics))
	}
	for name, want := range map[string]float64{
		"coverage.queue.age": 30, "coverage.matcher.duration": 2,
		"coverage.route.duration": 12, "coverage.admission.wait": 3,
	} {
		instrument := metrics[name]
		histogram, ok := instrument.Data.(metricdata.Histogram[float64])
		if !ok || instrument.Unit != "s" || len(histogram.DataPoints) != 1 {
			t.Fatalf("%s: unexpected histogram %#v", name, instrument)
		}
		point := histogram.DataPoints[0]
		if point.Count != 1 || point.Sum != want || point.Attributes.Len() != 0 {
			t.Errorf("%s: point=%#v, want one unlabelled observation of %v", name, point, want)
		}
	}
	for name, want := range map[string]map[string]int64{
		"coverage.route.outcomes": {
			"applied/none": 1, "no_evidence/none": 1, "superseded/none": 1, "cancelled/none": 1,
			"failed/timeout": 1, "failed/limit": 1, "failed/invalid": 1, "failed/route_read": 1,
			"failed/persist": 1, "failed/osm": 1, "failed/other": 1,
		},
		"coverage.control.calls": {
			"heartbeat/renewed": 1, "heartbeat/lost": 1, "heartbeat/timeout": 1, "heartbeat/cancelled": 1,
			"admission/denied": 1, "admission/acquired": 1, "release/released": 1, "release/not_found": 1, "release/error": 1,
		},
		"coverage.retry.attempts":   {"recovery": 1, "retry": 1, "timeout_backoff": 1},
		"coverage.stale.rejections": {"persist": 1, "fail": 1, "supersede": 1, "cancel": 1},
	} {
		instrument := metrics[name]
		sum, ok := instrument.Data.(metricdata.Sum[int64])
		if !ok || !sum.IsMonotonic || instrument.Unit != "{event}" || len(sum.DataPoints) != len(want) {
			t.Fatalf("%s: unexpected counter %#v", name, instrument)
		}
		for _, point := range sum.DataPoints {
			keys := []string{"operation"}
			switch name {
			case "coverage.route.outcomes":
				keys = []string{"outcome", "failure"}
			case "coverage.control.calls":
				keys = []string{"operation", "result"}
			case "coverage.retry.attempts":
				keys = []string{"kind"}
			}
			if point.Attributes.Len() != len(keys) {
				t.Fatalf("%s: unexpected attributes %v", name, point.Attributes)
			}
			label := ""
			for i, key := range keys {
				value, ok := point.Attributes.Value(attribute.Key(key))
				if !ok {
					t.Fatalf("%s: missing attribute %s", name, key)
				}
				if i > 0 {
					label += "/"
				}
				label += value.AsString()
			}
			if expected, ok := want[label]; !ok || point.Value != expected {
				t.Errorf("%s: %s=%d, want %d (known=%v)", name, label, point.Value, expected, ok)
			}
		}
	}
}

func TestCoverageQueueAgeClampsClockSkew(t *testing.T) {
	m, reader := coverageTestMetrics(t)
	now := time.Now()
	m.loaded(context.Background(), now, now.Add(time.Minute), 1, false, 0)
	histogram := collectCoverageMetrics(t, reader)["coverage.queue.age"].Data.(metricdata.Histogram[float64])
	if point := histogram.DataPoints[0]; point.Count != 1 || point.Sum != 0 {
		t.Fatalf("negative queue age was not clamped: %#v", point)
	}
}

func TestCoverageHeartbeatControlTimeoutCancelsWithoutDurableOutcome(t *testing.T) {
	m, reader := coverageTestMetrics(t)
	config, err := pgxpool.ParseConfig("postgresql://coverage:unused@127.0.0.1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	// Fail control acquisition before any network I/O, not a full runner test.
	config.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		return context.DeadlineExceeded
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	r := &CoverageRunner{db: pool, metrics: m, heartbeatInterval: time.Millisecond}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan error, 1)
	go r.heartbeat(ctx, coverageClaim{}, cancel, done)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatalf("heartbeat timeout did not cancel the route: heartbeat=%v cause=%v", err, context.Cause(ctx))
		}
	case <-time.After(cleanupTimeout + time.Second):
		t.Fatal("heartbeat did not stop after control timeout")
	}
	metrics := collectCoverageMetrics(t, reader)
	if _, found := metrics["coverage.route.outcomes"]; found {
		t.Fatal("heartbeat timeout recorded an outcome without a durable transition")
	}
	sum := metrics["coverage.control.calls"].Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("unexpected heartbeat observations: %#v", sum)
	}
	operation, _ := sum.DataPoints[0].Attributes.Value("operation")
	result, _ := sum.DataPoints[0].Attributes.Value("result")
	if operation.AsString() != "heartbeat" || result.AsString() != "timeout" {
		t.Fatalf("unexpected heartbeat attributes: %v", sum.DataPoints[0].Attributes)
	}
}

type coverageRollbackTx struct {
	pgx.Tx
	err    error
	called bool
}

func (tx *coverageRollbackTx) Rollback(ctx context.Context) error {
	tx.called = true
	deadline, ok := ctx.Deadline()
	if ctx.Err() != nil || !ok || time.Until(deadline) > cleanupTimeout {
		return fmt.Errorf("unusable rollback context: %v", ctx.Err())
	}
	return tx.err
}

func TestCoverageRollbackReportsErrorsNotClosedTransactions(t *testing.T) {
	m, reader := coverageTestMetrics(t)
	r := &CoverageRunner{metrics: m, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, err := range []error{nil, pgx.ErrTxClosed, context.DeadlineExceeded} {
		tx := &coverageRollbackTx{err: err}
		r.rollback(tx)
		if !tx.called {
			t.Fatal("rollback not called")
		}
	}
	sum := collectCoverageMetrics(t, reader)["coverage.control.calls"].Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("unexpected rollback error count: %#v", sum)
	}
	point := sum.DataPoints[0]
	operation, _ := point.Attributes.Value("operation")
	result, _ := point.Attributes.Value("result")
	if operation.AsString() != "rollback" || result.AsString() != "timeout" {
		t.Fatalf("unexpected rollback attributes: %v", point.Attributes)
	}
}
