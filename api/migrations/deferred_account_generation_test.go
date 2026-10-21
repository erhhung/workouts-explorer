package migrations

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func foldedGenerationSection(t *testing.T) string {
	t.Helper()
	raw, err := Files.ReadFile("028_ingest_coverage_pipeline.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(raw), "-- Keep source/job/ownership fencing intact without holding the map generation")
	if !found {
		t.Fatal("canonical 028 lacks folded generation section")
	}
	section, _, _ = strings.Cut(section, "-- +goose Down")
	return section
}

func TestDeferredGenerationMigrationPreservesTriggerScopes(t *testing.T) {
	source := foldedGenerationSection(t)
	triggers := map[string]string{
		"workout_routes_data_generation_after_write":            "009_raw_route_map.sql",
		"workouts_data_generation_after_write":                  "009_raw_route_map.sql",
		"workout_types_data_generation_after_update":            "009_raw_route_map.sql",
		"workout_coverage_states_data_generation_after_current": "017_coverage_reconciliation.sql",
	}
	if strings.Count(source, "CREATE CONSTRAINT TRIGGER ") != len(triggers) || strings.Count(source, "DROP TRIGGER ") != len(triggers) {
		t.Fatal("migration must replace exactly the four generation triggers")
	}
	for name, file := range triggers {
		t.Run(name, func(t *testing.T) {
			prior, err := Files.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			pattern := regexp.MustCompile(`(?s)CREATE (?:CONSTRAINT )?TRIGGER ` + name + `\s.*?;`)
			oldDefinition := pattern.FindString(string(prior))
			newDefinition := pattern.FindString(source)
			if oldDefinition == "" || newDefinition == "" {
				t.Fatal("missing old or replacement trigger")
			}
			newDefinition = strings.Replace(newDefinition, "CREATE CONSTRAINT TRIGGER", "CREATE TRIGGER", 1)
			newDefinition = strings.Replace(newDefinition, "DEFERRABLE INITIALLY DEFERRED ", "", 1)
			// Parentheses and semicolons are retained: only layout may differ.
			compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
			if compact(newDefinition) != compact(oldDefinition) {
				t.Fatalf("event/column/WHEN/function scope changed:\nold: %s\nnew: %s", oldDefinition, newDefinition)
			}
		})
	}
	for _, forbidden := range []string{"DROP FUNCTION", "CREATE FUNCTION", "ALTER FUNCTION", "ALTER TABLE", "GRANT ", "REVOKE ", "CREATE SEQUENCE", "migration_baseline =", "NO TRANSACTION"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("DB-only deferral must not contain %q", forbidden)
		}
	}
	if !sqlStrings.Contains(source, "UPDATE app.schema_metadata SET schema_version = 28,minimum_runtime_version = 28 WHERE singleton;") {
		t.Fatal("canonical migration must end at schema/minimum 28")
	}
}

