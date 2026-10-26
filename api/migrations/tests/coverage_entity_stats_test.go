package migrations_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func coverageEntityStatsCTE(t *testing.T, body, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)\b` + regexp.QuoteMeta(name) + ` AS (?:MATERIALIZED )?\((.*?)\n    \)`)
	match := pattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing coverage CTE %s", name)
	}
	return match[1]
}

func TestCoverageEntityStatsSignatureAndSecurity(t *testing.T) {
	source := coverageDisplaySQL(t)
	signature := "app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid)"
	definition := regexp.MustCompile(`(?s)CREATE FUNCTION app\.map_selection_coverage_entity_stats\s*\(.*?\$function\$;`).FindString(source)
	for _, required := range []string{
		`CREATE FUNCTION app.map_selection_coverage_entity_stats (
            target_account_id uuid, target_session_id uuid, target_selection_id uuid,
            target_generation bigint, target_entity_kind text, target_entity_id uuid
        ) RETURNS TABLE (`,
		") LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app SET jit = off AS $function$",
	} {
		if !strings.Contains(compactSQL(definition), compactSQL(required)) {
			t.Errorf("missing stats function contract %q", required)
		}
	}
	result := regexp.MustCompile(`(?s)RETURNS TABLE \((.*?)\) LANGUAGE`)
	statsResult := result.FindStringSubmatch(definition)
	listResult := result.FindStringSubmatch(source)
	if len(statsResult) != 2 || len(listResult) != 2 || compactSQL(statsResult[1]) != compactSQL(listResult[1]) {
		t.Fatal("stats must return the list reader's exact ordered column names and types")
	}
	oldDetail, err := migrations.Files.ReadFile("020_facility_coverage_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	detailResult := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION app\.map_selection_coverage_entity_detail\(.*?RETURNS TABLE \((.*?)\)\s*LANGUAGE`).FindStringSubmatch(string(oldDetail))
	if len(detailResult) != 2 {
		t.Fatal("missing shipped detail result")
	}
	metadata, _, found := strings.Cut(detailResult[1], ",\n    geometry jsonb")
	if !found || compactSQL(metadata) != compactSQL(statsResult[1]) {
		t.Fatal("stats must preserve every detail metadata field, excluding geometry and fit bounds only")
	}
	for _, required := range []string{
		"GRANT CREATE ON SCHEMA app TO workouts_security_owner;\nALTER FUNCTION " + signature + " OWNER TO workouts_security_owner;\nREVOKE CREATE ON SCHEMA app FROM workouts_security_owner;",
		"REVOKE ALL ON FUNCTION " + signature + "\nFROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;",
		"GRANT EXECUTE ON FUNCTION " + signature + " TO workouts_api;",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("missing hardened stats ownership/ACL %q", required)
		}
	}
	grants := regexp.MustCompile(`(?is)\bGRANT\s+[^;]+;`).FindAllString(source, -1)
	for _, grant := range grants {
		if grant != "GRANT CREATE ON SCHEMA app TO workouts_security_owner;" &&
			grant != "GRANT EXECUTE ON FUNCTION "+signature+" TO workouts_api;" {
			t.Errorf("unexpected runtime or table privilege: %s", grant)
		}
	}
	if strings.Contains(definition, "public") || strings.Contains(source, "CREATE OR REPLACE FUNCTION app.map_selection_coverage_entity_detail") {
		t.Fatal("stats must not trust public or replace the shipped geometry reader")
	}
}

func TestCoverageEntityStatsAuthorizationBeforeFacts(t *testing.T) {
	source := coverageDisplaySQL(t)
	stats := coverageDisplayFunction(t, source, "map_selection_coverage_entity_stats")
	reader := coverageDisplayFunction(t, source, "map_selection_coverage_entities")
	_, validation, found := strings.Cut(stats, "BEGIN\n")
	if !found {
		t.Fatal("missing procedural authorization barrier")
	}
	validation, query, found := strings.Cut(validation, ";\n    IF NOT FOUND THEN\n        RETURN;\n    END IF;\n\n    RETURN QUERY\n")
	if !found {
		t.Fatal("invalid selection must return before executing the fact query")
	}
	validation = strings.Replace(validation, " INTO selected_start_date, selected_end_date", "", 1)
	if compactSQL(validation) != compactSQL(coverageEntityStatsCTE(t, reader, "selected")) {
		t.Fatal("stats must retain exact selection/session/account/generation authorization before any fact reads")
	}
	if compactSQL(coverageEntityStatsCTE(t, query, "selected")) != compactSQL("SELECT selected_start_date start_date, selected_end_date end_date") {
		t.Fatal("range query must use the dates from the validated selection")
	}
	eligible := coverageEntityStatsCTE(t, query, "eligible")
	eligible = strings.Replace(eligible, "FROM candidate_workout_ids candidate\n        JOIN app.workouts workout ON workout.id = candidate.workout_id", "FROM app.workouts workout", 1)
	if compactSQL(eligible) != compactSQL(coverageEntityStatsCTE(t, reader, "eligible")) {
		t.Fatal("stats must retain live list eligibility, including adopted coverage and pending deletion rules")
	}
}

