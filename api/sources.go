package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const sourceConnectionCheckScope = "source-connection-check"

type sourceRecord struct {
	id              uuid.UUID
	displayName     string
	typeName        string
	autoSyncEnabled bool
	status          string
	generation      int64
	configEnvelope  []byte
	statusCode      *string
	statusSummary   *string
	checkedAt       *time.Time
	createdAt       time.Time
	updatedAt       time.Time
}

func (s *Server) ListSources(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), sourceSelect+` ORDER BY canonical_display_name,id`)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	defer rows.Close()
	result := generated.SourceList{Items: []generated.Source{}}
	for rows.Next() {
		record, scanErr := scanSource(rows)
		if scanErr != nil {
			writeSourceUnavailable(w, r)
			return
		}
		item, decodeErr := s.sourceResponse(*session.accountID, record)
		if decodeErr != nil {
			writeSourceUnavailable(w, r)
			return
		}
		result.Items = append(result.Items, item)
	}
	if rows.Err() != nil {
		writeSourceUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) CreateSource(w http.ResponseWriter, r *http.Request, params generated.CreateSourceParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var union generated.SourceCreate
	if !decodeJSON(w, r, &union) {
		return
	}
	input, err := union.AsHealthAutoExportLocalSourceCreate()
	if err != nil || input.Type != generated.HealthAutoExportLocalSourceCreateTypeHealthAutoExportLocal {
		writeFieldError(w, r, "type", "invalid", "source type is invalid")
		return
	}
	display, canonical, valid := canonicalSourceDisplayName(input.DisplayName)
	if !valid {
		writeFieldError(w, r, "displayName", "invalid", "display name is invalid")
		return
	}
	local, configJSON, err := sourceconfig.CanonicalizeLocal(sourceconfig.Local{Version: int(input.Config.Version), Path: input.Config.Path}, s.config.LocalSourceRoots)
	if err != nil {
		writeFieldError(w, r, "config", "invalid", "source configuration is invalid")
		return
	}
	sourceID, jobID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	sourceEnvelope, err := s.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: *session.accountID, RecordID: sourceID}, configJSON)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	snapshotEnvelope, err := s.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: *session.accountID, RecordID: jobID}, configJSON)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	var createdAt, updatedAt time.Time
	if err == nil {
		defer tx.Rollback(r.Context())
		err = tx.QueryRow(r.Context(), `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,auto_sync_enabled,status,generation,config_envelope) VALUES($1,$2,$3,$4,$5,$6,'checking-connection',1,$7) RETURNING created_at,updated_at`, sourceID, *session.accountID, display, canonical, sourceconfig.LocalType, input.AutoSyncEnabled, sourceEnvelope).Scan(&createdAt, &updatedAt)
		if err == nil {
			err = enqueueSourceConnectionCheck(r.Context(), tx, *session.accountID, sourceID, 1, jobID, snapshotEnvelope, requestIDFrom(r.Context()))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil {
		if isSourceNameConflict(err) {
			writeSourceNameConflict(w, r)
		} else {
			writeSourceUnavailable(w, r)
		}
		return
	}
	w.Header().Set("Location", "/api/sources/"+compactUUID(sourceID))
	writeJSON(w, http.StatusCreated, generated.Source{Id: compactUUID(sourceID), DisplayName: display, Type: generated.SourceType(sourceconfig.LocalType), AutoSyncEnabled: input.AutoSyncEnabled, Status: generated.SourceStatusCheckingConnection, Generation: 1, Config: generated.HealthAutoExportLocalConfig{Version: generated.HealthAutoExportLocalConfigVersion(local.Version), Path: local.Path}, CreatedAt: createdAt, UpdatedAt: updatedAt})
}

func (s *Server) GetSource(w http.ResponseWriter, r *http.Request, sourceID generated.SourceID) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(sourceID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "source ID is invalid")
		return
	}
	record, err := s.readSource(r.Context(), *session.accountID, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		writeSourceNotFound(w, r)
		return
	}
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	response, err := s.sourceResponse(*session.accountID, record)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) UpdateSource(w http.ResponseWriter, r *http.Request, sourceID generated.SourceID, params generated.UpdateSourceParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, valid := parseCompactUUID(sourceID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "source ID is invalid")
		return
	}
	var patch generated.SourcePatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	record, err := querySource(r.Context(), tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		writeSourceNotFound(w, r)
		return
	}
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	display, canonical := record.displayName, ""
	if patch.DisplayName != nil {
		var nameOK bool
		display, canonical, nameOK = canonicalSourceDisplayName(*patch.DisplayName)
		if !nameOK {
			writeFieldError(w, r, "displayName", "invalid", "display name is invalid")
			return
		}
	} else if err = tx.QueryRow(r.Context(), `SELECT canonical_display_name FROM app.sources WHERE id=$1`, id).Scan(&canonical); err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	autoSync := record.autoSyncEnabled
	if patch.AutoSyncEnabled != nil {
		autoSync = *patch.AutoSyncEnabled
	}
	plaintext, err := s.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: *session.accountID, RecordID: id}, record.configEnvelope)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	_, currentJSON, err := sourceconfig.DecodeLocal(plaintext, s.config.LocalSourceRoots)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	configJSON := currentJSON
	if patch.Config != nil {
		_, configJSON, err = sourceconfig.CanonicalizeLocal(sourceconfig.Local{Version: int(patch.Config.Version), Path: patch.Config.Path}, s.config.LocalSourceRoots)
		if err != nil {
			writeFieldError(w, r, "config", "invalid", "source configuration is invalid")
			return
		}
	}
	configChanged := !bytes.Equal(currentJSON, configJSON)
	generation := record.generation
	status, statusCode, statusSummary, checkedAt := record.status, record.statusCode, record.statusSummary, record.checkedAt
	envelope := record.configEnvelope
	var jobID uuid.UUID
	var snapshotEnvelope []byte
	if configChanged {
		if generation == int64(^uint64(0)>>1) {
			writeSourceUnavailable(w, r)
			return
		}
		generation++
		status, statusCode, statusSummary, checkedAt = "checking-connection", nil, nil, nil
		envelope, err = s.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: *session.accountID, RecordID: id}, configJSON)
		if err == nil {
			jobID = uuid.Must(uuid.NewV7())
			snapshotEnvelope, err = s.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: *session.accountID, RecordID: jobID}, configJSON)
		}
		if err != nil {
			writeSourceUnavailable(w, r)
			return
		}
	}
	err = tx.QueryRow(r.Context(), `UPDATE app.sources SET display_name=$1,canonical_display_name=$2,auto_sync_enabled=$3,status=$4,generation=$5,config_envelope=$6,status_code=$7,status_summary=$8,checked_at=$9 WHERE id=$10 RETURNING updated_at`, display, canonical, autoSync, status, generation, envelope, statusCode, statusSummary, checkedAt, id).Scan(&record.updatedAt)
	if err == nil && configChanged {
		err = enqueueSourceConnectionCheck(r.Context(), tx, *session.accountID, id, generation, jobID, snapshotEnvelope, requestIDFrom(r.Context()))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		if isSourceNameConflict(err) {
			writeSourceNameConflict(w, r)
		} else {
			writeSourceUnavailable(w, r)
		}
		return
	}
	record.displayName, record.autoSyncEnabled, record.status, record.generation = display, autoSync, status, generation
	record.configEnvelope, record.statusCode, record.statusSummary, record.checkedAt = envelope, statusCode, statusSummary, checkedAt
	response, err := s.sourceResponse(*session.accountID, record)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) DeleteSource(w http.ResponseWriter, r *http.Request, sourceID generated.SourceID, params generated.DeleteSourceParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, valid := parseCompactUUID(sourceID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "source ID is invalid")
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var deleted bool
	err = tx.QueryRow(r.Context(), `SELECT app.delete_source($1,$2)`, id, session.principalID).Scan(&deleted)
	if err == nil && !deleted {
		writeSourceNotFound(w, r)
		return
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeSourceUnavailable(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const sourceSelect = `SELECT id,display_name,type,auto_sync_enabled,status,generation,config_envelope,status_code,status_summary,checked_at,created_at,updated_at FROM app.sources`

func (s *Server) readSource(ctx context.Context, accountID, id uuid.UUID, lock bool) (sourceRecord, error) {
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return sourceRecord{}, err
	}
	defer tx.Rollback(ctx)
	return querySource(ctx, tx, id, lock)
}

type sourceQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func querySource(ctx context.Context, query sourceQuerier, id uuid.UUID, lock bool) (sourceRecord, error) {
	suffix := ` WHERE id=$1`
	if lock {
		suffix += ` FOR UPDATE`
	}
	return scanSource(query.QueryRow(ctx, sourceSelect+suffix, id))
}

type sourceScanner interface{ Scan(...any) error }

func scanSource(row sourceScanner) (sourceRecord, error) {
	var value sourceRecord
	err := row.Scan(&value.id, &value.displayName, &value.typeName, &value.autoSyncEnabled, &value.status, &value.generation, &value.configEnvelope, &value.statusCode, &value.statusSummary, &value.checkedAt, &value.createdAt, &value.updatedAt)
	return value, err
}

func (s *Server) sourceResponse(accountID uuid.UUID, record sourceRecord) (generated.Source, error) {
	if record.typeName != sourceconfig.LocalType {
		return generated.Source{}, errors.New("unsupported source type")
	}
	plaintext, err := s.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: record.id}, record.configEnvelope)
	if err != nil {
		return generated.Source{}, err
	}
	local, _, err := sourceconfig.DecodeLocal(plaintext, s.config.LocalSourceRoots)
	if err != nil {
		return generated.Source{}, err
	}
	return generated.Source{Id: compactUUID(record.id), DisplayName: record.displayName, Type: generated.SourceType(record.typeName), AutoSyncEnabled: record.autoSyncEnabled, Status: generated.SourceStatus(record.status), Generation: record.generation, Config: generated.HealthAutoExportLocalConfig{Version: generated.HealthAutoExportLocalConfigVersion(local.Version), Path: local.Path}, StatusCode: record.statusCode, StatusSummary: record.statusSummary, CheckedAt: record.checkedAt, CreatedAt: record.createdAt, UpdatedAt: record.updatedAt}, nil
}

