package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func diagnosticReaderDatabases(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	apiURL, adminURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if apiURL == "" || adminURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	apiDB, err := pgxpool.New(context.Background(), apiURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(apiDB.Close)
	adminDB, err := pgxpool.New(context.Background(), adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminDB.Close)
	return apiDB, adminDB
}

func diagnosticReaderAccount(t *testing.T, adminDB *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	principalID, accountID := insertSourceTestUser(t, adminDB)
	t.Cleanup(func() {
		ctx := context.Background()
		tx := integrationAccountTransaction(t, adminDB, accountID)
		defer tx.Rollback(ctx)
		// Delete only this fixture's reservations, including deliberately retained ones.
		if _, err := tx.Exec(ctx, `DELETE FROM app.matcher_slots WHERE account_id=$1`, accountID); err != nil {
			t.Error(err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Error(err)
		}
	})
	return principalID, accountID
}

func TestDiagnosticReaderLedgerIntegration(t *testing.T) {
	apiDB, adminDB := diagnosticReaderDatabases(t)
	ctx := context.Background()
	_, accountID := diagnosticReaderAccount(t, adminDB)
	_, workerAccountID := diagnosticReaderAccount(t, adminDB)
	_, foreignAccountID := diagnosticReaderAccount(t, adminDB)
	server := &Server{db: apiDB, diagnostics: &coverageDiagnosticService{ownerID: "diagnostic-ledger-" + uuid.NewString(), timeout: time.Second}}
	slotID, requestID := uuid.New(), uuid.New()
	acquired, err := server.acquireDiagnosticMatcherSlot(ctx, accountID, []string{}, slotID, requestID)
	if err != nil || !acquired {
		t.Fatalf("acquire diagnostic slot=%t err=%v", acquired, err)
	}
	defer server.releaseDiagnosticMatcherSlot(accountID, slotID, requestID)
	identity := func(value string) string {
		digest := sha256.Sum256([]byte(value))
		return hex.EncodeToString(digest[:])
	}
	cluster, busy, idle := identity(uuid.NewString()), identity("busy"), identity("idle")
	var readerLimit int
	if err := adminDB.QueryRow(ctx, `SELECT reader_limit FROM app.matcher_slot_limits WHERE singleton`).Scan(&readerLimit); err != nil {
		t.Fatal(err)
	}
	// Model already-assigned remote production jobs without changing shared limits.
	// The diagnostic acquired admission first; these rows exercise placement only.
	parentID, jobID := uuid.New(), uuid.New()
	tx := integrationAccountTransaction(t, adminDB, workerAccountID)
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'coverage_update',20)`, parentID, workerAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority) VALUES($1,$2,$3,'coverage_update_route',20)`, jobID, parentID, workerAccountID); err != nil {
		t.Fatal(err)
	}
	for range readerLimit {
		if _, err := tx.Exec(ctx, `INSERT INTO app.matcher_slots(slot_token,slot_kind,account_id,job_id,owner_id,lease_token,region_ids,expires_at,reader_cluster_key,reader_backend_key)
			VALUES($1,'production',$2,$3,'remote-coverage-worker',$4,'{}',clock_timestamp()+interval '2 minutes',$5,$6)`,
			uuid.New(), workerAccountID, jobID, uuid.New(), cluster, busy); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	chosen, err := server.assignDiagnosticMatcherReader(ctx, accountID, slotID, requestID, cluster, []string{busy, idle})
	if err != nil || chosen != idle {
		t.Fatalf("diagnostic ignored production reader load: chosen=%q err=%v", chosen, err)
	}
	for _, fence := range []struct {
		account, slot, request uuid.UUID
		owner                  string
	}{
		{foreignAccountID, slotID, requestID, server.diagnostics.ownerID},
		{accountID, uuid.New(), requestID, server.diagnostics.ownerID},
		{accountID, slotID, uuid.New(), server.diagnostics.ownerID},
		{accountID, slotID, requestID, "another-api-process"},
	} {
		foreign := &Server{db: apiDB, diagnostics: &coverageDiagnosticService{ownerID: fence.owner}}
		chosen, err := foreign.assignDiagnosticMatcherReader(ctx, fence.account, fence.slot, fence.request, cluster, []string{idle})
		if err != nil || chosen != "" {
			t.Fatalf("reader assignment accepted a foreign fence: chosen=%q err=%v", chosen, err)
		}
		if err := foreign.clearDiagnosticMatcherReader(ctx, fence.account, fence.slot, fence.request); err != nil {
			t.Fatal(err)
		}
		var stillAssigned bool
		if err := adminDB.QueryRow(ctx, `SELECT reader_backend_key=$2 FROM app.matcher_slots WHERE slot_token=$1`, slotID, idle).Scan(&stillAssigned); err != nil || !stillAssigned {
			t.Fatalf("foreign clear changed the assignment: assigned=%t err=%v", stillAssigned, err)
		}
	}
	if err := server.clearDiagnosticMatcherReader(ctx, accountID, slotID, requestID); err != nil {
		t.Fatal(err)
	}
	var admissionHeld bool
	if err := adminDB.QueryRow(ctx, `SELECT reader_backend_key IS NULL AND reader_cluster_key IS NULL AND expires_at>clock_timestamp()
		FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&admissionHeld); err != nil || !admissionHeld {
		t.Fatalf("failed-attempt clear released admission: held=%t err=%v", admissionHeld, err)
	}
	chosen, err = server.assignDiagnosticMatcherReader(ctx, accountID, slotID, requestID, cluster, []string{busy})
	if err != nil || chosen != "" {
		t.Fatalf("nullable capacity result: chosen=%q err=%v", chosen, err)
	}
	var before, after time.Time
	if err := adminDB.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	renewed, err := server.heartbeatDiagnosticMatcherSlot(ctx, accountID, slotID, requestID)
	if err != nil || !renewed {
		t.Fatalf("renewed=%t err=%v", renewed, err)
	}
	if err := adminDB.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&after); err != nil || !after.After(before) {
		t.Fatalf("diagnostic lease did not advance: before=%v after=%v err=%v", before, after, err)
	}
	server.releaseDiagnosticMatcherSlot(accountID, slotID, requestID)
	var remaining int
	if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("released slot remains: count=%d err=%v", remaining, err)
	}
}

func TestDiagnosticDesiredGenerationsAndCleanupIntegration(t *testing.T) {
	apiDB, adminDB := diagnosticReaderDatabases(t)
	ctx := context.Background()
	principalID, accountID := diagnosticReaderAccount(t, adminDB)
	workoutID, typeID := uuid.New(), uuid.New()
	tx := integrationAccountTransaction(t, adminDB, accountID)
	defer tx.Rollback(ctx)
	// As in the job API fixtures, omit unrelated source/import ancestry.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'running','Outdoor Run')`, typeID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
		VALUES($1,$2,$3,$4,$5,'diagnostic-reader-fixture',$6,'Outdoor Run','2026-09-10T08:00:00Z','2026-09-10T09:00:00Z','2026-09-10',3600)`,
		workoutID, accountID, uuid.New(), uuid.New(), typeID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,longitude,latitude,recorded_at)
		VALUES($1,$2,0,-122,37,'2026-09-10T08:00:00Z')`, accountID, workoutID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude)
		VALUES($1,$2,1,-122,37,-122,37,false)`, accountID, workoutID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state)
		VALUES($1,$2,1,$3,'unresolved')`, accountID, workoutID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	server := integrationServer(t, apiDB, &recordingSender{})
	serviceCtx, stopService := context.WithCancel(ctx)
	defer stopService()
	service, err := newCoverageDiagnosticService(serviceCtx, config.CoverageDiagnostics{
		Enabled: true, Timeout: time.Second, Concurrency: 1,
		OSMDatabaseURL: "postgresql://user@private-host.invalid/private-database",
	})
	if err != nil {
		t.Fatal(err)
	}
	server.diagnostics = service
	token := insertTestSession(t, apiDB, principalID, "bearer", "")
	call := callEndpoint(http.MethodPost, "/api/workouts/"+compactUUID(workoutID)+"/coverage-diagnostic-runs", "{}", token, "")
	server.CreateCoverageDiagnosticRun(call.recorder, call.request, generated.WorkoutID(compactUUID(workoutID)), generated.CreateCoverageDiagnosticRunParams{})
	if call.recorder.Code != http.StatusConflict {
		t.Fatalf("missing targets status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
	}
	tx = integrationAccountTransaction(t, adminDB, accountID)
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
		VALUES($1,$2,'fixture:north',7),($1,$2,'fixture:south',3)`, accountID, workoutID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	workout, status := server.loadDiagnosticWorkout(ctx, accountID, workoutID)
	if status != 0 || len(workout.targetGenerations) != 2 || workout.targetGenerations[0].RegionID != "fixture:north" ||
		workout.targetGenerations[0].GenerationID != 7 || workout.targetGenerations[1].GenerationID != 3 {
		t.Fatalf("desired targets=%+v status=%d", workout.targetGenerations, status)
	}
	// Inject snapshots to exercise the handler's terminal cleanup policy.
	service.reader.Close()
	service.reader = nil
	service.minimumTraversalMeters = 5
	service.roadGeometry = coverage.DefaultRoadGeometryRules()
	failure := errors.New("fixture failure")
	for _, test := range []struct {
		name                            string
		beginErr, evaluateErr, closeErr error
		legacy, physicalClosed, retain  bool
		status                          int
	}{
		{name: "legacy_evaluation_failure", evaluateErr: failure, legacy: true, status: http.StatusServiceUnavailable},
		{name: "begin_failure", beginErr: failure, status: http.StatusServiceUnavailable},
		{name: "begin_cleanup_unconfirmed", beginErr: errors.Join(failure, readerplacement.ErrCleanupUnconfirmed), retain: true, status: http.StatusServiceUnavailable},
		{name: "snapshot_cleanup_unconfirmed", retain: true, status: http.StatusServiceUnavailable},
		{name: "snapshot_cleanup_unconfirmed_after_failure", evaluateErr: failure, closeErr: failure, retain: true, status: http.StatusServiceUnavailable},
		{name: "confirmed_close_after_failure", evaluateErr: failure, closeErr: failure, physicalClosed: true, status: http.StatusServiceUnavailable},
		{name: "confirmed_success", physicalClosed: true, status: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			var slotID, requestID uuid.UUID
			closed := false
			clusterDigest := sha256.Sum256([]byte(accountID.String()))
			backendDigest := sha256.Sum256([]byte(test.name))
			cluster, backend := hex.EncodeToString(clusterDigest[:]), hex.EncodeToString(backendDigest[:])
			t.Cleanup(func() {
				// These are synthetic connections. Retire only this case's scratch slot.
				tx := integrationAccountTransaction(t, adminDB, accountID)
				defer tx.Rollback(ctx)
				if _, err := tx.Exec(ctx, `DELETE FROM app.matcher_slots WHERE account_id=$1 AND slot_token=$2`, accountID, slotID); err != nil {
					t.Error(err)
					return
				}
				if err := tx.Commit(ctx); err != nil {
					t.Error(err)
				}
			})
			service.begin = func(beginCtx context.Context) (coverageDiagnosticSnapshot, error) {
				tx := integrationAccountTransaction(t, adminDB, accountID)
				defer tx.Rollback(ctx)
				if err := tx.QueryRow(beginCtx, `SELECT slot_token,lease_token FROM app.matcher_slots WHERE account_id=$1 AND owner_id=$2`, accountID, service.ownerID).Scan(&slotID, &requestID); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(beginCtx); err != nil {
					t.Fatal(err)
				}
				chosen, err := server.assignDiagnosticMatcherReader(beginCtx, accountID, slotID, requestID, cluster, []string{backend})
				if err != nil || chosen != backend {
					t.Fatalf("fixture assignment=%q err=%v", chosen, err)
				}
				if test.beginErr != nil {
					return nil, test.beginErr
				}
				snapshot := &diagnosticSnapshotFixture{generationErr: test.evaluateErr, closeErr: test.closeErr, close: func(closeCtx context.Context) {
					var held bool
					if err := adminDB.QueryRow(closeCtx, `SELECT reader_backend_key=$2 AND expires_at>clock_timestamp() FROM app.matcher_slots WHERE slot_token=$1`, slotID, backend).Scan(&held); err != nil || !held {
						t.Errorf("snapshot closed after reservation release: held=%t err=%v", held, err)
					}
					closed = true
				}}
				if test.legacy {
					return snapshot, nil
				}
				return &diagnosticPhysicalSnapshotFixture{diagnosticSnapshotFixture: snapshot, physicalClosed: test.physicalClosed}, nil
			}
			call := callEndpoint(http.MethodPost, "/api/workouts/"+compactUUID(workoutID)+"/coverage-diagnostic-runs", "{}", token, "")
			server.CreateCoverageDiagnosticRun(call.recorder, call.request, generated.WorkoutID(compactUUID(workoutID)), generated.CreateCoverageDiagnosticRunParams{})
			if call.recorder.Code != test.status || closed != (test.beginErr == nil) {
				t.Fatalf("closed=%t status=%d body=%s", closed, call.recorder.Code, call.recorder.Body.String())
			}
			var remaining, assigned int
			if err := adminDB.QueryRow(ctx, `SELECT count(*),count(reader_backend_key) FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&remaining, &assigned); err != nil {
				t.Fatal(err)
			}
			want := 0
			if test.retain {
				want = 1
			}
			if remaining != want || assigned != want {
				t.Fatalf("reservation/assignment=%d/%d want %d/%d", remaining, assigned, want, want)
			}
		})
	}
}

