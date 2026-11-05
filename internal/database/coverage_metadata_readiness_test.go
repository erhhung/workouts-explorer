package database

import (
	"crypto/md5"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestCoverageMetadataNativeBodiesPinned(t *testing.T) {
	feature := coverageMetadataFeature(t)
	functions := regexp.MustCompile(`CREATE (?:OR REPLACE )?FUNCTION app\.([a-z_]+)\(`).FindAllStringSubmatch(feature, -1)
	for _, function := range functions {
		body := metadataFunction(t, feature, function[1])
		expected := fmt.Sprintf("('%s','%x')", function[1], md5.Sum([]byte(body)))
		if !strings.Contains(runtimeCoverageMetadataReadinessSQL, expected) {
			t.Errorf("missing exact native function body fingerprint: %s", expected)
		}
	}
}

func coverageMetadataFeature(t *testing.T) string {
	t.Helper()
	raw, err := migrations.Files.ReadFile("029_ingest_status_reporting.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, feature, found := strings.Cut(string(raw), "-- BEGIN PRECISE NEAR-IDLE COVERAGE RECONCILIATION")
	if !found {
		t.Fatal("missing explicitly delimited feature")
	}
	feature, after, found := strings.Cut(feature, "-- END PRECISE NEAR-IDLE COVERAGE RECONCILIATION")
	if !found || !strings.Contains(after, "schema_version = 29, minimum_runtime_version = 29") ||
		!strings.Contains(after, "-- +goose Down") {
		t.Fatal("feature must precede the one-time 29/29 metadata checkpoint and Down")
	}
	if strings.Contains(feature, "UPDATE app.schema_metadata") {
		t.Fatal("feature added an intermediate schema/runtime checkpoint")
	}
	return feature
}

func metadataFunction(t *testing.T, feature, name string) string {
	t.Helper()
	pattern := `(?s)CREATE (?:OR REPLACE )?FUNCTION app\.` + regexp.QuoteMeta(name) + `\(.*?AS \$function\$(.*?)\$function\$;`
	match := regexp.MustCompile(pattern).FindStringSubmatch(feature)
	if match == nil {
		t.Fatalf("missing native feature function %s", name)
	}
	return match[1]
}

func TestCoverageMetadataReadinessComposition(t *testing.T) {
	if SupportedSchemaVersion != 31 || !strings.Contains(runtimeFullReadinessSQL, " AND ("+runtimeCoverageMetadataReadinessSQL+")") {
		t.Fatal("runtime30 must require the dedicated metadata feature readiness contract")
	}
	for _, required := range []string{"SELECT $1 < 29 OR (", "migration_baseline=27", "coverage_route_changes_dirty_idx",
		"coverage_route_selection_proofs_catalog_idx",
		"pg_get_userbyid(c.relowner)<>'workouts_security_owner'", "NOT c.relrowsecurity OR NOT c.relforcerowsecurity",
		"has_any_column_privilege", "aclexplode", "p.proconfig IS DISTINCT FROM", "p.proisstrict OR p.proleakproof",
		"pg_get_function_result(p.oid)", "t.tgnewtable", "t.tgoldtable", "i.indisvalid AND i.indisready AND i.indislive",
		"coverage_production_descriptor_current", "route selection input changed", "proof.route_input_revision=state.route_input_revision"} {
		if !strings.Contains(runtimeCoverageMetadataReadinessSQL, required) {
			t.Errorf("missing feature readiness guard %q", required)
		}
	}
	for _, guard := range []string{"pk.contype='p'", "NOT pk.condeferrable", "i.indisprimary AND i.indisunique AND i.indimmediate",
		"unnest(pk.conkey)", "unnest(i.indkey)", "ARRAY['account_id','transaction_id']", "ARRAY['account_id','workout_id']"} {
		if !strings.Contains(runtimeCoverageMetadataReadinessSQL, guard) {
			t.Errorf("missing exact primary-key contract %q", guard)
		}
	}
}

func TestCoverageMetadataReadinessRegistersEveryNewFunction(t *testing.T) {
	feature := coverageMetadataFeature(t)
	functions := regexp.MustCompile(`CREATE FUNCTION app\.([a-z_]+)\(`).FindAllStringSubmatch(feature, -1)
	if len(functions) < 19 {
		t.Fatal("incomplete metadata feature function registry")
	}
	for _, function := range functions {
		if !strings.Contains(runtimeCoverageMetadataReadinessSQL, "app."+function[1]+"(") {
			t.Errorf("new function is not covered by readiness attributes and capability ACL checks: %s", function[1])
		}
	}
	for _, table := range []string{"coverage_route_changes", "coverage_route_selection_proofs", "coverage_applied_match_proofs", "coverage_reconciliation_wakes"} {
		if !strings.Contains(feature, "CREATE TABLE app."+table+" (") || !strings.Contains(runtimeCoverageMetadataReadinessSQL, "('"+table+"',ARRAY[") {
			t.Errorf("private table missing native DDL/readiness: %s", table)
		}
	}
}

func TestCoverageMetadataNativeIdleAndProofContract(t *testing.T) {
	feature := coverageMetadataFeature(t)
	for _, name := range []string{"coverage_selection_metadata", "coverage_route_actionable", "coverage_reconciliation_queue_due", "dirty_coverage_route_inputs"} {
		body := metadataFunction(t, feature, name)
		for _, forbidden := range []string{"FROM app.workout_route_points", "sha256(", "ST_", "account_data_generations"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("metadata/point-hook function %s performs forbidden work %q", name, forbidden)
			}
		}
	}
	observer := metadataFunction(t, feature, "observe_coverage_catalog")
	identical := strings.Index(observer, "THEN RETURN true; END IF;")
	if identical < 0 || identical > strings.Index(observer, "FOR UPDATE") || identical > strings.Index(observer, "UPDATE app.") ||
		identical > strings.Index(observer, "FROM app.accounts") {
		t.Fatal("identical catalog observation must return before locks, DML or account enumeration")
	}
	for _, required := range []string{"revision<prior.catalog_revision", "event_id<prior.last_osm_event_id", "conflicting coverage catalog revision"} {
		if !strings.Contains(observer, required) {
			t.Errorf("observer omitted monotonic acceptance guard %q", required)
		}
	}
	advance := metadataFunction(t, feature, "advance_coverage_reconciliation")
	for _, required := range []string{"WHEN NOT scan_required THEN cursor_workout_id", "WHEN completed AND scan_required",
		"lease_runtime_version>=30", "minimum_runtime_version"} {
		if !strings.Contains(advance, required) {
			t.Errorf("queue-only completion omitted campaign/authority fence %q", required)
		}
	}
	claim := metadataFunction(t, feature, "claim_coverage_reconciliation")
	if strings.Contains(claim, "completed_revision >= candidate.desired_revision") || strings.Contains(claim, "interval '24 hours'") {
		t.Fatal("claim reset a resumable campaign or reintroduced periodic scans")
	}
	wake := metadataFunction(t, feature, "wake_coverage_reconciliation")
	if !strings.Contains(wake, "ON CONFLICT(account_id,transaction_id) DO NOTHING") || strings.Contains(wake, "DO UPDATE") ||
		!strings.Contains(feature, "PRIMARY KEY(account_id,transaction_id)") ||
		!strings.Contains(claim, "DELETE FROM app.coverage_reconciliation_wakes wake WHERE wake.account_id=candidate.account_id") {
		t.Fatal("ingest wakes must be transaction-coalesced, without a shared account mutex or transaction-ID high-water loss")
	}
	capture := metadataFunction(t, feature, "capture_coverage_matching_descriptor")
	if !strings.Contains(capture, "NEW.matcher_contract_version") || strings.Contains(capture, "current_coverage_matcher_contract()") {
		t.Fatal("immutable matching contexts must capture their admitted producer, not a later catalog observation")
	}
	for _, required := range []string{"app.record_coverage_selection_proof(target_account_id,workout_id,item)",
		"app.coverage_verified_route_digest(target_account_id,workout_id)", "mutation_version=app.coverage_route_changes.mutation_version+1",
		"context.result_outcome IN ('applied','no_evidence')", "parent.matching_descriptor", "OLD TABLE AS changed_points"} {
		if !strings.Contains(feature, required) {
			t.Errorf("missing exact input/provenance feature contract %q", required)
		}
	}
}

func TestCoverageMetadataBaselinePatchAnchors(t *testing.T) {
	raw, err := migrations.Files.ReadFile("028_ingest_coverage_pipeline.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseline := string(raw)
	for _, test := range []struct {
		name, anchor string
		count        int
	}{
		{"ingest_coverage_existing_target", "AND state.target_job_id IS NULL\n", 1},
		{"ingest_coverage_existing_target", "AND parent.matcher_contract_version=app.current_coverage_matcher_contract()", 2},
		{"apply_ingest_coverage_batch", "app.ingest_coverage_snapshot_digest(target_account_id,workout_id)", 1},
		{"apply_ingest_coverage_batch", "        RETURN NEXT;", 1},
		{"apply_ingest_coverage_batch", "DECLARE builder app.ingest_coverage_builders%ROWTYPE; root_id uuid; canonical_existing boolean;", 1},
		{"apply_ingest_coverage_batch", "    -- Claimers take Coverage parent/child before state.", 1},
		{"seal_ingest_coverage_cohort", "    PERFORM app.cleanup_ingest_coverage_origins", 1},
		{"seal_ingest_coverage_cohort", "DECLARE new_parent uuid; new_child uuid; old_parent uuid; changed boolean:=false; targets integer;", 1},
		{"seal_ingest_coverage_cohort", "    -- Parent/child before workout/state", 1},
		{"seal_ingest_coverage_cohort", "        SELECT * INTO current_event FROM app.ingest_coverage_candidates", 1},
		{"seal_ingest_coverage_cohort", "app.ingest_coverage_snapshot_digest(target_account_id,prepared.workout_id)", 1},
		{"read_ingest_coverage_prepared", "'matcherContract',builder.matcher_contract", 1},
		{"ingest_coverage_candidates", "AND (candidate.origin = 'adopted' OR EXISTS", 1},
	} {
		body := metadataFunction(t, baseline, test.name)
		if got := strings.Count(body, test.anchor); got != test.count {
			t.Errorf("baseline anchor %s %q occurs %d times, want %d", test.name, test.anchor, got, test.count)
		}
	}
}
