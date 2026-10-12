package telemetry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	metricscollector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourceproto "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestSetupDisabled(t *testing.T) {
	meter, tracer, propagator := otel.GetMeterProvider(), otel.GetTracerProvider(), otel.GetTextMapPropagator()
	shutdown, err := Setup(context.Background(), "disabled", "")
	if err != nil || shutdown == nil {
		t.Fatalf("disabled setup: shutdown=%v err=%v", shutdown != nil, err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if otel.GetMeterProvider() != meter || otel.GetTracerProvider() != tracer || otel.GetTextMapPropagator() != propagator {
		t.Fatal("disabled setup changed global providers")
	}
}

func TestParseEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		DefaultEndpoint, "grpcs://collector:4317", "grpcs://[::1]:4317",
		"grpc://localhost:4317", "grpc://127.0.0.1:4317", "grpc://[::1]:4317",
		"http://collector:4328", "https://collector:4318", "https://collector",
		"http://collector:4328/otlp", "http://collector:4328/",
	} {
		t.Run(endpoint, func(t *testing.T) {
			parsed, err := parseEndpoint(endpoint)
			if err != nil || parsed.String() != endpoint {
				t.Fatalf("parse valid endpoint: %v, %v", parsed, err)
			}
		})
	}
	for _, endpoint := range []string{
		"collector:4317", "//collector:4317", "ftp://collector:4317", "grpcs:collector:4317",
		"grpcs://", "grpcs://:4317", "grpcs://collector", "grpcs://collector:",
		"grpcs://collector:0", "grpcs://collector:65536", "grpcs://collector:abc", "grpcs://[::1",
		"grpcs://user@collector:4317", "grpcs://user:password@collector:4317",
		"grpcs://collector:4317/", "grpcs://collector:4317/v1/traces", "grpcs://collector:4317/%2f",
		"grpcs://collector:4317?x=1", "grpcs://collector:4317?", "grpcs://collector:4317#fragment", "grpcs://collector:4317#",
		"grpc://collector:4317", "grpc://otel-gateway-collector.observability.svc.cluster.local:4317", "grpc://10.0.0.1:4317",
		"http://user@collector:4328", "https://collector?x=1", "https://collector#fragment", "http://collector:0",
		"grpcs://bad host:4317", "grpcs://collector:4317\n",
	} {
		t.Run(endpoint, func(t *testing.T) {
			meter, tracer := otel.GetMeterProvider(), otel.GetTracerProvider()
			if shutdown, err := Setup(context.Background(), "invalid", endpoint); err == nil || shutdown != nil {
				t.Fatalf("invalid endpoint accepted: shutdown=%v err=%v", shutdown != nil, err)
			}
			if otel.GetMeterProvider() != meter || otel.GetTracerProvider() != tracer {
				t.Fatal("invalid endpoint changed global providers")
			}
		})
	}
}

type testMetricsServer struct {
	metricscollector.UnimplementedMetricsServiceServer
	requests chan *metricscollector.ExportMetricsServiceRequest
}

func (s *testMetricsServer) Export(_ context.Context, request *metricscollector.ExportMetricsServiceRequest) (*metricscollector.ExportMetricsServiceResponse, error) {
	s.requests <- request
	return &metricscollector.ExportMetricsServiceResponse{}, nil
}

type testTraceServer struct {
	tracecollector.UnimplementedTraceServiceServer
	requests chan *tracecollector.ExportTraceServiceRequest
}

func (s *testTraceServer) Export(_ context.Context, request *tracecollector.ExportTraceServiceRequest) (*tracecollector.ExportTraceServiceResponse, error) {
	s.requests <- request
	return &tracecollector.ExportTraceServiceResponse{}, nil
}

func newTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OTLP test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func newTestServerCertificate(t *testing.T, hostname string, ca *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), DNSNames: []string{hostname},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func startTestGRPCCollector(t *testing.T, certificate *tls.Certificate) (string, *testMetricsServer, *testTraceServer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var options []grpc.ServerOption
	scheme := "grpc"
	if certificate != nil {
		scheme = "grpcs"
		options = append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{*certificate}, MinVersion: tls.VersionTLS12,
		})))
	}
	server := grpc.NewServer(options...)
	metrics := &testMetricsServer{requests: make(chan *metricscollector.ExportMetricsServiceRequest, 16)}
	traces := &testTraceServer{requests: make(chan *tracecollector.ExportTraceServiceRequest, 16)}
	metricscollector.RegisterMetricsServiceServer(server, metrics)
	tracecollector.RegisterTraceServiceServer(server, traces)
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(server.Stop)
	return scheme + "://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), metrics, traces
}

func preserveTestProviders(t *testing.T) {
	t.Helper()
	meter, tracer, propagator := otel.GetMeterProvider(), otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetMeterProvider(meter)
		otel.SetTracerProvider(tracer)
		otel.SetTextMapPropagator(propagator)
	})
}

func assertServiceResource(t *testing.T, res *resourceproto.Resource, service string) {
	t.Helper()
	for _, attr := range res.GetAttributes() {
		if attr.Key == "service.name" && attr.Value.GetStringValue() == service {
			return
		}
	}
	t.Fatalf("missing service.name=%q: %v", service, res)
}

