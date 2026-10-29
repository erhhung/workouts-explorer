package migrations_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func coverageDisplaySQL(t *testing.T) string {
	t.Helper()
	body, err := migrations.Files.ReadFile("030_coverage_display.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func coverageDisplayFunction(t *testing.T, source, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION app\.` + regexp.QuoteMeta(name) +
		`\s*\(.*?\$function\$(.*?)\$function\$;`)
	match := pattern.FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("missing function %s", name)
	}
	return match[1]
}

func TestCoverageDisplayPrecomputationsSchemaAndSecurity(t *testing.T) {
	source := coverageDisplaySQL(t)
	precomputations, _, found := strings.Cut(source, "-- Display identity is a read-model policy")
	if !found || strings.Count(precomputations, "CREATE TABLE app.") != 4 || strings.Count(precomputations, "CREATE CONSTRAINT TRIGGER ") != 1 {
		t.Fatal("expected three projections, one dirty queue and one deferred coordinator")
	}
	for _, table := range []string{"workout_path_display_partials", "workout_path_display_context",
		"workout_park_display_bounds", "coverage_display_dirty"} {
		for _, required := range []string{
			"ALTER TABLE app." + table + " ENABLE ROW LEVEL SECURITY;",
			"ALTER TABLE app." + table + " FORCE ROW LEVEL SECURITY;",
			"CREATE POLICY " + table + "_owner_policy ON app." + table,
			"ALTER TABLE app." + table + " OWNER TO workouts_security_owner;",
		} {
			if !strings.Contains(source, required) {
				t.Errorf("missing owner-only projection contract %q", required)
			}
		}
	}
	for _, required := range []string{
		"PRIMARY KEY (account_id, workout_id, entity_id, logical_path_id, qualifies_without_name)",
		"UNIQUE NULLS NOT DISTINCT",
		"(transaction_id, account_id, scope, workout_id, region_id, generation_id, physical_segment_id)",
		"REVOKE ALL ON app.workout_path_display_partials, app.workout_path_display_context,",
		"REVOKE ALL ON FUNCTION app.rebuild_coverage_display_precomputations(uuid),",
		"FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;",
		"schema_version = 30, minimum_runtime_version = 30", "forward-only", "restore the pre-migration backup",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("missing display projection contract %q", required)
		}
	}
	queue, _, _ := strings.Cut(strings.SplitN(source, "CREATE TABLE app.coverage_display_dirty (", 2)[1], "\n);")
	if strings.Contains(queue, "REFERENCES") {
		t.Fatal("dirty tombstones must survive deletion of their source rows")
	}
	restrictedSource := precomputations
	if regexp.MustCompile(`(?i)\bGRANT\s+EXECUTE\b`).MatchString(restrictedSource) {
		t.Fatal("precomputation maintenance must not gain runtime execute privileges")
	}
	for _, forbidden := range []string{"GRANT SELECT", "GRANT EXECUTE", "DISABLE TRIGGER", "NO FORCE ROW LEVEL SECURITY",
		"SET CONSTRAINTS", "CREATE OR REPLACE FUNCTION app.persist_coverage_route",
		"CREATE OR REPLACE FUNCTION app.advance_account_data_generation", "migration_baseline ="} {
		if strings.Contains(restrictedSource, forbidden) {
			t.Errorf("unexpected privilege, writer or generation contract change %q", forbidden)
		}
	}
}

