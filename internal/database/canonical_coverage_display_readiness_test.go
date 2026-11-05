package database

import (
	"crypto/md5"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func canonicalDisplayMigration(t *testing.T) string {
	t.Helper()
	raw, err := migrations.Files.ReadFile("030_coverage_display.sql")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(raw), "-- +goose Down")[0]
}

func canonicalDisplayFunctions(source string) [][]string {
	functions := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION app\.(\w+)\s*\((.*?)\)\s*RETURNS\s+(.*?)\s+AS \$function\$(.*?)\$function\$;`).FindAllStringSubmatch(source, -1)
	var canonical [][]string
	for _, function := range functions {
		switch function[1] {
		case "rebuild_coverage_display_precomputations", "flush_coverage_display_precomputations", "flush_coverage_display_dirty_account":
			// These evidence-only helpers have their own readiness contract.
		default:
			canonical = append(canonical, function)
		}
	}
	return canonical
}

func TestCanonicalCoverageDisplayReadinessComposition(t *testing.T) {
	if SupportedSchemaVersion != 31 {
		t.Fatalf("supported schema=%d, want 31", SupportedSchemaVersion)
	}
	want := "SELECT (" + runtimeReadinessSQL + ") AND (" + runtimeDeferredReadinessSQL + ") AND (" + runtimeCohortReadinessSQL + ") AND (" + runtimeCoverageDisplayReadinessSQL + ") AND (" + runtimeCanonicalCoverageDisplayReadinessSQL + ") AND (" + runtimeIngestPhaseReadinessSQL + ") AND (" + runtimeTileReadinessSQL + ") AND (" + runtimeTileLODReadinessSQL + ") AND (" + runtimeCoverageMetadataReadinessSQL + ") AND (" + runtimeRouteGapReadinessSQL + ")"
	if runtimeFullReadinessSQL != want || !strings.Contains(runtimeCanonicalCoverageDisplayReadinessSQL, "SELECT $1 < 30 OR (") {
		t.Fatal("Ready must compose all previous contracts and the version-gated canonical contract")
	}
	for _, required := range []string{
		"schema_version>=30", "minimum_runtime_version>=30", "migration_baseline=27", "BETWEEN 180000 AND 189999",
		"current_setting('server_encoding')='UTF8'", "to_regcollation('pg_catalog.pg_c_utf8')", "collprovider='b'",
		"collencoding=pg_char_to_encoding('UTF8')", "coverage-display-identity-v1",
	} {
		if !strings.Contains(runtimeCanonicalCoverageDisplayReadinessSQL, required) {
			t.Errorf("missing policy/runtime guard %q", required)
		}
	}
	for _, forbidden := range []string{"regexp_replace(p.prosrc", "postcss", "styles.css", "coverage_display_policy"} {
		if strings.Contains(strings.ToLower(runtimeCanonicalCoverageDisplayReadinessSQL), forbidden) {
			t.Errorf("unexpected readiness dependency %q", forbidden)
		}
	}
}

func TestCanonicalCoverageDisplayReadinessCatalogContract(t *testing.T) {
	for _, required := range []string{
		"c.relkind<>'r' OR c.relpersistence<>'p'", "pg_get_userbyid(c.relowner)<>'workouts_security_owner'",
		"NOT c.relrowsecurity OR NOT c.relforcerowsecurity", "FROM pg_policy p WHERE p.polrelid=c.oid)<>1",
		"p.polname=expected.name||'_owner_policy'", "p.polroles=ARRAY['workouts_security_owner'::regrole::oid]",
		"pg_get_expr(p.polqual,p.polrelid)='true' AND pg_get_expr(p.polwithcheck,p.polrelid)='true'",
		"IS DISTINCT FROM expected.columns", "a.attidentity<>''", "a.attgenerated::text<>", "a.atthasdef<>(d.oid IS NOT NULL)",
		"pg_get_expr(d.adbin,d.adrelid) IS DISTINCT FROM CASE", "THEN 'canonical_id'", "THEN '1'",
		"WHEN 'policy_version' THEN '''coverage-display-identity-v1''::text'", "WHEN 'label_locale' THEN '''en''::text'",
		"WHEN 'ready' THEN 'false'", "WHEN 'id' THEN 'gen_random_uuid()'", "THEN 'pg_catalog.\"C\"'::regcollation::oid",
		"cardinality(expected.definitions)", "k.convalidated AND k.conenforced AND NOT k.condeferrable AND NOT k.condeferred",
		"pg_get_constraintdef(k.oid)=definition", "PRIMARY KEY (txid, account_id)",
		"UNIQUE (account_id, generation, revision, start_date, end_date, policy_version, label_locale)",
		"FOREIGN KEY (account_id, context_id) REFERENCES app.coverage_display_contexts(account_id, id) ON DELETE CASCADE",
		"FOREIGN KEY (account_id, coverage_display_context_id) REFERENCES app.coverage_display_contexts(account_id, id) ON DELETE SET NULL (coverage_display_context_id)",
		"NOT a.attisdropped AND NOT a.attnotnull AND NOT a.atthasdef", "has_column_privilege('workouts_security_owner',a.attrelid,a.attnum,'UPDATE')",
		"has_column_privilege(r.oid,a.attrelid,a.attnum,'UPDATE')",
		"NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive OR NOT i.indimmediate",
		"i.indisunique OR i.indisprimary OR i.indisexclusion OR i.indnullsnotdistinct", "i.indkey::text<>array_to_string(k.conkey,' ')",
		"pg_get_expr(i.indpred,i.indrelid) IS DISTINCT FROM expected.predicate", "i.indnkeyatts<>cardinality(expected.columns)",
		"unnest(i.indoption) option WHERE option<>0", "NOT op.opcdefault OR op.opcnamespace<>'pg_catalog'::regnamespace",
		"aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner)))", "aclexplode(a.attacl)",
		"has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')",
		"has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')",
		"p.prosecdef<>expected.definer", "p.proisstrict<>expected.strict", "p.proparallel::text<>expected.parallel",
		"p.provolatile::text<>expected.volatility", "p.prolang<>(SELECT oid FROM pg_language WHERE lanname=expected.language)",
		"p.proretset<>expected.returns_set", "p.pronargdefaults<>expected.defaults OR p.proleakproof",
		"pg_get_function_result(p.oid)<>expected.result OR p.proconfig IS DISTINCT FROM expected.config",
		"md5(p.prosrc)<>expected.body_hash", "has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api",
		"has_function_privilege('workouts_tiles',p.oid,'EXECUTE')<>expected.tiles", "acl.is_grantable OR NOT ((expected.api",
		"t.tgisinternal OR t.tgenabled<>'O' OR t.tgtype<>expected.events", "t.tgfoid IS DISTINCT FROM to_regprocedure(expected.function)",
		"t.tgattr::text<>'' OR t.tgnargs<>0 OR t.tgargs<>''::bytea", "t.tgdeferrable<>expected.deferred OR t.tginitdeferred<>expected.deferred",
		"substring(pg_get_triggerdef(t.oid) FROM ' WHEN \\((.*)\\) EXECUTE FUNCTION ') IS DISTINCT FROM expected.qual",
		"('app.queue_coverage_display_metadata_invalidation()',5)", "('app.flush_coverage_display_invalidations()',1)",
	} {
		if !strings.Contains(runtimeCanonicalCoverageDisplayReadinessSQL, required) {
			t.Errorf("missing canonical catalog guard %q", required)
		}
	}
	source := canonicalDisplayMigration(t)
	tables := regexp.MustCompile(`CREATE TABLE app\.(coverage_display_(?:revisions|contexts|members|visits|entities|invalidations))`).FindAllStringSubmatch(source, -1)
	if len(tables) != 6 {
		t.Fatalf("want six private tables, got %d", len(tables))
	}
	for _, table := range tables {
		if !strings.Contains(runtimeCanonicalCoverageDisplayReadinessSQL, "('"+table[1]+"',ARRAY[") {
			t.Errorf("unprotected private table %s", table[1])
		}
		if strings.Contains(runtimeCanonicalCoverageDisplayReadinessSQL, "FROM app."+table[1]+" ") {
			t.Errorf("restricted readiness must not query private rows in %s", table[1])
		}
	}
	indexes := regexp.MustCompile(`CREATE INDEX (\w+)\s+ON (app\.(?:coverage_display_\w+|map_selections))\s*\(([^)]+)\)`).FindAllStringSubmatch(source, -1)
	if len(indexes) != 6 {
		t.Fatalf("want six canonical lookup/GC indexes, got %d", len(indexes))
	}
	for _, index := range indexes {
		want := fmt.Sprintf("('app.%s','%s',ARRAY['%s']", index[1], index[2], strings.Join(strings.Split(index[3], ", "), "','"))
		if !strings.Contains(runtimeCanonicalCoverageDisplayReadinessSQL, want) {
			t.Errorf("missing index %s", want)
		}
	}
}

func TestCanonicalCoverageDisplayReadinessBodyFingerprints(t *testing.T) {
	functions := canonicalDisplayFunctions(canonicalDisplayMigration(t))
	fingerprints := regexp.MustCompile(`(?s)\('app\.([a-z_]+)\([^']*\)'.*?,'([a-f0-9]{32})'\)`).FindAllStringSubmatch(runtimeCanonicalCoverageDisplayReadinessSQL, -1)
	if len(functions) != 21 || len(fingerprints) != len(functions) {
		t.Fatalf("want 21 protected canonical functions including detail/MVT/focus in 030; migration=%d readiness=%d", len(functions), len(fingerprints))
	}
	for _, function := range functions {
		t.Run(function[1], func(t *testing.T) {
			body := function[4]
			if function[1] == "coverage_mvt" {
				raw, err := migrations.Files.ReadFile("031_tile_rendering.sql")
				if err != nil {
					t.Fatal(err)
				}
				for _, replacement := range canonicalDisplayFunctions(string(raw)) {
					if replacement[1] == "coverage_mvt" {
						body = replacement[4]
					}
				}
			}
			want := fmt.Sprintf("%x", md5.Sum([]byte(body)))
			var got string
			for _, fingerprint := range fingerprints {
				if fingerprint[1] == function[1] {
					got = fingerprint[2]
				}
			}
			if got != want {
				t.Errorf("%s fingerprint=%q want %q", function[1], got, want)
			}
			if function[1] == "mark_coverage_display_dirty" || function[1] == "map_selection_coverage_entities" || function[1] == "map_selection_coverage_entity_stats" {
				pattern := regexp.MustCompile(`(?s)\('app\.` + function[1] + `\([^']*\)'.*?,'([a-f0-9]{32})'\)`)
				old := pattern.FindStringSubmatch(runtimeCoverageDisplayReadinessSQL)
				if len(old) != 2 || old[1] != want {
					t.Errorf("source-facts contract must require the canonical 030 body for %s", function[1])
				}
			}
		})
	}
	// These changes preserve the old whitespace-normalized fingerprint but alter
	// either a policy literal or whether the deferred barrier is commented out.
	for _, fixture := range []struct{ name, old, replacement string }{
		{"coverage_display_identity", "'coverage-display-identity-v1'", "'coverage-display-identity- v1'"},
		{"flush_coverage_display_invalidations", "-- only writes; otherwise an evidence flush could invert these locks.\n        UPDATE", "-- only writes; otherwise an evidence flush could invert these locks. UPDATE"},
	} {
		var body string
		for _, function := range functions {
			if function[1] == fixture.name {
				body = function[4]
			}
		}
		variant := strings.Replace(body, fixture.old, fixture.replacement, 1)
		if variant == body || strings.Join(strings.Fields(variant), "") != strings.Join(strings.Fields(body), "") || md5.Sum([]byte(body)) == md5.Sum([]byte(variant)) {
			t.Errorf("invalid exact-body regression fixture for %s", fixture.name)
		}
	}
}

