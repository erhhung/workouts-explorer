package migrations_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func providerIdentityRaces(t *testing.T, db testDatabases) {
	t.Run("AdvisoryContentionThenRetry", func(t *testing.T) {
		first := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
		second := providerIdentityNewSource(t, db, first.account, "health-auto-export-icloud")
		holder := providerIdentityBegin(t, db, first)
		contender := beginAccount(t, db.ctx, db.worker, second.account)
		_, err := contender.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,26)`, second.job, providerIdentityWorker, second.lease)
		providerIdentitySQLState(t, err, "55P03")
		_ = contender.Rollback(db.ctx)
		_ = holder.Rollback(db.ctx)
		retry := providerIdentityBegin(t, db, second)
		result := providerIdentityResolve(t, db, retry, second, "concurrent/new", nil, nil)
		if result.outcome != "created" {
			t.Fatalf("contention changed identity outcome: %+v", result)
		}
		_ = retry.Rollback(db.ctx)
	})
	t.Run("NewSourceEpochChangeIsTransient", func(t *testing.T) {
		for _, boundary := range []string{"resolver", "commit"} {
			t.Run(boundary, func(t *testing.T) {
				f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
				tx := providerIdentityBegin(t, db, f)
				// This source did not exist in the fence's locked source snapshot.
				newSource := providerIdentityNewSource(t, db, f.account, "health-auto-export-icloud")
				providerIdentityDeleteSource(t, db, newSource)
				var err error
				if boundary == "resolver" {
					_, err = tx.Exec(db.ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,'epoch/new-source',NULL,NULL)`,
						f.job, providerIdentityWorker, f.lease, f.file, uuid.New())
				} else {
					err = tx.Commit(db.ctx)
				}
				providerIdentitySQLState(t, err, "55P03")
				_ = tx.Rollback(db.ctx)
				retry := providerIdentityBegin(t, db, f)
				result := providerIdentityResolve(t, db, retry, f, "epoch/new-source", nil, nil)
				if result.outcome != "created" {
					t.Fatalf("epoch contention became a permanent refusal: %+v", result)
				}
				_ = retry.Rollback(db.ctx)
			})
		}
	})
	t.Run("SourceAndGenerationContentionYieldWithoutWaiting", func(t *testing.T) {
		for _, table := range []string{"sources", "account_data_generations"} {
			t.Run(table, func(t *testing.T) {
				f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
				blocker := beginAccount(t, db.ctx, db.migration, f.account)
				defer blocker.Rollback(db.ctx)
				query := `SELECT 1 FROM app.sources WHERE id=$1 FOR UPDATE`
				key := f.source
				if table == "account_data_generations" {
					query, key = `SELECT 1 FROM app.account_data_generations WHERE account_id=$1 FOR UPDATE`, f.account
				}
				providerIdentityExec(t, db, blocker, query, key)
				tx := beginAccount(t, db.ctx, db.worker, f.account)
				_, err := tx.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, providerIdentityWorker, f.lease)
				providerIdentitySQLState(t, err, "55P03")
				_ = tx.Rollback(db.ctx)
				_ = blocker.Rollback(db.ctx)
				retry := providerIdentityBegin(t, db, f)
				_ = retry.Rollback(db.ctx)
			})
		}
	})
	t.Run("SourceDeletionLinearizesOwnerDecision", func(t *testing.T) {
		owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
		other := providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
		workout := providerIdentitySeed(t, db, owner, "delete/race", true)
		deletion := beginAccount(t, db.ctx, db.api, owner.account)
		defer deletion.Rollback(db.ctx)
		var deleted bool
		if err := deletion.QueryRow(db.ctx, `SELECT app.delete_source($1,$2)`, owner.source, owner.requester).Scan(&deleted); err != nil || !deleted {
			t.Fatalf("start source deletion: %t %v", deleted, err)
		}
		tx := beginAccount(t, db.ctx, db.worker, other.account)
		_, err := tx.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,26)`, other.job, providerIdentityWorker, other.lease)
		providerIdentitySQLState(t, err, "55P03")
		_ = tx.Rollback(db.ctx)
		if err := deletion.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		retry := providerIdentityBegin(t, db, other)
		result := providerIdentityResolve(t, db, retry, other, "delete/race", nil, nil)
		if result.outcome != "adopted" || result.workout == nil || *result.workout != workout {
			t.Fatalf("committed tombstone not observed: %+v", result)
		}
		_ = retry.Rollback(db.ctx)
	})
	t.Run("WorkoutAndCoverageFKContentionYield", func(t *testing.T) {
		for _, mode := range []string{"UPDATE", "KEY SHARE"} {
			t.Run(mode, func(t *testing.T) {
				owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
				other := providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
				workout := providerIdentitySeed(t, db, owner, "workout/race", true)
				providerIdentityDeleteSource(t, db, owner)
				blocker := beginAccount(t, db.ctx, db.migration, owner.account)
				defer blocker.Rollback(db.ctx)
				providerIdentityExec(t, db, blocker, `SELECT 1 FROM app.workouts WHERE id=$1 FOR `+mode, workout)
				tx := providerIdentityBegin(t, db, other)
				_, err := tx.Exec(db.ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,'workout/race',NULL,NULL)`,
					other.job, providerIdentityWorker, other.lease, other.file, uuid.New())
				providerIdentitySQLState(t, err, "55P03")
				_ = tx.Rollback(db.ctx)
				_ = blocker.Rollback(db.ctx)
				retry := providerIdentityBegin(t, db, other)
				result := providerIdentityResolve(t, db, retry, other, "workout/race", nil, nil)
				if result.outcome != "adopted" {
					t.Fatalf("row contention became a missing/conflicting identity: %+v", result)
				}
				_ = retry.Rollback(db.ctx)
			})
		}
	})
	t.Run("SourceDeletionEpochCannotWaitBehindAccountFirstWriter", func(t *testing.T) {
		f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
		blocker := beginAccount(t, db.ctx, db.migration, f.account)
		defer blocker.Rollback(db.ctx)
		providerIdentityExec(t, db, blocker, `SELECT 1 FROM app.accounts WHERE id=$1 FOR UPDATE`, f.account)
		deletion := beginAccount(t, db.ctx, db.api, f.account)
		_, err := deletion.Exec(db.ctx, `SELECT app.delete_source($1,$2)`, f.source, f.requester)
		providerIdentitySQLState(t, err, "55P03")
		_ = deletion.Rollback(db.ctx)
		_ = blocker.Rollback(db.ctx)
		providerIdentityDeleteSource(t, db, f)
		check := beginAccount(t, db.ctx, db.migration, f.account)
		defer check.Rollback(db.ctx)
		var epoch int64
		if err := check.QueryRow(db.ctx, `SELECT workout_ownership_epoch FROM app.accounts WHERE id=$1`, f.account).Scan(&epoch); err != nil || epoch != 1 {
			t.Fatalf("failed source deletion leaked epoch/state: %d %v", epoch, err)
		}
	})
	t.Run("WorkoutDeletionRequestWinsBeforeReplacementIngest", func(t *testing.T) {
		owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
		other := providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
		workout := providerIdentitySeed(t, db, owner, "purge/race", true)
		deletion := beginAccount(t, db.ctx, db.api, owner.account)
		defer deletion.Rollback(db.ctx)
		providerIdentityExec(t, db, deletion, `SELECT * FROM app.enqueue_workout_deletion($1,$2)`, workout, owner.requester)
		tx := beginAccount(t, db.ctx, db.worker, other.account)
		_, err := tx.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,26)`, other.job, providerIdentityWorker, other.lease)
		providerIdentitySQLState(t, err, "55P03")
		_ = tx.Rollback(db.ctx)
		if err := deletion.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		retry := providerIdentityBegin(t, db, other)
		result := providerIdentityResolve(t, db, retry, other, "purge/race", nil, nil)
		if result.outcome != "suppressed" {
			t.Fatalf("deletion request was resurrected: %+v", result)
		}
		_ = retry.Rollback(db.ctx)
	})
	t.Run("AdoptionDoesNotLockOrAdvanceAccountEpoch", func(t *testing.T) {
		owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
		other := providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
		workout := providerIdentitySeed(t, db, owner, "epoch/race", true)
		providerIdentityDeleteSource(t, db, owner)
		blocker := beginAccount(t, db.ctx, db.migration, owner.account)
		defer blocker.Rollback(db.ctx)
		providerIdentityExec(t, db, blocker, `SELECT 1 FROM app.accounts WHERE id=$1 FOR NO KEY UPDATE`, owner.account)
		tx := providerIdentityBegin(t, db, other)
		result := providerIdentityResolve(t, db, tx, other, "epoch/race", nil, nil)
		if result.outcome != "adopted" {
			t.Fatalf("account FK lock blocked identity resolution: %+v", result)
		}
		providerIdentityExec(t, db, tx, `UPDATE app.workouts SET source_id=$2,source_file_id=$3 WHERE id=$1`, workout, other.source, other.file)
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatalf("adoption waited on or changed the account epoch: %v", err)
		}
		_ = blocker.Rollback(db.ctx)
		check := beginAccount(t, db.ctx, db.migration, owner.account)
		defer check.Rollback(db.ctx)
		var adopted bool
		if err := check.QueryRow(db.ctx, `SELECT source_id=$2 AND source_file_id=$3
			AND (SELECT workout_ownership_epoch=1 FROM app.accounts WHERE id=$4)
			FROM app.workouts WHERE id=$1`, workout, other.source, other.file, owner.account).Scan(&adopted); err != nil || !adopted {
			t.Fatalf("adoption changed epoch or failed to preserve ownership: %t %v", adopted, err)
		}
	})
	t.Run("FencedFileCommitsAfterWallClockExpiry", func(t *testing.T) {
		for _, outcome := range []string{"created", "adopted"} {
			t.Run(outcome, func(t *testing.T) {
				owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
				f := owner
				var workout uuid.UUID
				if outcome == "adopted" {
					workout = providerIdentitySeed(t, db, owner, "expired/provider", true)
					f = providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
					providerIdentityDeleteSource(t, db, owner)
				}
				if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.heartbeat_job($1,$2,$3,interval '1 second')`, f.job, providerIdentityWorker, f.lease) {
					t.Fatal("set bounded lease before fencing")
				}
				tx := providerIdentityBegin(t, db, f)
				providerIdentityExec(t, db, tx, `SELECT pg_sleep(1.05)`)
				var expired bool
				if err := tx.QueryRow(db.ctx, `SELECT lease_expires_at<clock_timestamp() FROM app.jobs WHERE id=$1`, f.job).Scan(&expired); err != nil || !expired {
					t.Fatalf("test did not cross the lease deadline: %t %v", expired, err)
				}
				// Recovery/heartbeat cannot acquire the job row held by this file.
				contender := beginAccount(t, db.ctx, db.migration, f.account)
				_, err := contender.Exec(db.ctx, `SELECT 1 FROM app.jobs WHERE id=$1 FOR UPDATE NOWAIT`, f.job)
				providerIdentitySQLState(t, err, "55P03")
				_ = contender.Rollback(db.ctx)
				for _, mismatch := range []struct {
					job     uuid.UUID
					worker  string
					lease   uuid.UUID
					runtime int
				}{{uuid.New(), providerIdentityWorker, f.lease, 26}, {f.job, "other-worker", f.lease, 26},
					{f.job, providerIdentityWorker, uuid.New(), 26}, {f.job, providerIdentityWorker, f.lease, 27}} {
					attempt, err := tx.Begin(db.ctx)
					if err != nil {
						t.Fatal(err)
					}
					_, err = attempt.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,$4)`, mismatch.job, mismatch.worker, mismatch.lease, mismatch.runtime)
					providerIdentitySQLState(t, err, "25001")
					if err := attempt.Rollback(db.ctx); err != nil {
						t.Fatal(err)
					}
				}
				var source uuid.UUID
				if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, providerIdentityWorker, f.lease).Scan(&source); err != nil || source != f.source {
					t.Fatalf("matching repeated fence expired: %s %v", source, err)
				}
				result := providerIdentityResolve(t, db, tx, f, "expired/provider", nil, nil)
				if result.outcome != outcome || result.workout == nil || (outcome == "adopted" && *result.workout != workout) {
					t.Fatalf("fenced identity expired: %+v", result)
				}
				workout = *result.workout
				if outcome == "created" {
					providerIdentityInsert(t, db, tx, f, workout, "expired/provider", nil, nil)
				} else {
					providerIdentityExec(t, db, tx, `UPDATE app.workouts SET source_id=$2,source_file_id=$3 WHERE id=$1`, workout, f.source, f.file)
				}
				providerIdentityEvent(t, db, tx, f, workout, outcome)
				if result := providerIdentityResolve(t, db, tx, f, "expired/provider", nil, nil); result.outcome != "existing" {
					t.Fatalf("same-source resolution expired: %+v", result)
				}
				if err := tx.Commit(db.ctx); err != nil {
					t.Fatalf("correctly fenced file could not commit after clock expiry: %v", err)
				}
				check := beginAccount(t, db.ctx, db.migration, f.account)
				defer check.Rollback(db.ctx)
				var persisted bool
				if err := check.QueryRow(db.ctx, `SELECT EXISTS(SELECT 1 FROM app.workouts WHERE id=$1 AND source_id=$2 AND source_file_id=$3)
					AND EXISTS(SELECT 1 FROM app.workout_import_events WHERE workout_id=$1 AND job_id=$4 AND kind=$5)`, workout, f.source, f.file, f.job, outcome).Scan(&persisted); err != nil || !persisted {
					t.Fatalf("fenced file was not persisted: %t %v", persisted, err)
				}
			})
		}
	})
	t.Run("ExpiredBeforeFreshFenceOrContentionRetryIsDenied", func(t *testing.T) {
		for _, contention := range []bool{false, true} {
			f := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
			if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.heartbeat_job($1,$2,$3,interval '1 second')`, f.job, providerIdentityWorker, f.lease) {
				t.Fatal("set bounded lease before fresh fence")
			}
			blocker := beginAccount(t, db.ctx, db.migration, f.account)
			if contention {
				providerIdentityExec(t, db, blocker, `SELECT 1 FROM app.sources WHERE id=$1 FOR UPDATE`, f.source)
				attempt := beginAccount(t, db.ctx, db.worker, f.account)
				_, err := attempt.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, providerIdentityWorker, f.lease)
				providerIdentitySQLState(t, err, "55P03")
				_ = attempt.Rollback(db.ctx)
			}
			providerIdentityExec(t, db, blocker, `SELECT pg_sleep(1.05)`)
			_ = blocker.Rollback(db.ctx)
			retry := beginAccount(t, db.ctx, db.worker, f.account)
			var source uuid.UUID
			err := retry.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, providerIdentityWorker, f.lease).Scan(&source)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("expired fresh lease admitted (contention=%t): %v", contention, err)
			}
			_, err = retry.Exec(db.ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,'expired/unfenced',NULL,NULL)`, f.job, providerIdentityWorker, f.lease, f.file, uuid.New())
			providerIdentitySQLState(t, err, "42501")
			_ = retry.Rollback(db.ctx)
		}
	})
}
