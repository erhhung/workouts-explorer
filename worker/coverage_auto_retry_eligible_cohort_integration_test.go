package worker

import (
	"testing"

	"github.com/google/uuid"
)

// These assertions apply to the policy introduced in 028, not the frozen 027
// migration checkpoints. Failure-code eligibility is existential; target
// authority remains universal so automatic retry never strands a failed sibling.
func testCoverageAutoRetryEligibleCohort(t *testing.T, newFixture func(*testing.T, int) *coverageAutoRetryFixture) {
	for _, trigger := range []string{"coverage-route-timeout", "coverage-transient-failed"} {
		for _, permanent := range []string{"coverage-route-too-large", "coverage-route-invalid", "unknown-private-failure-canary", "NULL"} {
			t.Run("eligible cohort carries all failures/"+trigger+"/"+permanent, func(t *testing.T) {
				f := newFixture(t, 3)
				root, succeeded := f.parent, f.children[2]
				f.synthetic(`UPDATE app.coverage_job_contexts SET parent_batch_size=3 WHERE job_id=$1`, root)
				f.finish(0, trigger)
				f.finish(1, "coverage-route-too-large")
				if permanent == "NULL" {
					f.synthetic(`UPDATE app.jobs SET failure_code=NULL,failure_summary=NULL WHERE id=$1`, f.children[1])
				} else {
					f.synthetic(`UPDATE app.jobs SET failure_code=$2 WHERE id=$1`, f.children[1], permanent)
				}
				f.finish(2, "")
				f.age()
				f.automatic(0, false)
				first := f.automatic(1, true)
				assertCountQuery(t, f.admin, 2, `SELECT count(*) FROM app.jobs WHERE parent_job_id=$1`, first)
				assertCountQuery(t, f.admin, 0, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1`, succeeded)
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_job_contexts WHERE job_id=$1 AND parent_batch_size=3`, first)
				assertCountQuery(t, f.admin, 2, `SELECT count(*) FROM app.jobs child
					JOIN app.jobs prior ON prior.id=child.retry_of_job_id
					JOIN app.coverage_route_job_contexts target ON target.job_id=child.id
					JOIN app.coverage_route_job_contexts original ON original.job_id=prior.id
					WHERE child.parent_job_id=$1 AND prior.parent_job_id=$2 AND prior.status='failed'
					AND target.workout_id=original.workout_id AND target.route_input_revision=original.route_input_revision
					AND target.route_input_sha256=original.route_input_sha256 AND target.target_generations=original.target_generations
					AND target.timeout_retry_count=CASE WHEN prior.failure_code='coverage-route-timeout' THEN 1 ELSE 0 END`, first, root)
				// Duplicate polling and explicit retry of an ancestor cannot fork.
				f.automatic(20, false)
				if _, err := f.manual(root, f.principal); err == nil {
					t.Fatal("ancestor remained manually retryable after automatic successor")
				}
				f.adopt(first)
				// Preserve the same mixture for another generation. Budget counts
				// parents, not routes, and no successful sibling is filled back in.
				f.finish(0, "coverage-transient-failed")
				f.finish(1, "coverage-route-too-large")
				f.age()
				f.automatic(1, false)
				second := f.automatic(2, true)
				f.checkRetry(second, first, 2, 0)
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=2`, root)
				f.adopt(second)
				f.finish(0, "coverage-transient-failed")
				f.finish(1, "coverage-route-too-large")
				f.age()
				f.automatic(2, false)
				manual, err := f.manual(second, f.principal)
				if err != nil || manual == uuid.Nil {
					t.Fatalf("latest permanent sibling lost manual continuation: %v", err)
				}
				f.checkRetry(manual, second, 2, 0)
				f.adopt(manual)
				f.finish(0, "coverage-transient-failed")
				f.finish(1, "coverage-route-too-large")
				f.age()
				f.automatic(2, false) // Manual continuation does not fund a fresh budget.
				f.adopt(f.automatic(3, true))
				for index := range f.children {
					f.finish(index, "")
				}
				assertCountQuery(t, f.admin, 5, `SELECT count(*) FROM app.coverage_job_contexts WHERE account_id=$1 AND parent_batch_size=3`, f.account)
				assertCountQuery(t, f.admin, 4, `SELECT count(*) FROM app.jobs parent WHERE parent.account_id=$1
					AND parent.kind='coverage_update' AND parent.retry_of_job_id IS NOT NULL
					AND (SELECT count(*) FROM app.jobs child WHERE child.parent_job_id=parent.id)=2`, f.account)
			})
		}
	}
	t.Run("eligible cohort stops when transient heals but permanent remains", func(t *testing.T) {
		f := newFixture(t, 2)
		root, transient := f.parent, f.children[0]
		f.finish(0, "coverage-transient-failed")
		f.finish(1, "coverage-route-too-large")
		f.age()
		f.adopt(f.automatic(20, true))
		for index, child := range f.children {
			var prior uuid.UUID
			if err := f.admin.QueryRow(f.ctx, `SELECT retry_of_job_id FROM app.jobs WHERE id=$1`, child).Scan(&prior); err != nil {
				t.Fatal(err)
			}
			code := "coverage-route-too-large"
			if prior == transient {
				code = ""
			}
			f.finish(index, code)
		}
		f.age()
		f.automatic(20, false)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, root)
		retry, err := f.manual(f.parent, f.principal)
		if err != nil || retry == uuid.Nil {
			t.Fatalf("permanent-only latest descendant must remain manually reachable: %v", err)
		}
		f.checkRetry(retry, f.parent, 1, 0)
		f.adopt(retry)
		f.finish(0, "")
	})
	for _, change := range []string{"current", "superseded", "changed input", "missing context", "cancelled"} {
		t.Run("eligible cohort requires every failed target authority/"+change, func(t *testing.T) {
			f := newFixture(t, 2)
			f.finish(0, "coverage-transient-failed")
			f.finish(1, "coverage-route-too-large")
			f.age()
			query := map[string]string{
				"current":         `UPDATE app.workout_coverage_states SET processing_state='current' WHERE workout_id=$1`,
				"superseded":      `UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_state='stale' WHERE workout_id=$1`,
				"changed input":   `UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE workout_id=$1`,
				"missing context": `DELETE FROM app.coverage_route_job_contexts WHERE workout_id=$1`,
				"cancelled":       `UPDATE app.jobs SET status='cancelled' WHERE id=(SELECT job_id FROM app.coverage_route_job_contexts WHERE workout_id=$1)`,
			}[change]
			f.synthetic(query, f.workouts[1])
			f.automatic(20, false)
			assertCountQuery(t, f.admin, 0, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent)
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=0`, f.parent)
		})
	}
}
