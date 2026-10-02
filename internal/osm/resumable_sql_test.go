package osm

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestResumableSQLUsesOneBatchPerInvocationAndLoggedState(t *testing.T) {
	files := []string{
		"derive-batched.sql", "clip-localities-candidates.sql", "clip-localities-replacements.sql", "clip-localities-apply.sql",
		"clip-localities-residual.sql", "attribute-parks-batched.sql", "attribute-education-batched.sql",
		"attribute-slivers-batched.sql", "identity-segments-batched.sql", "identity-edges-batched.sql",
		"identity-proximity-batched.sql", "identity-propagate-batched.sql", "identity-label-overrides-batched.sql", "identity-rewrite-batched.sql",
	}
	for _, name := range files {
		contents, err := os.ReadFile("../../osm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sql := strings.ToUpper(string(contents))
		if strings.Contains(sql, "END LOOP") || strings.Contains(sql, "UNLOGGED") {
			t.Errorf("%s contains an internal loop or crash-unsafe staging table", name)
		}
		if !strings.Contains(sql, "CHECKPOINT_GENERATION_STAGE") && !strings.Contains(sql, "COMPLETE_GENERATION_STAGE") {
			t.Errorf("%s does not persist a stage checkpoint", name)
		}
		for _, unsupported := range []string{"MAX(SEGMENT_ID)", "MAX(WINNER_ID)", "MAX(ROOT_SEGMENT_ID)"} {
			if strings.Contains(sql, unsupported) {
				t.Errorf("%s uses unsupported PostgreSQL UUID aggregate %s", name, unsupported)
			}
		}
	}
}

func TestOperationalBatchSizeOverridesMatchSQL(t *testing.T) {
	tests := []struct {
		key         string
		file        string
		declaration string
	}{
		{"clip-apply", "clip-localities-apply.sql", "batch_size constant integer := 18000;"},
		{"clip-residual", "clip-localities-residual.sql", "batch_size constant integer := 10000;"},
		{"identity-propagate/hook", "identity-propagate-batched.sql", "hook_batch_size constant integer := 100000;"},
	}
	for _, test := range tests {
		contents, err := os.ReadFile("../../osm/" + test.file)
		if err != nil {
			t.Fatal(err)
		}
		size := batchSizeOverrides[test.key]
		if size == 0 || !strings.Contains(test.declaration, fmt.Sprint(size)) {
			t.Errorf("override %s=%d does not match %q", test.key, size, test.declaration)
		}
		if !strings.Contains(string(contents), test.declaration) {
			t.Errorf("%s does not declare %q", test.file, test.declaration)
		}
	}
}

func TestIdentityComponentBatchSizesMatchSQL(t *testing.T) {
	contents, err := os.ReadFile("../../osm/identity-components.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range []string{
		"filter_batch_size constant integer := 750000;",
		"component_batch_size constant integer := 2000000;",
	} {
		if !strings.Contains(string(contents), declaration) {
			t.Errorf("identity-components.sql does not declare %q", declaration)
		}
	}
}

func TestIdentityPropagationPersistsPerPassBatchProgress(t *testing.T) {
	contents, err := os.ReadFile("../../osm/identity-propagate-batched.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, field := range []string{"'batch_index'", "'batch_total'"} {
		if !strings.Contains(sql, field) {
			t.Errorf("identity-propagate-batched.sql does not persist %s", field)
		}
	}
	reset := "IF accumulated > 0 THEN\n        SELECT count(*) INTO phase_rows FROM attribution_identity_components;"
	if !strings.Contains(sql, reset) {
		t.Error("identity-propagate-batched.sql does not count the next compression pass before resetting its progress")
	}
}

func TestDocumentedRepeatedBatchesPersistProgressMetadata(t *testing.T) {
	type repeatedBatch struct {
		file        string
		marker      string
		sizePattern string
	}
	batches := map[string]repeatedBatch{
		"derive/node-counts":                   {"derive-batched.sql", "phase = 'node-counts'", `(?i)node_count_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"derive/segments":                      {"derive-batched.sql", "phase = 'segments'", `(?i)segment_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"clip-candidates":                      {"clip-localities-candidates.sql", "stage = 'clip-candidates'", `(?is)CREATE\s+TEMP\s+TABLE\s+clip_candidate_keys.*?LIMIT\s+(\d+)`},
		"clip-replacements":                    {"clip-localities-replacements.sql", "stage = 'clip-replacements'", `(?is)CREATE\s+TEMP\s+TABLE\s+clip_replacement_keys.*?LIMIT\s+(\d+)`},
		"clip-apply":                           {"clip-localities-apply.sql", "stage = 'clip-apply'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"clip-residual":                        {"clip-localities-residual.sql", "stage = 'clip-residual'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"attribute-parks-tags":                 {"attribute-parks-batched.sql", "stage = 'attribute-parks-tags'", `(?is)CREATE\s+TEMP\s+TABLE\s+area_batch_keys.*?LIMIT\s+(\d+)`},
		"attribute-education":                  {"attribute-education-batched.sql", "stage = 'attribute-education'", `(?is)CREATE\s+TEMP\s+TABLE\s+area_batch_keys.*?LIMIT\s+(\d+)`},
		"attribute-slivers/discover":           {"attribute-slivers-batched.sql", "phase = 'discover'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"attribute-slivers/apply":              {"attribute-slivers-batched.sql", "phase <> 'apply'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-segments":                    {"identity-segments-batched.sql", "stage = 'identity-segments'", `(?is)CREATE\s+TEMP\s+TABLE\s+identity_segment_keys.*?LIMIT\s+(\d+)`},
		"identity-edges/orientation-1":         {"identity-edges-batched.sql", "orientation = 1", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-edges/orientation-2":         {"identity-edges-batched.sql", "orientation = 2", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-edges/orientation-4":         {"identity-edges-batched.sql", "orientation = 4", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-proximity/roads":             {"identity-proximity-batched.sql", "phase = 'roads'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-proximity/candidates":        {"identity-proximity-batched.sql", "phase = 'candidates'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-proximity/edges":             {"identity-proximity-batched.sql", "phase = 'edges'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-components/filter-edges":     {"identity-components.sql", "phase = 'filter-edges'", `(?i)filter_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-components/components-left":  {"identity-components.sql", "phase = 'components-left'", `(?i)component_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-components/components-right": {"identity-components.sql", "'components-right'", `(?i)component_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-propagate/compress":          {"identity-propagate-batched.sql", "phase = 'compress'", `(?i)compress_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-propagate/hook":              {"identity-propagate-batched.sql", "phase = 'hook'", `(?i)hook_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-propagate/assign":            {"identity-propagate-batched.sql", "phase = 'assign'", `(?i)assign_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-label-overrides/labels":      {"identity-label-overrides-batched.sql", "phase = 'labels'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"identity-rewrite":                     {"identity-rewrite-batched.sql", "stage = 'identity-rewrite'", `(?i)batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"validate/logical-paths":               {"validate.sql", "phase = 'logical-paths'", `(?i)logical_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
		"validate/locality-residuals":          {"validate.sql", "phase = 'locality-residuals'", `(?i)residual_batch_size\s+constant\s+integer\s*:=\s*(\d+)`},
	}

	documentation, err := os.ReadFile("../../docs/osm-resumable-rebuild.md")
	if err != nil {
		t.Fatal(err)
	}
	repeatedRow := regexp.MustCompile("(?m)^\\|\\s*\\d+\\s*\\|\\s*`([^`]+)`\\s*\\|\\s*Repeated,\\s*([0-9,]+)")
	documented := repeatedRow.FindAllStringSubmatch(string(documentation), -1)
	if len(documented) != len(batches) {
		t.Fatalf("documented %d repeated batches, test maps %d", len(documented), len(batches))
	}
	documentedSizes := make(map[string]int, len(documented))
	for _, match := range documented {
		if _, ok := batches[match[1]]; !ok {
			t.Errorf("documented repeated batch %q has no SQL metadata contract test", match[1])
		}
		size, parseErr := strconv.Atoi(strings.ReplaceAll(match[2], ",", ""))
		if parseErr != nil {
			t.Fatalf("parse documented batch size for %s: %v", match[1], parseErr)
		}
		documentedSizes[match[1]] = size
	}

	files := make(map[string]string)
	for name, batch := range batches {
		sql, ok := files[batch.file]
		if !ok {
			contents, readErr := os.ReadFile("../../osm/" + batch.file)
			if readErr != nil {
				t.Fatal(readErr)
			}
			sql = string(contents)
			files[batch.file] = sql
		}
		if !strings.Contains(sql, batch.marker) {
			t.Errorf("%s maps to %s without phase marker %q", name, batch.file, batch.marker)
		}
		sizeMatch := regexp.MustCompile(batch.sizePattern).FindStringSubmatch(sql)
		if len(sizeMatch) != 2 {
			t.Errorf("%s (%s) does not expose its SQL batch size", name, batch.file)
		} else {
			actual, parseErr := strconv.Atoi(sizeMatch[1])
			if parseErr != nil {
				t.Fatalf("parse SQL batch size for %s: %v", name, parseErr)
			}
			if expected := documentedSizes[name]; actual != expected {
				t.Errorf("%s batch size=%d in %s, documented as %d", name, actual, batch.file, expected)
			}
		}

		compact := strings.Join(strings.Fields(sql), "")
		for _, contract := range []string{
			"cursor->>'batch_index'",
			"cursor->>'batch_total'",
			"batch_indexISNULLORbatch_totalISNULL",
			"'batch_index',batch_index+1,'batch_total',batch_total",
		} {
			if !strings.Contains(compact, contract) {
				t.Errorf("%s (%s) lacks metadata contract %q", name, batch.file, contract)
			}
		}
		if !regexp.MustCompile(`batch_total:=\([^;]+\)/[^;]+\+1;`).MatchString(compact) {
			t.Errorf("%s (%s) does not derive a terminal-inclusive total", name, batch.file)
		}
	}
}

func TestRepeatedPhaseTransitionsResetProgress(t *testing.T) {
	tests := []struct {
		file          string
		minimumResets int
	}{
		{"derive-batched.sql", 2},
		{"attribute-slivers-batched.sql", 1},
		{"identity-edges-batched.sql", 1},
		{"identity-proximity-batched.sql", 2},
		{"identity-components.sql", 3},
		{"identity-propagate-batched.sql", 4},
		{"identity-label-overrides-batched.sql", 1},
		{"validate.sql", 2},
	}
	for _, test := range tests {
		contents, err := os.ReadFile("../../osm/" + test.file)
		if err != nil {
			t.Fatal(err)
		}
		compact := strings.Join(strings.Fields(string(contents)), "")
		reset := "'batch_index',1,'batch_total',"
		if count := strings.Count(compact, reset); count < test.minimumResets {
			t.Errorf("%s has %d repeated-phase progress resets, want at least %d", test.file, count, test.minimumResets)
		}
	}
}