func TestDiagnosticReservationRenewalUntilSnapshotCleanupIntegration(t *testing.T) {
	apiDB, adminDB := diagnosticReaderDatabases(t)
	ctx := context.Background()
	_, accountID := diagnosticReaderAccount(t, adminDB)
	server := &Server{db: apiDB, diagnostics: &coverageDiagnosticService{ownerID: "diagnostic-renewal-" + uuid.NewString()}}
	slotID, requestID := uuid.New(), uuid.New()
	acquired, err := server.acquireDiagnosticMatcherSlot(ctx, accountID, []string{}, slotID, requestID)
	if err != nil || !acquired {
		t.Fatalf("acquire diagnostic slot=%t err=%v", acquired, err)
	}
	var originalExpiry time.Time
	if err := adminDB.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	closing, finishClose, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server.diagnostics.begin = func(context.Context) (coverageDiagnosticSnapshot, error) {
		return &diagnosticSnapshotFixture{generationErr: context.Canceled, close: func(context.Context) {
			close(closing)
			<-finishClose
		}}, nil
	}
	stopHeartbeat := server.keepDiagnosticMatcherSlot(requestCtx, accountID, slotID, requestID, cancelRequest)
	go func() {
		defer close(done)
		defer func() {
			stopHeartbeat()
			server.releaseDiagnosticMatcherSlot(accountID, slotID, requestID)
		}()
		_, _ = server.evaluateCoverageDiagnostic(requestCtx, diagnosticWorkout{}, time.Now(), accountID, slotID, requestID)
	}()
	defer func() {
		close(finishClose)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("diagnostic cleanup did not finish")
		}
	}()
	<-closing
	cancelRequest()
	deadline := time.Now().Add(diagnosticHeartbeatInterval + 5*time.Second)
	for {
		var expiry time.Time
		if err := adminDB.QueryRow(ctx, `SELECT expires_at FROM app.matcher_slots WHERE slot_token=$1`, slotID).Scan(&expiry); err != nil {
			t.Fatalf("reservation disappeared during cleanup: %v", err)
		}
		if expiry.After(originalExpiry) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request cancellation stopped reservation renewal before snapshot cleanup")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
