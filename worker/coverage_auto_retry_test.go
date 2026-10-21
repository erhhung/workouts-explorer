package worker

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
)

func TestCoverageAutoRetryDisabledDoesNotAccessDatabase(t *testing.T) {
	zero := 0
	runner := NewCoverageRunnerWithOptions(nil, nil, slog.Default(), CoverageRunnerOptions{AutoRetryCount: &zero})
	if worked, err := runner.retryCoverageOnce(context.Background()); worked || err != nil {
		t.Fatalf("disabled retry: worked=%t err=%v", worked, err)
	}
}

func TestCoverageAutoRetryLoopStopsWithRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The default-enabled control loop must observe shutdown before using a pool,
	// and Run must join it before its caller closes that pool.
	if err := NewCoverageRunner(nil, nil, slog.Default()).Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageAutoRetryMigrationContract(t *testing.T) {
	raw, err := migrationfixture.PhaseSQL(migrations.Files, migrationfixture.AutoRetries)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, contract := range []string{
		"CREATE TABLE app.coverage_retry_roots", "CREATE TABLE app.coverage_retry_attempts",
		"UNIQUE (root_job_id, automatic_ordinal)", "CREATE UNIQUE INDEX coverage_retry_attempts_predecessor_idx",
		"AFTER UPDATE OF status ON app.jobs", "OLD.status IS DISTINCT FROM NEW.status",
		"CREATE OR REPLACE FUNCTION app.retry_coverage_update(prior_job_id uuid, requester_id uuid, max_ordinal integer)",
		"principal.disabled_at IS NULL", "state.target_job_id = child.id", "state.processing_state = 'failed'",
		"cancel_requested_at IS NOT NULL", "workout.deletion_requested_at IS NULL", "account.state = 'active'",
		"interval '30 seconds'", "LIMIT 32", "FOR UPDATE NOWAIT", "WHEN lock_not_available",
		"job.retry_of_job_id IS NULL",
		"GRANT EXECUTE ON FUNCTION app.retry_next_coverage_update(integer,integer) TO workouts_coverage_worker",
		"ALTER TABLE app.coverage_job_contexts ADD COLUMN matcher_contract_version",
		"coverage reconciliation retry-policy anchor must occur exactly once",
		"coverage reconciliation matcher anchor must occur exactly once",
		"IF current_state.readiness_state = ''map_data_ready'' AND EXISTS",
		"child.status IN (''failed'',''cancelled'')", "parent.cancel_requested_at IS NOT NULL",
		"parent_context.matcher_contract_version = app.current_coverage_matcher_contract()",
		"CREATE FUNCTION app.coverage_auto_retry_parent_eligible(prior_job_id uuid, target_account_id uuid)",
		"failure_code NOT IN ('coverage-route-timeout','coverage-transient-failed')",
		"IF automatic_limit IS NOT NULL AND NOT app.coverage_auto_retry_parent_eligible(prior_job_id,target_account_id) THEN RETURN; END IF;",
		"IF automatic_limit IS NOT NULL AND (prior_parent.cancel_requested_at IS NOT NULL OR prior_parent.status = 'cancelled') THEN RETURN; END IF;",
		"child.status IN ('failed','cancelled')", "state.processing_finished_at = child.terminal_at",
		"other_context.created_at >= context.created_at",
	} {
		if !strings.Contains(sql, contract) {
			t.Errorf("migration missing contract %q", contract)
		}
	}
	if strings.Contains(sql, "set_config('app.principal") {
		t.Fatal("automatic retry must not impersonate a principal")
	}
	gate := strings.Index(sql, "AND app.coverage_auto_retry_parent_eligible(job.id,job.account_id)")
	limit := strings.Index(sql, "ORDER BY job.terminal_at,job.id LIMIT 32")
	if gate < 0 || gate > limit {
		t.Fatal("automatic policy gate must filter candidates before LIMIT 32")
	}
	constructor := sql[strings.Index(sql, "CREATE FUNCTION app.construct_coverage_retry("):]
	lockedStates := strings.Index(constructor, "ORDER BY state.workout_id FOR UPDATE OF state NOWAIT")
	policy := strings.Index(constructor, "IF automatic_limit IS NOT NULL AND NOT app.coverage_auto_retry_parent_eligible(")
	firstInsert := strings.Index(constructor, "INSERT INTO app.coverage_retry_roots")
	if lockedStates < 0 || policy < lockedStates || policy > firstInsert {
		t.Fatal("constructor must recheck automatic policy after locks and before budget/job writes")
	}
	start := strings.Index(sql, "CREATE FUNCTION app.coverage_retry_targets(")
	end := strings.Index(sql[start:], "$function$;") + start
	if strings.Contains(sql[start:end], "coverage-transient-failed") {
		t.Fatal("manual retry targets must not inherit the automatic failure allowlist")
	}
}