func TestCoverageDisplayPrecomputationsDeferredBatching(t *testing.T) {
	source := coverageDisplaySQL(t)
	mark := coverageDisplayFunction(t, source, "mark_coverage_display_dirty")
	for _, required := range []string{
		"OLD.account_id, 'workout', OLD.workout_id", "NEW.account_id, 'workout', NEW.workout_id",
		"OLD.account_id, 'segment', OLD.region_id, OLD.generation_id, OLD.physical_segment_id",
		"NEW.account_id, 'segment', NEW.region_id, NEW.generation_id, NEW.physical_segment_id",
		"FROM unnest(ARRAY[old_account, new_account]) account_id", "'account'", "ON CONFLICT DO NOTHING",
		"OLD.highway, OLD.tags) IS NOT DISTINCT FROM", "NEW.highway, NEW.tags)",
	} {
		if !strings.Contains(mark, required) {
			t.Errorf("missing cheap source invalidation %q", required)
		}
	}
	for _, forbidden := range []string{"JOIN app.workout_segment_matches", "rebuild_coverage_display_precomputations",
		"flush_coverage_display_precomputations", "UPDATE app.account_data_generations"} {
		if strings.Contains(mark, forbidden) {
			t.Errorf("source trigger must enqueue only, not refresh or expand: %s", forbidden)
		}
	}
	for _, required := range []string{
		"AFTER INSERT OR UPDATE OR DELETE ON app.workout_segment_matches",
		"AFTER UPDATE OR DELETE ON app.path_segments",
		"AFTER INSERT ON app.coverage_display_dirty DEFERRABLE INITIALLY DEFERRED",
		"FOR EACH ROW WHEN (NEW.scope = 'account')",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("missing deferred source coverage %q", required)
		}
	}
	flush := coverageDisplayFunction(t, source, "flush_coverage_display_precomputations")
	previous := -1
	for _, anchor := range []string{
		"IF NOT EXISTS (SELECT 1 FROM app.coverage_display_dirty",
		"UPDATE app.account_data_generations SET generation = generation",
		"INSERT INTO app.coverage_display_dirty (transaction_id, account_id, scope, workout_id)",
		"JOIN app.workout_segment_matches match", "dirty.scope = 'segment'",
		"PERFORM app.rebuild_coverage_display_precomputations(target_account_id);",
	} {
		index := strings.Index(flush, anchor)
		if index <= previous {
			t.Fatalf("missing/out-of-order barrier, fresh expansion or batched refresh: %s", anchor)
		}
		previous = index
	}
	precomputations, _, found := strings.Cut(source, "-- Display identity is a read-model policy")
	if !found || strings.Count(precomputations, "UPDATE app.account_data_generations") != 1 || strings.Contains(flush, "EXCEPTION WHEN") {
		t.Fatal("only flush may take the generation barrier; serialization/deadlock failures must propagate")
	}
	if !strings.Contains(flush[previous:], "DELETE FROM app.coverage_display_dirty") {
		t.Fatal("successful refresh must drain keys, allowing a later write to re-enqueue")
	}
}

