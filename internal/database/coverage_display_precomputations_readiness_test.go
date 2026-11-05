package database

import (
	"crypto/md5"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestCoverageDisplayReadinessComposition(t *testing.T) {
	if SupportedSchemaVersion != 31 {
		t.Fatalf("supported schema = %d, want 31", SupportedSchemaVersion)
	}
	want := "SELECT (" + runtimeReadinessSQL + ") AND (" + runtimeDeferredReadinessSQL + ") AND (" + runtimeCohortReadinessSQL + ") AND (" + runtimeCoverageDisplayReadinessSQL + ") AND (" + runtimeCanonicalCoverageDisplayReadinessSQL + ") AND (" + runtimeIngestPhaseReadinessSQL + ") AND (" + runtimeTileReadinessSQL + ") AND (" + runtimeTileLODReadinessSQL + ") AND (" + runtimeCoverageMetadataReadinessSQL + ") AND (" + runtimeRouteGapReadinessSQL + ")"
	if runtimeFullReadinessSQL != want || !strings.Contains(runtimeCoverageDisplayReadinessSQL, "SELECT $1 < 30 OR (") {
		t.Fatal("full readiness must compose the version-gated display contract with all existing contracts")
	}
}

func TestCoverageDisplayReadinessCatalogContract(t *testing.T) {
	for _, required := range []string{
		"SELECT minimum_runtime_version >= 30 FROM app.schema_metadata WHERE singleton",
		"('workout_path_display_partials', ARRAY[", "('workout_path_display_context', ARRAY[",
		"('workout_park_display_bounds', ARRAY[", "('coverage_display_dirty', ARRAY[",
		"c.relkind<>'r' OR c.relpersistence<>'p'", "pg_get_userbyid(c.relowner)<>'workouts_security_owner'",
		"NOT c.relrowsecurity OR NOT c.relforcerowsecurity", "FROM pg_policy p WHERE p.polrelid=c.oid)<>1",
		"p.polname=expected.name||'_owner_policy' AND p.polcmd='*' AND p.polpermissive",
		"p.polroles=ARRAY['workouts_security_owner'::regrole::oid]",
		"pg_get_expr(p.polqual,p.polrelid)='true' AND pg_get_expr(p.polwithcheck,p.polrelid)='true'",
		"IS DISTINCT FROM expected.columns", "a.atthasdef OR a.attgenerated<>'' OR a.attidentity<>''",
		"k.convalidated AND NOT k.condeferrable AND NOT k.condeferred", "pg_get_constraintdef(k.oid)=definition",
		"cardinality(expected.definitions)", "PRIMARY KEY (account_id, workout_id, entity_id, logical_path_id, qualifies_without_name)",
		"FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE CASCADE",
		"UNIQUE NULLS NOT DISTINCT (transaction_id, account_id, scope, workout_id, region_id, generation_id, physical_segment_id)",
		"CHECK ((scope = ANY", "(physical_segment_id IS NULL)", "(physical_segment_id IS NOT NULL)",
		"NOT i.indisvalid", "NOT i.indisready", "NOT i.indislive", "NOT i.indimmediate", "am.amname<>'btree'",
		"i.indisunique OR i.indisprimary OR i.indisexclusion OR i.indnullsnotdistinct",
		"i.indpred IS NOT NULL OR i.indexprs IS NOT NULL OR i.indnatts<>3 OR i.indnkeyatts<>3",
		"i.indoption::text<>'0 0 0'",
		"i.indrelid IS DISTINCT FROM to_regclass(expected.relation)",
		"ARRAY(SELECT pg_get_indexdef(i.indexrelid,n,true) FROM generate_series(1,i.indnkeyatts) n ORDER BY n)",
		"i.indnullsnotdistinct<>(k.contype='u')", "i.indkey::text<>array_to_string(k.conkey,' ')",
		"aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner)))", "aclexplode(a.attacl)",
		"has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')",
		"has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')",
		"p.proconfig IS DISTINCT FROM expected.config", "pg_get_function_result(p.oid)<>expected.result",
		"NOT p.prosecdef OR p.prokind<>'f' OR p.provolatile::text<>expected.volatility",
		"('app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text)','plpgsql','s',true,1,", "expected.defaults>0 AND pg_get_expr(p.proargdefaults,0) IS DISTINCT FROM 'NULL::text'",
		"('app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)','plpgsql','s',true,0,",
		"ARRAY['search_path=pg_catalog, app','jit=off']",
		"has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api",
		"r.rolname IN ('workouts_worker','workouts_coverage_worker','workouts_tiles')",
		"NOT expected.api OR acl.grantee<>'workouts_api'::regrole::oid OR acl.is_grantable",
		"('app.workout_segment_matches','workout_segment_matches_display_dirty_after_write',29,false",
		"('app.path_segments','path_segments_display_dirty_after_change',25,false",
		"('app.coverage_display_dirty','coverage_display_dirty_account_flush',5,true",
		"'(new.scope = ''account''::text)'", "t.tgisinternal OR t.tgenabled<>'O' OR t.tgtype<>expected.events",
		"t.tgfoid IS DISTINCT FROM to_regprocedure(expected.function)", "t.tgattr::text<>'' OR t.tgnargs<>0 OR t.tgargs<>''::bytea",
		"t.tgdeferrable<>expected.deferred OR t.tginitdeferred<>expected.deferred",
		"c.conrelid<>t.tgrelid OR NOT c.condeferrable OR NOT c.condeferred",
		"substring(pg_get_triggerdef(t.oid) FROM ' WHEN \\((.*)\\) EXECUTE FUNCTION ') IS DISTINCT FROM expected.qual",
		"tgfoid=to_regprocedure('app.mark_coverage_display_dirty()'))=2",
		"tgfoid=to_regprocedure('app.flush_coverage_display_dirty_account()'))=1",
	} {
		if !strings.Contains(runtimeCoverageDisplayReadinessSQL, required) {
			t.Errorf("missing display readiness guard %q", required)
		}
	}
}

func TestCoverageDisplayReadinessEntityIndexes(t *testing.T) {
	raw, err := migrations.Files.ReadFile("030_coverage_display.sql")
	if err != nil {
		t.Fatal(err)
	}
	indexes := regexp.MustCompile(`CREATE INDEX (\w+)\s+ON (app\.workout_\w+) \(([^)]+)\);`).FindAllStringSubmatch(string(raw), -1)
	if len(indexes) != 3 {
		t.Fatalf("want three entity-first evidence indexes in migration 030, got %d", len(indexes))
	}
	for _, index := range indexes {
		columns := strings.Split(index[3], ", ")
		want := fmt.Sprintf("('app.%s','%s',ARRAY['%s'])", index[1], index[2], strings.Join(columns, "','"))
		if !strings.Contains(runtimeCoverageDisplayReadinessSQL, want) {
			t.Errorf("missing embedded migration index contract %s", want)
		}
	}
}

func TestCoverageDisplayReadinessBodyFingerprints(t *testing.T) {
	raw, err := migrations.Files.ReadFile("030_coverage_display.sql")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?s)CREATE (?:OR REPLACE )?FUNCTION app\.([a-z_]+)\s*\(.*?AS \$function\$(.*?)\$function\$;`)
	functions := map[string]string{}
	for _, function := range pattern.FindAllStringSubmatch(string(raw), -1) {
		switch function[1] {
		case "rebuild_coverage_display_precomputations", "flush_coverage_display_precomputations", "mark_coverage_display_dirty", "flush_coverage_display_dirty_account", "map_selection_coverage_entities", "map_selection_coverage_entity_stats":
			if _, duplicate := functions[function[1]]; duplicate {
				t.Fatalf("duplicate evidence function definition for %s", function[1])
			}
			functions[function[1]] = function[2]
		}
	}
	fingerprints := regexp.MustCompile(`(?s)\('app\.([a-z_]+)\([^']*\)'.*?,'([a-f0-9]{32})'\)`).FindAllStringSubmatch(runtimeCoverageDisplayReadinessSQL, -1)
	if len(functions) != 6 || len(fingerprints) != 6 {
		t.Fatalf("want six protected bodies, got migration=%d readiness=%d", len(functions), len(fingerprints))
	}
	for name, body := range functions {
		t.Run(name, func(t *testing.T) {
			var hash string
			for _, fingerprint := range fingerprints {
				if fingerprint[1] == name {
					hash = fingerprint[2]
				}
			}
			want := fmt.Sprintf("%x", md5.Sum([]byte(body)))
			if hash != want {
				t.Fatalf("readiness fingerprint = %q, current embedded migration body = %q", hash, want)
			}
			var collision string
			switch name {
			case "rebuild_coverage_display_precomputations":
				collision = strings.ReplaceAll(body, "'state_park'", "'state_ park'")
			case "flush_coverage_display_precomputations":
				collision = strings.Replace(body, "-- public generation unchanged and take this barrier only at flush time.\n    UPDATE", "-- public generation unchanged and take this barrier only at flush time. UPDATE", 1)
			default:
				return
			}
			if collision == body || md5.Sum([]byte(collision)) == md5.Sum([]byte(body)) {
				t.Fatal("semantic whitespace mutation must change the exact body fingerprint")
			}
			compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
			if compact(collision) != compact(body) {
				t.Fatal("regression fixture must collide under the old whitespace normalization")
			}
		})
	}
	if !strings.Contains(runtimeCoverageDisplayReadinessSQL, "md5(p.prosrc)<>expected.body_hash") || strings.Contains(runtimeCoverageDisplayReadinessSQL, "regexp_replace(p.prosrc") {
		t.Fatal("display readiness must fingerprint exact body bytes, including whitespace in literals and comments")
	}
	readers := regexp.MustCompile(`(?s)FUNCTION app\.map_selection_coverage_(?:entities|entity_stats)\s*\(.*?RETURNS TABLE \((.*?)\) LANGUAGE`).FindAllStringSubmatch(string(raw), -1)
	if len(readers) != 2 {
		t.Fatalf("want list and targeted stats reader results, got %d", len(readers))
	}
	for _, reader := range readers {
		result := "TABLE(" + strings.Join(strings.Fields(reader[1]), " ") + ")"
		if len(strings.Split(reader[1], ",")) != 21 || strings.Count(runtimeCoverageDisplayReadinessSQL, "'"+result+"'") != 2 {
			t.Fatal("both canonical readers must preserve the exact 21-column result in migration 030")
		}
	}
}
