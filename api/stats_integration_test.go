package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewCoverageStatsPostgreSQL(t *testing.T) {
	if os.Getenv("STATS_TEST_DISPOSABLE") != "1" {
		t.Skip("STATS_TEST_DISPOSABLE=1 and an isolated loopback workouts_stats_test database are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var pools []*pgxpool.Pool
	var endpoint string
	for i, name := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL"} {
		cfg, err := pgxpool.ParseConfig(os.Getenv(name))
		if err != nil || cfg == nil || (cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "::1") ||
			!strings.HasPrefix(cfg.ConnConfig.Database, "workouts_stats_test") {
			t.Fatal("Stats fixtures require explicit isolated loopback test URLs: " + name)
		}
		c := cfg.ConnConfig
		for _, fallback := range c.Fallbacks {
			if fallback.Host != c.Host || fallback.Port != c.Port {
				t.Fatal("Stats fixtures prohibit alternate endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", c.Host, c.Port, c.Database)
		if endpoint != "" && target != endpoint {
			t.Fatal("Stats fixture URLs must address the same disposable database")
		}
		endpoint = target
		cfg.MaxConns = 2
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		var role string
		var privileged bool
		if err := pool.QueryRow(ctx, `SELECT current_user,rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&role, &privileged); err != nil ||
			role != []string{"workouts_api", "workouts_migration", "workouts_worker"}[i] || privileged {
			t.Fatalf("Stats fixtures require restricted roles: role=%s privileged=%t err=%v", role, privileged, err)
		}
		pools = append(pools, pool)
	}
	apiDB, adminDB, workerDB := pools[0], pools[1], pools[2]
	principal, account := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, account)
	seedStatsVisits(t, ctx, adminDB, account, fixture)
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	otherSession := insertTestSession(t, apiDB, principal, "bearer", "")
	foreignPrincipal, _ := insertSourceTestUser(t, adminDB)
	foreign := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	create := func(start, end string) generated.StatsReportScope {
		t.Helper()
		response := routeMapRequest(handler, http.MethodPost, "/api/stats/report-scopes",
			fmt.Sprintf(`{"startDate":%q,"endDate":%q,"tz":"Ignored/Explicit"}`, start, end), bearer)
		if response.Code != http.StatusCreated {
			t.Fatalf("prepare status=%d body=%s", response.Code, response.Body.String())
		}
		validateRecordedResponse(t, http.MethodPost, "/api/stats/report-scopes", response)
		var scope generated.StatsReportScope
		if err := json.Unmarshal(response.Body.Bytes(), &scope); err != nil || scope.Range.Timezone != "UTC" {
			t.Fatalf("scope=%+v err=%v", scope, err)
		}
		return scope
	}
	scope := create("2024-03-25", "2026-10-01")
	root := "/api/stats/report-scopes/" + scope.Id + "/new-coverage"
	query := fmt.Sprintf("?generation=%d", scope.DataGeneration)
	readBuckets := func(suffix string) generated.NewCoverageBucketList {
		t.Helper()
		response := routeOwnerRead(handler, root+query+suffix, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("buckets %s: status=%d body=%s", suffix, response.Code, response.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, root+query+suffix, response)
		var result generated.NewCoverageBucketList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	readVisits := func(suffix string) generated.NewCoverageVisitList {
		t.Helper()
		path := root + "/visits" + query + suffix
		response := routeOwnerRead(handler, path, bearer)
		if response.Code != http.StatusOK {
			t.Fatalf("visits %s: status=%d body=%s", suffix, response.Code, response.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, response)
		var result generated.NewCoverageVisitList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	t.Run("PartialRangeZeroBucketsDedupAndCalendarReturns", func(t *testing.T) {
		monthly := readBuckets("&pageSize=100")
		if monthly.Pagination.TotalItems != 32 || len(monthly.Items) != 32 || monthly.Items[0].BucketStart.String() != "2026-10-01" || monthly.Items[31].BucketStart.String() != "2024-03-01" {
			t.Fatalf("month range=%+v", monthly)
		}
		expected := map[string][2]int64{"2025-03-01": {0, 1}, "2026-03-01": {0, 1}, "2026-10-01": {2, 2}}
		var allTime, pastYear int64
		for _, item := range monthly.Items {
			want := expected[item.BucketStart.String()]
			if item.AllTime != want[0] || item.PastYear != want[1] {
				t.Fatalf("bucket=%+v want=%v", item, want)
			}
			allTime += item.AllTime
			pastYear += item.PastYear
		}
		yearly := readBuckets("&groupBy=year")
		if len(yearly.Items) != 3 || yearly.Items[0].BucketStart.String() != "2026-01-01" ||
			yearly.Items[0].AllTime != 2 || yearly.Items[0].PastYear != 3 || yearly.Items[1].AllTime != 0 || yearly.Items[1].PastYear != 1 ||
			yearly.Items[2].AllTime != 0 || yearly.Items[2].PastYear != 0 || allTime != 2 || pastYear != 4 {
			t.Fatalf("yearly=%+v", yearly)
		}
		zero := readVisits("&bucketStart=2024-03-01&metric=all_time")
		if zero.TotalVisits != 0 || len(zero.Items) != 0 || zero.Pagination.TotalItems != 0 {
			t.Fatalf("outside-range history was promoted to new: %+v", zero)
		}
		first, second := readBuckets("&pageSize=1"), readBuckets("&pageSize=1&page=2")
		if len(first.Items) != 1 || len(second.Items) != 1 || first.Items[0].BucketStart == second.Items[0].BucketStart || second.Pagination.TotalItems != 32 {
			t.Fatal("unstable bucket pagination")
		}
		beyond := readBuckets("&pageSize=1&page=100")
		if len(beyond.Items) != 0 || beyond.Pagination.TotalItems != 32 {
			t.Fatal("out-of-range page lost its true total")
		}
	})
	t.Run("QualifyingDatePreviousVisitSearchAndCountParity", func(t *testing.T) {
		returns := readVisits("&groupBy=year&bucketStart=2026-01-01&metric=past_year")
		if returns.TotalVisits != 3 || len(returns.Items) != 3 || returns.Items[2].Name != "Return Road" ||
			returns.Items[2].NewVisit.String() != "2026-03-01" {
			t.Fatalf("return details=%+v", returns)
		}
		previous, err := returns.Items[2].PreviousVisit.Get()
		if err != nil || previous.String() != "2025-02-28" || !returns.Items[0].PreviousVisit.IsNull() {
			t.Fatalf("qualifying event predecessor=%v err=%v", previous, err)
		}
		for _, visit := range returns.Items {
			var date time.Time
			if err := adminDB.QueryRow(ctx, `SELECT local_start_date FROM app.workouts WHERE account_id=$1 AND id=$2`, account, visit.NewVisitWorkoutId).Scan(&date); err != nil || date.Format(time.DateOnly) != visit.NewVisit.String() {
				t.Fatalf("new visit did not target its qualifying workout: %v", err)
			}
			if visit.PreviousVisit.IsNull() {
				if !visit.PreviousVisitWorkoutId.IsNull() {
					t.Fatal("first-ever visit has a fabricated previous workout")
				}
				continue
			}
			id, idErr := visit.PreviousVisitWorkoutId.Get()
			prior, priorErr := visit.PreviousVisit.Get()
			if idErr != nil || priorErr != nil {
				t.Fatal("previous workout metadata missing")
			}
			if err := adminDB.QueryRow(ctx, `SELECT local_start_date FROM app.workouts WHERE account_id=$1 AND id=$2`, account, id).Scan(&date); err != nil || date.Format(time.DateOnly) != prior.String() {
				t.Fatalf("previous visit did not target its actual workout: %v", err)
			}
		}
		for _, check := range []struct{ date, order, expected string }{
			{"2026-03-01", "ASC", returns.Items[2].NewVisitWorkoutId},
			{"2025-02-28", "DESC", returns.Items[2].PreviousVisitWorkoutId.MustGet()},
		} {
			var expected uuid.UUID
			if err := adminDB.QueryRow(ctx, `SELECT w.id FROM app.workouts w WHERE w.account_id=$1 AND w.local_start_date=$2::date AND EXISTS(
				SELECT 1 FROM app.workout_segment_matches m JOIN app.coverage_paths p ON p.account_id=m.account_id AND p.logical_path_id=m.logical_path_id
				WHERE m.account_id=w.account_id AND m.workout_id=w.id AND p.name='Return Road') ORDER BY w.started_at `+check.order+`,w.id `+check.order+` LIMIT 1`, account, check.date).Scan(&expected); err != nil || compactUUID(expected) != check.expected {
				t.Fatal("same-date workout tie was not resolved deterministically")
			}
		}
		first := readVisits("&bucketStart=2026-10-01&metric=all_time&sort=name:asc")
		if first.TotalVisits != 2 || len(first.Items) != 2 || first.Items[0].NewVisit.String() != "2026-10-01" ||
			first.Items[1].EntityKind != generated.NewCoverageVisitEntityKindPark {
			t.Fatalf("park/path first visits=%+v", first)
		}
		literal := readVisits("&bucketStart=2026-10-01&metric=past_year&search=%25_")
		if literal.TotalVisits != 2 || literal.Pagination.TotalItems != 1 || len(literal.Items) != 1 || literal.Items[0].Name != "Fresh%_Way" {
			t.Fatalf("literal search=%+v", literal)
		}
		place := readVisits("&bucketStart=2026-10-01&metric=past_year&search=FIXTURE%20TOWN")
		if place.Pagination.TotalItems != 2 {
			t.Fatal("case-insensitive displayed-place search failed")
		}
		for _, direction := range []string{"asc", "desc"} {
			ordered := readVisits("&groupBy=year&bucketStart=2026-01-01&metric=past_year&sort=previousVisit:" + direction)
			if ordered.Items[0].PreviousVisit.IsNull() || !ordered.Items[2].PreviousVisit.IsNull() {
				t.Fatal("missing previous visits must sort last in both directions")
			}
		}
	})
	t.Run("NavigationMetadataRetainsOutOfRangeDatesWithoutChangingScope", func(t *testing.T) {
		narrow := create("2026-03-01", "2026-03-31")
		path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage/visits?generation=%d&bucketStart=2026-03-01&metric=past_year", narrow.Id, narrow.DataGeneration)
		response := routeOwnerRead(handler, path, bearer)
		var result generated.NewCoverageVisitList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || len(result.Items) != 1 {
			t.Fatal("narrow navigation metadata unavailable")
		}
		visit := result.Items[0]
		prior, err := visit.PreviousVisit.Get()
		if err != nil || prior.String() != "2025-02-28" || visit.PreviousVisitWorkoutId.IsNull() || result.Scope.Range.StartDate.String() != "2026-03-01" {
			t.Fatal("navigation silently expanded the range or lost the previous workout")
		}
		var sessionID uuid.UUID
		if err := adminDB.QueryRow(ctx, `SELECT session_id FROM app.stats_report_scopes WHERE account_id=$1 AND id=$2`, account, narrow.Id).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		tx, err := server.statsTransaction(ctx, authenticatedSession{accountID: &account, sessionID: sessionID}, true)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var rows int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.stats_visit_workouts($1,$2,$3,$4,$5,$6,$7)`, account, uuid.New(), narrow.Id, narrow.DataGeneration, visit.EntityId, visit.NewVisit.Time, prior.Time).Scan(&rows); err != nil || rows != 0 {
			t.Fatal("navigation reader exposed another session's scope")
		}
	})
	t.Run("FirstEverEducationDateIsNotLatestRepeat", func(t *testing.T) {
		november := create("2026-11-01", "2026-11-30")
		path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage/visits?generation=%d&bucketStart=2026-11-01&metric=past_year", november.Id, november.DataGeneration)
		response := routeOwnerRead(handler, path, bearer)
		var result generated.NewCoverageVisitList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK ||
			len(result.Items) != 1 || result.Items[0].Name != "Fixture School" || result.Items[0].NewVisit.String() != "2026-11-03" || !result.Items[0].PreviousVisit.IsNull() {
			t.Fatalf("first education visit=%+v status=%d err=%v", result, response.Code, err)
		}
	})
	t.Run("InterveningRepeatResetsReturnAnniversary", func(t *testing.T) {
		future := create("2027-01-01", "2027-12-31")
		path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage/visits?generation=%d&groupBy=year&bucketStart=2027-01-01&metric=past_year", future.Id, future.DataGeneration)
		response := routeOwnerRead(handler, path, bearer)
		var result generated.NewCoverageVisitList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.TotalVisits != 0 || len(result.Items) != 0 {
			t.Fatalf("intervening repeat ignored: result=%+v status=%d err=%v", result, response.Code, err)
		}
	})
	t.Run("ReportPinsSurviveCanonicalContextCollection", func(t *testing.T) {
		for day := 1; day <= 10; day++ {
			create(fmt.Sprintf("2025-01-%02d", day), "2026-10-01")
		}
		if got := readBuckets("&pageSize=100"); got.Pagination.TotalItems != 32 {
			t.Fatal("an actively pinned Stats context was collected")
		}
	})
	t.Run("ColdContextMetadataUsesCanonicalIndexProbe", func(t *testing.T) {
		cold := create("2025-02-03", "2026-10-02")
		var contextID uuid.UUID
		if err := adminDB.QueryRow(ctx, `SELECT coverage_display_context_id FROM app.stats_report_scopes WHERE account_id=$1 AND id=$2`, account, cold.Id).Scan(&contextID); err != nil {
			t.Fatal(err)
		}
		raw, err := migrations.Files.ReadFile("030_coverage_display.sql")
		if err != nil {
			t.Fatal(err)
		}
		_, body, found := strings.Cut(string(raw), "CREATE FUNCTION app.stats_new_coverage_events")
		if !found {
			t.Fatal("missing report event function")
		}
		_, body, found = strings.Cut(body, "RETURN QUERY ")
		if !found {
			t.Fatal("missing report event query")
		}
		body, _, found = strings.Cut(body, ";\nEND;")
		if !found {
			t.Fatal("missing report event query end")
		}
		body = strings.NewReplacer("target_account_id", "$1::uuid", "authorized_id", "$2::uuid", "selected_start", "$3::date", "selected_end", "$4::date", "selected_all_time", "false").Replace(body)
		tx := integrationAccountTransaction(t, adminDB, account)
		defer tx.Rollback(ctx)
		// Small fixtures can favor sequential scans. Force index paths only here
		// to verify that canonical identity is an index condition, not a join filter.
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
			t.Fatal(err)
		}
		var plan []byte
		if err := tx.QueryRow(ctx, "EXPLAIN(FORMAT JSON) "+body, account, contextID, "2025-02-03", "2026-10-02").Scan(&plan); err != nil {
			t.Fatal(err)
		}
		var documents []map[string]any
		if err := json.Unmarshal(plan, &documents); err != nil {
			t.Fatal(err)
		}
		probes := 0
		var walk func(map[string]any)
		walk = func(node map[string]any) {
			if node["Relation Name"] == "coverage_display_entities" {
				condition, _ := node["Index Cond"].(string)
				if !strings.Contains(condition, "canonical_id = qualifying.canonical_id") {
					t.Fatalf("metadata probe lost its canonical index condition: %s", condition)
				}
				probes++
			}
			children, _ := node["Plans"].([]any)
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
		walk(documents[0]["Plan"].(map[string]any))
		if probes != 1 {
			t.Fatalf("expected exactly one canonical metadata probe, got %d", probes)
		}
	})
	t.Run("ScopeIsolationValidationAndIdempotentDeletion", func(t *testing.T) {
		for _, token := range []string{otherSession, foreign} {
			if response := routeOwnerRead(handler, root+query, token); response.Code != http.StatusNotFound {
				t.Fatalf("foreign report was readable: status=%d", response.Code)
			}
		}
		for _, suffix := range []string{"&sort=name:asc", "&groupBy=day", "&pageSize=101"} {
			if response := routeOwnerRead(handler, root+query+suffix, bearer); response.Code != http.StatusBadRequest {
				t.Fatalf("invalid report arguments accepted: %s status=%d", suffix, response.Code)
			}
		}
		for _, suffix := range []string{"&bucketStart=2024-03-25&metric=all_time", "&bucketStart=2024-02-01&metric=all_time", "&bucketStart=2026-03-01&groupBy=year&metric=past_year"} {
			if response := routeOwnerRead(handler, root+"/visits"+query+suffix, bearer); response.Code != http.StatusBadRequest {
				t.Fatalf("unaligned/nonintersecting bucket accepted: %s status=%d", suffix, response.Code)
			}
		}
		deletePath := "/api/stats/report-scopes/" + scope.Id
		if response := routeMapRequest(handler, http.MethodDelete, deletePath, "", foreign); response.Code != http.StatusNoContent {
			t.Fatal("foreign deletion must be idempotent without deleting the owned scope")
		}
		readBuckets("")
		for range 2 {
			if response := routeMapRequest(handler, http.MethodDelete, deletePath, "", bearer); response.Code != http.StatusNoContent {
				t.Fatal("owned deletion must be idempotent")
			}
		}
		if response := routeOwnerRead(handler, root+query, bearer); response.Code != http.StatusNotFound {
			t.Fatal("deleted scope remained readable")
		}
	})
	t.Run("ExpiryAndRevocationFenceDatabaseReaders", func(t *testing.T) {
		expired := create("2026-10-01", "2026-10-31")
		if _, err := adminDB.Exec(ctx, `UPDATE app.stats_report_scopes SET expires_at=transaction_timestamp()-interval '1 minute'
			WHERE account_id=$1 AND id=$2`, account, expired.Id); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage?generation=%d", expired.Id, expired.DataGeneration)
		if response := routeOwnerRead(handler, path, bearer); response.Code != http.StatusNotFound {
			t.Fatal("expired report remained readable")
		}
		revoked := create("2026-10-01", "2026-10-31")
		var sessionID uuid.UUID
		if err := adminDB.QueryRow(ctx, `SELECT session_id FROM app.stats_report_scopes WHERE account_id=$1 AND id=$2`, account, revoked.Id).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		// Roll back revocation so later HTTP subtests keep their shared session.
		tx := integrationAccountTransaction(t, adminDB, account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.session_id',$1,true)`, sessionID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.sessions SET revoked_at=transaction_timestamp() WHERE id=$1`, sessionID); err != nil {
			t.Fatal(err)
		}
		var rows int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.stats_new_coverage_events($1,$2,$3,$4)`, account, sessionID, revoked.Id, revoked.DataGeneration).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("revoked SQL scope returned events: rows=%d err=%v", rows, err)
		}
	})
	t.Run("FailedReplacementRetainsAppliedVisits", func(t *testing.T) {
		tx := integrationAccountTransaction(t, adminDB, account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `UPDATE app.workout_coverage_states SET processing_state='failed' WHERE account_id=$1`, account); err != nil || tx.Commit(ctx) != nil {
			t.Fatal(err)
		}
		retained := create("2024-03-25", "2026-10-01")
		path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage?generation=%d&groupBy=year", retained.Id, retained.DataGeneration)
		response := routeOwnerRead(handler, path, bearer)
		var result generated.NewCoverageBucketList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || len(result.Items) != 3 ||
			result.Items[0].AllTime != 2 || result.Items[0].PastYear != 3 || result.Items[1].PastYear != 1 {
			t.Fatalf("failed replacement discarded visits: result=%+v status=%d err=%v", result, response.Code, err)
		}
	})
	t.Run("GenerationInvalidationAndNoMapCapability", func(t *testing.T) {
		fresh := create("2024-03-25", "2026-10-01")
		tx := integrationAccountTransaction(t, adminDB, account)
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `UPDATE app.coverage_paths SET name='Renamed Return Road',normalized_name='renamed return road' WHERE account_id=$1 AND name='Return Road'`, account); err != nil || tx.Commit(ctx) != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage?generation=%d", fresh.Id, fresh.DataGeneration)
		if response := routeOwnerRead(handler, path, bearer); response.Code != http.StatusNotFound {
			t.Fatalf("obsolete report accepted: status=%d body=%s", response.Code, response.Body.String())
		}
		var count int
		if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM app.map_selections WHERE account_id=$1`, account).Scan(&count); err != nil || count != 0 {
			t.Fatalf("Stats created Map capabilities: count=%d err=%v", count, err)
		}
	})
	t.Run("FutureEmptyBucketsAreHiddenButActualFutureWorkoutsRemain", func(t *testing.T) {
		var today time.Time
		if err := adminDB.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date`).Scan(&today); err != nil {
			t.Fatal(err)
		}
		read := func(scope generated.StatsReportScope, group string, page, size int) generated.NewCoverageBucketList {
			path := fmt.Sprintf("/api/stats/report-scopes/%s/new-coverage?generation=%d&groupBy=%s&page=%d&pageSize=%d", scope.Id, scope.DataGeneration, group, page, size)
			response := routeOwnerRead(handler, path, bearer)
			var list generated.NewCoverageBucketList
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil {
				t.Fatalf("future buckets: %d %s", response.Code, response.Body.String())
			}
			return list
		}
		annual := create(fmt.Sprintf("%d-01-01", today.Year()), fmt.Sprintf("%d-12-31", today.Year()))
		months := read(annual, "month", 1, 100)
		monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		check := integrationAccountTransaction(t, adminDB, account)
		defer check.Rollback(ctx)
		for _, item := range months.Items {
			if item.BucketStart.Time.After(monthStart) {
				var present bool
				if err := check.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.workouts WHERE account_id=$1 AND local_start_date>=$2::date AND local_start_date<($2::date+interval '1 month'))`, account, item.BucketStart.Time).Scan(&present); err != nil || !present {
					t.Fatalf("future empty month retained: %s (%v)", item.BucketStart.String(), err)
				}
			}
		}
		distant := time.Date(today.Year()+5, time.March, 12, 0, 0, 0, 0, time.UTC)
		empty := create(fmt.Sprintf("%d-01-01", distant.Year()), fmt.Sprintf("%d-12-31", distant.Year()))
		for _, group := range []string{"month", "year"} {
			list := read(empty, group, 1, 1)
			if len(list.Items) != 0 || list.Pagination.TotalItems != 0 {
				t.Fatalf("future empty %s retained: %+v", group, list)
			}
		}
		tx := integrationAccountTransaction(t, workerDB, account)
		defer tx.Rollback(ctx)
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2,$3)`, fixture.jobID, fixture.childLease, database.SupportedSchemaVersion).Scan(&source); err != nil {
			t.Fatal(err)
		}
		workout := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
          VALUES($1,$2,$3,$4,$5,$1::uuid::text,$6,'Running',$7::date+time '08:00' AT TIME ZONE 'UTC',$7::date+time '08:05' AT TIME ZONE 'UTC',$7::date,300)`, workout, account, fixture.sourceID, fixture.fileID, fixture.runningID, fixture.hash[:], distant); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		populated := create(fmt.Sprintf("%d-01-01", distant.Year()), fmt.Sprintf("%d-12-31", distant.Year()))
		for _, group := range []string{"month", "year"} {
			list := read(populated, group, 1, 1)
			if len(list.Items) != 1 || list.Pagination.TotalItems != 1 || list.Items[0].AllTime != 0 || list.Items[0].PastYear != 0 {
				t.Fatalf("actual future workout without Coverage lost for %s: %+v", group, list)
			}
			tail := read(populated, group, 2, 1)
			if len(tail.Items) != 0 || tail.Pagination.TotalItems != 1 {
				t.Fatal("future filtering was applied after pagination")
			}
		}
	})
}

