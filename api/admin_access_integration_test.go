package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM10AccountAccessIntegration(t *testing.T) {
	if os.Getenv("M10_ADMIN_TEST_DISPOSABLE") != "1" {
		t.Skip("requires an explicitly owned disposable database")
	}
	ctx := context.Background()
	pools := make([]*pgxpool.Pool, 0, 4)
	for _, key := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL"} {
		cfg, err := pgxpool.ParseConfig(os.Getenv(key))
		if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || !strings.HasPrefix(cfg.ConnConfig.Database, "workouts_admin_test_") {
			t.Fatal("requires loopback workouts_admin_test_* database")
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	db, ownerDB, workerDB, coverageDB := pools[0], pools[1], pools[2], pools[3]
	server := integrationServer(t, db, &recordingSender{})
	principal, account := insertSourceTestUser(t, ownerDB)
	other, _ := insertSourceTestUser(t, ownerDB)
	admin := insertIngestTestAdministrator(t, ownerDB)
	const password = "synthetic owner password"
	hash, err := server.passwords.hash(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := ownerDB.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE app.authentication_principals SET password_hash=$2,username=$3,canonical_username=$3 WHERE id=$1`, principal, hash, "access"+strings.ToLower(compactUUID(principal)[20:]))
	var username, email string
	if err := db.QueryRow(ctx, `SELECT username,email FROM app.authentication_principals WHERE id=$1`, principal).Scan(&username, &email); err != nil {
		t.Fatal(err)
	}
	adminCSRF := strings.Repeat("a", 43)
	adminToken := insertTestSession(t, db, admin, "cookie", adminCSRF)
	adminVerifier, _ := tokenVerifier(adminToken)
	var adminSession uuid.UUID
	if err := db.QueryRow(ctx, `SELECT id FROM app.sessions WHERE credential_verifier=$1`, adminVerifier).Scan(&adminSession); err != nil {
		t.Fatal(err)
	}
	ownerCSRF := strings.Repeat("o", 43)
	ownerCookie := insertTestSession(t, db, principal, "cookie", ownerCSRF)
	ownerBearer := insertTestSession(t, db, principal, "bearer", "")
	otherBearer := insertTestSession(t, db, other, "bearer", "")
	handler := generated.Handler(server)
	call := func(method, path, body, csrf, bearer string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		c := callEndpoint(method, path, body, bearer, csrf)
		if cookie != nil {
			c.request.AddCookie(cookie)
		}
		handler.ServeHTTP(c.recorder, c.request)
		return c.recorder
	}
	adminCookie := &http.Cookie{Name: adminSessionCookieName, Value: adminToken}
	path := "/api/admin/users/" + compactUUID(principal) + "/access"
	access := func(enabled bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(generated.AdminUserAccess{Enabled: enabled})
		return call("PUT", path, string(body), adminCSRF, "", adminCookie)
	}
	fixture := insertWorkoutReadFixtures(t, ownerDB, workerDB, account)
	scanAccount := func(query string, args ...any) (bool, error) {
		t.Helper()
		tx := integrationAccountTransaction(t, ownerDB, account)
		defer tx.Rollback(ctx)
		var result bool
		err := tx.QueryRow(ctx, query, args...).Scan(&result)
		return result, err
	}
	queuedParent, queuedChild := insertLegacyActiveIngest(t, ownerDB, account, fixture.sourceID)
	coverageParent, coverageChild := uuid.New(), uuid.New()
	coverageRunning, coverageLease := uuid.New(), uuid.New()
	tx := integrationAccountTransaction(t, ownerDB, account)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'coverage_update',20)`, []any{coverageParent, account}},
		{`INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority) VALUES($1,$2,$3,'coverage_update_route',20)`, []any{coverageChild, coverageParent, account}},
		{`INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority) VALUES($1,$2,$3,'coverage_update_route',20)`, []any{coverageRunning, coverageParent, account}},
		{`SELECT set_config('app.job_transition','claim',true)`, nil},
		{`UPDATE app.jobs SET status='running',attempt=1,worker_id='synthetic-coverage',lease_token=$2,
		 claimed_at=clock_timestamp(),heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '5 minutes',started_at=clock_timestamp() WHERE id=$1`, []any{coverageRunning, coverageLease}},
		{`INSERT INTO app.account_sync_schedules(account_id,next_run_at,lease_worker,lease_token,lease_expires_at,lease_runtime_version)
		 VALUES($1,clock_timestamp()-interval '1 hour','synthetic-scheduler',$2,clock_timestamp()+interval '5 minutes',32)`, []any{account, uuid.New()}},
	} {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("authority and CSRF", func(t *testing.T) {
		for _, csrf := range []string{"", ownerCSRF} {
			if w := call("PUT", path, `{"enabled":false}`, csrf, "", adminCookie); w.Code != 403 {
				t.Fatalf("CSRF: %d %s", w.Code, w.Body.String())
			}
		}
		if call("PUT", path, `{"enabled":false}`, "", ownerBearer, adminCookie).Code != 403 {
			t.Fatal("owner bearer changed account access")
		}
		for _, body := range []string{`{}`, `{"enabled":null}`} {
			if call("PUT", path, body, adminCSRF, "", adminCookie).Code != 400 {
				t.Fatal("missing enabled accepted")
			}
		}
		for _, id := range []uuid.UUID{admin, uuid.New()} {
			if call("PUT", "/api/admin/users/"+compactUUID(id)+"/access", `{"enabled":false}`, adminCSRF, "", adminCookie).Code != 404 {
				t.Fatal("administrator/unknown target accepted")
			}
		}
	})
	t.Run("disable revokes both credential types and cancels account work", func(t *testing.T) {
		w := access(false)
		if w.Code != 200 || w.Body.String() != "{\"enabled\":false}\n" {
			t.Fatalf("disable: %d %s", w.Code, w.Body.String())
		}
		validateRecordedResponse(t, "PUT", path, w)
		for _, token := range []string{ownerBearer, ownerCookie} {
			var result *httptest.ResponseRecorder
			if token == ownerCookie {
				result = call("GET", "/api/session", "", "", "", &http.Cookie{Name: sessionCookieName, Value: token})
			} else {
				result = call("GET", "/api/session", "", "", token, nil)
			}
			if result.Code != 401 {
				t.Fatal("old credential remained active")
			}
		}
		if call("GET", "/api/session", "", "", otherBearer, nil).Code != 200 || call("GET", "/api/admin/session", "", "", "", adminCookie).Code != 200 {
			t.Fatal("disable affected another identity")
		}
		var safe bool
		if err := ownerDB.QueryRow(ctx, `SELECT
		 (SELECT state='disabled' FROM app.accounts WHERE id=$1)
		 AND (SELECT disabled_at IS NOT NULL AND password_hash=$3 FROM app.authentication_principals WHERE id=$2)
		 AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND status IN ('queued','running') AND cancel_requested_at IS NULL)
		 AND NOT EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=$4)
		 AND EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE job_id=$5)
		 AND (SELECT lease_token IS NULL AND next_run_at<clock_timestamp() FROM app.account_sync_schedules WHERE account_id=$1)`, account, principal, hash, queuedChild, fixture.jobID).Scan(&safe); err != nil || !safe {
			t.Fatalf("suspension/cancellation/snapshot invariant: %v", err)
		}
		w = call("GET", "/api/admin/directory", "", "", "", adminCookie)
		if !strings.Contains(w.Body.String(), `"state":"disabled"`) {
			t.Fatal("directory did not reflect disabled state")
		}
	})
	t.Run("only correct credentials disclose disabled status", func(t *testing.T) {
		for _, identity := range []string{username, email} {
			for _, kind := range []string{"cookie", "bearer"} {
				for _, correct := range []bool{false, true} {
					pw := "incorrect password"
					if correct {
						pw = password
					}
					body, _ := json.Marshal(map[string]string{"username": identity, "password": pw})
					c := callEndpoint("POST", "/api/session", string(body), "", "")
					if kind == "cookie" {
						server.CreateBrowserSession(c.recorder, c.request)
					} else {
						server.CreateSessionToken(c.recorder, c.request)
					}
					if c.recorder.Code != 401 || strings.Contains(c.recorder.Body.String(), "account-disabled") != correct || len(c.recorder.Result().Cookies()) != 0 {
						t.Fatalf("%s credentials correct=%t: %d %s", kind, correct, c.recorder.Code, c.recorder.Body.String())
					}
				}
			}
		}
	})
	t.Run("workers can finish cancellation and retained data remains intact", func(t *testing.T) {
		tx := integrationAccountTransaction(t, workerDB, account)
		var done bool
		if err := tx.QueryRow(ctx, `SELECT app.finish_job($1,'read-fixture',$2,'cancelled')`, fixture.jobID, fixture.childLease).Scan(&done); err != nil || !done {
			t.Fatalf("finish cancellation: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		coverageTx := integrationAccountTransaction(t, coverageDB, account)
		defer coverageTx.Rollback(ctx)
		if err := coverageTx.QueryRow(ctx, `SELECT app.finish_job($1,'synthetic-coverage',$2,'cancelled')`, coverageRunning, coverageLease).Scan(&done); err != nil || !done {
			t.Fatalf("finish Coverage cancellation: %v", err)
		}
		if err := coverageTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if intact, err := scanAccount(`SELECT
		 (SELECT count(*)=3 FROM app.workouts WHERE account_id=$1)
		 AND (SELECT count(*)=2 FROM app.workout_routes WHERE account_id=$1)
		 AND EXISTS(SELECT 1 FROM app.sources WHERE account_id=$1 AND id=$2 AND config_envelope=$3)
		 AND EXISTS(SELECT 1 FROM app.preferences WHERE account_id=$1)
		 AND NOT EXISTS(SELECT 1 FROM app.job_config_snapshots WHERE account_id=$1)`, account, fixture.sourceID, []byte("fixture")); err != nil || !intact {
			t.Fatalf("retained data or terminal snapshot cleanup: %v", err)
		}
		if _, err := ownerDB.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'source_connection_check',20)`, uuid.New(), account); err == nil {
			t.Fatal("post-disable enqueue succeeded")
		}
	})
	t.Run("reenable preserves cancelled work and requires new signin", func(t *testing.T) {
		for range 2 {
			if w := access(true); w.Code != 200 {
				t.Fatalf("enable: %d %s", w.Code, w.Body.String())
			}
		}
		if call("GET", "/api/session", "", "", ownerBearer, nil).Code != 401 {
			t.Fatal("enable restored revoked session")
		}
		if preserved, err := scanAccount(`SELECT
		 (SELECT state='active' FROM app.accounts WHERE id=$1)
		 AND (SELECT disabled_at IS NULL FROM app.authentication_principals WHERE id=$2)
		 AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE id=ANY($3::uuid[]) AND status<>'cancelled')
		 AND (SELECT count(*)=3 FROM app.audit_records WHERE account_id=$1 AND action IN ('account.disabled','account.enabled'))`, account, principal, []uuid.UUID{queuedParent, queuedChild, coverageParent, coverageChild, coverageRunning, fixture.jobID}); err != nil || !preserved {
			t.Fatalf("reactivation invariant: %v", err)
		}
		body, _ := json.Marshal(map[string]string{"username": username, "password": password})
		c := callEndpoint("POST", "/api/session", string(body), "", "")
		server.CreateBrowserSession(c.recorder, c.request)
		if c.recorder.Code != 201 {
			t.Fatalf("new signin: %d %s", c.recorder.Code, c.recorder.Body.String())
		}
		if !database.Ready(ctx, db) || !database.Ready(ctx, workerDB) {
			t.Fatal("folded runtime32 readiness rejected lifecycle contract")
		}
	})
	t.Run("deleting accounts cannot be enabled", func(t *testing.T) {
		exec(`UPDATE app.accounts SET state='deleting' WHERE id=$1`, account)
		if w := access(true); w.Code != 409 {
			t.Fatalf("deleting enable: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("signin fence serializes with suspension", func(t *testing.T) {
		p, a := insertSourceTestUser(t, ownerDB)
		exec(`UPDATE app.authentication_principals SET password_hash=$2 WHERE id=$1`, p, hash)
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var state string
		if err := tx.QueryRow(ctx, `SELECT app.lock_signin_principal($1,$2)`, p, hash).Scan(&state); err != nil || state != "active" {
			t.Fatalf("signin fence: %s %v", state, err)
		}
		cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = ownerDB.Exec(cctx, `SELECT app.set_account_access($1,$2,false,$3,'synthetic-race')`, adminSession, p, uuid.New())
		if err == nil {
			t.Fatal("suspension passed an uncommitted signin")
		}
		if err := tx.Rollback(ctx); err != nil && err != pgx.ErrTxClosed {
			t.Fatal(err)
		}
		exec(`UPDATE app.accounts SET state='disabled' WHERE id=$1`, a)
		if err := db.QueryRow(ctx, `SELECT app.lock_signin_principal($1,$2)`, p, hash).Scan(&state); err != nil || state != "disabled" {
			t.Fatalf("post-suspension signin: %s %v", state, err)
		}
	})
}
