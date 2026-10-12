package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	commonproto "go.opentelemetry.io/proto/otlp/common/v1"
)

type captureIdentityExporter struct {
	sdkmetric.Exporter
	export func(context.Context, *metricdata.ResourceMetrics) error
}

func (e captureIdentityExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	return e.export(ctx, data)
}

func identityFixtures[N int64 | float64](attrs attribute.Set) []metricdata.Aggregation {
	now := time.Unix(100, 0)
	point := metricdata.DataPoint[N]{Attributes: attrs, StartTime: now, Time: now.Add(time.Second), Value: N(7),
		Exemplars: []metricdata.Exemplar[N]{{Value: N(3), TraceID: []byte{1}, SpanID: []byte{2}}}}
	return []metricdata.Aggregation{
		metricdata.Gauge[N]{DataPoints: []metricdata.DataPoint[N]{point, point}},
		metricdata.Sum[N]{DataPoints: []metricdata.DataPoint[N]{point, point}, Temporality: metricdata.CumulativeTemporality, IsMonotonic: true},
		metricdata.Histogram[N]{Temporality: metricdata.DeltaTemporality, DataPoints: []metricdata.HistogramDataPoint[N]{
			{Attributes: attrs, StartTime: now, Time: now.Add(time.Second), Count: 2, Sum: N(7),
				Bounds: []float64{5}, BucketCounts: []uint64{1, 1}, Min: metricdata.NewExtrema(N(3)), Max: metricdata.NewExtrema(N(4)), Exemplars: point.Exemplars},
		}},
		metricdata.ExponentialHistogram[N]{Temporality: metricdata.CumulativeTemporality, DataPoints: []metricdata.ExponentialHistogramDataPoint[N]{
			{Attributes: attrs, StartTime: now, Time: now.Add(time.Second), Count: 2, Sum: N(7), Scale: 1,
				PositiveBucket: metricdata.ExponentialBucket{Offset: 1, Counts: []uint64{2}}, ZeroThreshold: 0.01, Exemplars: point.Exemplars},
		}},
	}
}

func TestIdentityExporterAllAggregations(t *testing.T) {
	attrs := attribute.NewSet(attribute.String("http.route", "/tiles/{z}/{x}/{y}"), attribute.String("service_name", "wrong"))
	fixtures := append(identityFixtures[int64](attrs), identityFixtures[float64](attrs)...)
	fixtures = append(fixtures, metricdata.Summary{DataPoints: []metricdata.SummaryDataPoint{{Attributes: attrs, Count: 2, Sum: 7,
		QuantileValues: []metricdata.QuantileValue{{Quantile: 0.5, Value: 3}}}}})
	res := resource.NewWithAttributes("fixture-schema",
		attribute.String("service.name", "workouts-api"), attribute.String("service.namespace", "workouts-explorer"),
		attribute.String("deployment.environment.name", "development"), attribute.String("service.instance.id", "pod-uid"),
		attribute.String("user.id", "must-not-promote"))
	for _, fixture := range fixtures {
		t.Run(reflect.TypeOf(fixture).String(), func(t *testing.T) {
			input := metricdata.ResourceMetrics{Resource: res, ScopeMetrics: []metricdata.ScopeMetrics{
				{Metrics: []metricdata.Metrics{{Name: "first", Description: "fixture", Unit: "s", Data: fixture}}},
				{Metrics: []metricdata.Metrics{{Name: "second", Data: fixture}}},
			}}
			ctx := context.WithValue(context.Background(), struct{}{}, "fixture")
			wantErr := errors.New("export failure")
			exporter := identityExporter{captureIdentityExporter{export: func(gotCtx context.Context, output *metricdata.ResourceMetrics) error {
				if gotCtx != ctx || output.Resource != res {
					t.Fatal("context or resource was replaced")
				}
				for i, scope := range output.ScopeMetrics {
					m := scope.Metrics[0]
					points := reflect.ValueOf(m.Data).FieldByName("DataPoints")
					for j := range points.Len() {
						got := points.Index(j).FieldByName("Attributes").Interface().(attribute.Set)
						want := attribute.NewSet(attribute.String("http.route", "/tiles/{z}/{x}/{y}"),
							attribute.String("service_name", "workouts-api"), attribute.String("service_namespace", "workouts-explorer"),
							attribute.String("deployment_environment", "development"), attribute.String("service_instance_id", "pod-uid"))
						if !got.Equals(&want) {
							t.Fatalf("point attributes: %v, want %v", got, want)
						}
						original := reflect.ValueOf(input.ScopeMetrics[i].Metrics[0].Data).FieldByName("DataPoints").Index(j).FieldByName("Attributes").Interface().(attribute.Set)
						if !original.Equals(&attrs) {
							t.Fatal("input attributes were mutated")
						}
						// Restore only the copied attributes to compare all measurement metadata.
						points.Index(j).FieldByName("Attributes").Set(reflect.ValueOf(attrs))
					}
					if !reflect.DeepEqual(m, input.ScopeMetrics[i].Metrics[0]) {
						t.Fatal("measurement data changed")
					}
				}
				return wantErr
			}}}
			if err := exporter.Export(ctx, &input); err != wantErr {
				t.Fatalf("export error: %v", err)
			}
			for _, scope := range input.ScopeMetrics {
				if !reflect.DeepEqual(scope.Metrics[0].Data, fixture) {
					t.Fatal("input data was mutated")
				}
			}
		})
	}
}

