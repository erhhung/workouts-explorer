package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

func adminPasswordLegacyHash(password string) string {
	// Sixteen bytes, and deliberately below the current Argon2 work profile.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(password), salt, 1, 8192, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func TestAdminPasswordLegacyMismatchRequestsRehash(t *testing.T) {
	hasher := newPasswordHasher(12)
	hash := adminPasswordLegacyHash("target own password")
	for _, password := range []string{"target own password", "administrator password"} {
		verified, rehash, err := hasher.verify(context.Background(), password, hash)
		if err != nil || verified != (password == "target own password") || !rehash {
			t.Fatalf("legacy verification: verified=%t rehash=%t err=%v", verified, rehash, err)
		}
	}
}

func TestAdminPasswordSharedDigestNamespace(t *testing.T) {
	key := []byte("synthetic-rate-limit-key")
	shared := rateDigest(key, "signin", "admin-password", []byte("shared"))
	for _, kind := range []string{"subject", "network"} {
		for _, value := range []string{"shared", "admin-password", "admin-password:shared", "198.51.100.0/24"} {
			if bytes.Equal(shared, rateDigest(key, "signin", kind, []byte(value))) {
				t.Fatal("shared admin-password bucket collides with an ordinary bucket")
			}
		}
	}
}

func TestAdminPasswordPolicyAndArgonGate(t *testing.T) {
	hasher := newPasswordHasher(20)
	for _, password := range []string{"short", strings.Repeat("x", 19), strings.Repeat("x", 129), "long password with\x00nul"} {
		if hasher.validate(password) == nil {
			t.Fatal("password outside configured policy was accepted")
		}
	}
	if cap(hasher.gate) != 2 {
		t.Fatal("password verification must retain the two-slot Argon2 gate")
	}
	hasher.gate <- struct{}{}
	hasher.gate <- struct{}{}
	defer func() { <-hasher.gate; <-hasher.gate }()
	if _, _, err := hasher.verify(context.Background(), "administrator password", adminPasswordLegacyHash("administrator password")); err == nil {
		t.Fatal("admin password verification bypassed the occupied Argon2 gate")
	}
}

func TestAdminPasswordLimitsPrecedePolicyAndSanitizeErrors(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgresql://unused@127.0.0.1:1/unused?sslmode=disable&pool_min_conns=0&pool_min_idle_conns=0")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	for _, enabled := range []bool{false, true} {
		for _, kind := range []string{"cookie", "bearer"} {
			server := &Server{db: pool, config: config.API{AllowAdminPassword: enabled}, passwords: newPasswordHasher(12)}
			call := callEndpoint(http.MethodPost, "/api/session-tokens", `{"username":"private-target","password":"short"}`, "", "")
			server.createSession(call.recorder, call.request, kind)
			if call.recorder.Code != http.StatusServiceUnavailable || call.recorder.Header().Get("Set-Cookie") != "" {
				t.Fatalf("enabled=%t kind=%s limits must run before policy: status=%d", enabled, kind, call.recorder.Code)
			}
			if strings.Contains(call.recorder.Body.String(), "short") || strings.Contains(call.recorder.Body.String(), "private-target") || strings.Contains(call.recorder.Body.String(), "closed") {
				t.Fatal("authentication error exposes private input or database details")
			}
		}
	}
}

func TestAdminPasswordBrowserOriginStillRequired(t *testing.T) {
	server := &Server{config: config.API{AllowAdminPassword: true, PublicURL: "https://workouts.example.test"}}
	call := callEndpoint(http.MethodPost, "/api/session", `{"username":"target","password":"administrator password"}`, "", "")
	call.request.Header.Set("Origin", "https://untrusted.example.test")
	server.CreateBrowserSession(call.recorder, call.request)
	if call.recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin browser fallback status=%d", call.recorder.Code)
	}
}

func adminPasswordSigninBody(t *testing.T, username, password string) string {
	t.Helper()
	value, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}
