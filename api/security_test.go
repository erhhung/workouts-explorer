package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
)

func TestCanonicalUsernameCollisions(t *testing.T) {
	fixtures := [][]string{
		{"Straße", "STRASSE"},
		{"Ｆｏｏ", "foo"},
		{"Élan", "E\u0301LAN"},
		{"\u3000Alice\u00a0", "alice"},
	}
	for _, fixture := range fixtures {
		_, left, leftErr := canonicalUsername(fixture[0])
		_, right, rightErr := canonicalUsername(fixture[1])
		if leftErr != nil || rightErr != nil || left != right {
			t.Fatalf("%q/%q canonicalized to %q/%q (%v/%v)", fixture[0], fixture[1], left, right, leftErr, rightErr)
		}
	}
}

func TestPreferencesUseEmbeddedIANAData(t *testing.T) {
	t.Setenv("ZONEINFO", "/does/not/exist")
	server := &Server{config: config.API{PageSizeMaximum: 100}}
	field, message := server.validatePreferences(generated.Preferences{
		Theme:          "dark",
		Units:          "imperial",
		Timezone:       "America/Los_Angeles",
		FirstWeekday:   "monday",
		ClockFormat:    "12h",
		WorkoutColumns: []generated.PreferencesWorkoutColumns{"date", "type", "duration"},
		PageSize:       25,
	})
	if field != "" || message != "" {
		t.Fatalf("valid embedded IANA timezone rejected: %s: %s", field, message)
	}
}

func TestDateRangePreferenceValidation(t *testing.T) {
	for _, value := range []string{"thisWeek", "last30Days", "lastYear", "2024-02-29/2024-03-01", "2026-01-01/2026-01-01"} {
		if !validDateRangePreference(value) {
			t.Errorf("valid date range rejected: %q", value)
		}
	}
	for _, value := range []string{"last-30-days", "last30days", "2023-02-29/2023-03-01", "2026-03-02/2026-03-01", "2026-1-01/2026-01-02", "2026-01-01", ""} {
		if validDateRangePreference(value) {
			t.Errorf("invalid date range accepted: %q", value)
		}
	}
}

func TestDateRangePreferenceContractRejectsLegacyValue(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/me/preferences", strings.NewReader(`{"dateRange":"last-30-days"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	testHandler(t).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("legacy date range contract response=%d %s", response.Code, response.Body.String())
	}
}

func TestBearerPrecedenceAndCSRFMatrix(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/me", nil)
	request.Header.Set("Authorization", "Bearer malformed")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "otherwise-valid-cookie"})
	if _, err := (&Server{}).authenticate(context.Background(), request); err == nil {
		t.Fatal("invalid bearer credential fell back to cookie authentication")
	}
	bearer := authenticatedSession{kind: "bearer"}
	malformed := "not-an-opaque-token"
	if !requireCSRF(httptest.NewRecorder(), request, bearer, &malformed) {
		t.Fatal("bearer mutation unexpectedly required CSRF")
	}
	cookie := authenticatedSession{kind: "cookie"}
	response := httptest.NewRecorder()
	if requireCSRF(response, request, cookie, nil) || response.Code != http.StatusForbidden {
		t.Fatalf("cookie mutation without CSRF status=%d", response.Code)
	}
}

func TestCanonicalEmailIDNAAndStrictSyntax(t *testing.T) {
	_, unicodeDomain, err := canonicalEmail("User@bücher.example")
	if err != nil {
		t.Fatal(err)
	}
	_, asciiDomain, err := canonicalEmail("user@xn--bcher-kva.example")
	if err != nil || unicodeDomain != asciiDomain {
		t.Fatalf("IDNA mismatch: %q/%q (%v)", unicodeDomain, asciiDomain, err)
	}
	for _, invalid := range []string{`Name <user@example.com>`, `"user"@example.com`, `user@[127.0.0.1]`, `user@example.com.`, `user@example..com`, `user@.example.com`} {
		if _, _, err := canonicalEmail(invalid); err == nil {
			t.Fatalf("accepted invalid email %q", invalid)
		}
	}
	if unicode15String("\U0001CC00") {
		t.Fatal("accepted a scalar assigned after Unicode 15.0")
	}
	if canonicalizationVersion != 1 || canonicalUnicodeVersion != "15.0.0" || canonicalIDNAProfile != "uts46-lookup-nontransitional-bidi-dns-v1" {
		t.Fatal("canonical identity data versions changed without a migration")
	}
}

func TestPasswordHashProfileAndStrictParsing(t *testing.T) {
	hasher := newPasswordHasher(12)
	hash, err := hasher.hash(context.Background(), "long enough password")
	if err != nil {
		t.Fatal(err)
	}
	valid, rehash, err := hasher.verify(context.Background(), "long enough password", hash)
	if err != nil || !valid || rehash {
		t.Fatalf("valid=%t rehash=%t err=%v", valid, rehash, err)
	}
	valid, _, err = hasher.verify(context.Background(), "incorrect password", hash)
	if err != nil || valid {
		t.Fatalf("incorrect password valid=%t err=%v", valid, err)
	}
	for _, malformed := range []string{
		"$argon2i$v=19$m=65536,t=3,p=1$AA$AA",
		"$argon2id$v=19$m=65536,t=3,p=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=999999999,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if _, _, err := hasher.verify(context.Background(), "long enough password", malformed); err == nil {
			t.Fatalf("accepted malformed hash %q", malformed)
		}
	}
}

