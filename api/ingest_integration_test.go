package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManualIngestIntegration(t *testing.T) {
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
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, db, principalID, "bearer", "")
	sourcePath := "/data/workouts/manual-ingest-original"
	source := createIntegrationSource(t, server, bearer, "Manual ingest", sourcePath)
	sourceID, _ := parseCompactUUID(source.Id)
	setIntegrationSourceStatus(t, adminDB, accountID, sourceID, "connected")

	unauthenticated := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, "", "", false)
	if unauthenticated.recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.recorder.Code)
	}
	adminID := insertIngestTestAdministrator(t, adminDB)
	adminBearer := insertTestSession(t, db, adminID, "bearer", "")
	admin := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, adminBearer, "", false)
	if admin.recorder.Code != http.StatusForbidden {
		t.Fatalf("administrator status=%d body=%s", admin.recorder.Code, admin.recorder.Body.String())
	}
	cookie := insertTestSession(t, db, principalID, "cookie", "ingest-csrf")
	missingCSRF := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, cookie, "", true)
	if missingCSRF.recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", missingCSRF.recorder.Code)
	}
	badCSRF := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, cookie, "wrong", true)
	if badCSRF.recorder.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF status=%d", badCSRF.recorder.Code)
	}

	accepted := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, cookie, "ingest-csrf", true)
	if accepted.recorder.Code != http.StatusAccepted {
		t.Fatalf("accepted status=%d body=%s", accepted.recorder.Code, accepted.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/ingest", accepted.recorder)
	var response generated.IngestAccepted
	if err := json.Unmarshal(accepted.recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	parentID, valid := parseCompactUUID(response.JobId)
	if !valid || response.Status != generated.IngestAcceptedStatus("queued") || accepted.recorder.Header().Get("Location") != "/api/jobs/"+response.JobId {
		t.Fatalf("invalid accepted response=%#v location=%q", response, accepted.recorder.Header().Get("Location"))
	}

	childID, sourceEnvelope, snapshotEnvelope := assertManualIngestArtifacts(t, server, adminDB, accountID, sourceID, parentID, sourcePath)
	plaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: childID}, snapshotEnvelope)
	if err != nil || string(plaintext) != `{"version":1,"path":"`+sourcePath+`"}` {
		t.Fatalf("snapshot plaintext=%q err=%v", plaintext, err)
	}
	if bytes.Equal(sourceEnvelope, snapshotEnvelope) || bytes.Contains(snapshotEnvelope, []byte(sourcePath)) {
		t.Fatal("snapshot was not independently encrypted or contains plaintext configuration")
	}
	if _, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: parentID}, snapshotEnvelope); !errors.Is(err, sourcecrypto.ErrAuthentication) {
		t.Fatalf("snapshot accepted parent AAD: %v", err)
	}
	if _, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: sourceID}, snapshotEnvelope); !errors.Is(err, sourcecrypto.ErrAuthentication) {
		t.Fatalf("snapshot accepted source AAD: %v", err)
	}

	conflict := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, bearer, "", false)
	if conflict.recorder.Code != http.StatusConflict || strings.Contains(conflict.recorder.Body.String(), sourcePath) {
		t.Fatalf("coalescing status=%d body=%s", conflict.recorder.Code, conflict.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/ingest", conflict.recorder)
	assertManualIngestJobCount(t, server, accountID, sourceID, 2)

	metadata := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"displayName":"Renamed after ingest"}`, bearer, "")
	server.UpdateSource(metadata.recorder, metadata.request, source.Id, generated.UpdateSourceParams{})
	if metadata.recorder.Code != http.StatusOK {
		t.Fatalf("metadata update status=%d body=%s", metadata.recorder.Code, metadata.recorder.Body.String())
	}
	updatedPath := "/data/workouts/manual-ingest-updated"
	configUpdate := callEndpoint(http.MethodPatch, "/api/sources/"+source.Id, `{"config":{"version":1,"path":"`+updatedPath+`"}}`, bearer, "")
	server.UpdateSource(configUpdate.recorder, configUpdate.request, source.Id, generated.UpdateSourceParams{})
	if configUpdate.recorder.Code != http.StatusOK {
		t.Fatalf("config update status=%d body=%s", configUpdate.recorder.Code, configUpdate.recorder.Body.String())
	}
	oldSnapshot := readSnapshotEnvelope(t, adminDB, accountID, childID)
	oldPlaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: childID}, oldSnapshot)
	if err != nil || string(oldPlaintext) != `{"version":1,"path":"`+sourcePath+`"}` || bytes.Contains(oldPlaintext, []byte(updatedPath)) {
		t.Fatalf("source update changed captured snapshot: plaintext=%q err=%v", oldPlaintext, err)
	}

	foreignPrincipal, _ := insertSourceTestUser(t, adminDB)
	foreignBearer := insertTestSession(t, db, foreignPrincipal, "bearer", "")
	foreign := routeIngest(handler, `{"sourceId":"`+source.Id+`"}`, foreignBearer, "", false)
	if foreign.recorder.Code != http.StatusNotFound || strings.Contains(foreign.recorder.Body.String(), sourcePath) {
		t.Fatalf("foreign status=%d body=%s", foreign.recorder.Code, foreign.recorder.Body.String())
	}

	notConnected := createIntegrationSource(t, server, bearer, "Not connected ingest", "/data/workouts/not-connected-ingest")
	notConnectedCall := routeIngest(handler, `{"sourceId":"`+notConnected.Id+`"}`, bearer, "", false)
	if notConnectedCall.recorder.Code != http.StatusConflict {
		t.Fatalf("not-connected status=%d body=%s", notConnectedCall.recorder.Code, notConnectedCall.recorder.Body.String())
	}
	deleted := createIntegrationSource(t, server, bearer, "Deleted ingest", "/data/workouts/deleted-ingest")
	deleteCall := callEndpoint(http.MethodDelete, "/api/sources/"+deleted.Id, "", bearer, "")
	server.DeleteSource(deleteCall.recorder, deleteCall.request, deleted.Id, generated.DeleteSourceParams{})
	if deleteCall.recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", deleteCall.recorder.Code)
	}
	deletedCall := routeIngest(handler, `{"sourceId":"`+deleted.Id+`"}`, bearer, "", false)
	if deletedCall.recorder.Code != http.StatusNotFound {
		t.Fatalf("deleted status=%d body=%s", deletedCall.recorder.Code, deletedCall.recorder.Body.String())
	}

	fault := createIntegrationSource(t, server, bearer, "Canonical fault", "/data/workouts/canonical-fault")
	faultID, _ := parseCompactUUID(fault.Id)
	setIntegrationSourceStatus(t, adminDB, accountID, faultID, "connected")
	noncanonical := []byte(`{"version":1, "path":"/data/workouts/canonical-fault"}`)
	faultEnvelope, err := server.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: faultID}, noncanonical)
	if err != nil {
		t.Fatal(err)
	}
	updateIntegrationSourceEnvelope(t, adminDB, accountID, faultID, faultEnvelope)
	faultCall := routeIngest(handler, `{"sourceId":"`+fault.Id+`"}`, bearer, "", false)
	if faultCall.recorder.Code != http.StatusServiceUnavailable || strings.Contains(faultCall.recorder.Body.String(), "canonical-fault") {
		t.Fatalf("canonical fault status=%d body=%s", faultCall.recorder.Code, faultCall.recorder.Body.String())
	}
	assertManualIngestJobCount(t, server, accountID, faultID, 0)
}

func assertManualIngestArtifacts(t *testing.T, server *Server, adminDB *pgxpool.Pool, accountID, sourceID, parentID uuid.UUID, path string) (uuid.UUID, []byte, []byte) {
	t.Helper()
	tx, err := server.accountTransaction(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var parentKind, parentStatus string
	var parentPriority, parentSnapshots int
	var parentParameters map[string]any
	if err := tx.QueryRow(context.Background(), `SELECT kind,status,priority,parameters,(SELECT count(*) FROM app.job_config_snapshots WHERE job_id=j.id) FROM app.jobs j WHERE id=$1`, parentID).Scan(&parentKind, &parentStatus, &parentPriority, &parentParameters, &parentSnapshots); err != nil {
		t.Fatal(err)
	}
	var childID uuid.UUID
	var childKind, childStatus, scope string
	var childPriority, coalescingVersion int
	var childParameters map[string]any
	var key []byte
	var generation int64
	if err := tx.QueryRow(context.Background(), `SELECT j.id,j.kind,j.status,j.priority,j.parameters,j.coalescing_version,j.coalescing_scope,j.coalescing_key,s.source_generation FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id WHERE j.parent_job_id=$1`, parentID).Scan(&childID, &childKind, &childStatus, &childPriority, &childParameters, &coalescingVersion, &scope, &key, &generation); err != nil {
		t.Fatal(err)
	}
	expectedKey := manualIngestSourceKey(sourceID)
	for name, parameters := range map[string]map[string]any{"parent": parentParameters, "child": childParameters} {
		if len(parameters) != 2 || parameters["sourceId"] != compactUUID(sourceID) || parameters["generation"] != float64(1) {
			t.Fatalf("%s unsafe parameters=%#v", name, parameters)
		}
	}
	if parentKind != "manual_ingest" || parentStatus != "queued" || parentPriority != 80 || parentSnapshots != 0 || childKind != "manual_ingest_source" || childStatus != "queued" || childPriority != 80 || generation != 1 || coalescingVersion != 1 || scope != manualIngestSourceScope || !bytes.Equal(key, expectedKey[:]) {
		t.Fatalf("invalid artifacts parent=%s/%s/%d snapshots=%d child=%s/%s/%d generation=%d coalescing=%d/%s", parentKind, parentStatus, parentPriority, parentSnapshots, childKind, childStatus, childPriority, generation, coalescingVersion, scope)
	}
	_ = tx.Rollback(context.Background())

	admin := integrationAccountTransaction(t, adminDB, accountID)
	defer admin.Rollback(context.Background())
	var sourceEnvelope, snapshotEnvelope []byte
	if err := admin.QueryRow(context.Background(), `SELECT s.config_envelope,js.config_envelope FROM app.sources s JOIN app.job_config_snapshots js ON js.source_id=s.id WHERE s.id=$1 AND js.job_id=$2`, sourceID, childID).Scan(&sourceEnvelope, &snapshotEnvelope); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snapshotEnvelope, []byte(path)) {
		t.Fatal("snapshot envelope contains source path")
	}
	return childID, sourceEnvelope, snapshotEnvelope
}

func routeIngest(handler http.Handler, body, credential, csrf string, cookie bool) endpointCall {
	call := callEndpoint(http.MethodPost, "/api/ingest", body, "", csrf)
	if cookie {
		call.request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: credential})
	} else if credential != "" {
		call.request.Header.Set("Authorization", "Bearer "+credential)
	}
	handler.ServeHTTP(call.recorder, call.request)
	return call
}

func setIntegrationSourceStatus(t *testing.T, db *pgxpool.Pool, accountID, sourceID uuid.UUID, status string) {
	t.Helper()
	tx := integrationAccountTransaction(t, db, accountID)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `UPDATE app.sources SET status=$1,status_code=NULL,status_summary=NULL,checked_at=transaction_timestamp() WHERE id=$2`, status, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func updateIntegrationSourceEnvelope(t *testing.T, db *pgxpool.Pool, accountID, sourceID uuid.UUID, envelope []byte) {
	t.Helper()
	tx := integrationAccountTransaction(t, db, accountID)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `UPDATE app.sources SET config_envelope=$1 WHERE id=$2`, envelope, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func readSnapshotEnvelope(t *testing.T, db *pgxpool.Pool, accountID, jobID uuid.UUID) []byte {
	t.Helper()
	tx := integrationAccountTransaction(t, db, accountID)
	defer tx.Rollback(context.Background())
	var envelope []byte
	if err := tx.QueryRow(context.Background(), `SELECT config_envelope FROM app.job_config_snapshots WHERE job_id=$1`, jobID).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func assertManualIngestJobCount(t *testing.T, server *Server, accountID, sourceID uuid.UUID, want int) {
	t.Helper()
	tx, err := server.accountTransaction(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var count int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM app.jobs WHERE kind IN ('manual_ingest','manual_ingest_source') AND parameters->>'sourceId'=$1`, compactUUID(sourceID)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("manual ingest artifact count=%d want=%d", count, want)
	}
}

func integrationAccountTransaction(t *testing.T, db *pgxpool.Pool, accountID uuid.UUID) pgx.Tx {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err == nil {
		_, err = tx.Exec(context.Background(), `SELECT set_config('app.account_id',$1,true)`, accountID.String())
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback(context.Background())
		}
		t.Fatal(err)
	}
	return tx
}

func insertIngestTestAdministrator(t *testing.T, db *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	suffix := strings.ToLower(strings.ReplaceAll(id.String(), "-", ""))
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'administrator',$2,$2,$3,$3,1,'test-only-hash','Ingest Administrator')`, id, "ingestadmin"+suffix, "ingestadmin"+suffix+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.administrators(principal_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return id
}
