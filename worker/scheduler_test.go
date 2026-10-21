package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
)

func TestSchedulerICloudSnapshotsIntegration(t *testing.T) {
	workerURL, migrationURL := os.Getenv("WORKER_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if workerURL == "" || migrationURL == "" {
		t.Skip("WORKER_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	workerDB := openWorkerTestPool(t, ctx, workerURL)
	setupDB := openWorkerTestPool(t, ctx, migrationURL)
	keys := testKeyring(t)
	scheduler := NewScheduler(workerDB, slog.New(slog.NewTextHandler(io.Discard, nil)), keys, []string{"/synthetic"}, SchedulerOptions{PollInterval: time.Minute, Lease: 5 * time.Minute})
	accountID, foreignAccount := uuid.New(), uuid.New()
	config := sourceconfig.ICloud{Version: 1, Path: "Auto Export/Synthetic", AppleID: "scheduler-apple-canary@example.test", Password: strings.Repeat("A", 24), Cookies: "scheduler-cookie-canary", TrustToken: "scheduler-trust-canary"}
	_, cloudJSON, err := sourceconfig.CanonicalizeICloud(config)
	if err != nil {
		t.Fatal(err)
	}
	localJSON := []byte(`{"version":1,"path":"/synthetic/exports"}`)
	expected := make(map[uuid.UUID][]byte)
	envelopes := make(map[uuid.UUID][]byte)
	for _, owner := range []uuid.UUID{accountID, foreignAccount} {
		tx := beginWorkerTestAccount(t, setupDB, owner)
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts(id) VALUES($1)`, owner); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 4; index++ {
			id := uuid.New()
			typeName, canonical := sourceconfig.ICloudType, cloudJSON
			status, autoSync := "connected", true
			if index == 1 {
				typeName, canonical = sourceconfig.LocalType, localJSON
			} else if index == 2 {
				status = "connection-failed"
			} else if index == 3 {
				autoSync = false
			}
			envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: owner, RecordID: id}, canonical)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,auto_sync_enabled,config_envelope) VALUES($1,$2,$3,$3,$4,$5,$6,$7)`, id, owner, "scheduler-"+id.String(), typeName, status, autoSync, envelope); err != nil {
				t.Fatal(err)
			}
			if owner == accountID && index < 2 {
				expected[id], envelopes[id] = canonical, envelope
			}
		}
		if owner == accountID {
			if _, err := tx.Exec(ctx, `INSERT INTO app.account_sync_schedules(account_id,next_run_at) VALUES($1,now())`, owner); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	claimLease := func() uuid.UUID {
		t.Helper()
		tx := beginWorkerTestAccount(t, setupDB, accountID)
		defer tx.Rollback(ctx)
		// Prioritize this fixture without manufacturing the runtime-versioned lease.
		if _, err := tx.Exec(ctx, `UPDATE app.account_sync_schedules SET next_run_at='-infinity' WHERE account_id=$1`, accountID); err != nil {
			t.Fatal(err)
		}
		token := uuid.New()
		var claimedAccount, claimedToken uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT account_id,lease_token FROM app.claim_due_sync_account($1,$2,$3,$4)`, scheduler.workerID, token, scheduler.lease, database.SupportedSchemaVersion).Scan(&claimedAccount, &claimedToken); err != nil {
			t.Fatal(err)
		}
		if claimedAccount != accountID || claimedToken != token {
			t.Fatal("scheduler claim did not acquire the fixture account and requested token")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := claimLease()
	if _, err := scheduler.enqueue(ctx, accountID, uuid.New()); err == nil {
		t.Fatal("scheduler accepted a foreign lease token")
	}
	parentID, err := scheduler.enqueue(ctx, accountID, token)
	if err != nil || parentID == nil {
		t.Fatalf("scheduled iCloud enqueue failed: %v", err)
	}
	tx := beginWorkerTestAccount(t, setupDB, accountID)
	rows, err := tx.Query(ctx, `SELECT j.id,s.source_id,s.source_generation,s.config_envelope,j.parameters,c.source_type FROM app.jobs j JOIN app.job_config_snapshots s ON s.job_id=j.id JOIN app.job_source_contexts c ON c.job_id=j.id WHERE j.parent_job_id=$1`, *parentID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var childID, sourceID uuid.UUID
		var generation int64
		var snapshot, parameters []byte
		var sourceType string
		if err := rows.Scan(&childID, &sourceID, &generation, &snapshot, &parameters, &sourceType); err != nil {
			t.Fatal(err)
		}
		canonical, ok := expected[sourceID]
		if !ok || generation != 1 {
			t.Fatal("scheduler included a disconnected, disabled, or foreign source")
		}
		plaintext, err := keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: childID}, snapshot)
		if err != nil || !bytes.Equal(plaintext, canonical) || bytes.Equal(snapshot, envelopes[sourceID]) {
			t.Fatal("scheduler snapshot was not independently encrypted")
		}
		if bytes.Equal(canonical, cloudJSON) && sourceType != sourceconfig.ICloudType {
			t.Fatal("scheduled source context lost iCloud type")
		}
		for _, secret := range []string{config.AppleID, config.Password, config.Cookies, config.TrustToken, "appleId", "password", "cookies", "trustToken"} {
			if bytes.Contains(parameters, []byte(secret)) || bytes.Contains(snapshot, []byte(secret)) {
				t.Fatal("scheduler exposed a credential in job artifacts")
			}
		}
		if _, err := keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: foreignAccount, RecordID: childID}, snapshot); !errors.Is(err, sourcecrypto.ErrAuthentication) {
			t.Fatal("scheduled snapshot accepted a foreign account")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	_ = tx.Rollback(ctx)
	if count != 2 {
		t.Fatalf("scheduled snapshots=%d want=2", count)
	}
	token = claimLease()
	reusedID, err := scheduler.enqueue(ctx, accountID, token)
	if err != nil || reusedID == nil || *reusedID != *parentID {
		t.Fatalf("scheduled iCloud enqueue lost coalescing: %v", err)
	}
}

func TestScheduledIngestKeyIsNormalizedAndGenerationBound(t *testing.T) {
	first := scheduledSource{id: uuid.MustParse("00000000-0000-0000-0000-000000000001"), generation: 1}
	second := scheduledSource{id: uuid.MustParse("00000000-0000-0000-0000-000000000002"), generation: 1}
	key := scheduledIngestKey([]scheduledSource{first, second})
	if key != scheduledIngestKey([]scheduledSource{first, second}) {
		t.Fatal("scheduled ingest key is not deterministic")
	}
	second.generation++
	if key == scheduledIngestKey([]scheduledSource{first, second}) {
		t.Fatal("scheduled ingest key is not generation-bound")
	}
	if bytes.Contains(key[:], first.id[:]) {
		t.Fatal("scheduled ingest key exposes a source identifier")
	}
}

func TestSchedulerLoopCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cycles := 0
	err := runSchedulerLoop(ctx, time.Hour, func(context.Context) (bool, error) {
		cycles++
		cancel()
		return false, context.Canceled
	}, func(error) { t.Fatal("cancellation was reported as a cycle failure") })
	if err != nil || cycles != 1 {
		t.Fatalf("cancelled loop cycles=%d err=%v", cycles, err)
	}
}

func TestSafeSchedulerErrorRedactsDetails(t *testing.T) {
	err := safeSchedulerError(errors.New("decrypt /private/source/path: secret envelope"))
	if err == nil || err.Error() != "scheduler operation failed" {
		t.Fatalf("unsafe scheduler error: %v", err)
	}
	if safeSchedulerError(nil) != nil {
		t.Fatal("nil scheduler error was changed")
	}
}
