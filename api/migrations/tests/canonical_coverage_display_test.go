package migrations_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func canonicalCoverageSQL(t *testing.T) string {
	t.Helper()
	body, err := migrations.Files.ReadFile("030_coverage_display.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, found := strings.Cut(string(body), "-- Display identity is a read-model policy")
	if !found {
		t.Fatal("missing canonical display section")
	}
	return canonical
}

func canonicalRequire(t *testing.T, source string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(compactSQL(source), compactSQL(fragment)) {
			t.Errorf("missing canonical display contract %q", fragment)
		}
	}
}

func canonicalForbid(t *testing.T, source string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(compactSQL(source), compactSQL(fragment)) {
			t.Errorf("unexpected canonical display operation %q", fragment)
		}
	}
}

func TestCanonicalCoverageDisplayPrivateSchema(t *testing.T) {
	source := canonicalCoverageSQL(t)
	for _, table := range []string{"coverage_display_revisions", "coverage_display_contexts", "coverage_display_members",
		"coverage_display_visits", "coverage_display_entities", "coverage_display_invalidations"} {
		canonicalRequire(t, source,
			"CREATE TABLE app."+table+" (",
			"ALTER TABLE app."+table+" ENABLE ROW LEVEL SECURITY;",
			"ALTER TABLE app."+table+" FORCE ROW LEVEL SECURITY;",
			"CREATE POLICY "+table+"_owner_policy ON app."+table+" TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);",
			"ALTER TABLE app."+table+" OWNER TO workouts_security_owner;")
	}
	canonicalRequire(t, source,
		"PRIMARY KEY (account_id, context_id, source_kind, source_id)",
		"PRIMARY KEY (account_id, context_id, canonical_id, workout_id)",
		"PRIMARY KEY (account_id, context_id, canonical_id)",
		"UNIQUE (account_id, generation, revision, start_date, end_date, policy_version, label_locale)",
		"ON app.coverage_display_members(account_id, context_id, canonical_id, source_kind, source_id)",
		"ON app.coverage_display_members(account_id, canonical_id)",
		"ON app.coverage_display_entities(account_id, context_id, canonical_id) WHERE range_workout_count > 0",
		"ON app.coverage_display_visits(account_id, context_id, canonical_id, local_start_date, started_at, workout_id)",
		"REFERENCES app.accounts(id) ON DELETE CASCADE",
		"ON DELETE SET NULL (coverage_display_context_id)",
		"REVOKE ALL ON app.coverage_display_revisions, app.coverage_display_contexts, app.coverage_display_members,",
		"FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;",
		"schema_version = 30, minimum_runtime_version = 30",
		"forward-only", "restore the pre-migration backup")
	queue := regexp.MustCompile(`(?s)CREATE TABLE app\.coverage_display_invalidations \((.*?)\n\);`).FindString(source)
	canonicalForbid(t, queue, "REFERENCES")
	canonicalForbid(t, source, "migration_baseline =", "GRANT SELECT", "DISABLE TRIGGER", "NO FORCE ROW LEVEL SECURITY",
		"CREATE OR REPLACE FUNCTION app.persist_coverage_route", "UPDATE app.workouts", "UPDATE app.path_segments",
		"UPDATE app.workout_segment_matches", "UPDATE app.workout_coverage_states")
	core, _, _ := strings.Cut(source, "-- Canonical geometry and tile readers are installed here.")
	canonicalForbid(t, core, "EXCEPTION WHEN")
	marker := strings.Index(source, "-- Canonical geometry and tile readers are installed here.")
	if marker < 0 || marker > strings.Index(source, "UPDATE app.schema_metadata") {
		t.Fatal("geometry integration marker must precede the final schema update")
	}
}

func TestCanonicalCoverageDisplayPolicyAndIdentity(t *testing.T) {
	source := canonicalCoverageSQL(t)
	normalize := coverageDisplayFunction(t, source, "coverage_display_normalize")
	identity := coverageDisplayFunction(t, source, "coverage_display_identity")
	canonicalRequire(t, source,
		"NOT BETWEEN 180000 AND 189999", "current_setting('server_encoding') <> 'UTF8'",
		"to_regcollation('pg_catalog.pg_c_utf8')", "collprovider = 'b'",
		"CHECK (policy_version = 'coverage-display-identity-v1')", "CHECK (label_locale = 'en')")
	canonicalRequire(t, normalize,
		"normalize(casefold(normalize(value, NFC) COLLATE pg_catalog.pg_c_utf8), NFC)",
		`U&'[\0009-\000D\0020\0085\00A0\1680\2000-\200A\2028\2029\202F\205F\3000]+'`,
		"btrim(regexp_replace(")
	canonicalForbid(t, normalize, "unaccent", "normalized_name", "[[:space:]]", "lower(", "replace(value, 'east'", "replace(value, 'west'")
	canonicalRequire(t, identity,
		"ARRAY['workouts-explorer/coverage-display', 'coverage-display-identity-v1', key_region, key_name, key_locality, discriminator]",
		"convert_to(component, 'UTF8')", "int4send(octet_length(encoded))", "sha256(payload)",
		"substring(sha256(payload) FROM 1 FOR 16)",
		"set_byte(digest, 6, (get_byte(digest, 6) & 15) | 128)",
		"set_byte(digest, 8, (get_byte(digest, 8) & 63) | 128)")
	canonicalForbid(t, identity, "source_kind", "source_id", "broad_class", "generation", "start_date", "label_locale", "md5")
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	canonicalRequire(t, prepare,
		"CASE WHEN normalized_name = '' THEN source_kind || '/' || source_id::text ELSE '' END key_discriminator",
		"app.coverage_display_identity(key_region, normalized_name, normalized_locality, key_discriminator)",
		"(member.key_region, member.normalized_name, member.normalized_locality, member.key_discriminator) IS DISTINCT FROM",
		"RAISE EXCEPTION 'canonical coverage display UUID collision' USING ERRCODE = '23505'")
}

