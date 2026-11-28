package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func enqueueLockFixture(ctx context.Context, f *coverageIntegrationFixture, tx pgx.Tx, workout uuid.UUID, minimum float64) (uuid.UUID, error) {
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT route_input_revision FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout).Scan(&revision); err != nil {
		return uuid.Nil, err
	}
	targets, err := json.Marshal([]map[string]any{{"jobId": uuid.New(), "workoutId": workout, "routeRevision": revision, "generations": []coverageTargetGeneration{{f.regionID, f.generation}}}})
	if err != nil {
		return uuid.Nil, err
	}
	var parent uuid.UUID
	var count int
	var reused bool
	err = tx.QueryRow(ctx, `SELECT job_id,route_count,reused FROM app.enqueue_coverage_update($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, f.accountID, uuid.New(), f.regionID, f.generation, revision, string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1), string(coverage.CurrentPathPolicyVersion), minimum, targets).Scan(&parent, &count, &reused)
	return parent, err
}

func TestCoverageAdmissionUsesDedicatedMutexIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	first := f.accountTx(f.ctx, f.workerDB)
	parent, err := enqueueLockFixture(f.ctx, f, first, workout, 5)
	if err != nil {
		t.Fatal(err)
	}
	// An actual admission must coexist with account FK checks, but a second
	// admission still serializes before it can create a duplicate target.
	other := f.accountTx(f.ctx, f.workerDB)
	reference := f.accountTx(f.ctx, f.adminDB)
	if _, err := reference.Exec(f.ctx, `SELECT 1 FROM app.accounts WHERE id=$1 FOR KEY SHARE NOWAIT`, f.accountID); err != nil {
		t.Fatal("admission blocked an unrelated account reference", err)
	}
	_ = reference.Rollback(f.ctx)
	_, err = enqueueLockFixture(f.ctx, f, other, workout, 5)
	var busy *pgconn.PgError
	if !errors.As(err, &busy) || busy.Code != "55P03" {
		t.Fatal("concurrent admission did not use its private mutex", err)
	}
	_ = other.Rollback(f.ctx)
	f.commit(first)
	retry := f.accountTx(f.ctx, f.workerDB)
	actual, err := enqueueLockFixture(f.ctx, f, retry, workout, 5)
	if err != nil || actual != parent {
		t.Fatal("admission retry did not reuse the existing target", actual, parent, err)
	}
	f.commit(retry)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_job_contexts WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout)
}

func TestCoverageAdmissionSkipsUnrelatedJobLocksIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	first := f.addWorkout(coverageIntegrationRoute(0))
	second := f.addWorkout(coverageIntegrationRoute(1))
	setup := f.accountTx(f.ctx, f.workerDB)
	otherParent, err := enqueueLockFixture(f.ctx, f, setup, second, 7)
	if err != nil {
		t.Fatal(err)
	}
	f.commit(setup)
	locked := f.accountTx(f.ctx, f.adminDB)
	f.exec(locked, `SELECT 1 FROM app.jobs WHERE id=$1 OR parent_job_id=$1 FOR UPDATE`, otherParent)
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	tx := f.accountTx(ctx, f.workerDB)
	parent, err := enqueueLockFixture(ctx, f, tx, first, 5)
	if err != nil || parent == otherParent {
		t.Fatal("unrelated batch blocked admission", parent, err)
	}
	f.commit(tx)
	_ = locked.Rollback(f.ctx)
	// Lifecycle changes must still exclude admission and metadata writes.
	lifecycle := f.accountTx(f.ctx, f.adminDB)
	f.exec(lifecycle, `SELECT 1 FROM app.accounts WHERE id=$1 FOR UPDATE`, f.accountID)
	blocked := f.accountTx(f.ctx, f.workerDB)
	_, err = enqueueLockFixture(f.ctx, f, blocked, first, 5)
	var busy *pgconn.PgError
	if !errors.As(err, &busy) || busy.Code != "55P03" {
		t.Fatal("lifecycle exclusion was weakened", err)
	}
	_ = blocked.Rollback(f.ctx)
	_ = lifecycle.Rollback(f.ctx)
}
