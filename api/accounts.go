package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const sessionCookieName = "workouts_session"

type authenticatedSession struct {
	sessionID      uuid.UUID
	principalID    uuid.UUID
	accountID      *uuid.UUID
	role           string
	username       string
	email          string
	canonicalEmail string
	fullName       string
	expiresAt      time.Time
	csrfToken      *string
	kind           string
}

func (s *Server) CreateBrowserSession(w http.ResponseWriter, r *http.Request) {
	if !s.validBrowserSigninOrigin(r) {
		writeProblem(w, r, http.StatusForbidden, "Forbidden", "browser sign-in origin is not allowed")
		return
	}
	s.createSession(w, r, "cookie")
}

func (s *Server) CreateSessionToken(w http.ResponseWriter, r *http.Request) {
	s.createSession(w, r, "bearer")
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, kind string) {
	var input generated.SigninRequest
	if !decodeJSON(w, r, &input) || input.Password == nil {
		return
	}
	canonical, valid := canonicalSignin(input.Username)
	if !valid {
		canonical = "invalid"
	}
	allowed, retry, err := s.consumeLimits(r.Context(), r, "signin", canonical)
	if err != nil {
		var addressError *clientAddressError
		if errors.As(err, &addressError) {
			writeProblem(w, r, http.StatusBadRequest, "Bad Request", "forwarded client address metadata is invalid")
			return
		}
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprint(retry))
		writeProblem(w, r, http.StatusTooManyRequests, "Too Many Requests", "too many authentication attempts")
		return
	}
	if !valid {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "invalid username or password")
		return
	}
	principal, passwordHash, err := s.findPrincipalForSignin(r.Context(), canonical, strings.Contains(input.Username, "@"))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// A fixed valid profile keeps nonexistent-identity work in the Argon2 path.
		passwordHash = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$Jg8CDQ8C8w7HqX1fV62H4h2H5QzKxU0fXwP8dF4G9tQ"
	}
	verified, rehash, verifyErr := s.passwords.verify(r.Context(), *input.Password, passwordHash)
	if verifyErr != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
		return
	}
	if !verified || principal.principalID == uuid.Nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "invalid username or password")
		return
	}
	credential, verifier, err := randomToken()
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
		return
	}
	var csrf *string
	if kind == "cookie" {
		value, _, tokenErr := randomToken()
		if tokenErr != nil {
			writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
			return
		}
		csrf = &value
	}
	sessionID := uuid.Must(uuid.NewV7())
	lifetime := s.config.SessionLifetime
	if lifetime == 0 {
		lifetime = 2 * time.Hour
	}
	expiresAt := time.Now().UTC().Add(lifetime)
	tx, err := s.db.Begin(r.Context())
	if err == nil {
		defer tx.Rollback(r.Context())
		if rehash {
			var replacement string
			replacement, err = s.passwords.hash(r.Context(), *input.Password)
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE app.authentication_principals SET password_hash=$1, updated_at=transaction_timestamp() WHERE id=$2`, replacement, principal.principalID)
			}
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, sessionID, principal.principalID, kind, verifier, csrf, expiresAt)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
		return
	}
	metadata := sessionMetadata(sessionID, principal, expiresAt)
	if kind == "cookie" {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: credential, Path: "/", HttpOnly: true, Secure: s.secureCookie(), SameSite: http.SameSiteLaxMode, Expires: expiresAt})
		writeJSON(w, http.StatusCreated, generated.BrowserSession{Id: metadata.Id, Identity: metadata.Identity, ExpiresAt: expiresAt, CsrfToken: *csrf})
		return
	}
	writeJSON(w, http.StatusCreated, generated.TokenSession{Id: metadata.Id, Identity: metadata.Identity, ExpiresAt: expiresAt, AccessToken: credential, TokenType: generated.Bearer})
}

func (s *Server) GetCurrentSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "")
	if !ok {
		return
	}
	metadata := sessionMetadata(session.sessionID, session, session.expiresAt)
	if session.kind == "cookie" {
		writeJSON(w, http.StatusOK, generated.BrowserSession{Id: metadata.Id, Identity: metadata.Identity, ExpiresAt: metadata.ExpiresAt, CsrfToken: *session.csrfToken})
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func (s *Server) DeleteCurrentSession(w http.ResponseWriter, r *http.Request, params generated.DeleteCurrentSessionParams) {
	session, ok := s.requireSession(w, r, "")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE app.sessions SET revoked_at=transaction_timestamp() WHERE id=$1 AND revoked_at IS NULL`, session.sessionID); err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "session could not be revoked")
		return
	}
	if session.kind == "cookie" {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", HttpOnly: true, Secure: s.secureCookie(), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) CreateInvitation(w http.ResponseWriter, r *http.Request, params generated.CreateInvitationParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.InvitationCreate
	if !decodeJSON(w, r, &input) {
		return
	}
	email, canonical, err := canonicalEmail(input.Email)
	if err != nil {
		writeFieldError(w, r, "email", "invalid", "email address is invalid")
		return
	}
	token, verifier, err := randomToken()
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "invitation could not be created")
		return
	}
	id := uuid.Must(uuid.NewV7())
	var issuedAt, expiresAt time.Time
	tx, err := s.db.Begin(r.Context())
	if err == nil {
		defer tx.Rollback(r.Context())
		var exists bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM app.authentication_principals WHERE canonical_email=$1)`, canonical).Scan(&exists)
		if err == nil && exists {
			writeFieldConflict(w, r, "email")
			return
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.invitations(id,email,canonical_email,created_by) VALUES($1,$2,$3,$4)`, id, email, canonical, session.principalID)
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO app.invitation_tokens(invitation_id,verifier) VALUES($1,$2) RETURNING issued_at,expires_at`, id, verifier).Scan(&issuedAt, &expiresAt)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id) VALUES($1,$2,'invitation.created','invitation',$3,$4)`, uuid.Must(uuid.NewV7()), session.principalID, id, requestIDFrom(r.Context()))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeFieldConflict(w, r, "email")
		} else {
			writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "invitation could not be created")
		}
		return
	}
	result := make(chan string, 1)
	work := deliveryWork{to: email, subject: "Your Workouts Explorer invitation", body: "Register at " + strings.TrimRight(s.config.PublicURL, "/") + "/register?token=" + url.QueryEscape(token), result: result,
		done: func(category string) bool { return s.recordDelivery("invitation_tokens", verifier, category) }}
	if !s.delivery.enqueue(work) {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "invitation remains retryable because email delivery is unavailable")
		return
	}
	select {
	case category := <-result:
		if category != "" {
			writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "invitation remains retryable because email delivery is unavailable")
			return
		}
	case <-r.Context().Done():
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "invitation remains retryable because email delivery is unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, generated.Invitation{Id: compactUUID(id), Email: email, State: generated.InvitationState("pending"), DeliveryState: generated.InvitationDeliveryState("delivered"), IssuedAt: issuedAt, ExpiresAt: expiresAt})
}

