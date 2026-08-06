package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkoutOwnerReadsIntegration(t *testing.T) {
	apiURL, migrationURL, workerURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("WORKER_DATABASE_URL")
	if apiURL == "" || migrationURL == "" || workerURL == "" {
		t.Skip("API_DATABASE_URL, MIGRATION_DATABASE_URL, and WORKER_DATABASE_URL are required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	workerDB, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()

	principalID, accountID := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, accountID)
	workouts := fixture.workouts
	bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	types := routeOwnerRead(handler, "/api/workout-types", bearer)
	if types.Code != http.StatusOK {
		t.Fatalf("types status=%d body=%s", types.Code, types.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/workout-types", types)
	var typeList generated.WorkoutTypeList
	if err := json.Unmarshal(types.Body.Bytes(), &typeList); err != nil || len(typeList.Items) != 2 || typeList.Items[0].DisplayName != "Cycling" || typeList.Items[1].DisplayName != "Running" {
		t.Fatalf("types=%#v err=%v", typeList, err)
	}

	page := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&tz=Not%2FAZone&page=1&pageSize=2&sort=distance:asc", bearer)
	if page.Code != http.StatusOK {
		t.Fatalf("workouts status=%d body=%s", page.Code, page.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&tz=Not%2FAZone&page=1&pageSize=2&sort=distance:asc", page)
	var list generated.WorkoutList
	if err := json.Unmarshal(page.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Range.Timezone != "UTC" || list.Pagination.TotalItems != 3 || list.Pagination.TotalPages != 2 || len(list.Items) != 2 || list.Items[0].Id != compactUUID(workouts[1]) || list.Items[1].Id != compactUUID(workouts[0]) {
		t.Fatalf("unexpected first page=%#v", list)
	}
	displayTimezone, _ := list.Items[0].DisplayTimezone.Get()
	if list.Items[0].Duration != "120.125" || list.Items[0].RoutePointCount != 1 || !list.Items[0].RouteAvailable || displayTimezone != "UTC-07:00" {
		t.Fatalf("exact/fallback/route fields=%#v", list.Items[0])
	}

	nulls := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&page=2&pageSize=2&sort=distance:asc", bearer)
	var nullPage generated.WorkoutList
	if err := json.Unmarshal(nulls.Body.Bytes(), &nullPage); err != nil || len(nullPage.Items) != 1 || nullPage.Items[0].Id != compactUUID(workouts[2]) || !nullPage.Items[0].Distance.IsNull() {
		t.Fatalf("null-last page=%#v err=%v", nullPage, err)
	}
	if !nullPage.Items[0].Pace.IsNull() || !nullPage.Items[0].Calories.IsNull() || !nullPage.Items[0].HeartRate.IsNull() || !nullPage.Items[0].Elevation.IsNull() || !nullPage.Items[0].DisplayTimezone.IsNull() {
		t.Fatalf("suspicious units or unknown timezone were exposed: %#v", nullPage.Items[0])
	}
	canonical := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&pageSize=3&sort=pace:asc", bearer)
	var canonicalList generated.WorkoutList
	if err := json.Unmarshal(canonical.Body.Bytes(), &canonicalList); err != nil || len(canonicalList.Items) != 3 {
		t.Fatalf("canonical metric list=%#v err=%v", canonicalList, err)
	}
	pace, paceErr := canonicalList.Items[0].Pace.Get()
	if paceErr != nil || pace.Unit != "min/km" || pace.Value != "5" || canonicalList.Items[0].Id != compactUUID(workouts[0]) {
		t.Fatalf("canonical pace was not safely derived or sorted: %#v", canonicalList.Items)
	}

	tie := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&pageSize=3&sort=duration:desc", bearer)
	var tieList generated.WorkoutList
	if err := json.Unmarshal(tie.Body.Bytes(), &tieList); err != nil || len(tieList.Items) != 3 {
		t.Fatalf("tie list=%#v err=%v", tieList, err)
	}
	if tieList.Items[0].Id > tieList.Items[1].Id && tieList.Items[0].Duration == tieList.Items[1].Duration {
		t.Fatal("equal sort values did not use ascending UUID tie-break")
	}

	tooLarge := routeOwnerRead(handler, "/api/workouts?dateRangeEnum=thisMonth&pageSize=101", bearer)
	if tooLarge.Code != http.StatusBadRequest {
		t.Fatalf("page max status=%d body=%s", tooLarge.Code, tooLarge.Body.String())
	}
	prefTx := integrationAccountTransaction(t, adminDB, accountID)
	if _, err := prefTx.Exec(ctx, `UPDATE app.preferences SET page_size=250 WHERE account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	if err := prefTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	clamped := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09", bearer)
	var clampedList generated.WorkoutList
	if err := json.Unmarshal(clamped.Body.Bytes(), &clampedList); err != nil || clampedList.Pagination.PageSize != 100 {
		t.Fatalf("default page size was not server-bounded: %#v err=%v", clampedList.Pagination, err)
	}

	summary := routeOwnerRead(handler, "/api/summary?startDate=2026-03-07&endDate=2026-03-09", bearer)
	if summary.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summary.Code, summary.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/summary?startDate=2026-03-07&endDate=2026-03-09", summary)
	var aggregate generated.WorkoutSummary
	if err := json.Unmarshal(summary.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}
	distance, _ := aggregate.Totals.Distance.Get()
	energy, _ := aggregate.Totals.Energy.Get()
	if aggregate.Totals.Count != 3 || aggregate.Totals.Duration != "600.375" || distance.Value != "8.5" || energy.Value != "300.5" || len(aggregate.ByType) != 2 {
		t.Fatalf("summary=%#v", aggregate)
	}
	runningDistance, _ := aggregate.ByType[1].Totals.Distance.Get()
	runningEnergy, _ := aggregate.ByType[1].Totals.Energy.Get()
	if aggregate.ByType[1].Type.DisplayName != "Running" || aggregate.ByType[1].Totals.Count != 2 || runningDistance.Value != "5.5" || runningEnergy.Value != "200.25" {
		t.Fatalf("mixed-unit type totals included suspicious values: %#v", aggregate.ByType[1])
	}
	unknownSummary := routeOwnerRead(handler, "/api/summary?startDate=2026-03-09&endDate=2026-03-09", bearer)
	var unknownAggregate generated.WorkoutSummary
	if err := json.Unmarshal(unknownSummary.Body.Bytes(), &unknownAggregate); err != nil || unknownAggregate.Totals.Count != 1 || unknownAggregate.Totals.Duration != "240.125" || !unknownAggregate.Totals.Distance.IsNull() || !unknownAggregate.Totals.Energy.IsNull() || len(unknownAggregate.ByType) != 1 || !unknownAggregate.ByType[0].Totals.Distance.IsNull() || !unknownAggregate.ByType[0].Totals.Energy.IsNull() {
		t.Fatalf("suspicious-only summary did not remain unavailable: %#v err=%v", unknownAggregate, err)
	}

	snapshot, err := server.accountTransactionWithOptions(ctx, accountID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback(ctx)
	var snapshotCount int64
	if err := snapshot.QueryRow(ctx, `SELECT count(*) FROM app.workouts WHERE local_start_date BETWEEN '2026-03-07' AND '2026-03-09'`).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	insertConcurrentWorkout(t, workerDB, accountID, fixture)
	snapshotItems, err := queryWorkouts(ctx, snapshot, resolvedRange{start: dateOnly(time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)), end: dateOnly(time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))}, 1, 100, []workoutSort{{"date", "desc"}})
	if err != nil || snapshotCount != 3 || len(snapshotItems) != 3 {
		t.Fatalf("repeatable-read count/page diverged: count=%d items=%d err=%v", snapshotCount, len(snapshotItems), err)
	}
	if err := snapshot.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09", bearer)
	var freshList generated.WorkoutList
	if err := json.Unmarshal(fresh.Body.Bytes(), &freshList); err != nil || freshList.Pagination.TotalItems != 4 || len(freshList.Items) != 4 {
		t.Fatalf("concurrent insert was not visible to a fresh request: %#v err=%v", freshList.Pagination, err)
	}

	foreignPrincipal, _ := insertSourceTestUser(t, adminDB)
	foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
	isolated := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09", foreignBearer)
	var isolatedList generated.WorkoutList
	if err := json.Unmarshal(isolated.Body.Bytes(), &isolatedList); err != nil || isolatedList.Pagination.TotalItems != 0 || len(isolatedList.Items) != 0 {
		t.Fatalf("account isolation=%#v err=%v", isolatedList, err)
	}
	empty := routeOwnerRead(handler, "/api/summary?startDate=1999-01-01&endDate=1999-01-01", bearer)
	var emptySummary generated.WorkoutSummary
	if err := json.Unmarshal(empty.Body.Bytes(), &emptySummary); err != nil {
		t.Fatal(err)
	}
	emptyDistance, _ := emptySummary.Totals.Distance.Get()
	emptyEnergy, _ := emptySummary.Totals.Energy.Get()
	if emptySummary.Totals.Count != 0 || emptySummary.Totals.Duration != "0" || emptyDistance.Value != "0" || emptyEnergy.Value != "0" || len(emptySummary.ByType) != 0 {
		t.Fatalf("empty summary=%#v", emptySummary)
	}
}

type workoutReadFixture struct {
	workouts                    []uuid.UUID
	sourceID, fileID, runningID uuid.UUID
	jobID, childLease           uuid.UUID
	hash                        [32]byte
}

func insertWorkoutReadFixtures(t *testing.T, adminDB, workerDB *pgxpool.Pool, accountID uuid.UUID) workoutReadFixture {
	t.Helper()
	ctx := context.Background()
	sourceID, parentID, jobID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tx := integrationAccountTransaction(t, adminDB, accountID)
	_, err := tx.Exec(ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope,status)
	 VALUES($1,$2,$3,$3,'health-auto-export-local',$4,'connected')`, sourceID, accountID, "read-fixture-"+sourceID.String(), []byte("fixture"))
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,status) VALUES($1,$2,'manual_ingest',80,'queued')`, parentID, accountID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,status) VALUES($1,$2,$3,'manual_ingest_source',80,'queued')`, jobID, parentID, accountID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, jobID, accountID, sourceID, []byte("fixture"))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	childLease := uuid.New()
	if !claimFixtureJob(t, workerDB, accountID, jobID, "read-fixture", childLease) {
		t.Fatal("fixture jobs could not be claimed")
	}

	fileID, cyclingID, runningID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	workouts := []uuid.UUID{
		uuid.Must(uuid.NewV7()),
		uuid.Must(uuid.NewV7()),
		uuid.Must(uuid.NewV7()),
	}
	hash := sha256.Sum256([]byte(sourceID.String()))
	tx, err = workerDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err == nil {
		var fenced uuid.UUID
		err = tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2)`, jobID, childLease).Scan(&fenced)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at) VALUES($1,$2,$3,$4,'fixture.json',1,$5,'succeeded',transaction_timestamp(),transaction_timestamp())`, fileID, accountID, sourceID, jobID, hash[:])
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'cycling','Cycling'),($3,$2,'running','Running')`, cyclingID, accountID, runningID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,start_offset_minutes,end_offset_minutes,local_start_date,provider_duration,is_indoor,location)
		 VALUES
		 ($1,$4,$5,$6,$7,'read-1',$8,'Running','2026-03-07T15:00:00Z','2026-03-07T15:04:00Z',-420,-420,'2026-03-07',240.125,false,'Trail'),
		 ($2,$4,$5,$6,$9,'read-2',$8,'Cycling','2026-03-08T15:00:00Z','2026-03-08T15:02:00Z',-420,-360,'2026-03-08',120.125,NULL,NULL),
		 ($3,$4,$5,$6,$7,'read-3',$8,'Running','2026-03-09T15:00:00Z','2026-03-09T15:04:00Z',NULL,NULL,'2026-03-09',240.125,true,'Gym')`, workouts[0], workouts[1], workouts[2], accountID, sourceID, fileID, runningID, hash[:], cyclingID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.workout_aggregates(account_id,workout_id,metric,value,unit,origin) VALUES
		 ($1,$2,'distance',5.5,'km','provider_direct'),($1,$2,'active_energy_burned',200.25,'kcal','provider_direct'),
		 ($1,$2,'speed_average',12,'km/hr','provider_direct'),
		 ($1,$3,'distance',3,'km','provider_direct'),($1,$3,'active_energy_burned',100.25,'kcal','provider_direct'),
		 ($1,$3,'heart_rate_average',123.5,'count/min','provider_direct'),($1,$3,'elevation_up',42.75,'m','provider_direct'),
		 ($1,$4,'distance',99,'mi','provider_direct'),($1,$4,'active_energy_burned',999,'kJ','provider_direct'),
		 ($1,$4,'speed_average',20,'m/s','provider_direct'),($1,$4,'heart_rate_average',170,'bpm','provider_direct'),
		 ($1,$4,'elevation_up',1000,'ft','provider_direct')`, accountID, workouts[0], workouts[1], workouts[2])
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude) VALUES($1,$2,0,'2026-03-08T15:01:00Z',40,-105)`, accountID, workouts[1])
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	return workoutReadFixture{workouts: workouts, sourceID: sourceID, fileID: fileID, runningID: runningID, jobID: jobID, childLease: childLease, hash: hash}
}

func insertConcurrentWorkout(t *testing.T, workerDB *pgxpool.Pool, accountID uuid.UUID, fixture workoutReadFixture) {
	t.Helper()
	ctx := context.Background()
	tx, err := workerDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		t.Fatal(err)
	}
	var sourceID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2)`, fixture.jobID, fixture.childLease).Scan(&sourceID); err != nil || sourceID != fixture.sourceID {
		t.Fatalf("concurrent fixture fence source=%s err=%v", sourceID, err)
	}
	workoutID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
	 VALUES($1,$2,$3,$4,$5,$6,$7,'Running','2026-03-09T20:00:00Z','2026-03-09T20:01:00Z','2026-03-09',60)`, workoutID, accountID, fixture.sourceID, fixture.fileID, fixture.runningID, "concurrent-"+workoutID.String(), fixture.hash[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func claimFixtureJob(t *testing.T, db *pgxpool.Pool, accountID, jobID uuid.UUID, worker string, lease uuid.UUID) bool {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		t.Fatal(err)
	}
	var claimed bool
	if err := tx.QueryRow(context.Background(), `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, jobID, worker, lease).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return claimed
}

func routeOwnerRead(handler http.Handler, path, bearer string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
