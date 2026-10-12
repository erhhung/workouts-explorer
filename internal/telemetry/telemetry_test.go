package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourceproto "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func TestSetupExportsWithSDKResourceSchema(t *testing.T) {
	priorMeter, priorTracer, priorPropagator := otel.GetMeterProvider(), otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetMeterProvider(priorMeter)
		otel.SetTracerProvider(priorTracer)
		otel.SetTextMapPropagator(priorPropagator)
	})
	var mu sync.Mutex
	seen := make(map[string]bool)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var resources []*resourceproto.Resource
		switch r.URL.Path {
		case "/v1/metrics":
			var request metricscollector.ExportMetricsServiceRequest
			if err := proto.Unmarshal(body, &request); err != nil {
				t.Error(err)
			}
			for _, item := range request.ResourceMetrics {
				resources = append(resources, item.Resource)
			}
		case "/v1/traces":
			var request tracecollector.ExportTraceServiceRequest
			if err := proto.Unmarshal(body, &request); err != nil {
				t.Error(err)
			}
			for _, item := range request.ResourceSpans {
				resources = append(resources, item.Resource)
			}
		default:
			t.Errorf("unexpected export path %q", r.URL.Path)
		}
		for _, res := range resources {
			for _, attr := range res.GetAttributes() {
				if attr.Key == "service.name" && attr.Value.GetStringValue() == "coverage-telemetry-test" {
					mu.Lock()
					seen[r.URL.Path] = true
					mu.Unlock()
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdown, err := Setup(ctx, "coverage-telemetry-test", collector.URL)
	if err != nil {
		t.Fatalf("enabled telemetry setup: %v", err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	counter, err := otel.Meter("coverage-telemetry-test").Int64Counter("test.coverage.calls")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	_, span := otel.Tracer("coverage-telemetry-test").Start(ctx, "coverage-test")
	span.End()
	if err := otel.GetMeterProvider().(*sdkmetric.MeterProvider).ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := otel.GetTracerProvider().(*sdktrace.TracerProvider).ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen["/v1/metrics"] || !seen["/v1/traces"] {
		t.Fatalf("missing named service exports: %v", seen)
	}
}
