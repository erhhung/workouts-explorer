package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobRequestsAreValidatedByActualRouter(t *testing.T) {
	handler := testHandler(t)
	jobID := "018F8E7D7A4C7C03A1C23D4E5F607182"
	tests := []struct {
		method, path, body, contentType string
		want                            int
	}{
		{http.MethodGet, "/api/jobs", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/jobs?page=0", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs?pageSize=101", "", "", http.StatusBadRequest},
		{http.MethodGet, "/api/jobs?status=invented", "", "", http.StatusBadRequest},
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
