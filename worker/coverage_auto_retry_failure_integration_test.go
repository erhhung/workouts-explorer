package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func testCoverageAutoRetryFailurePolicy(t *testing.T, newFixture func(*testing.T, int) *coverageAutoRetryFixture) {
	for _, code := range []string{"coverage-osm-unavailable", "coverage-persist-failed", "coverage-route-read-failed", "coverage-route-invalid", "coverage-route-too-large", "unknown-private-failure-canary", "NULL"} {
		t.Run("failure policy manual only/"+code, func(t *testing.T) {
			f := newFixture(t, 1)
			initial := code
			if code == "NULL" {
				initial = "coverage-osm-unavailable"
			}
			f.finish(0, initial)
			if code == "NULL" {
				f.synthetic(`UPDATE app.jobs SET failure_code=NULL,failure_summary=NULL WHERE id=$1`, f.children[0])
			}
			f.age()
			f.automatic(20, false)
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=0`, f.parent)
			retry, err := f.manual(f.parent, f.principal)
			if err != nil || retry == uuid.Nil {
				t.Fatalf("manual eligibility lost for %s", code)
			}
			f.checkRetry(retry, f.parent, 1, 0)
		})
	}
	for _, field := range []string{"graph node", "graph edge", "graph layer", "private-field-canary postgresql://private-host-canary"} {
		t.Run("failure policy typed graph limit manual only/"+field, func(t *testing.T) {
			f := newFixture(t, 1)
			cause := errors.Join(context.DeadlineExceeded, io.EOF, &coverage.OSMEvaluationLimitError{Field: field, Limit: 2048})
			failure := f.finishClassified(coverageStageEvaluate, cause, true)
			if failure.code != "coverage-route-too-large" || failure.sqlState != "" {
				t.Fatal("plain Go graph limit became a timeout or invented a SQLSTATE")
			}
			f.age()
			f.automatic(20, false)
			if child := f.reconcile("", "", 0, true); child != f.children[0] {
				t.Fatal("normal reconciliation bypassed typed-limit manual-only policy")
			}
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=0`, f.parent)
			if retry, err := f.manual(f.parent, f.principal); err != nil || retry == uuid.Nil {
				t.Fatal("typed graph limit lost explicit manual retry")
			}
		})
	}
	t.Run("failure policy typed graph limit is carried with transient sibling", func(t *testing.T) {
		f := newFixture(t, 2)
		failure := f.finishClassified(coverageStageEvaluate, &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}, true)
		if failure.code != "coverage-route-too-large" {
			t.Fatal("graph node limit classification failed")
		}
		f.finish(1, "coverage-transient-failed")
		f.age()
		retry := f.automatic(1, true)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, f.parent)
		f.checkRetry(retry, f.parent, 2, 0)
	})
	for _, permanent := range []string{"coverage-osm-unavailable", "coverage-route-too-large", "coverage-persist-failed", "unknown-private-failure-canary"} {
		t.Run("failure policy mixed parent carries both failed siblings/"+permanent, func(t *testing.T) {
			f := newFixture(t, 2)
			f.finish(0, "coverage-transient-failed")
			f.finish(1, permanent)
			f.age()
			retry := f.automatic(1, true)
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, f.parent)
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent)
			f.checkRetry(retry, f.parent, 2, 0)
		})
	}
	t.Run("failure policy transient sibling succeeds and timeout counter resets", func(t *testing.T) {
		f := newFixture(t, 2)
		f.finish(0, "coverage-transient-failed")
		f.finish(1, "")
		f.age()
		retry := f.automatic(1, true)
		f.checkRetry(retry, f.parent, 1, 0)
		f.adopt(retry)
		f.finish(0, "")
	})
	t.Run("failure policy all failed children allowlisted with timeout only backoff", func(t *testing.T) {
		f := newFixture(t, 2)
		f.finish(0, "coverage-route-timeout")
		f.finish(1, "coverage-transient-failed")
		f.age()
		retry := f.automatic(1, true)
		assertCountQuery(t, f.admin, 2, `SELECT count(*) FROM app.jobs WHERE parent_job_id=$1`, retry)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs child JOIN app.jobs prior ON prior.id=child.retry_of_job_id
			JOIN app.coverage_route_job_contexts context ON context.job_id=child.id
			WHERE child.parent_job_id=$1 AND prior.failure_code='coverage-route-timeout' AND context.timeout_retry_count=1`, retry)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs child JOIN app.jobs prior ON prior.id=child.retry_of_job_id
			JOIN app.coverage_route_job_contexts context ON context.job_id=child.id
			WHERE child.parent_job_id=$1 AND prior.failure_code='coverage-transient-failed' AND context.timeout_retry_count=0`, retry)
		f.adopt(retry)
		for index := range f.children {
			f.finish(index, "")
		}
	})
	t.Run("failure policy transient after timeout resets only timeout counter", func(t *testing.T) {
		f := newFixture(t, 1)
		root := f.parent
		f.finish(0, "coverage-route-timeout")
		f.age()
		first := f.automatic(1, true)
		f.checkRetry(first, f.parent, 1, 1)
		f.adopt(first)
		f.finish(0, "coverage-transient-failed")
		f.age()
		second := f.automatic(2, true)
		f.checkRetry(second, first, 1, 0)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=2`, root)
		f.adopt(second)
		f.finish(0, "")
	})
	t.Run("failure policy over thirty two excluded parents cannot starve eligible work", func(t *testing.T) {
		for range 35 {
			f := newFixture(t, 1)
			f.finish(0, "coverage-osm-unavailable")
			f.age()
		}
		f := newFixture(t, 1)
		f.finish(0, "coverage-transient-failed")
		f.age()
		retry := f.automatic(1, true)
		f.checkRetry(retry, f.parent, 1, 0)
		f.adopt(retry)
		f.finish(0, "")
	})
	t.Run("failure policy private constructor rechecks changed gate and metadata", func(t *testing.T) {
		f := newFixture(t, 2)
		f.finish(0, "coverage-route-timeout")
		f.finish(1, "coverage-transient-failed")
		f.age()
		var eligible bool
		if err := f.admin.QueryRow(f.ctx, `SELECT app.coverage_auto_retry_parent_eligible($1,$2)`, f.parent, f.account).Scan(&eligible); err != nil || !eligible {
			t.Fatalf("expected an initially eligible parent: %t", eligible)
		}
		// Removing only one trigger is no longer enough: remove all triggers
		// between discovery and construction to prove the gate is rechecked.
		f.synthetic(`UPDATE app.jobs SET failure_code='coverage-persist-failed' WHERE id=ANY($1::uuid[])`, f.children)
		tx := beginWorkerTestAccount(t, f.admin, f.account)
		defer tx.Rollback(f.ctx)
		var id uuid.UUID
		var routes int
		err := tx.QueryRow(f.ctx, `SELECT * FROM app.construct_coverage_retry($1,$2,100,1)`, f.parent, f.account).Scan(&id, &routes)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("private constructor trusted an earlier eligibility result")
		}
		if err := tx.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=0`, f.parent)
		f.synthetic(`UPDATE app.jobs SET failure_code='coverage-transient-failed' WHERE id=$1`, f.children[1])
		f.synthetic(`DELETE FROM app.coverage_route_job_contexts WHERE job_id=$1`, f.children[1])
		if err := f.admin.QueryRow(f.ctx, `SELECT app.coverage_auto_retry_parent_eligible($1,$2)`, f.parent, f.account).Scan(&eligible); err != nil || eligible {
			t.Fatal("missing direct-child metadata passed automatic gate")
		}
	})

	for _, code := range []string{"40001", "08006", "42501", "28P01", "42P01", "22023", "53100", "53200", "XX000", "57014"} {
		t.Run("failure policy actual SQLSTATE precedence and log redaction/"+code, func(t *testing.T) {
			f := newFixture(t, 1)
			_, cause := f.worker.Exec(f.ctx, `DO $block$ BEGIN RAISE EXCEPTION USING ERRCODE='`+code+`', MESSAGE='private-password-canary postgresql://private-host-canary/source GPS-canary', DETAIL='private-query-canary'; END; $block$;`)
			if cause == nil {
				t.Fatal("synthetic database SQLSTATE was not raised")
			}
			transient := code == "40001" || code == "08006"
			if !transient && code != "57014" {
				cause = errors.Join(&net.OpError{Op: "read", Err: syscall.ECONNRESET}, io.EOF, cause)
			}
			failure := f.finishClassified(coverageStageEvaluate, coverageDatabaseFailure(cause), !transient && code != "57014")
			want := "coverage-osm-unavailable"
			if transient {
				want = "coverage-transient-failed"
			}
			if failure.code != want || failure.sqlState != code {
				t.Fatalf("actual SQLSTATE=%s classified as %s/%s", code, failure.code, failure.sqlState)
			}
			f.age()
			if transient {
				retry := f.automatic(1, true)
				f.checkRetry(retry, f.parent, 1, 0)
			} else {
				f.automatic(20, false)
				if retry, err := f.manual(f.parent, f.principal); err != nil || retry == uuid.Nil {
					t.Fatal("actual permanent/unexplained SQL failure lost manual retry")
				}
			}
		})
	}
	t.Run("failure policy actual disposable database disconnect", func(t *testing.T) {
		f := newFixture(t, 1)
		conn, err := f.worker.Acquire(f.ctx)
		if err != nil {
			t.Fatal("acquire disposable connection")
		}
		defer conn.Release()
		var terminated bool
		if err := f.admin.QueryRow(f.ctx, `SELECT pg_terminate_backend($1)`, conn.Conn().PgConn().PID()).Scan(&terminated); err != nil || !terminated {
			t.Fatal("terminate disposable test connection")
		}
		_, cause := conn.Exec(f.ctx, `SELECT 1`)
		if cause == nil {
			t.Fatal("terminated disposable connection unexpectedly usable")
		}
		failure := f.finishClassified(coverageStagePersist, coverageDatabaseFailure(cause), false)
		if failure.code != "coverage-transient-failed" {
			t.Fatalf("actual disconnect classified as %s", failure.code)
		}
		f.age()
		retry := f.automatic(1, true)
		f.checkRetry(retry, f.parent, 1, 0)
	})
}