func TestCoverageEntityStatsCandidateWorkouts(t *testing.T) {
	stats := coverageDisplayFunction(t, coverageDisplaySQL(t), "map_selection_coverage_entity_stats")
	if !strings.Contains(stats, "candidate_workout_ids AS MATERIALIZED (") {
		t.Fatal("stats must build entity-targeted candidate workout IDs before live eligibility")
	}
	candidates := coverageEntityStatsCTE(t, stats, "candidate_workout_ids")
	// Independent context/bounds workouts must survive even when they have no
	// qualifying path visit or park attribution; UNION deduplicates the IDs.
	want := `
        SELECT partial.workout_id
        FROM app.workout_path_display_partials partial
        WHERE partial.account_id = target_account_id
            AND target_entity_kind = 'path' AND partial.entity_id = target_entity_id
        UNION
        SELECT context.workout_id
        FROM app.workout_path_display_context context
        WHERE context.account_id = target_account_id
            AND target_entity_kind = 'path' AND context.entity_id = target_entity_id
        UNION
        SELECT attribution.workout_id
        FROM app.workout_park_attributions attribution
        WHERE attribution.account_id = target_account_id
            AND target_entity_kind = 'park' AND attribution.park_id = target_entity_id
        UNION
        SELECT bounds.workout_id
        FROM app.workout_park_display_bounds bounds
        WHERE bounds.account_id = target_account_id
            AND target_entity_kind = 'park' AND bounds.park_id = target_entity_id`
	if compactSQL(candidates) != compactSQL(want) {
		t.Fatal("candidate IDs must union all four independently targeted fact sources without qualification, attribution, canonical-region or date restrictions")
	}
	eligible := coverageEntityStatsCTE(t, stats, "eligible")
	if !strings.Contains(compactSQL(eligible), compactSQL(`
        FROM candidate_workout_ids candidate
        JOIN app.workouts workout ON workout.id = candidate.workout_id
        JOIN app.workout_routes route`)) {
		t.Fatal("live eligibility must depend on candidate workout IDs, not all account history")
	}
}

func TestCoverageEntityStatsTargetedFactsAndIndexes(t *testing.T) {
	source := coverageDisplaySQL(t)
	stats := coverageDisplayFunction(t, source, "map_selection_coverage_entity_stats")
	for _, forbidden := range []string{
		"app.map_selection_coverage_entities(", "app.map_selection_coverage_entity_detail(",
		"app.workout_segment_matches", "app.path_segments", ".geom", "fit_", "count(DISTINCT",
		"flush_coverage_display", "rebuild_coverage_display",
	} {
		if strings.Contains(compactSQL(stats), compactSQL(forbidden)) {
			t.Errorf("stats must not use full-list/detail/geometry or maintenance work: %s", forbidden)
		}
	}
	if regexp.MustCompile(`(?i)\bst_\w+\s*\(`).MatchString(stats) {
		t.Fatal("stats must aggregate compact numeric facts, not geometry")
	}
	for _, target := range []struct{ cte, predicate string }{
		{"path_visits", "AND target_entity_kind = 'path' AND partial.entity_id = target_entity_id"},
		{"park_visits", "AND target_entity_kind = 'park' AND attribution.park_id = target_entity_id"},
		{"park_bounds", "AND target_entity_kind = 'park' AND bounds.park_id = target_entity_id"},
		{"regional_park_path_context", "AND target_entity_kind = 'path' AND context.entity_id = target_entity_id"},
		{"path_identity", "WHERE target_entity_kind = 'path' AND visit.entity_id = target_entity_id"},
	} {
		cte := coverageEntityStatsCTE(t, stats, target.cte)
		beforeAggregate, _, _ := strings.Cut(cte, "GROUP BY")
		if !strings.Contains(compactSQL(beforeAggregate), compactSQL(target.predicate)) {
			t.Errorf("%s must filter entity and kind before aggregation", target.cte)
		}
	}
	for _, index := range []string{
		"CREATE INDEX workout_path_display_partials_entity_idx ON app.workout_path_display_partials (account_id, entity_id, workout_id);",
		"CREATE INDEX workout_path_display_context_entity_idx ON app.workout_path_display_context (account_id, entity_id, workout_id);",
		"CREATE INDEX workout_park_display_bounds_park_idx ON app.workout_park_display_bounds (account_id, park_id, workout_id);",
	} {
		if !strings.Contains(compactSQL(source), compactSQL(index)) {
			t.Errorf("missing entity-first fact access path %q", index)
		}
	}
	attribution, err := migrations.Files.ReadFile("018_park_attribution.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compactSQL(string(attribution)), compactSQL("ON app.workout_park_attributions (account_id, park_id, workout_id)")) {
		t.Fatal("park attribution lookup also requires its existing entity-first index")
	}
}

