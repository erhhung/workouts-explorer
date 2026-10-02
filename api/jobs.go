package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const jobDetailSelect = `SELECT j.id,j.parent_job_id,j.kind,j.status,j.attempt,j.progress_current,COALESCE(j.progress_total,0),
	j.cancel_requested_at,j.started_at,j.terminal_at,j.failure_code,j.failure_summary,j.retry_of_job_id,j.created_at,j.updated_at,
	COALESCE(p.files_discovered,0),COALESCE(p.files_skipped,0),COALESCE(p.files_succeeded,0),COALESCE(p.files_failed,0),
	COALESCE(p.workouts_created,0),COALESCE(p.workouts_updated,0),COALESCE(p.workouts_unchanged,0),COALESCE(p.workouts_rejected,0),
	c.source_id,c.source_generation,c.display_name,c.source_type,
	COALESCE(cp.routes_total,0),COALESCE(cp.routes_processed,0),COALESCE(cp.routes_succeeded,0),
	COALESCE(cp.routes_failed,0),COALESCE(cp.routes_cancelled,0),COALESCE(cp.routes_superseded,0),
	COALESCE((SELECT count(*) FROM app.jobs running_child WHERE running_child.parent_job_id=j.id AND running_child.status='running'),0),
	cc.region_id,cc.target_osm_generation,cc.target_work_revision,cc.rules_version,cc.sampling_version,cc.path_policy_version,
	rc.workout_id,rc.result_outcome,rc.duration_milliseconds,w.started_at,w.local_start_date,wt.provider_label,
	CASE WHEN rc.job_id IS NULL THEN NULL ELSE (SELECT COALESCE(jsonb_agg(jsonb_build_object(
		'regionId',target.item->>'regionId','generation',(target.item->>'generation')::bigint,
		'latest',COALESCE(region.desired_osm_generation=(target.item->>'generation')::bigint,false))
		ORDER BY target.item->>'regionId',(target.item->>'generation')::bigint DESC),'[]'::jsonb)
		FROM jsonb_array_elements(rc.target_generations) target(item)
		LEFT JOIN app.workout_coverage_regions region ON region.account_id=rc.account_id AND region.workout_id=rc.workout_id
			AND region.region_id=target.item->>'regionId') END
	FROM app.jobs j LEFT JOIN app.job_progress p ON p.job_id=j.id AND p.account_id=j.account_id
	LEFT JOIN app.job_source_contexts c ON c.job_id=j.id AND c.account_id=j.account_id
	LEFT JOIN app.coverage_job_progress cp ON cp.job_id=j.id AND cp.account_id=j.account_id
	LEFT JOIN app.coverage_job_contexts cc ON cc.job_id=j.id AND cc.account_id=j.account_id
	LEFT JOIN app.coverage_route_job_contexts rc ON rc.job_id=j.id AND rc.account_id=j.account_id
	LEFT JOIN app.workouts w ON w.id=rc.workout_id AND w.account_id=rc.account_id
	LEFT JOIN app.workout_types wt ON wt.id=w.workout_type_id AND wt.account_id=w.account_id`

const (
	maxJobRetryOrdinal  = 100
	jobRetryLimitDetail = "job retry limit has been reached"
)

var errInvalidJobRetryLineage = errors.New("invalid stored job retry lineage")

