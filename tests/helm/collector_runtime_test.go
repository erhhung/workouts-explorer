package helm_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/proto"
)

type metricsSink struct {
	collectorpb.UnimplementedMetricsServiceServer
	requests chan *collectorpb.ExportMetricsServiceRequest
}

func (s *metricsSink) Export(_ context.Context, request *collectorpb.ExportMetricsServiceRequest) (*collectorpb.ExportMetricsServiceResponse, error) {
	select {
	case s.requests <- request:
	default:
	}
	return &collectorpb.ExportMetricsServiceResponse{}, nil
}

// This opt-in test exercises the vendor binary, not a custom application proxy.
func TestCollectorNativeAndSelfExport(t *testing.T) {
	binary := os.Getenv("OTELCOL_CONTRIB_BINARY")
	if binary == "" {
		t.Skip("set OTELCOL_CONTRIB_BINARY to the pinned 0.156.0 binary for export verification")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "version 0.156.0") {
		t.Fatalf("expected vendor version 0.156.0: %v %s", err, version)
	}
	for _, scheme := range []string{"grpc", "grpcs", "http", "https"} {
		for _, component := range []string{"ui", "tiles"} {
			t.Run(scheme+"/"+component, func(t *testing.T) {
				sink := &metricsSink{requests: make(chan *collectorpb.ExportMetricsServiceRequest, 128)}
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/metrics" {
						http.NotFound(w, r)
						return
					}
					var body io.Reader = r.Body
					if r.Header.Get("Content-Encoding") == "gzip" {
						gz, err := gzip.NewReader(r.Body)
						if err != nil {
							http.Error(w, err.Error(), 400)
							return
						}
						defer gz.Close()
						body = gz
					}
					data, err := io.ReadAll(body)
					request := new(collectorpb.ExportMetricsServiceRequest)
					if err != nil || proto.Unmarshal(data, request) != nil {
						http.Error(w, "invalid OTLP", 400)
						return
					}
					_, _ = sink.Export(r.Context(), request)
					w.Header().Set("Content-Type", "application/x-protobuf")
				})
				secure := scheme == "https" || scheme == "grpcs"
				server := httptest.NewUnstartedServer(handler)
				if secure {
					server.StartTLS()
				} else {
					server.Start()
				}
				defer server.Close()
				endpoint := server.URL
				if strings.HasPrefix(scheme, "grpc") {
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					var options []grpc.ServerOption
					if secure {
						options = append(options, grpc.Creds(credentials.NewTLS(server.TLS)))
					}
					grpcServer := grpc.NewServer(options...)
					collectorpb.RegisterMetricsServiceServer(grpcServer, sink)
					go func() { _ = grpcServer.Serve(listener) }()
					defer grpcServer.Stop()
					endpoint = scheme + "://" + listener.Addr().String()
				}
				items := renderCollectorChart(t, "otel.endpoint="+endpoint, "otel.environment=export-test", "otel.serviceNamespace=test-namespace")
				config := items["ConfigMap/workouts-explorer-"+component+"-collector"].Data["config.yaml"]
				if secure {
					ca := filepath.Join(t.TempDir(), "ca.crt")
					if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
						t.Fatal(err)
					}
					config = strings.ReplaceAll(config, "/var/run/secrets/workouts-otel/ca.crt", ca)
				}
				// Dynamic local ports and short intervals keep this fixture independent of a cluster.
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if component == "ui" && r.URL.Path == "/nginx_status" {
						_, _ = io.WriteString(w, "Active connections: 4\nserver accepts handled requests\n 20 20 30\nReading: 1 Writing: 1 Waiting: 2\n")
					} else if component == "tiles" && r.URL.Path == "/_/metrics" {
						w.Header().Set("Content-Type", "text/plain; version=0.0.4")
						_, _ = io.WriteString(w, `# HELP martin_http_requests_total Total HTTP requests
# TYPE martin_http_requests_total counter
martin_http_requests_total{endpoint="/{source_ids}/{z}/{x}/{y}",method="GET",status="200"} 2
# HELP martin_http_request_duration_secs HTTP request duration
# TYPE martin_http_request_duration_secs histogram
martin_http_request_duration_secs_bucket{endpoint="/{source_ids}/{z}/{x}/{y}",method="GET",status="200",le="0.1"} 1
martin_http_request_duration_secs_bucket{endpoint="/{source_ids}/{z}/{x}/{y}",method="GET",status="200",le="0.5"} 2
martin_http_request_duration_secs_bucket{endpoint="/{source_ids}/{z}/{x}/{y}",method="GET",status="200",le="+Inf"} 2
martin_http_request_duration_secs_sum{endpoint="/{source_ids}/{z}/{x}/{y}",method="GET",status="200"} 0.4
martin_http_request_duration_secs_count{endpoint="/{source_ids}/{z}/{x}/{y}",method="GET",status="200"} 2
`)
					} else {
						http.NotFound(w, r)
					}
				}))
				defer source.Close()
				config = strings.ReplaceAll(config, "127.0.0.1:8083", source.Listener.Addr().String())
				config = strings.ReplaceAll(config, "127.0.0.1:3000", source.Listener.Addr().String())
				for _, port := range []string{"13133", "8888"} {
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					localPort := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
					_ = listener.Close()
					config = strings.ReplaceAll(config, ":"+port, ":"+localPort)
					config = strings.ReplaceAll(config, "port: "+port, "port: "+localPort)
				}
				config = strings.ReplaceAll(config, "30s", "1s")
				config = strings.ReplaceAll(config, "timeout: 5s", "timeout: 1s")
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "--config="+path)
				cmd.Env = append(os.Environ(), "POD_UID=export-test-pod", "GOMEMLIMIT=192MiB")
				var logs bytes.Buffer
				cmd.Stdout, cmd.Stderr = &logs, &logs
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				stopped := false
				stop := func() {
					if !stopped {
						cancel()
						_ = cmd.Wait()
						stopped = true
					}
				}
				defer stop()
				wanted := map[string]bool{
					"otelcol_process_cpu_seconds":                    false,
					"otelcol_process_memory_rss":                     false,
					"otelcol_process_runtime_heap_alloc_bytes":       false,
					"otelcol_process_runtime_total_alloc_bytes":      false,
					"otelcol_process_runtime_total_sys_memory_bytes": false,
					"otelcol_process_uptime":                         false,
				}
				if component == "ui" {
					for _, name := range []string{"nginx.connections_accepted", "nginx.connections_handled", "nginx.connections_current", "nginx.requests"} {
						wanted[name] = false
					}
				} else {
					wanted["martin_http_requests_total"] = false
					wanted["martin_http_request_duration_secs"] = false
				}
				states := map[string]bool{}
				deadline := time.NewTimer(15 * time.Second)
				defer deadline.Stop()
				for {
					select {
					case request := <-sink.requests:
						for _, rm := range request.ResourceMetrics {
							resource := metricLabels(rm.Resource.Attributes)
							for _, scope := range rm.ScopeMetrics {
								for _, metric := range scope.Metrics {
									tracked := false
									for name := range wanted {
										tracked = tracked || strings.HasPrefix(metric.Name, name)
									}
									if !tracked {
										continue
									}
									self := strings.HasPrefix(metric.Name, "otelcol_")
									service := "workouts-" + component
									if self {
										service += "-collector"
									}
									if resource["service.name"] != service {
										t.Errorf("%s resource service identity = %v", metric.Name, resource)
									}
									if metric.Name == "martin_http_request_duration_secs" && metric.GetHistogram() == nil {
										t.Errorf("Martin duration must remain a histogram: %v", metric)
									}
									var points [][]*commonpb.KeyValue
									if sum := metric.GetSum(); sum != nil {
										if metric.Name == "martin_http_requests_total" && !sum.IsMonotonic {
											t.Error("Martin request count must remain a monotonic counter")
										}
										for _, p := range sum.DataPoints {
											points = append(points, p.Attributes)
										}
									}
									if gauge := metric.GetGauge(); gauge != nil {
										for _, p := range gauge.DataPoints {
											points = append(points, p.Attributes)
										}
									}
									if hist := metric.GetHistogram(); hist != nil {
										for _, p := range hist.DataPoints {
											points = append(points, p.Attributes)
											if metric.Name == "martin_http_request_duration_secs" && (p.Count != 2 || p.GetSum() != 0.4 || len(p.ExplicitBounds) != 2 || p.ExplicitBounds[0] != 0.1 || p.ExplicitBounds[1] != 0.5 || len(p.BucketCounts) != 3 || p.BucketCounts[0] != 1 || p.BucketCounts[1] != 1 || p.BucketCounts[2] != 0) {
												t.Errorf("Martin histogram changed: %v", p)
											}
										}
									}
									for _, attrs := range points {
										labels := metricLabels(attrs)
										if labels["service_name"] != service || labels["service_namespace"] != "test-namespace" || labels["service_instance_id"] != "export-test-pod" || labels["deployment_environment"] != "export-test" {
											t.Errorf("%s missing stable point identity: %v", metric.Name, labels)
										}
										if strings.HasPrefix(metric.Name, "martin_http") && (labels["martin_route"] != "/{source_ids}/{z}/{x}/{y}" || labels["endpoint"] != "" || labels["method"] != "GET" || labels["status"] != "200") {
											t.Errorf("Martin route/method/status labels changed: %v", labels)
										}
										if metric.Name == "nginx.connections_current" {
											states[labels["state"]] = true
										}
									}
									for name := range wanted {
										if strings.HasPrefix(metric.Name, name) && len(points) > 0 {
											wanted[name] = true
										}
									}
								}
							}
						}
						complete := true
						for _, seen := range wanted {
							complete = complete && seen
						}
						if complete {
							if component == "ui" {
								for _, state := range []string{"active", "reading", "writing", "waiting"} {
									if !states[state] {
										t.Errorf("nginx connection state %s is missing: %v", state, states)
									}
								}
							}
							return
						}
					case <-deadline.C:
						stop()
						t.Fatalf("metrics not exported: %v\n%s", wanted, &logs)
					}
				}
			})
		}
	}
}

func metricLabels(attributes []*commonpb.KeyValue) map[string]string {
	labels := make(map[string]string, len(attributes))
	for _, kv := range attributes {
		labels[kv.Key] = kv.Value.GetStringValue()
	}
	return labels
}
