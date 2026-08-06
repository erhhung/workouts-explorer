package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const manualIngestSourceScope = "manual-ingest-source"

func (s *Server) CreateIngest(w http.ResponseWriter, r *http.Request, params generated.CreateIngestParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.IngestCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	sourceID, valid := parseCompactUUID(input.SourceId)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "source ID is invalid")
		return
	}

	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeIngestUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())

	source, err := querySource(r.Context(), tx, sourceID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "source is unavailable")
		return
	}
	if err != nil {
		writeIngestUnavailable(w, r)
		return
	}
	if source.typeName != sourceconfig.LocalType || source.status != "connected" {
		writeProblem(w, r, http.StatusConflict, "Conflict", "source is not available for ingest")
		return
	}

	plaintext, err := s.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: *session.accountID, RecordID: sourceID}, source.configEnvelope)
	if err != nil {
		writeIngestUnavailable(w, r)
		return
	}
	_, canonical, err := sourceconfig.DecodeLocal(plaintext, s.config.LocalSourceRoots)
	if err != nil || !bytes.Equal(plaintext, canonical) {
		writeIngestUnavailable(w, r)
		return
	}

	parentID, childID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	snapshot, err := s.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: *session.accountID, RecordID: childID}, canonical)
	if err == nil {
		err = enqueueManualIngest(r.Context(), tx, *session.accountID, sourceID, source.generation, parentID, childID, snapshot, requestIDFrom(r.Context()))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		if isManualIngestConflict(err) {
			writeProblem(w, r, http.StatusConflict, "Conflict", "an ingest is already active for this source")
		} else {
			writeIngestUnavailable(w, r)
		}
		return
	}

	jobID := compactUUID(parentID)
	w.Header().Set("Location", "/api/jobs/"+jobID)
	writeJSON(w, http.StatusAccepted, generated.IngestAccepted{JobId: jobID, Status: generated.IngestAcceptedStatus("queued")})
}

func enqueueManualIngest(ctx context.Context, tx pgx.Tx, accountID, sourceID uuid.UUID, generation int64, parentID, childID uuid.UUID, snapshot []byte, requestID string) error {
	parameters, err := json.Marshal(struct {
		SourceID   string `json:"sourceId"`
		Generation int64  `json:"generation"`
	}{compactUUID(sourceID), generation})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,parameters,originating_request_id) VALUES($1,$2,'manual_ingest',80,$3,$4)`, parentID, accountID, parameters, requestID); err != nil {
		return err
	}
	key := manualIngestSourceKey(sourceID)
	if _, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key,originating_request_id) VALUES($1,$2,$3,'manual_ingest_source',80,$4,1,$5,$6,$7)`, childID, parentID, accountID, parameters, manualIngestSourceScope, key[:], requestID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,$4,$5)`, childID, accountID, sourceID, generation, snapshot)
	return err
}

// The source-only key rejects concurrent manual requests across generations.
func manualIngestSourceKey(sourceID uuid.UUID) [32]byte {
	return sha256.Sum256([]byte("workouts-explorer/manual-ingest-source/v1\n" + sourceID.String()))
}

func isManualIngestConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "jobs_active_coalescing_idx"
}

func writeIngestUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "ingest service is temporarily unavailable")
}