func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, params generated.ListJobsParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	page, pageSize := 1, 25
	if params.Page != nil {
		page = *params.Page
	}
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())

	status, operation := "", ""
	if params.Status != nil {
		status = string(*params.Status)
	}
	if params.Operation != nil {
		operation = string(*params.Operation)
	}
	var total int64
	err = tx.QueryRow(r.Context(), `SELECT count(*) FROM app.jobs WHERE parent_job_id IS NULL
		AND kind IN ('manual_ingest','scheduled_ingest','workout_deletion','coverage_update') AND ($1='' OR
			CASE WHEN kind='coverage_update' AND status='running' AND NOT EXISTS (
				SELECT 1 FROM app.jobs running_child WHERE running_child.parent_job_id=jobs.id AND running_child.status='running')
			THEN 'queued' ELSE status END=$1)
		AND NOT EXISTS (SELECT 1 FROM app.jobs successor WHERE successor.retry_of_job_id=jobs.id
			AND successor.parent_job_id IS NULL AND ((jobs.kind IN ('manual_ingest','scheduled_ingest') AND successor.kind IN ('manual_ingest','scheduled_ingest'))
				OR (jobs.kind='workout_deletion' AND successor.kind='workout_deletion')
				OR (jobs.kind='coverage_update' AND successor.kind='coverage_update')))
		AND ($2='' OR kind=CASE $2 WHEN 'manual_sync' THEN 'manual_ingest' WHEN 'automated_sync' THEN 'scheduled_ingest' WHEN 'workout_deletion' THEN 'workout_deletion' WHEN 'coverage_update' THEN 'coverage_update' END)`, status, operation).Scan(&total)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT j.id,j.kind,j.status,j.progress_current,COALESCE(j.progress_total,0),
		j.started_at,j.terminal_at,j.created_at,j.updated_at,
		COALESCE(p.files_discovered,0),COALESCE(p.files_skipped,0),COALESCE(p.files_succeeded,0),COALESCE(p.files_failed,0),
		COALESCE(p.workouts_created,0),COALESCE(p.workouts_updated,0),COALESCE(p.workouts_unchanged,0),COALESCE(p.workouts_rejected,0),
		COALESCE(cp.routes_total,0),COALESCE(cp.routes_processed,0),COALESCE(cp.routes_succeeded,0),
		COALESCE(cp.routes_failed,0),COALESCE(cp.routes_cancelled,0),COALESCE(cp.routes_superseded,0),
		COALESCE((SELECT count(*) FROM app.jobs running_child WHERE running_child.parent_job_id=j.id AND running_child.status='running'),0)
		FROM app.jobs j LEFT JOIN app.job_progress p ON p.job_id=j.id AND p.account_id=j.account_id
		LEFT JOIN app.coverage_job_progress cp ON cp.job_id=j.id AND cp.account_id=j.account_id
		WHERE j.parent_job_id IS NULL AND j.kind IN ('manual_ingest','scheduled_ingest','workout_deletion','coverage_update') AND ($1='' OR
			CASE WHEN j.kind='coverage_update' AND j.status='running' AND NOT EXISTS (
				SELECT 1 FROM app.jobs running_filter_child WHERE running_filter_child.parent_job_id=j.id AND running_filter_child.status='running')
			THEN 'queued' ELSE j.status END=$1)
		AND NOT EXISTS (SELECT 1 FROM app.jobs successor WHERE successor.retry_of_job_id=j.id
			AND successor.parent_job_id IS NULL AND ((j.kind IN ('manual_ingest','scheduled_ingest') AND successor.kind IN ('manual_ingest','scheduled_ingest'))
				OR (j.kind='workout_deletion' AND successor.kind='workout_deletion')
				OR (j.kind='coverage_update' AND successor.kind='coverage_update')))
		AND ($2='' OR j.kind=CASE $2 WHEN 'manual_sync' THEN 'manual_ingest' WHEN 'automated_sync' THEN 'scheduled_ingest' WHEN 'workout_deletion' THEN 'workout_deletion' WHEN 'coverage_update' THEN 'coverage_update' END)
		ORDER BY j.created_at DESC,j.id DESC LIMIT $3 OFFSET $4`, status, operation, pageSize, (page-1)*pageSize)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]generated.JobSummary, 0)
	for rows.Next() {
		var id uuid.UUID
		var kind, jobStatus string
		var current, progressTotal int64
		var started, terminal *time.Time
		var created, updated time.Time
		progress := generated.JobProgress{}
		routeStats := generated.CoverageRouteStats{}
		if err = rows.Scan(&id, &kind, &jobStatus, &current, &progressTotal, &started, &terminal, &created, &updated,
			&progress.FilesDiscovered, &progress.FilesSkipped, &progress.FilesSucceeded, &progress.FilesFailed,
			&progress.WorkoutsCreated, &progress.WorkoutsUpdated, &progress.WorkoutsUnchanged, &progress.WorkoutsRejected,
			&routeStats.Total, &routeStats.Processed, &routeStats.Succeeded, &routeStats.Failed,
			&routeStats.Cancelled, &routeStats.Superseded, &routeStats.Running); err != nil {
			writeJobUnavailable(w, r)
			return
		}
		progress.Current, progress.Total = current, progressTotal
		operation := generated.JobSummaryOperationDataSync
		if kind == "workout_deletion" {
			operation = generated.JobSummaryOperationWorkoutDeletion
		} else if kind == "coverage_update" {
			operation = generated.JobSummaryOperationCoverageUpdate
		}
		if kind == "coverage_update" {
			jobStatus = coverageParentPresentationStatus(jobStatus, routeStats.Running)
		}
		item := generated.JobSummary{Id: compactUUID(id), Trigger: jobTrigger(kind), Status: generated.JobStatus(jobStatus),
			Operation: &operation, Progress: progress, StartedAt: started, TerminalAt: terminal, CreatedAt: created, UpdatedAt: updated}
		if kind == "coverage_update" {
			item.RouteStats = &routeStats
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeJobUnavailable(w, r)
		return
	}
	totalPages := int64(0)
	if total > 0 {
		totalPages = (total + int64(pageSize) - 1) / int64(pageSize)
	}
	writeJSON(w, http.StatusOK, generated.JobList{Items: items, Pagination: generated.Pagination{
		Page: page, PageSize: pageSize, TotalItems: total, TotalPages: totalPages,
	}})
}

func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, jobID generated.JobID) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(jobID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "job ID is invalid")
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	detail, err := readJobDetail(r.Context(), tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "job was not found")
		return
	}
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) CreateJobCancellation(w http.ResponseWriter, r *http.Request, jobID generated.JobID, params generated.CreateJobCancellationParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.JobCancellationCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	id, valid := parseCompactUUID(jobID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "job ID is invalid")
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var jobKind string
	if err = tx.QueryRow(r.Context(), `SELECT kind FROM app.jobs WHERE id=$1`, id).Scan(&jobKind); errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "job was not found")
		return
	} else if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	if jobKind == "workout_deletion" {
		writeProblem(w, r, http.StatusConflict, "Conflict", "workout deletion cannot be cancelled")
		return
	}
	if _, err = readJobDetail(r.Context(), tx, id); errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "job was not found")
		return
	} else if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	var changed bool
	if err = tx.QueryRow(r.Context(), `SELECT app.request_owned_job_cancellation($1,$2)`, id, session.principalID).Scan(&changed); err != nil {
		writeJobUnavailable(w, r)
		return
	}
	if !changed {
		writeProblem(w, r, http.StatusConflict, "Conflict", "terminal jobs cannot be cancelled")
		return
	}
	detail, err := readJobDetail(r.Context(), tx, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) CreateJobRetry(w http.ResponseWriter, r *http.Request, jobID generated.JobID, params generated.CreateJobRetryParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.EmptyCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	id, valid := parseCompactUUID(jobID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "job ID is invalid")
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())

	var kind, status string
	var parentID *uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT kind,status,parent_job_id FROM app.jobs WHERE id=$1
		AND kind IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source','workout_deletion','coverage_update')`, id).Scan(&kind, &status, &parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "job was not found")
		return
	}
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	lineages, err := readJobRetryLineages(r.Context(), tx, []uuid.UUID{id})
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	if lineages[id].ordinal >= maxJobRetryOrdinal {
		writeProblem(w, r, http.StatusConflict, "Conflict", jobRetryLimitDetail)
		return
	}
	if status != "failed" && status != "cancelled" && status != "partially_succeeded" {
		writeProblem(w, r, http.StatusConflict, "Conflict", "job is not retryable")
		return
	}
	if kind == "coverage_update" {
		var retryID uuid.UUID
		var routeCount int
		err = tx.QueryRow(r.Context(), `SELECT job_id,route_count FROM app.retry_coverage_update($1,$2,$3)`,
			id, session.principalID, maxJobRetryOrdinal).Scan(&retryID, &routeCount)
		if errors.Is(err, pgx.ErrNoRows) {
			writeProblem(w, r, http.StatusConflict, "Conflict", "coverage update has no unsuccessful routes to retry")
			return
		}
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && (databaseError.Code == "55000" || databaseError.Code == "54001") {
			writeProblem(w, r, http.StatusConflict, "Conflict", databaseError.Message)
			return
		}
		if err != nil || routeCount < 1 || tx.Commit(r.Context()) != nil {
			writeJobUnavailable(w, r)
			return
		}
		writeIngestAccepted(w, retryID, "queued", false)
		return
	}
	if kind == "workout_deletion" {
		var retryID uuid.UUID
		var targetCount int
		if err = tx.QueryRow(r.Context(), `SELECT job_id,target_count FROM app.retry_workout_deletion($1,$2,$3)`,
			id, session.principalID, maxJobRetryOrdinal).Scan(&retryID, &targetCount); errors.Is(err, pgx.ErrNoRows) {
			writeProblem(w, r, http.StatusConflict, "Conflict", "workout deletion has no pending targets to retry")
			return
		} else if err != nil {
			writeJobUnavailable(w, r)
			return
		}
		if targetCount < 1 || tx.Commit(r.Context()) != nil {
			writeJobUnavailable(w, r)
			return
		}
		writeIngestAccepted(w, retryID, "queued", false)
		return
	}

	type retryChild struct {
		id       uuid.UUID
		sourceID uuid.UUID
		raw      []byte
	}
	children := make([]retryChild, 0)
	if parentID == nil {
		rows, queryErr := tx.Query(r.Context(), `SELECT child.id,context.source_id,child.parameters
			FROM app.jobs child JOIN app.job_source_contexts context ON context.job_id=child.id AND context.account_id=child.account_id
			WHERE child.parent_job_id=$1 AND child.status IN ('failed','cancelled') ORDER BY context.source_id`, id)
		if queryErr != nil {
			writeJobUnavailable(w, r)
			return
		}
		for rows.Next() {
			var child retryChild
			if err = rows.Scan(&child.id, &child.sourceID, &child.raw); err != nil {
				rows.Close()
				writeJobUnavailable(w, r)
				return
			}
			children = append(children, child)
		}
		err = rows.Err()
		rows.Close()
	} else {
		var child retryChild
		err = tx.QueryRow(r.Context(), `SELECT job.id,context.source_id,job.parameters FROM app.jobs job
			JOIN app.job_source_contexts context ON context.job_id=job.id AND context.account_id=job.account_id
			WHERE job.id=$1`, id).Scan(&child.id, &child.sourceID, &child.raw)
		children = append(children, child)
	}
	if err != nil || len(children) == 0 {
		if err == nil {
			writeProblem(w, r, http.StatusConflict, "Conflict", "job has no unsuccessful sources to retry")
		} else {
			writeJobUnavailable(w, r)
		}
		return
	}
	childIDs := make([]uuid.UUID, 0, len(children))
	for _, child := range children {
		if child.id != id {
			childIDs = append(childIDs, child.id)
		}
	}
	if len(childIDs) > 0 {
		childLineages, lineageErr := readJobRetryLineages(r.Context(), tx, childIDs)
		if lineageErr != nil {
			writeJobUnavailable(w, r)
			return
		}
		for _, childID := range childIDs {
			if childLineages[childID].ordinal >= maxJobRetryOrdinal {
				writeProblem(w, r, http.StatusConflict, "Conflict", jobRetryLimitDetail)
				return
			}
		}
	}

	sourceIDs := make([]uuid.UUID, 0, len(children))
	childRetries := make(map[uuid.UUID]uuid.UUID, len(children))
	var dateRange ingestRange
	for index, child := range children {
		parsed, parseErr := decodeRetryIngestRange(child.raw)
		if parseErr != nil || (index > 0 && !sameIngestRange(dateRange, parsed)) {
			writeProblem(w, r, http.StatusConflict, "Conflict", "job parameters cannot be retried safely")
			return
		}
		dateRange = parsed
		sourceIDs = append(sourceIDs, child.sourceID)
		childRetries[child.sourceID] = child.id
	}
	retryParentID := id
	if parentID != nil {
		retryParentID = *parentID
	}
	result, err := s.enqueueIngest(r.Context(), tx, *session.accountID, requestIDFrom(r.Context()), ingestEnqueueRequest{
		trigger: "manual", priority: 80, sourceIDs: sourceIDs, dateRange: dateRange,
		retryOfParent: &retryParentID, childRetries: childRetries,
	})
	if errors.Is(err, errIngestSourceUnavailable) || errors.Is(err, errIngestSourceDisconnected) {
		writeProblem(w, r, http.StatusConflict, "Conflict", "one or more retry sources are unavailable")
		return
	}
	if errors.Is(err, errEarlierIngestActive) {
		writeProblem(w, r, http.StatusConflict, "Conflict", errEarlierIngestActive.Error())
		return
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeJobUnavailable(w, r)
		return
	}
	writeIngestAccepted(w, result.parentID, result.status, result.reused)
}

