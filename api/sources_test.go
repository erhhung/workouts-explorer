package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCanonicalSourceDisplayName(t *testing.T) {
	display, canonical, ok := canonicalSourceDisplayName("  ＦｏＯ  ")
	if !ok || display != "ＦｏＯ" || canonical != "foo" {
		t.Fatalf("display=%q canonical=%q ok=%t", display, canonical, ok)
	}
	for _, invalid := range []string{
		"",
		" \t ",
		"line\nbreak",
		"zero\u200bwidth",
		"left\u200eto-right",
		"right\u200fto-left",
		"override\u202etext",
		"isolate\u2066text\u2069",
		"joiner\u200dtext",
		"grapheme\u034fjoiner",
		"variation\ufe0fselector",
		string([]byte{0xff}),
	} {
		if _, _, ok := canonicalSourceDisplayName(invalid); ok {
			t.Fatalf("accepted invalid display name %q", invalid)
		}
	}
}

func TestSourceRequestsAreValidatedByActualRouter(t *testing.T) {
	handler := testHandler(t)
	sourceID := "018F8E7D7A4C7C03A1C23D4E5F607182"
	validCreate := `{"displayName":"Source","type":"health-auto-export-local","autoSyncEnabled":false,"config":{"version":1,"path":"/data/workouts/source"}}`
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"post malformed", http.MethodPost, "/api/sources", `{"displayName":`, http.StatusBadRequest},
		{"post unknown", http.MethodPost, "/api/sources", strings.TrimSuffix(validCreate, "}") + `,"unknown":true}`, http.StatusBadRequest},
		{"post null", http.MethodPost, "/api/sources", `null`, http.StatusBadRequest},
		{"post trailing", http.MethodPost, "/api/sources", validCreate + ` {}`, http.StatusBadRequest},
		{"post empty", http.MethodPost, "/api/sources", ``, http.StatusBadRequest},
		{"post valid unauthorized", http.MethodPost, "/api/sources", validCreate, http.StatusUnauthorized},
		{"patch malformed", http.MethodPatch, "/api/sources/" + sourceID, `{"displayName":`, http.StatusBadRequest},
		{"patch unknown", http.MethodPatch, "/api/sources/" + sourceID, `{"unknown":true}`, http.StatusBadRequest},
		{"patch null body", http.MethodPatch, "/api/sources/" + sourceID, `null`, http.StatusBadRequest},
		{"patch null property", http.MethodPatch, "/api/sources/" + sourceID, `{"displayName":null}`, http.StatusBadRequest},
		{"patch trailing", http.MethodPatch, "/api/sources/" + sourceID, `{"autoSyncEnabled":true} {}`, http.StatusBadRequest},
		{"patch empty object", http.MethodPatch, "/api/sources/" + sourceID, `{}`, http.StatusBadRequest},
		{"patch empty body", http.MethodPatch, "/api/sources/" + sourceID, ``, http.StatusBadRequest},
		{"patch valid unauthorized", http.MethodPatch, "/api/sources/" + sourceID, `{"autoSyncEnabled":true}`, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("content-type=%q", response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestCompactSourceUUID(t *testing.T) {
	id := uuid.MustParse("018f8e7d-7a4c-7c03-a1c2-3d4e5f607182")
	encoded := compactUUID(id)
	parsed, ok := parseCompactUUID(encoded)
	if !ok || parsed != id {
		t.Fatalf("compact UUID did not round trip: %q", encoded)
	}
	for _, invalid := range []string{id.String(), "018F8E7D7A4C7C03A1C23D4E5F60718g", "018f8e7d7a4c7c03a1c23d4e5f607182"} {
		if _, ok := parseCompactUUID(invalid); ok {
			t.Fatalf("accepted invalid compact UUID %q", invalid)
		}
	}
}

func TestSourceConnectionCheckCoalescingKey(t *testing.T) {
	sourceID := uuid.MustParse("018f8e7d-7a4c-7c03-a1c2-3d4e5f607182")
	first := sourceConnectionCheckKey(sourceID, 1)
	if first != sourceConnectionCheckKey(sourceID, 1) {
		t.Fatal("coalescing key is not deterministic")
	}
	if first == sourceConnectionCheckKey(sourceID, 2) || first == sourceConnectionCheckKey(uuid.Must(uuid.NewV7()), 1) {
		t.Fatal("coalescing key is not bound to source and generation")
	}
}