func (s *Server) GetInvitation(w http.ResponseWriter, r *http.Request, token generated.Token) {
	verifier, valid := tokenVerifier(token)
	if !valid {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "invitation is unavailable")
		return
	}
	var invitation generated.PublicInvitation
	err := s.db.QueryRow(r.Context(), `SELECT i.email,t.expires_at FROM app.invitation_tokens t JOIN app.invitations i ON i.id=t.invitation_id WHERE t.verifier=$1 AND i.state='pending' AND t.used_at IS NULL AND t.revoked_at IS NULL AND transaction_timestamp()<t.expires_at`, verifier).Scan(&invitation.Email, &invitation.ExpiresAt)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "invitation is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, invitation)
}

func (s *Server) CreateRegistration(w http.ResponseWriter, r *http.Request) {
	var input generated.RegistrationCreate
	if !decodeJSON(w, r, &input) || input.Password == nil || input.PasswordConfirmation == nil {
		return
	}
	verifier, validToken := tokenVerifier(input.Token)
	if !validToken {
		writeFieldError(w, r, "token", "invalid", "invitation is invalid or expired")
		return
	}
	allowed, retry, err := s.consumeLimits(r.Context(), r, "registration", string(verifier))
	if err != nil || !allowed {
		writeLimitResult(w, r, err, retry)
		return
	}
	username, canonicalUsernameValue, err := canonicalUsername(input.Username)
	if err != nil {
		writeFieldError(w, r, "username", "invalid", "username is invalid")
		return
	}
	fullName := trimUnicode15Whitespace(input.FullName)
	if fullName == "" || !utf8.ValidString(fullName) || utf8.RuneCountInString(fullName) > 200 {
		writeFieldError(w, r, "fullName", "invalid", "full name is invalid")
		return
	}
	if *input.Password != *input.PasswordConfirmation {
		writeFieldError(w, r, "passwordConfirmation", "mismatch", "password confirmation does not match")
		return
	}
	if err := s.passwords.validate(*input.Password); err != nil {
		writeFieldError(w, r, "password", "policy", "password does not meet policy")
		return
	}
	passwordHash, err := s.passwords.hash(r.Context(), *input.Password)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "password service is temporarily unavailable")
		return
	}
	principalID, accountID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var email, canonicalEmailValue string
	tx, err := s.db.Begin(r.Context())
	if err == nil {
		defer tx.Rollback(r.Context())
		var invitationID uuid.UUID
		err = tx.QueryRow(r.Context(), `SELECT i.id,i.email,i.canonical_email FROM app.invitation_tokens t JOIN app.invitations i ON i.id=t.invitation_id WHERE t.verifier=$1 AND i.state='pending' AND t.used_at IS NULL AND t.revoked_at IS NULL AND transaction_timestamp()<t.expires_at FOR UPDATE OF i,t`, verifier).Scan(&invitationID, &email, &canonicalEmailValue)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'user',$2,$3,$4,$5,1,$6,$7)`, principalID, username, canonicalUsernameValue, email, canonicalEmailValue, passwordHash, fullName)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.accounts(id) VALUES($1)`, accountID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principalID, accountID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `SELECT set_config('app.account_id',$1,true)`, accountID.String())
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.preferences(account_id) VALUES($1)`, accountID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE app.invitation_tokens SET used_at=transaction_timestamp() WHERE verifier=$1`, verifier)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE app.invitations SET state='accepted',accepted_by=$1,accepted_at=transaction_timestamp() WHERE id=$2`, principalID, invitationID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,account_id,action,target_type,target_id,request_id) VALUES($1,$2,'registration.completed','account',$2,$3)`, uuid.Must(uuid.NewV7()), accountID, requestIDFrom(r.Context()))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeFieldError(w, r, "token", "invalid", "invitation is invalid or expired")
		} else if field := uniqueField(err); field != "" {
			writeFieldConflict(w, r, field)
		} else {
			writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "registration could not be completed")
		}
		return
	}
	writeJSON(w, http.StatusCreated, profile(principalID, username, email, fullName))
}

