package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const manualIngestScope = "manual-ingest"

var (
	errIngestSourceUnavailable  = errors.New("ingest source unavailable")
	errIngestSourceDisconnected = errors.New("ingest source disconnected")
	errIngestConfigUnavailable  = errors.New("ingest source config unavailable")
	errEarlierIngestActive      = errors.New("an earlier ingest is still active")
)

type ingestRange struct {
	mode      string
	startDate *string
	endDate   *string
}

type ingestSource struct {
	sourceRecord
	canonical []byte
}

type ingestEnqueueRequest struct {
	trigger       string
	priority      int
	sourceIDs     []uuid.UUID
	dateRange     ingestRange
	retryOfParent *uuid.UUID
	childRetries  map[uuid.UUID]uuid.UUID
}

type ingestEnqueueResult struct {
	parentID uuid.UUID
	status   string
	reused   bool
}

func (s *Server) CreateIngest(w http.ResponseWriter, r *http.Request, params generated.CreateIngestParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.IngestCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	sourceIDs, valid := normalizedIngestSourceIDs(input.SourceIds)
	if !valid {
		writeFieldError(w, r, "sourceIds", "duplicate", "source IDs must be unique and valid")
		return
	}
	dateRange, valid := normalizedIngestRange(input)
	if !valid {
		writeFieldError(w, r, "startDate", "invalid-range", "startDate and endDate must both be omitted or form an ordered range")
		return
	}

	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeIngestUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())

	result, err := s.enqueueIngest(r.Context(), tx, *session.accountID, requestIDFrom(r.Context()), ingestEnqueueRequest{
		trigger: "manual", priority: 80, sourceIDs: sourceIDs, dateRange: dateRange,
	})
	if errors.Is(err, errIngestSourceUnavailable) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "one or more sources are unavailable")
		return
	}
	if errors.Is(err, errIngestSourceDisconnected) {
		writeProblem(w, r, http.StatusConflict, "Conflict", "one or more sources are not connected")
		return
	}
	if errors.Is(err, errEarlierIngestActive) {
		writeProblem(w, r, http.StatusConflict, "Conflict", errEarlierIngestActive.Error())
		return
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeIngestUnavailable(w, r)
		return
	}
	writeIngestAccepted(w, result.parentID, result.status, result.reused)
}