func TestCanonicalCoverageDisplayHistoricalUniverse(t *testing.T) {
	source := canonicalCoverageSQL(t)
	visits := coverageDisplayFunction(t, source, "coverage_display_source_visits")
	canonicalRequire(t, visits,
		"workout.deletion_requested_at IS NULL", "workout.local_start_date IS NOT NULL", "route.route IS NOT NULL",
		"coverage.applied_route_input_revision IS NOT NULL",
		"WHERE path.name IS NOT NULL OR partial.qualifies_without_name",
		"GROUP BY eligible.id, eligible.local_start_date, eligible.started_at, partial.entity_id",
		"CASE WHEN bool_or(visit.education_key_present) THEN 'other'",
		"COALESCE(min(visit.education_name COLLATE \"C\"), min(visit.name COLLATE \"C\"))",
		"min(visit.locality_name COLLATE \"C\") locality_name",
		"bool_or(visit.local_start_date BETWEEN selected_start AND selected_end) has_range",
		"COALESCE(context.range_locality, CASE WHEN NOT identity.has_range THEN context.historical_locality END, identity.locality_name)",
		"min(context.locality_name COLLATE \"C\") historical_locality",
		"LEFT JOIN app.workout_park_display_bounds bounds",
		"FROM path_visits visit JOIN path_identity identity USING (entity_id)",
		"FROM park_visits visit JOIN park_identity identity USING (entity_id)")
	eligible := coverageEntityStatsCTE(t, visits, "eligible")
	canonicalForbid(t, eligible, "selected_start", "selected_end", "processing_state = 'current'")
	for _, name := range []string{"path_visits", "park_visits"} {
		body := coverageEntityStatsCTE(t, visits, name)
		canonicalForbid(t, body, "BETWEEN", "selected_start", "selected_end")
	}
	context := coverageEntityStatsCTE(t, visits, "path_context")
	canonicalForbid(t, context, "path_visits", "qualifies_without_name", "coverage_paths")
	canonicalForbid(t, visits, "app.map_selection_coverage_entities", "map_selection_workouts", "focused_workout_id",
		"app.workout_segment_matches", "app.path_segments", "ST_", "normalized_name")
}

func TestCanonicalCoverageDisplayActualPlaceBeforeIdentity(t *testing.T) {
	source := canonicalCoverageSQL(t)
	visits := coverageDisplayFunction(t, source, "coverage_display_source_visits")
	resolved := coverageEntityStatsCTE(t, visits, "resolved_sources")
	canonicalRequire(t, resolved, "SELECT 'path'::text source_kind", "SELECT 'park'::text", "UNION ALL",
		"identity.locality_name) locality_name", "park.name, park.locality_name, identity.region_id")
	_, place, found := strings.Cut(visits, "SELECT source.source_kind, source.source_id")
	if !found {
		t.Fatal("both source kinds must share the actual displayed-place resolver after their union")
	}
	// A NULL locality uses the catalog label, or the title-cased last region
	// slug without a catalog. A non-NULL empty locality never falls back.
	canonicalRequire(t, place, "COALESCE(source.locality_name, catalog.display_name,",
		"regexp_replace(source.region_id, '^.*:', '')", "'[-_]+'", "WITH ORDINALITY part(word, position)",
		"WHERE part.word <> ''", "ORDER BY part.position", "source.region_id) locality_name",
		"upper(left(part.word, 1) COLLATE pg_catalog.pg_c_utf8)",
		"lower(substr(part.word, 2) COLLATE pg_catalog.pg_c_utf8)",
		"FROM resolved_sources source LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = source.region_id")
	canonicalForbid(t, place, "NULLIF(source.locality_name", "NULLIF(catalog.display_name", "source_kind =",
		"COALESCE(source.locality_name, '')", "initcap(")
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	canonicalRequire(t, prepare, "app.coverage_display_normalize(COALESCE(locality_name, '')) normalized_locality",
		"broad_class, name, locality_name, region_id, region_name,", "representative.locality_name, representative.region_id")
	if strings.Index(prepare, "app.coverage_display_source_visits(") > strings.Index(prepare, "normalized_locality") {
		t.Fatal("catalog/slug place fallback must resolve before normalized identity keys are computed")
	}
}