func (s *Server) CreatePasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	var input generated.PasswordResetRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	canonical, valid := canonicalSignin(input.Username)
	if !valid {
		canonical = "invalid"
	}
	allowed, retry, err := s.consumeLimits(r.Context(), r, "password_reset_request", canonical)
	if err != nil || !allowed {
		writeLimitResult(w, r, err, retry)
		return
	}
	token, verifier, err := randomToken()
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "recovery service is temporarily unavailable")
		return
	}
	var principalID uuid.UUID
	var email string
	lookupErr := s.db.QueryRow(r.Context(), `SELECT result_principal_id,result_email FROM app.issue_password_reset($1,$2,$3)`, canonical, strings.Contains(input.Username, "@"), verifier).Scan(&principalID, &email)
	if lookupErr == nil {
		work := deliveryWork{to: email, subject: "Reset your Workouts Explorer password", body: "Reset at " + strings.TrimRight(s.config.PublicURL, "/") + "/reset-password?token=" + url.QueryEscape(token), done: func(category string) bool { return s.recordDelivery("password_resets", verifier, category) }}
		_ = s.delivery.enqueue(work)
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "recovery service is temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusAccepted, generated.Accepted{Status: generated.AcceptedStatus("accepted")})
}

func (s *Server) CreatePasswordReset(w http.ResponseWriter, r *http.Request) {
	var input generated.PasswordResetCreate
	if !decodeJSON(w, r, &input) || input.Password == nil || input.PasswordConfirmation == nil {
		return
	}
	verifier, valid := tokenVerifier(input.Token)
	if !valid {
		writeFieldError(w, r, "token", "invalid", "reset token is invalid or expired")
		return
	}
	allowed, retry, err := s.consumeLimits(r.Context(), r, "password_reset", string(verifier))
	if err != nil || !allowed {
		writeLimitResult(w, r, err, retry)
		return
	}
	if *input.Password != *input.PasswordConfirmation {
		writeFieldError(w, r, "passwordConfirmation", "mismatch", "password confirmation does not match")
		return
	}
	if err := s.passwords.validate(*input.Password); err != nil {
		writeFieldError(w, r, "password", "policy", "password does not meet policy")
		return
	}
	hash, err := s.passwords.hash(r.Context(), *input.Password)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "password service is temporarily unavailable")
		return
	}
	var completed bool
	err = s.db.QueryRow(r.Context(), `SELECT app.complete_password_reset($1,$2,$3,$4)`, verifier, hash, uuid.Must(uuid.NewV7()), requestIDFrom(r.Context())).Scan(&completed)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "password could not be reset")
		return
	}
	if !completed {
		writeFieldError(w, r, "token", "invalid", "reset token is invalid or expired")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, profile(session.principalID, session.username, session.email, session.fullName))
}

