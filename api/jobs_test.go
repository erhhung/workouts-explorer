package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCoverageRetryConflictsAreSafeAndTyped(t *testing.T) {
	for _, test := range []struct{ code, message, reason string }{
		{"55000", "coverage retry requires a terminal parent", "not-terminal"},
		{"55000", "only the latest coverage attempt may be retried", "not-latest"},
		{"55000", "coverage retry target is busy; retry the latest attempt", "busy"},
		{"54001", "coverage retry limit reached", "retry-limit"},
		{"55000", "private-sql-canary /secret/path", "unavailable"},
		{"54001", "private-sql-canary /secret/path", "unavailable"},
		{"54001", "coverage retry requires a terminal parent", "unavailable"},
		{"55000", "coverage retry limit reached", "unavailable"},
		{"55000", "only the latest coverage attempt may be retried private-sql-canary", "unavailable"},
	} {
		reason := coverageRetryErrorReason(&pgconn.PgError{Code: test.code, Message: test.message})
		if reason != test.reason {
			t.Fatalf("%q mapped to %q, want %q", test.message, reason, test.reason)
		}
		response := httptest.NewRecorder()
		writeCoverageRetryConflict(response, httptest.NewRequest(http.MethodPost, "/api/jobs/test/retry", nil), reason)
		var problem generated.Problem
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusConflict || response.Header().Get("Content-Type") != "application/problem+json" ||
			problem.Type != "https://workouts-explorer.invalid/problems/coverage-retry/"+test.reason ||
			strings.Contains(response.Body.String(), "private-sql-canary") {
			t.Fatalf("unsafe conflict: %s", response.Body.String())
		}
	}
}

func TestCoverageParentPresentationStatus(t *testing.T) {
	if generated.JobStatus("superseded").Valid() || !generated.JobDisplayStatusSuperseded.Valid() {
		t.Fatal("superseded must remain display-only, not an execution status")
	}
	for _, test := range []struct {
		status  string
		running int
		want    string
	}{{"running", 0, "queued"}, {"running", 1, "running"}, {"running", 2, "running"}, {"queued", 0, "queued"}, {"failed", 0, "failed"}} {
		if got := coverageParentPresentationStatus(test.status, test.running); got != test.want {
			t.Errorf("coverageParentPresentationStatus(%q, %d) = %q, want %q", test.status, test.running, got, test.want)
		}
	}
}

func TestJobRequestsAreValidatedByActualRouter(t *testing.T) {
	handler := testHandler(t)
	jobID := "018F8E7D7A4C7C03A1C23D4E5F607182"
	tests := []struct {
		method, path, body, contentType string
		want                            int
	}{
		{http.MethodGet, "/api/jobs", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/jobs?status=superseded", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/jobs?page=0", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs?pageSize=101", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs?status=invented", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs?operation=invented", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs/" + jobID, "", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/jobs/" + jobID + "/cancellation", `{}`, "application/json", http.StatusUnauthorized},
		{http.MethodPost, "/api/jobs/" + jobID + "/cancellation", `{"reason":"raw"}`, "application/json", http.StatusBadRequest},
		{http.MethodPost, "/api/jobs/" + jobID + "/retry", `{}`, "application/json", http.StatusUnauthorized},
		{http.MethodPost, "/api/jobs/" + jobID + "/retry", `{"path":"/private/leak"}`, "application/json", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs/" + jobID + "/files?pageSize=101", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs/" + jobID + "/events", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/jobs/" + jobID + "/logs", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/data-sync", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/notifications?state=unresolved", "", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/notifications/" + jobID + "/dismissal", `{"raw":"hostile"}`, "application/json", http.StatusBadRequest},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		if test.contentType != "" {
			request.Header.Set("Content-Type", test.contentType)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s %s status=%d want=%d body=%s", test.method, test.path, response.Code, test.want, response.Body.String())
		}
		validateRecordedResponse(t, test.method, strings.Split(test.path, "?")[0], response)
	}
}

func TestRetryParametersAndFailureRedaction(t *testing.T) {
	rangeValue, err := decodeRetryIngestRange([]byte(`{"sourceId":"018F8E7D7A4C7C03A1C23D4E5F607182","generation":4,"mode":"bounded","startDate":"2026-01-01","endDate":"2026-01-02"}`))
	if err != nil || rangeValue.mode != "bounded" || rangeValue.startDate == nil || *rangeValue.startDate != "2026-01-01" {
		t.Fatalf("range=%#v err=%v", rangeValue, err)
	}
	if _, err := decodeRetryIngestRange([]byte(`{"sourceId":"018F8E7D7A4C7C03A1C23D4E5F607182","generation":4,"mode":"incremental","path":"/private/leak"}`)); err == nil {
		t.Fatal("hostile unknown retry parameter was accepted")
	}
	hostile := "/private/leak"
	code, summary := safeJobFailure(&hostile)
	if code == nil || summary == nil || strings.Contains(*code+*summary, hostile) {
		t.Fatalf("hostile failure was not redacted: %v %v", code, summary)
	}
}

func TestCoverageFailureRedaction(t *testing.T) {
	for _, value := range []string{"coverage-route-invalid", "coverage-route-read-failed", "coverage-route-timeout",
		"coverage-route-too-large", "coverage-osm-unavailable", "coverage-persist-failed", "coverage-transient-failed"} {
		code, summary := safeJobFailure(&value)
		if code == nil || *code != value || summary == nil || *summary == "" {
			t.Fatalf("coverage failure %q mapped to %v/%v", value, code, summary)
		}
	}
	for value, want := range map[string]string{
		"coverage-osm-unavailable":  "Coverage map data could not be processed.",
		"coverage-transient-failed": "Coverage processing was interrupted by a transient database failure.",
	} {
		_, summary := safeJobFailure(&value)
		if summary == nil || *summary != want {
			t.Fatalf("coverage failure %q summary=%v want=%q", value, summary, want)
		}
	}
}

func TestICloudFailureReauthenticationGuidance(t *testing.T) {
	for _, value := range []string{"source-auth-expired", "source-auth-failed", "source-adp-approval-required"} {
		code, summary := safeJobFailure(&value)
		if code == nil || *code != value || summary == nil || !strings.Contains(*summary, "externally") ||
			!strings.Contains(*summary, "update this source's credentials") {
			t.Fatalf("iCloud failure %q did not provide safe recovery guidance", value)
		}
	}
}
