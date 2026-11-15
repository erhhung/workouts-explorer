package api

import (
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
)

func TestConsistencyCalendarBuckets(t *testing.T) {
	for _, test := range []struct{ date, weekday, group, want string }{
		{"2026-03-08", "monday", "week", "2026-03-02"},
		{"2026-03-08", "sunday", "week", "2026-03-08"},
		{"2026-01-01", "monday", "week", "2025-12-29"},
		{"2024-02-29", "sunday", "month", "2024-02-01"},
	} {
		date, _ := time.Parse(time.DateOnly, test.date)
		got := consistencyBucketStart(date, generated.ConsistencyGrouping(test.group), test.weekday)
		if got.Format(time.DateOnly) != test.want {
			t.Fatalf("%#v got %s", test, got)
		}
	}
	start := time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, weeks := range []int{maximumConsistencyBuckets - 1, maximumConsistencyBuckets, maximumConsistencyBuckets + 1} {
		_, _, err := consistencyBucketBounds(resolvedRange{start: start, end: start.AddDate(0, 0, weeks*7)}, generated.ConsistencyGroupingWeek, "monday")
		if (err == nil) != (weeks < maximumConsistencyBuckets) {
			t.Fatalf("%d weeks: %v", weeks, err)
		}
	}
}

func TestConsistencyStopsAtLocalToday(t *testing.T) {
	for _, test := range []struct{ now, zone, want string }{
		{"2026-11-16T00:30:00Z", "UTC", "2026-11-16"},
		{"2026-11-16T00:30:00Z", "America/Los_Angeles", "2026-11-15"},
		{"2026-12-01T00:30:00Z", "America/Los_Angeles", "2026-11-30"},
		{"2026-12-01T00:30:00Z", "Asia/Tokyo", "2026-12-01"},
	} {
		now, _ := time.Parse(time.RFC3339, test.now)
		start, _ := time.Parse(time.DateOnly, "2026-01-01")
		end, _ := time.Parse(time.DateOnly, "2026-12-31")
		requested := resolvedRange{start: start, end: end, timezone: test.zone}
		visible, ok, err := consistencyVisibleRange(requested, now)
		if err != nil || !ok || visible.end.Format(time.DateOnly) != test.want || requested.end != end {
			t.Fatalf("%#v got %#v ok=%v err=%v", test, visible, ok, err)
		}
		for _, group := range []generated.ConsistencyGrouping{generated.ConsistencyGroupingWeek, generated.ConsistencyGroupingMonth} {
			_, last, err := consistencyBucketBounds(visible, group, "monday")
			want := consistencyBucketStart(visible.end, group, "monday")
			if err != nil || !last.Equal(want) {
				t.Fatalf("wrong final period %v %v", last, err)
			}
		}
	}
	start, _ := time.Parse(time.DateOnly, "2027-01-01")
	end := start.AddDate(0, 0, 7)
	now, _ := time.Parse(time.DateOnly, "2026-11-13")
	if _, ok, err := consistencyVisibleRange(resolvedRange{start: start, end: end, timezone: "UTC"}, now); err != nil || ok {
		t.Fatalf("future-only range ok=%v err=%v", ok, err)
	}
}
