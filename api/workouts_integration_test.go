package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	extentDuration, extentDurationErr := list.ColumnExtents.Duration.Get()
	extentDistance, extentDistanceErr := list.ColumnExtents.Distance.Get()
	extentPace, extentPaceErr := list.ColumnExtents.Pace.Get()
	extentCalories, extentCaloriesErr := list.ColumnExtents.Calories.Get()
	extentHeartRate, extentHeartRateErr := list.ColumnExtents.HeartRate.Get()
	extentElevationGain, extentElevationGainErr := list.ColumnExtents.ElevationGain.Get()
	if extentDurationErr != nil || extentDuration != "240.125" || extentDistanceErr != nil || extentDistance.Value != "5.5" ||
		extentPaceErr != nil || extentPace.Value != "0.72765151515151515152" || extentCaloriesErr != nil || extentCalories.Value != "300.25" ||
		extentHeartRateErr != nil || extentHeartRate.Value != "123.5" || extentElevationGainErr != nil || extentElevationGain.Value != "42.75" {
		t.Fatalf("column extents=%#v", list.ColumnExtents)
	}
	displayTimezone, _ := list.Items[0].DisplayTimezone.Get()
	if list.Items[0].Duration != "120.125" || list.Items[0].RoutePointCount != 2 || !list.Items[0].RouteAvailable || displayTimezone != "UTC-07:00" {
		t.Fatalf("exact/fallback/route fields=%#v", list.Items[0])
	}
	totalCalories, totalCaloriesErr := list.Items[0].Calories.Get()
	activeCalories, activeCaloriesErr := list.Items[0].ActiveCalories.Get()
	heartRateMaximum, heartRateMaximumErr := list.Items[0].MaximumHeartRate.Get()
	minimumElevation, minimumElevationErr := list.Items[0].MinimumElevation.Get()
	if totalCaloriesErr != nil || totalCalories.Value != "150.25" || activeCaloriesErr != nil || activeCalories.Value != "100.25" ||
		heartRateMaximumErr != nil || heartRateMaximum.Value != "180" || minimumElevationErr != nil || minimumElevation.Value != "1600.25" {
		t.Fatalf("workout detail metrics=%#v", list.Items[0])
	}
	pointsPath := "/api/workouts/" + strings.ToLower(workouts[1].String()) + "/route/points"
	pointsResponse := routeOwnerRead(handler, pointsPath, bearer)
	if pointsResponse.Code != http.StatusOK {
		t.Fatalf("points status=%d body=%s", pointsResponse.Code, pointsResponse.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, pointsPath, pointsResponse)
	if disposition := pointsResponse.Header().Get("Content-Disposition"); disposition != `attachment; filename="2026-03-08-cycling.json"` {
		t.Fatalf("points content disposition=%q", disposition)
	}
	if cache := pointsResponse.Header().Get("Cache-Control"); cache != "private, no-store" {
		t.Fatalf("points cache control=%q", cache)
	}
	var points generated.WorkoutPointsExport
	if err := json.Unmarshal(pointsResponse.Body.Bytes(), &points); err != nil {
		t.Fatal(err)
	}
	altitude, altitudeErr := points.Points[0].AltitudeMeters.Get()
	if points.SchemaVersion != generated.WorkoutPointsExportSchemaVersionN1 || points.WorkoutId != compactUUID(workouts[1]) || points.WorkoutType != "Cycling" || len(points.Points) != 2 ||
		points.Points[0].Sequence != 0 || points.Points[1].Sequence != 1 || !points.Points[0].RecordedAt.Equal(points.Points[1].RecordedAt) || altitudeErr != nil || altitude != 1600.25 || points.Points[1].AltitudeMeters.IsNull() {
		t.Fatalf("normalized points=%#v altitude=%v err=%v", points, altitude, altitudeErr)
	}
	geoPath := "/api/workouts/" + compactUUID(workouts[1]) + "/route"
	geoResponse := routeOwnerRead(handler, geoPath, bearer)
	if geoResponse.Code != http.StatusOK || geoResponse.Header().Get("Content-Type") != "application/geo+json" || geoResponse.Header().Get("Content-Disposition") != `attachment; filename="2026-03-08-cycling.geojson"` {
		t.Fatalf("GeoJSON status=%d headers=%v body=%s", geoResponse.Code, geoResponse.Header(), geoResponse.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, geoPath, geoResponse)
	var geo generated.WorkoutGeoJSONFeature
	if err := json.Unmarshal(geoResponse.Body.Bytes(), &geo); err != nil {
		t.Fatal(err)
	}
	elevation, elevationErr := geo.Properties.ElevationSummary.Get()
	if geo.Type != generated.Feature || geo.Geometry.Type != generated.LineString || len(geo.Geometry.Coordinates) != 2 || len(geo.Geometry.Coordinates[0]) != 3 ||
		geo.Geometry.Coordinates[0][0] != -105 || geo.Geometry.Coordinates[0][2] != 1600.25 || elevationErr != nil || elevation.GainMeters != 1 || geo.Properties.Bounds.MaximumLatitude != 40.1 {
		t.Fatalf("3D GeoJSON=%#v elevation=%#v err=%v", geo, elevation, elevationErr)
	}
	twoDimensional := routeOwnerRead(handler, "/api/workouts/"+compactUUID(workouts[0])+"/route", bearer)
	var twoDimensionalGeo generated.WorkoutGeoJSONFeature
	if err := json.Unmarshal(twoDimensional.Body.Bytes(), &twoDimensionalGeo); err != nil || twoDimensional.Code != http.StatusOK || len(twoDimensionalGeo.Geometry.Coordinates) != 2 || len(twoDimensionalGeo.Geometry.Coordinates[0]) != 2 || !twoDimensionalGeo.Properties.ElevationSummary.IsNull() {
		t.Fatalf("2D GeoJSON status=%d feature=%#v err=%v", twoDimensional.Code, twoDimensionalGeo, err)
	}
	noRoute := routeOwnerRead(handler, "/api/workouts/"+compactUUID(workouts[2])+"/route/points", bearer)
	if noRoute.Code != http.StatusConflict {
		t.Fatalf("route-less export status=%d body=%s", noRoute.Code, noRoute.Body.String())
	}
	provenancePath := "/api/workouts/" + strings.ToLower(workouts[1].String()) + "/provenance"
	provenanceResponse := routeOwnerRead(handler, provenancePath, bearer)
	if provenanceResponse.Code != http.StatusOK {
		t.Fatalf("provenance status=%d body=%s", provenanceResponse.Code, provenanceResponse.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, provenancePath, provenanceResponse)
	var provenance generated.WorkoutProvenance
	if err := json.Unmarshal(provenanceResponse.Body.Bytes(), &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance.WorkoutId != compactUUID(workouts[1]) || len(provenance.Items) != 3 ||
		provenance.Items[0].Kind != generated.Created || provenance.Items[1].Kind != generated.Updated || provenance.Items[2].Kind != generated.MatchedUnchanged {
		t.Fatalf("provenance chronology=%#v", provenance)
	}
	if provenance.Items[0].SourceName != "Archived NFS" || provenance.Items[0].SourceType != "health-auto-export-local" || provenance.Items[0].SourceFile != "fixture.json" || len(provenance.Items[1].Warnings) != 1 || provenance.Items[1].Warnings[0].RoutePoint == nil || *provenance.Items[1].Warnings[0].RoutePoint != 0 {
		t.Fatalf("provenance context=%#v", provenance.Items)
	}
	if malformed := routeOwnerRead(handler, "/api/workouts/not-a-uuid/provenance", bearer); malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed workout ID status=%d body=%s", malformed.Code, malformed.Body.String())
	}

	nulls := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&page=2&pageSize=2&sort=distance:asc", bearer)
	var nullPage generated.WorkoutList
	if err := json.Unmarshal(nulls.Body.Bytes(), &nullPage); err != nil || len(nullPage.Items) != 1 || nullPage.Items[0].Id != compactUUID(workouts[2]) || !nullPage.Items[0].Distance.IsNull() {
		t.Fatalf("null-last page=%#v err=%v", nullPage, err)
	}
	if !nullPage.Items[0].Pace.IsNull() || !nullPage.Items[0].Calories.IsNull() || !nullPage.Items[0].ActiveCalories.IsNull() || !nullPage.Items[0].HeartRate.IsNull() || !nullPage.Items[0].MaximumHeartRate.IsNull() || !nullPage.Items[0].ElevationGain.IsNull() || !nullPage.Items[0].DisplayTimezone.IsNull() {
		t.Fatalf("suspicious units or unknown timezone were exposed: %#v", nullPage.Items[0])
	}
	canonical := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09&pageSize=3&sort=pace:asc", bearer)
	var canonicalList generated.WorkoutList
	if err := json.Unmarshal(canonical.Body.Bytes(), &canonicalList); err != nil || len(canonicalList.Items) != 3 {
		t.Fatalf("canonical metric list=%#v err=%v", canonicalList, err)
	}
	pace, paceErr := canonicalList.Items[0].Pace.Get()
	if paceErr != nil || pace.Unit != "min/km" || pace.Value != "0.66736111111111111111" || canonicalList.Items[0].Id != compactUUID(workouts[1]) {
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
	defer prefTx.Rollback(ctx)
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
	routedDistance, _ := aggregate.Totals.RoutedDistance.Get()
	if aggregate.Totals.Count != 3 || aggregate.Totals.Duration != "600.375" || distance.Value != "8.5" || energy.Value != "450.5" || aggregate.Totals.RouteCount != 2 || routedDistance.Value != "8.5" || len(aggregate.ByType) != 2 {
		t.Fatalf("summary=%#v", aggregate)
	}
	runningDistance, _ := aggregate.ByType[1].Totals.Distance.Get()
	runningEnergy, _ := aggregate.ByType[1].Totals.Energy.Get()
	if aggregate.ByType[1].Type.DisplayName != "Running" || aggregate.ByType[1].Totals.Count != 2 || runningDistance.Value != "5.5" || runningEnergy.Value != "300.25" || aggregate.ByType[1].Totals.RouteCount != 1 {
		t.Fatalf("mixed-unit type totals included suspicious values: %#v", aggregate.ByType[1])
	}
	unknownSummary := routeOwnerRead(handler, "/api/summary?startDate=2026-03-09&endDate=2026-03-09", bearer)
	var unknownAggregate generated.WorkoutSummary
	if err := json.Unmarshal(unknownSummary.Body.Bytes(), &unknownAggregate); err != nil || unknownAggregate.Totals.Count != 1 || unknownAggregate.Totals.Duration != "240.125" || !unknownAggregate.Totals.Distance.IsNull() || !unknownAggregate.Totals.Energy.IsNull() || unknownAggregate.Totals.RouteCount != 0 || !unknownAggregate.Totals.RoutedDistance.IsNull() || len(unknownAggregate.ByType) != 1 || !unknownAggregate.ByType[0].Totals.Distance.IsNull() || !unknownAggregate.ByType[0].Totals.Energy.IsNull() {
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
	foreignProvenance := routeOwnerRead(handler, "/api/workouts/"+compactUUID(workouts[1])+"/provenance", foreignBearer)
	if foreignProvenance.Code != http.StatusNotFound {
		t.Fatalf("foreign provenance status=%d body=%s", foreignProvenance.Code, foreignProvenance.Body.String())
	}
	foreignPoints := routeOwnerRead(handler, "/api/workouts/"+compactUUID(workouts[1])+"/route/points", foreignBearer)
	if foreignPoints.Code != http.StatusNotFound {
		t.Fatalf("foreign points status=%d body=%s", foreignPoints.Code, foreignPoints.Body.String())
	}
	foreignGeoJSON := routeOwnerRead(handler, "/api/workouts/"+compactUUID(workouts[1])+"/route", foreignBearer)
	if foreignGeoJSON.Code != http.StatusNotFound {
		t.Fatalf("foreign GeoJSON status=%d body=%s", foreignGeoJSON.Code, foreignGeoJSON.Body.String())
	}
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
	foreignDelete := routeWorkoutDelete(handler, workouts[1], foreignBearer)
	if foreignDelete.Code != http.StatusNotFound {
		t.Fatalf("foreign delete status=%d body=%s", foreignDelete.Code, foreignDelete.Body.String())
	}
	deleted := routeWorkoutDelete(handler, workouts[1], bearer)
	if deleted.Code != http.StatusAccepted || deleted.Header().Get("Location") == "" {
		t.Fatalf("delete status=%d headers=%v body=%s", deleted.Code, deleted.Header(), deleted.Body.String())
	}
	validateRecordedResponse(t, http.MethodDelete, "/api/workouts/"+compactUUID(workouts[1]), deleted)
	var accepted generated.WorkoutDeletionAccepted
	if err := json.Unmarshal(deleted.Body.Bytes(), &accepted); err != nil || accepted.Reused || accepted.TargetCount != 1 || accepted.Status != "queued" {
		t.Fatalf("deletion accepted=%#v err=%v", accepted, err)
	}
	hidden := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09", bearer)
	var hiddenList generated.WorkoutList
	if err := json.Unmarshal(hidden.Body.Bytes(), &hiddenList); err != nil || hiddenList.Pagination.TotalItems != 3 {
		t.Fatalf("logically hidden list=%#v err=%v", hiddenList.Pagination, err)
	}
	if hiddenProvenance := routeOwnerRead(handler, "/api/workouts/"+compactUUID(workouts[1])+"/provenance", bearer); hiddenProvenance.Code != http.StatusNotFound {
		t.Fatalf("hidden provenance status=%d body=%s", hiddenProvenance.Code, hiddenProvenance.Body.String())
	}
	reusedDelete := routeWorkoutDelete(handler, workouts[1], bearer)
	var reused generated.WorkoutDeletionAccepted
	if err := json.Unmarshal(reusedDelete.Body.Bytes(), &reused); err != nil || reusedDelete.Code != http.StatusAccepted || !reused.Reused || reused.JobId != accepted.JobId {
		t.Fatalf("reused deletion status=%d value=%#v err=%v", reusedDelete.Code, reused, err)
	}
	job := routeOwnerRead(handler, "/api/jobs/"+accepted.JobId, bearer)
	if job.Code != http.StatusOK {
		t.Fatalf("deletion job status=%d body=%s", job.Code, job.Body.String())
	}
	listedJobs := routeOwnerRead(handler, "/api/jobs?page=1&pageSize=100&operation=workout_deletion", bearer)
	var jobList generated.JobList
	if err := json.Unmarshal(listedJobs.Body.Bytes(), &jobList); err != nil {
		t.Fatal(err)
	}
	listedDeletion := false
	for _, item := range jobList.Items {
		if item.Id == accepted.JobId && item.Operation != nil && *item.Operation == generated.JobSummaryOperationWorkoutDeletion {
			listedDeletion = true
		}
	}
	if listedJobs.Code != http.StatusOK || !listedDeletion {
		t.Fatalf("deletion job is missing from history: status=%d list=%#v", listedJobs.Code, jobList)
	}
	rangeDelete := routeWorkoutRangeDelete(handler, "2026-03-07", "2026-03-09", bearer)
	var rangeAccepted generated.WorkoutDeletionAccepted
	if err := json.Unmarshal(rangeDelete.Body.Bytes(), &rangeAccepted); err != nil || rangeDelete.Code != http.StatusAccepted || rangeAccepted.TargetCount != 3 || rangeAccepted.Reused {
		t.Fatalf("range deletion status=%d accepted=%#v err=%v", rangeDelete.Code, rangeAccepted, err)
	}
	afterRange := routeOwnerRead(handler, "/api/workouts?startDate=2026-03-07&endDate=2026-03-09", bearer)
	var afterRangeList generated.WorkoutList
	if err := json.Unmarshal(afterRange.Body.Bytes(), &afterRangeList); err != nil || afterRangeList.Pagination.TotalItems != 0 {
		t.Fatalf("range deletion visibility=%#v err=%v", afterRangeList.Pagination, err)
	}
	reusedRange := routeWorkoutRangeDelete(handler, "2026-03-07", "2026-03-09", bearer)
	var reusedRangeAccepted generated.WorkoutDeletionAccepted
	if err := json.Unmarshal(reusedRange.Body.Bytes(), &reusedRangeAccepted); err != nil || reusedRange.Code != http.StatusAccepted || !reusedRangeAccepted.Reused || reusedRangeAccepted.JobId != rangeAccepted.JobId || reusedRangeAccepted.TargetCount != 3 {
		t.Fatalf("reused range deletion status=%d accepted=%#v err=%v", reusedRange.Code, reusedRangeAccepted, err)
	}
}

func routeWorkoutDelete(handler http.Handler, workoutID uuid.UUID, bearer string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/workouts/"+compactUUID(workoutID), nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	handler.ServeHTTP(response, request)
	return response
}

func routeWorkoutRangeDelete(handler http.Handler, startDate, endDate, bearer string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/workouts?startDate="+startDate+"&endDate="+endDate, nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	handler.ServeHTTP(response, request)
	return response
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
	defer tx.Rollback(ctx)
	_, err := tx.Exec(ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope,status)
	 VALUES($1,$2,$3,$3,'health-auto-export-local',$4,'connected')`, sourceID, accountID, "read-fixture-"+sourceID.String(), []byte("fixture"))
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,status) VALUES($1,$2,'manual_ingest',80,'queued')`, parentID, accountID)
	}
	if err == nil {
		parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, compactUUID(sourceID))
		_, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,status,parameters)
			VALUES($1,$2,$3,'manual_ingest_source',80,'queued',$4)`, jobID, parentID, accountID, parameters)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, jobID, accountID, sourceID, []byte("fixture"))
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
		 VALUES($1,$2,$3,1,'Archived NFS','health-auto-export-local')`, jobID, accountID, sourceID)
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
		_, err = tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at) VALUES($1,$2,$3,$4,'archive/fixture.json',1,$5,'succeeded',transaction_timestamp(),transaction_timestamp())`, fileID, accountID, sourceID, jobID, hash[:])
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
		 ($1,$2,'distance',5.5,'km','provider_direct'),($1,$2,'active_energy_burned',200.25,'kcal','provider_direct'),($1,$2,'total_energy',300.25,'kcal','provider_direct'),
		 ($1,$2,'speed_average',12,'km/hr','provider_direct'),
		 ($1,$3,'distance',3,'km','provider_direct'),($1,$3,'active_energy_burned',100.25,'kcal','provider_direct'),($1,$3,'total_energy',150.25,'kcal','provider_direct'),
		 ($1,$3,'heart_rate_average',123.5,'count/min','provider_direct'),($1,$3,'heart_rate_maximum',180,'count/min','provider_direct'),($1,$3,'elevation_up',42.75,'m','provider_direct'),
		 ($1,$4,'distance',99,'mi','provider_direct'),($1,$4,'active_energy_burned',999,'kJ','provider_direct'),
		 ($1,$4,'speed_average',20,'m/s','provider_direct'),($1,$4,'heart_rate_average',170,'bpm','provider_direct'),
		 ($1,$4,'elevation_up',1000,'ft','provider_direct')`, accountID, workouts[0], workouts[1], workouts[2])
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,timestamp_offset_minutes,latitude,longitude,altitude,speed,course,horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy) VALUES
		 ($1,$2,0,'2026-03-08T15:01:00Z',-420,40,-105,1600.25,3.5,180,4.2,5.3,0.4,1.5),
		 ($1,$2,1,'2026-03-08T15:01:00Z',-420,40.1,-104.9,1601.25,NULL,NULL,NULL,NULL,NULL,NULL),
		 ($1,$3,0,'2026-03-07T15:01:00Z',-420,39.9,-105.1,NULL,NULL,NULL,NULL,NULL,NULL,NULL),
		 ($1,$3,1,'2026-03-07T15:02:00Z',-420,40,-105,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`, accountID, workouts[1], workouts[0])
	}
	if err == nil {
		var replaced bool
		err = tx.QueryRow(ctx, `SELECT app.replace_workout_route_summary($1,2,-105,40,-104.9,40.1,1600.25,1601.25,1,true)`, workouts[1]).Scan(&replaced)
		if err == nil && !replaced {
			err = errors.New("3D fixture route summary was not replaced")
		}
	}
	if err == nil {
		var replaced bool
		err = tx.QueryRow(ctx, `SELECT app.replace_workout_route_summary($1,2,-105.1,39.9,-105,40,NULL,NULL,NULL,false)`, workouts[0]).Scan(&replaced)
		if err == nil && !replaced {
			err = errors.New("2D fixture route summary was not replaced")
		}
	}
	if err == nil {
		for _, workoutID := range workouts[:2] {
			var replaced bool
			err = tx.QueryRow(ctx, `SELECT app.replace_workout_split_summary($1)`, workoutID).Scan(&replaced)
			if err != nil || !replaced {
				break
			}
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,warnings,created_at) VALUES
		 ($1,$4,$5,$6,$7,$8,'created',$9,'[]','2026-03-08T15:03:00Z'),
		 ($2,$4,$5,$6,$7,$8,'updated',$9,'[{"code":"invalid_optional_route_value","field":"route_speed","route_point":0}]','2026-03-08T15:04:00Z'),
		 ($3,$4,$5,$6,$7,$8,'matched_unchanged',$9,'[]','2026-03-08T15:05:00Z')`,
			uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), accountID, sourceID, workouts[1], fileID, jobID, hash[:])
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
