package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConsistencyOwnerReadsIntegration(t *testing.T) {
	urls := []string{os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("WORKER_DATABASE_URL")}
	if urls[0] == "" || urls[1] == "" || urls[2] == "" {
		t.Skip("requires disposable role-specific PostgreSQL URLs")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, urls[0])
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	adminDB, err := pgxpool.New(ctx, urls[1])
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	workerDB, err := pgxpool.New(ctx, urls[2])
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	user, account := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, account)
	ids := fixture.workouts
	server := integrationServer(t, apiDB, &recordingSender{})
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	bearer := insertTestSession(t, apiDB, user, "bearer", "")
	read := func(path string) generated.ConsistencyReport {
		t.Helper()
		response := routeOwnerRead(handler, path, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, response)
		if response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("private report is cacheable")
		}
		var result generated.ConsistencyReport
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := "/api/stats/consistency?startDate=2026-03-07&endDate=2026-03-09"
	weekly := read(base + "&groupBy=week&firstWeekday=monday")
	if len(weekly.Buckets) != 2 || weekly.Buckets[0].BucketStart.Format(time.DateOnly) != "2026-03-02" || weekly.Buckets[0].WorkoutCount != 2 || weekly.Buckets[0].ActiveDays != 2 || weekly.Buckets[0].Duration != "360.25" {
		t.Fatalf("weekly=%#v", weekly)
	}
	first := weekly.Buckets[0]
	distance, _ := first.Distance.Get()
	calories, _ := first.Calories.Get()
	maximum, _ := first.LongestDuration.Get()
	if distance.Value != "8.5" || calories.Value != "450.5" || maximum != "240.125" {
		t.Fatalf("exact first bucket=%#v", first)
	}
	if !weekly.Buckets[1].Calories.IsNull() {
		t.Fatal("missing energy was converted to zero")
	}
	sunday := read(base + "&groupBy=week&firstWeekday=sunday")
	if len(sunday.Buckets) != 2 || sunday.Buckets[0].ActiveDays != 1 || sunday.Buckets[1].ActiveDays != 2 || sunday.Buckets[1].BucketStart.Format(time.DateOnly) != "2026-03-08" {
		t.Fatalf("Sunday buckets=%#v", sunday)
	}
	monthly := read(base + "&groupBy=month")
	if len(monthly.Buckets) != 1 || monthly.Buckets[0].ActiveDays != 3 || monthly.Buckets[0].Duration != "600.375" {
		t.Fatalf("monthly=%#v", monthly)
	}
	maximumDistance, _ := monthly.Buckets[0].LongestDistance.Get()
	maximumCalories, _ := monthly.Buckets[0].HighestCalories.Get()
	if maximumDistance.Value != "5.5" || maximumCalories.Value != "300.25" {
		t.Fatal("incorrect maxima")
	}
	partial := read("/api/stats/consistency?startDate=2026-03-08&endDate=2026-03-08&groupBy=week")
	if len(partial.Buckets) != 1 || partial.Buckets[0].WorkoutCount != 1 || partial.Buckets[0].Duration != "120.125" {
		t.Fatal("partial bucket included out-of-range workouts")
	}
	empty := read("/api/stats/consistency?startDate=2026-02-01&endDate=2026-02-28&groupBy=month")
	zero, _ := empty.Buckets[0].Distance.Get()
	if len(empty.Buckets) != 1 || empty.Buckets[0].ActiveDays != 0 || zero.Value != "0" || !empty.Buckets[0].LongestDuration.IsNull() {
		t.Fatal("empty buckets not preserved")
	}
	foreignUser, _ := insertSourceTestUser(t, adminDB)
	foreignBearer := insertTestSession(t, apiDB, foreignUser, "bearer", "")
	response := routeOwnerRead(handler, base, foreignBearer)
	if response.Code != http.StatusOK {
		t.Fatalf("foreign report status=%d", response.Code)
	}
	var foreign generated.ConsistencyReport
	if err := json.Unmarshal(response.Body.Bytes(), &foreign); err != nil {
		t.Fatal(err)
	}
	for _, bucket := range foreign.Buckets {
		if bucket.WorkoutCount != 0 {
			t.Fatal("foreign account read workout data")
		}
	}
	for _, path := range []string{base + "&groupBy=year", base + "&firstWeekday=tuesday", "/api/stats/consistency?startDate=2026-03-09&endDate=2026-03-07"} {
		if got := routeOwnerRead(handler, path, bearer); got.Code != 400 {
			t.Fatalf("invalid selector status=%d", got.Code)
		}
	}
	if got := routeOwnerRead(handler, "/api/stats/consistency?startDate=0001-01-08&endDate=9999-12-31&groupBy=week", bearer); got.Code != 422 {
		t.Fatalf("unbounded chart response status=%d", got.Code)
	}
	// Within a fenced rollback, two workouts share a local date across DST.
	readTx := integrationAccountTransaction(t, apiDB, account)
	defer readTx.Rollback(ctx)
	clock := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	from, _ := time.Parse(time.DateOnly, "2026-01-01")
	until, _ := time.Parse(time.DateOnly, "2026-12-31")
	through, err := queryConsistency(ctx, readTx, resolvedRange{start: from, end: until, timezone: "UTC"}, generated.ConsistencyGroupingMonth, "monday", clock)
	if err != nil || len(through.Buckets) != 3 || through.Buckets[2].WorkoutCount != 2 || through.Buckets[2].Duration != "360.25" || through.Range.EndDate.Time != until {
		t.Fatalf("future month clipping=%#v err=%v", through, err)
	}
	throughWeek, err := queryConsistency(ctx, readTx, resolvedRange{start: from, end: until, timezone: "UTC", allTime: true}, generated.ConsistencyGroupingWeek, "monday", clock)
	if err != nil || throughWeek.Buckets[len(throughWeek.Buckets)-1].BucketStart.Format(time.DateOnly) != "2026-03-02" {
		t.Fatalf("future week clipping=%#v err=%v", throughWeek, err)
	}
	var total int64
	for _, bucket := range throughWeek.Buckets {
		total += bucket.WorkoutCount
	}
	if total != 2 {
		t.Fatalf("all time included future-dated workout: %d", total)
	}
	futureStart := clock.AddDate(0, 0, 1)
	future, err := queryConsistency(ctx, readTx, resolvedRange{start: futureStart, end: until, timezone: "UTC"}, generated.ConsistencyGroupingWeek, "monday", clock)
	if err != nil || len(future.Buckets) != 0 {
		t.Fatalf("future-only result=%#v err=%v", future, err)
	}
	readTx.Rollback(ctx)

	tx, err := workerDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
		t.Fatal(err)
	}
	var source uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2,$3)`, fixture.jobID, fixture.childLease, database.SupportedSchemaVersion).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app.workouts SET local_start_date='2026-03-08' WHERE id=$1`, ids[2]); err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.DateOnly, "2026-03-07")
	end, _ := time.Parse(time.DateOnly, "2026-03-09")
	dedup, err := queryConsistency(ctx, tx, resolvedRange{start: start, end: end, timezone: "UTC"}, generated.ConsistencyGroupingMonth, "monday")
	if err != nil || dedup.Buckets[0].ActiveDays != 2 || dedup.Buckets[0].WorkoutCount != 3 {
		t.Fatalf("active day dedup=%#v err=%v", dedup, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app.workouts SET local_start_date=NULL WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	undated, err := queryConsistency(ctx, tx, resolvedRange{start: start, end: end, timezone: "UTC", allTime: true}, generated.ConsistencyGroupingMonth, "monday")
	if err != nil || undated.UndatedWorkoutCount != 1 || undated.Buckets[0].WorkoutCount != 2 {
		t.Fatalf("undated report=%#v err=%v", undated, err)
	}
}
