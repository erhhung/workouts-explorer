package database

import (
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestDeferredGenerationRuntimeReadinessComposition(t *testing.T) {
	if SupportedSchemaVersion != 28 {
		t.Fatalf("runtime version = %d, want 28", SupportedSchemaVersion)
	}
	if runtimeFullReadinessSQL != "SELECT ("+runtimeReadinessSQL+") AND ("+runtimeDeferredReadinessSQL+") AND ("+runtimeCohortReadinessSQL+")" {
		t.Fatal("full readiness must require base, deferred generation and eligible-cohort contracts")
	}
	if strings.Contains(runtimeDeferredReadinessSQL, "$1") || strings.Contains(runtimeDeferredReadinessSQL, "schema_version") {
		t.Fatal("deferred object checks must not add a hardcoded schema/version gate")
	}
}

func TestDeferredGenerationReadinessExactTriggerContract(t *testing.T) {
	for _, required := range []string{
		"('app.workout_routes','workout_routes_data_generation_after_write',29,ARRAY[]::text[],NULL::text)",
		"('app.workouts','workouts_data_generation_after_write',29,ARRAY[]::text[],NULL::text)",
		"('app.workout_types','workout_types_data_generation_after_update',17,ARRAY['type_key','provider_label'],NULL::text)",
		"('app.workout_coverage_states','workout_coverage_states_data_generation_after_current',17,ARRAY['processing_finished_at']",
		"((new.processing_state = ''current''::text) AND (new.processing_finished_at IS DISTINCT FROM old.processing_finished_at))",
		"t.tgisinternal OR t.tgenabled<>'O'", "t.tgtype<>expected.events",
		"NOT t.tgdeferrable OR NOT t.tginitdeferred", "c.oid=t.tgconstraint",
		"c.contype<>'t'", "c.conname<>expected.name OR c.conrelid<>t.tgrelid",
		"NOT c.condeferrable OR NOT c.condeferred", "t.tgattr::text IS DISTINCT FROM",
		"substring(pg_get_triggerdef(t.oid) FROM ' WHEN \\((.*)\\) EXECUTE FUNCTION ') IS DISTINCT FROM expected.qual",
		"(t.tgqual IS NULL) IS DISTINCT FROM (expected.qual IS NULL)",
		"t.tgfoid<>'app.advance_account_data_generation()'::regprocedure",
		"WHERE tgfoid='app.advance_account_data_generation()'::regprocedure AND NOT tgisinternal)=4",
		"tgname='accounts_data_generation_after_insert'", "tgtype=5 AND tgconstraint=0",
		"NOT tgdeferrable AND NOT tginitdeferred", "tgfoid='app.seed_account_data_generation()'::regprocedure",
	} {
		if !strings.Contains(runtimeDeferredReadinessSQL, required) {
			t.Errorf("missing exact trigger readiness guard %q", required)
		}
	}
}

func TestDeferredGenerationReadinessProtectedBodies(t *testing.T) {
	bodies := regexp.MustCompile(`(?s)\$body\$(.*?)\$body\$`).FindAllStringSubmatch(runtimeDeferredReadinessSQL, -1)
	if len(bodies) != 2 {
		t.Fatalf("expected pure counter and nonlocking helper fingerprints, got %d", len(bodies))
	}
	for i, fixture := range []struct{ file, function string }{
		{"009_raw_route_map.sql", "advance_account_data_generation"},
		{"028_ingest_coverage_pipeline.sql", "lock_account_data_generation"},
	} {
		raw, err := migrations.Files.ReadFile(fixture.file)
		if err != nil {
			t.Fatal(err)
		}
		pattern := regexp.MustCompile(`(?s)FUNCTION app\.` + fixture.function + `\s*\(\).*?AS \$function\$(.*?)\$function\$;`)
		body := pattern.FindStringSubmatch(string(raw))
		if len(body) != 2 {
			t.Fatalf("missing %s body", fixture.function)
		}
		compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
		if compact(body[1]) != compact(bodies[i][1]) {
			t.Errorf("readiness fingerprint differs from %s", fixture.function)
		}
	}
	for _, required := range []string{
		"md5(regexp_replace(p.prosrc,'[[:space:]]','','g'))",
		"md5(regexp_replace(expected.body,'[[:space:]]','','g'))",
		"NOT p.prosecdef OR p.provolatile<>'v'", "pg_get_userbyid(p.proowner)<>'workouts_security_owner'",
		"p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog, app']",
		"has_function_privilege('workouts_api',p.oid,'EXECUTE') IS DISTINCT FROM expected.api",
		"r.rolname IN ('workouts_worker','workouts_coverage_worker','workouts_tiles')",
		"NOT expected.api OR acl.grantee<>'workouts_api'::regrole::oid OR acl.is_grantable",
		"to_regprocedure('app.fence_ingest_job(uuid,text,uuid,integer)')",
		"p.prosrc ~* '\\mPERFORM\\s+1\\s+FROM\\s+app\\s*\\.\\s*account_data_generations\\s+WHERE\\s+account_id\\s*=\\s*target_account\\s*;\\s*IF\\s+NOT\\s+FOUND\\s+THEN\\s+RAISE\\s+EXCEPTION\\s+''missing account data generation''\\s+USING\\s+ERRCODE\\s*=\\s*''55000''\\s*;\\s*END\\s+IF\\s*;'",
		"position('''missing account data generation''' IN p.prosrc)>0",
		"regexp_matches(p.prosrc,'\\maccount_data_generations\\M','gi'))=1",
		"p.prosrc !~* '\\m(?:FROM|JOIN)\\s+app\\s*\\.\\s*account_data_generations\\M[^;]*\\mFOR\\M'",
		"has_function_privilege('workouts_worker',p.oid,'EXECUTE')",
		"acl.grantee<>'workouts_worker'::regrole::oid OR acl.is_grantable",
		"has_table_privilege('workouts_security_owner','app.account_data_generations','SELECT')",
		"has_table_privilege('workouts_security_owner','app.account_data_generations','UPDATE')",
		"has_any_column_privilege(r.oid,'app.account_data_generations','INSERT,UPDATE,REFERENCES')",
	} {
		if !strings.Contains(runtimeDeferredReadinessSQL, required) {
			t.Errorf("missing protected generation readiness guard %q", required)
		}
	}
}

func TestDeferredGenerationReadinessFenceFormatting(t *testing.T) {
	quoted := regexp.MustCompile(`p\.prosrc ~\* '((?:[^']|'')*)'`).FindStringSubmatch(runtimeDeferredReadinessSQL)
	if len(quoted) != 2 {
		t.Fatal("missing structural nonlocking fence check")
	}
	pattern, err := regexp.Compile("(?i)" + strings.ReplaceAll(strings.ReplaceAll(quoted[1], "''", "'"), `\m`, `\b`))
	if err != nil {
		t.Fatal(err)
	}
	canonical := "PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account;\n" +
		"    IF NOT FOUND THEN RAISE EXCEPTION 'missing account data generation' USING ERRCODE = '55000'; END IF;"
	for name, body := range map[string]string{
		"canonical":    canonical,
		"comment free": "BEGIN\n" + canonical + "\nEND;",
		"comments":     "-- arbitrary documentation\n" + canonical + "\n-- more documentation",
		"mixed case":   strings.Replace(canonical, "PERFORM 1 FROM app.", "pErFoRm 1 fRoM APP . ", 1),
		"compact":      strings.ReplaceAll(strings.ReplaceAll(canonical, " = ", "="), "; END IF;", ";end if;"),
	} {
		t.Run(name, func(t *testing.T) {
			if !pattern.MatchString(body) {
				t.Fatal("valid nonlocking fence formatting rejected")
			}
		})
	}
	for name, body := range map[string]string{
		"locking":         strings.Replace(canonical, "target_account;", "target_account FOR UPDATE NOWAIT;", 1),
		"wrong SQLSTATE":  strings.Replace(canonical, "55000", "42501", 1),
		"wrong error":     strings.Replace(canonical, "missing account data generation", "other error", 1),
		"no error":        strings.Split(canonical, "\n")[0],
		"wrong account":   strings.Replace(canonical, "target_account;", "app.current_account_id();", 1),
		"clobbered FOUND": strings.Replace(canonical, ";\n", "; PERFORM 1;\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if pattern.MatchString(body) {
				t.Fatal("unsafe nonlocking fence structure admitted")
			}
		})
	}
}
