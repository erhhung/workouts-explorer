package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/google/uuid"
)

func TestIngestRequestsAreValidatedByActualRouter(t *testing.T) {
	handler := testHandler(t)
	sourceID := "018F8E7D7A4C7C03A1C23D4E5F607182"
	tests := []struct {
		name        string
		body        string
		contentType string
		chunked     bool
		want        int
	}{
		{"malformed", `{"sourceId":`, "application/json", false, http.StatusBadRequest},
		{"unknown field", `{"sourceId":"` + sourceID + `","path":"/private/leak"}`, "application/json", false, http.StatusBadRequest},
		{"null", `null`, "application/json", false, http.StatusBadRequest},
		{"missing source", `{}`, "application/json", false, http.StatusBadRequest},
		{"hyphenated source", `{"sourceId":"018f8e7d-7a4c-7c03-a1c2-3d4e5f607182"}`, "application/json", false, http.StatusBadRequest},
		{"lowercase source", `{"sourceId":"018f8e7d7a4c7c03a1c23d4e5f607182"}`, "application/json", false, http.StatusBadRequest},
		{"unsupported media", `{"sourceId":"` + sourceID + `"}`, "text/plain", false, http.StatusUnsupportedMediaType},
		{"oversized chunked", strings.Repeat(" ", int(config.MaxRequestBodyBytes)+1), "application/json", true, http.StatusRequestEntityTooLarge},
		{"valid unauthorized", `{"sourceId":"` + sourceID + `"}`, "application/json", false, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/ingest", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.chunked {
				request.ContentLength = -1
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("status=%d want=%d content-type=%q body=%s", response.Code, test.want, response.Header().Get("Content-Type"), response.Body.String())
			}
			if strings.Contains(response.Body.String(), "/private/leak") {
				t.Fatal("validation response leaked request data")
			}
			validateRecordedResponse(t, http.MethodPost, "/api/ingest", response)
		})
	}
}

func TestManualIngestSourceCoalescingKey(t *testing.T) {
	sourceID := uuid.MustParse("018f8e7d-7a4c-7c03-a1c2-3d4e5f607182")
	if manualIngestSourceKey(sourceID) != manualIngestSourceKey(sourceID) {
		t.Fatal("coalescing key is not deterministic")
	}
	if manualIngestSourceKey(sourceID) == manualIngestSourceKey(uuid.Must(uuid.NewV7())) {
		t.Fatal("coalescing key is not source-bound")
	}
}
