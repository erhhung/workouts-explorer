package worker

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func testCoverageRetryManualCancellation(t *testing.T, newFixture func(*testing.T, int) *coverageAutoRetryFixture) {
	for _, running := range []bool{false, true} {
		mode := "queued"
		if running {
			mode = "running"
		}
		for _, partial := range []string{"root", "successful sibling", "failed sibling"} {
			t.Run("manual cancellation retry/"+mode+"/"+partial, func(t *testing.T) {
				n, index, routes, status := 1, 0, 1, "cancelled"
				if partial != "root" {
					n, index = 2, 1
				}
				f := newFixture(t, n)
				switch partial {
				case "successful sibling":
					f.finish(0, "")
					status = "partially_succeeded"
				case "failed sibling":
					f.finish(0, "coverage-route-timeout")
					routes, status = 2, "failed"
				}
				f.cancelForManualRetry(index, running)
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status=$2 AND cancel_requested_at IS NOT NULL`, f.parent, status)
				f.age()
				f.automatic(20, false)
				if _, err := f.manual(f.parent, uuid.New()); err == nil {
					t.Fatal("cancelled parent accepted a forged manual requester")
				}
				retry, err := f.manual(f.parent, f.principal)
				if err != nil || retry == uuid.Nil {
					t.Fatalf("POST-equivalent explicit cancellation retry refused: %v", err)
				}
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND retry_of_job_id=$2 AND status='queued' AND cancel_requested_at IS NULL`, retry, f.parent)
				assertCountQuery(t, f.admin, routes, `SELECT count(*) FROM app.jobs child JOIN app.jobs prior ON prior.id=child.retry_of_job_id
					WHERE child.parent_job_id=$1 AND prior.parent_job_id=$2 AND prior.status IN ('failed','cancelled') AND child.cancel_requested_at IS NULL`, retry, f.parent)
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs child JOIN app.coverage_route_job_contexts context ON context.job_id=child.id
					WHERE child.parent_job_id=$1 AND child.retry_of_job_id=$2 AND context.timeout_retry_count=0`, retry, f.children[index])
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=0`, f.parent)
				if _, err := f.manual(f.parent, f.principal); !coverageManualRetryConflict(err) {
					t.Fatal("older cancelled attempt bypassed latest-only retry")
				}
			})
		}
	}

	for _, cleared := range []string{"pointer", "all target metadata"} {
		t.Run("manual cancellation retry/proven cleared "+cleared, func(t *testing.T) {
			f := newFixture(t, 1)
			f.cancelForManualRetry(0, false)
			// Real normal apply records current desired generations but must not
			// replace the retained cancelled target.
			if child := f.reconcile("", "", 0, true); child != f.children[0] {
				t.Fatal("normal apply revived cancellation")
			}
			if cleared == "pointer" {
				f.synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL WHERE workout_id=$1`, f.workouts[0])
			} else {
				f.synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL,target_revision=NULL,target_rules_version=NULL,
					target_sampling_version=NULL,target_path_policy_version=NULL,target_generations=NULL WHERE workout_id=$1`, f.workouts[0])
			}
			f.age()
			f.automatic(20, false)
			if child := f.reconcile("", "", 0, true); child != uuid.Nil {
				t.Fatal("normal apply revived a provably cancelled NULL target")
			}
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account)
			retry, err := f.manual(f.parent, f.principal)
			if err != nil || retry == uuid.Nil {
				t.Fatalf("safe explicit recovery of cleared cancellation refused: %v", err)
			}
			f.checkRetry(retry, f.parent, 1, 0)
		})
	}

	for _, exclusion := range []string{"digest", "revision", "current", "deleting workout", "inactive account", "no generation proof", "wrong cancellation finish", "changed desired generation"} {
		t.Run("manual cancellation retry/refuse "+exclusion, func(t *testing.T) {
			f := newFixture(t, 1)
			f.cancelForManualRetry(0, false)
			sql := map[string]string{
				"digest":                     `UPDATE app.workout_coverage_states SET route_input_sha256=decode(repeat('02',32),'hex') WHERE account_id=$1`,
				"revision":                   `UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE account_id=$1`,
				"current":                    `UPDATE app.workout_coverage_states SET processing_state='current' WHERE account_id=$1`,
				"deleting workout":           `UPDATE app.workouts SET deletion_requested_at=clock_timestamp(),deletion_target_id=gen_random_uuid() WHERE account_id=$1`,
				"inactive account":           `UPDATE app.accounts SET state='deleting' WHERE id=$1`,
				"no generation proof":        `UPDATE app.workout_coverage_states SET target_job_id=NULL,target_revision=NULL,target_rules_version=NULL,target_sampling_version=NULL,target_path_policy_version=NULL,target_generations=NULL WHERE account_id=$1`,
				"wrong cancellation finish":  `UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_finished_at=processing_finished_at-interval '1 second' WHERE account_id=$1`,
				"changed desired generation": `UPDATE app.workout_coverage_states SET target_job_id=NULL WHERE account_id=$1`,
			}[exclusion]
			f.synthetic(sql, f.account)
			if exclusion == "changed desired generation" {
				f.synthetic(`INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation) VALUES($1,$2,'geofabrik:test',2)`, f.account, f.workouts[0])
			}
			f.automatic(20, false)
			if retry, err := f.manual(f.parent, f.principal); err != nil || retry != uuid.Nil {
				t.Fatalf("unsafe cancellation recovery accepted: id=%s err=%v", retry, err)
			}
			assertCountQuery(t, f.admin, 0, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent)
		})
	}

	for _, state := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		for _, cleared := range []bool{false, true} {
			name := "retained pointer"
			if cleared {
				name = "NULL pointer"
			}
			t.Run("manual cancellation retry/refuse newer "+state+"/"+name, func(t *testing.T) {
				f := newFixture(t, 1)
				f.cancelForManualRetry(0, false)
				newer := f.enqueueCancellationReplacement()
				switch state {
				case "running":
					newer.claimManualCancellationRoute(0)
				case "succeeded":
					newer.finish(0, "")
				case "failed":
					newer.finish(0, "coverage-route-timeout")
				case "cancelled":
					newer.cancelForManualRetry(0, false)
				}
				if cleared {
					// Even a matching old cancellation footprint must not authorize
					// stealing an independent newer attempt whose pointer was lost.
					f.synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_state='stale',
						processing_finished_at=(SELECT terminal_at FROM app.jobs WHERE id=$2) WHERE workout_id=$1`, f.workouts[0], f.children[0])
				}
				if retry, err := f.manual(f.parent, f.principal); err != nil || retry != uuid.Nil {
					t.Fatalf("older cancelled root stole newer work: %s %v", retry, err)
				}
				assertCountQuery(t, f.admin, 0, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent)
				// Do not leave a due synthetic timeout for global discovery in
				// later cases; the tested old root stays cancelled either way.
				if state == "failed" && !cleared {
					f.synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_state='stale' WHERE workout_id=$1`, f.workouts[0])
				}
			})
		}
	}

	t.Run("manual cancellation retry/latest independent cancelled root remains eligible", func(t *testing.T) {
		f := newFixture(t, 1)
		f.cancelForManualRetry(0, false)
		newer := f.enqueueCancellationReplacement()
		newer.cancelForManualRetry(0, false)
		newer.synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL WHERE workout_id=$1`, newer.workouts[0])
		if retry, err := f.manual(f.parent, f.principal); err != nil || retry != uuid.Nil {
			t.Fatal("older independent cancelled root regained authority")
		}
		retry, err := newer.manual(newer.parent, newer.principal)
		if err != nil || retry == uuid.Nil {
			t.Fatalf("latest cancelled root could not recover its cleared pointer: %v", err)
		}
		newer.checkRetry(retry, newer.parent, 1, 0)
	})
	t.Run("manual cancellation retry/refuse ambiguous older creation with later success", func(t *testing.T) {
		f := newFixture(t, 1)
		f.cancelForManualRetry(0, false)
		newer := f.enqueueCancellationReplacement()
		newer.finish(0, "")
		// A long-lived transaction can have an older creation timestamp despite
		// committing later. Completion history must also veto NULL recovery.
		f.synthetic(`UPDATE app.coverage_route_job_contexts SET created_at=(SELECT created_at-interval '1 second' FROM app.coverage_route_job_contexts WHERE job_id=$2) WHERE job_id=$1`, newer.children[0], f.children[0])
		f.synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_state='stale',processing_finished_at=(SELECT terminal_at FROM app.jobs WHERE id=$2) WHERE workout_id=$1`, f.workouts[0], f.children[0])
		if retry, err := f.manual(f.parent, f.principal); err != nil || retry != uuid.Nil {
			t.Fatal("ambiguous later success was stolen using an older context creation timestamp")
		}
	})
}