func TestCanonicalCoverageDisplaySourceRangeBoundsAndVisibility(t *testing.T) {
	source := canonicalCoverageSQL(t)
	definition := regexp.MustCompile(`(?s)CREATE FUNCTION app\.coverage_display_source_visits\(.*?RETURNS TABLE \((.*?)\)\s*LANGUAGE`).FindStringSubmatch(source)
	want := `source_kind text, source_id uuid, workout_id uuid, local_start_date date, started_at timestamptz,
        broad_class text, name text, locality_name text, region_id text, region_name text,
        minimum_longitude double precision, minimum_latitude double precision,
        maximum_longitude double precision, maximum_latitude double precision,
        range_minimum_longitude double precision, range_minimum_latitude double precision,
        range_maximum_longitude double precision, range_maximum_latitude double precision`
	if len(definition) != 2 || compactSQL(definition[1]) != compactSQL(want) {
		t.Fatal("source helper must retain fourteen visit/metadata fields and append four explicit source-range bbox fields")
	}
	members := regexp.MustCompile(`(?s)CREATE TABLE app\.coverage_display_members \((.*?)\n\);`).FindString(source)
	for _, field := range []string{"range_minimum_longitude", "range_minimum_latitude", "range_maximum_longitude", "range_maximum_latitude"} {
		canonicalRequire(t, members, field+" double precision")
	}
	visits := coverageDisplayFunction(t, source, "coverage_display_source_visits")
	parkVisits := coverageEntityStatsCTE(t, visits, "park_visits")
	canonicalRequire(t, parkVisits, "bounds.workout_id = attribution.workout_id",
		"bounds.minimum_longitude, bounds.minimum_latitude, bounds.maximum_longitude, bounds.maximum_latitude")
	canonicalForbid(t, parkVisits, "JOIN park_bounds", "range_minimum", "range_maximum")
	parkBounds := coverageEntityStatsCTE(t, visits, "park_bounds")
	canonicalRequire(t, parkBounds, "FROM eligible JOIN app.workout_park_display_bounds bounds",
		"min(bounds.region_id COLLATE \"C\") FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_region_id",
		"min(bounds.region_id COLLATE \"C\") historical_region_id",
		"min(bounds.minimum_longitude) FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_minimum_longitude",
		"max(bounds.maximum_latitude) FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_maximum_latitude")
	canonicalForbid(t, parkBounds, "app.workout_park_attributions", "FROM park_visits", "ST_", ".geom")
	if regexp.MustCompile(`(?m)^\s*WHERE eligible\.local_start_date`).MatchString(parkBounds) {
		t.Fatal("historical park-region evidence must survive outside the selected date range")
	}
	parkIdentity := coverageEntityStatsCTE(t, visits, "park_identity")
	canonicalRequire(t, parkIdentity, "COALESCE(min(visit.region_id COLLATE \"C\") FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end), bounds.range_region_id, min(visit.region_id COLLATE \"C\"), bounds.historical_region_id)")
	pathIdentity := coverageEntityStatsCTE(t, visits, "path_identity")
	canonicalRequire(t, pathIdentity, "min(visit.minimum_longitude) FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end) range_minimum_longitude",
		"max(visit.maximum_latitude) FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end) range_maximum_latitude")
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	// A historical-only attributed park can still contribute range geometry on
	// an unattributed workout. Count dates and member bounds must stay separate.
	_, bounds, found := strings.Cut(prepare, "JOIN LATERAL (")
	if !found {
		t.Fatal("canonical range bounds must aggregate source members independently of workout visit dates")
	}
	bounds, _, _ = strings.Cut(bounds, ") representative ON true")
	canonicalRequire(t, bounds, "FROM app.coverage_display_members member",
		"min(member.range_minimum_longitude) OVER () canonical_minimum_longitude",
		"max(member.range_maximum_latitude) OVER () canonical_maximum_latitude",
		"member.account_id = target_account_id AND member.context_id = prepared_id",
		"AND member.canonical_id = summary.canonical_id")
	canonicalForbid(t, bounds, "FILTER", "in_range", "local_start_date", "workout_id")
	canonicalRequire(t, prepare, "FROM summaries summary JOIN LATERAL (",
		"WHERE summary.range_workout_count = 0 OR (representative.region_id IS NOT NULL",
		"AND representative.canonical_minimum_longitude IS NOT NULL AND representative.canonical_minimum_latitude IS NOT NULL",
		"AND representative.canonical_maximum_longitude IS NOT NULL AND representative.canonical_maximum_latitude IS NOT NULL)",
		"representative.canonical_minimum_longitude, representative.canonical_minimum_latitude, representative.canonical_maximum_longitude, representative.canonical_maximum_latitude")
	canonicalForbid(t, prepare, "min(minimum_longitude) FILTER (WHERE in_range)", "summary.minimum_longitude",
		"DELETE FROM app.coverage_display_members", "DELETE FROM app.coverage_display_visits")
}

func TestCanonicalCoverageDisplayBuildAndCacheLifecycle(t *testing.T) {
	source := canonicalCoverageSQL(t)
	prepare := coverageDisplayFunction(t, source, "prepare_coverage_display_context")
	anchors := []string{
		"app.coverage_display_selection_scope(", "IF NOT FOUND THEN RAISE EXCEPTION",
		"INSERT INTO app.coverage_display_contexts(", "WITH source_visits AS MATERIALIZED (",
		"INSERT INTO app.coverage_display_members(", "INSERT INTO app.coverage_display_visits(",
		"canonical coverage display UUID collision", "WITH visits AS MATERIALIZED (",
		"INSERT INTO app.coverage_display_entities(", "SET ready = true",
		"UPDATE app.map_selections SET coverage_display_context_id = prepared_id",
		"WITH unpinned AS MATERIALIZED (", "RETURN QUERY SELECT prepared_id, scope.revision",
	}
	previous := -1
	for _, anchor := range anchors {
		index := strings.Index(prepare, anchor)
		if index <= previous {
			t.Fatalf("missing/out-of-order atomic context build step %q", anchor)
		}
		previous = index
	}
	canonicalRequire(t, prepare,
		"RETURNING source_kind, source_id, canonical_id",
		"GROUP BY member.canonical_id, visit.workout_id, visit.local_start_date, visit.started_at",
		"ORDER BY (member.source_kind = 'park') DESC",
		"count(*) FILTER (WHERE in_range) range_workout_count", "count(*) all_time_workout_count",
		"ORDER BY local_start_date DESC, started_at, workout_id",
		"ON CONFLICT ON CONSTRAINT coverage_display_contexts_scope_key DO NOTHING",
		"AND context.ready", "AND NOT EXISTS (SELECT 1 FROM app.map_selections selection",
		"selection.expires_at > transaction_timestamp()", "context.revision = scope.revision AND context.ready",
		"interval '1 day'", "unpinned.reusable_rank > 8")
	if strings.Count(prepare, "app.coverage_display_source_visits(") != 1 {
		t.Fatal("one all-history source scan must supply membership and distinct canonical visits")
	}
	canonicalForbid(t, prepare, "UPDATE app.account_data_generations", "FOR UPDATE", "FOR SHARE", "pg_advisory",
		"app.map_selection_coverage_entities(", "SET start_date", "map_selection_workouts", "focused_workout_id", "EXCEPTION WHEN")
}

