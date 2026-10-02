package osm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type primaryRow struct {
	recovery bool
	address  *string
	err      error
}

func (r primaryRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*(destinations[0].(*bool)) = r.recovery
	*(destinations[1].(**string)) = r.address
	return nil
}

func TestPrometheusStoragePreflightAcceptsThresholdAndRechecksPrimary(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	server := storageMetricsServer(t, now, 1000, 10000)
	defer server.Close()
	address := "10.0.0.7"
	db := &preflightDB{rows: []primaryRow{{address: &address}, {address: &address}}}
	provider := PrometheusStoragePreflight{DB: db, HTTPClient: server.Client(), PrometheusURL: server.URL,
		PVCTemplate: "pgdata-{{POD}}", MaximumAge: 2 * time.Minute, Now: func() time.Time { return now }}
	observation, err := provider.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.FreeBytes != 1000 || observation.CapacityBytes != 10000 || observation.RequiredFreeBytes != 1000 || observation.NodeName != "k8s4" || !observation.SampledAt.Equal(now) || db.calls != 2 {
		t.Fatalf("unexpected observation %#v with %d SQL calls", observation, db.calls)
	}
}

func TestPrometheusStoragePreflightBlocksBelowThreshold(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	server := storageMetricsServer(t, now, 999, 10000)
	defer server.Close()
	address := "10.0.0.7"
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{address: &address}, {address: &address}}},
		HTTPClient: server.Client(), PrometheusURL: server.URL, PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute, Now: func() time.Time { return now }}
	_, err := provider.Check(context.Background())
	var blocked *StoragePreflightError
	if !errors.As(err, &blocked) || blocked.Code != StorageInsufficientSpace || blocked.Observation.RequiredFreeBytes != 1000 {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestPrometheusStoragePreflightRetriesTransientMetricQueries(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	server := storageMetricsServer(t, now, 1000, 10000)
	defer server.Close()
	attempts := 0
	transport := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if attempts <= 4 {
			return nil, &net.DNSError{Err: "temporary failure", Name: request.URL.Hostname(), IsTemporary: true}
		}
		return transport.RoundTrip(request)
	})}
	address := "10.0.0.7"
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{address: &address}, {address: &address}}},
		HTTPClient: client, PrometheusURL: server.URL, PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute,
		RetryDelays: []time.Duration{0, 0, 0, 0}, Now: func() time.Time { return now }}
	if _, err := provider.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 10 {
		t.Fatalf("HTTP attempts=%d, want 10 (four retries plus six successful metric queries)", attempts)
	}
}

func TestDefaultPrometheusRetryDelays(t *testing.T) {
	want := []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second}
	if len(prometheusRetryDelays) != len(want) {
		t.Fatalf("retry delays=%v, want %v", prometheusRetryDelays, want)
	}
	for index := range want {
		if prometheusRetryDelays[index] != want[index] {
			t.Fatalf("retry delays=%v, want %v", prometheusRetryDelays, want)
		}
	}
}

func TestPrometheusStoragePreflightRetriesMissingPrimaryMapping(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	base := storageMetricsServer(t, now, 1000, 10000)
	defer base.Close()
	attempts := 0
	server := mappingRetryServer(t, base, 4, &attempts)
	defer server.Close()
	address := "10.0.0.7"
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{address: &address}, {address: &address}}},
		HTTPClient: server.Client(), PrometheusURL: server.URL, PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute,
		RetryDelays: []time.Duration{0, 0, 0, 0}, Now: func() time.Time { return now }}
	if _, err := provider.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 5 {
		t.Fatalf("mapping attempts=%d, want 5", attempts)
	}
}

func TestPrometheusStoragePreflightExhaustsMissingPrimaryMappingRetries(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	base := storageMetricsServer(t, now, 1000, 10000)
	defer base.Close()
	attempts := 0
	server := mappingRetryServer(t, base, 5, &attempts)
	defer server.Close()
	address := "10.0.0.7"
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{address: &address}}},
		HTTPClient: server.Client(), PrometheusURL: server.URL, PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute,
		RetryDelays: []time.Duration{0, 0, 0, 0}, Now: func() time.Time { return now }}
	_, err := provider.Check(context.Background())
	var blocked *StoragePreflightError
	if !errors.As(err, &blocked) || blocked.Code != StorageMappingMissing {
		t.Fatalf("unexpected error %v", err)
	}
	if attempts != 5 {
		t.Fatalf("mapping attempts=%d, want 5", attempts)
	}
}