func TestSetupGRPCLoopback(t *testing.T) {
	endpoint, metrics, traces := startTestGRPCCollector(t, nil)
	preserveTestProviders(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown, err := Setup(ctx, "loopback-fixture", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	_, span := otel.Tracer("telemetry-test").Start(ctx, "local export")
	span.End()
	if err := otel.GetTracerProvider().(*sdktrace.TracerProvider).ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := otel.GetMeterProvider().(*sdkmetric.MeterProvider).ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(metrics.requests) == 0 || len(traces.requests) == 0 {
		t.Fatal("missing loopback gRPC exports")
	}
}

func TestSetupGRPCTLS(t *testing.T) {
	// SystemCertPool caches process-wide roots. Isolate SSL_CERT_FILE from other tests.
	if os.Getenv("WORKOUTS_TELEMETRY_TLS_TEST") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSetupGRPCTLS$", "-test.v")
		command.Env = append(os.Environ(), "WORKOUTS_TELEMETRY_TLS_TEST=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("TLS fixture tests: %v\n%s", err, output)
		}
		return
	}
	ca, caKey := newTestCA(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)
	// Explicit TLS credentials must also override exporter insecure environment settings.
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_INSECURE", "true")
	certificate := newTestServerCertificate(t, "localhost", ca, caKey)
	for _, service := range []string{"workouts-api", "workouts-worker", "workouts-coverage-worker"} {
		for _, limited := range []bool{false, true} {
			name := service + "/unlimited"
			limit := int64(math.MaxInt64)
			if limited {
				name = service + "/finite-memory-limit"
				limit = 1 << 30
			}
			t.Run(name, func(t *testing.T) {
				priorLimit := debug.SetMemoryLimit(limit)
				t.Cleanup(func() { debug.SetMemoryLimit(priorLimit) })
				endpoint, metrics, traces := startTestGRPCCollector(t, &certificate)
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
				_, span := otel.Tracer("telemetry-test").Start(ctx, "TLS export")
				span.End()
				if err := otel.GetTracerProvider().(*sdktrace.TracerProvider).ForceFlush(ctx); err != nil {
					t.Fatal(err)
				}
				if err := otel.GetMeterProvider().(*sdkmetric.MeterProvider).ForceFlush(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case request := <-traces.requests:
					if len(request.ResourceSpans) != 1 {
						t.Fatalf("unexpected resource spans: %v", request)
					}
					assertServiceResource(t, request.ResourceSpans[0].Resource, service)
					if request.ResourceSpans[0].SchemaUrl != resource.Default().SchemaURL() {
						t.Fatal("trace resource schema changed")
					}
				case <-ctx.Done():
					t.Fatal("no TLS trace export received")
				}
				select {
				case request := <-metrics.requests:
					seen := make(map[string]bool)
					for _, res := range request.ResourceMetrics {
						assertServiceResource(t, res.Resource, service)
						if res.SchemaUrl != resource.Default().SchemaURL() {
							t.Fatal("metric resource schema changed")
						}
						for _, scope := range res.ScopeMetrics {
							for _, metric := range scope.Metrics {
								seen[metric.Name] = true
								if metric.Name == "go.memory.limit" {
									points := metric.GetSum().GetDataPoints()
									if len(points) != 1 || points[0].GetAsInt() != limit {
										t.Fatalf("unexpected memory limit: %v", metric)
									}
								}
							}
						}
					}
					for _, name := range []string{"go.memory.used", "go.memory.allocated", "go.memory.allocations", "go.memory.gc.goal", "go.goroutine.count", "go.processor.limit", "go.config.gogc"} {
						if !seen[name] {
							t.Errorf("missing runtime metric %q: %v", name, seen)
						}
					}
					if seen["go.memory.limit"] != limited {
						t.Errorf("memory-limit metric present=%t, want %t", seen["go.memory.limit"], limited)
					}
				case <-ctx.Done():
					t.Fatal("no TLS metric export received")
				}
			})
		}
	}
	wrongCA, wrongCAKey := newTestCA(t)
	for _, test := range []struct {
		name string
		cert tls.Certificate
	}{
		{"wrong hostname", newTestServerCertificate(t, "collector.invalid", ca, caKey)},
		{"untrusted CA", newTestServerCertificate(t, "localhost", wrongCA, wrongCAKey)},
		{"plaintext server", tls.Certificate{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			certificate := &test.cert
			if test.name == "plaintext server" {
				certificate = nil
			}
			endpoint, metrics, traces := startTestGRPCCollector(t, certificate)
			if certificate == nil {
				endpoint = "grpcs" + endpoint[len("grpc"):]
			}
			preserveTestProviders(t)
			shutdown, err := Setup(context.Background(), "invalid-TLS", endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = shutdown(ctx)
			}()
			_, span := otel.Tracer("telemetry-test").Start(context.Background(), "rejected TLS")
			span.End()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := otel.GetTracerProvider().(*sdktrace.TracerProvider).ForceFlush(ctx); err == nil {
				t.Error("trace export accepted invalid TLS")
			}
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			if err := otel.GetMeterProvider().(*sdkmetric.MeterProvider).ForceFlush(ctx); err == nil {
				t.Error("metric export accepted invalid TLS")
			}
			cancel()
			if len(metrics.requests) != 0 || len(traces.requests) != 0 {
				t.Fatal("invalid TLS fell through to a successful export")
			}
		})
	}
}
