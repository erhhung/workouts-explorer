package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	handler, err := NewHandlerContext(ctx, testAPIConfig(t), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func testAPIConfig(t *testing.T) config.API {
	t.Helper()
	keyring := t.TempDir() + "/keyring.json"
	if err := os.WriteFile(keyring, []byte(`{"activeKeyId":"test","keys":{"test":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.API{Common: config.Common{SourceKeyringFile: keyring, LocalSourceRoots: []string{"/data/workouts"}}, PollingIntervalSeconds: 30, MapFitPaddingPixels: 48}
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

func TestPublicConfigExposesEffectiveValidationPolicy(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var public generated.PublicConfig
	if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public.PasswordMinimumLength != 12 || public.PageSizeMaximum != 100 {
		t.Fatalf("public policy passwordMinimum=%d pageMaximum=%d", public.PasswordMinimumLength, public.PageSizeMaximum)
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

func TestUnavailableAuthenticationUsesProblemDetails(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/session-tokens", strings.NewReader(`{"username":"someone","password":"not-a-real-password"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/problem+json" {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, err := NewHandlerContext(ctx, testAPIConfig(t), db, logger)
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

func TestAccessLogSuppressesSuccessfulHealthRequests(t *testing.T) {
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/health/live", http.StatusOK},
		{"/health/ready", http.StatusNoContent},
	} {
		t.Run(test.path, func(t *testing.T) {
			var output bytes.Buffer
			handler := accessLog(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))

			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, test.path, nil))
			if output.Len() != 0 {
				t.Fatalf("successful health request was logged: %s", output.String())
			}
		})
	}
}

func TestAccessLogImplicitWriteUsesOKAndSuppressesExactHealth(t *testing.T) {
	for _, path := range []string{"/health/live", "/health/ready"} {
		t.Run(path, func(t *testing.T) {
			var output bytes.Buffer
			handler := accessLog(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok"))
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.String() != "ok" {
				t.Fatalf("response status=%d body=%q", response.Code, response.Body.String())
			}
			if output.Len() != 0 {
				t.Fatalf("implicitly successful health request was logged: %s", output.String())
			}
		})
	}
}

func TestStatusRecorderImplicitWriteAndUnwrap(t *testing.T) {
	response := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
	if _, err := recorder.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	recorder.WriteHeader(http.StatusServiceUnavailable)

	if recorder.status != http.StatusOK || !recorder.wroteHeader {
		t.Fatalf("recorded status=%d wroteHeader=%t", recorder.status, recorder.wroteHeader)
	}
	if response.Code != http.StatusOK || response.Body.String() != "body" {
		t.Fatalf("response status=%d body=%q", response.Code, response.Body.String())
	}
	if recorder.Unwrap() != response {
		t.Fatal("Unwrap did not return the underlying ResponseWriter")
	}
	if err := http.NewResponseController(recorder).Flush(); err != nil || !response.Flushed {
		t.Fatalf("ResponseController flush: err=%v flushed=%t", err, response.Flushed)
	}
}

func TestAccessLogLogsFailedReadinessWithRequestFields(t *testing.T) {
	record := captureAccessLog(t, "/health/ready", http.StatusServiceUnavailable)
	if record["msg"] != "request completed" || record["request_id"] != "request-id" || record["method"] != http.MethodGet || record["path"] != "/health/ready" || record["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("unexpected readiness access log: %#v", record)
	}
	if _, ok := record["duration_ms"]; !ok {
		t.Fatalf("readiness access log lacks duration: %#v", record)
	}
}

func TestAccessLogLogsNormalAndHealthPrefixRequests(t *testing.T) {
	for _, path := range []string{"/api/config", "/health/live/details"} {
		t.Run(path, func(t *testing.T) {
			record := captureAccessLog(t, path, http.StatusOK)
			if record["path"] != path || record["status"] != float64(http.StatusOK) {
				t.Fatalf("unexpected access log: %#v", record)
			}
		})
	}
}

func captureAccessLog(t *testing.T, path string, status int) map[string]any {
	t.Helper()
	var output bytes.Buffer
	handler := accessLog(slog.New(slog.NewJSONHandler(&output, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, "request-id"))
	handler.ServeHTTP(httptest.NewRecorder(), request)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode access log: %v; output=%q", err, output.String())
	}
	return record
}