func decodeRetryIngestRange(raw []byte) (ingestRange, error) {
	var value struct {
		SourceID   string  `json:"sourceId"`
		Generation int64   `json:"generation"`
		Mode       string  `json:"mode"`
		StartDate  *string `json:"startDate"`
		EndDate    *string `json:"endDate"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Generation < 1 {
		return ingestRange{}, errors.New("invalid ingest parameters")
	}
	if value.Mode == "incremental" && value.StartDate == nil && value.EndDate == nil {
		return ingestRange{mode: "incremental"}, nil
	}
	if value.Mode != "bounded" || value.StartDate == nil || value.EndDate == nil {
		return ingestRange{}, errors.New("invalid ingest parameters")
	}
	start, startErr := time.Parse(time.DateOnly, *value.StartDate)
	end, endErr := time.Parse(time.DateOnly, *value.EndDate)
	if startErr != nil || endErr != nil || end.Before(start) {
		return ingestRange{}, errors.New("invalid ingest parameters")
	}
	return ingestRange{mode: "bounded", startDate: value.StartDate, endDate: value.EndDate}, nil
}

func sameIngestRange(left, right ingestRange) bool {
	if left.mode != right.mode || (left.startDate == nil) != (right.startDate == nil) {
		return false
	}
	return left.startDate == nil || (*left.startDate == *right.startDate && *left.endDate == *right.endDate)
}

func (s *Server) ListJobFiles(w http.ResponseWriter, r *http.Request, jobID generated.JobID, params generated.ListJobFilesParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(jobID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "job ID is invalid")
		return
	}
	page, pageSize := ownerPage(params.Page, params.PageSize)
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	if err = requireOwnedIngestJob(r.Context(), tx, id); errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "job was not found")
		return
	} else if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	var total int64
	err = tx.QueryRow(r.Context(), `SELECT app.count_owned_job_rows($1,'file')`, id).Scan(&total)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT total_count,id,job_id,source_id,source_generation,display_name,source_type,basename,
		state,size_bytes,processing_started_at,processed_at,failure_code,failure_summary,created_at,updated_at
		FROM app.read_owned_job_files($1,$2,$3)`, id, pageSize, (page-1)*pageSize)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]generated.JobFile, 0)
	for rows.Next() {
		var item generated.JobFile
		var fileID, childID, sourceID uuid.UUID
		var ignoredTotal int64
		if err = rows.Scan(&ignoredTotal, &fileID, &childID, &sourceID, &item.Source.Generation, &item.Source.DisplayName, &item.Source.SourceType,
			&item.Basename, &item.State, &item.SizeBytes, &item.ProcessingStartedAt, &item.ProcessedAt, &item.FailureCode,
			&item.FailureSummary, &item.CreatedAt, &item.UpdatedAt); err != nil {
			writeJobUnavailable(w, r)
			return
		}
		item.Id, item.JobId, item.Source.SourceId = compactUUID(fileID), compactUUID(childID), compactUUID(sourceID)
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeJobUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, generated.JobFileList{Items: items, Pagination: pagination(page, pageSize, total)})
}