func TestCanonicalCoverageDisplayColdMetadataUsesCanonicalMemberProbes(t *testing.T) {
	prepare := coverageDisplayFunction(t, canonicalCoverageSQL(t), "prepare_coverage_display_context")
	_, metadata, found := strings.Cut(prepare, "WITH visits AS MATERIALIZED (")
	if !found {
		t.Fatal("cold metadata build must summarize materialized visits once")
	}
	for _, name := range []string{"visits", "summaries"} {
		if len(regexp.MustCompile(`\b`+name+` AS MATERIALIZED \(`).FindAllString(prepare, -1)) != 1 {
			t.Errorf("%s must have one materialized producer to prevent per-entity reaggregation", name)
		}
	}
	if strings.Count(metadata, "FROM app.coverage_display_members member") != 1 ||
		strings.Count(metadata, "FROM app.coverage_display_visits visit") != 1 {
		t.Fatal("metadata assembly must use one visit producer and one scoped canonical-member probe")
	}
	canonicalRequire(t, metadata, "FROM visits visit GROUP BY visit.canonical_id", "FROM summaries summary JOIN LATERAL (",
		"WHERE member.account_id = target_account_id AND member.context_id = prepared_id AND member.canonical_id = summary.canonical_id",
		"ORDER BY (member.source_kind = 'park') DESC", "LIMIT 1 ) representative ON true")
	canonicalForbid(t, metadata, "representatives AS", "range_bounds AS", "JOIN summaries", "GROUP BY member.canonical_id")
	_, probe, _ := strings.Cut(metadata, "JOIN LATERAL (")
	probe, _, _ = strings.Cut(probe, ") representative ON true")
	for _, window := range []string{
		"min(member.range_minimum_longitude) OVER () canonical_minimum_longitude",
		"min(member.range_minimum_latitude) OVER () canonical_minimum_latitude",
		"max(member.range_maximum_longitude) OVER () canonical_maximum_longitude",
		"max(member.range_maximum_latitude) OVER () canonical_maximum_latitude",
	} {
		canonicalRequire(t, probe, window)
		if strings.Index(probe, window) > strings.Index(probe, "LIMIT 1") {
			t.Fatal("all-member window bounds must be evaluated before representative LIMIT")
		}
	}
	canonicalRequire(t, canonicalCoverageSQL(t), "ON app.coverage_display_members(account_id, context_id, canonical_id, source_kind, source_id)")
	canonicalForbid(t, prepare, "enable_nestloop", "enable_hashjoin", "enable_mergejoin", "set_config(", "SET LOCAL")
}

func TestCanonicalCoverageDisplayWarmReuseDoesNotWriteSharedContext(t *testing.T) {
	prepare := coverageDisplayFunction(t, canonicalCoverageSQL(t), "prepare_coverage_display_context")
	canonicalForbid(t, prepare, "SET last_used_at", "DO UPDATE")
	if strings.Count(prepare, "UPDATE app.coverage_display_contexts") != 1 {
		t.Fatal("only the newly built context may be updated to ready")
	}
	_, warm, _ := strings.Cut(prepare, "IF inserted_id IS NULL THEN")
	warm, _, _ = strings.Cut(warm, "ELSE")
	canonicalRequire(t, warm, "SELECT context.id INTO prepared_id", "AND context.ready")
	canonicalForbid(t, warm, "UPDATE", "DELETE")
	_, afterBuild, found := strings.Cut(prepare, "SET ready = true")
	if !found {
		t.Fatal("missing new-build completion barrier")
	}
	canonicalRequire(t, afterBuild, "END IF; UPDATE app.map_selections SET coverage_display_context_id = prepared_id",
		"IF inserted_id IS NOT NULL THEN", "WITH unpinned AS MATERIALIZED (",
		"DELETE FROM app.coverage_display_contexts context USING unpinned",
		"END IF; RETURN QUERY SELECT prepared_id")
	if strings.Index(afterBuild, "IF inserted_id IS NOT NULL THEN") > strings.Index(afterBuild, "WITH unpinned AS MATERIALIZED (") {
		t.Fatal("warm reuse must not run shared-context GC")
	}
}

