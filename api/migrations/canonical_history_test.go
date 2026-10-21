package migrations

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestCanonicalCoverageSQL(t *testing.T) {
	raw, err := Files.ReadFile("027_coverage_recovery_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{"legacy_", "initial_version", "goose_db_version", "NO TRANSACTION", "Code generated"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("canonical migration contains %q", forbidden)
		}
	}
	if strings.Count(source, "SET schema_version = 27,") != 1 {
		t.Fatal("expected one final canonical schema update")
	}
	for _, match := range regexp.MustCompile(`(?i)schema_version\s*=\s*(\d+)`).FindAllStringSubmatch(source, -1) {
		if match[1] != "27" {
			t.Fatalf("noncanonical schema assignment: %s", match[0])
		}
	}
	phases := []string{"route-counts", "auto-retries", "runtime-capability", "diagnostic-limits", "retry-presentation", "canonical-metadata"}
	position := 0
	for _, phase := range phases {
		begin := "-- coverage-contract:" + phase + ":begin\n"
		end := "-- coverage-contract:" + phase + ":end"
		start, stop := strings.Index(source, begin), strings.Index(source, end)
		if strings.Count(source, begin) != 1 || strings.Count(source, end) != 1 || start < position || stop <= start {
			t.Fatalf("invalid semantic checkpoint %s", phase)
		}
		body := source[start+len(begin) : stop]
		if strings.Contains(body, "+goose") || (phase != "canonical-metadata" && strings.Contains(body, "UPDATE app.schema_metadata")) {
			t.Fatalf("checkpoint %s contains migration control statements", phase)
		}
		position = stop + len(end)
	}
	if strings.Count(source, "-- +goose StatementBegin") != 2 || strings.Count(source, "-- +goose StatementEnd") != 2 {
		t.Fatal("expected one transactional Up group and one forward-only Down group")
	}
}

func TestCanonicalActivePrefix(t *testing.T) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	version := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version++
		prefix, _, _ := strings.Cut(entry.Name(), "_")
		got, err := strconv.Atoi(prefix)
		if err != nil || got != version {
			t.Fatalf("active migration %q breaks contiguous history", entry.Name())
		}
	}
	if version < 27 {
		t.Fatal("canonical coverage migration missing")
	}
}