func TestPrometheusStoragePreflightRejectsPrimaryChange(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	server := storageMetricsServer(t, now, 1000, 10000)
	defer server.Close()
	first, second := "10.0.0.7", "10.0.0.8"
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{address: &first}, {address: &second}}},
		HTTPClient: server.Client(), PrometheusURL: server.URL, PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute, Now: func() time.Time { return now }}
	_, err := provider.Check(context.Background())
	var blocked *StoragePreflightError
	if !errors.As(err, &blocked) || blocked.Code != StoragePrimaryChanged {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestPrometheusStoragePreflightRejectsStaleSourceSample(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	server := storageMetricsServer(t, now.Add(-2*time.Minute), 1000, 10000)
	defer server.Close()
	address := "10.0.0.7"
	requests := 0
	transport := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return transport.RoundTrip(request)
	})}
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{address: &address}, {address: &address}}},
		HTTPClient: client, PrometheusURL: server.URL, PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute, Now: func() time.Time { return now }}
	_, err := provider.Check(context.Background())
	var blocked *StoragePreflightError
	if !errors.As(err, &blocked) || blocked.Code != StorageMetricStale {
		t.Fatalf("unexpected error %v", err)
	}
	if requests != 2 {
		t.Fatalf("HTTP requests=%d, want 2 without semantic retries", requests)
	}
}

func TestPrometheusStoragePreflightPreservesCancellation(t *testing.T) {
	provider := PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{err: context.Canceled}}}, HTTPClient: http.DefaultClient,
		PrometheusURL: "https://prometheus.example", PVCTemplate: "pgdata-{{POD}}", MaximumAge: time.Minute}
	_, err := provider.Check(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}

type preflightDB struct {
	rows  []primaryRow
	calls int
}

func (d *preflightDB) QueryRow(context.Context, string, ...any) pgx.Row {
	row := d.rows[d.calls]
	d.calls++
	return row
}

func storageMetricsServer(t *testing.T, at time.Time, available, capacity int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		var result []any
		switch {
		case query == "pg_replication_is_replica":
			result = []any{map[string]any{"metric": map[string]string{"__name__": "pg_replication_is_replica", "instance": "10.0.0.7:9187", "namespace": "database", "pod": "postgres-1"}, "value": []any{float64(at.Unix()), "0"}}}
		case query == "timestamp(pg_replication_is_replica)":
			result = []any{map[string]any{"metric": map[string]string{"instance": "10.0.0.7:9187", "namespace": "database", "pod": "postgres-1"}, "value": []any{float64(at.Unix()), strconv.FormatInt(at.Unix(), 10)}}}
		case strings.HasPrefix(query, "kubelet_volume_stats_available_bytes") && strings.Contains(query, `persistentvolumeclaim="pgdata-postgres-1"`):
			result = []any{map[string]any{"metric": map[string]string{"__name__": "kubelet_volume_stats_available_bytes", "namespace": "database", "node": "k8s4", "persistentvolumeclaim": "pgdata-postgres-1"}, "value": []any{float64(at.Unix()), stringInt(available)}}}
		case strings.HasPrefix(query, "timestamp(kubelet_volume_stats_available_bytes") && strings.Contains(query, `persistentvolumeclaim="pgdata-postgres-1"`):
			result = []any{map[string]any{"metric": map[string]string{"namespace": "database", "node": "k8s4", "persistentvolumeclaim": "pgdata-postgres-1"}, "value": []any{float64(at.Unix()), strconv.FormatInt(at.Unix(), 10)}}}
		case strings.HasPrefix(query, "kubelet_volume_stats_capacity_bytes") && strings.Contains(query, `persistentvolumeclaim="pgdata-postgres-1"`):
			result = []any{map[string]any{"metric": map[string]string{"__name__": "kubelet_volume_stats_capacity_bytes", "namespace": "database", "node": "k8s4", "persistentvolumeclaim": "pgdata-postgres-1"}, "value": []any{float64(at.Unix()), stringInt(capacity)}}}
		case strings.HasPrefix(query, "timestamp(kubelet_volume_stats_capacity_bytes") && strings.Contains(query, `persistentvolumeclaim="pgdata-postgres-1"`):
			result = []any{map[string]any{"metric": map[string]string{"namespace": "database", "node": "k8s4", "persistentvolumeclaim": "pgdata-postgres-1"}, "value": []any{float64(at.Unix()), strconv.FormatInt(at.Unix(), 10)}}}
		default:
			t.Errorf("unexpected query %q", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	}))
}

func mappingRetryServer(t *testing.T, base *httptest.Server, missingAttempts int, attempts *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		if query == "pg_replication_is_replica" {
			*attempts++
		}
		if (query == "pg_replication_is_replica" || query == "timestamp(pg_replication_is_replica)") && *attempts <= missingAttempts {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
			return
		}
		response, err := base.Client().Get(base.URL + request.URL.RequestURI())
		if err != nil {
			t.Errorf("proxy Prometheus request: %v", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
}

func stringInt(value int64) string { return strconv.FormatInt(value, 10) }