func coverageManualRetryConflict(err error) bool {
	var sqlErr *pgconn.PgError
	return errors.As(err, &sqlErr) && sqlErr.Code == "55000"
}

func (f *coverageAutoRetryFixture) claimManualCancellationRoute(index int) uuid.UUID {
	f.t.Helper()
	lease := uuid.New()
	tx := beginWorkerTestAccount(f.t, f.admin, f.account)
	defer tx.Rollback(f.ctx)
	var ok bool
	if err := tx.QueryRow(f.ctx, `SELECT app.claim_job($1,'synthetic-manual-cancel',$2,interval '7 minutes')`, f.children[index], lease).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("claim cancellation route: %t %v", ok, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return lease
}

func (f *coverageAutoRetryFixture) cancelForManualRetry(index int, running bool) {
	f.t.Helper()
	var lease uuid.UUID
	if running {
		lease = f.claimManualCancellationRoute(index)
	}
	tx := beginWorkerTestAccount(f.t, f.api, f.account)
	defer tx.Rollback(f.ctx)
	var ok bool
	if err := tx.QueryRow(f.ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, f.parent, f.principal).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("request real owner cancellation: %t %v", ok, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	if !running {
		return
	}
	if _, err := f.manual(f.parent, f.principal); !coverageManualRetryConflict(err) {
		f.t.Fatal("manual retry accepted a still-running cancellation")
	}
	tx = beginWorkerTestAccount(f.t, f.worker, f.account)
	defer tx.Rollback(f.ctx)
	if err := tx.QueryRow(f.ctx, `SELECT app.finish_job($1,'synthetic-manual-cancel',$2,'cancelled')`, f.children[index], lease).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("finish real running cancellation: %t %v", ok, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *coverageAutoRetryFixture) enqueueCancellationReplacement() *coverageAutoRetryFixture {
	f.t.Helper()
	tx := beginWorkerTestAccount(f.t, f.admin, f.account)
	defer tx.Rollback(f.ctx)
	parent, child := uuid.New(), uuid.New()
	var actual uuid.UUID
	var routes int
	var reused bool
	if err := tx.QueryRow(f.ctx, `SELECT * FROM app.enqueue_adopted_coverage_update($1,$2,'geofabrik:test',1,2,
		'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,
		jsonb_build_array(jsonb_build_object('jobId',$3::uuid,'workoutId',$4::uuid,'routeRevision',1,
		'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb)))`, f.account, parent, child, f.workouts[0]).Scan(&actual, &routes, &reused); err != nil {
		f.t.Fatal(err)
	}
	if actual != parent || routes != 1 || reused {
		f.t.Fatal("independent replacement did not create a fresh target")
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	next := *f
	next.parent, next.children = parent, []uuid.UUID{child}
	return &next
}
