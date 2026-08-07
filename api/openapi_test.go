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
	for _, path := range []string{"/api/config", "/api/openapi.yaml", "/swagger", "/health/live", "/health/ready", "/api/session", "/api/session-tokens", "/api/admin/invitations", "/api/invitations/{token}", "/api/registrations", "/api/password-reset-requests", "/api/password-resets", "/api/me", "/api/me/preferences", "/api/me/avatar", "/api/sources", "/api/sources/{sourceId}", "/api/ingest", "/api/jobs", "/api/jobs/{jobId}", "/api/jobs/{jobId}/cancellation", "/api/jobs/{jobId}/retry", "/api/jobs/{jobId}/files", "/api/jobs/{jobId}/events", "/api/jobs/{jobId}/logs", "/api/data-sync", "/api/notifications", "/api/notifications/{notificationId}/dismissal", "/api/workouts", "/api/workout-types", "/api/summary"} {
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
	for _, route := range []string{"/api/session", "/api/session-tokens", "/api/admin/invitations", "/api/registrations", "/api/password-reset-requests", "/api/password-resets", "/api/me", "/api/me/preferences", "/api/sources", "/api/sources/{sourceId}", "/api/ingest"} {
		item := document.Paths.Find(route)
		operation := item.Post
		if operation == nil {
			operation = item.Patch
		}
		if operation == nil || operation.Responses.Value("413") == nil || operation.Responses.Value("415") == nil {
			t.Errorf("request-body route %s lacks 413/415 responses", route)
		}
	}
	ingestAccepted := document.Components.Schemas["IngestAccepted"].Value
	if ingestAccepted.AdditionalProperties.Has == nil || *ingestAccepted.AdditionalProperties.Has || len(ingestAccepted.Required) != 3 {
		t.Fatal("ingest accepted response is not closed and fully required")
	}
	ingestCreate := document.Components.Schemas["IngestCreate"].Value
	sourceIDs := ingestCreate.Properties["sourceIds"].Value
	if ingestCreate.AdditionalProperties.Has == nil || *ingestCreate.AdditionalProperties.Has || len(ingestCreate.Required) != 1 ||
		sourceIDs.MinItems != 1 || sourceIDs.MaxItems == nil || *sourceIDs.MaxItems != 100 || !sourceIDs.UniqueItems {
		t.Fatal("ingest create contract is not closed with a bounded unique source set")
	}
	ingest := document.Paths.Find("/api/ingest").Post
	if ingest.OperationID != "createIngest" || ingest.Security == nil || len(*ingest.Security) != 2 {
		t.Fatal("manual ingest operation lacks its operation ID or owner security alternatives")
	}
	for _, status := range []string{"202", "400", "401", "403", "404", "409", "413", "415", "503"} {
		if ingest.Responses.Value(status) == nil {
			t.Errorf("manual ingest operation lacks %s response", status)
		}
	}
	if ingest.Responses.Value("202").Value.Headers["Location"] == nil {
		t.Fatal("manual ingest accepted response lacks Location")
	}
	for _, route := range []string{"/api/jobs", "/api/jobs/{jobId}", "/api/jobs/{jobId}/cancellation"} {
		item := document.Paths.Find(route)
		operation := item.Get
		if operation == nil {
			operation = item.Post
		}
		if operation == nil || operation.Security == nil || len(*operation.Security) != 2 {
			t.Errorf("job owner route %s lacks operation or security alternatives", route)
		}
	}
	for _, route := range []string{"/api/workouts", "/api/workout-types", "/api/summary"} {
		operation := document.Paths.Find(route).Get
		if operation == nil || operation.Security == nil || len(*operation.Security) != 2 {
			t.Errorf("owner read route %s lacks GET or security alternatives", route)
		}
	}
	for _, name := range []string{"ResolvedDateRange", "WorkoutType", "WorkoutTypeList", "ExactMetric", "Workout", "Pagination", "WorkoutList", "SummaryTotals", "WorkoutTypeSummary", "WorkoutSummary", "JobProgress", "JobSourceContext", "JobSummary", "JobDetail", "JobList", "JobFile", "JobFileList", "JobEvent", "JobEventList", "JobLog", "JobLogList", "Notification", "NotificationList", "SourceFreshness", "DataSyncSource", "DataSyncSchedule", "DataSync", "Problem"} {
		schema := document.Components.Schemas[name].Value
		if schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has {
			t.Errorf("owner response schema %s is not closed", name)
		}
	}
	jobDetail := document.Components.Schemas["JobDetail"].Value
	for _, name := range []string{"retryRootJobId", "retryOrdinal"} {
		if _, exists := jobDetail.Properties[name]; !exists {
			t.Errorf("job detail lacks %s", name)
		}
	}
	if ordinal := jobDetail.Properties["retryOrdinal"].Value; ordinal == nil || ordinal.Min == nil || *ordinal.Min != 1 {
		t.Error("job detail retry ordinal must have minimum 1")
	}
	for _, forbidden := range []string{"parameters", "configEnvelope", "workerId", "leaseToken", "originatingRequestId"} {
		if _, exists := jobDetail.Properties[forbidden]; exists {
			t.Errorf("job detail exposes %s", forbidden)
		}
	}
	dateRangePreference := document.Components.Schemas["DateRangePreference"].Value
	if dateRangePreference.Type == nil || !dateRangePreference.Type.Includes("string") || !dateRangePreference.Nullable || dateRangePreference.Pattern == "" || len(dateRangePreference.OneOf) != 0 || len(dateRangePreference.AnyOf) != 0 {
		t.Fatal("date range preference must remain one nullable constrained string schema")
	}
	source := document.Components.Schemas["Source"].Value
	for _, required := range []string{"id", "displayName", "type", "autoSyncEnabled", "status", "generation", "config", "createdAt", "updatedAt"} {
		if _, ok := source.Properties[required]; !ok {
			t.Errorf("source response lacks %s", required)
		}
	}
	for _, forbidden := range []string{"accountId", "configEnvelope", "keyId", "deletedAt"} {
		if _, ok := source.Properties[forbidden]; ok {
			t.Errorf("source response exposes %s", forbidden)
		}
	}
	rateResponse := document.Components.Responses["RateLimited"].Value
	if rateResponse.Headers["Retry-After"] == nil {
		t.Fatal("rate-limited response lacks Retry-After")
	}
	lower := strings.ToLower(string(openAPIDocument))
	for _, forbidden := range []string{"database_url", "authorization: bearer", "set-cookie: workouts_session=", "configenvelope", "wrappedkeynonce", "payloadnonce"} {
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
