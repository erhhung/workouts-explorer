package workouttimezone

import (
	"testing"
	"time"
)

func TestMatchesRecordedOffset(t *testing.T) {
	summer := time.Date(2025, time.July, 1, 18, 0, 0, 0, time.UTC)
	winter := time.Date(2025, time.January, 1, 18, 0, 0, 0, time.UTC)
	minusSix, minusSeven := -360, -420
	if !MatchesRecordedOffset("America/Denver", summer, &minusSix) {
		t.Fatal("expected Denver summer offset to match")
	}
	if !MatchesRecordedOffset("America/Denver", winter, &minusSeven) {
		t.Fatal("expected Denver winter offset to match")
	}
	if MatchesRecordedOffset("America/Denver", summer, &minusSeven) {
		t.Fatal("accepted a conflicting recorded offset")
	}
	if MatchesRecordedOffset("Not/A_Zone", summer, nil) {
		t.Fatal("accepted an invalid timezone")
	}
}
