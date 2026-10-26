package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Callers bound selected_notifications before joining presentation evidence.
const notificationSelect = `SELECT n.id,n.type,n.severity,n.state,n.subject_type,n.subject_id,n.job_id,n.source_id,
	n.title,n.message,n.created_at,n.updated_at,n.resolved_at,n.remind_at,
	COALESCE(j.kind,''),COALESCE(j.status,''),COALESCE(p.workouts_created,0),COALESCE(p.workouts_updated,0),
	CASE WHEN j.status='failed' AND (p.workouts_created>0 OR p.workouts_updated>0) THEN
		(SELECT COALESCE(bool_and(child.status='failed'
			AND child.kind IN ('manual_ingest_source','scheduled_ingest_source')
			AND child.failure_code IS NOT DISTINCT FROM 'source-file-invalid' AND EXISTS (
				SELECT 1 FROM app.source_files file WHERE file.job_id=child.id
				AND file.account_id=child.account_id AND file.state='failed')),false)
		FROM app.jobs child WHERE child.parent_job_id=j.id AND child.account_id=j.account_id
		AND child.status IN ('failed','cancelled'))
	ELSE false END
	FROM selected_notifications n LEFT JOIN app.jobs j ON j.id=n.job_id AND j.account_id=n.account_id
		AND n.type='ingest-terminal' AND j.parent_job_id IS NULL AND j.kind IN ('manual_ingest','scheduled_ingest')
	LEFT JOIN app.job_progress p ON p.job_id=j.id AND p.account_id=j.account_id`

func (s *Server) ListNotifications(w http.ResponseWriter, r *http.Request, params generated.ListNotificationsParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	page, pageSize := ownerPage(params.Page, params.PageSize)
	state := ""
	if params.State != nil {
		state = string(*params.State)
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeNotificationUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM app.notifications
		WHERE ($1='' OR state=$1) AND (state<>'remind' OR remind_at<=clock_timestamp())`, state).Scan(&total); err != nil {
		writeNotificationUnavailable(w, r)
		return
	}
	items, err := readNotifications(r.Context(), tx, state, pageSize, (page-1)*pageSize)
	if err != nil {
		writeNotificationUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, generated.NotificationList{Items: items, Pagination: pagination(page, pageSize, total)})
}

func (s *Server) CreateNotificationDismissal(w http.ResponseWriter, r *http.Request, notificationID generated.NotificationID, params generated.CreateNotificationDismissalParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.EmptyCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	id, valid := parseCompactUUID(notificationID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "notification ID is invalid")
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeNotificationUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var changedID uuid.UUID
	var state string
	err = tx.QueryRow(r.Context(), `SELECT notification_id,new_state FROM app.dismiss_owned_notification($1,$2)`, id, session.principalID).Scan(&changedID, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "notification was not found or is no longer active")
		return
	}
	if err != nil {
		writeNotificationUnavailable(w, r)
		return
	}
	items, err := readNotificationByID(r.Context(), tx, changedID)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeNotificationUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func readNotifications(ctx context.Context, tx pgx.Tx, state string, limit, offset int) ([]generated.Notification, error) {
	active := state == "active"
	rows, err := tx.Query(ctx, `WITH selected_notifications AS MATERIALIZED (
		SELECT id,account_id,type,severity,state,subject_type,subject_id,job_id,source_id,title,message,created_at,updated_at,resolved_at,remind_at
		FROM app.notifications WHERE ($1='' OR state=$1 OR ($2 AND state IN ('unresolved','remind')))
		AND (state<>'remind' OR remind_at<=clock_timestamp())
		ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4)
		`+notificationSelect+` ORDER BY n.created_at DESC,n.id DESC`, state, active, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]generated.Notification, 0)
	for rows.Next() {
		item, scanErr := scanNotification(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func readNotificationByID(ctx context.Context, tx pgx.Tx, id uuid.UUID) (generated.Notification, error) {
	return scanNotification(tx.QueryRow(ctx, `WITH selected_notifications AS MATERIALIZED (
		SELECT id,account_id,type,severity,state,subject_type,subject_id,job_id,source_id,title,message,created_at,updated_at,resolved_at,remind_at
		FROM app.notifications WHERE id=$1) `+notificationSelect, id))
}

func scanNotification(row interface{ Scan(...any) error }) (generated.Notification, error) {
	var item generated.Notification
	var id uuid.UUID
	var subjectID, jobID, sourceID *uuid.UUID
	var kind, status string
	var progress generated.JobProgress
	var fileOnlyFailure bool
	err := row.Scan(&id, &item.Type, &item.Severity, &item.State, &item.SubjectType, &subjectID, &jobID, &sourceID,
		&item.Title, &item.Message, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt, &item.RemindAt,
		&kind, &status, &progress.WorkoutsCreated, &progress.WorkoutsUpdated, &fileOnlyFailure)
	if err != nil {
		return generated.Notification{}, err
	}
	item.Id = compactUUID(id)
	if subjectID != nil {
		value := compactUUID(*subjectID)
		item.SubjectId = &value
	}
	if jobID != nil {
		value := compactUUID(*jobID)
		item.JobId = &value
	}
	if sourceID != nil {
		value := compactUUID(*sourceID)
		item.SourceId = &value
	}
	if display := ingestPresentationStatus(kind, status, progress); item.Type == "ingest-terminal" && display != status {
		switch display {
		case "partially_succeeded":
			item.Severity, item.Title = generated.NotificationSeverityWarning, "Data sync partially completed"
			item.Message = "Data sync partially completed. One or more sources could not finish. Successfully imported data has been kept. You can retry this sync."
			if fileOnlyFailure {
				item.Message = "Data sync partially completed. Some source files could not be processed. Successfully imported data has been kept. You can retry this sync."
			}
		case "failed":
			item.Severity, item.Title = generated.NotificationSeverityError, "Data sync failed"
			item.Message = "Data sync could not be completed. Check the source details, then retry this sync."
		}
	}
	return item, nil
}

func writeNotificationUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "notification service is temporarily unavailable")
}
