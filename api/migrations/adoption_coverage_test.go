package migrations_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestAdoptionCoverageMigrationBoundaries(t *testing.T) {
	data, err := migrations.Files.ReadFile("026_adopted_workout_coverage.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	request := strings.Split(strings.Split(source, "CREATE FUNCTION app.record_adopted_workout_coverage_refresh")[1], "$function$;")[0]
	for _, forbidden := range []string{"enqueue_coverage", "initialize_workout_coverage", "FOR UPDATE", "coverage_reconciliation_accounts", "workout_coverage_states", "job.lease_expires_at"} {
		if strings.Contains(request, forbidden) {
			t.Fatalf("file-transaction request takes Coverage authority: %s", forbidden)
		}
	}
	authority := strings.Split(strings.Split(source, "CREATE FUNCTION app.adopted_coverage_authority_current")[1], "$function$;")[0]
	for _, required := range []string{"job.lease_expires_at >= clock_timestamp()", "reconciliation.lease_expires_at >= clock_timestamp()"} {
		if !strings.Contains(authority, required) {
			t.Fatalf("post-commit authority lacks a fresh lease check: %s", required)
		}
	}
	for _, required := range []string{"capability.runtime_version >= 26", "app.valid_ingest_ownership_epoch(capability.account_id, capability.ownership_epoch, capability.runtime_version)", "capability.transaction_id = txid_current()", "file.source_id = workout.source_id", "file.job_id = job.id"} {
		if !strings.Contains(request, required) {
			t.Fatalf("request lacks freshness/ownership check %s", required)
		}
	}
	dueReader := strings.Split(strings.Split(source, "CREATE FUNCTION app.read_adopted_workout_coverage_refreshes")[1], "$function$;")[0]
	allPendingReader := strings.Split(strings.Split(source, "CREATE FUNCTION app.read_pending_adopted_workout_coverage_refreshes")[1], "$function$;")[0]
	if !strings.Contains(dueReader, "request.next_attempt_at <= clock_timestamp()") || strings.Contains(allPendingReader, "next_attempt_at") {
		t.Fatal("due replay and all-pending repair exclusions must have separate eligibility rules")
	}
	table := strings.Split(strings.Split(source, "CREATE TABLE app.adopted_workout_coverage_refreshes")[1], ");")[0]
	if strings.Contains(table, "REFERENCES app.accounts") || !strings.Contains(table, "REFERENCES app.workouts(id, account_id) ON DELETE CASCADE") {
		t.Fatal("outbox must have only the workout/account FK, not an account-header FK")
	}
	for _, required := range []string{"FORCE ROW LEVEL SECURITY", "FOR UPDATE NOWAIT", "AND event_id = target_event_id", "next_attempt_at = clock_timestamp() + interval '1 hour'", "runtime_version IS NULL OR runtime_version < 26", "schema_version = 26, minimum_runtime_version = 26"} {
		if !strings.Contains(source, required) {
			t.Fatalf("missing adoption safety boundary %s", required)
		}
	}
}

func TestAdoptionCoverageDynamicAnchorGuards(t *testing.T) {
	data, err := migrations.Files.ReadFile("026_adopted_workout_coverage.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	anchors := regexp.MustCompile(`(?s)anchor := \$pattern\$(.*?)\$pattern\$;`).FindAllStringSubmatchIndex(source, -1)
	if len(anchors) != 8 {
		t.Fatalf("guarded dynamic anchors=%d, want 8", len(anchors))
	}
	for i, anchor := range anchors {
		pattern := source[anchor[2]:anchor[3]]
		if !strings.HasPrefix(pattern, "(?i)") || !strings.Contains(pattern, "[[:space:]]") || strings.Contains(pattern, ".*") {
			t.Fatalf("anchor %d is not bounded and case/whitespace tolerant: %s", i, pattern)
		}
		end := len(source)
		if i+1 < len(anchors) {
			end = anchors[i+1][0]
		}
		guard := source[anchor[1]:end]
		if !strings.Contains(guard, "SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');") ||
			!strings.Contains(guard, "IF anchor_count <> 1 THEN RAISE EXCEPTION") {
			t.Fatalf("anchor %d does not reject both absent and ambiguous matches", i)
		}
	}
	if strings.Contains(source, ":= replace(definition") || strings.Contains(source, "position(anchor IN definition)") {
		t.Fatal("dynamic rewrite still uses a whitespace-sensitive literal anchor")
	}
}
