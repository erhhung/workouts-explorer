package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSourceLifecycleIntegration(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	server := integrationServer(t, db, &recordingSender{})
	unauthenticated := callEndpoint(http.MethodGet, "/api/sources", "", "", "")
	server.ListSources(unauthenticated.recorder, unauthenticated.request)
	if unauthenticated.recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", unauthenticated.recorder.Code)
	}
	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, db, principalID, "bearer", "")

	createBody := `{"displayName":" Foo ","type":"health-auto-export-local","autoSyncEnabled":false,"config":{"version":1,"path":"/data/workouts/inbox"}}`
	create := callEndpoint(http.MethodPost, "/api/sources", createBody, bearer, "")
	server.CreateSource(create.recorder, create.request, generated.CreateSourceParams{})
	if create.recorder.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.recorder.Code, create.recorder.Body.String())
	}
	var source generated.Source
	if err := json.Unmarshal(create.recorder.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	sourceID, ok := parseCompactUUID(source.Id)
	if !ok || source.DisplayName != "Foo" || source.Generation != 1 || source.Status != generated.SourceStatusCheckingConnection {
		t.Fatalf("unexpected source: %#v", source)
	}
	if create.recorder.Header().Get("Location") != "/api/sources/"+source.Id {
		t.Fatalf("source location = %q", create.recorder.Header().Get("Location"))
	}
	get := callEndpoint(http.MethodGet, "/api/sources/"+source.Id, "", bearer, "")
	server.GetSource(get.recorder, get.request, source.Id)
	if get.recorder.Code != http.StatusOK || !strings.Contains(get.recorder.Body.String(), `"path":"/data/workouts/inbox"`) {
		t.Fatalf("get: %d %s", get.recorder.Code, get.recorder.Body.String())
	}
	list := callEndpoint(http.MethodGet, "/api/sources", "", bearer, "")
	server.ListSources(list.recorder, list.request)
	if list.recorder.Code != http.StatusOK || !strings.Contains(list.recorder.Body.String(), source.Id) {
		t.Fatalf("list: %d %s", list.recorder.Code, list.recorder.Body.String())
	}

	tx, err := adminDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	var jobID uuid.UUID
	var sourceEnvelope, snapshotEnvelope []byte
	var parameters map[string]any
	var priority, coalescingVersion int
	var coalescingScope string
	var coalescingKey []byte
	err = tx.QueryRow(ctx, `SELECT s.config_envelope,j.id,j.parameters,j.priority,j.coalescing_version,j.coalescing_scope,j.coalescing_key,js.config_envelope FROM app.sources s JOIN app.job_config_snapshots js ON js.source_id=s.id JOIN app.jobs j ON j.id=js.job_id WHERE s.id=$1`, sourceID).Scan(&sourceEnvelope, &jobID, &parameters, &priority, &coalescingVersion, &coalescingScope, &coalescingKey, &snapshotEnvelope)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedKey := sourceConnectionCheckKey(sourceID, 1)
	if string(sourceEnvelope) == string(snapshotEnvelope) || parameters["sourceId"] != source.Id || parameters["generation"] != float64(1) || priority != 100 || coalescingVersion != 1 || coalescingScope != sourceConnectionCheckScope || !bytes.Equal(coalescingKey, expectedKey[:]) {
		t.Fatalf("source job snapshot or parameters are invalid: %#v", parameters)
	}
	sourcePlaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: sourceID}, sourceEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPlaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: jobID}, snapshotEnvelope)
	if err != nil || string(sourcePlaintext) != string(snapshotPlaintext) {
		t.Fatalf("independent envelope plaintext mismatch: %v", err)
	}
	if _, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: jobID}, sourceEnvelope); !errors.Is(err, sourcecrypto.ErrAuthentication) {
		t.Fatalf("source envelope accepted snapshot context: %v", err)
	}
	if _, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: uuid.Must(uuid.NewV7()), RecordID: sourceID}, sourceEnvelope); !errors.Is(err, sourcecrypto.ErrAuthentication) {
		t.Fatalf("source envelope accepted foreign account context: %v", err)
	}

	duplicate := callEndpoint(http.MethodPost, "/api/sources", strings.Replace(createBody, `" Foo "`, `"ＦＯＯ"`, 1), bearer, "")
	server.CreateSource(duplicate.recorder, duplicate.request, generated.CreateSourceParams{})
	if duplicate.recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate canonical name: %d %s", duplicate.recorder.Code, duplicate.recorder.Body.String())
	}
	metadata := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"autoSyncEnabled":true}`, bearer, "")
	server.UpdateSource(metadata.recorder, metadata.request, source.Id, generated.UpdateSourceParams{})
	if metadata.recorder.Code != http.StatusOK {
		t.Fatalf("metadata patch: %d %s", metadata.recorder.Code, metadata.recorder.Body.String())
	}
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 1, 1)
	sameConfig := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"config":{"version":1,"path":"/data/workouts/./inbox"}}`, bearer, "")
	server.UpdateSource(sameConfig.recorder, sameConfig.request, source.Id, generated.UpdateSourceParams{})
	if sameConfig.recorder.Code != http.StatusOK {
		t.Fatalf("canonical no-op config patch: %d %s", sameConfig.recorder.Code, sameConfig.recorder.Body.String())
	}
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 1, 1)
	changed := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"config":{"version":1,"path":"/data/workouts/other"}}`, bearer, "")
	server.UpdateSource(changed.recorder, changed.request, source.Id, generated.UpdateSourceParams{})
	if changed.recorder.Code != http.StatusOK {
		t.Fatalf("config patch: %d %s", changed.recorder.Code, changed.recorder.Body.String())
	}
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 2, 2)

	badPath := "/private/secret-marker"
	invalid := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"config":{"version":1,"path":"`+badPath+`"}}`, bearer, "")
	server.UpdateSource(invalid.recorder, invalid.request, source.Id, generated.UpdateSourceParams{})
	if invalid.recorder.Code != http.StatusBadRequest || strings.Contains(invalid.recorder.Body.String(), badPath) {
		t.Fatalf("config validation leaked path: %d %s", invalid.recorder.Code, invalid.recorder.Body.String())
	}
	cookie := insertTestSession(t, db, principalID, "cookie", "csrf-value")
	missingCSRF := callCookieEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"autoSyncEnabled":false}`, cookie)
	server.UpdateSource(missingCSRF.recorder, missingCSRF.request, source.Id, generated.UpdateSourceParams{})
	if missingCSRF.recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", missingCSRF.recorder.Code)
	}

	_, foreignAccount := insertSourceTestUser(t, adminDB)
	foreignPrincipal := principalForAccount(t, adminDB, foreignAccount)
	foreignBearer := insertTestSession(t, db, foreignPrincipal, "bearer", "")
	foreign := callEndpoint(http.MethodGet, "/api/sources/"+source.Id, "", foreignBearer, "")
	server.GetSource(foreign.recorder, foreign.request, source.Id)
	if foreign.recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-account get: %d %s", foreign.recorder.Code, foreign.recorder.Body.String())
	}
	malformed := callEndpoint(http.MethodGet, "/api/sources/not-an-id", "", bearer, "")
	server.GetSource(malformed.recorder, malformed.request, "not-an-id")
	if malformed.recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed source ID: %d", malformed.recorder.Code)
	}

	runningJob, leaseToken := markSourceJobRunning(t, adminDB, accountID, sourceID, 2)
	deleteCall := callEndpoint(http.MethodDelete, "/api/sources/"+source.Id, "", bearer, "")
	server.DeleteSource(deleteCall.recorder, deleteCall.request, source.Id, generated.DeleteSourceParams{})
	if deleteCall.recorder.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleteCall.recorder.Code, deleteCall.recorder.Body.String())
	}
	check, err := server.accountTransaction(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Rollback(ctx)
	var activeSources, activeJobs, snapshots int
	var cancellationPending bool
	if err := check.QueryRow(ctx, `SELECT count(*) FROM app.sources WHERE id=$1`, sourceID).Scan(&activeSources); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(ctx, `SELECT count(*) FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id WHERE s.source_id=$1 AND j.status IN ('queued','running')`, sourceID).Scan(&activeJobs); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(ctx, `SELECT count(*) FROM app.job_config_snapshots WHERE source_id=$1`, sourceID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM app.jobs WHERE id=$1`, runningJob).Scan(&cancellationPending); err != nil {
		t.Fatal(err)
	}
	if activeSources != 0 || activeJobs != 1 || snapshots != 1 || !cancellationPending {
		t.Fatalf("delete cleanup sources=%d jobs=%d snapshots=%d", activeSources, activeJobs, snapshots)
	}
	deleted := callEndpoint(http.MethodGet, "/api/sources/"+source.Id, "", bearer, "")
	server.GetSource(deleted.recorder, deleted.request, source.Id)
	if deleted.recorder.Code != http.StatusNotFound {
		t.Fatalf("deleted source get: %d %s", deleted.recorder.Code, deleted.recorder.Body.String())
	}
	finishSourceJob(t, adminDB, accountID, runningJob, leaseToken)
}