func TestCanonicalCoverageDisplayDeferredInvalidation(t *testing.T) {
	source := canonicalCoverageSQL(t)
	queue := coverageDisplayFunction(t, source, "enqueue_coverage_display_invalidations")
	bridge := coverageDisplayFunction(t, source, "queue_coverage_display_source_invalidation")
	metadata := coverageDisplayFunction(t, source, "queue_coverage_display_metadata_invalidation")
	advance := coverageDisplayFunction(t, source, "advance_account_data_generation")
	dirty := coverageDisplayFunction(t, coverageDisplaySQL(t), "mark_coverage_display_dirty")
	flush := coverageDisplayFunction(t, source, "flush_coverage_display_invalidations")
	canonicalRequire(t, source,
		"AFTER INSERT ON app.coverage_display_dirty FOR EACH ROW WHEN (NEW.scope = 'account')",
		"AFTER INSERT ON app.coverage_display_invalidations DEFERRABLE INITIALLY DEFERRED",
		"INSERT INTO app.coverage_display_revisions(account_id) SELECT id FROM app.accounts",
		"AFTER INSERT ON app.accounts FOR EACH ROW EXECUTE FUNCTION app.seed_coverage_display_revision()")
	canonicalRequire(t, queue, "SELECT DISTINCT txid_current(), account_id", "ORDER BY account_id ON CONFLICT DO NOTHING")
	canonicalRequire(t, bridge, "app.enqueue_coverage_display_invalidations(ARRAY[NEW.account_id])")
	canonicalRequire(t, metadata,
		"OLD.name, OLD.locality_name", "NEW.name, NEW.locality_name",
		"OLD.applied_route_input_revision", "NEW.applied_route_input_revision",
		"old_row->>'applied_route_input_revision' IS NOT NULL", "new_row->>'applied_route_input_revision' IS NOT NULL",
		"OLD.region_id, OLD.display_name", "NEW.region_id, NEW.display_name",
		"old_row - 'updated_at'", "new_row - 'updated_at'",
		"ORDER BY evidence.account_id ON CONFLICT DO NOTHING",
		"app.enqueue_coverage_display_invalidations(ARRAY[old_account, new_account])")
	canonicalRequire(t, advance, "to_jsonb(OLD) - 'updated_at'", "to_jsonb(NEW) - 'updated_at'",
		"app.enqueue_coverage_display_invalidations(ARRAY[old_account, new_account])")
	canonicalRequire(t, dirty, "OLD.covered_meters, public.ST_AsEWKB(OLD.geom)", "NEW.covered_meters, public.ST_AsEWKB(NEW.geom)",
		"IS NOT DISTINCT FROM", "OLD.highway, OLD.tags", "NEW.highway, NEW.tags",
		"OLD.account_id, 'workout', OLD.workout_id", "NEW.account_id, 'workout', NEW.workout_id",
		"OLD.account_id, 'segment', OLD.region_id, OLD.generation_id, OLD.physical_segment_id",
		"NEW.account_id, 'segment', NEW.region_id, NEW.generation_id, NEW.physical_segment_id",
		"FROM unnest(ARRAY[old_account, new_account]) account_id",
		"ORDER BY account_id ON CONFLICT DO NOTHING")
	for _, body := range []string{queue, bridge, metadata, advance, dirty} {
		canonicalForbid(t, body, "UPDATE app.account_data_generations", "UPDATE app.coverage_display_revisions",
			"rebuild_coverage", "flush_coverage", "FOR UPDATE")
	}
	for _, table := range []string{"coverage_paths", "coverage_parks", "workout_park_attributions", "workout_coverage_states", "coverage_region_catalog"} {
		canonicalRequire(t, source, "AFTER INSERT OR UPDATE OR DELETE ON app."+table)
	}
	canonicalRequire(t, flush, "WHERE txid = txid_current() ORDER BY account_id",
		"SET generation = generation WHERE account_id = target",
		"PERFORM app.flush_coverage_display_precomputations(target)",
		"SET revision = revision + 1", "last_invalidated_transaction_id IS DISTINCT FROM txid_current()",
		"IF FOUND THEN UPDATE app.account_data_generations", "ELSE generation + 1 END",
		"DELETE FROM app.coverage_display_invalidations WHERE txid = txid_current() AND account_id = target")
	if strings.Index(flush, "SET generation = generation") > strings.Index(flush, "SET revision = revision + 1") {
		t.Fatal("all deferred flushes must acquire generation before revision")
	}
	canonicalForbid(t, source, "CREATE OR REPLACE FUNCTION app.flush_coverage_display_precomputations",
		"DROP TRIGGER coverage_display_dirty_account_flush", "DROP TRIGGER workout_routes_data_generation_after_write",
		"DROP TRIGGER workouts_data_generation_after_write", "DROP TRIGGER workout_coverage_states_data_generation_after_current")
}

