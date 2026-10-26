package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This suite changes synthetic principals and rate counters, never database schema.
// It requires a freshly migrated disposable database, not application configuration.
func TestAdminPasswordIntegration(t *testing.T) {
	apiURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if os.Getenv("INGEST_ADMIN_PASSWORD_TEST_DISPOSABLE") != "1" || apiURL == "" || migrationURL == "" {
		t.Skip("INGEST_ADMIN_PASSWORD_TEST_DISPOSABLE=1 and fresh disposable API_DATABASE_URL/MIGRATION_DATABASE_URL are required")
	}
	var first *pgx.ConnConfig
	for _, dsn := range []string{apiURL, migrationURL} {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil || !strings.HasPrefix(cfg.Database, "workouts_") || !strings.Contains(cfg.Database, "_test_") ||
			(cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") || len(cfg.Fallbacks) != 0 {
			t.Fatal("admin-password tests require an explicitly named loopback workouts_*_test_* database without fallback hosts (use sslmode=disable)")
		}
		if first != nil && (first.Database != cfg.Database || first.Host != cfg.Host || first.Port != cfg.Port) {
			t.Fatal("API and migration connections must use the same disposable database, host, and port")
		}
		first = cfg
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal("open disposable API pool")
	}
	defer db.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal("open disposable migration pool")
	}
	defer adminDB.Close()
	var role string
	if err := db.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != "workouts_api" {
		t.Fatal("API connection must use the actual restricted workouts_api role")
	}
	var admins int
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM app.authentication_principals WHERE role='administrator'`).Scan(&admins); err != nil || admins != 0 {
		t.Fatal("a fresh disposable database without administrator fixtures is required")
	}
	server := integrationServer(t, db, &recordingSender{})
	server.config.AllowAdminPassword = true
	const ownPassword = "target own password"
	const adminPassword = "synthetic administrator password"
	const rotatedPassword = "rotated synthetic administrator password"
	legacyHash := adminPasswordLegacyHash(ownPassword)
	principal, account := insertSourceTestUser(t, adminDB)
	otherPrincipal, otherAccount := insertSourceTestUser(t, adminDB)
	username := "target" + strings.ReplaceAll(principal.String(), "-", "")[:12]
	email := username + "@example.test"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := adminDB.Exec(ctx, query, args...); err != nil {
			t.Fatal("synthetic fixture update failed: ", err)
		}
	}
	exec(`UPDATE app.authentication_principals SET username=$1,canonical_username=$1,email=$2,canonical_email=$2,password_hash=$3 WHERE id=$4`, username, email, legacyHash, principal)
	exec(`UPDATE app.authentication_principals SET username='othertarget',canonical_username='othertarget',password_hash=$1 WHERE id=$2`, legacyHash, otherPrincipal)
	clearLimits := func() {
		var remaining float64
		if err := adminDB.QueryRow(ctx, `SELECT 600-mod(extract(epoch FROM clock_timestamp()),600)::double precision`).Scan(&remaining); err != nil {
			t.Fatal("read synthetic rate-limit window")
		}
		// Keep an eleven-attempt assertion within one fixed ten-minute bucket.
		if remaining < 15 {
			time.Sleep(time.Duration(remaining*float64(time.Second)) + 100*time.Millisecond)
		}
		exec(`DELETE FROM app.rate_limits WHERE operation='signin'`)
	}
	signin := func(s *Server, kind, identity, password string, remote ...string) endpointCall {
		t.Helper()
		path := "/api/session-tokens"
		handler := http.HandlerFunc(s.CreateSessionToken)
		if kind == "cookie" {
			path, handler = "/api/session", s.CreateBrowserSession
		}
		call := callEndpoint(http.MethodPost, path, adminPasswordSigninBody(t, identity, password), "", "")
		if len(remote) != 0 {
			call.request.RemoteAddr = remote[0]
		}
		requestID(handler).ServeHTTP(call.recorder, call.request)
		return call
	}
	checkStatus := func(call endpointCall, want int) {
		t.Helper()
		if call.recorder.Code != want {
			t.Fatalf("sign-in status=%d want=%d", call.recorder.Code, want)
		}
		if want != http.StatusCreated {
			if call.recorder.Header().Get("Set-Cookie") != "" || strings.Contains(call.recorder.Body.String(), "accessToken") {
				t.Fatal("denied sign-in issued credentials")
			}
			if want == http.StatusUnauthorized && !strings.Contains(call.recorder.Body.String(), `"detail":"invalid username or password"`) {
				t.Fatal("credential rejection did not use the generic response")
			}
		}
	}
	counts := func() (int, int) {
		t.Helper()
		var sessions, audits int
		if err := adminDB.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.sessions WHERE principal_id=$1), (SELECT count(*) FROM app.audit_records WHERE target_id=$1 AND action='authentication.admin_password_signin')`, principal).Scan(&sessions, &audits); err != nil {
			t.Fatal(err)
		}
		return sessions, audits
	}
	readHash := func(id uuid.UUID) string {
		t.Helper()
		var hash string
		if err := db.QueryRow(ctx, `SELECT password_hash FROM app.authentication_principals WHERE id=$1`, id).Scan(&hash); err != nil {
			t.Fatal("read synthetic hash")
		}
		return hash
	}
	t.Run("missing admin still permits own password", func(t *testing.T) {
		clearLimits()
		checkStatus(signin(server, "bearer", username, adminPassword), http.StatusUnauthorized)
		checkStatus(signin(server, "bearer", username, ownPassword), http.StatusCreated)
	})

	passwordFile := t.TempDir() + "/synthetic-bootstrap-password"
	if err := os.WriteFile(passwordFile, []byte(adminPassword), 0o600); err != nil {
		t.Fatal("write synthetic private bootstrap fixture")
	}
	bootstrap := BootstrapAdminOptions{Username: "syntheticadmin", Email: "syntheticadmin@example.test", PasswordFile: passwordFile, PasswordMin: 12}
	if err := BootstrapAdmin(ctx, adminDB, bootstrap); err != nil {
		t.Fatal("bootstrap synthetic administrator: ", err)
	}
	adminID, adminHash, err := server.findAdminForPasswordSignin(ctx)
	if err != nil {
		t.Fatal("find sole synthetic administrator")
	}
	extraAdmin := uuid.Must(uuid.NewV7())
	fixtureTx, err := adminDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureTx.Rollback(ctx)
	if _, err := fixtureTx.Exec(ctx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name,disabled_at) VALUES($1,'administrator','extraadmin','extraadmin','extraadmin@example.test','extraadmin@example.test',1,$2,'Extra Administrator',transaction_timestamp())`, extraAdmin, adminHash); err != nil {
		t.Fatal("insert synthetic extra administrator")
	}
	if _, err := fixtureTx.Exec(ctx, `INSERT INTO app.administrators(principal_id) VALUES($1)`, extraAdmin); err != nil {
		t.Fatal("insert synthetic administrator subtype")
	}
	if err := fixtureTx.Commit(ctx); err != nil {
		t.Fatal("commit synthetic administrator fixture")
	}
	reset := func() {
		clearLimits()
		server.config.AllowAdminPassword = true
		server.passwords = newPasswordHasher(12)
		exec(`UPDATE app.authentication_principals SET disabled_at=NULL,password_hash=$1 WHERE id=$2`, legacyHash, principal)
		exec(`UPDATE app.accounts SET state='active' WHERE id=$1`, account)
		exec(`UPDATE app.authentication_principals SET disabled_at=NULL,password_hash=$1 WHERE id=$2`, adminHash, adminID)
		exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, extraAdmin)
	}

	for _, kind := range []string{"cookie", "bearer"} {
		for _, identity := range []string{username, email} {
			for _, mode := range []string{"default off fallback", "default off own", "enabled own", "enabled fallback"} {
				t.Run(kind+"/"+identity+"/"+mode, func(t *testing.T) {
					reset()
					server.config.AllowAdminPassword = strings.HasPrefix(mode, "enabled")
					password := adminPassword
					fallback := mode == "enabled fallback"
					if strings.HasSuffix(mode, "own") {
						password = ownPassword
					}
					beforeSessions, beforeAudits := counts()
					call := signin(server, kind, identity, password)
					if mode == "default off fallback" {
						checkStatus(call, http.StatusUnauthorized)
						return
					}
					checkStatus(call, http.StatusCreated)
					validateRecordedResponse(t, http.MethodPost, call.request.URL.Path, call.recorder)
					var result generated.TokenSession
					if err := json.Unmarshal(call.recorder.Body.Bytes(), &result); err != nil || result.Identity.Id != compactUUID(principal) || result.Identity.Role != "user" || result.Identity.Username != username {
						t.Fatal("session response is not the target user identity")
					}
					if remaining := time.Until(result.ExpiresAt); remaining < server.config.SessionLifetime-time.Minute || remaining > server.config.SessionLifetime {
						t.Fatal("session lifetime changed")
					}
					token, csrf := result.AccessToken, ""
					if kind == "cookie" {
						var browser generated.BrowserSession
						if err := json.Unmarshal(call.recorder.Body.Bytes(), &browser); err != nil {
							t.Fatal(err)
						}
						cookies := call.recorder.Result().Cookies()
						if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" || browser.CsrfToken == "" {
							t.Fatal("ordinary browser cookie/CSRF semantics changed")
						}
						token, csrf = cookies[0].Value, browser.CsrfToken
					} else if len(call.recorder.Result().Cookies()) != 0 || result.TokenType != generated.Bearer {
						t.Fatal("ordinary bearer semantics changed")
					}
					current := callEndpoint(http.MethodGet, "/api/session", "", token, "")
					if kind == "cookie" {
						current = callCookieEndpoint(http.MethodGet, "/api/session", "", token)
					}
					// Fallback creates no persistent session kind or flag-dependent provenance.
					server.config.AllowAdminPassword = false
					session, err := server.authenticate(ctx, current.request)
					if err != nil || session.principalID != principal || session.role != "user" || session.accountID == nil || *session.accountID != account || session.kind != kind {
						t.Fatal("stored session is not an ordinary target-account user session")
					}
					denied := callEndpoint(http.MethodPost, "/api/admin/invitations", `{}`, token, "")
					if kind == "cookie" {
						denied = callCookieEndpoint(http.MethodPost, "/api/admin/invitations", `{}`, token)
					}
					server.CreateInvitation(denied.recorder, denied.request, generated.CreateInvitationParams{})
					if denied.recorder.Code != http.StatusForbidden {
						t.Fatal("target session acquired administrator privileges")
					}
					tx, err := server.accountTransaction(ctx, *session.accountID)
					if err != nil {
						t.Fatal(err)
					}
					var own, other int
					err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE account_id=$1),count(*) FILTER (WHERE account_id=$2) FROM app.preferences`, account, otherAccount).Scan(&own, &other)
					_ = tx.Rollback(ctx)
					if err != nil || own != 1 || other != 0 {
						t.Fatal("session account scope did not enforce preferences RLS")
					}
					if !requireCSRF(call.recorder, current.request, session, &csrf) {
						t.Fatal("ordinary session CSRF rejected")
					}
					if kind == "cookie" {
						missing := callCookieEndpoint(http.MethodDelete, "/api/session", "", token)
						server.DeleteCurrentSession(missing.recorder, missing.request, generated.DeleteCurrentSessionParams{})
						if missing.recorder.Code != http.StatusForbidden {
							t.Fatal("cookie logout bypassed CSRF")
						}
					}
					sessions, audits := counts()
					wantAudits := beforeAudits
					if fallback {
						wantAudits++
						var actor, target, auditAccount uuid.UUID
						var targetType, request string
						err := adminDB.QueryRow(ctx, `SELECT actor_principal_id,account_id,target_id,target_type,request_id FROM app.audit_records WHERE target_id=$1 AND action='authentication.admin_password_signin' ORDER BY created_at DESC LIMIT 1`, principal).Scan(&actor, &auditAccount, &target, &targetType, &request)
						if err != nil || actor != adminID || auditAccount != account || target != principal || targetType != "principal" || request != call.recorder.Header().Get("X-Request-ID") {
							t.Fatal("fallback audit does not identify the authenticating admin and target")
						}
						if readHash(principal) != legacyHash || readHash(adminID) != adminHash {
							t.Fatal("fallback changed a target or admin password hash")
						}
					} else if readHash(principal) == legacyHash {
						t.Fatal("successful own-password verification did not rehash its legacy hash")
					}
					if sessions != beforeSessions+1 || audits != wantAudits {
						t.Fatal("unexpected session/audit count")
					}
					logout := generated.CSRFToken(csrf)
					server.DeleteCurrentSession(current.recorder, current.request, generated.DeleteCurrentSessionParams{XCSRFToken: &logout})
					if current.recorder.Code != http.StatusNoContent {
						t.Fatal("ordinary logout failed")
					}
					if _, err := server.authenticate(ctx, current.request); err != errUnauthenticated {
						t.Fatal("logged-out session remains usable")
					}
				})
			}
		}
	}

	for _, state := range []string{"unknown", "disabled", "deleting", "deleted", "administrator target", "wrong password", "admin disabled", "multiple admins", "invalid target hash", "invalid admin hash"} {
		t.Run(state, func(t *testing.T) {
			reset()
			identity, password, want := username, adminPassword, http.StatusUnauthorized
			switch state {
			case "unknown":
				identity = "unknownuser"
			case "disabled":
				exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, principal)
			case "deleting", "deleted":
				exec(`UPDATE app.accounts SET state=$1 WHERE id=$2`, state, account)
			case "administrator target":
				exec(`UPDATE app.authentication_principals SET disabled_at=NULL WHERE id=$1`, extraAdmin)
				identity = "extraadmin"
				// An incorrect admin own password must not fall back to another admin.
				exec(`UPDATE app.authentication_principals SET password_hash=$1 WHERE id=$2`, legacyHash, extraAdmin)
			case "wrong password":
				password = "incorrect synthetic password"
			case "admin disabled":
				exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, adminID)
			case "multiple admins":
				exec(`UPDATE app.authentication_principals SET disabled_at=NULL WHERE id=$1`, extraAdmin)
			case "invalid target hash":
				exec(`UPDATE app.authentication_principals SET password_hash='private-invalid-target-hash' WHERE id=$1`, principal)
				want = http.StatusServiceUnavailable
			case "invalid admin hash":
				exec(`UPDATE app.authentication_principals SET password_hash='private-invalid-admin-hash' WHERE id=$1`, adminID)
				want = http.StatusServiceUnavailable
			}
			beforeSessions, beforeAudits := counts()
			call := signin(server, "bearer", identity, password)
			checkStatus(call, want)
			if strings.Contains(call.recorder.Body.String(), "private-invalid") {
				t.Fatal("hash error exposed stored credentials")
			}
			sessions, audits := counts()
			if sessions != beforeSessions || audits != beforeAudits {
				t.Fatal("rejected authentication persisted a session or audit")
			}
			if state == "admin disabled" || state == "multiple admins" || state == "invalid admin hash" {
				checkStatus(signin(server, "bearer", username, ownPassword), http.StatusCreated)
			}
		})
	}

	t.Run("uniform fallback denial", func(t *testing.T) {
		reset()
		type targetFixture struct {
			name, identity string
			ineligible     bool
		}
		targets := []targetFixture{
			{"active", username, false},
			{"unknown", "unknownuser", true},
			{"administrator", "syntheticadmin", false},
		}
		// Create inactive fixtures once; login attempts never mutate their state.
		for _, state := range []string{"disabled", "deleting", "deleted"} {
			id, accountID := insertSourceTestUser(t, adminDB)
			identity := state + strings.ReplaceAll(id.String(), "-", "")[:12]
			exec(`UPDATE app.authentication_principals SET username=$1,canonical_username=$1,password_hash=$2 WHERE id=$3`, identity, legacyHash, id)
			if state == "disabled" {
				exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, id)
			} else {
				exec(`UPDATE app.accounts SET state=$1 WHERE id=$2`, state, accountID)
			}
			targets = append(targets, targetFixture{state, identity, true})
		}
		shared := rateDigest(server.config.RateLimitKey, "signin", "admin-password", []byte("shared"))
		exhaustShared := func(t *testing.T) {
			t.Helper()
			// Seed only the shared bucket so ordinary subject/network limits cannot mask it.
			for i := 0; i < 10; i++ {
				var allowed bool
				if err := db.QueryRow(ctx, `SELECT allowed FROM app.consume_rate_limit('signin','subject',$1)`, shared).Scan(&allowed); err != nil || !allowed {
					t.Fatal("seed shared fallback limit")
				}
			}
		}
		for _, kind := range []string{"cookie", "bearer"} {
			for _, budget := range []string{"available", "exhausted"} {
				for _, target := range targets {
					passwords := []string{"wrong synthetic administrator password"}
					if target.ineligible {
						passwords = append(passwords, adminPassword)
					}
					for index, password := range passwords {
						passwordCase := "wrong password"
						if index == 1 {
							passwordCase = "correct admin password"
						}
						t.Run(kind+"/"+budget+"/"+target.name+"/"+passwordCase, func(t *testing.T) {
							clearLimits()
							want, wantCount := http.StatusUnauthorized, 1
							if budget == "exhausted" {
								exhaustShared(t)
								want, wantCount = http.StatusTooManyRequests, 11
							}
							beforeSessions, beforeAudits := counts()
							call := signin(server, kind, target.identity, password)
							checkStatus(call, want)
							if want == http.StatusTooManyRequests && (call.recorder.Header().Get("Retry-After") == "" || !strings.Contains(call.recorder.Body.String(), `"detail":"too many authentication attempts"`)) {
								t.Fatal("shared fallback rejection must use the ordinary generic 429 response")
							}
							var sharedCount, networkCount int
							if err := adminDB.QueryRow(ctx, `SELECT (SELECT sum(count) FROM app.rate_limits WHERE operation='signin' AND key_kind='subject' AND key_digest=$1),(SELECT sum(count) FROM app.rate_limits WHERE operation='signin' AND key_kind='network')`, shared).Scan(&sharedCount, &networkCount); err != nil || sharedCount != wantCount || networkCount != 1 {
								t.Fatal("every failed own-password attempt must charge exactly one shared bucket and one standard network bucket")
							}
							sessions, audits := counts()
							if sessions != beforeSessions || audits != beforeAudits || readHash(principal) != legacyHash || readHash(adminID) != adminHash {
								t.Fatal("denied fallback changed sessions, audit records, or password hashes")
							}
						})
					}
				}
			}
			for _, target := range targets {
				t.Run(kind+"/lookup failure/"+target.name, func(t *testing.T) {
					clearLimits()
					tracer := &adminPasswordQueryHook{match: "SELECT id,password_hash", cancel: true}
					hooked := adminPasswordHookedServer(t, server, tracer)
					checkStatus(signin(hooked, kind, target.identity, "wrong synthetic administrator password"), http.StatusServiceUnavailable)
					if !tracer.fired {
						t.Fatal("failed own-password verification skipped the uniform admin lookup")
					}
				})
				t.Run(kind+"/second verification uses gate/"+target.name, func(t *testing.T) {
					clearLimits()
					tracer := &adminPasswordQueryHook{match: "app.consume_rate_limit('signin','subject'", run: func() {
						server.passwords.gate <- struct{}{}
						server.passwords.gate <- struct{}{}
					}}
					defer func() {
						if tracer.fired {
							<-server.passwords.gate
							<-server.passwords.gate
						}
					}()
					hooked := adminPasswordHookedServer(t, server, tracer)
					checkStatus(signin(hooked, kind, target.identity, "wrong synthetic administrator password"), http.StatusServiceUnavailable)
					var sharedCount int
					if !tracer.fired {
						t.Fatal("failed own-password verification skipped the shared limit and second verification")
					}
					if err := adminDB.QueryRow(ctx, `SELECT sum(count) FROM app.rate_limits WHERE operation='signin' AND key_kind='subject' AND key_digest=$1`, shared).Scan(&sharedCount); err != nil || sharedCount != 1 {
						t.Fatal("second verification must consume the shared limit before entering the Argon2 gate")
					}
				})
			}
			t.Run(kind+"/admin own password ignores shared exhaustion", func(t *testing.T) {
				clearLimits()
				exhaustShared(t)
				call := signin(server, kind, "syntheticadmin", adminPassword)
				checkStatus(call, http.StatusCreated)
				var session generated.TokenSession
				if err := json.Unmarshal(call.recorder.Body.Bytes(), &session); err != nil || session.Identity.Id != compactUUID(adminID) || session.Identity.Role != "administrator" {
					t.Fatal("normal admin own-password login lost its identity")
				}
				var sharedCount int
				if err := adminDB.QueryRow(ctx, `SELECT sum(count) FROM app.rate_limits WHERE operation='signin' AND key_kind='subject' AND key_digest=$1`, shared).Scan(&sharedCount); err != nil || sharedCount != 10 {
					t.Fatal("successful own-password verification consumed fallback budget")
				}
			})
		}
	})

	for _, adminState := range []string{"none enabled", "multiple enabled"} {
		for _, kind := range []string{"cookie", "bearer"} {
			for _, identity := range []string{username, "unknownuser"} {
				t.Run("dummy fallback verification/"+adminState+"/"+kind+"/"+identity, func(t *testing.T) {
					reset()
					if adminState == "none enabled" {
						exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, adminID)
					} else {
						exec(`UPDATE app.authentication_principals SET disabled_at=NULL WHERE id=$1`, extraAdmin)
					}
					tracer := &adminPasswordQueryHook{match: "app.consume_rate_limit('signin','subject'", run: func() {
						server.passwords.gate <- struct{}{}
						server.passwords.gate <- struct{}{}
					}}
					defer func() {
						if tracer.fired {
							<-server.passwords.gate
							<-server.passwords.gate
						}
					}()
					hooked := adminPasswordHookedServer(t, server, tracer)
					checkStatus(signin(hooked, kind, identity, adminPassword), http.StatusServiceUnavailable)
					if !tracer.fired {
						t.Fatal("unavailable unique admin skipped the shared limit or dummy Argon2 verification")
					}
				})
			}
		}
	}

	t.Run("policy applies to both paths only when enabled", func(t *testing.T) {
		for _, kind := range []string{"cookie", "bearer"} {
			for _, password := range []string{ownPassword, adminPassword, "short", strings.Repeat("x", 129), "password containing\x00NUL"} {
				reset()
				// Both matching passwords are now below policy, including a legacy own hash.
				server.passwords = newPasswordHasher(64)
				beforeSessions, beforeAudits := counts()
				checkStatus(signin(server, kind, username, password), http.StatusUnauthorized)
				sessions, audits := counts()
				if sessions != beforeSessions || audits != beforeAudits || readHash(principal) != legacyHash {
					t.Fatal("policy rejection changed authentication state")
				}
			}
			reset()
			shortHash, err := newPasswordHasher(1).hash(ctx, "short")
			if err != nil {
				t.Fatal(err)
			}
			exec(`UPDATE app.authentication_principals SET password_hash=$1 WHERE id=$2`, shortHash, principal)
			server.config.AllowAdminPassword = false
			checkStatus(signin(server, kind, username, "short"), http.StatusCreated)
		}
		reset()
		for i := 0; i < 10; i++ {
			checkStatus(signin(server, "bearer", username, "short"), http.StatusUnauthorized)
		}
		checkStatus(signin(server, "bearer", username, "short"), http.StatusTooManyRequests)
	})

	t.Run("DB rotation supersedes original bootstrap plaintext", func(t *testing.T) {
		reset()
		rotatedHash, err := server.passwords.hash(ctx, rotatedPassword)
		if err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE app.authentication_principals SET password_hash=$1 WHERE id=$2`, rotatedHash, adminID)
		for _, kind := range []string{"cookie", "bearer"} {
			checkStatus(signin(server, kind, email, adminPassword), http.StatusUnauthorized)
			checkStatus(signin(server, kind, email, rotatedPassword), http.StatusCreated)
		}
		if readHash(adminID) != rotatedHash || readHash(principal) != legacyHash {
			t.Fatal("fallback modified rotated admin or legacy user hash")
		}
	})

	t.Run("shared distributed throttle leaves normal own login unaffected", func(t *testing.T) {
		reset()
		second := adminPasswordHookedServer(t, server, &adminPasswordQueryHook{match: "unused"})
		second.passwords = newPasswordHasher(12)
		for attempt := 0; attempt < 11; attempt++ {
			s, identity, remote := server, username, "198.51.100.8:1234"
			if attempt%2 != 0 {
				s, identity, remote = second, "othertarget", "203.0.113.8:1234"
			}
			want := http.StatusUnauthorized
			if attempt == 10 {
				want = http.StatusTooManyRequests
			}
			call := signin(s, "bearer", identity, "wrong administrator password", remote)
			checkStatus(call, want)
			if want == http.StatusTooManyRequests && call.recorder.Header().Get("Retry-After") == "" {
				t.Fatal("shared throttle omitted Retry-After")
			}
		}
		checkStatus(signin(server, "bearer", email, adminPassword), http.StatusTooManyRequests)
		checkStatus(signin(server, "bearer", username, ownPassword), http.StatusCreated)
		var sharedCount, networkCount int
		shared := rateDigest(server.config.RateLimitKey, "signin", "admin-password", []byte("shared"))
		network := rateDigest(server.config.RateLimitKey, "signin", "network", []byte("198.51.100.0/24"))
		if err := adminDB.QueryRow(ctx, `SELECT (SELECT sum(count) FROM app.rate_limits WHERE operation='signin' AND key_kind='subject' AND key_digest=$1),(SELECT sum(count) FROM app.rate_limits WHERE operation='signin' AND key_kind='network' AND key_digest=$2)`, shared, network).Scan(&sharedCount, &networkCount); err != nil || sharedCount != 12 || networkCount != 8 {
			t.Fatal("fallback did not consume exactly one extra shared subject and no extra network counter")
		}
	})

	for _, change := range []string{"target disabled", "target deleting", "target deleted", "admin disabled", "admin hash rotated", "second admin enabled"} {
		t.Run("issuance recheck/"+change, func(t *testing.T) {
			reset()
			rotatedHash := adminPasswordLegacyHash(rotatedPassword)
			tracer := &adminPasswordQueryHook{match: "SELECT\n\t\t\t\tEXISTS", run: func() {
				switch change {
				case "target disabled":
					exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, principal)
				case "target deleting":
					exec(`UPDATE app.accounts SET state='deleting' WHERE id=$1`, account)
				case "target deleted":
					exec(`UPDATE app.accounts SET state='deleted' WHERE id=$1`, account)
				case "admin disabled":
					exec(`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, adminID)
				case "admin hash rotated":
					exec(`UPDATE app.authentication_principals SET password_hash=$1 WHERE id=$2`, rotatedHash, adminID)
				case "second admin enabled":
					exec(`UPDATE app.authentication_principals SET disabled_at=NULL WHERE id=$1`, extraAdmin)
				}
			}}
			hooked := adminPasswordHookedServer(t, server, tracer)
			beforeSessions, beforeAudits := counts()
			checkStatus(signin(hooked, "bearer", username, adminPassword), http.StatusUnauthorized)
			sessions, audits := counts()
			if !tracer.fired || sessions != beforeSessions || audits != beforeAudits {
				t.Fatal("issuance did not recheck state or rolled back incompletely")
			}
		})
	}

	for _, stage := range []string{"SELECT id,password_hash", "app.consume_rate_limit('signin','subject'", "SELECT\n\t\t\t\tEXISTS", "INSERT INTO app.sessions", "authentication.admin_password_signin", "commit"} {
		t.Run("database failure/"+stage, func(t *testing.T) {
			reset()
			tracer := &adminPasswordQueryHook{match: stage, cancel: true}
			if stage == "commit" {
				tracer.skip = 1 // The ordinary rate-limit transaction commits first.
			}
			hooked := adminPasswordHookedServer(t, server, tracer)
			beforeSessions, beforeAudits := counts()
			var logs bytes.Buffer
			call := callEndpoint(http.MethodPost, "/api/session-tokens", adminPasswordSigninBody(t, username, adminPassword), "", "")
			requestID(accessLog(slog.New(slog.NewJSONHandler(&logs, nil)))(http.HandlerFunc(hooked.CreateSessionToken))).ServeHTTP(call.recorder, call.request)
			checkStatus(call, http.StatusServiceUnavailable)
			sessions, audits := counts()
			if !tracer.fired || sessions != beforeSessions || audits != beforeAudits {
				t.Fatal("database failure did not roll back session and audit together")
			}
			for _, secret := range []string{username, adminPassword, adminHash, legacyHash, "credential_verifier", "password_hash"} {
				if strings.Contains(logs.String()+call.recorder.Body.String(), secret) {
					t.Fatal("failure log or response exposed credential/identity data")
				}
			}
		})
	}
	reset()
}

// Query hooks mutate synthetic state exactly after verification, or inject a
// database cancellation. They do not change production interfaces or DB schema.
type adminPasswordQueryHook struct {
	match  string
	skip   int
	run    func()
	cancel bool
	fired  bool
}

func (h *adminPasswordQueryHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !h.fired && strings.Contains(data.SQL, h.match) {
		if h.skip > 0 {
			h.skip--
			return ctx
		}
		h.fired = true
		if h.run != nil {
			h.run()
		}
		if h.cancel {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			return canceled
		}
	}
	return ctx
}

func (*adminPasswordQueryHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func adminPasswordHookedServer(t *testing.T, server *Server, hook *adminPasswordQueryHook) *Server {
	t.Helper()
	cfg := server.db.Config()
	cfg.ConnConfig.Tracer = hook
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("create synthetic failure pool")
	}
	t.Cleanup(pool.Close)
	copy := *server
	copy.db = pool
	return &copy
}
