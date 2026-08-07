package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recordingSender struct {
	mu       sync.Mutex
	messages []string
	err      error
}

func (s *recordingSender) Send(_ context.Context, _, _, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, body)
	return s.err
}

func (s *recordingSender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return ""
	}
	return s.messages[len(s.messages)-1]
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *recordingSender) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}

func TestAccountLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("API_DATABASE_URL")
	migrationURL := os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	db, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminDB, err := pgxpool.New(context.Background(), migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	sender := &recordingSender{}
	server := integrationServer(t, db, sender)
	suffix := strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))[:10]
	adminUsername, adminEmail := "admin"+suffix, "admin"+suffix+"@example.test"
	passwordFile := t.TempDir() + "/password"
	if err := os.WriteFile(passwordFile, []byte("administrator password"), 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrap := BootstrapAdminOptions{Username: adminUsername, Email: adminEmail, PasswordFile: passwordFile, PasswordMin: 12}
	if err := BootstrapAdmin(context.Background(), adminDB, bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapAdmin(context.Background(), adminDB, bootstrap); err != nil {
		t.Fatalf("idempotent bootstrap: %v", err)
	}
	mismatch := bootstrap
	mismatch.Email = "mismatch" + suffix + "@example.test"
	if err := BootstrapAdmin(context.Background(), adminDB, mismatch); err == nil {
		t.Fatal("bootstrap accepted mismatched existing credentials")
	}
	var adminID uuid.UUID
	if err := db.QueryRow(context.Background(), `SELECT id FROM app.authentication_principals WHERE canonical_email=$1`, adminEmail).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	adminToken := insertTestSession(t, db, adminID, "bearer", "")

	email := "owner" + suffix + "@example.test"
	response := callEndpoint(http.MethodPost, "/api/admin/invitations", `{"email":"`+email+`"}`, adminToken, "")
	server.CreateInvitation(response.recorder, response.request, generated.CreateInvitationParams{})
	if response.recorder.Code != http.StatusCreated {
		t.Fatalf("invitation: %d %s", response.recorder.Code, response.recorder.Body.String())
	}
	invitationToken := messageToken(t, sender.last())

	registrationBody := `{"token":"` + invitationToken + `","username":"owner` + suffix + `","fullName":"Owner Name","password":"registration password","passwordConfirmation":"registration password"}`
	registrations := make([]endpointCall, 2)
	var registrationWait sync.WaitGroup
	for index := range registrations {
		registrations[index] = callEndpoint(http.MethodPost, "/api/registrations", registrationBody, "", "")
		registrationWait.Add(1)
		go func(call endpointCall) {
			defer registrationWait.Done()
			server.CreateRegistration(call.recorder, call.request)
		}(registrations[index])
	}
	registrationWait.Wait()
	created := 0
	for _, registration := range registrations {
		if registration.recorder.Code == http.StatusCreated {
			created++
		}
		if registration.recorder.Code != http.StatusCreated && registration.recorder.Code != http.StatusBadRequest {
			t.Fatalf("concurrent registration: %d %s", registration.recorder.Code, registration.recorder.Body.String())
		}
	}
	if created != 1 {
		t.Fatalf("concurrent registration successes=%d", created)
	}
	replay := callEndpoint(http.MethodPost, "/api/registrations", registrationBody, "", "")
	server.CreateRegistration(replay.recorder, replay.request)
	if replay.recorder.Code != http.StatusBadRequest {
		t.Fatalf("registration replay: %d %s", replay.recorder.Code, replay.recorder.Body.String())
	}
	expiredEmail := "expired" + suffix + "@example.test"
	expiredInvite := callEndpoint(http.MethodPost, "/api/admin/invitations", `{"email":"`+expiredEmail+`"}`, adminToken, "")
	server.CreateInvitation(expiredInvite.recorder, expiredInvite.request, generated.CreateInvitationParams{})
	if expiredInvite.recorder.Code != http.StatusCreated {
		t.Fatalf("expiry fixture invitation: %d %s", expiredInvite.recorder.Code, expiredInvite.recorder.Body.String())
	}
	expiredToken := messageToken(t, sender.last())
	expiredVerifier, _ := tokenVerifier(expiredToken)
	if _, err := adminDB.Exec(context.Background(), `UPDATE app.invitation_tokens SET issued_at=issued_at-interval '8 days',expires_at=expires_at-interval '8 days' WHERE verifier=$1`, expiredVerifier); err != nil {
		t.Fatal(err)
	}
	expiredResponse := httptest.NewRecorder()
	server.GetInvitation(expiredResponse, httptest.NewRequest(http.MethodGet, "/api/invitations/"+expiredToken, nil), expiredToken)
	if expiredResponse.Code != http.StatusNotFound {
		t.Fatalf("expired invitation: %d", expiredResponse.Code)
	}

	var userID uuid.UUID
	if err := db.QueryRow(context.Background(), `SELECT id FROM app.authentication_principals WHERE canonical_email=$1`, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	bearerSignin := callEndpoint(http.MethodPost, "/api/session-tokens", `{"username":"`+email+`","password":"registration password"}`, "", "")
	server.CreateSessionToken(bearerSignin.recorder, bearerSignin.request)
	if bearerSignin.recorder.Code != http.StatusCreated {
		t.Fatalf("bearer sign-in: %d %s", bearerSignin.recorder.Code, bearerSignin.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/session-tokens", bearerSignin.recorder)
	var bearerSession generated.TokenSession
	if err := json.Unmarshal(bearerSignin.recorder.Body.Bytes(), &bearerSession); err != nil {
		t.Fatal(err)
	}
	userToken := bearerSession.AccessToken
	adminMe := callEndpoint(http.MethodGet, "/api/me", "", adminToken, "")
	server.GetMe(adminMe.recorder, adminMe.request)
	if adminMe.recorder.Code != http.StatusForbidden {
		t.Fatalf("administrator account access: %d", adminMe.recorder.Code)
	}
	userInvitation := callEndpoint(http.MethodPost, "/api/admin/invitations", `{"email":"other`+suffix+`@example.test"}`, userToken, "")
	server.CreateInvitation(userInvitation.recorder, userInvitation.request, generated.CreateInvitationParams{})
	if userInvitation.recorder.Code != http.StatusForbidden {
		t.Fatalf("user admin access: %d", userInvitation.recorder.Code)
	}

	browserSignin := callEndpoint(http.MethodPost, "/api/session", `{"username":"owner`+suffix+`","password":"registration password"}`, "", "")
	server.CreateBrowserSession(browserSignin.recorder, browserSignin.request)
	if browserSignin.recorder.Code != http.StatusCreated {
		t.Fatalf("browser sign-in: %d %s", browserSignin.recorder.Code, browserSignin.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/session", browserSignin.recorder)
	var browserSession generated.BrowserSession
	if err := json.Unmarshal(browserSignin.recorder.Body.Bytes(), &browserSession); err != nil {
		t.Fatal(err)
	}
	cookies := browserSignin.recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("browser sign-in cookie attributes are invalid")
	}
	cookieToken := cookies[0].Value
	missingCSRF := callCookieEndpoint(http.MethodPatch, "/api/me", `{"fullName":"Changed Name"}`, cookieToken)
	server.UpdateMe(missingCSRF.recorder, missingCSRF.request, generated.UpdateMeParams{})
	if missingCSRF.recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", missingCSRF.recorder.Code)
	}
	csrf := generated.CSRFToken(browserSession.CsrfToken)
	withCSRF := callCookieEndpoint(http.MethodPatch, "/api/me", `{"fullName":"Changed Name"}`, cookieToken)
	server.UpdateMe(withCSRF.recorder, withCSRF.request, generated.UpdateMeParams{XCSRFToken: &csrf})
	if withCSRF.recorder.Code != http.StatusOK {
		t.Fatalf("valid CSRF: %d %s", withCSRF.recorder.Code, withCSRF.recorder.Body.String())
	}
	invalidCSRF := generated.CSRFToken("malformed")
	mixed := callCookieEndpoint(http.MethodPatch, "/api/me", `{"fullName":"Bearer Precedence"}`, cookieToken)
	mixed.request.Header.Set("Authorization", "Bearer "+userToken)
	server.UpdateMe(mixed.recorder, mixed.request, generated.UpdateMeParams{XCSRFToken: &invalidCSRF})
	if mixed.recorder.Code != http.StatusOK {
		t.Fatalf("bearer precedence status=%d", mixed.recorder.Code)
	}
	invalidBearer := callCookieEndpoint(http.MethodPatch, "/api/me", `{"fullName":"Must Not Change"}`, cookieToken)
	invalidBearer.request.Header.Set("Authorization", "Bearer malformed")
	server.UpdateMe(invalidBearer.recorder, invalidBearer.request, generated.UpdateMeParams{XCSRFToken: &csrf})
	if invalidBearer.recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer fallback status=%d", invalidBearer.recorder.Code)
	}
	handlerContext, cancelHandler := context.WithCancel(context.Background())
	defer cancelHandler()
	handler, err := NewHandlerContext(handlerContext, server.config, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	contractCookie := callCookieEndpoint(http.MethodPatch, "/api/me", `{"fullName":"Contract Cookie"}`, cookieToken)
	contractCookie.request.Header.Set("Content-Type", "application/json")
	contractCookie.request.Header.Set("X-CSRF-Token", "malformed")
	handler.ServeHTTP(contractCookie.recorder, contractCookie.request)
	if contractCookie.recorder.Code != http.StatusForbidden {
		t.Fatalf("contract cookie malformed CSRF status=%d", contractCookie.recorder.Code)
	}
	contractBearer := callEndpoint(http.MethodPatch, "/api/me", `{"fullName":"Contract Bearer"}`, userToken, "malformed")
	handler.ServeHTTP(contractBearer.recorder, contractBearer.request)
	if contractBearer.recorder.Code != http.StatusOK {
		t.Fatalf("contract bearer malformed CSRF status=%d body=%s", contractBearer.recorder.Code, contractBearer.recorder.Body.String())
	}

	dateRangeSet := callEndpoint(http.MethodPatch, "/api/me/preferences", `{"dateRange":"last30Days"}`, userToken, "")
	server.UpdateMyPreferences(dateRangeSet.recorder, dateRangeSet.request, generated.UpdateMyPreferencesParams{})
	if dateRangeSet.recorder.Code != http.StatusOK || !strings.Contains(dateRangeSet.recorder.Body.String(), `"dateRange":"last30Days"`) {
		t.Fatalf("date-range set: %d %s", dateRangeSet.recorder.Code, dateRangeSet.recorder.Body.String())
	}
	dateRangeClear := callEndpoint(http.MethodPatch, "/api/me/preferences", `{"dateRange":null}`, userToken, "")
	server.UpdateMyPreferences(dateRangeClear.recorder, dateRangeClear.request, generated.UpdateMyPreferencesParams{})
	if dateRangeClear.recorder.Code != http.StatusOK || !strings.Contains(dateRangeClear.recorder.Body.String(), `"dateRange":null`) {
		t.Fatalf("date-range clear: %d %s", dateRangeClear.recorder.Code, dateRangeClear.recorder.Body.String())
	}
	dateRangeExplicit := callEndpoint(http.MethodPatch, "/api/me/preferences", `{"dateRange":"2024-02-29/2024-03-03"}`, userToken, "")
	server.UpdateMyPreferences(dateRangeExplicit.recorder, dateRangeExplicit.request, generated.UpdateMyPreferencesParams{})
	if dateRangeExplicit.recorder.Code != http.StatusOK || !strings.Contains(dateRangeExplicit.recorder.Body.String(), `"dateRange":"2024-02-29/2024-03-03"`) {
		t.Fatalf("explicit date-range set: %d %s", dateRangeExplicit.recorder.Code, dateRangeExplicit.recorder.Body.String())
	}
	for _, invalidRange := range []string{"last-30-days", "2023-02-29/2023-03-01", "2026-03-02/2026-03-01"} {
		invalid := callEndpoint(http.MethodPatch, "/api/me/preferences", `{"dateRange":"`+invalidRange+`"}`, userToken, "")
		server.UpdateMyPreferences(invalid.recorder, invalid.request, generated.UpdateMyPreferencesParams{})
		if invalid.recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid date range %q: %d %s", invalidRange, invalid.recorder.Code, invalid.recorder.Body.String())
		}
	}
	var userAccountID uuid.UUID
	if err := db.QueryRow(context.Background(), `SELECT account_id FROM app.users u JOIN app.authentication_principals p ON p.id=u.principal_id WHERE p.canonical_email=$1`, email).Scan(&userAccountID); err != nil {
		t.Fatal(err)
	}
	disjoint := concurrentPreferencePatches(t, server, adminDB, userAccountID, userToken,
		`{"dateRange":"last7Days"}`,
		`{"theme":"light","pageSize":50,"workoutColumns":["date","duration"]}`,
	)
	for index, response := range disjoint {
		if response.recorder.Code != http.StatusOK {
			t.Fatalf("concurrent disjoint preference patch %d: %d %s", index, response.recorder.Code, response.recorder.Body.String())
		}
	}
	preferences := getPreferences(t, server, userToken)
	if preferences.DateRange.MustGet() != "last7Days" || preferences.Theme != "light" || preferences.PageSize != 50 || len(preferences.WorkoutColumns) != 2 || preferences.WorkoutColumns[0] != "date" || preferences.WorkoutColumns[1] != "duration" {
		t.Fatalf("concurrent disjoint preference patches did not compose: %+v", preferences)
	}
	sameField := concurrentPreferencePatches(t, server, adminDB, userAccountID, userToken,
		`{"theme":"dark"}`,
		`{"theme":"light"}`,
	)
	for index, response := range sameField {
		if response.recorder.Code != http.StatusOK {
			t.Fatalf("concurrent same-field preference patch %d: %d %s", index, response.recorder.Code, response.recorder.Body.String())
		}
	}
	preferences = getPreferences(t, server, userToken)
	if preferences.Theme != "light" {
		t.Fatalf("concurrent same-field preference patches did not preserve lock order: theme=%s", preferences.Theme)
	}
	corruptPreferences := integrationAccountTransaction(t, adminDB, userAccountID)
	defer corruptPreferences.Rollback(context.Background())
	if _, err := corruptPreferences.Exec(context.Background(), `UPDATE app.preferences SET date_range='legacy-range' WHERE account_id=$1`, userAccountID); err != nil {
		t.Fatal(err)
	}
	if err := corruptPreferences.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	invalidRead := callEndpoint(http.MethodGet, "/api/me/preferences", "", userToken, "")
	server.GetMyPreferences(invalidRead.recorder, invalidRead.request)
	if invalidRead.recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid stored date range read: %d %s", invalidRead.recorder.Code, invalidRead.recorder.Body.String())
	}
	repairPreferences := integrationAccountTransaction(t, adminDB, userAccountID)
	defer repairPreferences.Rollback(context.Background())
	if _, err := repairPreferences.Exec(context.Background(), `UPDATE app.preferences SET date_range=NULL WHERE account_id=$1`, userAccountID); err != nil {
		t.Fatal(err)
	}
	if err := repairPreferences.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	beforeMissing := sender.count()
	missingRecovery := callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"missing`+suffix+`@example.test"}`, "", "")
	server.CreatePasswordResetRequest(missingRecovery.recorder, missingRecovery.request)
	if missingRecovery.recorder.Code != http.StatusAccepted || sender.count() != beforeMissing {
		t.Fatalf("missing recovery status=%d deliveries=%d", missingRecovery.recorder.Code, sender.count()-beforeMissing)
	}
	recoveries := []endpointCall{
		callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"`+email+`"}`, "", ""),
		callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"`+email+`"}`, "", ""),
	}
	var recoveryWait sync.WaitGroup
	for _, recovery := range recoveries {
		recoveryWait.Add(1)
		go func(call endpointCall) {
			defer recoveryWait.Done()
			server.CreatePasswordResetRequest(call.recorder, call.request)
		}(recovery)
	}
	recoveryWait.Wait()
	for _, recovery := range recoveries {
		if recovery.recorder.Code != http.StatusAccepted {
			t.Fatalf("concurrent recovery: %d %s", recovery.recorder.Code, recovery.recorder.Body.String())
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for sender.count() < beforeMissing+2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	messages := sender.snapshot()
	if len(messages) < beforeMissing+2 {
		t.Fatal("concurrent recovery delivery did not complete")
	}
	resetToken := ""
	for _, message := range messages[beforeMissing:] {
		candidate := messageToken(t, message)
		candidateVerifier, _ := tokenVerifier(candidate)
		var live bool
		if err := db.QueryRow(context.Background(), `SELECT revoked_at IS NULL AND used_at IS NULL FROM app.password_resets WHERE verifier=$1`, candidateVerifier).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live {
			resetToken = candidate
		}
	}
	if resetToken == "" {
		t.Fatal("concurrent recovery left no live reset")
	}
	reset := callEndpoint(http.MethodPost, "/api/password-resets", `{"token":"`+resetToken+`","password":"replacement password","passwordConfirmation":"replacement password"}`, "", "")
	server.CreatePasswordReset(reset.recorder, reset.request)
	if reset.recorder.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", reset.recorder.Code, reset.recorder.Body.String())
	}
	oldSession := callEndpoint(http.MethodGet, "/api/me", "", userToken, "")
	server.GetMe(oldSession.recorder, oldSession.request)
	if oldSession.recorder.Code != http.StatusUnauthorized {
		t.Fatalf("old session after reset: %d", oldSession.recorder.Code)
	}
	replayReset := callEndpoint(http.MethodPost, "/api/password-resets", `{"token":"`+resetToken+`","password":"another replacement","passwordConfirmation":"another replacement"}`, "", "")
	server.CreatePasswordReset(replayReset.recorder, replayReset.request)
	if replayReset.recorder.Code != http.StatusBadRequest {
		t.Fatalf("reset replay status=%d", replayReset.recorder.Code)
	}

	beforeExpiry := sender.count()
	expiryRecovery := callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"`+email+`"}`, "", "")
	server.CreatePasswordResetRequest(expiryRecovery.recorder, expiryRecovery.request)
	if expiryRecovery.recorder.Code != http.StatusAccepted {
		t.Fatalf("expiry recovery status=%d", expiryRecovery.recorder.Code)
	}
	deadline = time.Now().Add(2 * time.Second)
	for sender.count() == beforeExpiry && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	expiredResetToken := messageToken(t, sender.last())
	expiredResetVerifier, _ := tokenVerifier(expiredResetToken)
	if _, err := adminDB.Exec(context.Background(), `UPDATE app.password_resets SET issued_at=issued_at-interval '1 hour',expires_at=expires_at-interval '1 hour' WHERE verifier=$1`, expiredResetVerifier); err != nil {
		t.Fatal(err)
	}
	expiredReset := callEndpoint(http.MethodPost, "/api/password-resets", `{"token":"`+expiredResetToken+`","password":"expired replacement","passwordConfirmation":"expired replacement"}`, "", "")
	server.CreatePasswordReset(expiredReset.recorder, expiredReset.request)
	if expiredReset.recorder.Code != http.StatusBadRequest {
		t.Fatalf("expired reset status=%d", expiredReset.recorder.Code)
	}

	throttleSubject := "throttle" + suffix + "@example.test"
	for attempt := 1; attempt <= 4; attempt++ {
		call := callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"`+throttleSubject+`"}`, "", "")
		server.CreatePasswordResetRequest(call.recorder, call.request)
		want := http.StatusAccepted
		if attempt == 4 {
			want = http.StatusTooManyRequests
		}
		if call.recorder.Code != want || (attempt == 4 && call.recorder.Header().Get("Retry-After") == "") {
			t.Fatalf("recovery throttle attempt=%d status=%d retry=%q", attempt, call.recorder.Code, call.recorder.Header().Get("Retry-After"))
		}
	}
}

func TestInvitationSMTPFailureIsRetryable(t *testing.T) {
	databaseURL := os.Getenv("API_DATABASE_URL")
	migrationURL := os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	db, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminDB, err := pgxpool.New(context.Background(), migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	sender := &recordingSender{err: errors.New("provider detail must not persist")}
	server := integrationServer(t, db, sender)
	suffix := strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))[:10]
	adminID := uuid.Must(uuid.NewV7())
	adminEmail := "fail" + suffix + "@example.test"
	hash, _ := server.passwords.hash(context.Background(), "administrator password")
	tx, err := adminDB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(context.Background(), `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'administrator',$2,$2,$3,$3,1,$4,'Administrator')`, adminID, "fail"+suffix, adminEmail, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `INSERT INTO app.administrators(principal_id) VALUES($1)`, adminID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	token := insertTestSession(t, db, adminID, "bearer", "")
	email := "delivery" + suffix + "@example.test"
	request := callEndpoint(http.MethodPost, "/api/admin/invitations", `{"email":"`+email+`"}`, token, "")
	server.CreateInvitation(request.recorder, request.request, generated.CreateInvitationParams{})
	if request.recorder.Code != http.StatusServiceUnavailable || strings.Contains(request.recorder.Body.String(), "provider detail") {
		t.Fatalf("SMTP failure: %d %s", request.recorder.Code, request.recorder.Body.String())
	}
	var state, category string
	if err := db.QueryRow(context.Background(), `SELECT t.delivery_state,t.delivery_category FROM app.invitation_tokens t JOIN app.invitations i ON i.id=t.invitation_id WHERE i.canonical_email=$1`, email).Scan(&state, &category); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || category != "interrupted" {
		t.Fatalf("delivery state=%s category=%s", state, category)
	}
	recovery := callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"`+adminEmail+`"}`, "", "")
	server.CreatePasswordResetRequest(recovery.recorder, recovery.request)
	if recovery.recorder.Code != http.StatusAccepted || strings.Contains(recovery.recorder.Body.String(), "provider detail") {
		t.Fatalf("generic failed recovery status=%d body=%s", recovery.recorder.Code, recovery.recorder.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.QueryRow(context.Background(), `SELECT delivery_state,delivery_category FROM app.password_resets WHERE principal_id=$1 AND revoked_at IS NULL`, adminID).Scan(&state, &category); err == nil && state == "failed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if state != "failed" || category != "interrupted" {
		t.Fatalf("failed recovery state=%s category=%s", state, category)
	}

	fullDelivery := &deliveryService{queue: make(chan deliveryWork, 32)}
	for range 32 {
		fullDelivery.queue <- deliveryWork{}
	}
	server.delivery = fullDelivery
	queueFull := callEndpoint(http.MethodPost, "/api/password-reset-requests", `{"username":"`+adminEmail+`"}`, "", "")
	server.CreatePasswordResetRequest(queueFull.recorder, queueFull.request)
	if queueFull.recorder.Code != http.StatusAccepted {
		t.Fatalf("queue-full recovery status=%d", queueFull.recorder.Code)
	}
	if err := db.QueryRow(context.Background(), `SELECT delivery_state,delivery_category FROM app.password_resets WHERE principal_id=$1 AND revoked_at IS NULL`, adminID).Scan(&state, &category); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || category != "queue_full" {
		t.Fatalf("queue-full recovery state=%s category=%s", state, category)
	}
}

