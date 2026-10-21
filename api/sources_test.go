package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
)

func testICloudConfig() sourceconfig.ICloud {
	return sourceconfig.ICloud{Version: 1, Path: "Auto Export/Example", AppleID: "apple-canary@example.test", Password: "cGFzc3dvcmQtY2FuYXJ5LXN5bnRoZXRpYw", Cookies: "cookie-canary=synthetic; other=value", TrustToken: "trust-canary", ClientID: "synthetic-client"}
}

func assertICloudRedacted(t *testing.T, encoded []byte) {
	t.Helper()
	config := testICloudConfig()
	for _, forbidden := range []string{"appleId", "password", "cookies", "trustToken", config.AppleID, config.Password, config.Cookies, config.TrustToken} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("source response contains a private field or canary: %s", forbidden)
		}
	}
}

func TestICloudSourceResponseAllowlist(t *testing.T) {
	keys, err := sourcecrypto.LoadKeyring(testAPIConfig(t).SourceKeyringFile)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{sourceKeys: keys}
	accountID, sourceID := uuid.New(), uuid.New()
	config := testICloudConfig()
	_, canonical, err := sourceconfig.CanonicalizeICloud(config)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: sourceID}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.sourceResponse(accountID, sourceRecord{id: sourceID, typeName: sourceconfig.ICloudType, configEnvelope: envelope, generation: 1, status: "connected", createdAt: time.Now(), updatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	assertICloudRedacted(t, encoded)
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, response)
	validateRecordedResponse(t, http.MethodGet, "/api/sources/"+response.Id, recorder)
	if discriminator, err := response.Discriminator(); err != nil || discriminator != sourceconfig.ICloudType {
		t.Fatal("public response lacks the correct type discriminator")
	}
	if response.Config.Path != config.Path || response.Config.ClientId == nil || *response.Config.ClientId != config.ClientID {
		t.Fatal("public configuration does not contain the allowlisted fields")
	}
	if _, err := server.sourceResponse(uuid.New(), sourceRecord{id: sourceID, typeName: sourceconfig.ICloudType, configEnvelope: envelope}); err == nil {
		t.Fatal("foreign account decrypted source configuration")
	}
}

func TestICloudOpenAPISecretBoundaries(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(openAPIDocument)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HealthAutoExportICloudConfig", "HealthAutoExportICloudConfigPatch"} {
		schema := document.Components.Schemas[name].Value
		for _, field := range []string{"appleId", "password", "cookies", "trustToken"} {
			if schema.Properties[field] == nil || !schema.Properties[field].Value.WriteOnly {
				t.Fatalf("%s.%s is not write-only", name, field)
			}
		}
	}
	public := document.Components.Schemas["SourcePublicConfig"].Value
	if len(public.Properties) != 3 {
		t.Fatal("public config is not limited to version, path, and optional clientId")
	}
	for _, field := range []string{"appleId", "password", "cookies", "trustToken"} {
		if public.Properties[field] != nil {
			t.Fatal("public config schema exposes a credential")
		}
	}
	create := document.Components.Schemas["SourceCreate"].Value
	response := document.Components.Schemas["Source"].Value
	if response.Discriminator == nil || len(response.OneOf) != 2 || len(create.OneOf) != 2 ||
		response.Discriminator.Mapping[sourceconfig.ICloudType].Ref == create.Discriminator.Mapping[sourceconfig.ICloudType].Ref {
		t.Fatal("public response union is not distinct from credential-bearing create union")
	}
}

func TestICloudRequestsThroughActualRouter(t *testing.T) {
	handler := testHandler(t)
	config, _ := json.Marshal(testICloudConfig())
	create := `{"displayName":"iCloud","type":"health-auto-export-icloud","autoSyncEnabled":false,"config":` + string(config) + `}`
	id := "018F8E7D7A4C7C03A1C23D4E5F607182"
	tests := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/api/sources", create, http.StatusUnauthorized},
		{http.MethodPost, "/api/sources", create + " {}", http.StatusBadRequest},
		{http.MethodPost, "/api/sources", strings.Replace(create, `"trustToken":"trust-canary"`, `"trustToken":null`, 1), http.StatusBadRequest},
		{http.MethodPost, "/api/sources", strings.Replace(create, `"trustToken":"trust-canary"`, `"trustToken":""`, 1), http.StatusBadRequest},
		{http.MethodPost, "/api/sources", strings.Replace(create, `"trustToken":"trust-canary"`, `"_auth_session":"trust-canary"`, 1), http.StatusBadRequest},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{"path":"Auto Export/Changed"}}`, http.StatusUnauthorized},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{"cookies":"new-cookie-canary"}}`, http.StatusUnauthorized},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{"cookies":null}}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{"cookies":""}}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{"description":"new-cookie-canary"}}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{}}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/sources/" + id, `{"config":{"cookies":"new-cookie-canary"}} {}`, http.StatusBadRequest},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s body=%s status=%d want=%d", test.method, test.body, response.Code, test.want)
		}
		assertICloudRedacted(t, response.Body.Bytes())
		if strings.Contains(response.Body.String(), "new-cookie-canary") {
			t.Fatal("validation error leaked a credential")
		}
	}
}

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
	for _, accepted := range []string{id.String(), strings.ToUpper(id.String()), strings.ToLower(encoded)} {
		parsed, ok := parseCompactUUID(accepted)
		if !ok || parsed != id {
			t.Fatalf("accepted UUID %q parsed as %s, ok=%v", accepted, parsed, ok)
		}
	}
	for _, invalid := range []string{"018F8E7D7A4C7C03A1C23D4E5F60718g", encoded[:31], encoded + "0", id.String() + "0"} {
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
