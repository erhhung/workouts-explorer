package database

import (
	"strings"
	"testing"
)

func TestEligibleCohortReadinessRequiresComplete28(t *testing.T) {
	if SupportedSchemaVersion != 28 {
		t.Fatalf("supported schema = %d, want canonical 28", SupportedSchemaVersion)
	}
	for _, part := range []string{runtimeReadinessSQL, runtimeDeferredReadinessSQL, runtimeCohortReadinessSQL} {
		if !strings.Contains(runtimeFullReadinessSQL, part) {
			t.Fatal("Ready must require base, deferred and cohort contracts together")
		}
	}
	for _, required := range []string{
		"('app.ingest_coverage_builders','batch_size','integer')",
		"('app.ingest_coverage_prepared','route_input_sha256','bytea')",
		"('app.ingest_coverage_prepared','ownership_epoch','bigint')",
		"('app.ingest_coverage_prepared','generations','jsonb')",
		"('app.ingest_coverage_builders',9)", "('app.ingest_coverage_prepared',14)",
		"i.indisvalid AND i.indisready", "c.confdeltype='c'",
		"targets<builder.batch_size", "context.sealed_ingest_root_id IS NULL",
		"to_regprocedure('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision)') IS NULL",
	} {
		if !strings.Contains(runtimeCohortReadinessSQL, required) {
			t.Errorf("missing cohort readiness condition %q", required)
		}
	}
	for _, required := range []string{
		"('app.ingest_coverage_builders','ingest_coverage_builders_owner_policy')",
		"('app.ingest_coverage_prepared','ingest_coverage_prepared_owner_policy')",
		"('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)',true,true,true,false,'v')",
		"('app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer)',true,true,false,false,'v')",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing scoped ownership/ABI condition %q", required)
		}
	}
}
