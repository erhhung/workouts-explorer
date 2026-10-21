package database

import (
	"strings"
	"testing"
)

func TestLegacyCoverageBootstrapReadiness(t *testing.T) {
	for _, signature := range []string{
		"app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)",
		"app.read_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea)",
		"app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)",
		"app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)",
	} {
		if strings.Count(runtimeReadinessSQL, "'"+signature+"'::regprocedure") != 2 {
			t.Errorf("readiness must require both security owner and ACL checks for %s", signature)
		}
	}
	if !strings.Contains(runtimeReadinessSQL, "NOT has_table_privilege('workouts_coverage_worker','app.workout_coverage_states','INSERT,UPDATE,DELETE')") {
		t.Fatal("readiness permits direct coverage-state mutation")
	}
}