func (s *Server) ListJobEvents(w http.ResponseWriter, r *http.Request, jobID generated.JobID, params generated.ListJobEventsParams) {
	s.listJobDiagnostics(w, r, jobID, params.Page, params.PageSize, false)
}

func (s *Server) ListJobLogs(w http.ResponseWriter, r *http.Request, jobID generated.JobID, params generated.ListJobLogsParams) {
	s.listJobDiagnostics(w, r, jobID, params.Page, params.PageSize, true)
}

func (s *Server) listJobDiagnostics(w http.ResponseWriter, r *http.Request, jobID generated.JobID, pageParam, sizeParam *int, logs bool) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	id, valid := parseCompactUUID(jobID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "job ID is invalid")
		return
	}
	page, pageSize := ownerPage(pageParam, sizeParam)
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	if err = requireOwnedObservableJob(r.Context(), tx, id); errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "job was not found")
		return
	} else if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	var total int64
	if logs {
		err = tx.QueryRow(r.Context(), `SELECT app.count_owned_job_rows($1,'log')`, id).Scan(&total)
	} else {
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM app.job_events diagnostic JOIN app.jobs job
			ON job.id=diagnostic.job_id AND job.account_id=diagnostic.account_id WHERE job.id=$1 OR job.parent_job_id=$1`, id).Scan(&total)
	}
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	var rows pgx.Rows
	if logs {
		rows, err = tx.Query(r.Context(), `SELECT total_count,id,job_id,severity,code,message,fields,created_at
			FROM app.read_owned_job_logs($1,$2,$3)`, id, pageSize, (page-1)*pageSize)
	} else {
		rows, err = tx.Query(r.Context(), `SELECT diagnostic.id,diagnostic.job_id,diagnostic.severity,diagnostic.code,
			diagnostic.safe_message,diagnostic.fields,diagnostic.created_at FROM app.job_events diagnostic JOIN app.jobs job
			ON job.id=diagnostic.job_id AND job.account_id=diagnostic.account_id WHERE job.id=$1 OR job.parent_job_id=$1
			ORDER BY diagnostic.created_at,diagnostic.id LIMIT $2 OFFSET $3`, id, pageSize, (page-1)*pageSize)
	}
	if err != nil {
		writeJobUnavailable(w, r)
		return
	}
	defer rows.Close()
	if logs {
		items := make([]generated.JobLog, 0)
		for rows.Next() {
			var item generated.JobLog
			var childID uuid.UUID
			var fields []byte
			var ignoredTotal int64
			if err = rows.Scan(&ignoredTotal, &item.Id, &childID, &item.Severity, &item.Code, &item.Message, &fields, &item.CreatedAt); err != nil || json.Unmarshal(fields, &item.Fields) != nil {
				writeJobUnavailable(w, r)
				return
			}
			item.JobId = compactUUID(childID)
			items = append(items, item)
		}
		if rows.Err() != nil {
			writeJobUnavailable(w, r)
			return
		}
		writeJSON(w, http.StatusOK, generated.JobLogList{Items: items, Pagination: pagination(page, pageSize, total)})
		return
	}
	items := make([]generated.JobEvent, 0)
	for rows.Next() {
		var item generated.JobEvent
		var childID uuid.UUID
		var fields []byte
		if err = rows.Scan(&item.Id, &childID, &item.Severity, &item.Code, &item.Message, &fields, &item.CreatedAt); err != nil || json.Unmarshal(fields, &item.Fields) != nil {
			writeJobUnavailable(w, r)
			return
		}
		item.JobId = compactUUID(childID)
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeJobUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, generated.JobEventList{Items: items, Pagination: pagination(page, pageSize, total)})
}

func ownerPage(pageParam, sizeParam *int) (int, int) {
	page, size := 1, 25
	if pageParam != nil {
		page = *pageParam
	}
	if sizeParam != nil {
		size = *sizeParam
	}
	return page, size
}

func pagination(page, size int, total int64) generated.Pagination {
	pages := int64(0)
	if total > 0 {
		pages = (total + int64(size) - 1) / int64(size)
	}
	return generated.Pagination{Page: page, PageSize: size, TotalItems: total, TotalPages: pages}
}

func requireOwnedIngestJob(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT true FROM app.jobs WHERE id=$1 AND kind IN
		('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source')`, id).Scan(&exists)
	return err
}

