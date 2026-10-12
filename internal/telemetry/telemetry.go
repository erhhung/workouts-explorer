package telemetry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"google.golang.org/grpc/credentials"
)

const DefaultEndpoint = "grpcs://otel-gateway-collector.observability.svc.cluster.local:4317"

func Setup(ctx context.Context, serviceName, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	parsed, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	// Read the environment at setup time because resource.Default is cached.
	// Retain the default schema rather than pinning a conflicting semconv schema.
	envResource, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), envResource)
	if err != nil {
		return nil, err
	}
	var traceExporter *otlptrace.Exporter
	var metricExporter metric.Exporter
	switch parsed.Scheme {
	case "http", "https":
		traceExporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
		if err == nil {
			metricExporter, err = otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(endpoint))
		}
	case "grpc", "grpcs":
		traceOptions := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(parsed.Host)}
		metricOptions := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(parsed.Host)}
		if parsed.Scheme == "grpcs" {
			roots, rootErr := x509.SystemCertPool()
			if rootErr != nil {
				return nil, fmt.Errorf("load OTLP system certificate pool: %w", rootErr)
			}
			creds := credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
			traceOptions = append(traceOptions, otlptracegrpc.WithTLSCredentials(creds))
			metricOptions = append(metricOptions, otlpmetricgrpc.WithTLSCredentials(creds))
		} else {
			// Plaintext is only selected explicitly for a validated loopback endpoint.
			traceOptions = append(traceOptions, otlptracegrpc.WithInsecure())
			metricOptions = append(metricOptions, otlpmetricgrpc.WithInsecure())
		}
		traceExporter, err = otlptracegrpc.New(ctx, traceOptions...)
		if err == nil {
			metricExporter, err = otlpmetricgrpc.New(ctx, metricOptions...)
		}
	}
	if err != nil {
		if traceExporter != nil {
			err = errors.Join(err, traceExporter.Shutdown(context.Background()))
		}
		return nil, err
	}
	tracerProvider := trace.NewTracerProvider(trace.WithBatcher(traceExporter), trace.WithResource(res))
	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(identityExporter{metricExporter}, metric.WithInterval(30*time.Second))),
	)
	shutdown := func(shutdownCtx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(shutdownCtx), meterProvider.Shutdown(shutdownCtx))
	}
	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return nil, errors.Join(err, shutdown(context.Background()))
	}
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return shutdown, nil
}

func parseEndpoint(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid OTLP endpoint URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "grpc" && parsed.Scheme != "grpcs" {
		return nil, fmt.Errorf("OTLP endpoint must use http, https, grpc, or grpcs")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(endpoint, "#") {
		return nil, fmt.Errorf("OTLP endpoint must have a host without user information, query, or fragment")
	}
	isGRPC := parsed.Scheme == "grpc" || parsed.Scheme == "grpcs"
	if isGRPC && parsed.Path != "" {
		return nil, fmt.Errorf("OTLP gRPC endpoint must not contain a path")
	}
	if isGRPC || parsed.Port() != "" || strings.HasSuffix(parsed.Host, ":") {
		host, port, err := net.SplitHostPort(parsed.Host)
		if err != nil || host == "" {
			return nil, fmt.Errorf("OTLP endpoint must have an explicit host and port")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, fmt.Errorf("OTLP endpoint port must be between 1 and 65535")
		}
	}
	if parsed.Scheme == "grpc" {
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("plaintext OTLP gRPC is only allowed on loopback; use grpcs for remote collectors")
		}
	}
	return parsed, nil
}
