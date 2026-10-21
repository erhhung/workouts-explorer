package database

import (
	"strings"
	"testing"
)

func TestCoordinatedCoverageReadersReadiness(t *testing.T) {
	for _, signature := range []string{
		"app.assign_matcher_reader(uuid,text,uuid,text,text[])",
		"app.clear_matcher_reader(uuid,text,uuid)",
		"app.heartbeat_diagnostic_matcher_slot(uuid,text,uuid,interval)",
		"app.job_cancel_requested(uuid,text,uuid)",
	} {
		if strings.Count(runtimeReadinessSQL, "'"+signature+"'::regprocedure") != 2 {
			t.Errorf("readiness must check owner and execution ACLs for %s", signature)
		}
	}
	for _, required := range []string{
		"app.matcher_slots_reader_active_idx", "attname='reader_cluster_key'", "attname='reader_backend_key'", "attname='reader_limit'",
		"runtime_version IS NULL OR runtime_version < 26", "c.relrowsecurity AND c.relforcerowsecurity",
		"job.cancel_requested_at IS NULL AND parent.cancel_requested_at IS NULL",
		"'app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)'::regprocedure",
		"has_function_privilege('workouts_api',p.oid,'EXECUTE')",
		"has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')",
		"NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')",
		"NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')",
		"NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')",
		"p.provolatile='s'",
		"has_table_privilege(runtime_role.oid,private_table.name,'SELECT,INSERT,UPDATE,DELETE')",
		"has_any_column_privilege(runtime_role.oid,private_table.name,'SELECT,INSERT,UPDATE')",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("readiness lacks %q", required)
		}
	}
}
