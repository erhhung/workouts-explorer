package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func deferredSelectionSession(t *testing.T, db testDatabases, f bootstrapFixture) uuid.UUID {
	t.Helper()
	principal := accountRequester(t, db, f.account)
	session := uuid.New()
	bootstrapExec(t, db, f, `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at)
	 VALUES($1,$2,'bearer',decode(md5($1::uuid::text)||md5($1::uuid::text),'hex'),transaction_timestamp()+interval '1 hour')`, session, principal)
	return session
}

func deferredBeginSelection(t *testing.T, db testDatabases, account, session uuid.UUID) pgx.Tx {
	t.Helper()
	tx, err := db.api.BeginTx(db.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(db.ctx, `SELECT set_config('app.account_id',$1,true),set_config('app.session_id',$2,true)`, account.String(), session.String()); err != nil {
		t.Fatal(err)
	}
	return tx
}

const deferredInsertSelection = `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind,focused_workout_id)
 VALUES($1,$2,$3,$4,transaction_timestamp()+interval '30 minutes','2026-01-01','2026-01-01','explicit_subset',$5)`

func deferredSelectionDeadlock(t *testing.T, db testDatabases) {
	for _, resource := range []string{"workout", "account"} {
		t.Run(resource, func(t *testing.T) {
			f := newBootstrapFixture(t, db, 1)
			bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state) VALUES($1,$2,1,$3,'unresolved')`, f.account, f.workouts[0], bootstrapDigest())
			session := deferredSelectionSession(t, db, f)
			selection := deferredBeginSelection(t, db, f.account, session)
			var generation int64
			if err := selection.QueryRow(db.ctx, `SELECT app.lock_account_data_generation()`).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			writer := beginAccount(t, db.ctx, db.migration, f.account)
			defer writer.Rollback(db.ctx)
			if resource == "workout" {
				providerIdentityExec(t, db, writer, `SELECT 1 FROM app.workouts WHERE id=$1 FOR UPDATE`, f.workouts[0])
			} else {
				providerIdentityExec(t, db, writer, `SELECT 1 FROM app.accounts WHERE id=$1 FOR UPDATE`, f.account)
			}
			var writerPID, selectionPID int
			if err := writer.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
				t.Fatal(err)
			}
			if err := selection.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&selectionPID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 2)
			go func() {
				_, err := writer.Exec(db.ctx, `UPDATE app.workout_coverage_states SET processing_state='current',processing_finished_at=clock_timestamp() WHERE account_id=$1`, f.account)
				_ = writer.Rollback(context.Background())
				done <- err
			}()
			deferredWaitBlocked(t, db, writerPID, selectionPID)
			go func() {
				id := uuid.New()
				_, err := selection.Exec(db.ctx, deferredInsertSelection, id, f.account, session, generation, f.workouts[0])
				if err == nil {
					_, err = selection.Exec(db.ctx, `INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order) VALUES($1,$2,$3,0)`, f.account, id, f.workouts[0])
				}
				_ = selection.Rollback(context.Background())
				done <- err
			}()
			deadlocks := 0
			for range 2 {
				err := <-done
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
					deadlocks++
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if deadlocks != 1 {
				t.Fatalf("old generation -> %s FK cycle must deadlock exactly once, got %d", resource, deadlocks)
			}
		})
	}
}

func deferredWaitBlocked(t *testing.T, db testDatabases, waiter, blocker int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := db.migration.QueryRow(db.ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2))`, blocker, waiter).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("did not observe backend %d waiting for %d", waiter, blocker)
}

func deferredSelections(t *testing.T, db testDatabases) {
	for _, resource := range []string{"workout", "account"} {
		t.Run(resource, func(t *testing.T) {
			f := newBootstrapFixture(t, db, 1)
			bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state) VALUES($1,$2,1,$3,'unresolved')`, f.account, f.workouts[0], bootstrapDigest())
			session := deferredSelectionSession(t, db, f)
			writer := beginAccount(t, db.ctx, db.migration, f.account)
			defer writer.Rollback(db.ctx)
			if resource == "workout" {
				providerIdentityExec(t, db, writer, `SELECT 1 FROM app.workouts WHERE id=$1 FOR UPDATE`, f.workouts[0])
			} else {
				providerIdentityExec(t, db, writer, `SELECT 1 FROM app.accounts WHERE id=$1 FOR UPDATE`, f.account)
			}
			providerIdentityExec(t, db, writer, `UPDATE app.workout_coverage_states SET processing_state='current',processing_finished_at=clock_timestamp() WHERE account_id=$1`, f.account)
			selection := deferredBeginSelection(t, db, f.account, session)
			var generation int64
			// A locked counter is a stronger proof than merely inspecting the
			// helper body: a plain snapshot read must still complete immediately.
			counter := beginAccount(t, db.ctx, db.migration, f.account)
			defer counter.Rollback(db.ctx)
			providerIdentityExec(t, db, counter, `SELECT 1 FROM app.account_data_generations WHERE account_id=$1 FOR UPDATE`, f.account)
			providerIdentityExec(t, db, selection, `SET LOCAL lock_timeout='250ms'`)
			if err := selection.QueryRow(db.ctx, `SELECT app.lock_account_data_generation()`).Scan(&generation); err != nil {
				t.Fatal("nonlocking snapshot helper blocked on counter", err)
			}
			if err := counter.Rollback(db.ctx); err != nil {
				t.Fatal(err)
			}
			var selectionPID, writerPID int
			if err := selection.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&selectionPID); err != nil {
				t.Fatal(err)
			}
			if err := writer.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
				t.Fatal(err)
			}
			var locking bool
			if err := db.migration.QueryRow(db.ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation='app.account_data_generations'::regclass AND mode<>'AccessShareLock')`, selectionPID).Scan(&locking); err != nil || locking {
				t.Fatal("helper took a generation locking resource before FKs", err)
			}
			var oldLabel string
			const pairSQL = `SELECT generation.generation,state.processing_state FROM app.account_data_generations generation
			 JOIN app.workout_coverage_states state ON state.account_id=generation.account_id WHERE generation.account_id=$1`
			var readGeneration int64
			if err := selection.QueryRow(db.ctx, pairSQL, f.account).Scan(&readGeneration, &oldLabel); err != nil || readGeneration != generation || oldLabel != "not_started" {
				t.Fatal("initial snapshot pair", err)
			}
			providerIdentityExec(t, db, selection, `SET LOCAL lock_timeout='5s'`)
			id := uuid.New()
			done := make(chan error, 1)
			go func() {
				_, err := selection.Exec(db.ctx, deferredInsertSelection, id, f.account, session, generation, f.workouts[0])
				if err == nil {
					_, err = selection.Exec(db.ctx, `INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order) VALUES($1,$2,$3,0)`, f.account, id, f.workouts[0])
				}
				done <- err
			}()
			deferredWaitBlocked(t, db, selectionPID, writerPID)
			// With a nonlocking generation read, the writer can commit despite
			// the API waiting on its domain FK. The old helper forms a cycle.
			if err := writer.Commit(db.ctx); err != nil {
				t.Fatal("writer deadlocked behind selection", err)
			}
			if err := <-done; err != nil {
				t.Fatal("selection FK after domain release", err)
			}
			var label string
			if err := selection.QueryRow(db.ctx, pairSQL, f.account).Scan(&readGeneration, &label); err != nil || readGeneration != generation || label != oldLabel {
				t.Fatal("REPEATABLE READ mixed new generation with old data", err)
			}
			if err := selection.Commit(db.ctx); err != nil {
				t.Fatal("focused deferred FK commit", err)
			}
			tile := beginAccount(t, db.ctx, db.tiles, f.account)
			_, tileErr := tile.Exec(db.ctx, `SELECT app.raw_route_mvt(14,2639,6377,json_build_object('target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`, f.account, session, id, generation)
			bootstrapSQLState(t, tileErr, "42501")
			_ = tile.Rollback(db.ctx)
			current := beginAccount(t, db.ctx, db.api, f.account)
			defer current.Rollback(db.ctx)
			if err := current.QueryRow(db.ctx, pairSQL, f.account).Scan(&readGeneration, &label); err != nil || readGeneration != generation+1 || label != "current" {
				t.Fatal("new snapshot must see data and generation atomically", err)
			}
			var count int
			if err := current.QueryRow(db.ctx, `SELECT count(*) FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL)`, f.account, session, id, generation).Scan(&count); err != nil || count != 0 {
				t.Fatal("stale selection exposed current coverage", err)
			}
			providerIdentityExec(t, db, current, `SELECT set_config('app.session_id',$1,true)`, session.String())
			_, err := current.Exec(db.ctx, deferredInsertSelection, uuid.New(), f.account, session, generation, f.workouts[0])
			bootstrapSQLState(t, err, "42501")
		})
	}
	t.Run("FocusedConstraintStillDeferredAndEnforced", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		session := deferredSelectionSession(t, db, f)
		tx := deferredBeginSelection(t, db, f.account, session)
		var generation int64
		if err := tx.QueryRow(db.ctx, `SELECT app.lock_account_data_generation()`).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		providerIdentityExec(t, db, tx, deferredInsertSelection, uuid.New(), f.account, session, generation, f.workouts[0])
		err := tx.Commit(db.ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "23503" && pgErr.Code != "23514") {
			t.Fatal(fmt.Sprintf("missing focused membership accepted at commit: %v", err))
		}
	})
}

