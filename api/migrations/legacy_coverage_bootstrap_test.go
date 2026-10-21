package migrations

import (
	stdstrings "strings"
	"testing"
)

func TestLegacyCoverageBootstrapContract(t *testing.T) {
	source, err := Files.ReadFile("022_legacy_coverage_bootstrap.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := stdstrings.Split(string(source), "-- +goose Down")
	patch := stdstrings.Split(parts[0], "-- END SCHEMA 20 COMPATIBLE FUNCTION PATCH")[0]
	for _, forbidden := range []string{"CREATE TABLE", "ALTER TABLE", "GRANT", "REVOKE", "schema_metadata", "decode(", "repeat("} {
		if sqlStrings.Contains(patch, forbidden) {
			t.Errorf("schema-20 function patch contains %q", forbidden)
		}
	}
	if stdstrings.Count(patch, "CREATE OR REPLACE FUNCTION") != 5 {
		t.Fatal("hotpatch must contain only the five reviewed function replacements")
	}
	for _, name := range []string{"read_coverage_reconciliation_routes", "read_coverage_reconciliation_route", "repair_coverage_reconciliation_input"} {
		body := functionBody(t, patch, "CREATE OR REPLACE FUNCTION app."+name+" (")
		for _, required := range []string{"SECURITY DEFINER", "search_path = pg_catalog, app", "app.current_account_id()",
			"account.state = 'active'", "reconciliation.active_revision = reconciliation_revision",
			"reconciliation.worker_id = claiming_worker", "reconciliation.lease_token = current_lease_token",
			"reconciliation.lease_expires_at >= clock_timestamp()", "workout.deletion_requested_at IS NULL"} {
			if !sqlStrings.Contains(body, required) {
				t.Errorf("%s lacks %q", name, required)
			}
		}
	}
	list := functionBody(t, patch, "CREATE OR REPLACE FUNCTION app.read_coverage_reconciliation_routes (")
	for _, required := range []string{"LANGUAGE sql STABLE", "LEFT JOIN app.workout_coverage_states",
		"COALESCE(state.route_input_revision, 0)", "state.route_input_sha256", "workout.id > after_workout_id",
		"ORDER BY workout.id LIMIT LEAST(page_limit, 100)"} {
		if !sqlStrings.Contains(list, required) {
			t.Errorf("pagination lacks %q", required)
		}
	}
	read := functionBody(t, patch, "CREATE OR REPLACE FUNCTION app.read_coverage_reconciliation_route (")
	for _, required := range []string{"LANGUAGE sql STABLE", "state.workout_id IS NULL AND target_route_revision = 0 AND target_route_digest IS NULL",
		"state.route_input_revision = target_route_revision AND state.route_input_sha256 = target_route_digest", "LIMIT 50001"} {
		if !sqlStrings.Contains(read, required) {
			t.Errorf("point reader lacks %q", required)
		}
	}
	repair := functionBody(t, patch, "CREATE OR REPLACE FUNCTION app.repair_coverage_reconciliation_input (")
	for _, required := range []string{"old_route_revision = 0 AND old_route_digest IS NULL",
		"octet_length(new_route_digest) IS DISTINCT FROM 32", "octet_length(old_route_digest) IS DISTINCT FROM 32",
		"VALUES(target_account_id, target_workout_id, 1, new_route_digest, 'unresolved')",
		"ON CONFLICT (account_id, workout_id) DO NOTHING", "RETURN new_revision"} {
		if !sqlStrings.Contains(repair, required) {
			t.Errorf("bootstrap repair lacks %q", required)
		}
	}
	prior := -1
	for _, lock := range []string{"FROM app.accounts account", "FROM app.coverage_reconciliation_accounts reconciliation",
		"FROM app.workouts workout", "FROM app.workout_routes route", "FROM app.workout_coverage_states state", "INSERT INTO app.workout_coverage_states"} {
		index := stdstrings.Index(repair, lock)
		if index <= prior {
			t.Fatalf("missing or inverted bootstrap lock order: %s", lock)
		}
		prior = index
	}
	for _, required := range []string{"FOR UPDATE NOWAIT", "FOR SHARE NOWAIT", "WITH points AS MATERIALIZED",
		"count(*) BETWEEN 1 AND 50000", "pg_catalog.sha256(pg_catalog.int8send(count(*))", "string_agg(pg_catalog.int8send(point.sequence::bigint)",
		"point.recorded_at AT TIME ZONE 'UTC'", "'YYYY-MM-DD\"T\"HH24:MI:SS'", "'US'), '0')", "'UTF8'",
		"pg_catalog.float8send(point.latitude)", "pg_catalog.float8send(point.longitude)", "''::bytea ORDER BY point.sequence)",
		"current_digest IS DISTINCT FROM new_route_digest", "'coverage route input changed' USING ERRCODE = '40001'",
		"new_revision IS NOT NULL AND NOT EXISTS", "'coverage reconciliation lease expired' USING ERRCODE = '40001'",
		"set_config('lock_timeout', '1ms', true)", "set_config('lock_timeout', prior_lock_timeout, true)"} {
		if !sqlStrings.Contains(repair, required) {
			t.Errorf("repair hardening lacks %q", required)
		}
	}
	for _, field := range []string{"altitude", "speed", "course", "horizontal_accuracy", "vertical_accuracy", "speed_accuracy", "course_accuracy"} {
		if !sqlStrings.Contains(repair, "CASE WHEN point."+field+" IS NULL THEN '\\x00'::bytea ELSE '\\x01'::bytea || pg_catalog.float8send(point."+field+") END") {
			t.Errorf("canonical encoding lacks optional %s", field)
		}
	}
	write := stdstrings.Index(repair, "INSERT INTO app.workout_coverage_states")
	postFence := stdstrings.Index(repair, "IF new_revision IS NOT NULL AND NOT EXISTS")
	if stdstrings.Index(repair, "FROM app.workout_coverage_states state") > stdstrings.Index(repair, "WITH points AS MATERIALIZED") ||
		postFence <= write || stdstrings.Index(repair[postFence:], "RAISE EXCEPTION") < 0 {
		t.Fatal("state lock/digest validation/post-write rollback fences are out of order")
	}
	// The matching reader changes only its bound, not its authorization or shape.
	shipped, err := Files.ReadFile("016_durable_route_coverage.sql")
	if err != nil {
		t.Fatal(err)
	}
	original := functionBody(t, string(shipped), "CREATE FUNCTION app.read_coverage_route (")
	replacement := functionBody(t, patch, "CREATE OR REPLACE FUNCTION app.read_coverage_route (")
	original = stdstrings.Replace(original, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)
	original = stdstrings.Replace(original, "LIMIT 25001", "LIMIT 50001", 1)
	if compactSQL(original) != compactSQL(replacement) {
		t.Fatal("matching reader changes more than its overflow sentinel")
	}
	for _, required := range []string{"desired_revision = app.coverage_reconciliation_accounts.desired_revision + 1",
		"next_scan_at = transaction_timestamp()", "workout.deletion_requested_at IS NULL AND state.workout_id IS NULL",
		"schema_version = 22, minimum_runtime_version = 21", "completed AND reconciliation.desired_revision > reconciliation_revision"} {
		if !sqlStrings.Contains(parts[0], required) {
			t.Errorf("forward migration lacks %q", required)
		}
	}
	if !sqlStrings.Contains(parts[1], "forward-only") || !sqlStrings.Contains(parts[1], "ERRCODE = '55000'") {
		t.Fatal("bootstrap must reject downgrade")
	}
}
