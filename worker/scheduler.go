package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const scheduledIngestKeyDomain = "workouts-explorer/scheduled-ingest/v1\x00"

type Scheduler struct {
	db           *pgxpool.Pool
	logger       *slog.Logger
	keys         *sourcecrypto.Keyring
	localRoots   []string
	workerID     string
	pollInterval time.Duration
	lease        time.Duration
}

type SchedulerOptions struct {
	PollInterval time.Duration
	Lease        time.Duration
}

type scheduledSource struct {
	id          uuid.UUID
	generation  int64
	envelope    []byte
	displayName string
	typeName    string
	canonical   []byte
}

type scheduledChild struct {
	SourceID    uuid.UUID `json:"sourceId"`
	Generation  int64     `json:"generation"`
	ChildID     uuid.UUID `json:"childId"`
	Snapshot    string    `json:"snapshot,omitempty"`
	FailureCode string    `json:"failureCode,omitempty"`
}

func NewScheduler(db *pgxpool.Pool, logger *slog.Logger, keys *sourcecrypto.Keyring, localRoots []string, options SchedulerOptions) *Scheduler {
	return &Scheduler{db: db, logger: logger, keys: keys, localRoots: append([]string(nil), localRoots...),
		workerID: "sync-scheduler-" + uuid.NewString(), pollInterval: options.PollInterval, lease: options.Lease}
}

func ConfigureAutoSyncPolicy(ctx context.Context, db *pgxpool.Pool, cadence time.Duration, staleDays int) error {
	var configured bool
	if err := db.QueryRow(ctx, `SELECT app.configure_auto_sync_policy($1,$2)`, cadence, staleDays).Scan(&configured); err != nil {
		return fmt.Errorf("configure automatic sync policy: %w", err)
	}
	if !configured {
		return errors.New("configure automatic sync policy: database rejected update")
	}
	return nil
}

func (s *Scheduler) Run(ctx context.Context) error {
	return runSchedulerLoop(ctx, s.pollInterval, s.RunOnce, func(err error) {
		s.logger.WarnContext(ctx, "automatic sync scheduler cycle failed", "error", safeSchedulerError(err))
	})
}

func runSchedulerLoop(ctx context.Context, pollInterval time.Duration, cycle func(context.Context) (bool, error), report func(error)) error {
	for {
		worked, err := cycle(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if worked {
			continue
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) (bool, error) {
	accountID, token, err := s.claim(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	jobID, err := s.enqueue(ctx, accountID, token)
	if err == nil {
		return true, nil
	}
	// Cancellation deliberately leaves ownership to lease expiry for another replica.
	if ctx.Err() == nil {
		releaseCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if releaseErr := s.release(releaseCtx, accountID, token); releaseErr != nil {
			s.logger.WarnContext(ctx, "automatic sync schedule release failed", "account_id", accountID, "error", safeSchedulerError(releaseErr))
		}
	}
	_ = jobID
	return true, err
}

func (s *Scheduler) claim(ctx context.Context) (uuid.UUID, uuid.UUID, error) {
	token := uuid.New()
	var accountID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT account_id,lease_token FROM app.claim_due_sync_account($1,$2,$3,$4)`,
		s.workerID, token, s.lease, database.SupportedSchemaVersion).Scan(&accountID, &token)
	return accountID, token, err
}

func (s *Scheduler) enqueue(ctx context.Context, accountID, token uuid.UUID) (*uuid.UUID, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT source_id,generation,config_envelope,display_name,source_type
		FROM app.read_leased_sync_sources($1,$2,$3)`, accountID, s.workerID, token)
	if err != nil {
		return nil, err
	}
	var sources []scheduledSource
	for rows.Next() {
		var source scheduledSource
		if err := rows.Scan(&source.id, &source.generation, &source.envelope, &source.displayName, &source.typeName); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	children := make([]scheduledChild, 0, len(sources))
	for index := range sources {
		source := &sources[index]
		child := scheduledChild{SourceID: source.id, Generation: source.generation, ChildID: uuid.Must(uuid.NewV7())}
		plaintext, err := s.keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: source.id}, source.envelope)
		if err != nil || source.typeName != sourceconfig.LocalType {
			child.FailureCode = "source-config-invalid"
			children = append(children, child)
			continue
		}
		_, canonical, err := sourceconfig.DecodeLocal(plaintext, s.localRoots)
		if err != nil || !bytes.Equal(plaintext, canonical) {
			child.FailureCode = "source-config-invalid"
			children = append(children, child)
			continue
		}
		source.canonical = canonical
		snapshot, err := s.keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: child.ChildID}, canonical)
		if err != nil {
			child.FailureCode = "source-config-invalid"
			children = append(children, child)
			continue
		}
		child.Snapshot = base64.StdEncoding.EncodeToString(snapshot)
		children = append(children, child)
	}
	encodedChildren, err := json.Marshal(children)
	if err != nil {
		return nil, err
	}
	parentID := uuid.Must(uuid.NewV7())
	key := scheduledIngestKey(sources)
	var resultingJob *uuid.UUID
	var reused bool
	err = tx.QueryRow(ctx, `SELECT job_id,reused FROM app.enqueue_leased_scheduled_ingest($1,$2,$3,$4,$5,$6)`,
		accountID, s.workerID, token, parentID, key[:], encodedChildren).Scan(&resultingJob, &reused)
	if errors.Is(err, pgx.ErrNoRows) && len(sources) == 0 {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	var finished bool
	if err := tx.QueryRow(ctx, `SELECT app.finish_sync_account($1,$2,$3,$4)`, accountID, s.workerID, token, resultingJob).Scan(&finished); err != nil {
		return nil, err
	}
	if !finished {
		return nil, errors.New("scheduler lease lost")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resultingJob, nil
}

func scheduledIngestKey(sources []scheduledSource) [32]byte {
	hash := sha256.New()
	hash.Write([]byte(scheduledIngestKeyDomain))
	for _, source := range sources {
		hash.Write(source.id[:])
		var generation [8]byte
		binary.BigEndian.PutUint64(generation[:], uint64(source.generation))
		hash.Write(generation[:])
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func (s *Scheduler) release(ctx context.Context, accountID, token uuid.UUID) error {
	var released bool
	if err := s.db.QueryRow(ctx, `SELECT app.release_sync_account($1,$2,$3,$4)`, accountID, s.workerID, token, s.pollInterval).Scan(&released); err != nil {
		return err
	}
	return nil
}

func safeSchedulerError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("scheduler operation failed")
}
