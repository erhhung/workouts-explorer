package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestICloudSourceLifecycleIntegration(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	var logs bytes.Buffer
	handler, err := NewHandlerContext(ctx, server.config, db, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, db, principalID, "bearer", "")
	route := func(method, path, body, token string, want int) endpointCall {
		t.Helper()
		call := callEndpoint(method, path, body, token, "")
		handler.ServeHTTP(call.recorder, call.request)
		assertICloudRedacted(t, call.recorder.Body.Bytes())
		if strings.Contains(call.recorder.Body.String(), "rotated-cookie-canary") {
			t.Fatal("response leaked rotated credentials")
		}
		if call.recorder.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, call.recorder.Code, want, call.recorder.Body.String())
		}
		validateRecordedResponse(t, method, path, call.recorder)
		return call
	}
	config := testICloudConfig()
	_, original, err := sourceconfig.CanonicalizeICloud(config)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"displayName":"Synthetic iCloud","type":"health-auto-export-icloud","autoSyncEnabled":false,"config":` + string(original) + `}`
	route(http.MethodPost, "/api/sources", body, "", http.StatusUnauthorized)
	created := route(http.MethodPost, "/api/sources", body, bearer, http.StatusCreated)
	var source generated.Source
	if err := json.Unmarshal(created.recorder.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	sourceID, valid := parseCompactUUID(source.Id)
	if !valid || source.Generation != 1 || source.Type != generated.SourceType(sourceconfig.ICloudType) {
		t.Fatal("unexpected created source identity or generation")
	}
	sourcePath := "/api/sources/" + source.Id
	route(http.MethodGet, sourcePath, "", bearer, http.StatusOK)
	route(http.MethodGet, "/api/sources", "", bearer, http.StatusOK)
	record, err := server.readSource(ctx, accountID, sourceID, false)
	if err != nil {
		t.Fatal(err)
	}
	tx := integrationAccountTransaction(t, adminDB, accountID)
	var checkID uuid.UUID
	var checkSnapshot []byte
	if err := tx.QueryRow(ctx, `SELECT j.id,s.config_envelope FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id WHERE s.source_id=$1 AND j.kind='source_connection_check'`, sourceID).Scan(&checkID, &checkSnapshot); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	checkPlaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: checkID}, checkSnapshot)
	if err != nil || !bytes.Equal(checkPlaintext, original) || bytes.Equal(checkSnapshot, record.configEnvelope) {
		t.Fatal("connection snapshot was not independently encrypted with canonical config")
	}
	for _, field := range []string{config.AppleID, config.Password, config.Cookies, config.TrustToken} {
		if bytes.Contains(record.configEnvelope, []byte(field)) || bytes.Contains(checkSnapshot, []byte(field)) {
			t.Fatal("stored envelope contains plaintext credential")
		}
	}
	if _, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: sourceID}, checkSnapshot); !errors.Is(err, sourcecrypto.ErrAuthentication) {
		t.Fatal("snapshot accepted source context")
	}

	route(http.MethodPatch, sourcePath, `{"autoSyncEnabled":true}`, bearer, http.StatusOK)
	route(http.MethodPatch, sourcePath, `{"config":{"path":"Auto Export/Example"}}`, bearer, http.StatusOK)
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 1, 1)
	for _, patch := range []string{
		`{"config":{"appleId":null}}`, `{"config":{"password":""}}`, `{"config":{"cookies":null}}`,
		`{"config":{"trustToken":""}}`, `{"config":{"_auth_session":"secret-canary"}}`,
		`{"config":{"path":"../escape"}}`, `{"config":{"path":"remote:exports"}}`,
		`{"config":{"cookies":"first","cookies":"second"}}`, `{"config":{"path":"Changed"}} {}`,
	} {
		route(http.MethodPatch, sourcePath, patch, bearer, http.StatusBadRequest)
	}
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 1, 1)
	route(http.MethodPatch, sourcePath, `{"displayName":"Renamed iCloud","config":{"path":"Auto Export/Changed"}}`, bearer, http.StatusOK)
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 2, 2)
	config.Path = "Auto Export/Changed"
	assertCurrentICloudConfig(t, server, accountID, sourceID, config)

	ingestBody := `{"sourceIds":["` + source.Id + `"]}`
	route(http.MethodPost, "/api/ingest", ingestBody, bearer, http.StatusConflict)
	setIntegrationSourceStatus(t, adminDB, accountID, sourceID, "connected")
	accepted := route(http.MethodPost, "/api/ingest", ingestBody, bearer, http.StatusAccepted)
	var ingest generated.IngestAccepted
	if err := json.Unmarshal(accepted.recorder.Body.Bytes(), &ingest); err != nil {
		t.Fatal(err)
	}
	parentID, _ := parseCompactUUID(ingest.JobId)
	tx = integrationAccountTransaction(t, adminDB, accountID)
	var childID uuid.UUID
	var childSnapshot, parameters []byte
	var capturedGeneration int64
	if err := tx.QueryRow(ctx, `SELECT j.id,s.config_envelope,s.source_generation,j.parameters FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id WHERE j.parent_job_id=$1`, parentID).Scan(&childID, &childSnapshot, &capturedGeneration, &parameters); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	assertICloudRedacted(t, parameters)
	_, expectedSnapshot, _ := sourceconfig.CanonicalizeICloud(config)
	childPlaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: childID}, childSnapshot)
	record, readErr := server.readSource(ctx, accountID, sourceID, false)
	if err != nil || readErr != nil || capturedGeneration != 2 || !bytes.Equal(childPlaintext, expectedSnapshot) || bytes.Equal(childSnapshot, record.configEnvelope) {
		t.Fatal("manual ingest failed to capture an independent iCloud snapshot")
	}
	coalesced := route(http.MethodPost, "/api/ingest", ingestBody, bearer, http.StatusAccepted)
	var reused generated.IngestAccepted
	if err := json.Unmarshal(coalesced.recorder.Body.Bytes(), &reused); err != nil || reused.JobId != ingest.JobId || !reused.Reused {
		t.Fatal("iCloud manual enqueue did not preserve coalescing")
	}
	route(http.MethodPatch, sourcePath, `{"config":{"cookies":"rotated-cookie-canary"}}`, bearer, http.StatusOK)
	config.Cookies = "rotated-cookie-canary"
	assertCurrentICloudConfig(t, server, accountID, sourceID, config)
	assertSourceGenerationAndJobs(t, server, accountID, sourceID, 3, 4)
	if !bytes.Equal(readSnapshotEnvelope(t, adminDB, accountID, checkID), checkSnapshot) ||
		!bytes.Equal(readSnapshotEnvelope(t, adminDB, accountID, childID), childSnapshot) {
		t.Fatal("owner patch mutated a captured snapshot")
	}
	route(http.MethodPost, "/api/ingest", ingestBody, bearer, http.StatusConflict)

	foreignPrincipal, foreignAccount := insertSourceTestUser(t, adminDB)
	foreignBearer := insertTestSession(t, db, foreignPrincipal, "bearer", "")
	route(http.MethodGet, sourcePath, "", foreignBearer, http.StatusNotFound)
	route(http.MethodPatch, sourcePath, `{"config":{"cookies":"foreign"}}`, foreignBearer, http.StatusNotFound)
	route(http.MethodDelete, sourcePath, "", foreignBearer, http.StatusNotFound)
	route(http.MethodPost, "/api/ingest", ingestBody, foreignBearer, http.StatusNotFound)
	foreignList := route(http.MethodGet, "/api/sources", "", foreignBearer, http.StatusOK)
	if strings.Contains(foreignList.recorder.Body.String(), source.Id) {
		t.Fatal("foreign source list exposed owner source")
	}
	if _, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: foreignAccount, RecordID: childID}, childSnapshot); !errors.Is(err, sourcecrypto.ErrAuthentication) {
		t.Fatal("foreign account authenticated owner snapshot")
	}
	adminID := insertIngestTestAdministrator(t, adminDB)
	adminBearer := insertTestSession(t, db, adminID, "bearer", "")
	route(http.MethodPost, "/api/sources", body, adminBearer, http.StatusForbidden)
	cookie := insertTestSession(t, db, principalID, "cookie", "icloud-csrf")
	missingCSRF := callCookieEndpoint(http.MethodPatch, sourcePath, `{"config":{"path":"Other"}}`, cookie)
	handler.ServeHTTP(missingCSRF.recorder, missingCSRF.request)
	if missingCSRF.recorder.Code != http.StatusForbidden {
		t.Fatal("iCloud patch bypassed CSRF")
	}
	route(http.MethodDelete, sourcePath, "", bearer, http.StatusNoContent)
	route(http.MethodGet, sourcePath, "", bearer, http.StatusNotFound)
	assertICloudRedacted(t, logs.Bytes())
	if bytes.Contains(logs.Bytes(), []byte("rotated-cookie-canary")) {
		t.Fatal("logs leaked rotated credentials")
	}
}

func assertCurrentICloudConfig(t *testing.T, server *Server, accountID, sourceID uuid.UUID, expected sourceconfig.ICloud) {
	t.Helper()
	record, err := server.readSource(context.Background(), accountID, sourceID, false)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := server.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: sourceID}, record.configEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := sourceconfig.DecodeICloud(plaintext)
	if err != nil || current != expected {
		t.Fatal("partial PATCH did not preserve omitted credentials")
	}
}
