package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCoverageMetadataLegacyAccountRevisionBootstrapIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
	enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, producer, f.ctx)
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{})
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	ordinary, arrival := uuid.New(), uuid.New()
	t.Cleanup(func() {
		if _, err := f.adminDB.Exec(f.ctx, `DELETE FROM app.accounts WHERE id IN ($1,$2)`, ordinary, arrival); err != nil {
			t.Error(err)
		}
	})
	var prior int64
	var cursor uuid.UUID
	if err := f.adminDB.QueryRow(f.ctx, `SELECT desired_revision FROM app.coverage_reconciliation_state WHERE singleton`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if err := f.adminDB.QueryRow(f.ctx, `SELECT max(workout_id::text)::uuid FROM app.workout_coverage_states WHERE account_id=$1`, f.accountID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	// Model retained pre-feature input: no selection proof, independently boosted
	// completed/desired counters, an old cursor, and a retained campaign failure.
	if _, err := f.adminDB.Exec(f.ctx, `DELETE FROM app.coverage_route_selection_proofs WHERE account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET desired_revision=$2,completed_revision=$2,
		active_revision=$2,cursor_workout_id=$3,last_failure_code='legacy-bootstrap-failure',last_failure_summary='Synthetic retained failure.',
		next_scan_at='infinity',updated_at=clock_timestamp()-interval '1 day' WHERE account_id=$1`, f.accountID, prior+1, cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `INSERT INTO app.accounts(id) VALUES($1),($2)`, ordinary, arrival); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,completed_revision,active_revision)
		VALUES($1,$2,$2,$2)`, ordinary, prior); err != nil {
		t.Fatal(err)
	}
	// A genuine matching-target change, without a fabricated catalog fingerprint.
	r.minimumTraversal = 6
	if err := r.observe(f.ctx); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err := f.adminDB.QueryRow(f.ctx, `SELECT desired_revision FROM app.coverage_reconciliation_state WHERE singleton`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != prior+2 {
		t.Fatalf("new campaign=%d must exceed global=%d and retained account=%d", next, prior, prior+1)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1
		AND completed_revision=$2 AND desired_revision=$3 AND active_revision=$2 AND cursor_workout_id=$4
		AND last_failure_code='legacy-bootstrap-failure' AND next_scan_at<=clock_timestamp()`, f.accountID, prior+1, next, cursor)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1 AND completed_revision=$2 AND desired_revision=$3`, ordinary, prior, next)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1 AND completed_revision=0 AND desired_revision=$2`, arrival, next)
	snapshot := func() string {
		t.Helper()
		var value string
		if err := f.adminDB.QueryRow(f.ctx, `SELECT jsonb_build_array(
			(SELECT row_to_json(state)::text||xmin::text FROM app.coverage_reconciliation_state state WHERE singleton),
			(SELECT jsonb_agg(row_to_json(account)::text||xmin::text ORDER BY account_id) FROM app.coverage_reconciliation_accounts account))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	locked, err := f.adminDB.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(f.ctx)
	if _, err := locked.Exec(f.ctx, `LOCK TABLE app.coverage_reconciliation_accounts IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	err = r.observe(ctx)
	cancel()
	if err != nil {
		t.Fatalf("identical observation enumerated locked accounts: %v", err)
	}
	if err := locked.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("identical observation performed global/account DML")
	}
	seen := make(map[uuid.UUID]bool)
	for range 3 {
		claim, found, err := r.claim(f.ctx)
		if err != nil || !found || !claim.scanRequired || claim.revision != next || claim.cursor != nil || seen[claim.accountID] {
			t.Fatalf("fresh bootstrap claim: %+v found=%t err=%v", claim, found, err)
		}
		seen[claim.accountID] = true
		if err := r.process(f.ctx, claim); err != nil {
			t.Fatal(err)
		}
	}
	if !seen[f.accountID] || !seen[ordinary] || !seen[arrival] {
		t.Fatalf("not all three account cases were scheduled: %v", seen)
	}
	assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.coverage_route_selection_proofs WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id IN ($1,$2,$3)
		AND completed_revision=$4 AND desired_revision=$4`, f.accountID, ordinary, arrival, next)
}