func coverageParentPresentationStatus(status string, runningChildren int) string {
	if status == "running" && runningChildren == 0 {
		return "queued"
	}
	return status
}

func requireOwnedObservableJob(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var exists bool
	return tx.QueryRow(ctx, `SELECT true FROM app.jobs WHERE id=$1 AND kind IN
		('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source',
		 'coverage_update','coverage_update_route')`, id).Scan(&exists)
}

func readJobDetail(ctx context.Context, tx pgx.Tx, id uuid.UUID) (generated.JobDetail, error) {
	detail, kind, err := scanJobDetail(tx.QueryRow(ctx, jobDetailSelect+`
		WHERE j.id=$1 AND j.kind IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source','workout_deletion','coverage_update','coverage_update_route')`, id))
	if err != nil {
		return generated.JobDetail{}, err
	}
	if kind == "manual_ingest" || kind == "scheduled_ingest" {
		rows, queryErr := tx.Query(ctx, jobDetailSelect+` WHERE j.parent_job_id=$1
			AND j.kind IN ('manual_ingest_source','scheduled_ingest_source') ORDER BY c.source_id,j.id`, id)
		if queryErr != nil {
			return generated.JobDetail{}, queryErr
		}
		for rows.Next() {
			child, _, scanErr := scanJobDetail(rows)
			if scanErr != nil {
				rows.Close()
				return generated.JobDetail{}, scanErr
			}
			detail.Children = append(detail.Children, child)
		}
		if rows.Err() != nil {
			rows.Close()
			return generated.JobDetail{}, rows.Err()
		}
		rows.Close()
	} else if kind == "coverage_update" {
		rows, queryErr := tx.Query(ctx, jobDetailSelect+` WHERE j.parent_job_id=$1
			AND j.kind='coverage_update_route' ORDER BY w.started_at,j.id`, id)
		if queryErr != nil {
			return generated.JobDetail{}, queryErr
		}
		for rows.Next() {
			child, _, scanErr := scanJobDetail(rows)
			if scanErr != nil {
				rows.Close()
				return generated.JobDetail{}, scanErr
			}
			detail.Children = append(detail.Children, child)
		}
		if rows.Err() != nil {
			rows.Close()
			return generated.JobDetail{}, rows.Err()
		}
		rows.Close()
	}
	refs := make([]jobDetailRef, 0, len(detail.Children)+1)
	refs = append(refs, jobDetailRef{id: id, detail: &detail})
	for index := range detail.Children {
		childID, valid := parseCompactUUID(detail.Children[index].Id)
		if !valid {
			return generated.JobDetail{}, errors.New("invalid stored job ID")
		}
		refs = append(refs, jobDetailRef{id: childID, detail: &detail.Children[index]})
	}
	if err = readJobRetryMetadata(ctx, tx, refs); err != nil {
		return generated.JobDetail{}, err
	}
	return detail, nil
}

type jobDetailRef struct {
	id     uuid.UUID
	detail *generated.JobDetail
}

