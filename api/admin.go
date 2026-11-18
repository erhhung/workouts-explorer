package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func (s *Server) ListAdminUsers(w http.ResponseWriter, r *http.Request, params generated.ListAdminUsersParams) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	page, size := 1, 25
	if params.Page != nil {
		page = *params.Page
	}
	if params.PageSize != nil {
		size = *params.PageSize
	}
	maximum := s.config.PageSizeMaximum
	if maximum == 0 {
		maximum = 100
	}
	if page < 1 || size < 1 || size > maximum || page > 1000000 {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "pagination is invalid")
		return
	}
	search := ""
	if params.Search != nil {
		search = strings.TrimSpace(*params.Search)
	}
	tx, err := s.db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	const from = ` FROM app.authentication_principals p JOIN app.users u ON u.principal_id=p.id JOIN app.accounts a ON a.id=u.account_id
		WHERE p.role='user' AND ($1='' OR strpos(lower(p.username),lower($1))>0 OR strpos(lower(p.full_name),lower($1))>0 OR strpos(lower(p.email),lower($1))>0)`
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*)`+from, search).Scan(&total); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT p.id,u.account_id,p.username,p.full_name,p.email,a.state`+from+` ORDER BY p.canonical_username,p.id LIMIT $2 OFFSET $3`, search, size, (page-1)*size)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]generated.AdminUser, 0)
	for rows.Next() {
		var item generated.AdminUser
		var id, account uuid.UUID
		if err = rows.Scan(&id, &account, &item.Username, &item.FullName, &item.Email, &item.State); err != nil {
			writeAdminUnavailable(w, r)
			return
		}
		item.Id, item.AccountId = compactUUID(id), compactUUID(account)
		items = append(items, item)
	}
	if rows.Err() != nil || tx.Commit(r.Context()) != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, generated.AdminUserList{Items: items, Pagination: generated.Pagination{Page: page, PageSize: size, TotalItems: total, TotalPages: (total + int64(size) - 1) / int64(size)}})
}

func (s *Server) GetAdminConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	minimum, maximum, lifetime := s.config.PasswordMinimum, s.config.PageSizeMaximum, s.config.SessionLifetime
	if minimum == 0 {
		minimum = 12
	}
	if maximum == 0 {
		maximum = 100
	}
	if lifetime == 0 {
		lifetime = 6 * time.Hour
	}
	maps := s.config.BaseMaps
	if len(maps.StyleFamilies) == 0 {
		maps = config.DefaultBaseMaps()
	}
	labels := make([]string, 0, len(maps.StyleFamilies))
	for _, family := range maps.StyleFamilies {
		labels = append(labels, family.Label)
	}
	writeJSON(w, http.StatusOK, generated.AdminConfig{PasswordMinimumLength: minimum, PageSizeMaximum: maximum, SessionLifetimeSeconds: int64(lifetime / time.Second), PollingIntervalSeconds: s.config.PollingIntervalSeconds, MapFamilies: labels})
}