func TestSourceConcurrentMutationsAndRollbackIntegration(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	server := integrationServer(t, db, &recordingSender{})
	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, db, principalID, "bearer", "")

	patched := createIntegrationSource(t, server, bearer, "Concurrent patches", "/data/workouts/concurrent-initial")
	patchBodies := []string{
		`{"config":{"version":1,"path":"/data/workouts/concurrent-a"}}`,
		`{"config":{"version":1,"path":"/data/workouts/concurrent-b"}}`,
	}
	patchResponses := make([]endpointCall, len(patchBodies))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range patchBodies {
		patchResponses[index] = callEndpoint(http.MethodPatch, "/api/sources/"+patched.Id, patchBodies[index], bearer, "")
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			server.UpdateSource(patchResponses[index].recorder, patchResponses[index].request, patched.Id, generated.UpdateSourceParams{})
		}(index)
	}
	close(start)
	wait.Wait()
	for index, response := range patchResponses {
		if response.recorder.Code != http.StatusOK {
			t.Fatalf("concurrent patch %d: %d %s", index, response.recorder.Code, response.recorder.Body.String())
		}
	}
	patchedID, _ := parseCompactUUID(patched.Id)
	assertSourceGenerationAndJobs(t, server, accountID, patchedID, 3, 3)

	rollback := createIntegrationSource(t, server, bearer, "Rollback", "/data/workouts/rollback-initial")
	rollbackID, _ := parseCompactUUID(rollback.Id)
	blockerID := uuid.Must(uuid.NewV7())
	blockerKey := sourceConnectionCheckKey(rollbackID, 2)
	tx, err := server.accountTransaction(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,coalescing_version,coalescing_scope,coalescing_key) VALUES($1,$2,'workout_deletion',100,1,$3,$4)`, blockerID, accountID, sourceConnectionCheckScope, blockerKey[:]); err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	failingPatch := callEndpoint(http.MethodPatch, "/api/sources/"+rollback.Id, `{"config":{"version":1,"path":"/data/workouts/rollback-new"}}`, bearer, "")
	server.UpdateSource(failingPatch.recorder, failingPatch.request, rollback.Id, generated.UpdateSourceParams{})
	if failingPatch.recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("enqueue failure patch: %d %s", failingPatch.recorder.Code, failingPatch.recorder.Body.String())
	}
	assertSourceGenerationAndJobs(t, server, accountID, rollbackID, 1, 1)
	rolledBack := callEndpoint(http.MethodGet, "/api/sources/"+rollback.Id, "", bearer, "")
	server.GetSource(rolledBack.recorder, rolledBack.request, rollback.Id)
	if rolledBack.recorder.Code != http.StatusOK || !strings.Contains(rolledBack.recorder.Body.String(), `"path":"/data/workouts/rollback-initial"`) {
		t.Fatalf("failed enqueue did not roll back source: %d %s", rolledBack.recorder.Code, rolledBack.recorder.Body.String())
	}
	cancel, err := server.accountTransaction(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cancel.Exec(ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, blockerID, principalID); err == nil {
		err = cancel.Commit(ctx)
	} else {
		_ = cancel.Rollback(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}

	raced := createIntegrationSource(t, server, bearer, "Patch delete race", "/data/workouts/race-initial")
	racedID, _ := parseCompactUUID(raced.Id)
	patchRace := callEndpoint(http.MethodPatch, "/api/sources/"+raced.Id, `{"config":{"version":1,"path":"/data/workouts/race-new"}}`, bearer, "")
	deleteRace := callEndpoint(http.MethodDelete, "/api/sources/"+raced.Id, "", bearer, "")
	start = make(chan struct{})
	wait = sync.WaitGroup{}
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		server.UpdateSource(patchRace.recorder, patchRace.request, raced.Id, generated.UpdateSourceParams{})
	}()
	go func() {
		defer wait.Done()
		<-start
		server.DeleteSource(deleteRace.recorder, deleteRace.request, raced.Id, generated.DeleteSourceParams{})
	}()
	close(start)
	wait.Wait()
	if deleteRace.recorder.Code != http.StatusNoContent {
		t.Fatalf("concurrent delete: %d %s", deleteRace.recorder.Code, deleteRace.recorder.Body.String())
	}
	if patchRace.recorder.Code != http.StatusOK && patchRace.recorder.Code != http.StatusNotFound {
		t.Fatalf("concurrent patch/delete patch: %d %s", patchRace.recorder.Code, patchRace.recorder.Body.String())
	}
	check, err := server.accountTransaction(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Rollback(ctx)
	var activeSources, snapshots, activeJobs int
	if err := check.QueryRow(ctx, `SELECT count(*) FROM app.sources WHERE id=$1`, racedID).Scan(&activeSources); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(ctx, `SELECT count(*) FROM app.job_config_snapshots WHERE source_id=$1`, racedID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(ctx, `SELECT count(*) FROM app.jobs WHERE parameters->>'sourceId'=$1 AND status IN ('queued','running')`, raced.Id).Scan(&activeJobs); err != nil {
		t.Fatal(err)
	}
	if activeSources != 0 || snapshots != 0 || activeJobs != 0 {
		t.Fatalf("patch/delete race sources=%d snapshots=%d activeJobs=%d", activeSources, snapshots, activeJobs)
	}
}

func createIntegrationSource(t *testing.T, server *Server, bearer, displayName, path string) generated.Source {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"displayName":     displayName,
		"type":            "health-auto-export-local",
		"autoSyncEnabled": false,
		"config":          map[string]any{"version": 1, "path": path},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := callEndpoint(http.MethodPost, "/api/sources", string(body), bearer, "")
	server.CreateSource(call.recorder, call.request, generated.CreateSourceParams{})
	if call.recorder.Code != http.StatusCreated {
		t.Fatalf("create integration source: %d %s", call.recorder.Code, call.recorder.Body.String())
	}
	var source generated.Source
	if err := json.Unmarshal(call.recorder.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	return source
}

func markSourceJobRunning(t *testing.T, db *pgxpool.Pool, accountID, sourceID uuid.UUID, generation int64) (uuid.UUID, uuid.UUID) {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		t.Fatal(err)
	}
	var jobID uuid.UUID
	if err = tx.QueryRow(context.Background(), `SELECT job_id FROM app.job_config_snapshots WHERE source_id=$1 AND source_generation=$2`, sourceID, generation).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	leaseToken := uuid.Must(uuid.NewV7())
	if _, err = tx.Exec(context.Background(), `SELECT set_config('app.job_transition','claim',true)`); err == nil {
		_, err = tx.Exec(context.Background(), `UPDATE app.jobs SET status='running',attempt=attempt+1,worker_id='source-api-test',lease_token=$1,claimed_at=now(),heartbeat_at=now(),lease_expires_at=now()+interval '5 minutes',started_at=now() WHERE id=$2`, leaseToken, jobID)
	}
	if err == nil {
		err = tx.Commit(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
	return jobID, leaseToken
}

func finishSourceJob(t *testing.T, db *pgxpool.Pool, accountID, jobID, leaseToken uuid.UUID) {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err == nil {
		var finished bool
		err = tx.QueryRow(context.Background(), `SELECT app.finish_job($1,'source-api-test',$2,'cancelled')`, jobID, leaseToken).Scan(&finished)
		if err == nil && !finished {
			err = errors.New("running source job was not finished")
		}
	}
	if err == nil {
		err = tx.Commit(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
}

func insertSourceTestUser(t *testing.T, db *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	principalID, accountID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	suffix := strings.ToLower(strings.ReplaceAll(principalID.String(), "-", ""))
	tx, err := db.Begin(context.Background())
	if err == nil {
		defer tx.Rollback(context.Background())
		_, err = tx.Exec(context.Background(), `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'user',$2,$2,$3,$3,1,'test-only-hash','Source Test')`, principalID, "source"+suffix, "source"+suffix+"@example.test")
		if err == nil {
			_, err = tx.Exec(context.Background(), `INSERT INTO app.accounts(id) VALUES($1)`, accountID)
		}
		if err == nil {
			_, err = tx.Exec(context.Background(), `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principalID, accountID)
		}
		if err == nil {
			_, err = tx.Exec(context.Background(), `SELECT set_config('app.account_id',$1,true)`, accountID.String())
		}
		if err == nil {
			_, err = tx.Exec(context.Background(), `INSERT INTO app.preferences(account_id) VALUES($1)`, accountID)
		}
		if err == nil {
			err = tx.Commit(context.Background())
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return principalID, accountID
}

func principalForAccount(t *testing.T, db *pgxpool.Pool, accountID uuid.UUID) uuid.UUID {
	t.Helper()
	var principalID uuid.UUID
	if err := db.QueryRow(context.Background(), `SELECT principal_id FROM app.users WHERE account_id=$1`, accountID).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	return principalID
}

func assertSourceGenerationAndJobs(t *testing.T, server *Server, accountID, sourceID uuid.UUID, generation int64, jobs int) {
	t.Helper()
	tx, err := server.accountTransaction(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var actualGeneration int64
	var actualJobs int
	if err := tx.QueryRow(context.Background(), `SELECT generation FROM app.sources WHERE id=$1`, sourceID).Scan(&actualGeneration); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id WHERE s.source_id=$1`, sourceID).Scan(&actualJobs); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if actualGeneration != generation || actualJobs != jobs {
		t.Fatalf("generation=%d jobs=%d, want generation=%d jobs=%d", actualGeneration, actualJobs, generation, jobs)
	}
}
