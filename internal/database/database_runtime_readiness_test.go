package database

import (
	"fmt"
	"strings"
	"testing"
)

var runtimeCapabilityFunctions = []struct {
	signature, volatility string
	worker, coverage, api bool
}{
	{"app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)", "v", true, false, false},
	{"app.refresh_ingest_route_counts(uuid)", "v", false, false, false},
	{"app.retry_coverage_update(uuid,uuid,integer)", "v", false, false, true},
	{"app.read_coverage_retry_eligibility(uuid,integer)", "v", false, false, true},
	{"app.read_job_history(text,text,integer,bigint,integer)", "v", false, false, true},
	{"app.retry_next_coverage_update(integer,integer)", "v", false, true, false},
	{"app.construct_coverage_retry(uuid,uuid,integer,integer)", "v", false, false, false},
	{"app.coverage_retry_root(uuid)", "v", false, false, false},
	{"app.coverage_retry_targets(uuid,uuid)", "v", false, false, false},
	{"app.enroll_coverage_retry_failure()", "v", false, false, false},
	{"app.current_coverage_matcher_contract()", "s", false, false, false},
	{"app.coverage_auto_retry_parent_eligible(uuid,uuid)", "v", false, false, false},
}

func TestRuntimeCapabilityReadinessContracts(t *testing.T) {
	if !strings.Contains(runtimeReadinessSQL, "TABLE(job_id uuid, display_status text, total_items bigint)") {
		t.Fatal("history readiness must require the exact projection result")
	}
	if !strings.Contains(runtimeReadinessSQL, "TABLE(can_retry boolean, reason text, eligible_route_count bigint, unsuccessful_route_count bigint, current_route_count bigint, current_coverage_job_ids uuid[])") {
		t.Fatal("eligibility readiness must require the exact six-field result")
	}
	for _, function := range runtimeCapabilityFunctions {
		tuple := fmt.Sprintf("('%s',true,%t,%t,%t,'%s')", function.signature, function.worker, function.coverage, function.api, function.volatility)
		if !strings.Contains(runtimeReadinessSQL, tuple) {
			t.Errorf("missing exact function contract %s", tuple)
		}
	}
	for _, required := range []string{
		"('app.workout_import_events','has_route','boolean',false)",
		"('app.job_progress','workouts_created_with_route','bigint',false)",
		"('app.job_progress','workouts_updated_with_route','bigint',false)",
		"('app.coverage_job_contexts','matcher_contract_version','text',true)",
		"coverage_retry_roots_owner_policy", "coverage_retry_attempts_owner_policy",
		"coverage_retry_roots_budget_idx", "coverage_retry_attempts_predecessor_idx",
		"coverage_retry_attempts_root_job_id_automatic_ordinal_key", "coverage_retry_failure_enrollment",
		"automatic_ordinal IS NOT NULL", "automatic_ordinal IS NULL", "predecessor_job_id IS NOT NULL",
		"app.current_coverage_matcher_contract()", "pg_get_constraintdef(c.oid)=expected.definition",
		"pg_get_function_result(to_regprocedure(expected.signature)) IS DISTINCT FROM expected.result",
		"capability.backend_pid = pg_backend_pid()", "capability.transaction_id = txid_current()",
		"capability.account_id = target_account", "capability.job_id = job",
		"capability.worker_id = worker", "capability.lease_token = lease",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing runtime-capability readiness check %q", required)
		}
	}
}

func TestDiagnosticLimitParityReadinessContracts(t *testing.T) {
	for _, required := range []string{
		"('app.coverage_diagnostic_runs','original_point_count','integer',true)",
		"('app.coverage_diagnostic_runs','sampled_point_count','integer',true)",
		"('app.coverage_diagnostic_runs','CHECK (((original_point_count >= 0) AND (original_point_count <= 50000)))')",
		"('app.coverage_diagnostic_runs','CHECK (((sampled_point_count >= 0) AND (sampled_point_count <= 12000)))')",
		"diagnostic.name||'_account_policy'", "diagnostic.name||'_owner_policy'",
		"pg_get_expr(p.polwithcheck,p.polrelid)='(account_id = app.current_account_id())'",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing diagnostic parity readiness check %q", required)
		}
	}
}