func TestClientIPTrustAndPrefixes(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "203.0.113.9:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.8")
	ip, err := clientIP(request, nil)
	if err != nil || ip.String() != "203.0.113.9" || networkPrefix(ip) != "203.0.113.0/24" {
		t.Fatalf("untrusted result=%v prefix=%q err=%v", ip, networkPrefix(ip), err)
	}
	_, trusted, _ := net.ParseCIDR("203.0.113.0/24")
	ip, err = clientIP(request, []*net.IPNet{trusted})
	if err != nil || ip.String() != "198.51.100.8" {
		t.Fatalf("trusted result=%v err=%v", ip, err)
	}
}

func TestTrustedProxyParserRejectsAmbiguity(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("203.0.113.0/24")
	for _, test := range []struct {
		name    string
		headers []string
	}{
		{"malformed", []string{"not-an-ip"}},
		{"multiple fields", []string{"198.51.100.1", "198.51.100.2"}},
		{"too many", []string{strings.Repeat("198.51.100.1,", 20) + "198.51.100.2"}},
		{"too large", []string{strings.Repeat("1", 1025)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/session-tokens", nil)
			request.RemoteAddr = "203.0.113.9:1234"
			for _, value := range test.headers {
				request.Header.Add("X-Forwarded-For", value)
			}
			if _, err := clientIP(request, []*net.IPNet{trusted}); err == nil {
				t.Fatal("ambiguous forwarding metadata was accepted")
			}
		})
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "[2001:db8:ffff::1]:1234"
	_, trustedV6, _ := net.ParseCIDR("2001:db8:ffff::/48")
	request.Header.Set("X-Forwarded-For", "2001:db8:1::4, 2001:db8:ffff::2")
	ip, err := clientIP(request, []*net.IPNet{trustedV6})
	if err != nil || ip.String() != "2001:db8:1::4" || networkPrefix(ip) != "2001:db8:1::/64" {
		t.Fatalf("IPv6 chain result=%v err=%v", ip, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.9:1234"
	if ip, err = clientIP(request, []*net.IPNet{trusted}); err != nil || !ip.Equal(net.ParseIP("203.0.113.9")) {
		t.Fatalf("trusted peer without forwarding header result=%v err=%v", ip, err)
	}
}

func TestMalformedTrustedProxyReturnsBadRequest(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("203.0.113.0/24")
	server := &Server{config: config.API{TrustedProxyCIDRs: []*net.IPNet{trusted}}}
	request := httptest.NewRequest(http.MethodPost, "/api/session-tokens", strings.NewReader(`{"username":"someone","password":"not a real password"}`))
	request.RemoteAddr = "203.0.113.9:1234"
	request.Header.Set("X-Forwarded-For", "malformed")
	response := httptest.NewRecorder()
	server.CreateSessionToken(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed trusted proxy status=%d", response.Code)
	}
}

func TestBrowserSigninOriginAndFetchMetadata(t *testing.T) {
	server := &Server{config: config.API{PublicURL: "https://workouts.example.test"}}
	for _, test := range []struct {
		origin, site string
		valid        bool
	}{
		{"", "", true},
		{"https://workouts.example.test", "same-origin", true},
		{"https://workouts.example.test", "none", true},
		{"https://evil.example.test", "same-origin", false},
		{"https://workouts.example.test", "cross-site", false},
		{"https://workouts.example.test/path", "same-origin", false},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/session", nil)
		if test.origin != "" {
			request.Header.Set("Origin", test.origin)
		}
		if test.site != "" {
			request.Header.Set("Sec-Fetch-Site", test.site)
		}
		if server.validBrowserSigninOrigin(request) != test.valid {
			t.Fatalf("origin=%q site=%q validity mismatch", test.origin, test.site)
		}
	}
}

func TestCookieSecurityOnlyDowngradesForValidatedLocalDevelopment(t *testing.T) {
	for _, test := range []struct {
		config config.API
		secure bool
	}{
		{config.API{PublicURL: "https://workouts.example.test"}, true},
		{config.API{PublicURL: "http://localhost:5173", LocalDevelopment: true}, false},
		{config.API{PublicURL: "http://localhost:5173"}, true},
		{config.API{PublicURL: "http://10.0.0.2", LocalDevelopment: true}, true},
		{config.API{PublicURL: "malformed", LocalDevelopment: true}, true},
	} {
		if got := (&Server{config: test.config}).secureCookie(); got != test.secure {
			t.Fatalf("publicURL=%q local=%t secure=%t", test.config.PublicURL, test.config.LocalDevelopment, got)
		}
	}
}