func (s *Server) UpdateMe(w http.ResponseWriter, r *http.Request, params generated.UpdateMeParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var input generated.ProfilePatch
	if !decodeJSON(w, r, &input) || input.FullName == nil {
		return
	}
	fullName := trimUnicode15Whitespace(*input.FullName)
	if fullName == "" || !utf8.ValidString(fullName) || utf8.RuneCountInString(fullName) > 200 {
		writeFieldError(w, r, "fullName", "invalid", "full name is invalid")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err == nil {
		defer tx.Rollback(r.Context())
		_, err = tx.Exec(r.Context(), `UPDATE app.authentication_principals SET full_name=$1,updated_at=transaction_timestamp() WHERE id=$2`, fullName, session.principalID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,account_id,action,target_type,target_id,request_id) VALUES($1,$2,$3,'profile.updated','principal',$2,$4)`, uuid.Must(uuid.NewV7()), session.principalID, *session.accountID, requestIDFrom(r.Context()))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "profile could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, profile(session.principalID, session.username, session.email, fullName))
}

func (s *Server) GetMyPreferences(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	preferences, err := s.readPreferences(r.Context(), *session.accountID)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "preferences are unavailable")
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

func (s *Server) UpdateMyPreferences(w http.ResponseWriter, r *http.Request, params generated.UpdateMyPreferencesParams) {
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	var patch generated.PreferencesPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "preferences are unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	current, err := readPreferencesTx(r.Context(), tx, *session.accountID, true)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "preferences are unavailable")
		return
	}
	applyPreferencesPatch(&current, patch)
	if field, message := s.validatePreferences(current); field != "" {
		writeFieldError(w, r, field, "invalid", message)
		return
	}
	columns := make([]string, len(current.WorkoutColumns))
	for index := range current.WorkoutColumns {
		columns[index] = string(current.WorkoutColumns[index])
	}
	_, err = tx.Exec(r.Context(), `UPDATE app.preferences SET theme=$1,units=$2,timezone=$3,first_weekday=$4,clock_format=$5,workout_columns=$6,page_size=$7,date_range=$8,updated_at=transaction_timestamp() WHERE account_id=$9`, current.Theme, current.Units, current.Timezone, current.FirstWeekday, current.ClockFormat, columns, current.PageSize, nullableStringValue(current.DateRange), *session.accountID)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "preferences could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) GetMyAvatar(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "user")
	if !ok {
		return
	}
	entry := s.avatars.get(r.Context(), session.canonicalEmail, session.fullName)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("ETag", entry.etag)
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Header.Get("If-None-Match") == entry.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.body)
}

func (s *Server) authenticate(ctx context.Context, r *http.Request) (authenticatedSession, error) {
	credential := ""
	kind := ""
	if values, present := r.Header["Authorization"]; present {
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.Contains(strings.TrimPrefix(values[0], "Bearer "), " ") {
			return authenticatedSession{}, errors.New("invalid bearer credential")
		}
		kind, credential = "bearer", strings.TrimPrefix(values[0], "Bearer ")
	} else if cookie, err := r.Cookie(sessionCookieName); err == nil {
		kind, credential = "cookie", cookie.Value
	} else {
		return authenticatedSession{}, errors.New("missing credential")
	}
	verifier, valid := tokenVerifier(credential)
	if !valid {
		return authenticatedSession{}, errors.New("invalid credential")
	}
	var result authenticatedSession
	var accountID *uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT s.id,p.id,u.account_id,p.role,p.username,p.email,p.canonical_email,p.full_name,s.expires_at,s.csrf_token,s.credential_kind FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id LEFT JOIN app.users u ON u.principal_id=p.id LEFT JOIN app.accounts a ON a.id=u.account_id WHERE s.credential_verifier=$1 AND s.credential_kind=$2 AND s.revoked_at IS NULL AND transaction_timestamp()<s.expires_at AND p.disabled_at IS NULL AND (p.role='administrator' OR a.state='active')`, verifier, kind).Scan(&result.sessionID, &result.principalID, &accountID, &result.role, &result.username, &result.email, &result.canonicalEmail, &result.fullName, &result.expiresAt, &result.csrfToken, &result.kind)
	result.accountID = accountID
	return result, err
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request, role string) (authenticatedSession, bool) {
	session, err := s.authenticate(r.Context(), r)
	if err != nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized", "authentication is required")
		return authenticatedSession{}, false
	}
	if role != "" && session.role != role {
		writeProblem(w, r, http.StatusForbidden, "Forbidden", "this identity cannot access the resource")
		return authenticatedSession{}, false
	}
	return session, true
}