func enqueueSourceConnectionCheck(ctx context.Context, tx pgx.Tx, accountID, sourceID uuid.UUID, generation int64, jobID uuid.UUID, envelope []byte, requestID string) error {
	parameters, err := json.Marshal(struct {
		SourceID   string `json:"sourceId"`
		Generation int64  `json:"generation"`
	}{compactUUID(sourceID), generation})
	if err != nil {
		return err
	}
	key := sourceConnectionCheckKey(sourceID, generation)
	_, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key,originating_request_id) VALUES($1,$2,'source_connection_check',100,$3,1,$4,$5,$6)`, jobID, accountID, parameters, sourceConnectionCheckScope, key[:], requestID)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,$4,$5)`, jobID, accountID, sourceID, generation, envelope)
	}
	return err
}

func sourceConnectionCheckKey(sourceID uuid.UUID, generation int64) [32]byte {
	return sha256.Sum256([]byte("workouts-explorer/source-connection-check/v1\n" + sourceID.String() + "\n" + strconv.FormatInt(generation, 10)))
}

func canonicalSourceDisplayName(raw string) (display, canonical string, ok bool) {
	if !utf8.ValidString(raw) {
		return "", "", false
	}
	display = trimUnicode15Whitespace(raw)
	if display == "" || !unicode15String(display) || utf8.RuneCountInString(display) > 200 {
		return "", "", false
	}
	for _, r := range display {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
			unicode.Is(unicode.Variation_Selector, r) || r == unicode.ReplacementChar {
			return "", "", false
		}
	}
	canonical = norm.NFKC.String(cases.Fold().String(norm.NFKC.String(display)))
	if canonical == "" || utf8.RuneCountInString(canonical) > 200 {
		return "", "", false
	}
	return display, canonical, true
}

func parseCompactUUID(value string) (uuid.UUID, bool) {
	if len(value) != 32 {
		return uuid.Nil, false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			return uuid.Nil, false
		}
	}
	id, err := uuid.Parse(value)
	return id, err == nil
}

func writeSourceNameConflict(w http.ResponseWriter, r *http.Request) {
	message := "display name is already in use"
	writeValidationProblem(w, r, http.StatusConflict, "source conflicts with an existing record", generated.ValidationError{Field: "displayName", Code: "unique", Message: &message})
}

func isSourceNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "sources_active_canonical_display_name_idx"
}

func writeSourceNotFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusNotFound, "Not Found", "source is unavailable")
}

func writeSourceUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "source service is temporarily unavailable")
}
