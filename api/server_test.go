package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	db, err := pgxpool.New(context.Background(), "postgresql://127.0.0.1:1/unavailable?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	handler, err := NewHandler(config.API{PollingIntervalSeconds: 30, MapFitPaddingPixels: 48}, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestPublicEndpoints(t *testing.T) {
	handler := testHandler(t)
	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/api/config", "application/json", "Workouts Explorer"},
		{"/api/openapi.yaml", "application/yaml", "openapi: 3.0.3"},
		{"/swagger", "text/html", "/api/openapi.yaml"},
		{"/health/live", "application/json", `"status":"ok"`},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("unexpected response headers/body: %v %q", response.Header(), response.Body.String())
			}
			if response.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing request ID")
			}
		})
	}
}

func TestSwaggerAssetsAreEmbeddedWithCSP(t *testing.T) {
	handler := testHandler(t)
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/swagger", nil))
	if index.Code != http.StatusOK || strings.Contains(index.Body.String(), "unpkg.com") || strings.Contains(index.Body.String(), `src="http`) || strings.Contains(index.Body.String(), `href="http`) {
		t.Fatalf("Swagger index is not self-hosted: status=%d", index.Code)
	}
	csp := index.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sha256-") || strings.Contains(csp, "unsafe-inline") {
		t.Fatal("Swagger response is missing restrictive CSP")
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/swagger/swagger-ui.css", nil))
	if asset.Code != http.StatusOK || !strings.HasPrefix(asset.Header().Get("Content-Type"), "text/css") || asset.Body.Len() == 0 {
		t.Fatalf("embedded Swagger asset failed: status=%d content-type=%q", asset.Code, asset.Header().Get("Content-Type"))
	}
}

func TestSessionPlaceholderUsesProblemDetails(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/session-tokens", strings.NewReader(`{"username":"someone","password":"not-a-real-password"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	var problem generated.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.RequestId == "" {
		t.Fatalf("invalid Problem Details: %v %#v", err, problem)
	}
}

func TestRequestValidationUsesProblemDetails(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/session-tokens", strings.NewReader(`{"username":"someone","password":"not-a-real-password","unexpected":true}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestOversizedRequestUsesProblemDetails(t *testing.T) {
	handler := testHandler(t)
	for _, chunked := range []bool{false, true} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/session-tokens", strings.NewReader(strings.Repeat("x", int(config.MaxRequestBodyBytes)+1)))
		request.Header.Set("Content-Type", "application/json")
		if chunked {
			request.ContentLength = -1
		}
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge || response.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("chunked=%t response=%d %s", chunked, response.Code, response.Body.String())
		}
		var problem generated.Problem
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Status != http.StatusRequestEntityTooLarge || problem.RequestId == "" {
			t.Fatalf("invalid oversized Problem Details: %v %#v", err, problem)
		}
	}
}

func TestReadinessFailsClosedWithoutDatabase(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestRequestLogsContainCorrelationButNoSensitiveInput(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	db, err := pgxpool.New(context.Background(), "postgresql://127.0.0.1:1/unavailable?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	prior := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prior); _ = provider.Shutdown(context.Background()) })
	handler, err := NewHandler(config.API{PollingIntervalSeconds: 30, MapFitPaddingPixels: 48}, db, logger)
	if err != nil {
		t.Fatal(err)
	}
	secret := "sensitive-marker-must-not-appear"
	request := httptest.NewRequest(http.MethodPost, "/api/session-tokens?private="+secret, strings.NewReader(`{"username":"someone","password":"`+secret+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	logOutput := output.String()
	if !strings.Contains(logOutput, `"request_id"`) || !strings.Contains(logOutput, `"trace_id"`) || !strings.Contains(logOutput, `"span_id"`) {
		t.Fatalf("request log lacks correlation fields: %s", logOutput)
	}
	if strings.Contains(logOutput, secret) || strings.Contains(strings.ToLower(logOutput), "authorization") || strings.Contains(strings.ToLower(logOutput), "password") {
		t.Fatalf("request log contains sensitive input: %s", logOutput)
	}
}