func requireCSRF(w http.ResponseWriter, r *http.Request, session authenticatedSession, provided *string) bool {
	if session.kind != "cookie" {
		return true
	}
	if provided == nil || session.csrfToken == nil || len(*provided) != len(*session.csrfToken) || subtle.ConstantTimeCompare([]byte(*provided), []byte(*session.csrfToken)) != 1 {
		writeProblem(w, r, http.StatusForbidden, "Forbidden", "a valid CSRF token is required")
		return false
	}
	return true
}

func (s *Server) findPrincipalForSignin(ctx context.Context, canonical string, email bool) (authenticatedSession, string, error) {
	column := "canonical_username"
	if email {
		column = "canonical_email"
	}
	var principal authenticatedSession
	var hash string
	err := s.db.QueryRow(ctx, `SELECT p.id,u.account_id,p.role,p.username,p.email,p.canonical_email,p.full_name,p.password_hash FROM app.authentication_principals p LEFT JOIN app.users u ON u.principal_id=p.id LEFT JOIN app.accounts a ON a.id=u.account_id WHERE p.`+column+`=$1 AND p.disabled_at IS NULL AND (p.role='administrator' OR a.state='active')`, canonical).Scan(&principal.principalID, &principal.accountID, &principal.role, &principal.username, &principal.email, &principal.canonicalEmail, &principal.fullName, &hash)
	return principal, hash, err
}

