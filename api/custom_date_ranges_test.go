package api

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
)

func TestCustomDateRangeHistory(t *testing.T) {
	const a = "2022-01-01/2026-11-01"
	const b = "2024-02-29/2024-03-03"
	const c = "2026-01-01/2026-01-31"
	const d = "2026-02-01/2026-02-28"
	for _, test := range []struct {
		name    string
		history []string
		patch   string
		want    []string
	}{
		{"new range", nil, `{"dateRange":"` + a + `"}`, []string{a}},
		{"most recent first", []string{a, b, c}, `{"dateRange":"` + d + `"}`, []string{d, a, b}},
		{"reuse moves to front", []string{a, b, c}, `{"dateRange":"` + b + `"}`, []string{b, a, c}},
		{"current range is unique", []string{a, b, c}, `{"dateRange":"` + a + `"}`, []string{a, b, c}},
		{"shortcut does not enter history", []string{a, b}, `{"dateRange":"last7Days"}`, []string{a, b}},
		{"clear preserves history", []string{a, b}, `{"dateRange":null}`, []string{a, b}},
		{"unrelated patch preserves order", []string{a, b}, `{"theme":"light"}`, []string{a, b}},
		{"shrink evicts oldest", []string{a, b, c}, `{"customDateRangeLimit":1}`, []string{a}},
		{"grow retains existing", []string{a, b, c}, `{"customDateRangeLimit":5}`, []string{a, b, c}},
		{"combined range and limit", []string{a, b, c}, `{"customDateRangeLimit":2,"dateRange":"` + c + `"}`, []string{c, a}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefs := generated.Preferences{CustomDateRangeLimit: 3, RecentCustomDateRanges: test.history}
			var patch generated.PreferencesPatch
			if err := json.Unmarshal([]byte(test.patch), &patch); err != nil {
				t.Fatal(err)
			}
			applyPreferencesPatch(&prefs, patch)
			updateCustomDateRangeHistory(&prefs, patch)
			if !slices.Equal(prefs.RecentCustomDateRanges, test.want) {
				t.Fatalf("history=%v, want %v", prefs.RecentCustomDateRanges, test.want)
			}
		})
	}
}

func TestCustomDateRangeLimitValidation(t *testing.T) {
	server := &Server{}
	for _, limit := range []int{-1, 0, 1, 2, 3, 4, 5, 6} {
		prefs := generated.Preferences{Theme: "dark", Units: "metric", Timezone: "UTC", FirstWeekday: "monday", ClockFormat: "24h", PageSize: 25, CustomDateRangeLimit: limit}
		field, _ := server.validatePreferences(prefs)
		if limit >= 1 && limit <= 5 {
			if field != "" {
				t.Fatalf("valid limit %d rejected: %s", limit, field)
			}
		} else if field != "customDateRangeLimit" {
			t.Fatalf("invalid limit %d: field=%s", limit, field)
		}
	}
}
