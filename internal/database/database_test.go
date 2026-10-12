package database

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

func TestSupportedSchemaVersion(t *testing.T) {
	if SupportedSchemaVersion != 20 {
		t.Fatalf("SupportedSchemaVersion = %d, want 20", SupportedSchemaVersion)
	}
}

func TestRuntimeReadinessBackwardCompatibleSchema(t *testing.T) {
	sql := strings.Join(strings.Fields(runtimeReadinessSQL), " ")
	for _, required := range []string{
		"COALESCE(max(version_id) FILTER (WHERE is_applied), 0) >= $1",
		"WHERE singleton AND schema_version >= $1 AND minimum_runtime_version <= $1",
		"has_column_privilege('workouts_coverage_worker','app.jobs','created_at','SELECT')",
		"has_column_privilege('workouts_coverage_worker','app.jobs','attempt','SELECT')",
		"has_column_privilege('workouts_coverage_worker','app.jobs','retry_of_job_id','SELECT')",
		"NOT has_table_privilege('workouts_coverage_worker','app.jobs','SELECT')",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("runtime readiness SQL is missing %q", required)
		}
	}
	for _, forbidden := range []string{"schema_version = $1", "schema_version <= $1", "schema_version BETWEEN", "max(version_id) FILTER (WHERE is_applied), 0) = $1"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("runtime readiness SQL rejects compatible newer schemas: %q", forbidden)
		}
	}
}

func TestOpenDoesNotReturnDatabaseCredentials(t *testing.T) {
	secret := "do-not-expose-this-value"
	_, err := Open(context.Background(), "postgresql://user:"+secret+"@%zz", "test")
	if err == nil {
		t.Fatal("invalid database URL unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("database error exposed credentials")
	}
}

func TestPoolMetricsUseSafeApplicationName(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgresql://user:secret@private-database.internal/sensitive_name")
	if err != nil {
		t.Fatal(err)
	}
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := recordPoolStats(pool, "workouts-test", otelpgx.WithStatsMeterProvider(provider)); err != nil {
		t.Fatal(err)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	metricdatatest.AssertHasAttributes(t, metrics, semconv.DBClientConnectionPoolName("workouts-test"))
	serialized := fmt.Sprint(metrics)
	for _, forbidden := range []string{"secret", "private-database.internal", "sensitive_name"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("pool metrics contain sensitive value %q", forbidden)
		}
	}
}

func TestDatabaseTraceOmitsSQLParametersAndConnectionDetails(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer provider.Shutdown(context.Background())
	tracer := newTracer(otelpgx.WithTracerProvider(provider))
	secret := "sensitive-database-marker"
	parentCtx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	ctx := tracer.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT * FROM private_data WHERE credential = $1",
		Args: []any{secret},
	})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	parent.End()
	spans := recorder.Ended()
	var serialized string
	for _, span := range spans {
		if span.Name() == "postgresql.query" {
			serialized = fmt.Sprint(span.Attributes())
		}
	}
	if serialized == "" {
		t.Fatalf("unexpected database spans: %#v", spans)
	}
	for _, forbidden := range []string{secret, "private_data", "credential", "server.address", "db.namespace"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("database span contains sensitive value %q: %s", forbidden, serialized)
		}
	}
}
