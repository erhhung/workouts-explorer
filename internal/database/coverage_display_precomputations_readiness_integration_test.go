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

func TestCoverageDisplayReadinessPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_DISPLAY_READINESS_DATABASE_URL")
	if dsn == "" || os.Getenv("COVERAGE_DISPLAY_PRECOMPUTATIONS_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit migrated disposable display-readiness admin URL and opt-in required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid display-readiness admin URL")
	}
	validEndpoint := func(c *pgx.ConnConfig) bool {
		if !regexp.MustCompile(`^workouts_display_test_[a-z0-9_]+$`).MatchString(c.Database) ||
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
		t.Fatal("catalog mutations require a named workouts_display_test_ loopback scratch database without remote fallbacks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var safe, locked bool
	if err := admin.QueryRow(ctx, `SELECT current_database()=$1 AND rolsuper,
		pg_try_advisory_lock(320032,hashtext(current_database())) FROM pg_roles WHERE rolname=current_user`, config.ConnConfig.Database).Scan(&safe, &locked); err != nil || !safe || !locked {
		t.Fatalf("explicit scratch superuser and exclusive mutation harness required: safe=%t locked=%t err=%v", safe, locked, err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_version=31 AND minimum_runtime_version=31 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton`).Scan(&safe); err != nil || !safe {
		t.Fatalf("mutation harness requires current schema31/min31/marker27 metadata: %v", err)
	}

	roles := []struct{ name, env string }{
		{"workouts_api", "API_DATABASE_URL"}, {"workouts_worker", "WORKER_DATABASE_URL"},
		{"workouts_coverage_worker", "COVERAGE_WORKER_DATABASE_URL"},
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
			t.Fatalf("%s must directly authenticate the expected role on the same scratch endpoint", role.env)
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
		if !Ready(ctx, pool) {
			t.Fatalf("complete runtime30 schema must be ready for %s", role.name)
		}
		for _, runtime := range []int{28, 29} {
			var oldReady bool
			if err := pool.QueryRow(ctx, runtimeFullReadinessSQL, runtime).Scan(&oldReady); err != nil || oldReady {
				t.Fatalf("runtime%d must be rejected for %s: ready=%t err=%v", runtime, role.name, oldReady, err)
			}
		}
		var gated bool
		if err := pool.QueryRow(ctx, runtimeCoverageDisplayReadinessSQL, 29).Scan(&gated); err != nil || !gated {
			t.Fatalf("display object checks must remain gated below runtime30 for %s: %v", role.name, err)
		}
		pools = append(pools, pool)
	}

	type mutation struct{ name, sql, restore string }
	mutations := []mutation{
		{"minimum runtime lowered", `UPDATE app.schema_metadata SET minimum_runtime_version=29 WHERE singleton`,
			`UPDATE app.schema_metadata SET minimum_runtime_version=31 WHERE singleton`},
		{"minimum runtime raised", fmt.Sprintf(`UPDATE app.schema_metadata SET schema_version=%d,minimum_runtime_version=%d WHERE singleton`, SupportedSchemaVersion+1, SupportedSchemaVersion+1),
			`UPDATE app.schema_metadata SET schema_version=31,minimum_runtime_version=31 WHERE singleton`},
		{"extra source invalidator", `CREATE TRIGGER extra_display_dirty AFTER UPDATE ON app.path_segments FOR EACH ROW EXECUTE FUNCTION app.mark_coverage_display_dirty()`,
			`DROP TRIGGER extra_display_dirty ON app.path_segments`},
		{"extra deferred callback", `CREATE TRIGGER extra_display_flush AFTER INSERT ON app.coverage_display_dirty FOR EACH ROW EXECUTE FUNCTION app.flush_coverage_display_dirty_account()`,
			`DROP TRIGGER extra_display_flush ON app.coverage_display_dirty`},
		{"queue source FK forbidden", `ALTER TABLE app.coverage_display_dirty ADD CONSTRAINT extra_source_fk FOREIGN KEY (account_id) REFERENCES app.accounts(id)`,
			`ALTER TABLE app.coverage_display_dirty DROP CONSTRAINT extra_source_fk`},
	}
	quote := func(name string) string { return pgx.Identifier{name}.Sanitize() }
	for _, table := range []string{"workout_path_display_partials", "workout_path_display_context", "workout_park_display_bounds", "coverage_display_dirty"} {
		relation := "app." + quote(table)
		alter := "ALTER TABLE " + relation + " "
		policy := quote(table + "_owner_policy")
		mutations = append(mutations,
			mutation{table + " missing", alter + "RENAME TO missing_display_table", `ALTER TABLE app.missing_display_table RENAME TO ` + quote(table)},
			mutation{table + " owner", alter + "OWNER TO workouts_migration", alter + "OWNER TO workouts_security_owner"},
			mutation{table + " RLS", alter + "DISABLE ROW LEVEL SECURITY", alter + "ENABLE ROW LEVEL SECURITY"},
			mutation{table + " forced RLS", alter + "NO FORCE ROW LEVEL SECURITY", alter + "FORCE ROW LEVEL SECURITY"},
			mutation{table + " policy missing", "DROP POLICY " + policy + " ON " + relation,
				"CREATE POLICY " + policy + " ON " + relation + " TO workouts_security_owner USING (true) WITH CHECK (true)"},
			mutation{table + " extra policy", "CREATE POLICY extra_display_policy ON " + relation + " USING (true)", "DROP POLICY extra_display_policy ON " + relation},
			mutation{table + " policy role", "ALTER POLICY " + policy + " ON " + relation + " TO PUBLIC", "ALTER POLICY " + policy + " ON " + relation + " TO workouts_security_owner"},
			mutation{table + " policy predicate", "ALTER POLICY " + policy + " ON " + relation + " USING (false)", "ALTER POLICY " + policy + " ON " + relation + " USING (true)"},
			mutation{table + " policy check", "ALTER POLICY " + policy + " ON " + relation + " WITH CHECK (false)", "ALTER POLICY " + policy + " ON " + relation + " WITH CHECK (true)"},
			mutation{table + " extra column", alter + "ADD COLUMN coordinates jsonb", alter + "DROP COLUMN coordinates"},
			mutation{table + " column default", alter + "ALTER COLUMN account_id SET DEFAULT '00000000-0000-0000-0000-000000000000'::uuid", alter + "ALTER COLUMN account_id DROP DEFAULT"},
			mutation{table + " arbitrary ACL", "GRANT SELECT ON " + relation + " TO workouts_migration", "REVOKE SELECT ON " + relation + " FROM workouts_migration"},
		)
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			for _, privilege := range []string{"SELECT", "UPDATE(account_id)"} {
				mutations = append(mutations, mutation{table + " " + role + " " + privilege,
					"GRANT " + privilege + " ON " + relation + " TO " + role, "REVOKE " + privilege + " ON " + relation + " FROM " + role})
			}
		}
		rows, err := admin.Query(ctx, `SELECT conname,contype::text,pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid=to_regclass($1) AND contype IN ('p','u','f','c') ORDER BY conname`, "app."+table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, kind, definition string
			if err := rows.Scan(&name, &kind, &definition); err != nil {
				t.Fatal(err)
			}
			drop := alter + "DROP CONSTRAINT " + quote(name)
			add := alter + "ADD CONSTRAINT " + quote(name) + " "
			mutations = append(mutations, mutation{table + " missing " + name, drop, add + definition})
			if kind == "c" || kind == "f" {
				mutations = append(mutations, mutation{table + " unvalidated " + name, drop + "; " + add + definition + " NOT VALID", drop + "; " + add + definition})
			}
			if kind == "f" {
				mutations = append(mutations, mutation{table + " noncascading FK", drop + "; " + add + strings.ReplaceAll(definition, "ON DELETE CASCADE", "ON DELETE RESTRICT"), drop + "; " + add + definition})
			}
			if kind == "u" {
				mutations = append(mutations, mutation{table + " NULLS DISTINCT queue", drop + "; " + add + strings.ReplaceAll(definition, " NULLS NOT DISTINCT", ""), drop + "; " + add + definition})
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	for _, column := range []string{"education_key_present", "region_id", "minimum_longitude"} {
		alter := "ALTER TABLE app.workout_path_display_partials ALTER COLUMN " + column
		mutations = append(mutations, mutation{"partials nullable " + column, alter + " DROP NOT NULL", alter + " SET NOT NULL"})
	}
	mutations = append(mutations,
		mutation{"context column type", `ALTER TABLE app.workout_path_display_context ALTER COLUMN locality_name TYPE varchar`, `ALTER TABLE app.workout_path_display_context ALTER COLUMN locality_name TYPE text`},
		// Valid fixtures contain NULL regions; mutate only the catalog contract,
		// without rewriting evidence to satisfy an intentionally broken schema.
		mutation{"bounds nullable region", `UPDATE pg_attribute SET attnotnull=true WHERE attrelid='app.workout_park_display_bounds'::regclass AND attname='region_id'`,
			`UPDATE pg_attribute SET attnotnull=false WHERE attrelid='app.workout_park_display_bounds'::regclass AND attname='region_id'`},
	)
	for _, index := range []string{"workout_path_display_partials_pkey", "workout_path_display_context_pkey", "workout_park_display_bounds_pkey"} {
		for _, flag := range []string{"indisvalid", "indisready"} {
			update := "UPDATE pg_index SET " + flag + "="
			where := " WHERE indexrelid='app." + index + "'::regclass"
			mutations = append(mutations, mutation{index + " " + flag, update + "false" + where, update + "true" + where})
		}
	}
	for _, index := range []struct{ name, entity, alternate, include string }{
		{"workout_path_display_partials_entity_idx", "entity_id", "logical_path_id", "logical_path_id"},
		{"workout_path_display_context_entity_idx", "entity_id", "locality_name", "locality_name"},
		{"workout_park_display_bounds_park_idx", "park_id", "region_id", "region_id"},
	} {
		var definition string
		if err := admin.QueryRow(ctx, `SELECT pg_get_indexdef(to_regclass($1))`, "app."+index.name).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		drop := "DROP INDEX app." + index.name
		mutations = append(mutations, mutation{index.name + " missing", drop, definition})
		for _, flag := range []string{"indisvalid", "indisready", "indisunique"} {
			update := "UPDATE pg_index SET " + flag + "="
			where := " WHERE indexrelid='app." + index.name + "'::regclass"
			bad, original := "false", "true"
			if flag == "indisunique" {
				bad, original = "true", "false"
			}
			mutations = append(mutations, mutation{index.name + " " + flag, update + bad + where, update + original + where})
		}
		columns := "(account_id, " + index.entity + ", workout_id)"
		variants := map[string]string{
			"predicate":  definition + " WHERE (workout_id IS NOT NULL)",
			"include":    definition + " INCLUDE (" + index.include + ")",
			"key order":  strings.Replace(definition, columns, "(account_id, workout_id, "+index.entity+")", 1),
			"key column": strings.Replace(definition, columns, "(account_id, "+index.alternate+", workout_id)", 1),
			"descending": strings.Replace(definition, columns, "(account_id, "+index.entity+" DESC, workout_id)", 1),
		}
		for name, variant := range variants {
			if variant == definition {
				t.Fatalf("index mutation %s did not change %s", name, index.name)
			}
			mutations = append(mutations, mutation{index.name + " " + name, drop + "; " + variant, drop + "; " + definition})
		}
	}
	mutations = append(mutations, mutation{"queue index null semantics",
		`UPDATE pg_index SET indnullsnotdistinct=false WHERE indrelid='app.coverage_display_dirty'::regclass`,
		`UPDATE pg_index SET indnullsnotdistinct=true WHERE indrelid='app.coverage_display_dirty'::regclass`})

	for _, signature := range []string{"app.rebuild_coverage_display_precomputations(uuid)", "app.flush_coverage_display_precomputations(uuid)",
		"app.mark_coverage_display_dirty()", "app.flush_coverage_display_dirty_account()", "app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text)",
		"app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)",
		"app.coverage_display_source_visits(uuid,date,date)"} {
		var definition string
		if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef(to_regprocedure($1))`, signature).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		alter := "ALTER FUNCTION " + signature + " "
		mutations = append(mutations,
			mutation{signature + " invoker", alter + "SECURITY INVOKER", definition},
			mutation{signature + " volatility", alter + "IMMUTABLE", definition},
			mutation{signature + " search path", alter + "SET search_path=app,pg_catalog", definition},
			mutation{signature + " extra config", alter + "SET enable_seqscan=off", alter + "RESET enable_seqscan; " + definition},
			mutation{signature + " owner", alter + "OWNER TO workouts_migration", alter + "OWNER TO workouts_security_owner"},
		)
		reader := strings.HasPrefix(signature, "app.map_selection_coverage_")
		stats := strings.Contains(signature, "map_selection_coverage_entity_stats")
		if stats {
			withDefault := strings.Replace(definition, "target_entity_id uuid)", "target_entity_id uuid DEFAULT NULL::uuid)", 1)
			if withDefault == definition {
				t.Fatal("missing stats argument-default mutation anchor")
			}
			mutations = append(mutations,
				// CREATE OR REPLACE cannot remove a default. Reset its catalog
				// fields rather than dropping the function and its owner/ACLs.
				mutation{"stats argument default", withDefault,
					"UPDATE pg_proc SET pronargdefaults=0,proargdefaults=NULL WHERE oid='" + signature + "'::regprocedure; " + definition},
				mutation{"stats function missing", alter + "RENAME TO missing_display_stats",
					"ALTER FUNCTION app.missing_display_stats(uuid,uuid,uuid,bigint,text,uuid) RENAME TO map_selection_coverage_entity_stats"},
			)
		}
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles", "workouts_migration"} {
			grant, revoke := "GRANT EXECUTE ON FUNCTION "+signature+" TO "+role, "REVOKE EXECUTE ON FUNCTION "+signature+" FROM "+role
			if reader && role == "workouts_api" {
				mutations = append(mutations, mutation{signature + " missing API grant", revoke, grant},
					mutation{signature + " API grant option", grant + " WITH GRANT OPTION", "REVOKE GRANT OPTION FOR EXECUTE ON FUNCTION " + signature + " FROM workouts_api"})
			} else {
				mutations = append(mutations, mutation{signature + " ACL " + role, grant, revoke})
			}
		}
		// Each replacement preserves the signature, attributes and all other body
		// bytes, proving that catalog-only presence checks cannot admit corruption.
		changes := [][2]string{{"RETURN NULL;", "RETURN NEW;"}}
		collisions := map[string][2]string{}
		switch {
		case strings.Contains(signature, "rebuild_"):
			changes = [][2]string{{"WITH evidence AS MATERIALIZED (", "WITH evidence AS NOT MATERIALIZED ("}, {"ST_XMin(ST_Extent(geom)::box2d)", "0::double precision"}}
			collisions["literal whitespace collision"] = [2]string{"'state_park'", "'state_ park'"}
		case strings.Contains(signature, "flush_coverage_display_precomputations"):
			changes = [][2]string{{"SET generation = generation", "SET generation = generation + 1"},
				{"UPDATE app.account_data_generations SET generation = generation WHERE account_id = target_account_id;", "PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account_id;"},
				{"AND match.generation_id = dirty.generation_id", "AND true"},
				{"PERFORM app.rebuild_coverage_display_precomputations(target_account_id);", "PERFORM 1;"}}
			collisions["comment newline barrier collision"] = [2]string{
				"-- public generation unchanged and take this barrier only at flush time.\n    UPDATE",
				"-- public generation unchanged and take this barrier only at flush time. UPDATE"}
		case reader:
			mutations = append(mutations, mutation{signature + " JIT", alter + "SET jit=on", definition})
			changes = [][2]string{
				{"IF authorized_id IS NULL THEN RETURN; END IF;", "IF false THEN RETURN; END IF;"},
				{"entity.account_id = target_account_id", "true"},
				{"entity.context_id = authorized_id", "true"},
				{"entity.range_workout_count > 0", "true"},
			}
			if stats {
				changes = append(changes,
					[2]string{"entity.canonical_id = target_entity_id", "true"},
					[2]string{"entity.entity_kind = target_entity_kind", "true"},
				)
			}
		case strings.Contains(signature, "coverage_display_source_visits"):
			changes = [][2]string{
				{"AND coverage.applied_route_input_revision IS NOT NULL", "AND true"},
				{"WHERE path.name IS NOT NULL OR partial.qualifies_without_name", "WHERE true"},
				{"workout.deletion_requested_at IS NULL", "true"},
				{"route.route IS NOT NULL", "true"},
			}
		}
		for i, change := range changes {
			if !strings.Contains(definition, change[0]) {
				t.Fatalf("missing body mutation anchor %q in %s", change[0], signature)
			}
			mutations = append(mutations, mutation{fmt.Sprintf("%s body %d", signature, i), strings.Replace(definition, change[0], change[1], 1), definition})
		}
		for name, change := range collisions {
			variant := strings.ReplaceAll(definition, change[0], change[1])
			if variant == definition || strings.Join(strings.Fields(variant), "") != strings.Join(strings.Fields(definition), "") {
				t.Fatalf("%s must alter semantics without changing the whitespace-normalized definition", name)
			}
			mutations = append(mutations, mutation{name, variant, definition})
		}
	}

	for _, trigger := range []struct{ table, name string }{
		{"workout_segment_matches", "workout_segment_matches_display_dirty_after_write"},
		{"path_segments", "path_segments_display_dirty_after_change"},
		{"coverage_display_dirty", "coverage_display_dirty_account_flush"},
	} {
		var definition string
		if err := admin.QueryRow(ctx, `SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid=to_regclass($1) AND tgname=$2`, "app."+trigger.table, trigger.name).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		drop := "DROP TRIGGER " + trigger.name + " ON app." + trigger.table
		for _, mode := range []string{"DISABLE", "ENABLE REPLICA", "ENABLE ALWAYS"} {
			mutations = append(mutations, mutation{trigger.name + " " + mode,
				"ALTER TABLE app." + trigger.table + " " + mode + " TRIGGER " + trigger.name,
				"ALTER TABLE app." + trigger.table + " ENABLE TRIGGER " + trigger.name})
		}
		mutations = append(mutations, mutation{trigger.name + " missing", drop, definition})
		variants := map[string]string{}
		if trigger.table == "coverage_display_dirty" {
			variants["initially immediate"] = strings.Replace(definition, "INITIALLY DEFERRED", "INITIALLY IMMEDIATE", 1)
			variants["ordinary trigger"] = strings.Replace(strings.Replace(definition, "CREATE CONSTRAINT TRIGGER", "CREATE TRIGGER", 1), " DEFERRABLE INITIALLY DEFERRED", "", 1)
			variants["account qualification"] = strings.Replace(definition, "new.scope = 'account'::text", "new.scope <> 'segment'::text", 1)
			variants["missing qualification"] = strings.Replace(definition, " WHEN ((new.scope = 'account'::text))", "", 1)
		} else {
			if trigger.table == "workout_segment_matches" {
				variants["events narrowed"] = strings.Replace(definition, "INSERT OR DELETE OR UPDATE", "UPDATE", 1)
			} else {
				variants["events broadened"] = strings.Replace(definition, "DELETE OR UPDATE", "INSERT OR DELETE OR UPDATE", 1)
				variants["columns narrowed"] = strings.Replace(definition, "DELETE OR UPDATE", "DELETE OR UPDATE OF tags", 1)
			}
			variants["unexpected qualification"] = strings.Replace(definition, " EXECUTE FUNCTION ", " WHEN (app.current_account_id() IS NOT NULL) EXECUTE FUNCTION ", 1)
			variants["wrong function"] = strings.Replace(definition, "app.mark_coverage_display_dirty()", "app.flush_coverage_display_dirty_account()", 1)
			variants["unexpected arguments"] = strings.Replace(definition, "app.mark_coverage_display_dirty()", "app.mark_coverage_display_dirty('unexpected')", 1)
		}
		for name, variant := range variants {
			if variant == definition {
				t.Fatalf("trigger mutation %s did not change %s", name, trigger.name)
			}
			mutations = append(mutations, mutation{trigger.name + " " + name, drop + "; " + variant, drop + "; " + definition})
		}
	}

	for _, mutation := range mutations {
		if !t.Run(mutation.name, func(t *testing.T) {
			// Committed catalog changes are necessary: genuine runtime login
			// sessions must observe the mutation, not an administrator's SET ROLE.
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
				if err := pool.QueryRow(ctx, runtimeFullReadinessSQL, SupportedSchemaVersion).Scan(&ready); err != nil || ready {
					t.Errorf("%s full readiness=%t want false, err=%v", roles[i].name, ready, err)
				}
			}
		}) {
			t.Fatal("stopping mutation harness after failure")
		}
	}
	for i, pool := range pools {
		if !Ready(ctx, pool) {
			t.Errorf("restored schema must be ready for %s", roles[i].name)
		}
		for _, runtime := range []int{28, 29} {
			var oldReady bool
			if err := pool.QueryRow(ctx, runtimeFullReadinessSQL, runtime).Scan(&oldReady); err != nil || oldReady {
				t.Errorf("restored schema must reject runtime%d for %s: ready=%t err=%v", runtime, roles[i].name, oldReady, err)
			}
		}
	}
	t.Logf("verified %d committed mutations through each of three restricted runtime logins; runtime28/29 rejected", len(mutations))
}
