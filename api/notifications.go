package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

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
	rows, err := tx.Query(ctx, `SELECT id,type,severity,state,subject_type,subject_id,job_id,source_id,title,message,created_at,updated_at,resolved_at,remind_at
		FROM app.notifications WHERE ($1='' OR state=$1 OR ($2 AND state IN ('unresolved','remind')))
		AND (state<>'remind' OR remind_at<=clock_timestamp())
		ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, state, active, limit, offset)
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
	return scanNotification(tx.QueryRow(ctx, `SELECT id,type,severity,state,subject_type,subject_id,job_id,source_id,title,message,created_at,updated_at,resolved_at,remind_at
		FROM app.notifications WHERE id=$1`, id))
}

func scanNotification(row interface{ Scan(...any) error }) (generated.Notification, error) {
	var item generated.Notification
	var id uuid.UUID
	var subjectID, jobID, sourceID *uuid.UUID
	err := row.Scan(&id, &item.Type, &item.Severity, &item.State, &item.SubjectType, &subjectID, &jobID, &sourceID,
		&item.Title, &item.Message, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt, &item.RemindAt)
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
	return item, nil
}

func writeNotificationUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "notification service is temporarily unavailable")
}