func TestCoverageEntityStatsListSemanticsParity(t *testing.T) {
	source := coverageDisplaySQL(t)
	stats := coverageDisplayFunction(t, source, "map_selection_coverage_entity_stats")
	reader := coverageDisplayFunction(t, source, "map_selection_coverage_entities")
	// Only target pushdowns and the canonical-geometry availability gate may
	// differ; compare all count, identity, bounds and tie-break expressions.
	withoutComments := regexp.MustCompile(`--[^\n]*`)
	for _, cte := range []struct{ name, pushdown string }{
		{"path_visits", "AND target_entity_kind = 'path' AND partial.entity_id = target_entity_id"},
		{"park_visits", "AND target_entity_kind = 'park' AND attribution.park_id = target_entity_id"},
		{"path_range", ""}, {"path_all", ""}, {"park_range", ""}, {"park_all", ""},
		{"park_bounds", "AND target_entity_kind = 'park' AND bounds.park_id = target_entity_id"},
		{"regional_park_path_context", "AND target_entity_kind = 'path' AND context.entity_id = target_entity_id"},
		{"path_identity", "WHERE target_entity_kind = 'path' AND visit.entity_id = target_entity_id"},
		{"entities", ""},
	} {
		got := withoutComments.ReplaceAllString(coverageEntityStatsCTE(t, stats, cte.name), "")
		want := withoutComments.ReplaceAllString(coverageEntityStatsCTE(t, reader, cte.name), "")
		if cte.pushdown != "" {
			got = strings.Replace(got, cte.pushdown, "", 1)
		}
		if cte.name == "park_bounds" {
			got = strings.Replace(got, "HAVING bool_or(bounds.region_id IS NOT NULL)", "", 1)
		}
		if compactSQL(got) != compactSQL(want) {
			t.Errorf("%s differs from list semantics beyond targeting/canonical availability", cte.name)
		}
	}
}

func TestCoverageEntityStatsCanonicalParkAvailability(t *testing.T) {
	source := coverageDisplaySQL(t)
	stats := coverageDisplayFunction(t, source, "map_selection_coverage_entity_stats")
	bounds := coverageEntityStatsCTE(t, stats, "park_bounds")
	for _, required := range []string{
		"FROM eligible JOIN selected ON eligible.local_start_date BETWEEN selected.start_date AND selected.end_date",
		"GROUP BY bounds.park_id", "HAVING bool_or(bounds.region_id IS NOT NULL)",
	} {
		if !strings.Contains(bounds, required) {
			t.Errorf("missing range-only canonical park availability check %q", required)
		}
	}
	beforeGroup, _, _ := strings.Cut(bounds, "GROUP BY")
	if strings.Contains(beforeGroup, "region_id IS NOT NULL") || strings.Contains(bounds, "attribution") {
		t.Fatal("canonical availability must not shrink UUID-cast list bounds or require attribution on the geometry workout")
	}
	if !strings.Contains(stats, "JOIN park_bounds bounds USING (entity_id)") {
		t.Fatal("parks without canonical range geometry must remain unavailable as in 020 detail")
	}
	rebuild := coverageDisplayFunction(t, source, "rebuild_coverage_display_precomputations")
	for _, required := range []string{
		"min(region_id) FILTER (WHERE tags->>'workouts:park_id' = ((tags->>'workouts:park_id')::uuid)::text)",
		"GROUP BY account_id, workout_id, (tags->>'workouts:park_id')::uuid",
	} {
		if !strings.Contains(rebuild, required) {
			t.Errorf("compact park facts must distinguish canonical geometry from UUID-cast bounds: %s", required)
		}
	}
}