func readJobRetryMetadata(ctx context.Context, tx pgx.Tx, refs []jobDetailRef) error {
	ids := make([]uuid.UUID, len(refs))
	byID := make(map[uuid.UUID]*generated.JobDetail, len(refs))
	for index, ref := range refs {
		ids[index] = ref.id
		byID[ref.id] = ref.detail
		ref.detail.RetriedByJobIds = []generated.CompactUUID{}
	}

	lineages, err := readJobRetryLineages(ctx, tx, ids)
	if errors.Is(err, errInvalidJobRetryLineage) {
		lineages = make(map[uuid.UUID]jobRetryLineage, len(ids))
		for _, id := range ids {
			lineage, lineageErr := readJobRetryLineages(ctx, tx, []uuid.UUID{id})
			if lineageErr == nil {
				lineages[id] = lineage[id]
				continue
			}
			if !errors.Is(lineageErr, errInvalidJobRetryLineage) {
				return lineageErr
			}
		}
	} else if err != nil {
		return err
	}
	validIDs := make([]uuid.UUID, 0, len(lineages))
	for id, lineage := range lineages {
		root := compactUUID(lineage.rootID)
		ordinal := lineage.ordinal
		byID[id].RetryRootJobId = &root
		byID[id].RetryOrdinal = &ordinal
		validIDs = append(validIDs, id)
	}
	if len(validIDs) == 0 {
		return nil
	}

	rows, err := tx.Query(ctx, `SELECT requested.id,successor.id FROM unnest($1::uuid[]) requested(id)
		JOIN LATERAL (SELECT job.id,job.created_at FROM app.jobs job WHERE job.retry_of_job_id=requested.id
			AND job.kind IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source','workout_deletion','coverage_update','coverage_update_route')
			ORDER BY job.created_at,job.id LIMIT 100) successor ON true
		ORDER BY requested.id,successor.created_at,successor.id`, validIDs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var requestedID, successorID uuid.UUID
		if err := rows.Scan(&requestedID, &successorID); err != nil {
			return err
		}
		if detail := byID[requestedID]; detail != nil {
			detail.RetriedByJobIds = append(detail.RetriedByJobIds, compactUUID(successorID))
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	latestRows, err := tx.Query(ctx, `WITH RECURSIVE descendants(requested_id,id,retry_of_job_id,parent_job_id,kind,depth,path,cycle,created_at) AS (
		SELECT requested.id,job.id,job.retry_of_job_id,job.parent_job_id,job.kind,0,ARRAY[job.id],false,job.created_at
		FROM unnest($1::uuid[]) requested(id) JOIN app.jobs job ON job.id=requested.id
		UNION ALL
		SELECT current.requested_id,successor.id,successor.retry_of_job_id,successor.parent_job_id,successor.kind,current.depth+1,
			current.path||successor.id,successor.id=ANY(current.path),successor.created_at
		FROM descendants current JOIN app.jobs successor ON successor.retry_of_job_id=current.id
		WHERE current.depth<$2 AND NOT current.cycle AND current.parent_job_id IS NULL AND successor.parent_job_id IS NULL
			AND ((current.kind IN ('manual_ingest','scheduled_ingest') AND successor.kind IN ('manual_ingest','scheduled_ingest'))
				OR (current.kind='workout_deletion' AND successor.kind='workout_deletion')
				OR (current.kind='coverage_update' AND successor.kind='coverage_update'))
	), latest AS (
		SELECT DISTINCT ON (requested_id) requested_id,id,depth,cycle FROM descendants WHERE depth>0
		ORDER BY requested_id,depth DESC,created_at DESC,id DESC
	)
	SELECT requested_id,id,depth,cycle FROM latest ORDER BY requested_id`, validIDs, maxJobRetryOrdinal)
	if err != nil {
		return err
	}
	for latestRows.Next() {
		var requestedID, latestID uuid.UUID
		var depth int
		var cycle bool
		if err := latestRows.Scan(&requestedID, &latestID, &depth, &cycle); err != nil {
			return err
		}
		if cycle {
			return errInvalidJobRetryLineage
		}
		detail := byID[requestedID]
		lineage, ok := lineages[requestedID]
		if detail == nil || !ok {
			return errInvalidJobRetryLineage
		}
		compactLatest := compactUUID(latestID)
		latestOrdinal := lineage.ordinal + depth
		detail.LatestRetryJobId = &compactLatest
		detail.LatestRetryOrdinal = &latestOrdinal
	}
	if err := latestRows.Err(); err != nil {
		latestRows.Close()
		return err
	}
	latestRows.Close()
	return nil
}

type jobRetryLineage struct {
	rootID  uuid.UUID
	ordinal int
}

// readJobRetryLineages treats persisted retry pointers as hostile and validates them under account RLS.
func readJobRetryLineages(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) (map[uuid.UUID]jobRetryLineage, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE lineage(requested_id,id,retry_of_job_id,parent_job_id,kind,source_id,workout_id,depth,path,cycle,consistent) AS (
		SELECT requested.id,job.id,job.retry_of_job_id,job.parent_job_id,job.kind,source.source_id,route.workout_id,0,ARRAY[job.id],false,true
		FROM unnest($1::uuid[]) requested(id) JOIN app.jobs job ON job.id=requested.id
		LEFT JOIN app.job_source_contexts source ON source.job_id=job.id AND source.account_id=job.account_id
		LEFT JOIN app.coverage_route_job_contexts route ON route.job_id=job.id AND route.account_id=job.account_id
		UNION ALL
		SELECT current.requested_id,prior.id,prior.retry_of_job_id,prior.parent_job_id,prior.kind,prior_source.source_id,prior_route.workout_id,current.depth+1,
			current.path||prior.id,prior.id=ANY(current.path),current.consistent AND
			((current.kind IN ('manual_ingest','scheduled_ingest') AND prior.kind IN ('manual_ingest','scheduled_ingest')
				AND current.parent_job_id IS NULL AND prior.parent_job_id IS NULL) OR
			 (current.kind='workout_deletion' AND prior.kind='workout_deletion'
				AND current.parent_job_id IS NULL AND prior.parent_job_id IS NULL) OR
			 (current.kind='coverage_update' AND prior.kind='coverage_update'
				AND current.parent_job_id IS NULL AND prior.parent_job_id IS NULL) OR
			 (current.kind='coverage_update_route' AND prior.kind='coverage_update_route'
				AND current.workout_id IS NOT NULL AND current.workout_id=prior_route.workout_id
				AND current.parent_job_id IS NOT NULL AND prior.parent_job_id IS NOT NULL AND EXISTS (
					SELECT 1 FROM app.jobs current_parent JOIN app.jobs prior_parent ON prior_parent.id=prior.parent_job_id
					WHERE current_parent.id=current.parent_job_id AND current_parent.kind='coverage_update'
					AND prior_parent.kind='coverage_update' AND current_parent.retry_of_job_id=prior_parent.id)) OR
			 (current.kind IN ('manual_ingest_source','scheduled_ingest_source') AND prior.kind IN ('manual_ingest_source','scheduled_ingest_source')
				AND current.source_id IS NOT NULL AND prior_source.source_id IS NOT NULL AND current.source_id=prior_source.source_id
				AND current.parent_job_id IS NOT NULL AND prior.parent_job_id IS NOT NULL AND EXISTS (
					SELECT 1 FROM app.jobs current_parent JOIN app.jobs prior_parent ON prior_parent.id=prior.parent_job_id
					WHERE current_parent.id=current.parent_job_id AND current_parent.retry_of_job_id=prior_parent.id
					AND ((current.kind='manual_ingest_source' AND current_parent.kind='manual_ingest') OR
						 (current.kind='scheduled_ingest_source' AND current_parent.kind='scheduled_ingest'))
					AND ((prior.kind='manual_ingest_source' AND prior_parent.kind='manual_ingest') OR
						 (prior.kind='scheduled_ingest_source' AND prior_parent.kind='scheduled_ingest')))))
		FROM lineage current JOIN app.jobs prior ON prior.id=current.retry_of_job_id
		LEFT JOIN app.job_source_contexts prior_source ON prior_source.job_id=prior.id AND prior_source.account_id=prior.account_id
		LEFT JOIN app.coverage_route_job_contexts prior_route ON prior_route.job_id=prior.id AND prior_route.account_id=prior.account_id
		WHERE current.retry_of_job_id IS NOT NULL AND current.depth<$2 AND NOT current.cycle
	), checks AS (
		SELECT requested_id,bool_or(cycle) cycle,bool_and(consistent) consistent FROM lineage GROUP BY requested_id
	)
	SELECT checks.requested_id,deepest.id,deepest.depth,checks.cycle,checks.consistent,deepest.retry_of_job_id,
		COALESCE((deepest.kind IN ('manual_ingest','scheduled_ingest') AND deepest.parent_job_id IS NULL) OR
		 (deepest.kind='workout_deletion' AND deepest.parent_job_id IS NULL) OR
		 (deepest.kind='coverage_update' AND deepest.parent_job_id IS NULL) OR
		 (deepest.kind='coverage_update_route' AND deepest.workout_id IS NOT NULL AND parent.kind='coverage_update' AND parent.retry_of_job_id IS NULL) OR
		 (deepest.kind='manual_ingest_source' AND deepest.source_id IS NOT NULL AND parent.kind='manual_ingest' AND parent.retry_of_job_id IS NULL) OR
		 (deepest.kind='scheduled_ingest_source' AND deepest.source_id IS NOT NULL AND parent.kind='scheduled_ingest' AND parent.retry_of_job_id IS NULL),false) root_consistent
	FROM checks JOIN LATERAL (
		SELECT id,retry_of_job_id,parent_job_id,kind,source_id,workout_id,depth FROM lineage
		WHERE requested_id=checks.requested_id ORDER BY depth DESC LIMIT 1
	) deepest ON true
	LEFT JOIN app.jobs parent ON parent.id=deepest.parent_job_id`, ids, maxJobRetryOrdinal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lineages := make(map[uuid.UUID]jobRetryLineage, len(ids))
	for rows.Next() {
		var requestedID, rootID uuid.UUID
		var depth int
		var cycle, consistent, rootConsistent bool
		var unresolvedRetryID *uuid.UUID
		if err := rows.Scan(&requestedID, &rootID, &depth, &cycle, &consistent, &unresolvedRetryID, &rootConsistent); err != nil {
			return nil, err
		}
		if cycle || !consistent || unresolvedRetryID != nil || !rootConsistent {
			return nil, errInvalidJobRetryLineage
		}
		lineages[requestedID] = jobRetryLineage{rootID: rootID, ordinal: depth + 1}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(lineages) != len(ids) {
		return nil, errInvalidJobRetryLineage
	}
	return lineages, nil
}

type jobDetailScanner interface{ Scan(...any) error }

func scanJobDetail(row jobDetailScanner) (generated.JobDetail, string, error) {
	var detail generated.JobDetail
	var id uuid.UUID
	var parentID, retryID, sourceID *uuid.UUID
	var kind, status string
	var sourceGeneration *int64
	var displayName, sourceType *string
	var coverageRegion, coverageRules, coverageSampling, coveragePolicy *string
	var coverageGeneration, coverageRevision *int64
	var routeWorkoutID *uuid.UUID
	var routeOutcome, routeType *string
	var routeDuration *int
	var routeStarted, routeLocalDate *time.Time
	var routeGenerations []byte
	var rawFailureCode, rawFailureSummary *string
	var current, total int64
	progress := generated.JobProgress{}
	routeStats := generated.CoverageRouteStats{}
	err := row.Scan(&id, &parentID, &kind, &status, &detail.Attempt, &current, &total, &detail.CancelRequestedAt,
		&detail.StartedAt, &detail.TerminalAt, &rawFailureCode, &rawFailureSummary, &retryID, &detail.CreatedAt, &detail.UpdatedAt,
		&progress.FilesDiscovered, &progress.FilesSkipped, &progress.FilesSucceeded, &progress.FilesFailed,
		&progress.WorkoutsCreated, &progress.WorkoutsUpdated, &progress.WorkoutsUnchanged, &progress.WorkoutsRejected,
		&sourceID, &sourceGeneration, &displayName, &sourceType,
		&routeStats.Total, &routeStats.Processed, &routeStats.Succeeded, &routeStats.Failed,
		&routeStats.Cancelled, &routeStats.Superseded, &routeStats.Running,
		&coverageRegion, &coverageGeneration, &coverageRevision, &coverageRules, &coverageSampling, &coveragePolicy,
		&routeWorkoutID, &routeOutcome, &routeDuration, &routeStarted, &routeLocalDate, &routeType, &routeGenerations)
	if err != nil {
		return generated.JobDetail{}, "", err
	}
	if kind == "coverage_update" {
		status = coverageParentPresentationStatus(status, routeStats.Running)
	}
	detail.Id, detail.Status, detail.Trigger = compactUUID(id), generated.JobStatus(status), jobTrigger(kind)
	operation := generated.JobDetailOperationDataSync
	if kind == "workout_deletion" {
		operation = generated.JobDetailOperationWorkoutDeletion
	} else if kind == "coverage_update" || kind == "coverage_update_route" {
		operation = generated.JobDetailOperationCoverageUpdate
	}
	detail.Operation = &operation
	detail.FailureCode, detail.FailureSummary = safeJobFailure(rawFailureCode)
	detail.CancelRequested = detail.CancelRequestedAt != nil
	detail.Children = []generated.JobDetail{}
	detail.RetriedByJobIds = []generated.CompactUUID{}
	progress.Current, progress.Total = current, total
	detail.Progress = progress
	if kind != "coverage_update" && kind != "coverage_update_route" {
		detail.Results = &struct {
			FilesFailed       *int64 `json:"filesFailed,omitempty"`
			FilesSucceeded    *int64 `json:"filesSucceeded,omitempty"`
			WorkoutsCreated   *int64 `json:"workoutsCreated,omitempty"`
			WorkoutsRejected  *int64 `json:"workoutsRejected,omitempty"`
			WorkoutsUnchanged *int64 `json:"workoutsUnchanged,omitempty"`
			WorkoutsUpdated   *int64 `json:"workoutsUpdated,omitempty"`
		}{
			FilesFailed:       &progress.FilesFailed,
			FilesSucceeded:    &progress.FilesSucceeded,
			WorkoutsCreated:   &progress.WorkoutsCreated,
			WorkoutsRejected:  &progress.WorkoutsRejected,
			WorkoutsUnchanged: &progress.WorkoutsUnchanged,
			WorkoutsUpdated:   &progress.WorkoutsUpdated,
		}
	}
	if parentID != nil {
		value := compactUUID(*parentID)
		detail.ParentJobId = &value
	}
	if retryID != nil {
		value := compactUUID(*retryID)
		detail.RetryOfJobId = &value
	}
	if sourceID != nil && sourceGeneration != nil && displayName != nil && sourceType != nil {
		detail.Source = &generated.JobSourceContext{SourceId: compactUUID(*sourceID), Generation: *sourceGeneration, DisplayName: *displayName, SourceType: *sourceType}
	}
	if coverageRegion != nil && coverageGeneration != nil && coverageRevision != nil && coverageRules != nil && coverageSampling != nil && coveragePolicy != nil {
		detail.Coverage = &generated.CoverageJobContext{RegionId: *coverageRegion, TargetOsmGeneration: *coverageGeneration,
			TargetWorkRevision: *coverageRevision, RulesVersion: *coverageRules, SamplingVersion: *coverageSampling,
			PathPolicyVersion: *coveragePolicy}
		detail.RouteStats = &routeStats
	}
	if routeWorkoutID != nil && routeStarted != nil && routeType != nil {
		context := generated.CoverageRouteContext{WorkoutId: compactUUID(*routeWorkoutID), StartedAt: *routeStarted,
			WorkoutType: *routeType, DurationMilliseconds: routeDuration}
		if len(routeGenerations) == 0 || json.Unmarshal(routeGenerations, &context.TargetGenerations) != nil || len(context.TargetGenerations) == 0 {
			return generated.JobDetail{}, "", errors.New("invalid stored coverage generation vector")
		}
		sort.Slice(context.TargetGenerations, func(left, right int) bool {
			if context.TargetGenerations[left].RegionId == context.TargetGenerations[right].RegionId {
				return context.TargetGenerations[left].Generation > context.TargetGenerations[right].Generation
			}
			return context.TargetGenerations[left].RegionId < context.TargetGenerations[right].RegionId
		})
		if routeLocalDate == nil {
			context.LocalStartDate.SetNull()
		} else {
			context.LocalStartDate.Set(openapi_types.Date{Time: *routeLocalDate})
		}
		if routeOutcome != nil {
			value := generated.CoverageRouteContextResultOutcome(*routeOutcome)
			context.ResultOutcome = &value
		}
		detail.CoverageRoute = &context
	}
	return detail, kind, nil
}

func safeJobFailure(code *string) (*string, *string) {
	if code == nil {
		return nil, nil
	}
	summaries := map[string]string{
		"ingest-parameters-invalid":  "Ingest parameters were invalid.",
		"source-config-invalid":      "Source configuration could not be read.",
		"source-unavailable":         "Source data could not be accessed.",
		"source-directory-limit":     "The source contains too many entries.",
		"source-files-changed":       "Source files changed while ingest was running.",
		"source-file-invalid":        "One or more source files could not be processed.",
		"workout-delete-failed":      "The workout deletion could not be completed.",
		"coverage-route-invalid":     "Coverage route input was invalid.",
		"coverage-route-read-failed": "Coverage route input could not be read.",
		"coverage-route-timeout":     "Coverage matching timed out.",
		"coverage-route-too-large":   "Coverage route exceeded a processing limit.",
		"coverage-osm-unavailable":   "Coverage map data is temporarily unavailable.",
		"coverage-persist-failed":    "Coverage results could not be persisted.",
	}
	summary, ok := summaries[*code]
	if !ok {
		safeCode := "job-failed"
		safeSummary := "The job could not be completed."
		return &safeCode, &safeSummary
	}
	return code, &summary
}

func jobTrigger(kind string) generated.JobTrigger {
	if kind == "coverage_update" || kind == "coverage_update_route" {
		return generated.JobTrigger("system")
	}
	if kind == "scheduled_ingest" || kind == "scheduled_ingest_source" {
		return generated.JobTrigger("scheduled")
	}
	return generated.JobTrigger("manual")
}

func writeJobUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "job service is temporarily unavailable")
}