func (s *Server) enqueueIngest(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, requestID string, input ingestEnqueueRequest) (ingestEnqueueResult, error) {
	input.sourceIDs = normalizedUUIDs(input.sourceIDs)
	if len(input.sourceIDs) == 0 || len(input.sourceIDs) > 100 || !validNormalizedIngestRange(input.dateRange) ||
		(input.trigger != "manual" && input.trigger != "scheduled") || input.priority < 0 {
		return ingestEnqueueResult{}, errors.New("invalid ingest enqueue request")
	}
	lockedSources, err := lockIngestSources(ctx, tx, input.sourceIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestEnqueueResult{}, errIngestSourceUnavailable
	}
	if err != nil {
		return ingestEnqueueResult{}, err
	}
	sources := make([]ingestSource, 0, len(lockedSources))
	for _, source := range lockedSources {
		if source.typeName != sourceconfig.LocalType || source.status != "connected" {
			return ingestEnqueueResult{}, errIngestSourceDisconnected
		}
		plaintext, decryptErr := s.sourceKeys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: source.id}, source.configEnvelope)
		if decryptErr != nil {
			return ingestEnqueueResult{}, errIngestConfigUnavailable
		}
		_, canonical, decodeErr := sourceconfig.DecodeLocal(plaintext, s.config.LocalSourceRoots)
		if decodeErr != nil || !bytes.Equal(plaintext, canonical) {
			return ingestEnqueueResult{}, errIngestConfigUnavailable
		}
		sources = append(sources, ingestSource{sourceRecord: source, canonical: canonical})
	}
	var legacyActive bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM app.jobs child
		JOIN app.job_config_snapshots snapshot ON snapshot.job_id=child.id AND snapshot.account_id=child.account_id
		LEFT JOIN app.job_source_contexts source_context ON source_context.job_id=child.id AND source_context.account_id=child.account_id
		WHERE child.account_id=$1 AND child.kind IN ('manual_ingest_source','scheduled_ingest_source')
			AND child.status IN ('queued','running') AND snapshot.source_id=ANY($2)
			AND (child.coalescing_key IS NOT NULL OR source_context.job_id IS NULL)
	)`, accountID, input.sourceIDs).Scan(&legacyActive); err != nil {
		return ingestEnqueueResult{}, err
	}
	if legacyActive {
		return ingestEnqueueResult{}, errEarlierIngestActive
	}

	parentKind, childKind := input.trigger+"_ingest", input.trigger+"_ingest_source"
	key := manualIngestKey(sources, input.dateRange)
	parentID := uuid.Must(uuid.NewV7())
	parentParameters, err := ingestParentParameters(input.sourceIDs, input.dateRange)
	if err != nil {
		return ingestEnqueueResult{}, err
	}
	command, err := tx.Exec(ctx, `INSERT INTO app.jobs
		(id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key,originating_request_id,retry_of_job_id)
		VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9)
		ON CONFLICT ((CASE WHEN account_id IS NOT NULL THEN 'account' ELSE 'administrator' END),
			(COALESCE(account_id,administrator_id)),coalescing_scope,coalescing_key)
		WHERE status IN ('queued','running') AND coalescing_key IS NOT NULL DO NOTHING`,
		parentID, accountID, parentKind, input.priority, parentParameters, manualIngestScope, key[:], requestID, input.retryOfParent)
	if err != nil {
		return ingestEnqueueResult{}, err
	}
	if command.RowsAffected() == 0 {
		var result ingestEnqueueResult
		err = tx.QueryRow(ctx, `SELECT id,status,true FROM app.jobs WHERE account_id=$1 AND parent_job_id IS NULL
			AND coalescing_version=1 AND coalescing_scope=$2 AND coalescing_key=$3 AND status IN ('queued','running')`,
			accountID, manualIngestScope, key[:]).Scan(&result.parentID, &result.status, &result.reused)
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, parentID, accountID); err != nil {
		return ingestEnqueueResult{}, err
	}
	var parentProgressMatches bool
	if err = tx.QueryRow(ctx, `SELECT account_id=$2 FROM app.job_progress WHERE job_id=$1`, parentID, accountID).Scan(&parentProgressMatches); err != nil || !parentProgressMatches {
		if err == nil {
			err = errors.New("ingest parent progress mismatch")
		}
		return ingestEnqueueResult{}, err
	}
	for _, source := range sources {
		childID := uuid.Must(uuid.NewV7())
		snapshot, encryptErr := s.sourceKeys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: childID}, source.canonical)
		if encryptErr != nil {
			return ingestEnqueueResult{}, encryptErr
		}
		childParameters, marshalErr := ingestChildParameters(source.id, source.generation, input.dateRange)
		if marshalErr != nil {
			return ingestEnqueueResult{}, marshalErr
		}
		var retryOf *uuid.UUID
		if oldID, ok := input.childRetries[source.id]; ok {
			retryOf = &oldID
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app.jobs
			(id,parent_job_id,account_id,kind,priority,parameters,originating_request_id,retry_of_job_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, childID, parentID, accountID, childKind, input.priority, childParameters, requestID, retryOf); err != nil {
			return ingestEnqueueResult{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
			VALUES($1,$2,$3,$4,$5)`, childID, accountID, source.id, source.generation, snapshot); err != nil {
			return ingestEnqueueResult{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, childID, accountID, source.id, source.generation, source.displayName, source.typeName); err != nil {
			return ingestEnqueueResult{}, err
		}
		var contextMatches bool
		if err = tx.QueryRow(ctx, `SELECT source_id=$2 AND source_generation=$3 AND display_name=$4 AND source_type=$5
			FROM app.job_source_contexts WHERE job_id=$1 AND account_id=$6`, childID, source.id, source.generation,
			source.displayName, source.typeName, accountID).Scan(&contextMatches); err != nil || !contextMatches {
			if err == nil {
				err = errors.New("ingest source context mismatch")
			}
			return ingestEnqueueResult{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, childID, accountID); err != nil {
			return ingestEnqueueResult{}, err
		}
		var childProgressMatches bool
		if err = tx.QueryRow(ctx, `SELECT account_id=$2 FROM app.job_progress WHERE job_id=$1`, childID, accountID).Scan(&childProgressMatches); err != nil || !childProgressMatches {
			if err == nil {
				err = errors.New("ingest child progress mismatch")
			}
			return ingestEnqueueResult{}, err
		}
	}
	return ingestEnqueueResult{parentID: parentID, status: "queued"}, nil
}

