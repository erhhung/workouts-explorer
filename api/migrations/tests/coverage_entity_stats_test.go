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
	definition := regexp.MustCompile(`(?s)CREATE FUNCTION app\.map_selection_coverage_entity_stats\s*\(.*?\$function\$;`).FindString(source)
	canonicalRequire(t, definition,
		`CREATE FUNCTION app.map_selection_coverage_entity_stats (
            target_account_id uuid, target_session_id uuid, target_selection_id uuid,
            target_generation bigint, target_entity_kind text, target_entity_id uuid
        ) RETURNS TABLE (`,
		") LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app SET jit = off AS $function$")
	old, err := migrations.Files.ReadFile("020_facility_coverage_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	result := regexp.MustCompile(`(?s)RETURNS TABLE \((.*?)\)\s*LANGUAGE`)
	statsResult := result.FindStringSubmatch(definition)
	detail := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION app\.map_selection_coverage_entity_detail\(.*?\$function\$;`).FindString(string(old))
	detailResult := result.FindStringSubmatch(detail)
	if len(statsResult) != 2 || len(detailResult) != 2 {
		t.Fatal("missing shipped detail or current stats result ABI")
	}
	metadata, _, found := strings.Cut(detailResult[1], ",\n    geometry jsonb")
	if !found || compactSQL(metadata) != compactSQL(statsResult[1]) {
		t.Fatal("stats must retain all 21 shipped detail metadata columns in order")
	}
	canonicalRequire(t, source,
		"ALTER FUNCTION app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid) OWNER TO workouts_security_owner;",
		"REVOKE ALL ON FUNCTION app.coverage_display_normalize(text)",
		"app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid)",
		"app.map_selection_coverage_entity_members(uuid, uuid, uuid, bigint, text, uuid) TO workouts_api;")
	grants := regexp.MustCompile(`(?is)\bGRANT\s+EXECUTE\b[^;]+;`).FindAllString(source, -1)
	if len(grants) != 1 || !strings.Contains(grants[0], "app.map_selection_coverage_entity_stats(") {
		t.Fatal("stats must appear in the one API-only runtime execute allowlist")
	}
	canonicalForbid(t, grants[0], "workouts_worker", "workouts_tiles", "workouts_coverage_worker")
}

func TestCoverageEntityStatsAuthorizationBeforeFacts(t *testing.T) {
	stats := coverageDisplayFunction(t, coverageDisplaySQL(t), "map_selection_coverage_entity_stats")
	validation, query, found := strings.Cut(stats, "IF authorized_id IS NULL THEN RETURN; END IF;")
	if !found {
		t.Fatal("invalid selection must return before accessing cached facts")
	}
	canonicalRequire(t, validation, "authorized_id := app.coverage_display_authorized_context(target_account_id, target_session_id, target_selection_id, target_generation)")
	canonicalForbid(t, validation, "FROM app.coverage_display_entities")
	canonicalRequire(t, query, "RETURN QUERY SELECT", "entity.account_id = target_account_id", "entity.context_id = authorized_id")
	authorized := coverageDisplayFunction(t, coverageDisplaySQL(t), "coverage_display_authorized_context")
	canonicalRequire(t, authorized, "app.coverage_display_selection_scope(", "context.ready",
		"context.revision = scope.revision", "context.generation = target_generation",
		"context.start_date = scope.start_date", "context.end_date = scope.end_date")
}

func TestCoverageEntityStatsCanonicalPointProbe(t *testing.T) {
	stats := coverageDisplayFunction(t, coverageDisplaySQL(t), "map_selection_coverage_entity_stats")
	canonicalRequire(t, stats, "entity.canonical_id = target_entity_id", "entity.entity_kind = target_entity_kind",
		"entity.range_workout_count > 0")
	canonicalForbid(t, stats, "app.workouts", "app.workout_routes", "app.workout_park_attributions",
		"app.workout_path_display_partials", "app.workout_path_display_context", "app.workout_park_display_bounds",
		"count(", "GROUP BY", "source_visits(", "prepare_coverage", "flush_coverage", "rebuild_coverage",
		"INSERT INTO", "UPDATE app.", "DELETE FROM")
	if regexp.MustCompile(`(?i)\bst_\w+\s*\(`).MatchString(stats) {
		t.Fatal("warm stats may not perform geometry work")
	}
}

func TestCoverageEntityStatsTargetedFactsAndIndexes(t *testing.T) {
	source := coverageDisplaySQL(t)
	canonicalRequire(t, source, "PRIMARY KEY (account_id, context_id, canonical_id)",
		"ON app.coverage_display_entities(account_id, context_id, canonical_id) WHERE range_workout_count > 0",
		"ON app.workout_path_display_partials (account_id, entity_id, workout_id)",
		"ON app.workout_path_display_context (account_id, entity_id, workout_id)",
		"ON app.workout_park_display_bounds (account_id, park_id, workout_id)")
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	canonicalRequire(t, prepare, "FROM app.coverage_display_members member",
		"member.account_id = target_account_id AND member.context_id = prepared_id",
		"AND member.canonical_id = summary.canonical_id")
}

func TestCoverageEntityStatsListSemanticsParity(t *testing.T) {
	source := coverageDisplaySQL(t)
	stats := coverageDisplayFunction(t, source, "map_selection_coverage_entity_stats")
	list := coverageDisplayFunction(t, source, "map_selection_coverage_entities")
	projection := func(body string) string {
		t.Helper()
		_, body, found := strings.Cut(body, "RETURN QUERY SELECT ")
		if !found {
			t.Fatal("missing read-only result projection")
		}
		body, _, found = strings.Cut(body, "FROM app.coverage_display_entities entity")
		if !found {
			t.Fatal("missing cached entity source")
		}
		return compactSQL(body)
	}
	if projection(stats) != projection(list) {
		t.Fatal("stats and list must project the same cached 21-column metadata")
	}
}

func TestCoverageEntityStatsCanonicalParkAvailability(t *testing.T) {
	source := coverageDisplaySQL(t)
	visits := coverageDisplayFunction(t, source, "coverage_display_source_visits")
	bounds := coverageEntityStatsCTE(t, visits, "park_bounds")
	canonicalRequire(t, bounds, "FROM eligible JOIN app.workout_park_display_bounds bounds",
		"FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end)",
		"min(bounds.region_id COLLATE \"C\") historical_region_id")
	canonicalForbid(t, bounds, "app.workout_park_attributions", "HAVING", "ST_")
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	canonicalRequire(t, prepare, "WHERE summary.range_workout_count = 0 OR (representative.region_id IS NOT NULL",
		"representative.canonical_minimum_longitude IS NOT NULL", "representative.canonical_maximum_latitude IS NOT NULL")
	rebuild := coverageDisplayFunction(t, source, "rebuild_coverage_display_precomputations")
	canonicalRequire(t, rebuild,
		"min(region_id) FILTER (WHERE tags->>'workouts:park_id' = ((tags->>'workouts:park_id')::uuid)::text)",
		"GROUP BY account_id, workout_id, (tags->>'workouts:park_id')::uuid")
}
