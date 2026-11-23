package osmcontroller

import (
	"strings"
	"testing"
)

func TestFailureSummaryRetainsCauseWithoutTaskPrefix(t *testing.T) {
	detail := "Failed to download https://download.geofabrik.de/europe/norway/svalbard-janmayen/svalbard-janmayen-latest.osm.pbf | unexpected HTTP status 503"
	if got := failureDetailSummary(detail); got != detail {
		t.Fatalf("summary=%q, want %q", got, detail)
	}
	if got := failureDetailSummary("connection reset"); got != "connection reset" {
		t.Fatal("generic download failure gained task context", got)
	}
	if got := failureDetailSummary("psql output: ERROR: fixture constraint"); got != "ERROR: fixture constraint" {
		t.Fatal("SQL failure formatting changed", got)
	}
	if got := failureDetailSummary(strings.Repeat("x", 600)); len([]rune(got)) != 256 {
		t.Fatal("failure summary no longer bounded", len([]rune(got)))
	}
}
