package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func TestResolveRangeCalendarRules(t *testing.T) {
	date := func(value string) *openapi_types.Date {
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			t.Fatal(err)
		}
		return &openapi_types.Date{Time: parsed}
	}
	shortcut := func(value generated.DateRangeEnum) *generated.DateRangeEnum { return &value }
	tests := []struct {
		name      string
		input     rangeInput
		prefs     readPreferences
		now       time.Time
		wantStart string
		wantEnd   string
		wantZone  string
	}{
		{"monday week across DST", rangeInput{shortcut: shortcut(generated.ThisWeek)}, readPreferences{"America/Denver", "monday", 25}, time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC), "2026-03-09", "2026-03-15", "America/Denver"},
		{"spring transition before local midnight", rangeInput{shortcut: shortcut(generated.Last7Days)}, readPreferences{"America/New_York", "monday", 25}, time.Date(2026, 3, 8, 4, 59, 0, 0, time.UTC), "2026-03-01", "2026-03-07", "America/New_York"},
		{"spring transition after local midnight", rangeInput{shortcut: shortcut(generated.Last7Days)}, readPreferences{"America/New_York", "monday", 25}, time.Date(2026, 3, 8, 5, 1, 0, 0, time.UTC), "2026-03-02", "2026-03-08", "America/New_York"},
		{"fall transition before local midnight", rangeInput{shortcut: shortcut(generated.Last7Days)}, readPreferences{"America/New_York", "monday", 25}, time.Date(2026, 11, 1, 3, 59, 0, 0, time.UTC), "2026-10-25", "2026-10-31", "America/New_York"},
		{"fall transition after local midnight", rangeInput{shortcut: shortcut(generated.Last7Days)}, readPreferences{"America/New_York", "monday", 25}, time.Date(2026, 11, 1, 4, 1, 0, 0, time.UTC), "2026-10-26", "2026-11-01", "America/New_York"},
		{"unusual offset local midnight", rangeInput{shortcut: shortcut(generated.Last7Days)}, readPreferences{"Pacific/Chatham", "monday", 25}, time.Date(2026, 8, 5, 11, 16, 0, 0, time.UTC), "2026-07-31", "2026-08-06", "Pacific/Chatham"},
		{"sunday last week across year", rangeInput{shortcut: shortcut(generated.LastWeek)}, readPreferences{"Pacific/Auckland", "sunday", 25}, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "2025-12-21", "2025-12-27", "Pacific/Auckland"},
		{"last seven is inclusive", rangeInput{shortcut: shortcut(generated.Last7Days)}, readPreferences{"UTC", "monday", 25}, time.Date(2026, 8, 5, 23, 0, 0, 0, time.UTC), "2026-07-30", "2026-08-05", "UTC"},
		{"last month leap boundary", rangeInput{shortcut: shortcut(generated.LastMonth)}, readPreferences{"UTC", "monday", 25}, time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC), "2024-02-01", "2024-02-29", "UTC"},
		{"explicit ignores override", rangeInput{start: date("2026-03-08"), end: date("2026-03-09"), timezone: stringPointer("Not/AZone")}, readPreferences{"America/New_York", "monday", 25}, time.Time{}, "2026-03-08", "2026-03-09", "America/New_York"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, field, err := resolveRange(test.input, test.prefs, test.now)
			if err != nil || field != "" || got.start.Format("2006-01-02") != test.wantStart || got.end.Format("2006-01-02") != test.wantEnd || got.timezone != test.wantZone {
				t.Fatalf("range=%#v field=%q err=%v", got, field, err)
			}
		})
	}
}