func TestCanonicalCoverageDisplayReadinessPhysicalColumns(t *testing.T) {
	tables := regexp.MustCompile(`(?s)CREATE TABLE app\.(coverage_display_(?:revisions|contexts|members|visits|entities|invalidations)) \((.*?)\n\);`).FindAllStringSubmatch(canonicalDisplayMigration(t), -1)
	if len(tables) != 6 {
		t.Fatalf("want six complete private table definitions, got %d", len(tables))
	}
	for _, table := range tables {
		columns := regexp.MustCompile(`(?m)^    ([a-z_]+) (double precision|timestamptz|uuid|bigint|date|text|boolean)\b([^\n]*)`).FindAllStringSubmatch(table[2], -1)
		var expected []string
		for _, column := range columns {
			typeName := strings.ReplaceAll(column[2], "timestamptz", "timestamp with time zone")
			notNull := strings.Contains(column[3], "NOT NULL") || strings.Contains(column[3], "PRIMARY KEY")
			expected = append(expected, fmt.Sprintf("%s:%s:%t", column[1], typeName, notNull))
		}
		contract := regexp.MustCompile(`(?s)\('` + table[1] + `',ARRAY\[(.*?)\],ARRAY\[`).FindStringSubmatch(runtimeCanonicalCoverageDisplayReadinessSQL)
		if len(contract) != 2 {
			t.Fatalf("missing physical column contract for %s", table[1])
		}
		var actual []string
		for _, column := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(contract[1], -1) {
			actual = append(actual, column[1])
		}
		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Errorf("%s physical columns=%v, migration=%v", table[1], actual, expected)
		}
		if table[1] == "coverage_display_members" && len(columns) != 18 {
			t.Errorf("members must retain 14 metadata/key columns followed by four independent range bounds, got %d", len(columns))
		}
	}
}