func (s *Server) consumeLimits(ctx context.Context, r *http.Request, operation, subject string) (bool, int, error) {
	ip, err := clientIP(r, s.config.TrustedProxyCIDRs)
	if err != nil {
		return false, 0, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback(ctx)
	allowed := true
	retry := 1
	for _, counter := range []struct {
		kind, value string
	}{{"network", networkPrefix(ip)}, {"subject", subject}} {
		digest := rateDigest(s.config.RateLimitKey, operation, counter.kind, []byte(counter.value))
		var thisAllowed bool
		var thisRetry int
		if err = tx.QueryRow(ctx, `SELECT allowed,retry_after_seconds FROM app.consume_rate_limit($1,$2,$3)`, operation, counter.kind, digest).Scan(&thisAllowed, &thisRetry); err != nil {
			return false, 0, err
		}
		allowed = allowed && thisAllowed
		if thisRetry > retry {
			retry = thisRetry
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	return allowed, retry, nil
}

type clientAddressError struct{ reason string }

func (e *clientAddressError) Error() string { return e.reason }

func clientIP(r *http.Request, trusted []*net.IPNet) (net.IP, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return nil, &clientAddressError{reason: "invalid peer address"}
	}
	peerTrusted := false
	for _, network := range trusted {
		if network.Contains(peer) {
			peerTrusted = true
			break
		}
	}
	if !peerTrusted {
		return peer, nil
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return peer, nil
	}
	if len(values) != 1 {
		return nil, &clientAddressError{reason: "multiple forwarding headers"}
	}
	header := values[0]
	if len(header) > 1024 {
		return nil, &clientAddressError{reason: "forwarding header too long"}
	}
	parts := strings.Split(header, ",")
	if header == "" || len(parts) > 20 {
		return nil, &clientAddressError{reason: "invalid forwarding header"}
	}
	addresses := make([]net.IP, 0, len(parts)+1)
	for _, part := range parts {
		if strings.TrimSpace(part) != part {
			part = strings.TrimSpace(part)
		}
		ip := net.ParseIP(part)
		if ip == nil {
			return nil, &clientAddressError{reason: "invalid forwarding address"}
		}
		addresses = append(addresses, ip)
	}
	addresses = append(addresses, peer)
	for index := len(addresses) - 1; index >= 0; index-- {
		isTrusted := false
		for _, network := range trusted {
			if network.Contains(addresses[index]) {
				isTrusted = true
				break
			}
		}
		if !isTrusted {
			return addresses[index], nil
		}
	}
	return addresses[0], nil
}

func (s *Server) accountTransaction(ctx context.Context, accountID uuid.UUID) (pgx.Tx, error) {
	return s.accountTransactionWithOptions(ctx, accountID, pgx.TxOptions{})
}

func (s *Server) accountTransactionWithOptions(ctx context.Context, accountID uuid.UUID, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Server) readPreferences(ctx context.Context, accountID uuid.UUID) (generated.Preferences, error) {
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return generated.Preferences{}, err
	}
	defer tx.Rollback(ctx)
	return readPreferencesTx(ctx, tx, accountID, false)
}

func readPreferencesTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, forUpdate bool) (generated.Preferences, error) {
	var result generated.Preferences
	var columns []string
	var dateRange *string
	query := `SELECT theme,units,timezone,first_weekday,clock_format,workout_columns,page_size,date_range FROM app.preferences WHERE account_id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, accountID).Scan(&result.Theme, &result.Units, &result.Timezone, &result.FirstWeekday, &result.ClockFormat, &columns, &result.PageSize, &dateRange)
	if err != nil {
		return generated.Preferences{}, err
	}
	if dateRange == nil {
		result.DateRange.SetNull()
	} else {
		if !validDateRangePreference(*dateRange) {
			return generated.Preferences{}, errors.New("stored date range preference is invalid")
		}
		result.DateRange.Set(*dateRange)
	}
	for _, column := range columns {
		result.WorkoutColumns = append(result.WorkoutColumns, generated.PreferencesWorkoutColumns(column))
	}
	return result, nil
}

func applyPreferencesPatch(value *generated.Preferences, patch generated.PreferencesPatch) {
	if patch.Theme != nil {
		value.Theme = generated.PreferencesTheme(*patch.Theme)
	}
	if patch.Units != nil {
		value.Units = generated.PreferencesUnits(*patch.Units)
	}
	if patch.Timezone != nil {
		value.Timezone = *patch.Timezone
	}
	if patch.FirstWeekday != nil {
		value.FirstWeekday = generated.PreferencesFirstWeekday(*patch.FirstWeekday)
	}
	if patch.ClockFormat != nil {
		value.ClockFormat = generated.PreferencesClockFormat(*patch.ClockFormat)
	}
	if patch.PageSize != nil {
		value.PageSize = *patch.PageSize
	}
	if patch.DateRange.IsSpecified() {
		value.DateRange = patch.DateRange
	}
	if patch.WorkoutColumns != nil {
		value.WorkoutColumns = make([]generated.PreferencesWorkoutColumns, len(*patch.WorkoutColumns))
		for index := range *patch.WorkoutColumns {
			value.WorkoutColumns[index] = generated.PreferencesWorkoutColumns((*patch.WorkoutColumns)[index])
		}
	}
}

func (s *Server) validatePreferences(value generated.Preferences) (string, string) {
	if value.Theme != "dark" && value.Theme != "light" {
		return "theme", "theme is unsupported"
	}
	if value.Units != "imperial" && value.Units != "metric" {
		return "units", "units are unsupported"
	}
	if value.FirstWeekday != "monday" && value.FirstWeekday != "sunday" {
		return "firstWeekday", "first weekday is unsupported"
	}
	if value.ClockFormat != "12h" && value.ClockFormat != "24h" {
		return "clockFormat", "clock format is unsupported"
	}
	if value.Timezone == "Local" || strings.HasPrefix(value.Timezone, "/") || strings.Contains(value.Timezone, "..") {
		return "timezone", "timezone must be an IANA timezone name"
	}
	if _, err := time.LoadLocation(value.Timezone); err != nil {
		return "timezone", "timezone must be an IANA timezone name"
	}
	maximum := s.config.PageSizeMaximum
	if maximum == 0 {
		maximum = 100
	}
	if value.PageSize < 10 || value.PageSize > maximum {
		return "pageSize", fmt.Sprintf("page size must be between 10 and %d", maximum)
	}
	if value.DateRange.IsSpecified() && !value.DateRange.IsNull() && !validDateRangePreference(value.DateRange.MustGet()) {
		return "dateRange", "date range must be a supported shortcut or an inclusive YYYY-MM-DD/YYYY-MM-DD range"
	}
	allowed := map[string]bool{"date": true, "type": true, "duration": true, "distance": true, "pace": true, "calories": true, "heartRate": true, "elevation": true}
	seen := map[string]bool{}
	for _, column := range value.WorkoutColumns {
		if !allowed[string(column)] || seen[string(column)] {
			return "workoutColumns", "workout columns contain an unsupported or duplicate value"
		}
		seen[string(column)] = true
	}
	return "", ""
}

func validDateRangePreference(value string) bool {
	switch value {
	case "thisWeek", "lastWeek", "last7Days", "last30Days", "thisMonth", "lastMonth", "thisYear", "lastYear":
		return true
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(parts[0]) != 10 || len(parts[1]) != 10 {
		return false
	}
	start, startErr := time.Parse("2006-01-02", parts[0])
	end, endErr := time.Parse("2006-01-02", parts[1])
	return startErr == nil && endErr == nil && !start.After(end)
}

func (s *Server) validBrowserSigninOrigin(r *http.Request) bool {
	if len(r.Header.Values("Sec-Fetch-Site")) > 1 || len(r.Header.Values("Origin")) > 1 {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	public, err := url.Parse(s.config.PublicURL)
	provided, providedErr := url.Parse(origin)
	return err == nil && providedErr == nil && provided.User == nil && provided.Scheme == public.Scheme && strings.EqualFold(provided.Host, public.Host) && provided.Path == "" && provided.RawQuery == "" && provided.Fragment == ""
}

func (s *Server) secureCookie() bool {
	public, err := url.Parse(s.config.PublicURL)
	if err == nil && public.Scheme == "https" {
		return true
	}
	host := ""
	if err == nil {
		host = strings.ToLower(strings.TrimSuffix(public.Hostname(), "."))
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || strings.HasSuffix(host, ".localhost") || ip != nil && ip.IsLoopback()
	return !(s.config.LocalDevelopment && public.Scheme == "http" && loopback)
}

func nullableStringValue(value interface {
	IsNull() bool
	IsSpecified() bool
	MustGet() string
}) any {
	if !value.IsSpecified() || value.IsNull() {
		return nil
	}
	return value.MustGet()
}

func (s *Server) recordDelivery(table string, verifier []byte, category string) bool {
	state := "delivered"
	var categoryValue any
	if category != "" {
		state, categoryValue = "failed", category
	}
	if table != "invitation_tokens" && table != "password_resets" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command, err := s.db.Exec(ctx, `UPDATE app.`+table+` SET delivery_state=$1,delivery_category=$2 WHERE verifier=$3`, state, categoryValue, verifier)
	return err == nil && command.RowsAffected() == 1
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "request body is invalid")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "request body must contain one JSON value")
		return false
	}
	return true
}

func canonicalSignin(value string) (string, bool) {
	if strings.Contains(value, "@") {
		_, canonical, err := canonicalEmail(value)
		return canonical, err == nil
	}
	_, canonical, err := canonicalUsername(value)
	return canonical, err == nil
}

func sessionMetadata(id uuid.UUID, principal authenticatedSession, expires time.Time) generated.SessionMetadata {
	return generated.SessionMetadata{Id: compactUUID(id), ExpiresAt: expires, Identity: generated.IdentitySummary{Id: compactUUID(principal.principalID), Username: principal.username, FullName: principal.fullName, Role: generated.IdentitySummaryRole(principal.role)}}
}

func profile(id uuid.UUID, username, email, fullName string) generated.Profile {
	avatar := generated.ProfileAvatarUrl("/api/me/avatar")
	return generated.Profile{Id: compactUUID(id), Username: &username, Email: &email, FullName: fullName, AvatarUrl: &avatar}
}

func compactUUID(id uuid.UUID) string {
	return strings.ToUpper(strings.ReplaceAll(id.String(), "-", ""))
}

func writeFieldError(w http.ResponseWriter, r *http.Request, field, code, message string) {
	writeValidationProblem(w, r, http.StatusBadRequest, "one or more fields are invalid", generated.ValidationError{Field: field, Code: code, Message: &message})
}

func writeFieldConflict(w http.ResponseWriter, r *http.Request, field string) {
	message := field + " is already in use"
	writeValidationProblem(w, r, http.StatusConflict, "identity conflicts with an existing record", generated.ValidationError{Field: field, Code: "unique", Message: &message})
}

func writeLimitResult(w http.ResponseWriter, r *http.Request, err error, retry int) {
	if err != nil {
		var addressError *clientAddressError
		if errors.As(err, &addressError) {
			writeProblem(w, r, http.StatusBadRequest, "Bad Request", "forwarded client address metadata is invalid")
			return
		}
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "authentication service is temporarily unavailable")
		return
	}
	w.Header().Set("Retry-After", fmt.Sprint(retry))
	writeProblem(w, r, http.StatusTooManyRequests, "Too Many Requests", "too many authentication attempts")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func uniqueField(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return ""
	}
	if strings.Contains(pgErr.ConstraintName, "canonical_username") {
		return "username"
	}
	if strings.Contains(pgErr.ConstraintName, "canonical_email") {
		return "email"
	}
	return "identity"
}
