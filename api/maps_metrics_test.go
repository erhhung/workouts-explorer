package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type tileMetricWriter struct {
	*httptest.ResponseRecorder
	delay time.Duration
	fail  bool
}

func (w tileMetricWriter) Write(body []byte) (int, error) {
	time.Sleep(w.delay)
	if w.fail {
		return 0, errors.New("client disconnected")
	}
	return w.ResponseRecorder.Write(body)
}

func TestTileProxyDurationMetrics(t *testing.T) {
	const delay = 15 * time.Millisecond
	tests := []struct {
		name, outcome  string
		upstreamStatus int
		contentType    string
		body           string
		cancel, fail   bool
		status         int
	}{
		{"success", "success", http.StatusOK, "application/x-protobuf", "tile", false, false, http.StatusOK},
		{"empty", "success", http.StatusNoContent, "", "", false, false, http.StatusOK},
		{"upstream status", "invalid_response", http.StatusServiceUnavailable, "application/x-protobuf", "", false, false, http.StatusBadGateway},
		{"content type", "invalid_response", http.StatusOK, "text/html", "tile", false, false, http.StatusBadGateway},
		{"redirect", "invalid_response", http.StatusFound, "", "", false, false, http.StatusBadGateway},
		{"truncated body", "body_error", http.StatusOK, "application/x-protobuf", "tile", false, false, http.StatusBadGateway},
		{"oversized body", "too_large", http.StatusOK, "application/x-protobuf", strings.Repeat("x", maximumTileBytes+1), false, false, http.StatusBadGateway},
		{"canceled forwarding", "upstream_error", http.StatusOK, "application/x-protobuf", "tile", true, false, http.StatusBadGateway},
		{"failed write", "write_error", http.StatusOK, "application/x-protobuf", "tile", false, true, http.StatusOK},
	}
	for _, kind := range []string{"route", "coverage"} {
		for _, test := range tests {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				reader := sdkmetric.NewManualReader()
				provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
				previous := otel.GetMeterProvider()
				otel.SetMeterProvider(provider)
				t.Cleanup(func() {
					otel.SetMeterProvider(previous)
					_ = provider.Shutdown(context.Background())
				})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					wantPath := "/app.raw_route_mvt/12/655/1582"
					if kind == "coverage" {
						wantPath = "/app.coverage_mvt/12/655/1582"
					}
					if r.URL.Path != wantPath {
						t.Errorf("upstream path=%q, want %q", r.URL.Path, wantPath)
					}
					if test.name == "success" {
						time.Sleep(delay)
					}
					w.Header().Set("Content-Type", test.contentType)
					if test.name == "redirect" {
						w.Header().Set("Location", "/must-not-follow")
					}
					if test.name == "truncated body" {
						w.Header().Set("Content-Length", strconv.Itoa(len(test.body)+1))
					}
					w.WriteHeader(test.upstreamStatus)
					if test.name == "success" {
						_, _ = w.Write([]byte(test.body[:1]))
						w.(http.Flusher).Flush()
						time.Sleep(delay)
						_, _ = w.Write([]byte(test.body[1:]))
						return
					}
					_, _ = w.Write([]byte(test.body))
				}))
				defer upstream.Close()
				service := newMapTileService(context.Background(), config.MapTiles{}, upstream.URL, nil)
				defer service.close()
				key := mapTileKey{account: uuid.New(), session: uuid.New(), selection: uuid.New(), generation: 42,
					coverage: kind == "coverage", z: 12, x: 655, y: 1582}
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					writeMapTileResult(w, r, key.kind(), service.fetch(r.Context(), key), allowTileTestDelivery)
				})
				request := httptest.NewRequest(http.MethodGet, "/api/private/12/655/1582?generation=42", nil)
				if test.cancel {
					ctx, cancel := context.WithCancel(request.Context())
					cancel()
					request = request.WithContext(ctx)
				}
				writer := tileMetricWriter{ResponseRecorder: httptest.NewRecorder(), fail: test.fail}
				if test.name == "success" {
					writer.delay = delay
				}
				handler.ServeHTTP(writer, request)
				if writer.Code != test.status {
					t.Fatalf("status=%d, want %d", writer.Code, test.status)
				}
				if test.outcome == "success" && writer.Body.String() != test.body {
					t.Fatalf("body=%q, want %q", writer.Body.String(), test.body)
				}
				var metrics metricdata.ResourceMetrics
				if err := reader.Collect(context.Background(), &metrics); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, scope := range metrics.ScopeMetrics {
					for _, instrument := range scope.Metrics {
						if instrument.Name != "workouts.api.tile.duration" {
							continue
						}
						found = true
						histogram, ok := instrument.Data.(metricdata.Histogram[float64])
						if !ok || instrument.Unit != "s" || len(histogram.DataPoints) != 1 {
							t.Fatalf("unexpected histogram: %+v", instrument)
						}
						point := histogram.DataPoints[0]
						want := attribute.NewSet(attribute.String("tile_kind", kind), attribute.String("outcome", test.outcome),
							attribute.Int("http.response.status_code", test.status))
						if !point.Attributes.Equals(&want) || point.Count != 1 || point.Sum <= 0 || len(point.Bounds) == 0 {
							t.Fatalf("unexpected point: %+v, want attributes %v", point, want)
						}
						if test.name == "success" && point.Sum < (3*delay).Seconds() {
							t.Fatalf("duration %f excludes upstream headers/body or downstream write", point.Sum)
						}
					}
				}
				if !found {
					t.Fatal("tile duration histogram was not exported")
				}
			})
		}
	}
}
