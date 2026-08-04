package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
)

func TestOpenAPIContract(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(openAPIDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/config", "/api/openapi.yaml", "/swagger", "/health/live", "/health/ready", "/api/session", "/api/session-tokens", "/api/admin/invitations", "/api/invitations/{token}", "/api/registrations", "/api/password-reset-requests", "/api/password-resets", "/api/me", "/api/me/preferences", "/api/me/avatar"} {
		if document.Paths.Find(path) == nil {
			t.Errorf("missing path %s", path)
		}
	}
	if document.Components.SecuritySchemes["bearerAuth"] == nil || document.Components.SecuritySchemes["cookieAuth"] == nil {
		t.Error("ADR 0004 security schemes are missing")
	}
	csrf := document.Components.Parameters["CSRFToken"].Value.Schema.Value
	if csrf.MinLength != 0 || csrf.MaxLength != nil {
		t.Fatal("OpenAPI CSRF constraints would preempt transport-aware authorization")
	}
	for _, name := range []string{"BrowserSession", "TokenSession"} {
		schema := document.Components.Schemas[name].Value
		if len(schema.AllOf) != 0 || schema.Type == nil || !schema.Type.Includes("object") {
			t.Fatalf("%s is not a concrete response schema", name)
		}
	}
	for _, route := range []string{"/api/session", "/api/session-tokens", "/api/admin/invitations", "/api/registrations", "/api/password-reset-requests", "/api/password-resets", "/api/me", "/api/me/preferences"} {
		item := document.Paths.Find(route)
		operation := item.Post
		if operation == nil {
			operation = item.Patch
		}
		if operation == nil || operation.Responses.Value("413") == nil || operation.Responses.Value("415") == nil {
			t.Errorf("request-body route %s lacks 413/415 responses", route)
		}
	}
	rateResponse := document.Components.Responses["RateLimited"].Value
	if rateResponse.Headers["Retry-After"] == nil {
		t.Fatal("rate-limited response lacks Retry-After")
	}
	lower := strings.ToLower(string(openAPIDocument))
	for _, forbidden := range []string{"database_url", "authorization: bearer", "set-cookie: workouts_session="} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("public contract contains sensitive example marker %q", forbidden)
		}
	}
}

func TestImplementedResponsesMatchOpenAPI(t *testing.T) {
	openapi3filter.RegisterBodyDecoder("text/html", func(body io.Reader, _ http.Header, _ *openapi3.SchemaRef, _ openapi3filter.EncodingFn) (any, error) {
		value, err := io.ReadAll(body)
		return string(value), err
	})
	document, err := openapi3.NewLoader().LoadFromData(openAPIDocument)
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacy.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	handler := testHandler(t)
	tests := []struct {
		name, method, path, body, contentType string
		wantStatus                            int
	}{
		{"config", http.MethodGet, "/api/config", "", "", http.StatusOK},
		{"openapi", http.MethodGet, "/api/openapi.yaml", "", "", http.StatusOK},
		{"swagger", http.MethodGet, "/swagger", "", "", http.StatusOK},
		{"liveness", http.MethodGet, "/health/live", "", "", http.StatusOK},
		{"readiness unavailable", http.MethodGet, "/health/ready", "", "", http.StatusServiceUnavailable},
		{"browser unavailable", http.MethodPost, "/api/session", `{"username":"someone","password":"placeholder"}`, "application/json", http.StatusServiceUnavailable},
		{"token unavailable", http.MethodPost, "/api/session-tokens", `{"username":"someone","password":"placeholder"}`, "application/json", http.StatusServiceUnavailable},
		{"current unauthorized", http.MethodGet, "/api/session", "", "", http.StatusUnauthorized},
		{"signout unauthorized", http.MethodDelete, "/api/session", "", "", http.StatusUnauthorized},
		{"oversized", http.MethodPost, "/api/session-tokens", strings.Repeat("x", int(config.MaxRequestBodyBytes)+1), "application/json", http.StatusRequestEntityTooLarge},
		{"unsupported media", http.MethodPost, "/api/session-tokens", `username=someone`, "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("X-Request-ID") == "" || response.Header().Get("Content-Type") == "" {
				t.Fatalf("status=%d headers=%v", response.Code, response.Header())
			}
			routeRequest := httptest.NewRequest(test.method, test.path, nil)
			route, pathParams, err := router.FindRoute(routeRequest)
			if err != nil {
				t.Fatal(err)
			}
			input := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: routeRequest, PathParams: pathParams, Route: route},
				Status:                 response.Code,
				Header:                 response.Header(),
				Body:                   io.NopCloser(strings.NewReader(response.Body.String())),
			}
			if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
				t.Fatalf("response contract validation failed: %v; body=%s", err, response.Body.String())
			}
		})
	}
}

func validateRecordedResponse(t *testing.T, method, path string, response *httptest.ResponseRecorder) {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromData(openAPIDocument)
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacy.NewRouter(document)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, nil)
	route, pathParams, err := router.FindRoute(request)
	if err != nil {
		t.Fatal(err)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: request, PathParams: pathParams, Route: route},
		Status:                 response.Code, Header: response.Header(), Body: io.NopCloser(strings.NewReader(response.Body.String())),
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Fatalf("real response contract validation failed: %v", err)
	}
}