func integrationServer(t *testing.T, db *pgxpool.Pool, sender mailSender) *Server {
	t.Helper()
	delivery := newDeliveryService(sender)
	t.Cleanup(delivery.close)
	keyringFile := t.TempDir() + "/source-keyring.json"
	if err := os.WriteFile(keyringFile, []byte(`{"activeKeyId":"test","keys":{"test":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := sourcecrypto.LoadKeyring(keyringFile)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{config: config.API{Common: config.Common{SourceKeyringFile: keyringFile, LocalSourceRoots: []string{"/data/workouts"}}, PublicURL: "https://workouts.example.test", PasswordMinimum: 12, PageSizeMaximum: 100, SessionLifetime: 2 * time.Hour, RateLimitKey: []byte("0123456789abcdef0123456789abcdef")}, db: db, passwords: newPasswordHasher(12), delivery: delivery, avatars: newAvatarService(), sourceKeys: keyring}
}

func insertTestSession(t *testing.T, db *pgxpool.Pool, principalID uuid.UUID, kind, csrf string) string {
	t.Helper()
	token, verifier, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	var csrfValue any
	if kind == "cookie" {
		csrfValue = csrf
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5,transaction_timestamp()+interval '2 hours')`, uuid.Must(uuid.NewV7()), principalID, kind, verifier, csrfValue); err != nil {
		t.Fatal(err)
	}
	return token
}

type endpointCall struct {
	request  *http.Request
	recorder *httptest.ResponseRecorder
}

func callEndpoint(method, path, body, bearer, csrf string) endpointCall {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "198.51.100.8:1234"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	return endpointCall{request: request, recorder: httptest.NewRecorder()}
}

func callCookieEndpoint(method, path, body, cookie string) endpointCall {
	call := callEndpoint(method, path, body, "", "")
	call.request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	return call
}

func concurrentPreferencePatches(t *testing.T, server *Server, db *pgxpool.Pool, accountID uuid.UUID, token string, bodies ...string) []endpointCall {
	t.Helper()
	blocker := integrationAccountTransaction(t, db, accountID)
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(context.Background(), `SELECT true FROM app.preferences WHERE account_id=$1 FOR UPDATE`, accountID); err != nil {
		t.Fatal(err)
	}
	baseline := lockWaiterCount(t, db)

	responses := make([]endpointCall, len(bodies))
	done := make(chan int, len(bodies))
	for index, body := range bodies {
		responses[index] = callEndpoint(http.MethodPatch, "/api/me/preferences", body, token, "")
		go func(index int) {
			server.UpdateMyPreferences(responses[index].recorder, responses[index].request, generated.UpdateMyPreferencesParams{})
			done <- index
		}(index)
		waitForLockWaiters(t, db, baseline+index+1)
	}
	if err := blocker.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range bodies {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent preference patch did not complete")
		}
	}
	return responses
}

func waitForLockWaiters(t *testing.T, db *pgxpool.Pool, minimum int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lockWaiterCount(t, db) >= minimum {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d blocked preference patches", minimum)
}

func lockWaiterCount(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(DISTINCT pid) FROM pg_locks WHERE NOT granted`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func getPreferences(t *testing.T, server *Server, token string) generated.Preferences {
	t.Helper()
	response := callEndpoint(http.MethodGet, "/api/me/preferences", "", token, "")
	server.GetMyPreferences(response.recorder, response.request)
	if response.recorder.Code != http.StatusOK {
		t.Fatalf("get preferences: %d %s", response.recorder.Code, response.recorder.Body.String())
	}
	var preferences generated.Preferences
	if err := json.Unmarshal(response.recorder.Body.Bytes(), &preferences); err != nil {
		t.Fatal(err)
	}
	return preferences
}

func messageToken(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`token=([A-Za-z0-9_-]{43})`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("email did not contain the expected link token")
	}
	return match[1]
}