func TestCanonicalCoverageDisplayReadinessFunctionAttributes(t *testing.T) {
	tuples := regexp.MustCompile(`(?s)\('app\.(\w+)\(([^']*)\)','(sql|plpgsql)','([isv])',(true|false),(true|false),'([su])',(true|false),(\d+),\s*'([^']*)',\s*ARRAY\[([^]]*)\],(true|false),(true|false),'[a-f0-9]{32}'\)`).FindAllStringSubmatch(runtimeCanonicalCoverageDisplayReadinessSQL, -1)
	if len(tuples) != 21 {
		t.Fatalf("want 21 complete function attribute tuples, got %d", len(tuples))
	}
	functions := canonicalDisplayFunctions(canonicalDisplayMigration(t))
	tile, err := migrations.Files.ReadFile("031_tile_rendering.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range canonicalDisplayFunctions(string(tile)) {
		if current[1] == "coverage_mvt" {
			for i := range functions {
				if functions[i][1] == current[1] {
					functions[i] = current
				}
			}
		}
	}
	for _, function := range functions {
		header := strings.Join(strings.Fields(function[3]), " ")
		var tuple []string
		for _, candidate := range tuples {
			if candidate[1] == function[1] {
				tuple = candidate
			}
		}
		if tuple == nil {
			t.Errorf("missing function attributes for %s", function[1])
			continue
		}
		var types []string
		defaults := 0
		for _, arg := range strings.Split(function[2], ",") {
			if strings.TrimSpace(arg) == "" {
				continue
			}
			parts := strings.SplitN(strings.TrimSpace(arg), " DEFAULT ", 2)
			if len(parts) == 2 {
				defaults++
			}
			types = append(types, strings.Join(strings.Fields(parts[0])[1:], " "))
		}
		language := regexp.MustCompile(`LANGUAGE (\w+)`).FindStringSubmatch(header)
		volatility, parallel := "v", "u"
		if strings.Contains(header, "IMMUTABLE") {
			volatility = "i"
		} else if strings.Contains(header, "STABLE") {
			volatility = "s"
		}
		if strings.Contains(header, "PARALLEL SAFE") {
			parallel = "s"
		}
		result := strings.TrimSpace(strings.Split(header, "LANGUAGE")[0])
		returnsSet := strings.HasPrefix(result, "TABLE")
		if returnsSet {
			result = "TABLE(" + strings.TrimSpace(strings.TrimSuffix(strings.SplitN(result, "(", 2)[1], ")")) + ")"
		}
		result = strings.ReplaceAll(result, "timestamptz", "timestamp with time zone")
		path := regexp.MustCompile(`SET search_path = ([a-z_, ]+?)(?: SET |$)`).FindStringSubmatch(header)
		if len(path) != 2 || len(language) != 2 {
			t.Fatalf("cannot parse attributes of %s", function[1])
		}
		config := "'search_path=" + path[1] + "'"
		if strings.Contains(header, "SET jit = off") {
			config += ",'jit=off'"
		}
		if workMem := regexp.MustCompile(`SET work_mem = '([^']+)'`).FindStringSubmatch(header); len(workMem) == 2 {
			config += ",'work_mem=" + workMem[1] + "'"
		}
		api := function[1] == "prepare_coverage_display_context" || strings.HasPrefix(function[1], "map_selection_coverage_")
		want := []string{strings.Join(types, ","), language[1], volatility,
			fmt.Sprint(strings.Contains(header, "SECURITY DEFINER")), fmt.Sprint(strings.Contains(header, "STRICT")),
			parallel, fmt.Sprint(returnsSet), fmt.Sprint(defaults), result, config, fmt.Sprint(api), fmt.Sprint(function[1] == "coverage_mvt")}
		for i, expected := range want {
			if tuple[i+2] != expected {
				t.Errorf("%s attribute %d=%q, current contract=%q", function[1], i, tuple[i+2], expected)
			}
		}
	}
}