func TestDeferredGenerationFenceReplacementIsSingleAndSurgical(t *testing.T) {
	source := foldedGenerationSection(t)
	quoted := regexp.MustCompile(`(?s)pattern := '((?:[^']|'')*)';`).FindStringSubmatch(source)
	if len(quoted) != 2 {
		t.Fatal("expected one guarded replacement pattern")
	}
	// PostgreSQL's start-of-word escape is equivalent here to Go's boundary.
	pattern, err := regexp.Compile("(?i)" + strings.ReplaceAll(quoted[1], `\m`, `\b`))
	if err != nil {
		t.Fatal(err)
	}
	replacement := regexp.MustCompile(`(?s)\$replacement\$(.*?)\$replacement\$`).FindStringSubmatch(source)
	if len(replacement) != 2 {
		t.Fatal("missing replacement")
	}
	prior, err := Files.ReadFile("024_provider_workout_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := regexp.MustCompile(`(?s)CREATE FUNCTION app\.fence_ingest_job\(.*?\$function\$;`).FindString(string(prior))
	statement := "PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account FOR UPDATE NOWAIT;"
	errorBlock := "IF NOT FOUND THEN RAISE EXCEPTION 'missing account data generation' USING ERRCODE = '55000'; END IF;"
	if !strings.Contains(old, statement) || !strings.Contains(old, errorBlock) {
		t.Fatal("immutable fence anchor changed")
	}
	// Simulate later capability additions without duplicating their implementation.
	old = strings.Replace(old, "BEGIN\n", "BEGIN\n    -- existing runtime-28 loaned capability must remain byte-identical\n", 1)
	commentFree := regexp.MustCompile(`(?m)^[\t ]*--[^\n]*\n`).ReplaceAllString(old, "")
	mixedStatement := "pErFoRm\t1 fRoM APP . account_data_generations wHeRe account_id=target_account fOr uPdAtE nOwAiT ;"
	compactError := "if not found then raise exception 'missing account data generation' using errcode='55000';end if;"
	for name, definition := range map[string]string{
		"shipped":         old,
		"layout":          strings.ReplaceAll(strings.ReplaceAll(old, "\n", "\r\n"), "    ", "\t  "),
		"comment free":    commentFree,
		"comment drift":   strings.Replace(old, "All type/header/route writes", "Different documentation", 1),
		"mixed case":      strings.Replace(old, statement, mixedStatement, 1),
		"compact":         strings.ReplaceAll(strings.Replace(old, statement, strings.ToLower(strings.ReplaceAll(statement, " = ", "=")), 1), errorBlock, compactError),
		"combined layout": strings.ReplaceAll(strings.Replace(commentFree, statement, mixedStatement, 1), errorBlock, compactError),
	} {
		t.Run(name, func(t *testing.T) {
			matches := pattern.FindAllStringIndex(definition, -1)
			if len(matches) != 1 {
				t.Fatalf("anchor count = %d, want 1", len(matches))
			}
			location := matches[0]
			compact := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }
			if compact(definition[location[0]:location[1]]) != compact(statement) {
				t.Fatal("anchor includes more than the locking PERFORM statement")
			}
			got := pattern.ReplaceAllStringFunc(definition, func(string) string { return replacement[1] })
			want := definition[:location[0]] + replacement[1] + definition[location[1]:]
			if got != want {
				t.Fatal("replacement changed bytes outside the generation statement")
			}
			if strings.Count(got, errorBlock) != strings.Count(definition, errorBlock) || strings.Count(got, compactError) != strings.Count(definition, compactError) {
				t.Fatal("replacement changed the original missing-generation error block")
			}
		})
	}
	if len(pattern.FindAllStringIndex(strings.Replace(old, statement, "", 1), -1)) != 0 || len(pattern.FindAllStringIndex(old+old, -1)) != 2 {
		t.Fatal("missing/duplicate anchors must not be admitted")
	}
	if replacement[1] != "PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account;" {
		t.Fatal("replacement must only make the generation PERFORM nonlocking")
	}
	for _, required := range []string{
		"pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)",
		"SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi')",
		"IF occurrences <> 1 THEN", "EXECUTE definition;",
		"$replacement$,'gi')", "definition := replace(definition,",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("missing fence preservation guard %q", required)
		}
	}
	if strings.Contains(quoted[1], "--") || strings.Contains(quoted[1], "FOUND") || strings.Contains(quoted[1], "EXCEPTION") {
		t.Fatal("anchor must not depend on documentation or error-block layout")
	}
}

func TestDeferredGenerationMapHelperOnlyRemovesRowLock(t *testing.T) {
	prior, err := Files.ReadFile("009_raw_route_map.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw := foldedGenerationSection(t)
	pattern := regexp.MustCompile(`(?s)FUNCTION app\.lock_account_data_generation\s*\(\).*?AS \$function\$(.*?)\$function\$;`)
	old, next := pattern.FindStringSubmatch(string(prior)), pattern.FindStringSubmatch(raw)
	if len(old) != 2 || len(next) != 2 {
		t.Fatal("missing helper body")
	}
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if compact(strings.Replace(old[1], "FOR UPDATE", "", 1)) != compact(next[1]) {
		t.Fatal("helper changed more than its row lock")
	}
	if !strings.Contains(string(raw), "LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app") {
		t.Fatal("helper protection/volatility changed")
	}
}

func TestDeferredGenerationActiveInventoryAndImmutableBaseline(t *testing.T) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var active []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			active = append(active, entry.Name())
		}
	}
	if len(active) != 28 || active[27] != "028_ingest_coverage_pipeline.sql" {
		t.Fatalf("unexpected active migration inventory: %v", active)
	}
	for file, expected := range map[string]string{
		"027_coverage_recovery_baseline.sql": "33dea989a5e4653081bdb4e080f396a46580d1874e0bde35b1d99d72bb5e80dc",
	} {
		raw, err := Files.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != expected {
			t.Errorf("immutable %s changed: %s", file, got)
		}
	}
}
