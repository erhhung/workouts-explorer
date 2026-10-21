package migrations_test

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOwnerAwareInventoryDeletionRacesPostgreSQL(t *testing.T) {
	if os.Getenv("owner_aware_inventory_DISPOSABLE") != "1" {
		t.Skip("owner_aware_inventory_DISPOSABLE=1 and disposable role database URLs are required")
	}
	db := ownershipInventoryDatabases(t)

	t.Run("fenced file commits blocked inventory after lease clock expires", func(t *testing.T) {
		owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		workout := ownershipInventoryWorkout(t, db, owner)
		f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
		setup := beginAccount(t, db.ctx, db.migration, f.account)
		ownershipInventoryExec(t, db, setup, `SELECT set_config('app.job_transition','heartbeat',true)`)
		ownershipInventoryExec(t, db, setup, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, f.job)
		ownershipInventoryCommit(t, db, setup)

		tx := beginAccount(t, db.ctx, db.worker, f.account)
		var denied int
		if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, ownershipInventoryWorker, uuid.New()).Scan(&denied); err != nil || denied != 0 {
			t.Fatalf("wrong-token fence rows=%d err=%v", denied, err)
		}
		var source uuid.UUID
		if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, ownershipInventoryWorker, f.lease).Scan(&source); err != nil || source != f.source {
			t.Fatalf("initial live fence source=%s err=%v", source, err)
		}
		ownershipInventoryExec(t, db, tx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM lease_expires_at-clock_timestamp())+0.02)) FROM app.jobs WHERE id=$1`, f.job)
		var expired bool
		if err := tx.QueryRow(db.ctx, `SELECT lease_expires_at<clock_timestamp() FROM app.jobs WHERE id=$1`, f.job).Scan(&expired); err != nil || !expired {
			t.Fatalf("held transaction lease expired=%t err=%v", expired, err)
		}
		if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, ownershipInventoryWorker, f.lease).Scan(&source); err != nil || source != f.source {
			t.Fatalf("existing capability rejected after lease clock expired: source=%s err=%v", source, err)
		}
		file := uuid.New()
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,state,processing_started_at)
			VALUES($1,$2,$3,$4,$5,10,now(),'processing',now())`, file, f.account, f.source, f.job, ownershipInventoryName)
		ownershipInventoryExec(t, db, tx, `INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256)
			VALUES($1,$2,$3,$4,$5,$6,'skipped_live_owner',$7)`, uuid.New(), f.account, f.source, file, f.job, workout, bytes.Repeat([]byte{7}, 32))
		ownershipInventoryExec(t, db, tx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,processed_at=now() WHERE id=$1`, file, bytes.Repeat([]byte{7}, 32))
		var recorded bool
		if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,'2026-01-01',10,now(),$5,$6)`,
			f.job, ownershipInventoryWorker, uuid.New(), ownershipInventoryName, ownershipInventoryIdentity, bytes.Repeat([]byte{7}, 32)).Scan(&recorded); err != nil || recorded {
			t.Fatalf("wrong-token inventory recorded=%t err=%v", recorded, err)
		}
		ownershipInventoryRecordTx(t, db, tx, f, ownershipInventoryName, ownershipInventoryIdentity)
		ownershipInventoryCommit(t, db, tx)
		ownershipInventoryMarker(t, db, f, int64ptr(0))
		inspect := beginAccount(t, db.ctx, db.migration, f.account)
		var durable bool
		if err := inspect.QueryRow(db.ctx, `SELECT file.state='succeeded' AND count(event.id)=1
			AND NOT EXISTS(SELECT 1 FROM app.ingest_write_capabilities WHERE job_id=$1)
			FROM app.source_files file JOIN app.workout_import_events event ON event.source_file_id=file.id
			WHERE file.job_id=$1 AND event.kind='skipped_live_owner' GROUP BY file.state`, f.job).Scan(&durable); err != nil || !durable {
			t.Fatalf("expired-clock file/event not durable or capability leaked: durable=%t err=%v", durable, err)
		}
		_ = inspect.Rollback(db.ctx)
		fresh := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		ownershipInventoryAction(t, db, fresh, ownershipInventoryCandidates, "skip")
	})

	t.Run("lease expired before fence cannot record inventory", func(t *testing.T) {
		f := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		setup := beginAccount(t, db.ctx, db.migration, f.account)
		ownershipInventoryExec(t, db, setup, `SELECT set_config('app.job_transition','heartbeat',true)`)
		ownershipInventoryExec(t, db, setup, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.job)
		ownershipInventoryCommit(t, db, setup)
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		var count int
		if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, ownershipInventoryWorker, f.lease).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired initial fence rows=%d err=%v", count, err)
		}
		var recorded bool
		if err := tx.QueryRow(db.ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,'2026-01-01',10,now(),$5,$6)`,
			f.job, ownershipInventoryWorker, f.lease, ownershipInventoryName, ownershipInventoryIdentity, bytes.Repeat([]byte{7}, 32)).Scan(&recorded); err != nil || recorded {
			t.Fatalf("expired unfenced inventory recorded=%t err=%v", recorded, err)
		}
		ownershipInventoryCommit(t, db, tx)
	})

	t.Run("deletion waits for captured file epoch and invalidates afterward", func(t *testing.T) {
		owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		workout := ownershipInventoryWorkout(t, db, owner)
		f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
		ownershipInventoryBlockedFile(t, db, f, workout, true)
		writer := ownershipInventoryFence(t, db, f)
		deletion := beginAccount(t, db.ctx, db.api, f.account)
		var deletionPID int
		if err := deletion.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&deletionPID); err != nil {
			t.Fatal(err)
		}
		type result struct {
			deleted bool
			err     error
		}
		done := make(chan result, 1)
		go func() {
			var outcome result
			outcome.err = deletion.QueryRow(db.ctx, `SELECT app.delete_source($1,$2)`, owner.source, owner.job).Scan(&outcome.deleted)
			done <- outcome
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var blocked bool
			if err := db.migration.QueryRow(db.ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, deletionPID).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			select {
			case outcome := <-done:
				t.Fatalf("deletion passed the file fence: %+v", outcome)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("deletion never waited on the fenced source set")
			}
			time.Sleep(5 * time.Millisecond)
		}
		ownershipInventoryRecordTx(t, db, writer, f, ownershipInventoryName, ownershipInventoryIdentity)
		ownershipInventoryCommit(t, db, writer)
		outcome := <-done
		if outcome.err != nil || !outcome.deleted {
			t.Fatalf("concurrent deletion=%t err=%v", outcome.deleted, outcome.err)
		}
		ownershipInventoryCommit(t, db, deletion)
		ownershipInventoryMarker(t, db, f, int64ptr(0))
		ownershipInventoryEpoch(t, db, f, 1)
		ownershipInventoryFinish(t, db, f)
		next := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		ownershipInventoryAction(t, db, next, ownershipInventoryCandidates, "process")
	})

	t.Run("epoch change rejects commit without promoting captured marker", func(t *testing.T) {
		owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		workout := ownershipInventoryWorkout(t, db, owner)
		f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
		ownershipInventoryBlockedFile(t, db, f, workout, true)
		ownershipInventoryRecord(t, db, f, ownershipInventoryName, ownershipInventoryIdentity)
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		var source uuid.UUID
		if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, ownershipInventoryWorker, f.lease).Scan(&source); err != nil || source != f.source {
			t.Fatalf("migration fixture fence source=%s err=%v", source, err)
		}
		var deleted bool
		if err := tx.QueryRow(db.ctx, `SELECT app.delete_source($1,$2)`, owner.source, owner.job).Scan(&deleted); err != nil || !deleted {
			t.Fatalf("in-transaction deletion=%t err=%v", deleted, err)
		}
		ownershipInventoryRecordTx(t, db, tx, f, ownershipInventoryName, ownershipInventoryIdentity)
		var captured, current, blocked int64
		if err := tx.QueryRow(db.ctx, `SELECT capability.ownership_epoch,account.workout_ownership_epoch,inventory.ownership_blocked_epoch
			FROM app.ingest_write_capabilities capability
			JOIN app.accounts account ON account.id=capability.account_id
			JOIN app.source_objects inventory ON inventory.account_id=capability.account_id AND inventory.source_id=capability.source_id
			WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current() AND inventory.relative_name=$1`, ownershipInventoryName).Scan(&captured, &current, &blocked); err != nil || captured != 0 || current != 1 || blocked != captured {
			t.Fatalf("captured/current/blocked epochs=%d/%d/%d err=%v", captured, current, blocked, err)
		}
		err := tx.Commit(db.ctx)
		var postgres *pgconn.PgError
		if !errors.As(err, &postgres) || postgres.Code != "55P03" {
			t.Fatalf("stale epoch commit error=%v, want retryable 55P03", err)
		}
		ownershipInventoryMarker(t, db, f, int64ptr(0))
		ownershipInventoryEpoch(t, db, f, 0)
		ownershipInventoryFinish(t, db, f)
		if !callBool(t, db.ctx, db.api, owner.account, `SELECT app.delete_source($1,$2)`, owner.source, uuid.New()) {
			t.Fatal("owner deletion retry failed")
		}
		ownershipInventoryEpoch(t, db, f, 1)
		next := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		ownershipInventoryAction(t, db, next, ownershipInventoryCandidates, "process")
	})

	t.Run("inventory rollback cannot clear committed blockers", func(t *testing.T) {
		owner := ownershipInventoryJob(t, db, uuid.Nil, uuid.Nil, "incremental")
		workout := ownershipInventoryWorkout(t, db, owner)
		f := ownershipInventoryJob(t, db, owner.account, uuid.Nil, "incremental")
		ownershipInventoryBlockedFile(t, db, f, workout, false)
		ownershipInventoryRecord(t, db, f, ownershipInventoryName, ownershipInventoryIdentity)
		ownershipInventoryFinish(t, db, f)
		next := ownershipInventoryJob(t, db, f.account, f.source, "incremental")
		tx := ownershipInventoryFence(t, db, next)
		ownershipInventoryRecordTx(t, db, tx, next, ownershipInventoryName, ownershipInventoryIdentity)
		_ = tx.Rollback(db.ctx)
		ownershipInventoryMarker(t, db, f, int64ptr(0))
		ownershipInventoryAction(t, db, next, ownershipInventoryCandidates, "skip")
	})
}