func TestCanonicalCoverageDisplayReadinessSemanticBoundaries(t *testing.T) {
	functions := canonicalDisplayFunctions(canonicalDisplayMigration(t))
	bodies := make(map[string]string)
	for _, function := range functions {
		bodies[function[1]] = function[4]
	}
	for _, name := range []string{"enqueue_coverage_display_invalidations", "advance_account_data_generation", "mark_coverage_display_dirty", "queue_coverage_display_source_invalidation", "queue_coverage_display_metadata_invalidation", "prepare_coverage_display_context"} {
		for _, forbidden := range []string{"UPDATE app.account_data_generations", "UPDATE app.coverage_display_revisions", "FOR UPDATE", "FOR SHARE", "pg_advisory"} {
			if strings.Contains(bodies[name], forbidden) {
				t.Errorf("%s takes an early generation/revision lock: %s", name, forbidden)
			}
		}
	}
	for _, name := range []string{"map_selection_coverage_entities", "map_selection_coverage_entity_stats", "map_selection_coverage_entity_members"} {
		for _, forbidden := range []string{"app.workouts", "source_visits(", "prepare_coverage_display_context(", "INSERT INTO", "UPDATE app.", "DELETE FROM", "ST_", "count("} {
			if strings.Contains(bodies[name], forbidden) {
				t.Errorf("%s must remain an authorized indexed context reader: %s", name, forbidden)
			}
		}
		for _, required := range []string{"app.coverage_display_authorized_context(", "entity.context_id = authorized_id", "entity.range_workout_count > 0"} {
			if !strings.Contains(bodies[name], required) {
				t.Errorf("%s missing %s", name, required)
			}
		}
	}
	for _, fixture := range []struct{ name, fragment string }{
		{"coverage_display_normalize", "normalize(casefold(normalize(value, NFC) COLLATE pg_catalog.pg_c_utf8), NFC)"},
		{"coverage_display_identity", "int4send(octet_length(encoded))"},
		{"coverage_display_identity", "substring(sha256(payload) FROM 1 FOR 16)"},
		{"coverage_display_identity", "(get_byte(digest, 6) & 15) | 128"},
		{"coverage_display_identity", "(get_byte(digest, 8) & 63) | 128"},
		{"coverage_display_selection_scope", "target_account_id = app.current_account_id() AND target_session_id = app.current_session_id()"},
		{"coverage_display_authorized_context", "context.ready"},
		{"coverage_display_authorized_context", "ERRCODE = '55000'"},
		{"prepare_coverage_display_context", "canonical coverage display UUID collision"},
		{"prepare_coverage_display_context", "GROUP BY member.canonical_id, visit.workout_id"},
		{"map_selection_coverage_entity_detail", "app.map_selection_coverage_entity_stats("},
		{"coverage_mvt", "sourceEntityKind"},
		{"map_selection_coverage_focus", "sourceEntityKind"},
	} {
		if !strings.Contains(bodies[fixture.name], fixture.fragment) {
			t.Errorf("%s missing semantic boundary %s", fixture.name, fixture.fragment)
		}
	}
}