func seedStatsVisits(t *testing.T, ctx context.Context, db *pgxpool.Pool, account uuid.UUID, f workoutReadFixture) {
	t.Helper()
	tx := integrationAccountTransaction(t, db, account)
	defer tx.Rollback(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var source uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'read-fixture',$2,$3)`, f.jobID, f.childLease, database.SupportedSchemaVersion).Scan(&source); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO app.coverage_region_catalog(region_id,display_name) VALUES('geofabrik:stats','Fixture Region') ON CONFLICT DO NOTHING`)
	for _, history := range []struct {
		name, kind string
		dates      []string
	}{
		{"Return Road", "road", []string{"2024-02-29", "2025-02-28", "2025-02-28", "2026-03-01", "2026-03-01", "2026-03-25", "2027-03-02"}},
		{"Leap Trail", "road", []string{"2024-02-29", "2025-03-01"}},
		{"Boundary Path", "road", []string{"2024-03-24", "2024-03-25"}},
		{"Fresh%_Way", "road", []string{"2026-10-01", "2026-10-02"}},
		{"New Park", "park", []string{"2026-10-01", "2026-10-02"}},
		{"Fixture School", "education", []string{"2026-11-03", "2026-11-20"}},
		{"", "unnamed", []string{"2026-10-01"}},
	} {
		entity := uuid.New()
		var segments []uuid.UUID
		for range 2 {
			path, segment := uuid.New(), uuid.New()
			segments = append(segments, segment)
			var name any = history.name
			tags := "{}"
			if history.kind == "park" || history.kind == "unnamed" {
				name = nil
			}
			if history.kind == "park" {
				tags = fmt.Sprintf(`{"workouts:park_id":%q,"workouts:park_name":%q,"workouts:park_normalized_name":"new park","workouts:park_kind":"local_park"}`, entity.String(), history.name)
			} else if history.kind == "education" {
				tags = fmt.Sprintf(`{"workouts:education_id":%q,"workouts:education_name":%q}`, entity.String(), history.name)
			}
			exec(`INSERT INTO app.coverage_paths(account_id,logical_path_id,name,normalized_name,locality_relation_id,locality_relation_version,locality_name,broad_class)
				VALUES($1,$2,$3,lower($3),101,1,'Fixture Town','road')`, account, path, name)
			exec(`INSERT INTO app.path_segments(account_id,region_id,generation_id,physical_segment_id,logical_path_id,
				derivation_version,locality_relation_id,source_way_id,source_way_version,name,normalized_name,highway,broad_class,tags,segment_meters,geom)
				VALUES($1,'geofabrik:stats',1,$2,$3,4,101,1,1,$4,lower($4),'residential','road',$5::jsonb,30,
				ST_GeomFromText('LINESTRING(-105 40,-104.9999 40.0001)',4326))`, account, segment, path, name, tags)
		}
		if history.kind == "park" {
			exec(`INSERT INTO app.coverage_parks(account_id,park_id,name,normalized_name,park_kind,locality_relation_id,locality_relation_version,locality_name)
				VALUES($1,$2,'New Park','new park','local_park',101,1,'Fixture Town')`, account, entity)
		}
		for _, date := range history.dates {
			workout := uuid.Must(uuid.NewV7())
			exec(`INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,
				provider_label,started_at,ended_at,local_start_date,provider_duration)
				VALUES($1,$2,$3,$4,$5,$1::uuid::text,$6,'Running',$7::date+time '08:00' AT TIME ZONE 'UTC',
				$7::date+time '08:05' AT TIME ZONE 'UTC',$7::date,300)`, workout, account, f.sourceID, f.fileID, f.runningID, f.hash[:], date)
			exec(`INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
				VALUES($1,$2,0,$3::date+time '08:00' AT TIME ZONE 'UTC',40,-105),
				($1,$2,1,$3::date+time '08:01' AT TIME ZONE 'UTC',40.0001,-104.9999)`, account, workout, date)
			exec(`INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
				maximum_longitude,maximum_latitude,has_complete_altitude,route)
				VALUES($1,$2,2,-105,40,-104.9999,40.0001,false,
				ST_GeomFromText('MULTILINESTRING((-105 40,-104.9999 40.0001))',4326))`, account, workout)
			exec(`INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,
				readiness_state,map_data_ready_at,processing_state,applied_route_input_revision,applied_route_input_sha256,
				applied_rules_version,applied_sampling_version,applied_path_policy_version,applied_generations,processing_finished_at)
				VALUES($1,$2,1,$3,'map_data_ready',transaction_timestamp(),'current',1,$3,'coverage-experimental-v1',
				'coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',
				'[{"regionId":"geofabrik:stats","generation":1}]',transaction_timestamp())`, account, workout, f.hash[:])
			for order, segment := range segments {
				exec(`INSERT INTO app.workout_segment_matches(account_id,workout_id,physical_segment_id,region_id,generation_id,
					logical_path_id,first_traversed_at,first_route_order,covered_meters,geom)
					SELECT account_id,$2,physical_segment_id,region_id,generation_id,logical_path_id,
					$4::date+time '08:00' AT TIME ZONE 'UTC',$5,30,ST_Multi(geom)
					FROM app.path_segments WHERE account_id=$1 AND physical_segment_id=$3`, account, workout, segment, date, order)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