func normalizedUUIDs(values []uuid.UUID) []uuid.UUID {
	result := append([]uuid.UUID(nil), values...)
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i][:], result[j][:]) < 0 })
	for index, id := range result {
		if id == uuid.Nil || (index > 0 && id == result[index-1]) {
			return nil
		}
	}
	return result
}

func validNormalizedIngestRange(value ingestRange) bool {
	if value.mode == "incremental" {
		return value.startDate == nil && value.endDate == nil
	}
	if value.mode != "bounded" || value.startDate == nil || value.endDate == nil {
		return false
	}
	start, startErr := time.Parse(time.DateOnly, *value.startDate)
	end, endErr := time.Parse(time.DateOnly, *value.endDate)
	return startErr == nil && endErr == nil && !end.Before(start) &&
		start.Format(time.DateOnly) == *value.startDate && end.Format(time.DateOnly) == *value.endDate
}

func normalizedIngestSourceIDs(values []generated.CompactUUID) ([]uuid.UUID, bool) {
	if len(values) < 1 || len(values) > 100 {
		return nil, false
	}
	result := make([]uuid.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		id, valid := parseCompactUUID(value)
		if !valid {
			return nil, false
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, false
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i][:], result[j][:]) < 0 })
	return result, true
}

func normalizedIngestRange(input generated.IngestCreate) (ingestRange, bool) {
	if input.StartDate == nil && input.EndDate == nil {
		return ingestRange{mode: "incremental"}, true
	}
	if input.StartDate == nil || input.EndDate == nil || input.StartDate.Time.After(input.EndDate.Time) {
		return ingestRange{}, false
	}
	start, end := input.StartDate.Time.Format(time.DateOnly), input.EndDate.Time.Format(time.DateOnly)
	return ingestRange{mode: "bounded", startDate: &start, endDate: &end}, true
}

func lockIngestSources(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) ([]sourceRecord, error) {
	rows, err := tx.Query(ctx, sourceSelect+` WHERE id=ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]sourceRecord, 0, len(ids))
	for rows.Next() {
		record, scanErr := scanSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, record)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if len(result) != len(ids) {
		return nil, pgx.ErrNoRows
	}
	return result, nil
}

func ingestParentParameters(sourceIDs []uuid.UUID, dateRange ingestRange) ([]byte, error) {
	value := struct {
		Mode      string   `json:"mode"`
		SourceIDs []string `json:"sourceIds"`
		StartDate *string  `json:"startDate,omitempty"`
		EndDate   *string  `json:"endDate,omitempty"`
	}{Mode: dateRange.mode, StartDate: dateRange.startDate, EndDate: dateRange.endDate, SourceIDs: make([]string, len(sourceIDs))}
	for index, id := range sourceIDs {
		value.SourceIDs[index] = compactUUID(id)
	}
	return json.Marshal(value)
}

func ingestChildParameters(sourceID uuid.UUID, generation int64, dateRange ingestRange) ([]byte, error) {
	return json.Marshal(struct {
		SourceID   string  `json:"sourceId"`
		Generation int64   `json:"generation"`
		Mode       string  `json:"mode"`
		StartDate  *string `json:"startDate,omitempty"`
		EndDate    *string `json:"endDate,omitempty"`
	}{compactUUID(sourceID), generation, dateRange.mode, dateRange.startDate, dateRange.endDate})
}

func manualIngestKey(sources []ingestSource, dateRange ingestRange) [32]byte {
	hash := sha256.New()
	hash.Write([]byte("workouts-explorer/manual-ingest/v1\x00"))
	for _, source := range sources {
		hash.Write(source.id[:])
		var generation [8]byte
		binary.BigEndian.PutUint64(generation[:], uint64(source.generation))
		hash.Write(generation[:])
	}
	hash.Write([]byte("\x00" + dateRange.mode + "\x00"))
	if dateRange.startDate != nil {
		hash.Write([]byte(*dateRange.startDate + "\x00" + *dateRange.endDate))
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func writeIngestAccepted(w http.ResponseWriter, parentID uuid.UUID, status string, reused bool) {
	jobID := compactUUID(parentID)
	w.Header().Set("Location", "/api/jobs/"+jobID)
	writeJSON(w, http.StatusAccepted, generated.IngestAccepted{JobId: jobID, Status: generated.IngestAcceptedStatus(status), Reused: reused})
}

func writeIngestUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "ingest service is temporarily unavailable")
}
