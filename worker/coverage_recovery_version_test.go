package worker

import (
	"testing"

	"github.com/erhhung/workouts-explorer/internal/coverage"
)

func TestRecoveryMatcherVersionAndLegacyDescriptorRemainDistinct(t *testing.T) {
	current := productionMatchingDescriptor(5)
	legacy := productionMatchingDescriptorForPolicy(5, string(coverage.ExperimentalPathPolicyV82))
	if current.PathPolicy != "coverage-path-policy-experimental-v84" || legacy.PathPolicy != "coverage-path-policy-experimental-v82" || current.Contract != legacy.Contract {
		t.Fatalf("recovery rollout lost explicit policy identity: current=%+v legacy=%+v", current, legacy)
	}
}

func TestCoverageRunnerLegacyAndRecoveryPolicyIntegration(t *testing.T) {
	for _, policy := range []coverage.PathPolicyVersion{coverage.ExperimentalPathPolicyV82, coverage.ExperimentalPathPolicyV83, coverage.CurrentPathPolicyVersion} {
		t.Run(string(policy), func(t *testing.T) {
			fixture := newCoverageIntegrationFixture(t)
			fixture.seedOSM()
			workout := fixture.addWorkout(coverageIntegrationRoute(0))
			job := fixture.queuePolicy(workout, policy)
			runner := fixture.runner()
			if worked, err := runner.RunOnce(fixture.ctx); err != nil || !worked {
				t.Fatalf("policy %s was not consumed: worked=%t error=%v", policy, worked, err)
			}
			tx := fixture.accountTx(fixture.ctx, fixture.adminDB)
			defer coverageIntegrationRollback(tx)
			var state, applied, failure string
			fixture.scan(tx, `SELECT job.status,COALESCE(coverage.applied_path_policy_version,''),COALESCE(job.failure_code,'')
                FROM app.jobs job JOIN app.coverage_route_job_contexts context ON context.job_id=job.id
                JOIN app.workout_coverage_states coverage ON coverage.account_id=context.account_id AND coverage.workout_id=context.workout_id
				WHERE job.id=$1 AND job.account_id=$2`, []any{job.childID, fixture.accountID}, &state, &applied, &failure)
			if state != "succeeded" || applied != string(policy) {
				t.Fatalf("policy provenance changed: state=%s applied=%s want=%s failure=%s", state, applied, policy, failure)
			}
			var matches, classified int
			fixture.scan(tx, `SELECT count(*),count(display_class) FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=$2`, []any{fixture.accountID, workout}, &matches, &classified)
			if matches == 0 || (policy == coverage.ExperimentalPathPolicyV84 && classified != matches) || (policy != coverage.ExperimentalPathPolicyV84 && classified != 0) {
				t.Fatalf("policy %s has invalid display provenance: matches=%d classified=%d", policy, matches, classified)
			}
			fixture.commit(tx)
		})
	}
}
