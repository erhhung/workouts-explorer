package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/getkin/kin-openapi/openapi3"
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
		{"bounded false", `{"sourceIds":["` + sourceID + `"],"startDate":"2026-01-01","endDate":"2026-01-02","reprocessAllFiles":false}`, "application/json", false, http.StatusUnauthorized},
		{"bounded true", `{"sourceIds":["` + sourceID + `"],"startDate":"2026-01-01","endDate":"2026-01-02","reprocessAllFiles":true}`, "application/json", false, http.StatusUnauthorized},
		{"null policy", `{"sourceIds":["` + sourceID + `"],"reprocessAllFiles":null}`, "application/json", false, http.StatusBadRequest},
		{"string policy", `{"sourceIds":["` + sourceID + `"],"reprocessAllFiles":"false"}`, "application/json", false, http.StatusBadRequest},
		{"number policy", `{"sourceIds":["` + sourceID + `"],"reprocessAllFiles":0}`, "application/json", false, http.StatusBadRequest},
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

func TestIngestReprocessingContractAndParameters(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(openAPIDocument)
	if err != nil {
		t.Fatal(err)
	}
	create := document.Components.Schemas["IngestCreate"].Value
	policy := create.Properties["reprocessAllFiles"].Value
	if !policy.Type.Includes("boolean") || policy.Nullable || policy.Default != nil || policy.Description == "" {
		t.Fatal("reprocessAllFiles must be a non-null optional boolean with description, not an injected default")
	}
	for _, field := range create.Required {
		if field == "reprocessAllFiles" {
			t.Fatal("reprocessAllFiles must be optional")
		}
	}
	sourceID := uuid.New()
	for _, test := range []struct {
		name, fields string
		valid, force bool
	}{
		{"incremental", ``, true, false},
		{"incremental false", `"reprocessAllFiles":false`, false, false},
		{"incremental true", `"reprocessAllFiles":true`, false, false},
		{"missing end", `"startDate":"2026-01-01","reprocessAllFiles":false`, false, false},
		{"missing start", `"endDate":"2026-01-02","reprocessAllFiles":true`, false, false},
		{"bounded omission", `"startDate":"2026-01-01","endDate":"2026-01-02"`, true, false},
		{"bounded false", `"startDate":"2026-01-01","endDate":"2026-01-02","reprocessAllFiles":false`, true, false},
		{"bounded true", `"startDate":"2026-01-01","endDate":"2026-01-02","reprocessAllFiles":true`, true, true},
		{"reversed", `"startDate":"2026-01-02","endDate":"2026-01-01","reprocessAllFiles":true`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input generated.IngestCreate
			if err := json.Unmarshal([]byte(`{`+test.fields+`}`), &input); err != nil {
				t.Fatal(err)
			}
			dateRange, valid := normalizedIngestRange(input)
			if valid != test.valid || dateRange.reprocessAllFiles != test.force {
				t.Fatalf("range=%+v valid=%t", dateRange, valid)
			}
			if !valid {
				return
			}
			parent, err := ingestParentParameters([]uuid.UUID{sourceID}, dateRange)
			if err != nil {
				t.Fatal(err)
			}
			child, err := ingestChildParameters(sourceID, 3, dateRange)
			if err != nil {
				t.Fatal(err)
			}
			for index, raw := range [][]byte{parent, child} {
				var parameters map[string]any
				if err := json.Unmarshal(raw, &parameters); err != nil {
					t.Fatal(err)
				}
				wantFields := 2 + index
				if dateRange.mode == "bounded" {
					wantFields += 3
					if value, present := parameters["reprocessAllFiles"]; !present || value != test.force {
						t.Fatalf("bounded policy not explicit: %s", raw)
					}
				}
				if len(parameters) != wantFields {
					t.Fatalf("unexpected persisted parameter shape: %s", raw)
				}
			}
			decoded, err := decodeRetryIngestRange(child)
			if err != nil || !sameIngestRange(dateRange, decoded) {
				t.Fatalf("retry round trip=%+v err=%v", decoded, err)
			}
		})
	}
}

func TestIngestCoalescingSeparatesPolicyAndLineage(t *testing.T) {
	sourceA, sourceB := uuid.New(), uuid.New()
	sourceIDs := normalizedUUIDs([]uuid.UUID{sourceA, sourceB})
	sources := []ingestSource{{sourceRecord: sourceRecord{id: sourceIDs[0], generation: 1}}, {sourceRecord: sourceRecord{id: sourceIDs[1], generation: 2}}}
	start, end, otherEnd := "2026-01-01", "2026-01-02", "2026-01-03"
	bounded := ingestRange{mode: "bounded", startDate: &start, endDate: &end}
	force := bounded
	force.reprocessAllFiles = true
	if sameIngestRange(bounded, force) || manualIngestKey(sources, bounded) == manualIngestKey(sources, force) {
		t.Fatal("force policy is missing from range equality or coalescing")
	}
	parentA, parentB, childA, childB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	requests := []ingestEnqueueRequest{
		{dateRange: bounded},
		{dateRange: force},
		{dateRange: ingestRange{mode: "incremental"}},
		{dateRange: ingestRange{mode: "bounded", startDate: &start, endDate: &otherEnd}},
		{dateRange: bounded, retryOfParent: &parentA, childRetries: map[uuid.UUID]uuid.UUID{sourceA: childA}},
		{dateRange: bounded, retryOfParent: &parentB, childRetries: map[uuid.UUID]uuid.UUID{sourceA: childA}},
		{dateRange: bounded, retryOfParent: &parentA, childRetries: map[uuid.UUID]uuid.UUID{sourceA: childB}},
		{dateRange: bounded, retryOfParent: &parentA, childRetries: map[uuid.UUID]uuid.UUID{sourceB: childA}},
		{dateRange: bounded, retryOfParent: &parentA, childRetries: map[uuid.UUID]uuid.UUID{sourceA: childA, sourceB: childB}},
	}
	seen := make(map[[32]byte]bool)
	for _, request := range requests {
		key := ingestEnqueueKey(sources, request)
		if seen[key] {
			t.Fatalf("different intent or ancestry coalesced: %+v", request)
		}
		seen[key] = true
	}
	reordered := requests[len(requests)-1]
	reordered.childRetries = make(map[uuid.UUID]uuid.UUID)
	reordered.childRetries[sourceB] = childB
	reordered.childRetries[sourceA] = childA
	for range 100 {
		if ingestEnqueueKey(sources, reordered) != ingestEnqueueKey(sources, requests[len(requests)-1]) {
			t.Fatal("retry map iteration changed the key")
		}
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