func TestCanonicalCoverageDisplayExactGeometryChangeGuard(t *testing.T) {
	source := canonicalCoverageSQL(t)
	definition := regexp.MustCompile(`(?s)CREATE FUNCTION app\.mark_coverage_display_dirty\(\).*?\$function\$;`).FindString(coverageDisplaySQL(t))
	canonicalRequire(t, definition, "SET search_path = pg_catalog, app AS $function$",
		"OLD.covered_meters, public.ST_AsEWKB(OLD.geom)) IS NOT DISTINCT FROM",
		"NEW.covered_meters, public.ST_AsEWKB(NEW.geom)) THEN RETURN NULL")
	canonicalForbid(t, definition, "search_path = pg_catalog, app, public", "OLD.covered_meters, OLD.geom",
		"NEW.covered_meters, NEW.geom", "ST_Equals(", "ST_AsText(", "ST_AsBinary(", "ST_Normalize(")
	// Every raw geometry field in restricted-path guards must be converted to
	// exact SRID-aware bytes, not compared through a geometry record operator.
	functions := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION app\.\w+\s*\(.*?\$function\$;`).FindAllString(source, -1)
	for _, function := range functions {
		declaration, body, _ := strings.Cut(function, "AS $function$")
		if strings.Contains(declaration, "app, public") {
			continue
		}
		body = regexp.MustCompile(`(?i)public\.ST_AsEWKB\((?:OLD|NEW)\.geom\)`).ReplaceAllString(body, "exact_geometry_bytes")
		if regexp.MustCompile(`(?i)\b(?:OLD|NEW)\.geom\b`).MatchString(body) {
			t.Fatal("restricted-path function contains an unqualified raw geometry comparison")
		}
	}
}

func TestCanonicalCoverageDisplayReadOnlyABIAndAuthorization(t *testing.T) {
	source := canonicalCoverageSQL(t)
	scope := coverageDisplayFunction(t, source, "coverage_display_selection_scope")
	authorized := coverageDisplayFunction(t, source, "coverage_display_authorized_context")
	canonicalRequire(t, scope,
		"selection.session_id = target_session_id", "selection.generation = target_generation",
		"generation.generation = target_generation", "selection.expires_at > transaction_timestamp()",
		"session_row.revoked_at IS NULL", "session_row.expires_at > transaction_timestamp()",
		"principal.disabled_at IS NULL", "account.state = 'active'", "target_account_id = app.current_account_id()",
		"target_session_id = app.current_session_id()")
	canonicalRequire(t, authorized, "IF NOT FOUND THEN RETURN NULL", "context.id = scope.bound_context_id",
		"context.generation = target_generation", "context.revision = scope.revision", "context.ready",
		"context.start_date = scope.start_date", "context.end_date = scope.end_date",
		"context.policy_version = 'coverage-display-identity-v1'", "context.label_locale = 'en'", "ERRCODE = '55000'")
	result := regexp.MustCompile(`(?s)RETURNS TABLE \((.*?)\)\s*LANGUAGE`)
	old, err := migrations.Files.ReadFile("019_coverage_explorer_reads.sql")
	if err != nil {
		t.Fatal(err)
	}
	oldDefinition := regexp.MustCompile(`(?s)CREATE FUNCTION app\.map_selection_coverage_entities\s*\(.*?\$function\$;`).FindString(string(old))
	want := result.FindStringSubmatch(oldDefinition)
	for _, name := range []string{"map_selection_coverage_entities", "map_selection_coverage_entity_stats", "map_selection_coverage_entity_members"} {
		body := coverageDisplayFunction(t, source, name)
		canonicalRequire(t, body, "app.coverage_display_authorized_context(", "IF authorized_id IS NULL THEN RETURN",
			"entity.account_id = target_account_id", "entity.context_id = authorized_id", "entity.range_workout_count > 0")
		canonicalForbid(t, body, "INSERT INTO", "UPDATE app.", "DELETE FROM", "prepare_coverage", "source_visits(",
			"app.workouts", "app.workout_routes", "count(", "GROUP BY", "map_selection_workouts")
		if regexp.MustCompile(`(?i)\bst_\w+\s*\(`).MatchString(body) {
			t.Errorf("%s must not perform geometry work", name)
		}
		if name != "map_selection_coverage_entities" {
			canonicalRequire(t, body, "entity.canonical_id = target_entity_id", "entity.entity_kind = target_entity_kind")
		}
		if name == "map_selection_coverage_entity_members" {
			canonicalRequire(t, body, "SELECT member.source_kind, member.source_id", "member.canonical_id = entity.canonical_id")
			continue
		}
		definition := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION app\.` + name + `\s*\(.*?\$function\$;`).FindString(source)
		got := result.FindStringSubmatch(definition)
		if len(got) != 2 || len(want) != 2 || compactSQL(got[1]) != compactSQL(want[1]) {
			t.Errorf("%s must preserve the shipped list's exact 21-column ABI", name)
		}
	}
	validation := coverageDisplayFunction(t, source, "validate_map_selection")
	canonicalRequire(t, validation, "to_jsonb(OLD) - 'coverage_display_context_id'", "to_jsonb(NEW) - 'coverage_display_context_id'",
		"RAISE EXCEPTION 'map selections are immutable'", "context.ready", "context.revision = revision.revision")
	focus := coverageDisplayFunction(t, source, "validate_map_selection_focus")
	canonicalRequire(t, focus, "TG_OP = 'UPDATE'", "to_jsonb(OLD) - 'coverage_display_context_id'",
		"to_jsonb(NEW) - 'coverage_display_context_id'", "THEN RETURN NEW",
		"workout.local_start_date BETWEEN NEW.start_date AND NEW.end_date")
}

func TestCanonicalCoverageDisplayFunctionOwnershipAndACL(t *testing.T) {
	source := canonicalCoverageSQL(t)
	revokes := regexp.MustCompile(`(?is)\bREVOKE ALL ON FUNCTION\b[^;]+;`).FindString(source)
	functions := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION app\.(\w+)\s*\((.*?)\)\s*RETURNS`).FindAllStringSubmatch(source, -1)
	for _, function := range functions {
		owner := regexp.MustCompile(`ALTER FUNCTION app\.` + function[1] + `\([^;]*\) OWNER TO workouts_security_owner;`)
		if !owner.MatchString(source) {
			t.Errorf("missing private function owner for %s", function[1])
		}
		switch function[1] {
		case "map_selection_coverage_entity_detail", "coverage_mvt", "map_selection_coverage_focus":
			// CREATE OR REPLACE preserves these shipped 020 ACLs verbatim.
			continue
		}
		if !strings.Contains(revokes, "app."+function[1]+"(") {
			t.Errorf("missing function ACL for %s", function[1])
		}
	}
	canonicalRequire(t, source,
		"RETURNS TABLE (context_id uuid, display_revision bigint, policy_version text) LANGUAGE plpgsql VOLATILE SECURITY DEFINER",
		"GRANT UPDATE(coverage_display_context_id) ON app.map_selections TO workouts_security_owner",
		"REVOKE ALL ON FUNCTION app.coverage_display_normalize(text)",
		"app.coverage_display_source_visits(uuid, date, date), app.enqueue_coverage_display_invalidations(uuid[])",
		"app.coverage_display_selection_scope(uuid, uuid, uuid, bigint), app.coverage_display_authorized_context(uuid, uuid, uuid, bigint)",
		"GRANT EXECUTE ON FUNCTION app.prepare_coverage_display_context(uuid, uuid, uuid, bigint)",
		"app.map_selection_coverage_entity_members(uuid, uuid, uuid, bigint, text, uuid) TO workouts_api")
	grants := regexp.MustCompile(`(?is)\bGRANT\s+EXECUTE\b[^;]+;`).FindAllString(source, -1)
	if len(grants) != 1 {
		t.Fatalf("expected one explicit API-only execute allowlist, got %v", grants)
	}
	canonicalForbid(t, grants[0], "workouts_tiles", "workouts_worker", "workouts_coverage_worker", "source_visits", "authorized_context")
}

func TestCanonicalCoverageDisplayMapReaderABIAndACL(t *testing.T) {
	source := canonicalCoverageSQL(t)
	old, err := migrations.Files.ReadFile("020_facility_coverage_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, reader := range []struct{ name, signature, role string }{
		{"map_selection_coverage_entity_detail", "uuid, uuid, uuid, bigint, text, uuid", "workouts_api"},
		{"map_selection_coverage_focus", "uuid, uuid, uuid, bigint, uuid", "workouts_api"},
		{"coverage_mvt", "integer, integer, integer, json", "workouts_tiles"},
	} {
		declaration := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION app\.` + reader.name + `\s*\((.*?)\)\s*RETURNS\s+(.*?)\s+LANGUAGE`)
		before, after := declaration.FindStringSubmatch(string(old)), declaration.FindStringSubmatch(source)
		if len(before) != 3 || len(after) != 3 || compactSQL(before[1]) != compactSQL(after[1]) || compactSQL(before[2]) != compactSQL(after[2]) {
			t.Fatalf("%s must retain shipped argument names, types, defaults and ordered result ABI", reader.name)
		}
		definition := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION app\.` + reader.name + `\s*\(.*?\$function\$;`).FindString(source)
		canonicalRequire(t, definition, "STABLE SECURITY DEFINER", "SET search_path = pg_catalog, app, public", "SET jit = off")
		body := coverageDisplayFunction(t, source, reader.name)
		canonicalForbid(t, body, "INSERT INTO", "UPDATE app.", "DELETE FROM", "prepare_coverage_display_context",
			"coverage_display_source_visits(", "rebuild_coverage", "flush_coverage", "FOR UPDATE", "FOR SHARE",
			"ST_MakeLine", "ST_LineMerge", "ST_Snap", "ST_Simplify")
		canonicalRequire(t, string(old), "GRANT EXECUTE ON FUNCTION app."+reader.name+"("+reader.signature+") TO "+reader.role+";")
	}
	_, fragment, _ := strings.Cut(source, "-- Canonical geometry and tile readers are installed here.")
	canonicalForbid(t, fragment, "GRANT EXECUTE", "REVOKE ALL ON FUNCTION", "CREATE FUNCTION app.")
}

func TestCanonicalCoverageDisplayMapReaderCapabilityBarriers(t *testing.T) {
	source := canonicalCoverageSQL(t)
	for _, name := range []string{"coverage_mvt", "map_selection_coverage_focus"} {
		body := coverageDisplayFunction(t, source, name)
		validation, geometry, found := strings.Cut(body, "IF NOT FOUND THEN")
		if !found {
			t.Fatalf("%s must authorize before reading geometry", name)
		}
		canonicalRequire(t, validation,
			"session_row.id = selection.session_id", "principal.id = session_row.principal_id",
			"account_id = selection.account_id", "selection.id = target_selection_id", "selection.account_id = target_account_id",
			"selection.session_id = target_session_id", "selection.generation = target_generation",
			"generation.generation = target_generation", "selection.expires_at > transaction_timestamp()",
			"session_row.revoked_at IS NULL", "session_row.expires_at > transaction_timestamp()",
			"principal.disabled_at IS NULL", "account.state = 'active'",
			"context.account_id = selection.account_id", "context.id = selection.coverage_display_context_id",
			"revision.account_id = selection.account_id", "context.generation = target_generation",
			"context.revision = revision.revision", "context.start_date = selection.start_date", "context.end_date = selection.end_date",
			"context.ready", "context.policy_version = 'coverage-display-identity-v1'", "context.label_locale = 'en'")
		canonicalForbid(t, validation, "app.workout_segment_matches", "app.path_segments")
		canonicalRequire(t, geometry, "JOIN app.coverage_display_members member ON member.account_id = target_account_id",
			"member.context_id = display_context_id", "JOIN app.coverage_display_entities entity ON entity.account_id = target_account_id",
			"entity.context_id = display_context_id", "entity.canonical_id = shapes.canonical_id", "entity.range_workout_count > 0")
		canonicalForbid(t, body, "map_selection_coverage_entity_stats(", "coverage_display_authorized_context(", "coverage_display_selection_scope(")
		if name == "coverage_mvt" {
			canonicalForbid(t, body, "app.current_account_id()", "app.current_session_id()", "EXCEPTION WHEN OTHERS", "serialization_failure")
			canonicalRequire(t, validation, "z IS NULL OR x IS NULL OR y IS NULL", "target_generation IS NULL OR target_generation < 1",
				"count(*) FROM json_object_keys(query_params)) <> 4", "query_params::jsonb ?& ARRAY[",
				"EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range")
			canonicalRequire(t, geometry, "RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501'")
		} else {
			canonicalRequire(t, validation, "target_account_id = app.current_account_id()", "target_session_id = app.current_session_id()",
				"selected.workout_id = target_workout_id", "coverage.applied_route_input_revision IS NOT NULL",
				"workout.id = target_workout_id", "workout.deletion_requested_at IS NULL", "route.route IS NOT NULL",
				"workout.local_start_date BETWEEN selection.start_date AND selection.end_date")
			canonicalRequire(t, geometry, "RETURN NULL; END IF", "match.workout_id = target_workout_id")
			canonicalForbid(t, body, "selected.focused_workout_id", "EXCEPTION WHEN")
		}
	}
}

func TestCanonicalCoverageDisplayWholeGroupGeometryAndBounds(t *testing.T) {
	source := canonicalCoverageSQL(t)
	detail := coverageDisplayFunction(t, source, "map_selection_coverage_entity_detail")
	canonicalRequire(t, detail, "WITH metadata AS MATERIALIZED (", "app.map_selection_coverage_entity_stats(",
		"SELECT metadata.*, detail.* FROM metadata CROSS JOIN LATERAL (", "OFFSET 0",
		"member.context_id = selection.coverage_display_context_id", "member.canonical_id = metadata.entity_id",
		"JOIN members member ON member.source_kind = 'path'", "JOIN members member ON member.source_kind = 'park'",
		"segment.tags->>'workouts:park_id' = member.source_id::text",
		"workout.local_start_date BETWEEN selection.start_date AND selection.end_date",
		"workout.deletion_requested_at IS NULL", "route.route IS NOT NULL", "coverage.applied_route_input_revision IS NOT NULL",
		"ST_UnaryUnion(ST_Collect(geom))", "ST_AsGeoJSON(fitted.geom)::jsonb")
	canonicalForbid(t, detail, "map_selection_coverage_entities(", "map_selection_workouts", "focused_workout_id",
		"metadata.entity_kind = 'path'", "metadata.entity_kind = 'park'", "ST_Intersection", "ST_AsMVTGeom",
		"ST_XMin(fitted.geom)", "ST_XMax(fitted.geom)")
	old, err := migrations.Files.ReadFile("020_facility_coverage_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	fit := regexp.MustCompile(`(?s)fitted AS \((.*?FROM projected GROUP BY geom)\s*\)`)
	before, after := fit.FindStringSubmatch(string(old)), fit.FindStringSubmatch(detail)
	if len(before) != 2 || len(after) != 2 || compactSQL(before[1]) != compactSQL(after[1]) {
		t.Fatal("detail must preserve the two independent 500m Web Mercator fit axes from 020")
	}
	for _, name := range []string{"map_selection_coverage_entity_detail", "coverage_mvt", "map_selection_coverage_focus"} {
		body := coverageDisplayFunction(t, source, name)
		canonicalRequire(t, body, "'driveway', 'parking_aisle'", "IS DISTINCT FROM 'parking'",
			"path.name IS NOT NULL", "'state_park', 'national_park'", "workouts:education_id", "UNION ALL")
		canonicalForbid(t, body, "sum(", "min(path.name)", "app.coverage_display_identity(")
	}
}

func TestCanonicalCoverageDisplayTileAndFocusProvenance(t *testing.T) {
	source := canonicalCoverageSQL(t)
	tile := coverageDisplayFunction(t, source, "coverage_mvt")
	for _, layer := range []string{"coverage", "coverage_parks", "coverage_focus", "coverage_parks_focus"} {
		canonicalRequire(t, tile, "ST_AsMVT(rows, '"+layer+"', 4096, 'geometry')")
	}
	canonicalRequire(t, tile, "upper(replace(entity.canonical_id::text, '-', '')) \"entityId\"",
		"entity.entity_kind \"entityKind\"", "upper(replace(shapes.source_id::text, '-', '')) \"sourceEntityId\"",
		"shapes.source_kind \"sourceEntityKind\"", "entity.range_workout_count \"rangeWorkoutCount\"",
		"app.coverage_count_bucket(entity.range_workout_count)", "GROUP BY canonical_id, source_kind, source_id",
		"ST_AsMVTGeom(ST_Transform(shapes.geom, 3857), bounds::box2d, 4096, 64, true)",
		"JOIN eligible ON eligible.id = selected_workout.workout_id", "pieces.workout_id = selected.focused_workout_id",
		"member.source_id = (checked.tags->>'workouts:park_id')::uuid")
	features := coverageEntityStatsCTE(t, tile, "features")
	canonicalForbid(t, features, "checked", "coverage_paths", "min(", "count(", "GROUP BY")
	focus := coverageDisplayFunction(t, source, "map_selection_coverage_focus")
	canonicalRequire(t, focus, "'entityId', upper(replace(canonical_id::text, '-', '')), 'entityKind', entity_kind",
		"'sourceEntityId', upper(replace(source_id::text, '-', '')), 'sourceEntityKind', source_kind",
		"'rangeWorkoutCount', range_workout_count, 'countBucket', app.coverage_count_bucket(range_workout_count)",
		"'geometry', ST_AsGeoJSON(geom, 6)::jsonb", "'[]'::jsonb",
		"member.source_id = (checked.tags->>'workouts:park_id')::uuid")
}