func (f *coverageAutoRetryFixture) finishClassified(stage coverageFailureStage, cause error, expired bool) coverageFailure {
	f.t.Helper()
	var logs bytes.Buffer
	runner := NewCoverageRunner(f.worker, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	lease := uuid.New()
	tx := beginWorkerTestAccount(f.t, f.admin, f.account)
	defer tx.Rollback(f.ctx)
	var claimed bool
	if err := tx.QueryRow(f.ctx, `SELECT app.claim_job($1,$2,$3,interval '7 minutes')`, f.children[0], runner.workerID, lease).Scan(&claimed); err != nil || !claimed {
		f.t.Fatal("claim for classified synthetic failure")
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	ctx := f.ctx
	if expired {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer cancel()
	}
	failure := classifyCoverageFailure(ctx, stage, cause)
	if err := runner.failClassifiedDetached(coverageClaim{jobID: f.children[0], accountID: f.account, leaseID: lease}, time.Now(), stage, failure); err != nil {
		f.t.Fatal("persist classified synthetic failure")
	}
	if strings.Contains(logs.String(), "canary") || strings.Contains(logs.String(), "postgresql://") || strings.Contains(logs.String(), `"error"`) ||
		!strings.Contains(logs.String(), `"stage":"`+stage.String()+`"`) || !strings.Contains(logs.String(), `"sqlstate":"`+failure.sqlState+`"`) {
		f.t.Fatal("classified worker log leaked details or lost bounded diagnostics")
	}
	if failure.limitField != "" && (!strings.Contains(logs.String(), `"limit_field":"`+failure.limitField+`"`) ||
		!strings.Contains(logs.String(), `"limit":`+strconv.Itoa(failure.limitValue))) {
		f.t.Fatal("classified worker log lost safe typed-limit diagnostics")
	}
	assertCountQuery(f.t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='failed' AND failure_code=$2 AND failure_summary=$3`, f.children[0], failure.code, failure.summary)
	return failure
}
