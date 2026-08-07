package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/google/uuid"
)

func TestIngestRequestsAreValidatedByActualRouter(t *testing.T) {
	handler := testHandler(t)
	sourceID := "018F8E7D7A4C7C03A1C23D4E5F607182"
	tests := []struct {
		name, body, contentType string
		chunked                 bool
		want                    int
	}{
		{"malformed", `{"sourceIds":`, "application/json", false, http.StatusBadRequest},
		{"unknown field", `{"sourceIds":["` + sourceID + `"],"path":"/private/leak"}`, "application/json", false, http.StatusBadRequest},
		{"null", `null`, "application/json", false, http.StatusBadRequest},
		{"missing sources", `{}`, "application/json", false, http.StatusBadRequest},
		{"empty sources", `{"sourceIds":[]}`, "application/json", false, http.StatusBadRequest},
		{"duplicate sources", `{"sourceIds":["` + sourceID + `","` + sourceID + `"]}`, "application/json", false, http.StatusBadRequest},
		{"one date reaches authorization", `{"sourceIds":["` + sourceID + `"],"startDate":"2026-01-01"}`, "application/json", false, http.StatusUnauthorized},
		{"hyphenated source", `{"sourceIds":["018f8e7d-7a4c-7c03-a1c2-3d4e5f607182"]}`, "application/json", false, http.StatusBadRequest},
		{"unsupported media", `{"sourceIds":["` + sourceID + `"]}`, "text/plain", false, http.StatusUnsupportedMediaType},
		{"oversized chunked", strings.Repeat(" ", int(config.MaxRequestBodyBytes)+1), "application/json", true, http.StatusRequestEntityTooLarge},
		{"valid unauthorized", `{"sourceIds":["` + sourceID + `"]}`, "application/json", false, http.StatusUnauthorized},
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

func TestManualIngestNormalizationAndCoalescing(t *testing.T) {
	low := uuid.MustParse("018f8e7d-7a4c-7c03-a1c2-3d4e5f607182")
	high := uuid.MustParse("018f8e7d-7a4c-7c03-a1c2-3d4e5f607183")
	ids, ok := normalizedIngestSourceIDs([]string{compactUUID(high), compactUUID(low)})
	if !ok || ids[0] != low || ids[1] != high {
		t.Fatalf("IDs were not normalized: %v", ids)
	}
	if _, ok := normalizedIngestSourceIDs([]string{compactUUID(low), compactUUID(low)}); ok {
		t.Fatal("duplicate IDs were accepted")
	}
	incremental, ok := normalizedIngestRange(generated.IngestCreate{})
	if !ok || incremental.mode != "incremental" {
		t.Fatal("omitted dates did not select incremental mode")
	}
	sources := []ingestSource{{sourceRecord: sourceRecord{id: low, generation: 1}}, {sourceRecord: sourceRecord{id: high, generation: 2}}}
	key := manualIngestKey(sources, incremental)
	if key != manualIngestKey(sources, incremental) {
		t.Fatal("coalescing key is not deterministic")
	}
	sources[1].generation++
	if key == manualIngestKey(sources, incremental) {
		t.Fatal("coalescing key is not generation-bound")
	}
}