func TestIdentityExporterMissingIdentity(t *testing.T) {
	for _, res := range []*resource.Resource{nil, resource.Empty(), resource.NewSchemaless(
		attribute.String("service.namespace", ""), attribute.Int("service.instance.id", 42),
		attribute.String("deployment.environment", "legacy-not-mapped"), attribute.String("user.id", "private")),
		resource.NewSchemaless(attribute.String("service.name", "workouts-api")),
	} {
		input := metricdata.ResourceMetrics{Resource: res, ScopeMetrics: []metricdata.ScopeMetrics{{Metrics: []metricdata.Metrics{{
			Data: metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
		}}}}}
		exporter := identityExporter{captureIdentityExporter{export: func(_ context.Context, output *metricdata.ResourceMetrics) error {
			got := output.ScopeMetrics[0].Metrics[0].Data.(metricdata.Gauge[int64]).DataPoints[0].Attributes
			want := attribute.NewSet()
			if _, ok := res.Set().Value("service.name"); ok {
				want = attribute.NewSet(attribute.String("service_name", "workouts-api"))
			}
			if !got.Equals(&want) {
				t.Fatalf("invented identity or promoted unrelated resource: %v", got)
			}
			return nil
		}}}
		if err := exporter.Export(context.Background(), &input); err != nil {
			t.Fatal(err)
		}
	}
}

func assertIdentityAttributes(t *testing.T, attrs []*commonproto.KeyValue, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, attr := range attrs {
		got[attr.Key] = attr.Value.GetStringValue()
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s=%q, want %q", key, got[key], value)
		}
	}
	if _, ok := got["user.id"]; ok {
		t.Error("user identity promoted to datapoint")
	}
}

func TestSetupGRPCMetricIdentity(t *testing.T) {
	// Initialize the cached default before changing the environment for each setup.
	_ = resource.Default()
	for _, service := range []string{"workouts-api", "workouts-worker", "workouts-coverage-worker"} {
		t.Run(service, func(t *testing.T) {
			t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=ignored,service.namespace=workouts-explorer,service.instance.id="+service+"-uid,deployment.environment.name=development")
			endpoint, metrics, traces := startTestGRPCCollector(t, nil)
			preserveTestProviders(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdown, err := Setup(ctx, service, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := shutdown(ctx); err != nil {
					t.Error(err)
				}
			}()
			counter, err := otel.Meter("custom-fixture").Int64Counter("fixture.calls")
			if err != nil {
				t.Fatal(err)
			}
			counter.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("result", "ok")))
			otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "http-fixture").ServeHTTP(
				httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/fixture", nil))
			// A zero-minimum pool supplies real otelpgx metrics without a database connection.
			pool, err := pgxpool.New(ctx, "postgresql://fixture@127.0.0.1:1/fixture?pool_min_conns=0")
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if err := otelpgx.RecordStats(pool, otelpgx.WithStatsAttributes(attribute.String("db.client.connection.pool.name", service))); err != nil {
				t.Fatal(err)
			}
			_, span := otel.Tracer("identity-fixture").Start(ctx, "fixture")
			span.End()
			if err := otel.GetTracerProvider().(*sdktrace.TracerProvider).ForceFlush(ctx); err != nil {
				t.Fatal(err)
			}
			if err := otel.GetMeterProvider().(*sdkmetric.MeterProvider).ForceFlush(ctx); err != nil {
				t.Fatal(err)
			}
			wantResource := map[string]string{"service.name": service, "service.namespace": "workouts-explorer",
				"service.instance.id": service + "-uid", "deployment.environment.name": "development"}
			select {
			case request := <-traces.requests:
				for _, res := range request.ResourceSpans {
					assertIdentityAttributes(t, res.Resource.Attributes, wantResource)
					if res.SchemaUrl != resource.Default().SchemaURL() {
						t.Error("trace schema changed")
					}
				}
			case <-ctx.Done():
				t.Fatal("missing trace export")
			}
			select {
			case request := <-metrics.requests:
				want := map[string]string{"service_name": service, "service_namespace": "workouts-explorer",
					"service_instance_id": service + "-uid", "deployment_environment": "development"}
				seen := make(map[string]bool)
				for _, res := range request.ResourceMetrics {
					assertIdentityAttributes(t, res.Resource.Attributes, wantResource)
					if res.SchemaUrl != resource.Default().SchemaURL() {
						t.Error("metric schema changed")
					}
					for _, scope := range res.ScopeMetrics {
						for _, m := range scope.Metrics {
							seen[strings.Split(m.Name, ".")[0]] = true
							for _, point := range m.GetGauge().GetDataPoints() {
								assertIdentityAttributes(t, point.Attributes, want)
							}
							for _, point := range m.GetSum().GetDataPoints() {
								assertIdentityAttributes(t, point.Attributes, want)
								if m.Name == "fixture.calls" {
									assertIdentityAttributes(t, point.Attributes, map[string]string{"result": "ok"})
								}
							}
							for _, point := range m.GetHistogram().GetDataPoints() {
								assertIdentityAttributes(t, point.Attributes, want)
							}
						}
					}
				}
				for _, prefix := range []string{"fixture", "http", "pgxpool", "go"} {
					if !seen[prefix] {
						t.Errorf("missing %s metrics", prefix)
					}
				}
			case <-ctx.Done():
				t.Fatal("missing metric export")
			}
		})
	}
}
