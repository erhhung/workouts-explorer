package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

func TestCoverageDiagnosticServiceDisabledSkipsPool(t *testing.T) {
	service, err := newCoverageDiagnosticService(context.Background(), config.CoverageDiagnostics{OSMDatabaseURL: "invalid"})
	if err != nil || service.enabled || service.begin != nil || service.gate != nil {
		t.Fatalf("disabled diagnostics initialized a pool: service=%+v err=%v", service, err)
	}
}

func TestCoverageDiagnosticPoolErrorOmitsCredentials(t *testing.T) {
	const secret = "private-diagnostic-password"
	_, err := newCoverageDiagnosticService(context.Background(), config.CoverageDiagnostics{
		Enabled: true, Concurrency: 2, OSMDatabaseURL: "postgresql://user:" + secret + "@%zz",
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid diagnostic configuration returned unsafe error: %v", err)
	}
}

func TestCoverageDiagnosticPoolExportsSafeNameAndConcurrencyLimit(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	defer func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := newCoverageDiagnosticService(ctx, config.CoverageDiagnostics{
		Enabled: true, Concurrency: 2,
		OSMDatabaseURL: "postgresql://user:private-password@private-host.invalid/private-database?pool_max_conns=7&application_name=private-app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !service.enabled || service.begin == nil || cap(service.gate) != 2 {
		t.Fatalf("unexpected enabled service: %+v", service)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-password", "private-host", "private-database", "private-app"} {
		if strings.Contains(fmt.Sprint(metrics), forbidden) {
			t.Fatalf("diagnostic pool metrics expose %q", forbidden)
		}
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name != "pgxpool.max_connections" {
				continue
			}
			gauge, ok := instrument.Data.(metricdata.Gauge[int64])
			if !ok || len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != 2 {
				t.Fatalf("unexpected pool maximum: %+v", instrument)
			}
			name, ok := gauge.DataPoints[0].Attributes.Value(semconv.DBClientConnectionPoolNameKey)
			if !ok || name.AsString() != "workouts-api-coverage-diagnostics" {
				t.Fatalf("unexpected pool name: %v", name)
			}
			return
		}
	}
	t.Fatal("diagnostic pool maximum was not exported")
}

func TestDisabledCoverageDiagnosticsReturnsNotFoundWithoutOSM(t *testing.T) {
	called := false
	server := &Server{diagnostics: &coverageDiagnosticService{begin: func(context.Context) (coverageDiagnosticSnapshot, error) {
		called = true
		return nil, nil
	}}}
	request := httptest.NewRequest(http.MethodPost, "/api/workouts/00000000000000000000000000000000/coverage-diagnostic-runs", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.CreateCoverageDiagnosticRun(response, request, generated.WorkoutID("00000000000000000000000000000000"), generated.CreateCoverageDiagnosticRunParams{})
	if response.Code != http.StatusNotFound || called {
		t.Fatalf("status=%d osm_called=%t", response.Code, called)
	}
}

func TestDiagnosticMovementMode(t *testing.T) {
	for input, want := range map[string]string{"Outdoor Cycling": "bicycle", "Trail Running": "foot", "Other": "shared_public"} {
		if got := string(diagnosticMovementMode("", input)); got != want {
			t.Errorf("mode(%q)=%q want %q", input, got, want)
		}
	}
}

func TestDiagnosticResponseReportsCurrentPathPolicy(t *testing.T) {
	response := diagnosticResponse(diagnosticResult{unavailableRegions: []osm.MatcherUnavailableRegion{{RegionID: "geofabrik:new-york", DisplayName: "New York"}}})
	if response.PathPolicyVersion != generated.CoveragePathPolicyExperimentalV82 {
		t.Fatalf("path policy version=%q", response.PathPolicyVersion)
	}
	if len(response.UnavailableRegions) != 1 || response.UnavailableRegions[0].RegionId != "geofabrik:new-york" || response.UnavailableRegions[0].DisplayName != "New York" {
		t.Fatalf("unavailable regions=%+v", response.UnavailableRegions)
	}
}

func TestValidDiagnosticClipRejectsDegenerateGeometry(t *testing.T) {
	valid := osm.MatcherClippedPortion{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122.0001,37.0001]]}`)}
	if !validDiagnosticClip(valid) {
		t.Fatal("valid line rejected")
	}
	for _, clip := range []osm.MatcherClippedPortion{
		{LengthMeters: 0, GeoJSON: valid.GeoJSON},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122,37]]}`)},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"Point","coordinates":[-122,37]}`)},
	} {
		if validDiagnosticClip(clip) {
			t.Fatalf("degenerate clip accepted: %+v", clip)
		}
	}
}