func (s *Server) SetAdminUserAccess(w http.ResponseWriter, r *http.Request, userID generated.CompactUUID, params generated.SetAdminUserAccessParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(userID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "user ID is invalid")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeFieldError(w, r, "enabled", "required", "enabled is required")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var state string
	if _, err = tx.Exec(r.Context(), `SET LOCAL lock_timeout='3s'`); err == nil {
		err = tx.QueryRow(r.Context(), `SELECT app.set_account_access($1,$2,$3,$4,$5)`, session.sessionID, id, *input.Enabled, uuid.Must(uuid.NewV7()), requestIDFrom(r.Context())).Scan(&state)
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if state == "not_found" {
		writeProblem(w, r, 404, "Not Found", "user not found")
		return
	}
	if state == "conflict" {
		writeProblem(w, r, 409, "Conflict", "account deletion prevents changing access")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, generated.AdminUserAccess{Enabled: state == "active"})
}

func (s *Server) RevokeAdminUserSessions(w http.ResponseWriter, r *http.Request, userID generated.CompactUUID, params generated.RevokeAdminUserSessionsParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(userID)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "user ID is invalid")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `SET LOCAL lock_timeout='3s'`)
	var authorized bool
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT EXISTS (
			SELECT 1 FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id
			WHERE s.id=$1 AND p.role='administrator' AND p.disabled_at IS NULL
			AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() FOR SHARE OF s,p
		)`, session.sessionID).Scan(&authorized)
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if !authorized {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "administrator session is no longer active")
		return
	}
	// Session issuance holds this principal lock through commit. Do not lock the
	// workout account: revocation must not wait for an ingest file transaction.
	var target uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT id FROM app.authentication_principals WHERE id=$1 AND role='user' FOR UPDATE`, id).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "user not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	updated, err := tx.Exec(r.Context(), `UPDATE app.sessions SET revoked_at=clock_timestamp()
		WHERE principal_id=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp()`, target)
	if err == nil && updated.RowsAffected() > 0 {
		// Keep this identity-only audit free of account FK locks and token values.
		_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id)
			VALUES($1,$2,'user.sessions_revoked','principal',$3,$4)`, uuid.Must(uuid.NewV7()), session.principalID, target, requestIDFrom(r.Context()))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) DeleteAdminUser(w http.ResponseWriter, r *http.Request, userID generated.CompactUUID, params generated.DeleteAdminUserParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(userID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "user ID is invalid")
		return
	}
	// These are identity projections already available to administrators. Never
	// load a source config, workout, job diagnostic or user password for deletion.
	var username, email string
	err = s.db.QueryRow(r.Context(), `SELECT canonical_username,canonical_email FROM app.authentication_principals WHERE id=$1 AND role='user'`, id).Scan(&username, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "user not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var digests [][]byte
	for _, operation := range []string{"signin", "password_reset_request"} {
		for _, subject := range []string{username, email} {
			digests = append(digests, rateDigest(s.config.RateLimitKey, operation, "subject", []byte(subject)))
		}
	}
	rows, err := s.db.Query(r.Context(), `SELECT 'registration',t.verifier FROM app.invitation_tokens t JOIN app.invitations i ON i.id=t.invitation_id WHERE i.canonical_email=$1 UNION ALL SELECT 'password_reset',verifier FROM app.password_resets WHERE principal_id=$2`, email, id)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	for rows.Next() {
		var operation string
		var verifier []byte
		if err = rows.Scan(&operation, &verifier); err != nil {
			break
		}
		digests = append(digests, rateDigest(s.config.RateLimitKey, operation, "subject", verifier))
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		writeAdminUnavailable(w, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var job *uuid.UUID
	_, err = tx.Exec(r.Context(), `SET LOCAL lock_timeout='3s'`)
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT app.start_account_deletion($1,$2,$3,$4,$5,$6)`, session.sessionID, id, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), requestIDFrom(r.Context()), digests).Scan(&job)
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			// This transaction has not committed. The idempotent command can be
			// retried after a running ingest releases its account/source locks.
			slog.InfoContext(r.Context(), "account deletion waiting for active transaction", "request_id", requestIDFrom(r.Context()), "sqlstate", pgErr.Code)
			detail := "account is busy finishing a data update; retry deletion shortly"
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/problem+json")
			writeJSON(w, http.StatusServiceUnavailable, generated.Problem{
				Type: "https://workouts-explorer.invalid/problems/account-deletion-busy", Title: "Account Busy",
				Status: http.StatusServiceUnavailable, Detail: &detail, RequestId: requestIDFrom(r.Context()),
			})
			return
		}
		writeAdminUnavailable(w, r)
		return
	}
	if job == nil {
		writeProblem(w, r, 404, "Not Found", "user not found")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	result, err := s.adminDeletionProjection(r, session.sessionID, id)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) GetAdminAccountDeletion(w http.ResponseWriter, r *http.Request, userID generated.CompactUUID) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(userID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "user ID is invalid")
		return
	}
	result, err := s.adminDeletionProjection(r, session.sessionID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "deletion not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminDeletionProjection(r *http.Request, session, target uuid.UUID) (generated.AdminAccountDeletion, error) {
	var result generated.AdminAccountDeletion
	var job uuid.UUID
	err := s.db.QueryRow(r.Context(), `SELECT job_id,phase,rows_purged,workouts_total,routes_total,sources_total,last_error_code,completed_at FROM app.admin_account_deletion($1,$2)`, session, target).Scan(&job, &result.Phase, &result.RowsPurged, &result.WorkoutsTotal, &result.RoutesTotal, &result.SourcesTotal, &result.LastErrorCode, &result.CompletedAt)
	if err == nil {
		result.JobId = compactUUID(job)
	}
	return result, err
}

func (s *Server) GetAdminDirectory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// A single statement gives identities and invitation acceptance one consistent snapshot.
	rows, err := s.db.Query(r.Context(), `WITH latest_invitations AS (
		SELECT DISTINCT ON (i.canonical_email) i.id,i.email,i.canonical_email,
			CASE WHEN i.state='revoked' THEN 'revoked'
				WHEN t.revoked_at IS NOT NULL THEN 'revoked'
				WHEN t.expires_at>transaction_timestamp() THEN 'invited' ELSE 'expired' END AS state
		FROM app.invitations i
		LEFT JOIN LATERAL (
			SELECT expires_at,revoked_at FROM app.invitation_tokens
			WHERE invitation_id=i.id ORDER BY issued_at DESC,expires_at DESC LIMIT 1
		) t ON true
		WHERE i.state='pending' AND NOT EXISTS (
			SELECT 1 FROM app.authentication_principals p WHERE p.canonical_email=i.canonical_email
		)
		ORDER BY i.canonical_email,(i.state='pending') DESC,i.created_at DESC,i.id DESC
	), directory AS (
		SELECT p.id,'user' AS kind,p.username,p.full_name,p.email,p.created_at AS registered_at,
			CASE WHEN a.state<>'active' THEN a.state WHEN p.disabled_at IS NOT NULL THEN 'disabled' ELSE 'active' END AS state,
			(SELECT max(s.expires_at) FROM app.sessions s WHERE s.principal_id=p.id
			 AND p.disabled_at IS NULL AND a.state='active'
			 AND s.revoked_at IS NULL AND s.expires_at>statement_timestamp()) AS active_session_expires_at
		FROM app.authentication_principals p JOIN app.users u ON u.principal_id=p.id JOIN app.accounts a ON a.id=u.account_id
		WHERE p.role='user'
		UNION ALL
		SELECT id,'invitation',NULL,NULL,email,NULL,state,NULL::timestamptz FROM latest_invitations
	)
	SELECT id,kind,username,full_name,email,registered_at,state,active_session_expires_at FROM directory ORDER BY lower(email),kind,id LIMIT 1001`)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]generated.AdminDirectoryEntry, 0)
	for rows.Next() {
		var item generated.AdminDirectoryEntry
		var id uuid.UUID
		var username, fullName *string
		var registered, activeSessionExpires *time.Time
		if err := rows.Scan(&id, &item.Kind, &username, &fullName, &item.Email, &registered, &item.State, &activeSessionExpires); err != nil {
			writeAdminUnavailable(w, r)
			return
		}
		item.Id = compactUUID(id)
		setNullableString(&item.Username, username)
		setNullableString(&item.FullName, fullName)
		item.RegisteredAt.SetNull()
		if registered != nil {
			item.RegisteredAt.Set(*registered)
		}
		item.ActiveSessionExpiresAt.SetNull()
		if activeSessionExpires != nil {
			item.ActiveSessionExpiresAt.Set(*activeSessionExpires)
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if len(items) > 1000 {
		writeProblem(w, r, http.StatusUnprocessableEntity, "Unprocessable Content", "directory exceeds the supported unpaged size")
		return
	}
	writeJSON(w, http.StatusOK, generated.AdminDirectory{Items: items})
}

func (s *Server) GetAdminUserUsage(w http.ResponseWriter, r *http.Request, userID generated.CompactUUID, params generated.GetAdminUserUsageParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(userID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "user ID is invalid")
		return
	}
	if _, err := time.LoadLocation(params.Timezone); err != nil || params.Timezone == "" || len(params.Timezone) > 255 {
		writeProblem(w, r, 400, "Bad Request", "time zone is invalid")
		return
	}
	var result generated.AdminUserUsage
	var lastLogin *time.Time
	var lastWorkout *time.Time
	err = s.db.QueryRow(r.Context(), `SELECT full_name,last_login_at,workout_count,route_count,last_workout_date FROM app.admin_account_usage($1,$2,$3)`, session.sessionID, id, params.Timezone).Scan(&result.FullName, &lastLogin, &result.WorkoutCount, &result.RouteCount, &lastWorkout)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "user not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	result.LastLoginAt.SetNull()
	if lastLogin != nil {
		result.LastLoginAt.Set(*lastLogin)
	}
	result.LastWorkoutDate.SetNull()
	if lastWorkout != nil {
		result.LastWorkoutDate.Set(openapi_types.Date{Time: *lastWorkout})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) ResendAdminInvitation(w http.ResponseWriter, r *http.Request, invitationID generated.CompactUUID, params generated.ResendAdminInvitationParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, err := uuid.Parse(invitationID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "invitation ID is invalid")
		return
	}
	token, verifier, err := randomToken()
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var email, state string
	err = tx.QueryRow(r.Context(), `SELECT email,state FROM app.invitations WHERE id=$1 FOR UPDATE`, id).Scan(&email, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "invitation not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if state != "pending" {
		writeProblem(w, r, 409, "Conflict", "invitation is no longer pending")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE app.invitation_tokens SET revoked_at=transaction_timestamp(),replaced_at=transaction_timestamp() WHERE invitation_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, id)
	var issuedAt, expiresAt time.Time
	if err == nil {
		err = tx.QueryRow(r.Context(), `INSERT INTO app.invitation_tokens(invitation_id,verifier) VALUES($1,$2) RETURNING issued_at,expires_at`, id, verifier).Scan(&issuedAt, &expiresAt)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id) VALUES($1,$2,'invitation.resent','invitation',$3,$4)`, uuid.Must(uuid.NewV7()), session.principalID, id, requestIDFrom(r.Context()))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	s.sendInvitation(w, r, id, email, token, verifier, issuedAt, expiresAt, http.StatusOK)
}

func (s *Server) RevokeAdminInvitation(w http.ResponseWriter, r *http.Request, invitationID generated.CompactUUID, params generated.RevokeAdminInvitationParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, err := uuid.Parse(invitationID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "invitation ID is invalid")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var state string
	err = tx.QueryRow(r.Context(), `SELECT state FROM app.invitations WHERE id=$1 FOR UPDATE`, id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "invitation not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if state == "accepted" {
		writeProblem(w, r, 409, "Conflict", "invitation has already been accepted")
		return
	}
	if state == "pending" {
		_, err = tx.Exec(r.Context(), `UPDATE app.invitations SET state='revoked',revoked_at=transaction_timestamp() WHERE id=$1`, id)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE app.invitation_tokens SET revoked_at=transaction_timestamp() WHERE invitation_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, id)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id) VALUES($1,$2,'invitation.revoked','invitation',$3,$4)`, uuid.Must(uuid.NewV7()), session.principalID, id, requestIDFrom(r.Context()))
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeAdminUnavailable(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "administrative data is temporarily unavailable")
}