func TestCanonicalCoverageDisplayReadinessCorrectedSemantics(t *testing.T) {
	bodies := make(map[string]string)
	var sourceResult string
	for _, function := range canonicalDisplayFunctions(canonicalDisplayMigration(t)) {
		bodies[function[1]] = function[4]
		if function[1] == "coverage_display_source_visits" {
			sourceResult = strings.Split(function[3], "LANGUAGE")[0]
		}
	}
	source, prepare := bodies["coverage_display_source_visits"], bodies["prepare_coverage_display_context"]
	compact := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	require := func(t *testing.T, body string, fragments ...string) {
		t.Helper()
		for _, fragment := range fragments {
			if !strings.Contains(compact(body), compact(fragment)) {
				t.Errorf("missing corrected semantic boundary %q", fragment)
			}
		}
	}
	t.Run("display fallback before identity", func(t *testing.T) {
		require(t, source,
			"COALESCE(source.locality_name, catalog.display_name,",
			"upper(left(part.word, 1) COLLATE pg_catalog.pg_c_utf8)",
			"lower(substr(part.word, 2) COLLATE pg_catalog.pg_c_utf8)",
			"FROM regexp_split_to_table(regexp_replace(source.region_id, '^.*:', ''), '[-_]+')",
			"WITH ORDINALITY part(word, position) WHERE part.word <> ''), source.region_id) locality_name",
			"FROM resolved_sources source LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = source.region_id")
		if regexp.MustCompile(`(?i)NULLIF\s*\(\s*(?:source\.)?locality_name`).MatchString(source) {
			t.Error("a non-NULL empty locality must not fall through to a region label")
		}
		require(t, prepare,
			"SELECT * FROM app.coverage_display_source_visits(target_account_id, scope.start_date, scope.end_date)",
			"app.coverage_display_normalize(COALESCE(locality_name, '')) normalized_locality",
			"app.coverage_display_identity(key_region, normalized_name, normalized_locality, key_discriminator)")
		if strings.Index(prepare, "app.coverage_display_source_visits(") >= strings.Index(prepare, "app.coverage_display_identity(") {
			t.Error("displayed locality must be resolved before normalization and UUID generation")
		}
	})
	t.Run("independent range bounds", func(t *testing.T) {
		if len(strings.Split(sourceResult, ",")) != 18 {
			t.Error("private source_visits must return its original 14 columns plus four range bounds")
		}
		parkBounds := regexp.MustCompile(`(?s)\), park_bounds AS MATERIALIZED \((.*?)\n    \), park_identity`).FindStringSubmatch(source)
		rangeBounds := regexp.MustCompile(`(?s)JOIN LATERAL \((.*?)\n        \) representative ON true`).FindStringSubmatch(prepare)
		if len(parkBounds) != 2 || len(rangeBounds) != 2 {
			t.Fatal("missing independent source/group range-bounds scans")
		}
		require(t, parkBounds[1], "FROM eligible JOIN app.workout_park_display_bounds bounds", "GROUP BY bounds.park_id")
		require(t, rangeBounds[1], "FROM app.coverage_display_members member",
			"member.account_id = target_account_id AND member.context_id = prepared_id",
			"AND member.canonical_id = summary.canonical_id")
		for _, forbidden := range []string{"park_visits", "workout_park_attributions"} {
			if strings.Contains(parkBounds[1], forbidden) {
				t.Errorf("independent park bounds must not depend on attribution dates: %s", forbidden)
			}
		}
		if regexp.MustCompile(`(?m)^\s*WHERE\s+eligible\.local_start_date`).MatchString(parkBounds[1]) {
			t.Error("independent park bounds must retain historical region evidence outside per-aggregate range filters")
		}
		for _, forbidden := range []string{"coverage_display_visits", "in_range", "local_start_date", "GROUP BY"} {
			if strings.Contains(rangeBounds[1], forbidden) {
				t.Errorf("group bounds must include every member's range evidence: %s", forbidden)
			}
		}
		for _, column := range []string{"minimum_longitude", "minimum_latitude", "maximum_longitude", "maximum_latitude"} {
			aggregate := "min"
			if strings.HasPrefix(column, "maximum") {
				aggregate = "max"
			}
			require(t, parkBounds[1], aggregate+"(bounds."+column+") FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_"+column)
			require(t, rangeBounds[1], aggregate+"(member.range_"+column+") OVER () canonical_"+column)
			require(t, source, "source.range_"+column)
		}
		require(t, source, "bounds.range_region_id, min(visit.region_id COLLATE \"C\"), bounds.historical_region_id) region_id")
		require(t, prepare, "FROM summaries summary JOIN LATERAL (",
			"WHERE summary.range_workout_count = 0 OR (representative.region_id IS NOT NULL",
			"AND representative.canonical_minimum_longitude IS NOT NULL AND representative.canonical_minimum_latitude IS NOT NULL",
			"AND representative.canonical_maximum_longitude IS NOT NULL AND representative.canonical_maximum_latitude IS NOT NULL)")
	})
	t.Run("materialized indexed assembly", func(t *testing.T) {
		assembly := regexp.MustCompile(`(?s)WITH visits AS MATERIALIZED \((.*?)\n        UPDATE app\.coverage_display_contexts SET ready`).FindStringSubmatch(prepare)
		if len(assembly) != 2 {
			t.Fatal("missing materialized canonical entity assembly")
		}
		require(t, assembly[1],
			"FROM app.coverage_display_visits visit WHERE visit.account_id = target_account_id AND visit.context_id = prepared_id",
			"), summaries AS MATERIALIZED (",
			"FROM visits visit GROUP BY visit.canonical_id",
			"FROM summaries summary JOIN LATERAL (",
			"FROM app.coverage_display_members member WHERE member.account_id = target_account_id AND member.context_id = prepared_id AND member.canonical_id = summary.canonical_id",
			"ORDER BY (member.source_kind = 'park') DESC, member.name COLLATE \"C\", member.locality_name COLLATE \"C\", member.broad_class COLLATE \"C\", member.source_id LIMIT 1 ) representative ON true")
		for _, fragment := range []string{"FROM app.coverage_display_visits", "FROM app.coverage_display_members", "JOIN LATERAL ("} {
			if strings.Count(assembly[1], fragment) != 1 {
				t.Errorf("assembly must scan visits once and probe members once per canonical group: %s", fragment)
			}
		}
		for _, forbidden := range []string{"NOT MATERIALIZED", "representatives AS", "range_bounds AS", "DISTINCT ON", "GROUP BY member.canonical_id", "JOIN summaries", "JOIN range_bounds"} {
			if strings.Contains(assembly[1], forbidden) {
				t.Errorf("assembly must not restore global aggregate joins or repeated summary scans: %s", forbidden)
			}
		}
		limit := strings.Index(assembly[1], "LIMIT 1")
		for _, column := range []string{"minimum_longitude", "minimum_latitude", "maximum_longitude", "maximum_latitude"} {
			window := strings.Index(assembly[1], "OVER () canonical_"+column)
			if window < 0 || window >= limit {
				t.Errorf("all-member %s window must precede representative LIMIT", column)
			}
			require(t, assembly[1], "representative.canonical_"+column)
		}
	})
	t.Run("warm reuse avoids shared writes", func(t *testing.T) {
		warm := regexp.MustCompile(`(?s)IF inserted_id IS NULL THEN(.*?)\n    ELSE`).FindStringSubmatch(prepare)
		gc := regexp.MustCompile(`(?s)IF inserted_id IS NOT NULL THEN(.*?)\n    END IF;`).FindStringSubmatch(prepare)
		if len(warm) != 2 || len(gc) != 2 {
			t.Fatal("reuse and new-build-only GC must be separate branches")
		}
		require(t, warm[1], "SELECT context.id INTO prepared_id", "AND context.ready")
		if regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE|PERFORM)\b`).MatchString(warm[1]) {
			t.Error("warm context lookup must not write or invoke a mutating helper")
		}
		if regexp.MustCompile(`(?i)\bSET\s+last_used_at\s*=`).MatchString(prepare) {
			t.Error("warm selection preparation must not touch a shared context timestamp")
		}
		require(t, gc[1], "DELETE FROM app.coverage_display_contexts context USING unpinned")
		if strings.Count(prepare, "DELETE FROM app.coverage_display_contexts") != 1 || strings.Count(prepare, "UPDATE app.coverage_display_contexts") != 1 {
			t.Error("only a new build may publish readiness and garbage-collect contexts")
		}
		require(t, prepare, "UPDATE app.coverage_display_contexts SET ready = true WHERE account_id = target_account_id AND id = prepared_id; END IF; UPDATE app.map_selections SET coverage_display_context_id = prepared_id")
	})
	t.Run("exact geometry invalidation", func(t *testing.T) {
		dirty := bodies["mark_coverage_display_dirty"]
		require(t, dirty, "OLD.covered_meters, public.ST_AsEWKB(OLD.geom)", "NEW.covered_meters, public.ST_AsEWKB(NEW.geom)", "IS NOT DISTINCT FROM")
		for _, forbidden := range []string{"OLD.covered_meters, OLD.geom", "NEW.covered_meters, NEW.geom", "ST_Equals("} {
			if strings.Contains(dirty, forbidden) {
				t.Errorf("geometry invalidation must compare exact qualified EWKB, not %s", forbidden)
			}
		}
	})
}
