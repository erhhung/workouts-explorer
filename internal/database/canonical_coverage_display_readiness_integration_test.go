package database

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCanonicalCoverageDisplayReadinessPostgreSQL(t *testing.T) {
	const prefix = "PROJECT_CANONICAL_COVERAGE_DISPLAY_READINESS_"
	dsn := os.Getenv(prefix + "DATABASE_URL")
	if dsn == "" || os.Getenv(prefix+"DISPOSABLE") != "1" {
		t.Skip("explicit canonical-readiness scratch admin URL and PROJECT_CANONICAL_COVERAGE_DISPLAY_READINESS_DISPOSABLE=1 required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid canonical-readiness admin URL")
	}
	validEndpoint := func(c *pgx.ConnConfig) bool {
		if !regexp.MustCompile(`^workouts_canonical_display_test_[a-z0-9_]+$`).MatchString(c.Database) ||
			(c.Host != "127.0.0.1" && c.Host != "::1") {
			return false
		}
		for _, fallback := range c.Fallbacks {
			if fallback.Host != c.Host || fallback.Port != c.Port {
				return false
			}
		}
		return true
	}
	if !validEndpoint(config.ConnConfig) {
		t.Fatal("catalog mutations require a named workouts_canonical_display_test_ loopback scratch database without remote fallbacks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var safe, locked bool
	if err := admin.QueryRow(ctx, `SELECT current_database()=$1 AND rolsuper,
		pg_try_advisory_lock(330033,hashtext(current_database())) FROM pg_roles WHERE rolname=current_user`, config.ConnConfig.Database).Scan(&safe, &locked); err != nil || !safe || !locked {
		t.Fatalf("scratch superuser and exclusive mutation harness required: safe=%t locked=%t err=%v", safe, locked, err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_version=31 AND minimum_runtime_version=31 AND migration_baseline=27
		FROM app.schema_metadata WHERE singleton`).Scan(&safe); err != nil || !safe {
		t.Fatalf("mutation harness requires fully migrated schema31/min31 metadata: %v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM app.accounts account
		LEFT JOIN app.coverage_display_revisions revision ON revision.account_id=account.id
		WHERE revision.account_id IS NULL OR revision.revision<=0)`).Scan(&safe); err != nil || !safe {
		t.Fatalf("migration30 must seed a positive display revision for every account: %v", err)
	}
	roles := []struct{ name, env string }{
		{"workouts_api", prefix + "API_DATABASE_URL"},
		{"workouts_worker", prefix + "WORKER_DATABASE_URL"},
		{"workouts_coverage_worker", prefix + "COVERAGE_WORKER_DATABASE_URL"},
	}
	pools := make([]*pgxpool.Pool, 0, len(roles))
	for _, role := range roles {
		runtimeConfig := config.Copy()
		if runtimeDSN := os.Getenv(role.env); runtimeDSN != "" {
			runtimeConfig, err = pgxpool.ParseConfig(runtimeDSN)
			if err != nil {
				t.Fatalf("invalid %s", role.env)
			}
		} else {
			runtimeConfig.ConnConfig.User, runtimeConfig.ConnConfig.Password = role.name, ""
		}
		c := runtimeConfig.ConnConfig
		if !validEndpoint(c) || c.Host != config.ConnConfig.Host || c.Port != config.ConnConfig.Port ||
			c.Database != config.ConnConfig.Database || c.User != role.name {
			t.Fatalf("%s must authenticate the expected role on the same scratch endpoint", role.env)
		}
		delete(c.RuntimeParams, "role")
		runtimeConfig.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, runtimeConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var restricted bool
		if err := pool.QueryRow(ctx, `SELECT session_user=$1 AND current_user=$1 AND NOT rolsuper AND NOT rolbypassrls
			AND NOT pg_has_role(current_user,'workouts_security_owner','USAGE') FROM pg_roles WHERE rolname=current_user`, role.name).Scan(&restricted); err != nil || !restricted {
			t.Fatalf("%s is not a genuine restricted login: %v", role.name, err)
		}
		for _, query := range []string{runtimeCanonicalCoverageDisplayReadinessSQL, runtimeCoverageDisplayReadinessSQL, runtimeFullReadinessSQL} {
			var ready bool
			if err := pool.QueryRow(ctx, query, SupportedSchemaVersion).Scan(&ready); err != nil || !ready {
				t.Fatalf("current runtime must pass each readiness component for %s: ready=%t err=%v", role.name, ready, err)
			}
		}
		if !Ready(ctx, pool) {
			t.Fatalf("runtime30 Ready must succeed for %s", role.name)
		}
		var oldReady, gated bool
		if err := pool.QueryRow(ctx, runtimeFullReadinessSQL, 29).Scan(&oldReady); err != nil || oldReady {
			t.Fatalf("runtime29 must be rejected for %s: ready=%t err=%v", role.name, oldReady, err)
		}
		if err := pool.QueryRow(ctx, runtimeCanonicalCoverageDisplayReadinessSQL, 29).Scan(&gated); err != nil || !gated {
			t.Fatalf("canonical-only catalog guard must be gated below30 for %s: %v", role.name, err)
		}
		pools = append(pools, pool)
	}

	type mutation struct{ name, sql, restore string }
	mutations := []mutation{
		{"minimum runtime lowered", `UPDATE app.schema_metadata SET minimum_runtime_version=29 WHERE singleton`, `UPDATE app.schema_metadata SET minimum_runtime_version=31 WHERE singleton`},
		{"partial schema metadata", `UPDATE app.schema_metadata SET schema_version=29,minimum_runtime_version=29 WHERE singleton`, `UPDATE app.schema_metadata SET schema_version=31,minimum_runtime_version=31 WHERE singleton`},
		{"selection context nullable", `UPDATE pg_attribute SET attnotnull=true WHERE attrelid='app.map_selections'::regclass AND attname='coverage_display_context_id'`, `UPDATE pg_attribute SET attnotnull=false WHERE attrelid='app.map_selections'::regclass AND attname='coverage_display_context_id'`},
		{"selection context default", `ALTER TABLE app.map_selections ALTER COLUMN coverage_display_context_id SET DEFAULT gen_random_uuid()`, `ALTER TABLE app.map_selections ALTER COLUMN coverage_display_context_id DROP DEFAULT`},
		{"selection extra context FK", `ALTER TABLE app.map_selections ADD CONSTRAINT unexpected_canonical_fk FOREIGN KEY (account_id,coverage_display_context_id) REFERENCES app.coverage_display_contexts(account_id,id) ON DELETE CASCADE`, `ALTER TABLE app.map_selections DROP CONSTRAINT unexpected_canonical_fk`},
		{"extra source bridge", `CREATE TRIGGER extra_canonical_bridge AFTER INSERT ON app.coverage_display_dirty FOR EACH ROW EXECUTE FUNCTION app.queue_coverage_display_source_invalidation()`, `DROP TRIGGER extra_canonical_bridge ON app.coverage_display_dirty`},
		{"extra immediate flush", `CREATE TRIGGER extra_canonical_flush AFTER INSERT ON app.coverage_display_invalidations FOR EACH ROW EXECUTE FUNCTION app.flush_coverage_display_invalidations()`, `DROP TRIGGER extra_canonical_flush ON app.coverage_display_invalidations`},
	}
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
		mutations = append(mutations, mutation{"selection binding update " + role,
			"GRANT UPDATE(coverage_display_context_id) ON app.map_selections TO " + role,
			"REVOKE UPDATE(coverage_display_context_id) ON app.map_selections FROM " + role})
	}
	for _, column := range []struct{ table, name, wrong, original string }{
		{"coverage_display_revisions", "revision", "2", "1"},
		{"coverage_display_contexts", "ready", "true", "false"},
		{"coverage_display_contexts", "policy_version", "'wrong-policy'", "'coverage-display-identity-v1'"},
		{"coverage_display_contexts", "label_locale", "'fr'", "'en'"},
	} {
		alter := "ALTER TABLE app." + column.table + " ALTER COLUMN " + column.name + " SET DEFAULT "
		mutations = append(mutations, mutation{column.table + " default " + column.name, alter + column.wrong, alter + column.original})
	}
	quote := func(name string) string { return pgx.Identifier{name}.Sanitize() }
	literal := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	for _, table := range []string{"coverage_display_revisions", "coverage_display_contexts", "coverage_display_members", "coverage_display_visits", "coverage_display_entities", "coverage_display_invalidations"} {
		relation := "app." + quote(table)
		alter := "ALTER TABLE " + relation + " "
		policy := quote(table + "_owner_policy")
		mutations = append(mutations,
			mutation{table + " missing", alter + "RENAME TO missing_canonical_table", `ALTER TABLE app.missing_canonical_table RENAME TO ` + quote(table)},
			mutation{table + " owner", alter + "OWNER TO workouts_migration", alter + "OWNER TO workouts_security_owner"},
			mutation{table + " RLS", alter + "DISABLE ROW LEVEL SECURITY", alter + "ENABLE ROW LEVEL SECURITY"},
			mutation{table + " forced RLS", alter + "NO FORCE ROW LEVEL SECURITY", alter + "FORCE ROW LEVEL SECURITY"},
			mutation{table + " extra policy", "CREATE POLICY extra_canonical_policy ON " + relation + " USING (true)", "DROP POLICY extra_canonical_policy ON " + relation},
			mutation{table + " policy role", "ALTER POLICY " + policy + " ON " + relation + " TO PUBLIC", "ALTER POLICY " + policy + " ON " + relation + " TO workouts_security_owner"},
			mutation{table + " policy predicate", "ALTER POLICY " + policy + " ON " + relation + " USING (false)", "ALTER POLICY " + policy + " ON " + relation + " USING (true)"},
			mutation{table + " policy check", "ALTER POLICY " + policy + " ON " + relation + " WITH CHECK (false)", "ALTER POLICY " + policy + " ON " + relation + " WITH CHECK (true)"},
			mutation{table + " extra column", alter + "ADD COLUMN unexpected text", alter + "DROP COLUMN unexpected"},
		)
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles", "workouts_migration"} {
			for _, privilege := range []string{"SELECT", "UPDATE(account_id)"} {
				mutations = append(mutations, mutation{table + " " + role + " " + privilege,
					"GRANT " + privilege + " ON " + relation + " TO " + role, "REVOKE " + privilege + " ON " + relation + " FROM " + role})
			}
		}
		rows, err := admin.Query(ctx, `SELECT attname,attnotnull FROM pg_attribute WHERE attrelid=to_regclass($1) AND attnum>0 AND NOT attisdropped ORDER BY attnum`, "app."+table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var column string
			var notNull bool
			if err := rows.Scan(&column, &notNull); err != nil {
				t.Fatal(err)
			}
			where := " WHERE attrelid=" + literal("app."+table) + "::regclass AND attname=" + literal(column)
			mutations = append(mutations, mutation{table + " nullability " + column,
				fmt.Sprintf("UPDATE pg_attribute SET attnotnull=%t%s", !notNull, where), fmt.Sprintf("UPDATE pg_attribute SET attnotnull=%t%s", notNull, where)})
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	rows, err := admin.Query(ctx, `SELECT conrelid::regclass::text,conname,contype::text,pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid IN (SELECT oid FROM pg_class WHERE relnamespace='app'::regnamespace
		AND relname IN ('coverage_display_revisions','coverage_display_contexts','coverage_display_members','coverage_display_visits','coverage_display_entities','coverage_display_invalidations'))
		AND contype IN ('p','u','f','c') OR conrelid='app.map_selections'::regclass AND conname='map_selections_coverage_display_context_fk' ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var relation, name, kind, definition string
		if err := rows.Scan(&relation, &name, &kind, &definition); err != nil {
			t.Fatal(err)
		}
		drop := "ALTER TABLE " + relation + " DROP CONSTRAINT " + quote(name)
		add := "ALTER TABLE " + relation + " ADD CONSTRAINT " + quote(name) + " "
		// Context PK/scope keys have dependents; a catalog rename is not a
		// constraint removal. Mutate their validation/index flags instead.
		if kind == "f" || kind == "c" || relation != "app.coverage_display_contexts" {
			mutations = append(mutations, mutation{name + " missing", drop, add + definition})
		}
		if kind == "f" || kind == "c" {
			mutations = append(mutations, mutation{name + " unvalidated", drop + "; " + add + definition + " NOT VALID", drop + "; " + add + definition})
		}
		if kind == "f" {
			variant := strings.ReplaceAll(definition, "ON DELETE CASCADE", "ON DELETE RESTRICT")
			if name == "map_selections_coverage_display_context_fk" {
				variant = strings.Replace(definition, "SET NULL (coverage_display_context_id)", "SET NULL", 1)
			}
			if variant == definition {
				t.Fatalf("missing FK mutation anchor in %s", name)
			}
			mutations = append(mutations, mutation{name + " deletion action", drop + "; " + add + variant, drop + "; " + add + definition})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	rows, err = admin.Query(ctx, `SELECT i.indexrelid::regclass::text,pg_get_indexdef(i.indexrelid),i.indisunique
		FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid WHERE c.relnamespace='app'::regnamespace
		AND (c.relname IN ('coverage_display_revisions','coverage_display_contexts','coverage_display_members','coverage_display_visits','coverage_display_entities','coverage_display_invalidations')
		OR i.indexrelid=to_regclass('app.map_selections_coverage_display_context_idx')) ORDER BY i.indexrelid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, definition string
		var unique bool
		if err := rows.Scan(&name, &definition, &unique); err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"indisvalid", "indisready", "indislive", "indimmediate"} {
			where := " WHERE indexrelid=" + literal(name) + "::regclass"
			mutations = append(mutations, mutation{name + " " + flag, "UPDATE pg_index SET " + flag + "=false" + where, "UPDATE pg_index SET " + flag + "=true" + where})
		}
		if !unique {
			drop := "DROP INDEX " + name
			mutations = append(mutations, mutation{name + " missing", drop, definition})
			if strings.Contains(name, "entities_visible") {
				mutations = append(mutations, mutation{name + " wrong predicate", drop + "; " + strings.Replace(definition, "range_workout_count > 0", "range_workout_count >= 0", 1), drop + "; " + definition})
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	functions := canonicalDisplayFunctions(canonicalDisplayMigration(t))
	for _, function := range functions {
		var signature, definition string
		if err := admin.QueryRow(ctx, `SELECT oid::regprocedure::text,pg_get_functiondef(oid) FROM pg_proc
			WHERE pronamespace='app'::regnamespace AND proname=$1`, function[1]).Scan(&signature, &definition); err != nil {
			t.Fatal(err)
		}
		alter := "ALTER FUNCTION " + signature + " "
		security := "SECURITY INVOKER"
		if !strings.Contains(function[3], "SECURITY DEFINER") {
			security = "SECURITY DEFINER"
		}
		mutations = append(mutations,
			mutation{signature + " security", alter + security, definition},
			mutation{signature + " owner", alter + "OWNER TO workouts_migration", alter + "OWNER TO workouts_security_owner"},
			mutation{signature + " path", alter + "SET search_path=app,pg_catalog", definition},
			mutation{signature + " config", alter + "SET enable_seqscan=off", alter + "RESET enable_seqscan; " + definition},
			mutation{signature + " missing", alter + "RENAME TO missing_canonical_function", "ALTER FUNCTION app.missing_canonical_function(" + strings.SplitN(signature, "(", 2)[1] + " RENAME TO " + quote(function[1])},
		)
		volatility := "IMMUTABLE"
		if strings.Contains(function[3], "IMMUTABLE") {
			volatility = "VOLATILE"
		}
		mutations = append(mutations, mutation{signature + " volatility", alter + volatility, definition})
		if strings.Contains(function[3], "jit = off") {
			mutations = append(mutations, mutation{signature + " jit", alter + "SET jit=on", definition})
		}
		where := " WHERE oid=" + literal(signature) + "::regprocedure"
		parallel, originalParallel := "s", "u"
		if strings.Contains(function[3], "PARALLEL SAFE") {
			parallel, originalParallel = "u", "s"
		}
		language := regexp.MustCompile(`LANGUAGE (\w+)`).FindStringSubmatch(function[3])
		if len(language) != 2 {
			t.Fatalf("missing language for %s", signature)
		}
		otherLanguage := "sql"
		if language[1] == "sql" {
			otherLanguage = "plpgsql"
		}
		mutations = append(mutations,
			mutation{signature + " parallel", "UPDATE pg_proc SET proparallel=" + literal(parallel) + where, "UPDATE pg_proc SET proparallel=" + literal(originalParallel) + where},
			mutation{signature + " language", "UPDATE pg_proc SET prolang=(SELECT oid FROM pg_language WHERE lanname=" + literal(otherLanguage) + ")" + where,
				"UPDATE pg_proc SET prolang=(SELECT oid FROM pg_language WHERE lanname=" + literal(language[1]) + ")" + where},
		)
		for _, change := range [][2]string{{"DEFAULT NULL::text", "DEFAULT 'unexpected'::text"}, {"DEFAULT '{}'::json", "DEFAULT '{\"unexpected\":true}'::json"}} {
			if strings.Contains(definition, change[0]) {
				mutations = append(mutations, mutation{signature + " default expression", strings.Replace(definition, change[0], change[1], 1), definition})
			}
		}
		for _, field := range []string{"proisstrict", "proleakproof"} {
			mutations = append(mutations, mutation{signature + " " + field, "UPDATE pg_proc SET " + field + "=NOT " + field + where, "UPDATE pg_proc SET " + field + "=NOT " + field + where})
		}
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles", "workouts_migration"} {
			var granted bool
			if role != "PUBLIC" && role != "workouts_migration" {
				if err := admin.QueryRow(ctx, `SELECT has_function_privilege($1,to_regprocedure($2),'EXECUTE')`, role, signature).Scan(&granted); err != nil {
					t.Fatal(err)
				}
			}
			grant := "GRANT EXECUTE ON FUNCTION " + signature + " TO " + role
			revoke := "REVOKE EXECUTE ON FUNCTION " + signature + " FROM " + role
			if granted {
				mutations = append(mutations, mutation{signature + " missing " + role + " grant", revoke, grant},
					mutation{signature + " " + role + " grant option", grant + " WITH GRANT OPTION", "REVOKE GRANT OPTION FOR EXECUTE ON FUNCTION " + signature + " FROM " + role})
			} else {
				mutations = append(mutations, mutation{signature + " ACL " + role, grant, revoke})
			}
		}
		// prosrc changes preserve every signature/config/ACL attribute. They
		// deliberately bypass compilation so even removed security clauses can
		// be tested without executing an unsafe replacement function.
		var body string
		if err := admin.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid=$1::regprocedure`, signature).Scan(&body); err != nil {
			t.Fatal(err)
		}
		variants := map[string]string{"exact whitespace": body + "\n"}
		for _, anchor := range []string{
			"AND context.ready", "AND context.revision = scope.revision", "AND target_account_id = app.current_account_id()",
			"AND entity.range_workout_count > 0", "AND entity.canonical_id = target_entity_id",
			"public.ST_AsEWKB(OLD.geom)", "public.ST_AsEWKB(NEW.geom)", "ARRAY[old_account, new_account]",
			"SET revision = revision + 1", "substring(sha256(payload) FROM 1 FOR 16)", "COLLATE pg_catalog.pg_c_utf8",
			"COALESCE(source.locality_name, catalog.display_name,", "bounds.range_region_id,",
			"WITH visits AS MATERIALIZED (", "), summaries AS MATERIALIZED (", "JOIN LATERAL (",
			"AND member.canonical_id = summary.canonical_id",
			"min(member.range_minimum_longitude) OVER () canonical_minimum_longitude",
			"min(member.range_minimum_latitude) OVER () canonical_minimum_latitude",
			"max(member.range_maximum_longitude) OVER () canonical_maximum_longitude",
			"max(member.range_maximum_latitude) OVER () canonical_maximum_latitude",
			"ORDER BY (member.source_kind = 'park') DESC", "LIMIT 1\n        ) representative ON true",
			"WHERE summary.range_workout_count = 0 OR", "IF inserted_id IS NOT NULL THEN",
		} {
			if strings.Contains(body, anchor) {
				variants[anchor] = strings.Replace(body, anchor, "/* readiness mutation */", 1)
			}
		}
		if strings.Contains(body, "'coverage-display-identity-v1'") {
			variants["literal whitespace collision"] = strings.Replace(body, "'coverage-display-identity-v1'", "'coverage-display-identity- v1'", 1)
		}
		if function[1] == "flush_coverage_display_invalidations" {
			variants["comment newline collision"] = strings.Replace(body, "locks.\n        UPDATE", "locks. UPDATE", 1)
		}
		for name, variant := range variants {
			if variant == body {
				t.Fatalf("unchanged body mutation %s %s", function[1], name)
			}
			mutations = append(mutations, mutation{signature + " body " + name, "UPDATE pg_proc SET prosrc=" + literal(variant) + where, "UPDATE pg_proc SET prosrc=" + literal(body) + where})
		}
	}
	rows, err = admin.Query(ctx, `SELECT tgrelid::regclass::text,tgname,pg_get_triggerdef(oid),tgdeferrable
		FROM pg_trigger WHERE NOT tgisinternal AND tgfoid IN (SELECT oid FROM pg_proc WHERE pronamespace='app'::regnamespace
		AND proname IN ('queue_coverage_display_source_invalidation','queue_coverage_display_metadata_invalidation','flush_coverage_display_invalidations','seed_coverage_display_revision','validate_map_selection','validate_map_selection_focus')) ORDER BY tgname`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var relation, name, definition string
		var deferred bool
		if err := rows.Scan(&relation, &name, &definition, &deferred); err != nil {
			t.Fatal(err)
		}
		drop := "DROP TRIGGER " + quote(name) + " ON " + relation
		mutations = append(mutations, mutation{name + " missing", drop, definition},
			mutation{name + " disabled", "ALTER TABLE " + relation + " DISABLE TRIGGER " + quote(name), "ALTER TABLE " + relation + " ENABLE TRIGGER " + quote(name)})
		where := " WHERE tgrelid=" + literal(relation) + "::regclass AND tgname=" + literal(name)
		mutations = append(mutations, mutation{name + " projected events", "UPDATE pg_trigger SET tgtype=tgtype # 16" + where, "UPDATE pg_trigger SET tgtype=tgtype # 16" + where},
			mutation{name + " narrowed columns", "UPDATE pg_trigger SET tgattr='1'::int2vector" + where, "UPDATE pg_trigger SET tgattr=''::int2vector" + where})
		if deferred {
			mutations = append(mutations, mutation{name + " initially immediate", drop + "; " + strings.Replace(definition, "INITIALLY DEFERRED", "INITIALLY IMMEDIATE", 1), drop + "; " + definition})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, mutation := range mutations {
		if !t.Run(mutation.name, func(t *testing.T) {
			// Commit mutations so genuine restricted connections see each defect.
			if _, err := admin.Exec(ctx, mutation.sql); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				restoreCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if _, err := admin.Exec(restoreCtx, mutation.restore); err != nil {
					t.Fatalf("restore scratch catalog: %v", err)
				}
			})
			for i, pool := range pools {
				var ready bool
				if err := pool.QueryRow(ctx, runtimeCanonicalCoverageDisplayReadinessSQL, 30).Scan(&ready); err != nil || ready {
					t.Errorf("%s canonical readiness=%t want false, err=%v", roles[i].name, ready, err)
				}
				if Ready(ctx, pool) {
					t.Errorf("%s Ready accepts broken canonical contract", roles[i].name)
				}
			}
		}) {
			t.Fatal("stopping catalog mutation harness after failure")
		}
	}
	for i, pool := range pools {
		if !Ready(ctx, pool) {
			t.Errorf("restored runtime30 schema must be ready for %s", roles[i].name)
		}
	}
	t.Logf("verified %d committed mutations through three restricted runtime logins; runtime29 rejected", len(mutations))
}
