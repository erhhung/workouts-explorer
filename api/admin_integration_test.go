package api

import (
	"context"
	"encoding/json"
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
