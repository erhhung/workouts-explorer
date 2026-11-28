package osm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStorageUsageObservesActualRolesAndPerNodeDatabaseSizes(t *testing.T) {
	for _, scenario := range []string{"normal", "failover", "requested", "missing-request", "stale-request", "duplicate-request", "missing-database", "database-ahead", "stale-volume", "duplicate-volume", "no-primary", "multiple-primaries"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				query := r.URL.Query().Get("query")
				timestamp := strings.HasPrefix(query, "timestamp(")
				query = strings.TrimSuffix(strings.TrimPrefix(query, "timestamp("), ")")
				metric := strings.Split(query, "{")[0]
				var values []map[string]any
				emit := func(labels map[string]string, value int64) {
					sampleTime := now
					if scenario == "stale-volume" && metric == "kubelet_volume_stats_used_bytes" {
						sampleTime = now.Add(-3 * time.Minute)
					}
					if scenario == "stale-request" && metric == "kube_persistentvolumeclaim_resource_requests_storage_bytes" {
						sampleTime = now.Add(-3 * time.Minute)
					}
					if timestamp {
						value = sampleTime.Unix()
					}
					values = append(values, map[string]any{"metric": labels, "value": []any{now.Unix(), strconv.FormatInt(value, 10)}})
				}
				for i, pod := range []string{"pg-0", "pg-1", "pg-2"} {
					labels := map[string]string{"__name__": metric, "namespace": "postgresql", "pod": pod, "service": "pg-metrics", "job": "pg"}
					pvc := "data-" + pod
					switch metric {
					case "kube_pod_spec_volumes_persistentvolumeclaims_info":
						if strings.Contains(query, `persistentvolumeclaim="data-pg-0"`) && i != 0 {
							continue
						}
						labels["persistentvolumeclaim"] = pvc
						emit(labels, 1)
					case "pg_replication_is_replica":
						if strings.Contains(query, `pod="pg-0"`) && i != 0 {
							continue
						}
						replica := int64(1)
						primary := 0
						if scenario == "failover" {
							primary = 1
						}
						if i == primary && scenario != "no-primary" {
							replica = 0
						}
						if scenario == "multiple-primaries" && i == 1 {
							replica = 0
						}
						emit(labels, replica)
					case "pg_database_size_bytes":
						if scenario == "missing-database" && i == 1 {
							continue
						}
						labels["datname"] = "osm"
						size := int64(10-i) * (1 << 30)
						if scenario == "database-ahead" && i == 0 {
							size = 49 * (1 << 30)
						}
						emit(labels, size)
					default:
						if metric == "kube_persistentvolumeclaim_resource_requests_storage_bytes" && scenario == "missing-request" {
							continue
						}
						labels = map[string]string{"__name__": metric, "namespace": "postgresql", "persistentvolumeclaim": pvc, "node": pod}
						value := int64(50) * (1 << 30)
						if scenario == "requested" && metric == "kubelet_volume_stats_capacity_bytes" {
							value -= 64 * (1 << 20)
						}
						if metric == "kubelet_volume_stats_used_bytes" {
							value = 36 * (1 << 30)
						}
						if metric == "kubelet_volume_stats_available_bytes" {
							value = 14 * (1 << 30)
							if scenario == "requested" {
								value -= 64 * (1 << 20)
							}
						}
						emit(labels, value)
						if scenario == "duplicate-volume" && i == 0 {
							emit(labels, value)
						}
						if scenario == "duplicate-request" && metric == "kube_persistentvolumeclaim_resource_requests_storage_bytes" {
							emit(labels, value)
						}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": values}})
			}))
			defer server.Close()
			reader := StorageUsageReader{PrometheusURL: server.URL, SelectorLabels: map[string]string{"namespace": "postgresql", "persistentvolumeclaim": "data-pg-0"}, Databases: []string{"osm"}, HTTPClient: server.Client(), Now: func() time.Time { return now }}
			value, err := reader.Read(context.Background())
			if scenario == "stale-volume" || scenario == "duplicate-volume" || scenario == "no-primary" || scenario == "multiple-primaries" {
				if err == nil {
					t.Fatal("invalid observations admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			first := "pg-0"
			if scenario == "failover" {
				first = "pg-1"
			}
			if !value.Configured || len(value.Nodes) != 3 || value.Nodes[0].Pod != first || value.Nodes[0].Role != "primary" {
				t.Fatalf("incorrect current primary/membership: %+v", value)
			}
			for _, node := range value.Nodes {
				capacity, available := int64(50*(1<<30)), int64(14*(1<<30))
				if scenario == "requested" {
					capacity -= 64 * (1 << 20)
					available -= 64 * (1 << 20)
				}
				if node.CapacityBytes != capacity || node.UsedBytes != 36*(1<<30) || node.AvailableBytes != available {
					t.Fatalf("incorrect filesystem accounting: %+v", node)
				}
				if scenario == "missing-request" || scenario == "stale-request" || scenario == "duplicate-request" {
					if node.RequestedCapacityBytes != nil {
						t.Fatal("unavailable request size was fabricated")
					}
				} else if node.RequestedCapacityBytes == nil || *node.RequestedCapacityBytes != 50*(1<<30) {
					t.Fatalf("incorrect requested size: %+v", node)
				}
				unavailable := (scenario == "missing-database" && node.Pod == "pg-1") || (scenario == "database-ahead" && node.Pod == "pg-0")
				if unavailable {
					if node.ApplicationBytes != nil || node.Issue == "" {
						t.Fatal("missing/inconsistent database sizes presented as real zeroes")
					}
					continue
				}
				expected := map[string]int64{"pg-0": 10 * (1 << 30), "pg-1": 9 * (1 << 30), "pg-2": 8 * (1 << 30)}[node.Pod]
				if node.ApplicationBytes == nil || *node.ApplicationBytes != expected {
					t.Fatalf("per-node size replaced by another node's value: %+v", node)
				}
			}
			previous := calls
			if _, err := reader.Read(context.Background()); err != nil || calls != previous {
				t.Fatal("storage snapshot was not cached")
			}
		})
	}
}

func TestStorageUsageDisabledNeedsNoExternalConnection(t *testing.T) {
	reader := StorageUsageReader{}
	value, err := reader.Read(context.Background())
	if err != nil || value.Configured || value.Nodes == nil {
		t.Fatalf("disabled storage read: %+v %v", value, err)
	}
}
