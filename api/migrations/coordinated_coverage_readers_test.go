package migrations

import (
	"fmt"
	"regexp"
	stdstrings "strings"
	"testing"
)

func TestCoordinatedCoverageReadersContract(t *testing.T) {
	source, err := Files.ReadFile("023_coordinated_coverage_readers.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := sqlStrings.Split(string(source), "-- +goose Down")[0]
	for _, required := range []string{
		"ADD COLUMN reader_cluster_key text", "ADD COLUMN reader_backend_key text",
		"reader_cluster_key IS NULL AND reader_backend_key IS NULL",
		"reader_cluster_key IS NOT NULL AND reader_backend_key IS NOT NULL",
		"reader_cluster_key ~ '^[0-9a-f]{64}$' AND reader_backend_key ~ '^[0-9a-f]{64}$'",
		"ON app.matcher_slots (reader_cluster_key, reader_backend_key, expires_at)",
		"ADD COLUMN reader_limit integer NOT NULL DEFAULT 1 CHECK (reader_limit > 0)",
		"TO workouts_api, workouts_coverage_worker", "minimum_runtime_version = 23",
		"runtime_version IS NULL OR runtime_version < 23",
	} {
		if !sqlStrings.Contains(up, required) {
			t.Errorf("reader coordination lacks %q", required)
		}
	}
	for _, table := range []string{"matcher_slots", "matcher_slot_limits", "matcher_slot_guard"} {
		if !sqlStrings.Contains(up, "ALTER TABLE app."+table+" FORCE ROW LEVEL SECURITY") {
			t.Errorf("%s is not FORCE RLS protected", table)
		}
	}
	for _, forbidden := range []string{"SET global_limit", "SET account_limit", "SET region_limit", "INSERT INTO app.matcher_slot_limits"} {
		if sqlStrings.Contains(up, forbidden) {
			t.Errorf("migration resets existing admission limits: %s", forbidden)
		}
	}
	assign := functionBody(t, up, "CREATE FUNCTION app.assign_matcher_reader (")
	for _, required := range []string{
		"app.current_account_id()", "account.state = 'active' FOR SHARE",
		"s.owner_id = owner AND s.lease_token = lease", "s.expires_at >= clock_timestamp()",
		"job.status = 'running' AND job.cancel_requested_at IS NULL", "job.lease_expires_at >= clock_timestamp()",
		"cardinality(eligible_backend_keys) NOT BETWEEN 1 AND 64", "array_ndims(eligible_backend_keys) IS DISTINCT FROM 1",
		"count(DISTINCT key)", "s.reader_cluster_key = cluster_key", "s.slot_token <> slot",
		"HAVING count(s.slot_token) < backend_limit", "ORDER BY count(s.slot_token), md5(candidate.key || slot::text)",
		"reader_cluster_key = cluster_key, reader_backend_key = chosen",
	} {
		if !sqlStrings.Contains(assign, required) {
			t.Errorf("assignment lacks %q", required)
		}
	}
	jobLock := sqlStrings.Index(assign, "FOR UPDATE;")
	guard := sqlStrings.Index(assign, "FROM app.matcher_slot_guard")
	postFence := sqlStrings.Index(assign, "IF reservation.slot_kind = 'production' AND NOT EXISTS")
	if jobLock < 0 || guard <= jobLock || postFence <= guard {
		t.Fatal("assignment must lock job before guard and recheck lease after guard")
	}
	for _, name := range []string{"acquire_coverage_matcher_slot", "acquire_diagnostic_matcher_slot"} {
		body := functionBody(t, up, "CREATE OR REPLACE FUNCTION app."+name+" (")
		if sqlStrings.Contains(body, "NOT EXISTS (SELECT 1 FROM app.jobs") || !sqlStrings.Contains(body, "expired.expires_at < clock_timestamp()") {
			t.Errorf("%s must clean up expired reservations only", name)
		}
	}
	clear := functionBody(t, up, "CREATE FUNCTION app.clear_matcher_reader (")
	for _, forbidden := range []string{"DELETE FROM", "app.jobs", "status = 'running'"} {
		if sqlStrings.Contains(clear, forbidden) {
			t.Errorf("clear must preserve admission and work after terminal job: %s", forbidden)
		}
	}
	if !sqlStrings.Contains(clear, "s.expires_at >= clock_timestamp()") {
		t.Fatal("clear lacks unexpired owner fence")
	}
	heartbeat := functionBody(t, up, "CREATE FUNCTION app.heartbeat_diagnostic_matcher_slot (")
	for _, required := range []string{
		"RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER", "SET search_path = pg_catalog, app",
		"slot_duration IS NULL", "slot_duration < interval '1 second'", "slot_duration > interval '2 minutes'",
		"account.id = app.current_account_id() AND account.state = 'active' FOR SHARE",
		"s.slot_kind = 'diagnostic'", "s.owner_id = owner AND s.lease_token = lease FOR UPDATE",
		"SET expires_at = clock_timestamp() + slot_duration", "s.expires_at >= clock_timestamp()",
	} {
		if !sqlStrings.Contains(heartbeat, required) {
			t.Errorf("diagnostic heartbeat lacks %q", required)
		}
	}
	if sqlStrings.Index(heartbeat, "FOR UPDATE;") >= sqlStrings.Index(heartbeat, "UPDATE app.matcher_slots") {
		t.Fatal("diagnostic heartbeat checks expiry before taking reservation lock")
	}
	for _, forbidden := range []string{"reader_cluster_key", "reader_backend_key", "DELETE FROM", "INSERT INTO", "app.jobs"} {
		if sqlStrings.Contains(heartbeat, forbidden) {
			t.Errorf("diagnostic heartbeat must update only reservation expiry: %s", forbidden)
		}
	}
	for _, required := range []string{
		"ALTER FUNCTION app.heartbeat_diagnostic_matcher_slot (uuid, text, uuid, interval) OWNER TO workouts_security_owner",
		"REVOKE ALL ON FUNCTION app.heartbeat_diagnostic_matcher_slot (uuid, text, uuid, interval) FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles",
		"GRANT EXECUTE ON FUNCTION app.heartbeat_diagnostic_matcher_slot (uuid, text, uuid, interval) TO workouts_api;",
	} {
		if !sqlStrings.Contains(up, required) {
			t.Errorf("diagnostic heartbeat ACL lacks %q", required)
		}
	}
	for _, required := range []string{
		"'app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)'",
		"'app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)'",
		"pg_get_functiondef(signature::regprocedure)", "parent.cancel_requested_at IS NULL FOR UPDATE OF parent",
		"child.kind = ''coverage_update_route'' AND child.cancel_requested_at IS NULL",
		"job.cancel_requested_at IS NULL AND parent.cancel_requested_at IS NULL",
		"job.lease_token = current_lease_token AND job.lease_expires_at >= clock_timestamp()",
		"unexpected coverage completion definition",
	} {
		if !sqlStrings.Contains(up, required) {
			t.Errorf("coverage completion lock patch lacks %q", required)
		}
	}
	cancellation := functionBody(t, up, "CREATE FUNCTION app.job_cancel_requested (")
	for _, required := range []string{
		"RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER", "SET search_path = pg_catalog, app",
		"child.cancel_requested_at IS NOT NULL OR parent.cancel_requested_at IS NOT NULL",
		"parent.account_id = child.account_id", "account.state = 'active'",
		"child.id = job AND child.account_id = app.current_account_id()",
		"child.kind = 'coverage_update_route' AND child.status = 'running'",
		"child.worker_id = owner AND child.lease_token = lease AND child.lease_expires_at >= clock_timestamp()",
		"parent.kind = 'coverage_update' AND parent.status IN ('queued', 'running')",
	} {
		if !sqlStrings.Contains(cancellation, required) {
			t.Errorf("cancellation helper lacks %q", required)
		}
	}
	for _, forbidden := range []string{"UPDATE app.", "INSERT INTO", "DELETE FROM", "COALESCE", "FOR SHARE", "FOR UPDATE"} {
		if sqlStrings.Contains(cancellation, forbidden) {
			t.Errorf("cancellation helper must be a nullable, read-only observation: %s", forbidden)
		}
	}
	for _, required := range []string{
		"ALTER FUNCTION app.job_cancel_requested (uuid, text, uuid) OWNER TO workouts_security_owner",
		"REVOKE ALL ON FUNCTION app.job_cancel_requested (uuid, text, uuid) FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles",
		"GRANT EXECUTE ON FUNCTION app.job_cancel_requested (uuid, text, uuid) TO workouts_coverage_worker;",
	} {
		if !sqlStrings.Contains(up, required) {
			t.Errorf("cancellation helper ACL lacks %q", required)
		}
	}
}

func TestCoordinatedCoverageReaderUpgradeAnchors(t *testing.T) {
	source, err := Files.ReadFile("023_coordinated_coverage_readers.sql")
	if err != nil {
		t.Fatal(err)
	}
	pattern := func(name string) string {
		t.Helper()
		match := regexp.MustCompile(`(?s)` + name + ` text := \$pattern\$(.*?)\$pattern\$;`).FindStringSubmatch(string(source))
		if len(match) != 2 {
			t.Fatalf("missing PostgreSQL ARE pattern %s", name)
		}
		// These ASCII token patterns use PostgreSQL word-start/end boundaries;
		// Go's word boundary is equivalent at these fixed keyword positions.
		return stdstrings.NewReplacer(`\m`, `\b`, `\M`, `\b`).Replace(match[1])
	}
	floor := regexp.MustCompile(pattern("floor_pattern"))
	selectIntro := regexp.MustCompile(pattern("select_pattern"))
	for _, test := range []struct {
		name, source string
		pattern      *regexp.Regexp
		count        int
	}{
		{"claim formatted", "IF runtime_version < 15 THEN RAISE EXCEPTION 'message';", floor, 1},
		{"claim live compact", "IF runtime_version<15 THEN RAISE EXCEPTION 'message';", floor, 1},
		{"claim mixed whitespace", "iF\truntime_version\n<\t15\nThEn RAISE EXCEPTION 'message';", floor, 1},
		{"claim duplicate", "IF runtime_version<15 THEN IF runtime_version < 15 THEN", floor, 2},
		{"unsupported floor", "IF runtime_version<22 THEN", floor, 0},
		{"different identifier", "IF old_runtime_version<15 THEN", floor, 0},
		{"quoted unsupported identifier", `IF "runtime_version"<15 THEN`, floor, 0},
		{"select formatted", "SELECT job.account_id, context.workout_id, context.route_input_revision, context.route_input_sha256,", selectIntro, 1},
		{"select live compact", "SELECT job.account_id,context.workout_id,context.route_input_revision,context.route_input_sha256,", selectIntro, 1},
		{"select mixed whitespace", "select\tJOB . account_id,\nCONTEXT.workout_id ,context . route_input_revision,context.route_input_sha256,", selectIntro, 1},
		{"different alias", "SELECT other.account_id,context.workout_id,context.route_input_revision,context.route_input_sha256,", selectIntro, 0},
		{"quoted unsupported alias", `SELECT "job".account_id,context.workout_id,context.route_input_revision,context.route_input_sha256,`, selectIntro, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if matches := test.pattern.FindAllStringIndex(test.source, -1); len(matches) != test.count {
				t.Fatalf("anchor matches=%d want=%d", len(matches), test.count)
			}
		})
	}
	for _, value := range []string{"NULL", "false"} {
		rejection := regexp.MustCompile(fmt.Sprintf(pattern("rejection_template"), value))
		for _, text := range []string{
			"IF NOT FOUND THEN RETURN " + value + "; END IF;",
			"if not found then return " + value + ";end if;",
			"IF\nNOT\tFOUND THEN RETURN\n" + value + " ;\nEND\tIF;",
		} {
			if matches := rejection.FindAllStringIndex(text, -1); len(matches) != 1 {
				t.Fatalf("rejection anchor %q matches=%d", text, len(matches))
			}
		}
	}
	for _, required := range []string{
		"anchor_count <> 1", "select_count <> 1 OR rejection_count <> 1",
		"regexp_matches(definition, floor_pattern, 'g')", "regexp_matches(definition, select_pattern, 'g')",
		"regexp_matches(definition, rejection_pattern, 'g')", "USING ERRCODE = '55000'",
		"regexp_replace(definition, rejection_pattern, E'\\\\&' || fence)",
	} {
		if !stdstrings.Contains(string(source), required) {
			t.Errorf("upgrade must preserve matches and reject ambiguous definitions: %q", required)
		}
	}
	if stdstrings.Index(string(source), "regexp_replace(definition, rejection_pattern") > stdstrings.Index(string(source), "regexp_replace(definition, select_pattern") {
		t.Fatal("must patch original rejection before introducing parent rejection")
	}
	literal := "'keep  THIS\tmessage  exactly'"
	input := "IF runtime_version<15 THEN RAISE EXCEPTION " + literal + "; END IF;"
	output := floor.ReplaceAllString(input, "IF runtime_version IS NULL OR runtime_version < 23 THEN")
	if !stdstrings.Contains(output, literal) {
		t.Fatal("anchor replacement modified unrelated literal whitespace")
	}
}
