package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	workerapp "github.com/erhhung/workouts-explorer/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminAccountDeletionIntegration(t *testing.T) {
	if os.Getenv("M10_ADMIN_TEST_DISPOSABLE") != "1" {
		t.Skip("owned disposable database required")
	}
	ctx := context.Background()
	var pools []*pgxpool.Pool
	for _, key := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL"} {
		config, err := pgxpool.ParseConfig(os.Getenv(key))
		if err != nil || config.ConnConfig.Host != "127.0.0.1" || !strings.HasPrefix(config.ConnConfig.Database, "workouts_admin_test_") {
			t.Fatal("loopback owned database required")
		}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	db, owner, worker := pools[0], pools[1], pools[2]
	sender := &recordingSender{}
	server := integrationServer(t, db, sender)
	admin := insertIngestTestAdministrator(t, owner)
	adminCSRF := strings.Repeat("a", 43)
	adminToken := insertTestSession(t, db, admin, "cookie", adminCSRF)
	principal, account := insertSourceTestUser(t, owner)
	other, otherAccount := insertSourceTestUser(t, owner)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	username, email := "purge"+suffix, "purge"+suffix+"@example.test"
	if _, err := owner.Exec(ctx, `UPDATE app.authentication_principals SET username=$2,canonical_username=$2,email=$3,canonical_email=$3 WHERE id=$1`, principal, username, email); err != nil {
		t.Fatal(err)
	}
	ownerToken := insertTestSession(t, db, principal, "bearer", "")
	otherToken := insertTestSession(t, db, other, "bearer", "")
	fixture := insertWorkoutReadFixtures(t, owner, worker, account)
	accountDB, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer accountDB.Release()
	if _, err := accountDB.Exec(ctx, `SELECT set_config('app.account_id',$1,false)`, account.String()); err != nil {
		t.Fatal(err)
	}
	var registered bool
	if err := worker.QueryRow(ctx, `SELECT app.register_private_execution($1,'read-fixture',$2,'test-live-pod','test-pod-uid','private')`, fixture.jobID, fixture.childLease).Scan(&registered); err != nil || !registered {
		t.Fatalf("receipt %t %v", registered, err)
	}
	handler := generated.Handler(server)
	call := func(method, path, body, token, csrf string) *httpResult {
		c := callEndpoint(method, path, body, token, csrf)
		if token == "" {
			c.request.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: adminToken})
		}
		handler.ServeHTTP(c.recorder, c.request)
		return &httpResult{code: c.recorder.Code, body: c.recorder.Body.Bytes()}
	}
	path := "/api/admin/users/" + compactUUID(principal)
	if r := call("DELETE", path, "", ownerToken, ""); r.code != 403 {
		t.Fatalf("owner deletion %d", r.code)
	}
	if r := call("DELETE", path, "", "", ""); r.code != 403 {
		t.Fatalf("missing CSRF %d", r.code)
	}
	if r := call("DELETE", "/api/admin/users/"+compactUUID(admin), "", "", adminCSRF); r.code != 404 {
		t.Fatal("administrator target accepted")
	}
	// Ingest holds an account lock across a file commit. A timed-out acceptance
	// must roll back completely and identify only that condition as retryable.
	busy, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Rollback(ctx)
	if _, err := busy.Exec(ctx, `SELECT id FROM app.accounts WHERE id=$1 FOR NO KEY UPDATE`, account); err != nil {
		t.Fatal(err)
	}
	blocked := call("DELETE", path, "", "", adminCSRF)
	var problem generated.Problem
	if blocked.code != 503 || json.Unmarshal(blocked.body, &problem) != nil || problem.Type != "https://workouts-explorer.invalid/problems/account-deletion-busy" {
		t.Fatalf("busy deletion status=%d body=%s", blocked.code, blocked.body)
	}
	var untouched bool
	if err := owner.QueryRow(ctx, `SELECT state='active' AND NOT EXISTS(SELECT 1 FROM app.account_deletions WHERE target_account_id=$1) FROM app.accounts WHERE id=$1`, account).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("busy deletion partially accepted: %v", err)
	}
	if err := busy.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	accepted := call("DELETE", path, "", "", adminCSRF)
	if accepted.code != 202 {
		t.Fatalf("accept %d %s", accepted.code, accepted.body)
	}
	var task generated.AdminAccountDeletion
	if err := json.Unmarshal(accepted.body, &task); err != nil {
		t.Fatal(err)
	}
	again := call("DELETE", path, "", "", adminCSRF)
	var same generated.AdminAccountDeletion
	_ = json.Unmarshal(again.body, &same)
	if again.code != 202 || same.JobId != task.JobId {
		t.Fatal("pending deletion was not coalesced")
	}
	if r := call("GET", "/api/session", "", ownerToken, ""); r.code != 401 {
		t.Fatal("old owner credentials still work")
	}
	if r := call("GET", "/api/session", "", otherToken, ""); r.code != 200 {
		t.Fatal("other account session changed")
	}
	if r := call("PUT", path+"/access", `{"enabled":true}`, "", adminCSRF); r.code != 409 {
		t.Fatal("deletion was re-enabled")
	}
	var state string
	if err := owner.QueryRow(ctx, `SELECT state FROM app.accounts WHERE id=$1`, account).Scan(&state); err != nil || state != "deleting" {
		t.Fatal("deletion state missing")
	}
	runner := workerapp.NewAccountDeletionRunner(worker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	nudge := func() {
		if _, err := owner.Exec(ctx, `UPDATE app.account_deletions SET next_attempt_at=clock_timestamp() WHERE target_account_id=$1`, account); err != nil {
			t.Fatal(err)
		}
	}
	nudge()
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := accountDB.QueryRow(ctx, `SELECT count(*) FROM app.workouts WHERE account_id=$1`, account).Scan(&count); err != nil || count != 3 {
		t.Fatal("purged before running ingest drained")
	}
	tx := integrationAccountTransaction(t, worker, account)
	defer tx.Rollback(ctx)
	var finished bool
	if err := tx.QueryRow(ctx, `SELECT app.finish_job($1,'read-fixture',$2,'canceled')`, fixture.jobID, fixture.childLease).Scan(&finished); err != nil || !finished {
		t.Fatalf("finish cancellation %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	nudge()
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := accountDB.QueryRow(ctx, `SELECT count(*) FROM app.workouts WHERE account_id=$1`, account).Scan(&count); err != nil || count != 3 {
		t.Fatal("purged before staging receipt was cleaned")
	}
	if _, err := worker.Exec(ctx, `SELECT app.clear_private_execution('read-fixture',$1,$2)`, fixture.jobID, fixture.childLease); err != nil {
		t.Fatal(err)
	}
	// A failure in a later table must roll back that batch without restoring
	// prior committed batches. Remove the fault and resume with a new runner.
	if _, err := owner.Exec(ctx, `CREATE FUNCTION app.purge_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected purge interruption'; END $$; CREATE TRIGGER purge_test_failure BEFORE DELETE ON app.workout_route_points FOR EACH STATEMENT EXECUTE FUNCTION app.purge_test_failure()`); err != nil {
		t.Fatal(err)
	}
	faultSeen := false
	for i := 0; i < 250; i++ {
		nudge()
		_, err := runner.RunOnce(ctx)
		if err != nil {
			faultSeen = true
			break
		}
	}
	if !faultSeen {
		t.Fatal("purge interruption was not exercised")
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER purge_test_failure ON app.workout_route_points; DROP FUNCTION app.purge_test_failure()`); err != nil {
		t.Fatal(err)
	}
	runner = workerapp.NewAccountDeletionRunner(worker, slog.New(slog.NewTextHandler(io.Discard, nil)))
	complete := false
	for i := 0; i < 250; i++ {
		nudge()
		_, err := runner.RunOnce(ctx)
		if err != nil {
			t.Fatalf("purge resume %v", err)
		}
		if err := owner.QueryRow(ctx, `SELECT phase FROM app.account_deletions WHERE target_account_id=$1`, account).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			complete = true
			break
		}
	}
	if !complete {
		t.Fatal("purge did not complete")
	}
	rows, err := owner.Query(ctx, `SELECT unnest(app.account_purge_tables())`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	for _, name := range tables {
		if name == "coverage_retry_attempts" {
			continue
		}
		if err := accountDB.QueryRow(ctx, `SELECT count(*) FROM app.`+name+` WHERE account_id=$1`, account).Scan(&count); err != nil || count != 0 {
			t.Fatalf("remaining private rows in %s count=%d err=%v", name, count, err)
		}
	}
	if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.authentication_principals WHERE id=$1)+(SELECT count(*) FROM app.users WHERE principal_id=$1)+(SELECT count(*) FROM app.accounts WHERE id=$2)+(SELECT count(*) FROM app.sessions WHERE principal_id=$1)`, principal, account).Scan(&count); err != nil || count != 0 {
		t.Fatal("identity/auth scope retained")
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM app.accounts WHERE id=$1 AND state='active'`, otherAccount).Scan(&count); err != nil || count != 1 {
		t.Fatal("other account changed")
	}
	progress := call("GET", path+"/deletion", "", "", "")
	if progress.code != 200 || !strings.Contains(string(progress.body), `"phase":"completed"`) {
		t.Fatalf("progress %d %s", progress.code, progress.body)
	}
	directory := call("GET", "/api/admin/directory", "", "", "")
	if strings.Contains(string(directory.body), compactUUID(principal)) {
		t.Fatal("deleted identity remains in directory")
	}
	adminBearer := insertTestSession(t, db, admin, "bearer", "")
	invite := callEndpoint("POST", "/api/admin/invitations", `{"email":"`+email+`"}`, adminBearer, "")
	server.CreateInvitation(invite.recorder, invite.request, generated.CreateInvitationParams{})
	if invite.recorder.Code != 201 {
		t.Fatalf("reinvite %s", invite.recorder.Body.String())
	}
	token := messageToken(t, sender.last())
	register := callEndpoint("POST", "/api/registrations", `{"token":"`+token+`","username":"`+username+`","fullName":"Fresh User","password":"fresh registration password","passwordConfirmation":"fresh registration password"}`, "", "")
	server.CreateRegistration(register.recorder, register.request)
	if register.recorder.Code != 201 {
		t.Fatalf("reregister %s", register.recorder.Body.String())
	}
	var freshPrincipal, freshAccount uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT p.id,u.account_id FROM app.authentication_principals p JOIN app.users u ON u.principal_id=p.id WHERE p.canonical_username=$1`, username).Scan(&freshPrincipal, &freshAccount); err != nil || freshPrincipal == principal || freshAccount == account {
		t.Fatal("old identity was reused")
	}
	if _, err := accountDB.Exec(ctx, `SELECT set_config('app.account_id',$1,false)`, freshAccount.String()); err != nil {
		t.Fatal(err)
	}
	if err := accountDB.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.workouts WHERE account_id=$1)+(SELECT count(*) FROM app.jobs WHERE account_id=$1)+(SELECT count(*) FROM app.sources WHERE account_id=$1)`, freshAccount).Scan(&count); err != nil || count != 0 {
		t.Fatal("new identity inherited activity")
	}
}

type httpResult struct {
	code int
	body []byte
}
