package migrations

import (
	"regexp"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
)

func TestCoverageRetryEligibilityMigration(t *testing.T) {
	source, err := migrationfixture.PhaseSQL(Files, migrationfixture.RetryPresentation)
	if err != nil {
		t.Fatal(err)
	}
	parts := sqlStrings.Split(string(source), "-- +goose Down")
	body := functionBody(t, parts[0], "CREATE FUNCTION app.read_coverage_retry_eligibility")
	for _, required := range []string{
		"LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app",
		"job.account_id = app.current_account_id()", "account.state = 'active'", "job.parent_job_id IS NULL",
		"max_ordinal BETWEEN 1 AND 100", "app.coverage_retry_targets(owned.id,owned.account_id)",
		"app.coverage_retry_root(owned.id)", "successor.retry_of_job_id = owned.id", "ordinal >= max_ordinal",
		"state.readiness_state = 'map_data_ready' AND state.processing_state = 'current'",
		"unsuccessful > 0 AND current_count = unsuccessful", "ELSE 'no_eligible_routes'",
		"current_coverage_job_ids uuid[]", "historical AS MATERIALIZED", "current_child.id = state.target_job_id",
		"current_child.account_id = child.account_id", "current_context.workout_id = context.workout_id",
		"current_parent.account_id = child.account_id", "current_parent_context.account_id = child.account_id",
		"current_context.route_input_revision = state.applied_route_input_revision",
		"current_context.route_input_sha256 = state.applied_route_input_sha256",
		"app.coverage_generation_vectors_equal(current_context.target_generations,state.applied_generations)",
		"current_parent_context.rules_version = state.applied_rules_version",
		"current_parent_context.sampling_version = state.applied_sampling_version",
		"current_parent_context.path_policy_version = state.applied_path_policy_version",
		"current_parent_context.matcher_contract_version = app.current_coverage_matcher_contract()",
		"current_child.status = 'succeeded'", "GROUP BY current_parent_id, current_parent_created_at",
		"ORDER BY current_parent_created_at DESC, current_parent_id DESC LIMIT 100",
	} {
		if !sqlStrings.Contains(body, required) {
			t.Errorf("eligibility helper lacks %q", required)
		}
	}
	history := functionBody(t, parts[0], "CREATE FUNCTION app.read_job_history")
	for _, required := range []string{
		"LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app",
		"job.account_id = app.current_account_id()", "account.state = 'active'", "candidates AS MATERIALIZED",
		"eligibility AS MATERIALIZED", "candidate.status IN ('failed','partially_succeeded')",
		"app.read_coverage_retry_eligibility(candidate.id,max_ordinal)", "eligibility.reason = 'already_current' THEN 'superseded'",
		"projected AS MATERIALIZED", "filtered AS MATERIALIZED", "projected_status = status_filter",
		"plain_filtered AS MATERIALIZED", "plain_page AS MATERIALIZED", "proof_targets AS MATERIALIZED",
		"FROM proof_targets candidate", "FROM plain_page candidate", "COALESCE(status_filter,'') NOT IN ('failed','partially_succeeded','superseded')",
		"job.status = status_filter", "status_filter = 'superseded' AND job.kind = 'coverage_update'",
		"(SELECT count(*) FROM plain_filtered) + (SELECT count(*) FROM filtered)", "LEFT JOIN page ON true", "successor.account_id = job.account_id",
		"GRANT EXECUTE ON FUNCTION app.read_job_history(text,text,integer,bigint,integer) TO workouts_api",
	} {
		if !sqlStrings.Contains(parts[0], required) {
			t.Errorf("history projection lacks %q", required)
		}
	}
	if regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|PERFORM|CALL|FOR UPDATE|set_config)\b`).MatchString(history) {
		t.Fatal("history projection must remain SELECT-only")
	}
	if regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|PERFORM|CALL|FOR UPDATE|set_config)\b`).MatchString(body) {
		t.Fatal("eligibility helper must remain SELECT-only")
	}
	for _, forbidden := range []string{"current_parent.status =", "has_current_state", "owned.rules_version", "state.route_input_revision = context.route_input_revision"} {
		if sqlStrings.Contains(body, forbidden) {
			t.Errorf("current proof must not depend on old descriptors or whole-parent success: %q", forbidden)
		}
	}
	previous := -1
	for _, branch := range []string{
		"WHEN status NOT IN ('failed','partially_succeeded','cancelled')",
		"WHEN eligible = 0 AND unsuccessful > 0 AND current_count = unsuccessful THEN 'already_current'",
		"WHEN has_successor THEN 'not_latest'", "WHEN ordinal >= max_ordinal THEN 'retry_limit'", "WHEN eligible > 0 THEN 'eligible'",
	} {
		index := sqlStrings.Index(body, branch)
		if index <= previous {
			t.Fatalf("current proof must precede advisory latest/limit guards after terminal validation: %q", branch)
		}
		previous = index
	}
	for _, required := range []string{
		"OWNER TO workouts_security_owner", "FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration",
		"GRANT EXECUTE ON FUNCTION app.read_coverage_retry_eligibility(uuid,integer) TO workouts_api",
	} {
		if !sqlStrings.Contains(parts[0], required) {
			t.Errorf("migration lacks %q", required)
		}
	}
}
