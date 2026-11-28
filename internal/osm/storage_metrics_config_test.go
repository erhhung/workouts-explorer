package osm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStorageMetricsOptionalConfiguration(t *testing.T) {
	for _, env := range []map[string]string{{}, {"OSM_PROMETHEUS_URL": "https://metrics.example"}, {"OSM_STORAGE_METRICS_ENABLED": "true"}} {
		value, err := StorageMetricsFromEnv(func(key string) string { return env[key] })
		if err != nil || value.Enabled {
			t.Fatalf("unconfigured metrics were enabled: %#v %v", value, err)
		}
	}
	env := map[string]string{"OSM_STORAGE_METRICS_ENABLED": "true", "OSM_PROMETHEUS_URL": "https://metrics.example", "OSM_STORAGE_AVAILABLE_METRIC": "node_filesystem_avail_bytes", "OSM_STORAGE_CAPACITY_METRIC": "node_filesystem_size_bytes", "OSM_STORAGE_SELECTOR_LABELS_JSON": `{"instance":"outside-kubernetes:9100","mountpoint":"/srv/postgresql"}`}
	value, err := StorageMetricsFromEnv(func(key string) string { return env[key] })
	if err != nil || !value.Enabled || value.SelectorLabels["mountpoint"] != "/srv/postgresql" {
		t.Fatal(value, err)
	}
	env["OSM_STORAGE_AVAILABLE_METRIC"] = "up or vector(1)"
	if _, err := StorageMetricsFromEnv(func(key string) string { return env[key] }); err == nil {
		t.Fatal("arbitrary query accepted as metric name")
	}
}

func TestConfiguredStorageMetricsWithoutTopologyDiscovery(t *testing.T) {
	for _, fixture := range []struct {
		name, available, capacity string
		labels                    map[string]string
	}{
		{"node exporter", "node_filesystem_avail_bytes", "node_filesystem_size_bytes", map[string]string{"instance": "standalone:9100", "mountpoint": "/srv/postgresql"}},
		{"PVC", "kubelet_volume_stats_available_bytes", "kubelet_volume_stats_capacity_bytes", map[string]string{"namespace": "postgresql", "persistentvolumeclaim": "custom-volume"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			available := int64(1000)
			stale := false
			duplicate := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query().Get("query")
				if strings.Contains(query, "pg_replication") {
					t.Error("topology discovery was queried")
				}
				metric := fixture.available
				value := available
				if strings.Contains(query, fixture.capacity) {
					metric = fixture.capacity
					value = 10000
				}
				for name, label := range fixture.labels {
					if !strings.Contains(query, name+"=\""+label+"\"") {
						t.Error("configured selector missing", query)
					}
				}
				labels := map[string]string{}
				for key, value := range fixture.labels {
					labels[key] = value
				}
				labels["__name__"] = metric
				var sample any = strconv.FormatInt(value, 10)
				if strings.HasPrefix(query, "timestamp(") {
					delete(labels, "__name__")
					at := now
					if stale {
						at = now.Add(-time.Hour)
					}
					sample = strconv.FormatInt(at.Unix(), 10)
				}
				result := []any{map[string]any{"metric": labels, "value": []any{now.Unix(), sample}}}
				if duplicate {
					other := map[string]string{}
					for key, value := range labels {
						other[key] = value
					}
					other["device"] = "other"
					result = append(result, map[string]any{"metric": other, "value": []any{now.Unix(), sample}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
			}))
			defer server.Close()
			provider := func() PrometheusStoragePreflight {
				return PrometheusStoragePreflight{DB: &preflightDB{rows: []primaryRow{{}, {}}}, HTTPClient: server.Client(), PrometheusURL: server.URL, AvailableMetric: fixture.available, CapacityMetric: fixture.capacity, SelectorLabels: fixture.labels, MaximumAge: time.Minute, Now: func() time.Time { return now }}
			}
			observation, err := provider().Check(context.Background())
			if err != nil || observation.FreeBytes != 1000 || observation.CapacityBytes != 10000 {
				t.Fatal(observation, err)
			}
			checkError := func(code StoragePreflightCode) {
				t.Helper()
				_, err := provider().Check(context.Background())
				var failure *StoragePreflightError
				if !errors.As(err, &failure) || failure.Code != code {
					t.Fatalf("wanted %s, got %v", code, err)
				}
			}
			available = 999
			checkError(StorageInsufficientSpace)
			available = 1000
			stale = true
			checkError(StorageMetricStale)
			stale = false
			duplicate = true
			checkError(StorageMetricAmbiguous)
		})
	}
}