func deferredFileSelections(t *testing.T, db testDatabases) {
	for _, resource := range []string{"workout", "account"} {
		t.Run(resource, func(t *testing.T) {
			c := deferredCoverage(t, db, 28)
			f := c.f
			session := deferredSelectionSession(t, db, bootstrapFixture{account: f.account})
			f.file = uuid.New()
			file, incoming := deferredBeginFile(t, db, f, 28)
			defer file.Rollback(db.ctx)
			if resource == "workout" {
				providerIdentityExec(t, db, file, `SELECT 1 FROM app.workouts WHERE id=$1 FOR UPDATE`, c.workout)
			}
			// The real ingest fence already owns account KEY SHARE. It must
			// remain compatible with the API's account FK, not be upgraded by
			// giving the runtime a test-only account UPDATE privilege.
			selection := deferredBeginSelection(t, db, f.account, session)
			providerIdentityExec(t, db, selection, `SET LOCAL lock_timeout='250ms'`)
			var generation int64
			if err := selection.QueryRow(db.ctx, `SELECT app.lock_account_data_generation()`).Scan(&generation); err != nil {
				t.Fatal("snapshot helper blocked behind genuine file", err)
			}
			providerIdentityExec(t, db, selection, `SET LOCAL lock_timeout='5s'`)
			var writerPID, selectionPID int
			if err := file.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
				t.Fatal(err)
			}
			if err := selection.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&selectionPID); err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			done := make(chan error, 1)
			go func() {
				_, err := selection.Exec(db.ctx, deferredInsertSelection, id, f.account, session, generation, c.workout)
				if err == nil {
					_, err = selection.Exec(db.ctx, `INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order) VALUES($1,$2,$3,0)`, f.account, id, c.workout)
				}
				done <- err
			}()
			if resource == "workout" {
				deferredWaitBlocked(t, db, selectionPID, writerPID)
			} else {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal("API account FK incompatible with genuine ingest KEY SHARE", err)
					}
				case <-time.After(time.Second):
					t.Fatal("API account FK waited behind genuine ingest KEY SHARE")
				}
			}
			deferredFinishFile(t, db, file, f, incoming)
			if err := file.Commit(db.ctx); err != nil {
				t.Fatal("genuine file commit cycled with API FK", err)
			}
			if resource == "workout" {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			var snapshotGeneration int64
			var count int
			if err := selection.QueryRow(db.ctx, `SELECT app.lock_account_data_generation(),(SELECT count(*) FROM app.workouts WHERE account_id=$1)`, f.account).Scan(&snapshotGeneration, &count); err != nil || snapshotGeneration != generation || count != 1 {
				t.Fatalf("file commit mixed API snapshot generation/count=%d/%d: %v", snapshotGeneration, count, err)
			}
			if err := selection.Commit(db.ctx); err != nil {
				t.Fatal("focused selection commit after genuine source file", err)
			}
			reader := beginAccount(t, db.ctx, db.api, f.account)
			defer reader.Rollback(db.ctx)
			if err := reader.QueryRow(db.ctx, `SELECT app.lock_account_data_generation(),(SELECT count(*) FROM app.workouts WHERE account_id=$1)`, f.account).Scan(&snapshotGeneration, &count); err != nil || snapshotGeneration <= generation || count != 2 {
				t.Fatalf("new API snapshot generation/count=%d/%d: %v", snapshotGeneration, count, err)
			}
		})
	}
}