func TestCoverageDisplayPrecomputationsIndependentEvidence(t *testing.T) {
	source := coverageDisplaySQL(t)
	rebuild := coverageDisplayFunction(t, source, "rebuild_coverage_display_precomputations")
	if strings.Count(rebuild, "WITH evidence AS MATERIALIZED (") != 1 ||
		strings.Count(rebuild, "JOIN app.workout_segment_matches match") != 1 {
		t.Fatal("one evidence scan must feed all three projections")
	}
	for _, forbidden := range []string{"app.coverage_paths", "app.workout_park_attributions", "app.workout_routes",
		"app.workout_coverage_states", "local_start_date", "LOOP"} {
		if strings.Contains(rebuild, forbidden) {
			t.Errorf("rebuild must not snapshot live metadata/eligibility or loop over workouts: %s", forbidden)
		}
	}
	partials, context, ok := strings.Cut(rebuild, "), path_context AS (")
	if !ok {
		t.Fatal("missing independent context projection")
	}
	context, parks, ok := strings.Cut(context, "INSERT INTO app.workout_park_display_bounds (")
	if !ok {
		t.Fatal("missing independent park bounds projection")
	}
	for _, required := range []string{"bool_or(tags ? 'workouts:education_id')", "min(tags->>'workouts:education_name')",
		"COALESCE(NULLIF(tags->>'workouts:education_id', '')::uuid, logical_path_id)",
		"OR NOT (tags ? 'workouts:park_id')) IS TRUE", "'driveway', 'parking_aisle'", "IS DISTINCT FROM 'parking'"} {
		if !strings.Contains(partials, required) {
			t.Errorf("missing exact path qualification partial %q", required)
		}
	}
	if !strings.Contains(context, "NULLIF(tags->>'workouts:park_name', '') IS NOT NULL") ||
		!strings.Contains(context, "COALESCE(NULLIF(tags->>'workouts:education_id', '')::uuid, logical_path_id)") {
		t.Fatal("context must preserve independent education identity and nonempty regional park names")
	}
	for _, body := range []string{context, parks} {
		if strings.Contains(body, "highway") || strings.Contains(body, "amenity") || strings.Contains(body, "qualifies_without_name") {
			t.Fatal("excluded path segments must still contribute context and park bounds")
		}
	}
	if !strings.Contains(parks, "FILTER (WHERE tags->>'workouts:park_id' = ((tags->>'workouts:park_id')::uuid)::text)") {
		t.Fatal("park regions must preserve 019 canonical-text equality, separately from cast UUID bounds")
	}
	if !strings.Contains(source, "LOCK TABLE app.workouts, app.workout_segment_matches, app.path_segments") ||
		!strings.Contains(source, "SELECT DISTINCT txid_current(), account_id, 'workout', workout_id FROM app.workout_segment_matches;") ||
		!strings.Contains(source, "SELECT app.rebuild_coverage_display_precomputations(NULL);") {
		t.Fatal("migration must backfill all evidence as one consistent batch")
	}
}

func TestCoverageDisplayPrecomputationsReaderContract(t *testing.T) {
	source := coverageDisplaySQL(t)
	reader := coverageDisplayFunction(t, source, "coverage_display_source_visits")
	definition := regexp.MustCompile(`(?s)CREATE FUNCTION app\.coverage_display_source_visits\(.*?\$function\$;`).FindString(source)
	for _, required := range []string{
		") LANGUAGE sql STABLE SECURITY DEFINER", "SET jit = off AS $function$",
		"path.logical_path_id = partial.logical_path_id", "WHERE path.name IS NOT NULL OR partial.qualifies_without_name",
		"GROUP BY eligible.id, eligible.local_start_date, eligible.started_at, partial.entity_id",
		"CASE WHEN bool_or(visit.education_key_present) THEN 'other' ELSE min(visit.broad_class COLLATE \"C\") END",
		"COALESCE(min(visit.education_name COLLATE \"C\"), min(visit.name COLLATE \"C\"))", "LEFT JOIN app.workout_park_display_bounds bounds",
		"JOIN app.workout_path_display_context context", "LEFT JOIN park_bounds bounds ON bounds.park_id = visit.entity_id",
		"COALESCE(context.range_locality, CASE WHEN NOT identity.has_range THEN context.historical_locality END,",
		"workout.deletion_requested_at IS NULL", "coverage.applied_route_input_revision IS NOT NULL", "route.route IS NOT NULL",
	} {
		if !strings.Contains(definition, required) {
			t.Errorf("missing exact reader contract %q", required)
		}
	}
	for _, forbidden := range []string{"app.workout_segment_matches", "app.path_segments", "flush_coverage_display",
		"visit_date", "processing_state = 'current'", "count(DISTINCT"} {
		if strings.Contains(reader, forbidden) {
			t.Errorf("reader must use projections/live eligibility and count deduplicated visits: %s", forbidden)
		}
	}
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	if !strings.Contains(prepare, "ORDER BY local_start_date DESC, started_at, workout_id") || !strings.Contains(prepare, "ORDER BY local_start_date, started_at, workout_id") {
		t.Fatal("canonical summaries must retain first/latest date/start/UUID tie breaks")
	}
}
