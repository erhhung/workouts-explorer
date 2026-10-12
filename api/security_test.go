package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: strings.Repeat("A", 43)})
	if _, err := (&Server{}).authenticate(context.Background(), request); !errors.Is(err, errUnauthenticated) {
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

func TestRequireSessionCredentialFailures(t *testing.T) {
	token := strings.Repeat("A", 43)
	for _, test := range []struct {
		name    string
		headers []string
		cookie  string
	}{
		{"missing", nil, ""},
		{"invalid cookie", nil, "malformed"},
		{"empty authorization", []string{""}, token},
		{"empty bearer", []string{"Bearer "}, token},
		{"wrong scheme", []string{"Basic " + token}, token},
		{"lowercase scheme", []string{"bearer " + token}, token},
		{"extra space", []string{"Bearer  " + token}, token},
		{"trailing space", []string{"Bearer " + token + " "}, token},
		{"malformed bearer", []string{"Bearer malformed"}, token},
		{"noncanonical token", []string{"Bearer " + strings.Repeat("A", 42) + "B"}, token},
		{"double authorization", []string{"Bearer " + token, "Bearer " + token}, token},
		{"combined authorization", []string{"Bearer " + token + ", Bearer " + token}, token},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/session", nil)
			for _, header := range test.headers {
				request.Header.Add("Authorization", header)
			}
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: test.cookie})
			}
			server := &Server{}
			if _, err := server.authenticate(request.Context(), request); !errors.Is(err, errUnauthenticated) {
				t.Fatal("credential failure did not return unauthenticated sentinel")
			}
			response := httptest.NewRecorder()
			session, ok := server.requireSession(response, request, "user")
			if ok || session != (authenticatedSession{}) || response.Code != http.StatusUnauthorized {
				t.Fatalf("credential denial: granted=%t status=%d", ok, response.Code)
			}
		})
	}
}

func TestRequireSessionCanceledContext(t *testing.T) {
	for _, kind := range []string{"bearer", "cookie"} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", kind, expired), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				wantErr := context.Canceled
				if expired {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
					wantErr = context.DeadlineExceeded
				}
				cancel()
				request := httptest.NewRequest(http.MethodGet, "/api/session", nil).WithContext(ctx)
				token := strings.Repeat("A", 43)
				if kind == "bearer" {
					request.Header.Set("Authorization", "Bearer "+token)
				} else {
					request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
				}
				server := &Server{} // A canceled request must not touch even a nil pool.
				if _, err := server.authenticate(ctx, request); !errors.Is(err, wantErr) || errors.Is(err, errUnauthenticated) {
					t.Fatal("request context failure was not preserved")
				}
				response := httptest.NewRecorder()
				session, ok := server.requireSession(response, request, "user")
				if ok || session != (authenticatedSession{}) || response.Code != http.StatusServiceUnavailable {
					t.Fatalf("context denial: granted=%t status=%d", ok, response.Code)
				}
				var accessOutput bytes.Buffer
				handler := accessLog(slog.New(slog.NewJSONHandler(&accessOutput, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, ok := server.requireSession(w, r, "user"); ok {
						t.Error("canceled request reached protected handler")
					}
				}))
				handler.ServeHTTP(httptest.NewRecorder(), request)
				var record map[string]any
				if err := json.Unmarshal(accessOutput.Bytes(), &record); err != nil || record["status"] != float64(http.StatusServiceUnavailable) {
					t.Fatal("access log did not record canceled request as 503")
				}
			})
		}
	}
}

func TestRequireSessionClosedPool(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgresql://unused@127.0.0.1:1/unused?sslmode=disable&pool_min_conns=0&pool_min_idle_conns=0")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	server := &Server{db: pool}
	request := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	if _, err := server.authenticate(request.Context(), request); err == nil || errors.Is(err, errUnauthenticated) {
		t.Fatal("closed pool was not an infrastructure failure")
	}
	response := httptest.NewRecorder()
	session, ok := server.requireSession(response, request, "user")
	if ok || session != (authenticatedSession{}) || response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed pool denial: granted=%t status=%d", ok, response.Code)
	}
}

func TestSessionErrorResponseAndSanitizedLog(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	private := "private-token-hash-user-id-SQL-constraint"
	for _, test := range []struct {
		name     string
		err      error
		ctxErr   error
		class    string
		sqlstate string
	}{
		{"credential", fmt.Errorf("%s: %w", private, errUnauthenticated), nil, "", ""},
		{"database", errors.New(private), nil, "database", ""},
		{"wrapped canceled", fmt.Errorf("%s: %w", private, context.Canceled), nil, "canceled", ""},
		{"wrapped deadline", fmt.Errorf("%s: %w", private, context.DeadlineExceeded), nil, "deadline", ""},
		{"postgres", fmt.Errorf("%s: %w", private, &pgconn.PgError{Code: "08006", Message: private, Detail: private, ConstraintName: private, InternalQuery: private}), nil, "database", "08006"},
		{"postgres query cancellation", &pgconn.PgError{Code: "57014", Message: private}, nil, "database", "57014"},
		{"source canceled", &pgconn.PgError{Code: "57014", Message: private}, context.Canceled, "canceled", "57014"},
		{"source deadline", &pgconn.PgError{Code: "57014", Message: private}, context.DeadlineExceeded, "deadline", "57014"},
		{"unbounded sqlstate", &pgconn.PgError{Code: private, Message: private}, nil, "database", ""},
		{"invalid sqlstate", &pgconn.PgError{Code: "08\n06", Message: private}, nil, "database", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			output.Reset()
			request := httptest.NewRequest(http.MethodGet, "/api/session", nil)
			if test.ctxErr != nil {
				ctx, cancel := context.WithCancel(request.Context())
				if test.ctxErr == context.DeadlineExceeded {
					cancel()
					ctx, cancel = context.WithDeadline(request.Context(), time.Now().Add(-time.Second))
				}
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			writeSessionError(response, request, test.err)
			want := http.StatusServiceUnavailable
			if test.class == "" {
				want = http.StatusUnauthorized
			}
			var problem generated.Problem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || response.Code != want || problem.Status != want || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("session error response: status=%d decode=%v", response.Code, err)
			}
			if response.Header().Get("Set-Cookie") != "" || strings.Contains(response.Body.String()+output.String(), private) {
				t.Fatal("session failure changed credentials or exposed private error data")
			}
			if test.class == "" {
				if output.Len() != 0 {
					t.Fatal("credential denial logged infrastructure failure")
				}
				return
			}
			var record map[string]any
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			fields := 4
			if test.sqlstate != "" {
				fields++
			}
			if record["error_class"] != test.class || len(record) != fields {
				t.Fatal("log did not contain only bounded infrastructure fields")
			}
			if test.sqlstate != "" && record["sqlstate"] != test.sqlstate {
				t.Fatal("log did not preserve safe SQLSTATE")
			}
		})
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
	server := &Server{config: config.API{PublicURL: "https://workouts.example.test", BrowserSigninOrigins: []string{"https://workouts.example.test", "https://workouts.internal.test"}}}
	for _, test := range []struct {
		origin, site string
		valid        bool
	}{
		{"", "", true},
		{"https://workouts.example.test", "same-origin", true},
		{"https://workouts.example.test", "none", true},
		{"https://workouts.internal.test", "same-origin", true},
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
