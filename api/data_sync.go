package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func (s *Server) GetDataSync(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	tx, err := s.accountTransactionWithOptions(r.Context(), *session.accountID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		writeDataSyncUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	result := generated.DataSync{
		Sources:       []generated.DataSyncSource{},
		Notifications: []generated.Notification{},
	}
	result.Schedule.Cadence.SetNull()
	var lastJobID *uuid.UUID
	if err = tx.QueryRow(r.Context(), `SELECT cadence_seconds,stale_days,next_run_at,last_enqueued_at,last_job_id
		FROM app.read_owned_sync_schedule()`).Scan(&result.Schedule.CadenceSeconds, &result.Schedule.StaleDays,
		&result.Schedule.NextRunAt, &result.Schedule.LastEnqueuedAt, &lastJobID); err != nil {
		writeDataSyncUnavailable(w, r)
		return
	}
	if lastJobID != nil {
		value := compactUUID(*lastJobID)
		result.Schedule.LastJobId = &value
	}
	var evaluated int
	if err = tx.QueryRow(r.Context(), `SELECT app.evaluate_source_staleness(NULL,$1,clock_timestamp())`, result.Schedule.StaleDays).Scan(&evaluated); err != nil {
		writeDataSyncUnavailable(w, r)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT source.id,source.display_name,source.type,source.status,source.auto_sync_enabled,source.checked_at,
		state.last_sync_started_at,state.last_sync_succeeded_at,state.last_new_export_discovered_at,state.last_new_export_date,state.stale_since
		FROM app.sources source LEFT JOIN app.source_sync_state state ON state.account_id=source.account_id AND state.source_id=source.id
		ORDER BY source.id`)
	if err != nil {
		writeDataSyncUnavailable(w, r)
		return
	}
	for rows.Next() {
		var source generated.DataSyncSource
		var id uuid.UUID
		var exportDate, staleSince *time.Time
		if err = rows.Scan(&id, &source.DisplayName, &source.Type, &source.Status, &source.AutoSyncEnabled, &source.CheckedAt,
			&source.Freshness.LastSyncStartedAt, &source.Freshness.LastSyncSucceededAt,
			&source.Freshness.LastNewExportDiscoveredAt, &exportDate, &staleSince); err != nil {
			rows.Close()
			writeDataSyncUnavailable(w, r)
			return
		}
		source.Id = compactUUID(id)
		if exportDate != nil {
			source.Freshness.LastNewExportDate = &openapi_types.Date{Time: *exportDate}
		}
		if staleSince != nil {
			source.Freshness.StaleSince = &openapi_types.Date{Time: *staleSince}
		}
		if source.AutoSyncEnabled {
			result.Schedule.SourceCount++
		}
		result.Sources = append(result.Sources, source)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		writeDataSyncUnavailable(w, r)
		return
	}
	rows.Close()
	result.Schedule.Enabled = result.Schedule.SourceCount > 0
	result.ActiveJob, err = readDataSyncJob(r.Context(), tx, true)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeDataSyncUnavailable(w, r)
		return
	}
	result.LatestJob, err = readDataSyncJob(r.Context(), tx, false)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeDataSyncUnavailable(w, r)
		return
	}
	var activeNotificationCount int64
	err = tx.QueryRow(r.Context(), `SELECT count(*) FROM app.notifications
		WHERE state='unresolved' OR (state='remind' AND remind_at<=clock_timestamp())`).Scan(&activeNotificationCount)
	if err == nil {
		result.Notifications, err = readNotifications(r.Context(), tx, "active", 100, 0)
	}
	result.NotificationsTruncated = activeNotificationCount > 100
	if err != nil || tx.Commit(r.Context()) != nil {
		writeDataSyncUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func readDataSyncJob(ctx context.Context, tx pgx.Tx, active bool) (*generated.JobSummary, error) {
	statusClause := `j.status IN ('queued','running')`
	if !active {
		statusClause = `j.status IN ('succeeded','partially_succeeded','failed','cancelled')`
	}
	row := tx.QueryRow(ctx, `SELECT j.id,j.kind,j.status,j.progress_current,COALESCE(j.progress_total,0),j.started_at,j.terminal_at,j.created_at,j.updated_at,
		COALESCE(p.files_discovered,0),COALESCE(p.files_skipped,0),COALESCE(p.files_succeeded,0),COALESCE(p.files_failed,0),
		COALESCE(p.workouts_created,0),COALESCE(p.workouts_updated,0),COALESCE(p.workouts_unchanged,0),COALESCE(p.workouts_rejected,0)
		FROM app.jobs j LEFT JOIN app.job_progress p ON p.job_id=j.id AND p.account_id=j.account_id
		WHERE j.parent_job_id IS NULL AND j.kind IN ('manual_ingest','scheduled_ingest') AND `+statusClause+`
		ORDER BY j.created_at DESC,j.id DESC LIMIT 1`)
	var item generated.JobSummary
	var id uuid.UUID
	var kind string
	if err := row.Scan(&id, &kind, &item.Status, &item.Progress.Current, &item.Progress.Total, &item.StartedAt, &item.TerminalAt,
		&item.CreatedAt, &item.UpdatedAt, &item.Progress.FilesDiscovered, &item.Progress.FilesSkipped, &item.Progress.FilesSucceeded,
		&item.Progress.FilesFailed, &item.Progress.WorkoutsCreated, &item.Progress.WorkoutsUpdated,
		&item.Progress.WorkoutsUnchanged, &item.Progress.WorkoutsRejected); err != nil {
		return nil, err
	}
	item.Id, item.Trigger = compactUUID(id), jobTrigger(kind)
	return &item, nil
}

func writeDataSyncUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "data sync service is temporarily unavailable")
}
