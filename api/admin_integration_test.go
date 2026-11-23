package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM10AdminFoundationIntegration(t *testing.T) {
	if os.Getenv("M10_ADMIN_TEST_DISPOSABLE") != "1" {
		t.Skip("requires an explicitly owned disposable database")
	}
	ctx := context.Background()
	urls := []string{os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")}
	for _, value := range urls {
		cfg, err := pgxpool.ParseConfig(value)
		if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || !strings.HasPrefix(cfg.ConnConfig.Database, "workouts_admin_test_") {
			t.Fatal("requires loopback workouts_admin_test_* database")
		}
	}
	db, err := pgxpool.New(ctx, urls[0])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ownerDB, err := pgxpool.New(ctx, urls[1])
	if err != nil {
		t.Fatal(err)
	}
	defer ownerDB.Close()
	sender := &recordingSender{}
	server := integrationServer(t, db, sender)
	principal, account := insertSourceTestUser(t, ownerDB)
	admin := insertIngestTestAdministrator(t, ownerDB)
	hash, err := server.passwords.hash(ctx, "synthetic admin password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerDB.Exec(ctx, `UPDATE app.authentication_principals SET username='legacy-admin',canonical_username='legacy-admin',email='administrator@example.test',canonical_email='administrator@example.test',password_hash=$1 WHERE id=$2`, hash, admin); err != nil {
		t.Fatal(err)
	}
	legacy := insertTestSession(t, db, admin, "cookie", strings.Repeat("a", 43))
	ownerCSRF := strings.Repeat("o", 43)
	ownerToken := insertTestSession(t, db, principal, "cookie", ownerCSRF)
	file := t.TempDir() + "/password"
	if err := os.WriteFile(file, []byte("synthetic admin password"), 0600); err != nil {
		t.Fatal(err)
	}
	options := BootstrapAdminOptions{Username: "admin", Email: "administrator@example.test", PasswordFile: file, PasswordMin: 12}
	t.Run("explicit adoption and collision rollback", func(t *testing.T) {
		if BootstrapAdmin(ctx, ownerDB, options) == nil {
			t.Fatal("implicit rename accepted")
		}
		options.AdoptAdminUsername = true
		if _, err := ownerDB.Exec(ctx, `UPDATE app.authentication_principals SET username='admin',canonical_username='admin' WHERE id=$1`, principal); err != nil {
			t.Fatal(err)
		}
		if BootstrapAdmin(ctx, ownerDB, options) == nil {
			t.Fatal("owner collision accepted")
		}
		var name, retainedHash string
		if err := ownerDB.QueryRow(ctx, `SELECT username,password_hash FROM app.authentication_principals WHERE id=$1`, admin).Scan(&name, &retainedHash); err != nil || name != "legacy-admin" || retainedHash != hash {
			t.Fatal("collision mutated administrator")
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.authentication_principals SET username='synthetic-owner',canonical_username='synthetic-owner' WHERE id=$1`, principal); err != nil {
			t.Fatal(err)
		}
		if err := BootstrapAdmin(ctx, ownerDB, options); err != nil {
			t.Fatal(err)
		}
		if err := ownerDB.QueryRow(ctx, `SELECT username,password_hash FROM app.authentication_principals WHERE id=$1`, admin).Scan(&name, &retainedHash); err != nil || name != "admin" || retainedHash != hash {
			t.Fatal("adoption changed identity/hash")
		}
		var revoked, audits int
		if err := ownerDB.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE principal_id=$1 AND revoked_at IS NOT NULL`, admin).Scan(&revoked); err != nil || revoked != 1 {
			t.Fatal("legacy sessions not revoked")
		}
		if err := ownerDB.QueryRow(ctx, `SELECT count(*) FROM app.audit_records WHERE actor_principal_id=$1 AND action='administrator.username_reserved'`, admin).Scan(&audits); err != nil || audits != 1 {
			t.Fatal("adoption not audited")
		}
		options.AdoptAdminUsername = false
		if err := BootstrapAdmin(ctx, ownerDB, options); err != nil {
			t.Fatal("idempotent bootstrap: ", err)
		}
	})
	var browser generated.BrowserSession
	var cookie *http.Cookie
	t.Run("shared login sets only administrator cookie", func(t *testing.T) {
		call := callEndpoint("POST", "/api/session", `{"username":" ADMIN ","password":"synthetic admin password"}`, "", "")
		call.request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: ownerToken})
		server.CreateBrowserSession(call.recorder, call.request)
		if call.recorder.Code != 201 {
			t.Fatalf("login: %d %s", call.recorder.Code, call.recorder.Body.String())
		}
		if err := json.Unmarshal(call.recorder.Body.Bytes(), &browser); err != nil || browser.Identity.Id != compactUUID(admin) || browser.Identity.Role != "administrator" {
			t.Fatal("wrong authenticated identity")
		}
		cookies := call.recorder.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != adminSessionCookieName || !cookies[0].HttpOnly || !cookies[0].Secure {
			t.Fatal("role cookie policy")
		}
		cookie = cookies[0]
		denied := callEndpoint("POST", "/api/session", `{"username":"administrator@example.test","password":"synthetic admin password"}`, "", "")
		server.CreateBrowserSession(denied.recorder, denied.request)
		if denied.recorder.Code != 401 {
			t.Fatal("administrator email bypassed reserved username")
		}
	})
	if cookie == nil {
		t.Fatal("administrator signin prerequisite failed")
	}
	handler := generated.Handler(server)
	request := func(method, path, body, csrf, bearer string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	ownerCookie := &http.Cookie{Name: sessionCookieName, Value: ownerToken}
	t.Run("dual cookies are deterministic and role APIs deny crossing", func(t *testing.T) {
		for _, pair := range [][]*http.Cookie{{ownerCookie, cookie}, {cookie, ownerCookie}} {
			for _, path := range []string{"/api/session", "/api/admin/session", "/api/admin/users", "/api/admin/directory", "/api/admin/config"} {
				w := request("GET", path, "", "", "", pair...)
				if w.Code != 200 {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				validateRecordedResponse(t, "GET", path, w)
			}
		}
		if request("GET", "/api/admin/session", "", "", "", ownerCookie).Code != 401 {
			t.Fatal("admin fell back to owner cookie")
		}
		if request("GET", "/api/session", "", "", "", cookie).Code != 401 {
			t.Fatal("owner fell back to admin cookie")
		}
		if request("GET", "/api/admin/session", "", "", "invalid", cookie, ownerCookie).Code != 401 {
			t.Fatal("invalid bearer fell back")
		}
		ownerBearer := insertTestSession(t, db, principal, "bearer", "")
		if request("GET", "/api/admin/users", "", "", ownerBearer, cookie).Code != 403 {
			t.Fatal("owner bearer obtained admin directory")
		}
		adminBearer := insertTestSession(t, db, admin, "bearer", "")
		for _, path := range []string{"/api/session", "/api/me", "/api/me/preferences", "/api/sources", "/api/jobs", "/api/notifications", "/api/workouts"} {
			if w := request("GET", path, "", "", adminBearer, ownerCookie); w.Code != 403 {
				t.Fatalf("admin accessed owner endpoint %s: %d", path, w.Code)
			}
		}
		if request("GET", "/api/session", "", "", legacy).Code != 401 {
			t.Fatal("revoked legacy session active")
		}
	})
	t.Run("directory and config are bounded projections", func(t *testing.T) {
		w := request("GET", "/api/admin/users?search=synthetic-owner&page=1&pageSize=1", "", "", "", cookie)
		var result generated.AdminUserList
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 1 || result.Items[0].Id != compactUUID(principal) || result.Pagination.TotalItems != 1 {
			t.Fatalf("directory %d %s", w.Code, w.Body.String())
		}
		if request("GET", "/api/admin/users?pageSize=101", "", "", "", cookie).Code != 400 {
			t.Fatal("page maximum ignored")
		}
		if request("GET", "/api/admin/users?search=%25", "", "", "", cookie).Body.String() == w.Body.String() {
			t.Fatal("search wildcard treated as SQL wildcard")
		}
		if request("PATCH", "/api/admin/config", `{}`, browser.CsrfToken, "", cookie).Code != 405 {
			t.Fatal("config is editable")
		}
	})
	t.Run("combined directory retains invitations and replaces accepted identities", func(t *testing.T) {
		read := func() generated.AdminDirectory {
			t.Helper()
			w := request("GET", "/api/admin/directory", "", "", "", cookie)
			if w.Code != 200 {
				t.Fatalf("directory: %d %s", w.Code, w.Body.String())
			}
			validateRecordedResponse(t, "GET", "/api/admin/directory", w)
			var directory generated.AdminDirectory
			if err := json.Unmarshal(w.Body.Bytes(), &directory); err != nil {
				t.Fatal(err)
			}
			return directory
		}
		find := func(email string) generated.AdminDirectoryEntry {
			t.Helper()
			var found []generated.AdminDirectoryEntry
			for _, entry := range read().Items {
				if string(entry.Email) == email {
					found = append(found, entry)
				}
			}
			if len(found) != 1 {
				t.Fatalf("expected one directory identity for %s; got %d", email, len(found))
			}
			return found[0]
		}
		w := request("POST", "/api/admin/invitations", `{"email":"invited@example.test"}`, browser.CsrfToken, "", cookie)
		if w.Code != 201 {
			t.Fatalf("invite: %d %s", w.Code, w.Body.String())
		}
		token := messageToken(t, sender.last())
		var invitation generated.Invitation
		if err := json.Unmarshal(w.Body.Bytes(), &invitation); err != nil {
			t.Fatal(err)
		}
		resendURL := "/api/admin/invitations/" + invitation.Id + "/resend"
		if request("POST", resendURL, "", ownerCSRF, "", cookie).Code != 403 {
			t.Fatal("owner CSRF resent invitation")
		}
		resent := request("POST", resendURL, "", browser.CsrfToken, "", cookie)
		if resent.Code != 200 {
			t.Fatalf("resend: %d %s", resent.Code, resent.Body.String())
		}
		var refreshed generated.Invitation
		if err := json.Unmarshal(resent.Body.Bytes(), &refreshed); err != nil {
			t.Fatal(err)
		}
		if refreshed.ExpiresAt.Sub(refreshed.IssuedAt) != 7*24*time.Hour || !refreshed.ExpiresAt.After(invitation.ExpiresAt) {
			t.Fatal("resend did not grant a fresh period")
		}
		if request("GET", "/api/invitations/"+token, "", "", "").Code != 404 {
			t.Fatal("old invitation token still usable")
		}
		token = messageToken(t, sender.last())
		for range 2 {
			entry := find("invited@example.test")
			if entry.Kind != "invitation" || entry.State != "invited" || !entry.Username.IsNull() || !entry.FullName.IsNull() {
				t.Fatalf("pending entry: %+v", entry)
			}
		}
		registration := request("POST", "/api/registrations", `{"token":"`+token+`","username":"newexplorer","fullName":"New Explorer","password":"synthetic owner password","passwordConfirmation":"synthetic owner password"}`, "", "")
		if registration.Code != 201 {
			t.Fatalf("registration: %d %s", registration.Code, registration.Body.String())
		}
		entry := find("invited@example.test")
		if entry.Kind != "user" || entry.State != "active" || entry.Username.IsNull() || entry.Username.MustGet() != "newexplorer" || entry.RegisteredAt.IsNull() {
			t.Fatalf("accepted entry: %+v", entry)
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE canonical_email='invited@example.test'`); err != nil {
			t.Fatal(err)
		}
		if find("invited@example.test").State != "disabled" {
			t.Fatal("disabled identity reported active")
		}
		w = request("POST", "/api/admin/invitations", `{"email":"expired@example.test"}`, browser.CsrfToken, "", cookie)
		if w.Code != 201 {
			t.Fatal("expiry invitation: ", w.Code)
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.invitation_tokens SET issued_at=issued_at-interval '8 days',expires_at=expires_at-interval '8 days' WHERE invitation_id=(SELECT id FROM app.invitations WHERE canonical_email='expired@example.test')`); err != nil {
			t.Fatal(err)
		}
		if find("expired@example.test").State != "expired" {
			t.Fatal("expired invitation reported invited")
		}
		expired := find("expired@example.test")
		revokeURL := "/api/admin/invitations/" + expired.Id
		if request("DELETE", revokeURL, "", ownerCSRF, "", cookie).Code != 403 {
			t.Fatal("owner CSRF revoked invitation")
		}
		if response := request("DELETE", revokeURL, "", browser.CsrfToken, "", cookie); response.Code != 204 {
			t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
		}
		for _, row := range read().Items {
			if string(row.Email) == "expired@example.test" {
				t.Fatal("revoked invitation remains in directory")
			}
		}
		if request("POST", "/api/admin/invitations", `{"email":"expired@example.test"}`, browser.CsrfToken, "", cookie).Code != 201 {
			t.Fatal("revoked email cannot be reinvited")
		}
		ownerBearer := insertTestSession(t, db, principal, "bearer", "")
		if request("GET", "/api/admin/directory", "", "", ownerBearer, cookie).Code != 403 {
			t.Fatal("owner obtained combined directory")
		}
	})
	t.Run("resend traverses JSON middleware and real SMTP transport", func(t *testing.T) {
		created := request("POST", "/api/admin/invitations", `{"email":"router@example.test"}`, browser.CsrfToken, "", cookie)
		if created.Code != 201 {
			t.Fatal("router invitation prerequisite: ", created.Code)
		}
		var invitation generated.Invitation
		if err := json.Unmarshal(created.Body.Bytes(), &invitation); err != nil {
			t.Fatal(err)
		}
		address, transcript := startPlainSMTPServer(t)
		cfg := server.config
		cfg.SMTP.Address = address
		cfg.SMTP.FromAddress = "sender@example.test"
		cfg.SMTP.AllowInsecureLocal = true
		context, cancel := context.WithCancel(ctx)
		defer cancel()
		router, err := NewHandlerContext(context, cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/admin/invitations/" + invitation.Id + "/resend"
		call := func(body string, contentType bool) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", browser.CsrfToken)
			if contentType {
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			return w
		}
		if call("", false).Code != 415 {
			t.Fatal("POST content-type contract changed")
		}
		response := call("{}", true)
		if response.Code != 200 {
			t.Fatalf("full-router resend: %d %s", response.Code, response.Body.String())
		}
		if !strings.Contains(transcript(), "Subject: Your Workouts Explorer invitation") {
			t.Fatal("full-router resend did not send an SMTP message")
		}
	})
	t.Run("usage is a fenced aggregate and does not leak tenant scope", func(t *testing.T) {
		if !database.Ready(ctx, db) {
			t.Fatal("runtime32 readiness rejected the catalog")
		}
		workerDB, err := pgxpool.New(ctx, os.Getenv("WORKER_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer workerDB.Close()
		if !database.Ready(ctx, workerDB) {
			t.Fatal("runtime32 worker readiness rejected the catalog")
		}
		for _, role := range []string{"workouts_coverage_worker"} {
			cfg, err := pgxpool.ParseConfig(urls[0])
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.User = role
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			ready := database.Ready(ctx, pool)
			pool.Close()
			if !ready {
				t.Fatalf("runtime32 %s readiness rejected the catalog", role)
			}
		}
		insertWorkoutReadFixtures(t, ownerDB, workerDB, account)
		url := "/api/admin/users/" + compactUUID(principal) + "/usage?timezone=America/Los_Angeles"
		w := request("GET", url, "", "", "", cookie)
		if w.Code != 200 {
			t.Fatalf("usage: %d %s", w.Code, w.Body.String())
		}
		validateRecordedResponse(t, "GET", url, w)
		var usage generated.AdminUserUsage
		if json.Unmarshal(w.Body.Bytes(), &usage) != nil || usage.WorkoutCount != 3 || usage.RouteCount != 2 || usage.LastLoginAt.IsNull() || usage.LastWorkoutDate.IsNull() {
			t.Fatalf("usage values: %s", w.Body.String())
		}
		ownerBearer := insertTestSession(t, db, principal, "bearer", "")
		if request("GET", url, "", "", ownerBearer, cookie).Code != 403 {
			t.Fatal("owner accessed another role's usage API")
		}
		if request("GET", "/api/admin/users/"+compactUUID(admin)+"/usage?timezone=UTC", "", "", "", cookie).Code != 404 {
			t.Fatal("administrator exposed as a personal account")
		}
		if request("GET", "/api/admin/users/"+compactUUID(principal)+"/usage?timezone=Invalid/Zone", "", "", "", cookie).Code != 400 {
			t.Fatal("invalid time zone accepted")
		}
		var sessionID string
		verifier, _ := tokenVerifier(ownerBearer)
		if err := ownerDB.QueryRow(ctx, `SELECT id::text FROM app.sessions WHERE principal_id=$1 AND credential_verifier=$2`, principal, verifier).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id','',true)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT * FROM app.admin_account_usage($1,$2,'UTC')`, browser.Id, principal); err != nil {
			t.Fatal(err)
		}
		var scope *string
		var count int
		if err := tx.QueryRow(ctx, `SELECT current_setting('app.account_id',true)`).Scan(&scope); err != nil || scope != nil && *scope != "" {
			t.Fatal("usage leaked account scope")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.workouts`).Scan(&count); err != nil || count != 0 {
			t.Fatal("usage authorized raw workout rows")
		}
		if _, err := tx.Exec(ctx, `SELECT * FROM app.admin_account_usage($1,$2,'UTC')`, sessionID, principal); err == nil {
			t.Fatal("owner session called admin definer")
		}
		if _, err := ownerDB.Exec(ctx, `DELETE FROM app.sessions WHERE id=$1`, sessionID); err != nil {
			t.Fatal(err)
		}
		var lastLogin *time.Time
		if err := ownerDB.QueryRow(ctx, `SELECT last_login_at FROM app.authentication_principals WHERE id=$1`, principal).Scan(&lastLogin); err != nil || lastLogin == nil {
			t.Fatal("session cleanup erased last login")
		}
	})
	t.Run("active sessions are projected and revocation is scoped, audited and idempotent", func(t *testing.T) {
		target, targetAccount := insertSourceTestUser(t, ownerDB)
		path := "/api/admin/users/" + compactUUID(target) + "/sessions"
		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := ownerDB.Exec(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
		}
		read := func() generated.AdminDirectoryEntry {
			t.Helper()
			w := request("GET", "/api/admin/directory", "", "", "", cookie)
			validateRecordedResponse(t, "GET", "/api/admin/directory", w)
			var directory generated.AdminDirectory
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &directory) != nil {
				t.Fatalf("directory %d %s", w.Code, w.Body.String())
			}
			for _, entry := range directory.Items {
				if entry.Id == compactUUID(target) {
					return entry
				}
			}
			t.Fatal("target missing")
			return generated.AdminDirectoryEntry{}
		}
		if !read().ActiveSessionExpiresAt.IsNull() {
			t.Fatal("sessionless user has active sessions")
		}
		expired := insertTestSession(t, db, target, "bearer", "")
		expiredVerifier, _ := tokenVerifier(expired)
		exec(`UPDATE app.sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE credential_verifier=$1`, expiredVerifier)
		revoked := insertTestSession(t, db, target, "bearer", "")
		revokedVerifier, _ := tokenVerifier(revoked)
		exec(`UPDATE app.sessions SET revoked_at=clock_timestamp() WHERE credential_verifier=$1`, revokedVerifier)
		if !read().ActiveSessionExpiresAt.IsNull() {
			t.Fatal("expired or revoked token is considered active")
		}
		bearer := insertTestSession(t, db, target, "bearer", "")
		if read().ActiveSessionExpiresAt.IsNull() {
			t.Fatal("bearer-only user has no active sessions")
		}
		ownerBrowser := insertTestSession(t, db, target, "cookie", ownerCSRF)
		userCookie := &http.Cookie{Name: sessionCookieName, Value: ownerBrowser}
		if read().ActiveSessionExpiresAt.IsNull() {
			t.Fatal("active browser/bearer user has no active sessions")
		}
		for _, csrf := range []string{"", ownerCSRF} {
			if w := request("DELETE", path, "", csrf, "", cookie); w.Code != 403 {
				t.Fatalf("CSRF accepted: %d", w.Code)
			}
		}
		if w := request("DELETE", path, "", "", bearer); w.Code != 403 {
			t.Fatalf("owner revoked sessions: %d", w.Code)
		}
		if w := request("DELETE", path, "", ownerCSRF, "", userCookie); w.Code != 401 {
			t.Fatalf("owner cookie accepted: %d", w.Code)
		}
		for _, id := range []string{compactUUID(admin), compactUUID(uuid.New())} {
			if w := request("DELETE", "/api/admin/users/"+id+"/sessions", "", browser.CsrfToken, "", cookie); w.Code != 404 {
				t.Fatalf("invalid target accepted: %d", w.Code)
			}
		}
		// Unlike suspension/deletion, session-only revocation must not contend
		// with the account lock held by an ingest file transaction.
		busy, err := ownerDB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Rollback(ctx)
		if _, err := busy.Exec(ctx, `SELECT id FROM app.accounts WHERE id=$1 FOR NO KEY UPDATE`, targetAccount); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			w := request("DELETE", path, "", browser.CsrfToken, "", cookie)
			if w.Code != 204 {
				t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
			}
			validateRecordedResponse(t, "DELETE", path, w)
		}
		if err := busy.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		var valid bool
		if err := ownerDB.QueryRow(ctx, `SELECT
		 (SELECT count(*)=1 FROM app.audit_records WHERE target_id=$1 AND action='user.sessions_revoked')
		 AND (SELECT state='active' FROM app.accounts WHERE id=$2)
		 AND (SELECT password_hash='test-only-hash' FROM app.authentication_principals WHERE id=$1)
		 AND NOT EXISTS(SELECT 1 FROM app.sessions WHERE principal_id=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp())`, target, targetAccount).Scan(&valid); err != nil || !valid {
			t.Fatalf("revocation invariants: %v", err)
		}
		if !read().ActiveSessionExpiresAt.IsNull() {
			t.Fatal("revoked sessions still advertised")
		}
		if request("GET", "/api/session", "", "", bearer).Code != 401 || request("GET", "/api/session", "", "", "", userCookie).Code != 401 {
			t.Fatal("revoked browser/bearer credentials accepted")
		}
		if request("GET", "/api/session", "", "", "", ownerCookie).Code != 200 || request("GET", "/api/admin/session", "", "", "", cookie).Code != 200 {
			t.Fatal("another owner's or administrator's session revoked")
		}
		hash, err := server.passwords.hash(ctx, "synthetic owner password")
		if err != nil {
			t.Fatal(err)
		}
		name := "sessions" + compactUUID(target)[20:]
		exec(`UPDATE app.authentication_principals SET password_hash=$2,username=$3,canonical_username=lower($3) WHERE id=$1`, target, hash, name)
		body, _ := json.Marshal(map[string]string{"username": name, "password": "synthetic owner password"})
		if w := request("POST", "/api/session", string(body), "", ""); w.Code != 201 {
			t.Fatalf("new sign-in rejected: %d %s", w.Code, w.Body.String())
		}
		if read().ActiveSessionExpiresAt.IsNull() {
			t.Fatal("newly signed-in user not advertised")
		}
		issuance, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer issuance.Rollback(ctx)
		var state string
		if err := issuance.QueryRow(ctx, `SELECT app.lock_signin_principal($1,$2)`, target, hash).Scan(&state); err != nil || state != "active" {
			t.Fatalf("sign-in fence: %s %v", state, err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- request("DELETE", path, "", browser.CsrfToken, "", cookie) }()
		select {
		case <-done:
			t.Fatal("revocation passed an uncommitted sign-in fence")
		case <-time.After(100 * time.Millisecond):
		}
		if err := issuance.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case w := <-done:
			if w.Code != 204 {
				t.Fatalf("serialized revoke: %d %s", w.Code, w.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("serialized revocation did not complete")
		}
	})
	t.Run("OSM catalog refresh is administrator-only, CSRF-protected and coalesced", func(t *testing.T) {
		ownerBearer := insertTestSession(t, db, principal, "bearer", "")
		server.config.OSMCatalogEnabled = true
		defer func() { server.config.OSMCatalogEnabled = false }()
		const path = "/api/admin/osm/catalog-refreshes"
		for _, method := range []string{"GET", "POST"} {
			endpoint := "/api/admin/osm/regions"
			body := ""
			if method == "POST" {
				endpoint = path
				body = "{}"
			}
			if w := request(method, endpoint, body, "", "", ownerCookie); w.Code != 401 {
				t.Fatalf("owner cookie accepted: %d", w.Code)
			}
			if w := request(method, endpoint, body, "", ownerBearer); w.Code != 403 {
				t.Fatalf("owner bearer accepted: %d", w.Code)
			}
		}
		for _, csrf := range []string{"", ownerCSRF} {
			if w := request("POST", path, "{}", csrf, "", cookie); w.Code != 403 {
				t.Fatalf("wrong CSRF accepted: %d", w.Code)
			}
		}
		if w := request("POST", path, `{"sourceUrl":"https://arbitrary.invalid/extract"}`, browser.CsrfToken, "", cookie); w.Code != 400 {
			t.Fatalf("caller URL accepted: %d", w.Code)
		}
		first := request("POST", path, "{}", browser.CsrfToken, "", cookie)
		second := request("POST", path, "{}", browser.CsrfToken, "", cookie)
		if first.Code != 202 || second.Code != 202 || first.Body.String() != second.Body.String() {
			t.Fatalf("refresh not coalesced: %d %d", first.Code, second.Code)
		}
		validateRecordedResponse(t, "POST", path, first)
		var task struct {
			ID    string `json:"id"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(first.Body.Bytes(), &task); err != nil || task.State != "queued" {
			t.Fatal("invalid task")
		}
		get := request("GET", path+"/"+task.ID, "", "", "", cookie)
		if get.Code != 200 {
			t.Fatalf("task lookup: %d %s", get.Code, get.Body.String())
		}
		validateRecordedResponse(t, "GET", path+"/"+task.ID, get)
		var auditCount int
		if err := ownerDB.QueryRow(ctx, `SELECT count(*) FROM app.audit_records WHERE action='osm.catalog_refresh_requested' AND target_id=$1`, task.ID).Scan(&auditCount); err != nil || auditCount != 1 {
			t.Fatalf("coalesced audit: %d %v", auditCount, err)
		}
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.osm_region_catalog(id,provider,payload) VALUES
		('geofabrik:native-api','geofabrik','{"id":"geofabrik:native-api","provider":"geofabrik","name":"Native catalog","boundary":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}'),
		('other-provider:native-api','other-provider','{"id":"other-provider:native-api","provider":"other-provider","name":"unconfigured-canary"}')`); err != nil {
			t.Fatal(err)
		}
		defer ownerDB.Exec(ctx, `DELETE FROM app.osm_region_catalog WHERE id IN('geofabrik:native-api','other-provider:native-api')`)
		list := request("GET", "/api/admin/osm/regions", "", "", "", cookie)
		if list.Code != 200 || !strings.Contains(list.Body.String(), "Native Catalog") || strings.Contains(list.Body.String(), "unconfigured-canary") {
			t.Fatalf("catalog projection: %d %s", list.Code, list.Body.String())
		}
		validateRecordedResponse(t, "GET", "/api/admin/osm/regions", list)
		if strings.Contains(list.Body.String(), "coordinates") {
			t.Fatal("bulk catalog includes per-region boundary geometry")
		}
		boundaryPath := "/api/admin/osm/region-boundary?regionId=geofabrik%3Anative-api"
		boundary := request("GET", boundaryPath, "", "", "", cookie)
		if boundary.Code != 200 || !strings.Contains(boundary.Body.String(), "coordinates") {
			t.Fatalf("boundary lookup: %d %s", boundary.Code, boundary.Body.String())
		}
		validateRecordedResponse(t, "GET", boundaryPath, boundary)
		if request("GET", boundaryPath, "", "", ownerBearer).Code != 403 || request("GET", boundaryPath, "", "", "", ownerCookie).Code != 401 {
			t.Fatal("owner reached admin boundary endpoint")
		}
		if request("GET", "/api/admin/osm/region-boundary?regionId=missing", "", "", "", cookie).Code != 404 {
			t.Fatal("unknown boundary did not return 404")
		}
		if _, err := db.Exec(ctx, `UPDATE app.osm_catalog_refresh_jobs SET state='failed' WHERE id=$1`, task.ID); err == nil {
			t.Fatal("API has worker update authority")
		}
	})
	t.Run("OSM processing settings are persisted, audited and revision-fenced", func(t *testing.T) {
		const path = "/api/admin/osm/processing-settings"
		ownerBearer := insertTestSession(t, db, principal, "bearer", "")
		if request("GET", path, "", "", ownerBearer).Code != 403 || request("GET", path, "", "", "", ownerCookie).Code != 401 {
			t.Fatal("owner reached OSM settings")
		}
		get := request("GET", path, "", "", "", cookie)
		if get.Code != 200 {
			t.Fatal(get.Code, get.Body.String())
		}
		validateRecordedResponse(t, "GET", path, get)
		var settings osmProcessingSettings
		if err := json.Unmarshal(get.Body.Bytes(), &settings); err != nil || !settings.MonitorDatabaseStorage {
			t.Fatal("monitoring must default on", settings, err)
		}
		body := fmt.Sprintf(`{"monitorDatabaseStorage":false,"revision":%d}`, settings.Revision)
		if request("PATCH", path, body, "", "", cookie).Code != 403 {
			t.Fatal("settings accepted missing CSRF")
		}
		for _, invalid := range []string{`{"monitorDatabaseStorage":null,"revision":1}`, `{"monitorDatabaseStorage":false}`, `{"monitorDatabaseStorage":false,"revision":1,"extra":true}`} {
			if request("PATCH", path, invalid, browser.CsrfToken, "", cookie).Code != 400 {
				t.Fatal("invalid settings accepted", invalid)
			}
		}
		saved := request("PATCH", path, body, browser.CsrfToken, "", cookie)
		if saved.Code != 200 {
			t.Fatal(saved.Code, saved.Body.String())
		}
		validateRecordedResponse(t, "PATCH", path, saved)
		if replay := request("PATCH", path, body, browser.CsrfToken, "", cookie); replay.Code != 200 || replay.Body.String() != saved.Body.String() {
			t.Fatal("settings retry not idempotent")
		}
		stale := fmt.Sprintf(`{"monitorDatabaseStorage":true,"revision":%d}`, settings.Revision)
		if request("PATCH", path, stale, browser.CsrfToken, "", cookie).Code != 409 {
			t.Fatal("stale settings overwrote a newer choice")
		}
		if err := json.Unmarshal(saved.Body.Bytes(), &settings); err != nil {
			t.Fatal(err)
		}
		if settings.MonitorDatabaseStorage {
			t.Fatal("monitoring was not disabled")
		}
		body = fmt.Sprintf(`{"monitorDatabaseStorage":true,"revision":%d}`, settings.Revision)
		if response := request("PATCH", path, body, browser.CsrfToken, "", cookie); response.Code != 200 {
			t.Fatal("settings restore failed", response.Body.String())
		}
		var audits int
		if err := ownerDB.QueryRow(ctx, `SELECT count(*) FROM app.audit_records WHERE action IN('osm.storage_monitoring_enabled','osm.storage_monitoring_disabled')`).Scan(&audits); err != nil || audits != 2 {
			t.Fatal("settings changes not audited exactly once", audits, err)
		}
	})
	t.Run("region updates admit coalesced commands and retain ordered cancellation history", func(t *testing.T) {
		server.config.OSMUpdatesEnabled = true
		defer func() { server.config.OSMUpdatesEnabled = false }()
		server.config.OSMCatalogEnabled = true
		defer func() { server.config.OSMCatalogEnabled = false }()
		const region = "geofabrik:native-update"
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.osm_region_catalog(id,provider,payload) VALUES($1,'geofabrik',jsonb_build_object('id',$1::text,'provider','geofabrik','name','Native update','available',true))`, region); err != nil {
			t.Fatal(err)
		}
		const path = "/api/admin/osm/region-updates"
		body := `{"regionId":"` + region + `","action":"download"}`
		ownerBearer := insertTestSession(t, db, principal, "bearer", "")
		if request("POST", path, body, "", ownerBearer).Code != 403 || request("POST", path, body, ownerCSRF, "", ownerCookie).Code != 401 {
			t.Fatal("owner reached region update admission")
		}
		if request("POST", path, body, "", "", cookie).Code != 403 {
			t.Fatal("region update accepted missing CSRF")
		}
		if request("POST", path, `{"regionId":"`+region+`","action":"download","image":"evil"}`, browser.CsrfToken, "", cookie).Code != 400 {
			t.Fatal("caller execution config accepted")
		}
		first := request("POST", path, body, browser.CsrfToken, "", cookie)
		second := request("POST", path, body, browser.CsrfToken, "", cookie)
		if first.Code != 202 || second.Code != 202 || first.Body.String() != second.Body.String() {
			t.Fatalf("region queue admission %d %d: %s", first.Code, second.Code, first.Body.String())
		}
		validateRecordedResponse(t, "POST", path, first)
		var originalPolicy struct {
			ID      string `json:"id"`
			Monitor bool   `json:"monitorDatabaseStorage"`
		}
		if err := json.Unmarshal(first.Body.Bytes(), &originalPolicy); err != nil || !originalPolicy.Monitor {
			t.Fatal("new request did not capture monitoring default", err)
		}
		const settingsPath = "/api/admin/osm/processing-settings"
		var settings osmProcessingSettings
		settingResponse := request("GET", settingsPath, "", "", "", cookie)
		if err := json.Unmarshal(settingResponse.Body.Bytes(), &settings); err != nil {
			t.Fatal(err)
		}
		if changed := request("PATCH", settingsPath, fmt.Sprintf(`{"monitorDatabaseStorage":false,"revision":%d}`, settings.Revision), browser.CsrfToken, "", cookie); changed.Code != 200 {
			t.Fatal("could not change future monitoring policy", changed.Body.String())
		}
		preserved := request("GET", path+"/"+originalPolicy.ID, "", "", "", cookie)
		if !strings.Contains(preserved.Body.String(), `"monitorDatabaseStorage":true`) {
			t.Fatal("global setting changed an existing job")
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET monitor_database_storage=false WHERE id=$1`, originalPolicy.ID); err == nil {
			t.Fatal("captured job policy was mutable")
		}
		var update struct {
			ID           string     `json:"id"`
			State        string     `json:"state"`
			ElapsedSince *time.Time `json:"elapsedSince"`
			Steps        []struct {
				Order int    `json:"order"`
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"steps"`
		}
		if json.Unmarshal(first.Body.Bytes(), &update) != nil || update.State != "waiting" {
			t.Fatal("not queued as Waiting")
		}
		detail := request("GET", path+"/"+update.ID, "", "", "", cookie)
		if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &update) != nil || len(update.Steps) != 58 || update.Steps[0].Name != "prepare-source" || update.Steps[1].Name != "download" || update.Steps[20].Name != "clip-replacements" {
			t.Fatalf("ordered progress: %d %s", detail.Code, detail.Body.String())
		}
		validateRecordedResponse(t, "GET", path+"/"+update.ID, detail)
		batchStart := time.Now().UTC().Truncate(time.Millisecond).Add(-64 * time.Second)
		for _, q := range []string{`UPDATE app.osm_region_updates SET state='processing',current_order=20,elapsed_total_ms=2000000 WHERE id=$1`,
			`UPDATE app.osm_region_update_steps SET state='running' WHERE update_id=$1 AND task_order=20`} {
			if _, err := ownerDB.Exec(ctx, q, update.ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.osm_region_update_events(update_id,event_type,task_order,happened_at,outcome) VALUES
            ($1,'start',20,$2::timestamptz-interval '1 hour',NULL),
            ($1,'finish',NULL,$2::timestamptz-interval '30 minutes','paused'),
            ($1,'osm.region_update_resume',NULL,$2::timestamptz-interval '1 minute',NULL),
            ($1,'start',20,$2,NULL),($1,'start',20,$2::timestamptz+interval '1 second',NULL)`, update.ID, batchStart); err != nil {
			t.Fatal(err)
		}
		assertElapsedSince := func(want *time.Time) {
			t.Helper()
			response := request("GET", path+"/"+update.ID, "", "", "", cookie)
			update.ElapsedSince = nil
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &update) != nil {
				t.Fatal("elapsed projection unavailable", response.Code, response.Body.String())
			}
			if (update.ElapsedSince == nil) != (want == nil) || (want != nil && !update.ElapsedSince.Equal(*want)) {
				t.Fatal("elapsed projection crossed a batch/resume boundary", update.ElapsedSince, want)
			}
			validateRecordedResponse(t, "GET", path+"/"+update.ID, response)
		}
		assertElapsedSince(&batchStart)
		for _, state := range []string{"paused", "waiting"} {
			if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state=$2 WHERE id=$1`, update.ID, state); err != nil {
				t.Fatal(err)
			}
			assertElapsedSince(nil)
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='processing' WHERE id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.osm_region_update_events(update_id,event_type,task_order) VALUES($1,'complete',20)`, update.ID); err != nil {
			t.Fatal(err)
		}
		assertElapsedSince(nil)
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='waiting',current_order=0,elapsed_total_ms=0 WHERE id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_update_steps SET state='pending' WHERE update_id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		detail = request("GET", path+"/"+update.ID, "", "", "", cookie)
		if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &update) != nil {
			t.Fatal("reset elapsed fixture progress unavailable")
		}
		cancel := request("POST", path+"/"+update.ID+"/commands", `{"action":"cancel"}`, browser.CsrfToken, "", cookie)
		if cancel.Code != 202 || !strings.Contains(cancel.Body.String(), `"state":"canceled"`) {
			t.Fatalf("waiting cancellation: %d %s", cancel.Code, cancel.Body.String())
		}
		retained := request("GET", path+"/"+update.ID, "", "", "", cookie)
		if retained.Code != 200 || !strings.Contains(retained.Body.String(), "clip-replacements") {
			t.Fatal("cancel discarded progress history")
		}
		for _, step := range update.Steps {
			if step.State != "pending" {
				t.Fatal("unstarted task falsely marked running")
			}
		}
		if _, err := db.Exec(ctx, `UPDATE app.osm_region_updates SET slot_held=true WHERE id=$1`, update.ID); err == nil {
			t.Fatal("API can claim execution capacity")
		}
		commandPath := path + "/" + update.ID + "/commands"
		if request("POST", commandPath, `{"action":"clear"}`, "", "", cookie).Code != 403 {
			t.Fatal("clear accepted missing CSRF")
		}
		for i := 0; i < 2; i++ {
			cleared := request("POST", commandPath, `{"action":"clear"}`, browser.CsrfToken, "", cookie)
			if cleared.Code != 202 || !strings.Contains(cleared.Body.String(), `"cleared":true`) {
				t.Fatalf("clear canceled status: %d %s", cleared.Code, cleared.Body.String())
			}
		}
		if request("POST", commandPath, `{"action":"resume"}`, browser.CsrfToken, "", cookie).Code != 409 {
			t.Fatal("cleared history resumed")
		}
		var auditCount int
		if err := ownerDB.QueryRow(ctx, `SELECT count(*) FROM app.osm_region_update_events WHERE update_id=$1 AND event_type='osm.region_update_clear'`, update.ID).Scan(&auditCount); err != nil || auditCount != 1 {
			t.Fatal("clear was not idempotent", err)
		}
		assertBaseline := func(generation *int64) {
			t.Helper()
			list := request("GET", "/api/admin/osm/regions", "", "", "", cookie)
			var result struct {
				Regions []struct {
					ID         string           `json:"id"`
					Generation *int64           `json:"generation"`
					Update     *json.RawMessage `json:"update"`
				} `json:"regions"`
			}
			if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &result) != nil {
				t.Fatal("cleared catalog unavailable")
			}
			for _, r := range result.Regions {
				if r.ID == region {
					if r.Update != nil || ((r.Generation == nil) != (generation == nil)) {
						t.Fatal("clear revived an older overlay or lost baseline")
					}
					return
				}
			}
			t.Fatal("cleared region missing")
		}
		assertBaseline(nil)
		retry := request("POST", path, body, browser.CsrfToken, "", cookie)
		if retry.Code != 202 || json.Unmarshal(retry.Body.Bytes(), &update) != nil {
			t.Fatal("new run after clear unavailable")
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='failed',current_order=2,resumable=false WHERE id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		commandPath = path + "/" + update.ID + "/commands"
		resumed := request("POST", commandPath, `{"action":"resume"}`, browser.CsrfToken, "", cookie)
		if resumed.Code != 202 || !strings.Contains(resumed.Body.String(), `"state":"waiting"`) {
			t.Fatalf("pre-checkpoint failure could not retry: %d %s", resumed.Code, resumed.Body.String())
		}
		if request("POST", commandPath, `{"action":"clear"}`, browser.CsrfToken, "", cookie).Code != 409 {
			t.Fatal("active work cleared")
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='failed',resumable=true WHERE id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_catalog SET payload=payload||'{"generation":18,"buildId":62}'::jsonb WHERE id=$1`, region); err != nil {
			t.Fatal(err)
		}
		if request("POST", commandPath, `{"action":"clear"}`, browser.CsrfToken, "", cookie).Code != 202 {
			t.Fatal("checkpointed failure could not clear")
		}
		generation := int64(62)
		assertBaseline(&generation)
		completedID := uuid.New()
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,promoted,generation_id,created_at,started_at,finished_at,elapsed_total_ms)
        VALUES($1,$2,$3,'download','succeeded',true,62,'2026-11-08T00:00:00Z','2026-11-07T23:59:00Z','2026-11-08T16:24:23Z',59063000)`, completedID, admin, region); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated,state,started_at,finished_at)
        VALUES($1,1,'download',false,'completed','2026-11-08T00:00:00Z','2026-11-08T00:01:00Z')`, completedID); err != nil {
			t.Fatal(err)
		}
		elapsedList := request("GET", "/api/admin/osm/regions", "", "", "", cookie)
		if elapsedList.Code != 200 || !strings.Contains(elapsedList.Body.String(), `"generationElapsedSeconds":59063`) {
			t.Fatalf("promoted duration missing behind cleared/newer attempt: %d %s", elapsedList.Code, elapsedList.Body.String())
		}
		retained = request("GET", path+"/"+update.ID, "", "", "", cookie)
		if retained.Code != 200 || !strings.Contains(retained.Body.String(), "clip-replacements") {
			t.Fatal("clear deleted run history")
		}
		deleteBody := `{"regionId":"` + region + `","action":"delete"}`
		if request("POST", path, deleteBody, "", "", cookie).Code != 403 || request("POST", path, deleteBody, "", ownerBearer).Code != 403 {
			t.Fatal("region deletion authorization/CSRF bypass")
		}
		fresh := request("POST", path, body, browser.CsrfToken, "", cookie)
		if !strings.Contains(fresh.Body.String(), `"monitorDatabaseStorage":false`) {
			t.Fatal("new request ignored the disabled default", fresh.Body.String())
		}
		if fresh.Code != 202 || json.Unmarshal(fresh.Body.Bytes(), &update) != nil {
			t.Fatal("fresh deletion fixture", fresh.Body.String())
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='processing',slot_held=true WHERE id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		if request("POST", path, deleteBody, browser.CsrfToken, "", cookie).Code != 409 {
			t.Fatal("deletion raced an active updater")
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='paused',slot_held=false,resumable=true WHERE id=$1`, update.ID); err != nil {
			t.Fatal(err)
		}
		deletion := request("POST", path, deleteBody, browser.CsrfToken, "", cookie)
		if deletion.Code != 202 || !strings.Contains(deletion.Body.String(), `"state":"deleting"`) {
			t.Fatal("deletion not admitted", deletion.Code, deletion.Body.String())
		}
		validateRecordedResponse(t, "POST", path, deletion)
		var deleting struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(deletion.Body.Bytes(), &deleting); err != nil {
			t.Fatal(err)
		}
		duplicate := request("POST", path, deleteBody, browser.CsrfToken, "", cookie)
		if duplicate.Code != 202 || duplicate.Body.String() != deletion.Body.String() {
			t.Fatal("deletion duplicated")
		}
		if request("POST", path, body, browser.CsrfToken, "", cookie).Code != 409 {
			t.Fatal("download raced deletion")
		}
		if request("POST", path+"/"+update.ID+"/commands", `{"action":"resume"}`, browser.CsrfToken, "", cookie).Code != 409 {
			t.Fatal("superseded checkpoint resumed")
		}
		for _, action := range []string{"pause", "resume", "cancel", "clear"} {
			if request("POST", path+"/"+deleting.ID+"/commands", `{"action":"`+action+`"}`, browser.CsrfToken, "", cookie).Code != 409 {
				t.Fatal("deletion accepted", action)
			}
		}
		deletionDetail := request("GET", path+"/"+deleting.ID, "", "", "", cookie)
		if deletionDetail.Code != 200 || strings.Count(deletionDetail.Body.String(), `"order":`) != 3 || !strings.Contains(deletionDetail.Body.String(), "reclaim-storage") {
			t.Fatal("deletion progress incorrect", deletionDetail.Body.String())
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_updates SET state='succeeded' WHERE id=$1`, deleting.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ownerDB.Exec(ctx, `UPDATE app.osm_region_catalog SET payload=payload||'{"generation":null,"buildId":null}' WHERE id=$1`, region); err != nil {
			t.Fatal(err)
		}
		if next := request("POST", path, body, browser.CsrfToken, "", cookie); next.Code != 202 || next.Body.String() == deletion.Body.String() {
			t.Fatal("deleted region cannot be downloaded again", next.Code, next.Body.String())
		}
		latest := request("GET", settingsPath, "", "", "", cookie)
		if err := json.Unmarshal(latest.Body.Bytes(), &settings); err != nil {
			t.Fatal(err)
		}
		if restored := request("PATCH", settingsPath, fmt.Sprintf(`{"monitorDatabaseStorage":true,"revision":%d}`, settings.Revision), browser.CsrfToken, "", cookie); restored.Code != 200 {
			t.Fatal("default monitoring restore failed")
		}
	})
	t.Run("CSRF and independent signout", func(t *testing.T) {
		if request("DELETE", "/api/admin/session", "", ownerCSRF, "", cookie, ownerCookie).Code != 403 {
			t.Fatal("owner CSRF revoked admin")
		}
		w := request("DELETE", "/api/admin/session", "", browser.CsrfToken, "", cookie, ownerCookie)
		if w.Code != 204 {
			t.Fatal("administrator signout: ", w.Code)
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != adminSessionCookieName || cookies[0].MaxAge != -1 {
			t.Fatal("signout cleared wrong cookie")
		}
		if request("GET", "/api/admin/session", "", "", "", cookie, ownerCookie).Code != 401 {
			t.Fatal("revoked administrator active")
		}
		if request("GET", "/api/session", "", "", "", cookie, ownerCookie).Code != 200 {
			t.Fatal("owner session revoked with administrator")
		}
	})
	t.Run("canonical reservation precedes token consumption", func(t *testing.T) {
		for _, name := range []string{"admin", "ADMIN", "\uff41\uff44\uff4d\uff49\uff4e"} {
			call := callEndpoint("POST", "/api/registrations", `{"token":"`+strings.Repeat("A", 43)+`","username":"`+name+`","fullName":"Owner","password":"synthetic owner password","passwordConfirmation":"synthetic owner password"}`, "", "")
			server.CreateRegistration(call.recorder, call.request)
			if call.recorder.Code != 400 || !strings.Contains(call.recorder.Body.String(), `"code":"reserved"`) {
				t.Fatalf("reservation %s: %d %s", name, call.recorder.Code, call.recorder.Body.String())
			}
		}
	})
}
