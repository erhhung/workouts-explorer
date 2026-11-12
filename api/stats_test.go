package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/jackc/pgx/v5"
)

func TestStatsRequestContracts(t *testing.T) {
	handler := testHandler(t)
	root := "/api/stats/report-scopes/018F8E7D7A4C7C03A1C23D4E5F607182/new-coverage"
	for _, test := range []struct {
		path string
		code int
	}{
		{root + "?generation=1", http.StatusUnauthorized},
		{root + "?generation=1&groupBy=year&pageSize=100&sort=pastYear:desc", http.StatusUnauthorized},
		{root, http.StatusBadRequest},
		{root + "?generation=0", http.StatusBadRequest},
		{root + "?generation=1&groupBy=day", http.StatusBadRequest},
		{root + "?generation=1&pageSize=101", http.StatusBadRequest},
		{root + "?generation=1&page=0", http.StatusBadRequest},
		{root + "/visits?generation=1&bucketStart=2026-01-01&metric=past_year", http.StatusUnauthorized},
		{root + "/visits?generation=1&bucketStart=2026-01-01&metric=all_time&groupBy=year", http.StatusUnauthorized},
		{root + "/visits?generation=1&metric=all_time", http.StatusBadRequest},
		{root + "/visits?generation=1&bucketStart=2026-01-01&metric=unknown", http.StatusBadRequest},
		{root + "/visits?generation=1&bucketStart=2026-01-01&metric=all_time&search=" + strings.Repeat("x", 257), http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.code {
			t.Errorf("%s: status=%d want=%d body=%s", test.path, response.Code, test.code, response.Body.String())
		}
	}
}

func TestStatsSortRejectsSQLAndKeepsMissingDatesLast(t *testing.T) {
	fields := map[string]string{"previousVisit": "previous_visit", "newVisit": "new_visit"}
	for _, raw := range []string{"previousVisit:asc", "previousVisit:desc"} {
		order, err := statsOrder(&raw, fields, "newVisit", "entity_id")
		if err != nil || !strings.Contains(order, " NULLS LAST,entity_id") {
			t.Fatalf("nullable order=%q err=%v", order, err)
		}
	}
	for _, raw := range []string{"name;DROP TABLE app.accounts:asc", "newVisit:desc;--", "newVisit:asc,name:desc", "newVisit", "newVisit:ASC", "unknown:desc"} {
		if _, err := statsOrder(&raw, fields, "newVisit", "entity_id"); err == nil {
			t.Errorf("accepted sort injection or unknown order %q", raw)
		}
	}
}

func TestStatsPartialBucketsAndCalendarBoundaries(t *testing.T) {
	var scope generated.StatsReportScope
	scope.Range.StartDate.Time, _ = time.Parse("2006-01-02", "2024-03-25")
	scope.Range.EndDate.Time, _ = time.Parse("2006-01-02", "2026-10-01")
	for _, test := range []struct {
		date  string
		group generated.StatsGrouping
		valid bool
	}{
		{"2024-03-01", generated.Month, true}, {"2026-10-01", generated.Month, true},
		{"2024-02-01", generated.Month, false}, {"2026-11-01", generated.Month, false},
		{"2024-03-25", generated.Month, false}, {"2024-01-01", generated.Year, true},
		{"2026-01-01", generated.Year, true}, {"2023-01-01", generated.Year, false},
		{"2024-03-01", generated.Year, false},
	} {
		date, _ := time.Parse("2006-01-02", test.date)
		if validStatsBucket(date, test.group, scope) != test.valid {
			t.Errorf("bucket %s/%s validity differs", test.date, test.group)
		}
	}
	leap, _ := time.Parse("2006-01-02", "2024-02-01")
	if got := statsBucketEnd(leap, generated.Month).Format("2006-01-02"); got != "2024-02-29" {
		t.Fatalf("leap-month end=%s", got)
	}
}

func TestStatsErrorsDoNotDiscloseDatabaseDetailsOrBecomeZero(t *testing.T) {
	for _, test := range []struct {
		err  error
		code int
	}{
		{pgx.ErrNoRows, http.StatusNotFound},
		{errStatsResponseLimit, http.StatusUnprocessableEntity},
		{errors.New("postgres://secret@private-db/private with sensitive names"), http.StatusServiceUnavailable},
	} {
		response := httptest.NewRecorder()
		writeStatsError(response, httptest.NewRequest(http.MethodGet, "/api/stats", nil), test.err)
		if response.Code != test.code || strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "private-db") || strings.Contains(response.Body.String(), "\"items\"") {
			t.Fatalf("unsafe or inaccurate report error: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