func TestResolveRangeValidation(t *testing.T) {
	parsed := openapi_types.Date{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	earlier := openapi_types.Date{Time: parsed.AddDate(0, 0, -1)}
	shortcut := generated.ThisMonth
	tests := []struct {
		name, field string
		input       rangeInput
	}{
		{"missing selector", "dateRangeEnum", rangeInput{}},
		{"one explicit date", "startDate", rangeInput{start: &parsed}},
		{"reversed explicit dates", "endDate", rangeInput{start: &parsed, end: &earlier}},
		{"mixed selectors", "dateRangeEnum", rangeInput{start: &parsed, end: &parsed, shortcut: &shortcut}},
		{"invalid timezone", "tz", rangeInput{shortcut: &shortcut, timezone: stringPointer("Mars/Olympus")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, field, err := resolveRange(test.input, readPreferences{"UTC", "monday", 25}, time.Now())
			if err == nil || field != test.field {
				t.Fatalf("field=%q err=%v", field, err)
			}
		})
	}
}

func TestWorkoutSortValidation(t *testing.T) {
	valid := []string{"date:desc", "distance:asc", "heartRate:desc"}
	got, err := parseWorkoutSort(&valid)
	if err != nil || len(got) != 3 {
		t.Fatalf("valid sort: %#v %v", got, err)
	}
	for _, value := range [][]string{{"unknown:asc"}, {"date:up"}, {"date:asc", "date:desc"}, {"date"}, {"date:asc", "type:asc", "duration:asc", "distance:asc", "pace:asc", "calories:asc", "heartRate:asc", "elevationGain:asc", "date:desc"}} {
		if _, err := parseWorkoutSort(&value); err == nil {
			t.Fatalf("accepted invalid sort %v", value)
		}
	}
}

func TestWorkoutDisplayTimezoneFallback(t *testing.T) {
	zone, west, east := "America/Denver", -390, 345
	if got := displayTimezone(&zone, &west); got == nil || *got != zone {
		t.Fatalf("named timezone=%#v", got)
	}
	if got := displayTimezone(nil, &west); got == nil || *got != "UTC-06:30" {
		t.Fatalf("west offset=%#v", got)
	}
	if got := displayTimezone(nil, &east); got == nil || *got != "UTC+05:45" {
		t.Fatalf("east offset=%#v", got)
	}
	if got := displayTimezone(nil, nil); got != nil {
		t.Fatalf("missing offset=%#v", got)
	}
}

func TestWorkoutExportFilename(t *testing.T) {
	tests := map[string]string{
		"Outdoor Run":                           "2026-08-05-outdoor-run.json",
		"  Hike / Walk  ":                       "2026-08-05-hike-walk.json",
		"CAFÉ":                                  "2026-08-05-caf.json",
		"雪山":                                    "2026-08-05-workout.json",
		"A deliberately very long workout type": "2026-08-05-a-deliberately-very-long-workout-type.json",
	}
	for workoutType, want := range tests {
		if got := workoutExportFilename("2026-08-05", workoutType, "json"); got != want {
			t.Errorf("type %q filename=%q want=%q", workoutType, got, want)
		}
	}
}

func TestWorkoutReadRoutesRequireAuthentication(t *testing.T) {
	handler := testHandler(t)
	for _, path := range []string{"/api/workouts?dateRangeEnum=thisMonth", "/api/workouts/018F8E7D7A4C7C03A1C23D4E5F607182/provenance", "/api/workouts/018F8E7D7A4C7C03A1C23D4E5F607182/route/points", "/api/workout-types", "/api/summary?dateRangeEnum=thisMonth"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestWorkoutDeletionRequiresAuthentication(t *testing.T) {
	handler := testHandler(t)
	for _, path := range []string{"/api/workouts/018F8E7D7A4C7C03A1C23D4E5F607182", "/api/workouts?startDate=2026-08-01&endDate=2026-08-31"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodDelete, path, nil)
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestWorkoutReadRouterRejectsContractViolations(t *testing.T) {
	handler := testHandler(t)
	for _, path := range []string{
		"/api/workouts?dateRangeEnum=unknown",
		"/api/workouts?startDate=not-a-date&endDate=2026-01-01",
		"/api/workouts?dateRangeEnum=thisMonth&page=0",
		"/api/workouts?dateRangeEnum=thisMonth&sort=date:sideways",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
