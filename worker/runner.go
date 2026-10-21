package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/rclonesource"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPollInterval = time.Second
	defaultLease        = 2 * time.Minute
	defaultHeartbeat    = 30 * time.Second
	cleanupTimeout      = 5 * time.Second
)

var (
	errLeaseLost             = errors.New("job lease lost")
	errJobInterrupted        = errors.New("job execution interrupted")
	errCancellationRequested = errors.New("job cancellation requested")
)

type loggedExecutionError struct {
	err error
}

func (e loggedExecutionError) Error() string { return e.err.Error() }
func (e loggedExecutionError) Unwrap() error { return e.err }

type Runner struct {
	db                         *pgxpool.Pool
	osmDB                      *pgxpool.Pool
	osmAutoAddRegions          bool
	coverageMinTraversalMeters float64
	coverageBatchSize          int
	logger                     *slog.Logger
	keys                       *sourcecrypto.Keyring
	localRoots                 []string
	rcloneBinary               string
	stagingRoot                string
	stagingDirty               atomic.Bool
	pendingCleanup             []*rclonesource.Source
	workerID                   string
	pollInterval               time.Duration
	leaseDuration              time.Duration
	heartbeatInterval          time.Duration
	outcomeReader              func(context.Context, claimedJob) (jobOutcome, error)
	beforeProcessFile          func(sourceFile)
	fileSlots                  chan struct{}
}

type claimedJob struct {
	id         uuid.UUID
	accountID  uuid.UUID
	lease      uuid.UUID
	kind       string
	owner      string
	sourceName string
	sourceType string
	mode       ingestMode
	startDate  *time.Time
	endDate    *time.Time
	paramsErr  error
}

type RunnerOptions struct {
	RcloneBinary string
	// StagingRoot must be private to this serial runner, never shared by pods.
	StagingRoot                string
	FileConcurrency            int
	OSMDatabase                *pgxpool.Pool
	OSMAutoAddRegions          bool
	CoverageMinTraversalMeters float64
	CoverageBatchSize          int
}

type ingestMode string

const (
	ingestIncremental ingestMode = "incremental"
	ingestBounded     ingestMode = "bounded"
)

type executionResult struct {
	ingest *ingestResults
}

type connectionResult struct {
	status             string
	code               *string
	summary            *string
	refreshedEncrypted []byte
}

func NewRunner(db *pgxpool.Pool, logger *slog.Logger, keys *sourcecrypto.Keyring, localRoots []string) *Runner {
	return NewRunnerWithOptions(db, logger, keys, localRoots, RunnerOptions{})
}

func NewRunnerWithOptions(db *pgxpool.Pool, logger *slog.Logger, keys *sourcecrypto.Keyring, localRoots []string, options RunnerOptions) *Runner {
	if options.FileConcurrency == 0 {
		options.FileConcurrency = 2
	}
	if options.CoverageMinTraversalMeters == 0 {
		options.CoverageMinTraversalMeters = 5
	}
	if options.CoverageBatchSize < 1 || options.CoverageBatchSize > 1000 {
		options.CoverageBatchSize = 25
	}
	if options.RcloneBinary == "" {
		options.RcloneBinary = "rclone"
	}
	if options.StagingRoot == "" {
		options.StagingRoot = "/var/lib/workouts/staging"
	}
	return &Runner{
		db:                         db,
		osmDB:                      options.OSMDatabase,
		osmAutoAddRegions:          options.OSMAutoAddRegions,
		coverageMinTraversalMeters: options.CoverageMinTraversalMeters,
		coverageBatchSize:          options.CoverageBatchSize,
		logger:                     logger,
		keys:                       keys,
		localRoots:                 append([]string(nil), localRoots...),
		rcloneBinary:               options.RcloneBinary,
		stagingRoot:                options.StagingRoot,
		workerID:                   "source-worker-" + uuid.NewString(),
		pollInterval:               defaultPollInterval,
		leaseDuration:              defaultLease,
		heartbeatInterval:          defaultHeartbeat,
		fileSlots:                  make(chan struct{}, options.FileConcurrency),
	}
}

func ConfigureFileSlotLimits(ctx context.Context, db *pgxpool.Pool, accountLimit, globalLimit int) error {
	var configured bool
	if err := db.QueryRow(ctx, `SELECT app.configure_ingest_file_slot_limits($1,$2)`, accountLimit, globalLimit).Scan(&configured); err != nil {
		return fmt.Errorf("configure ingest file slot limits: %w", err)
	}
	if !configured {
		return errors.New("configure ingest file slot limits: active slots use different limits")
	}
	return nil
}

func ConfigureCoverageBatchSize(ctx context.Context, db *pgxpool.Pool, size int) error {
	if size < 1 || size > 1000 {
		return errors.New("coverage batch size must be between 1 and 1000")
	}
	var configured bool
	if err := db.QueryRow(ctx, `SELECT app.configure_coverage_batch_size($1,$2)`, database.SupportedSchemaVersion, size).Scan(&configured); err != nil {
		return fmt.Errorf("configure coverage batch size: %w", err)
	}
	if !configured {
		return errors.New("coverage batch size configuration rejected")
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	for {
		worked, err := r.RunOnce(ctx)
		r.logCycleFailure(ctx, err)
		if ctx.Err() != nil {
			return nil
		}
		if worked {
			continue
		}
		timer := time.NewTimer(r.pollInterval)
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

func (r *Runner) logCycleFailure(ctx context.Context, err error) {
	var logged loggedExecutionError
	if err != nil && !errors.Is(err, context.Canceled) && !errors.As(err, &logged) {
		r.logger.WarnContext(ctx, "worker cycle failed", "error", err)
	}
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	if err := r.recoverICloudStaging(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	job, err := r.claimNext(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r.loadJobLogContext(ctx, &job)
	return true, r.executeLogged(ctx, job, r.execute)
}

// StagingCleanupPending is safe to read from the runtime readiness handler.
func (r *Runner) StagingCleanupPending() bool { return r.stagingDirty.Load() }

func (r *Runner) loadJobLogContext(ctx context.Context, job *claimedJob) {
	var sourceName, sourceType *string
	err := r.db.QueryRow(ctx, `SELECT owner_username,source_name,source_type
		FROM app.read_worker_job_log_context($1,$2,$3)`, job.id, r.workerID, job.lease).
		Scan(&job.owner, &sourceName, &sourceType)
	if err == nil {
		if sourceName != nil {
			job.sourceName = *sourceName
		}
		if sourceType != nil {
			job.sourceType = *sourceType
		}
		return
	}
	job.owner = "unknown owner"
	if isSourceJob(job.kind) {
		job.sourceName, job.sourceType = "unknown source", "unknown"
	}
	r.logger.WarnContext(ctx, "job logging context unavailable",
		"job_id", job.id, "job_type", job.kind, "error", err)
}

func (r *Runner) executeLogged(ctx context.Context, job claimedJob, execute func(context.Context, claimedJob) (executionResult, error)) error {
	attributes := jobLogAttributes(job)
	r.logger.InfoContext(ctx, "job processing started", attributes...)
	started := time.Now()
	result, executionErr := execute(ctx, job)
	terminal := append(attributes, "duration_ms", time.Since(started).Milliseconds())
	outcomeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	readOutcome := r.readJobOutcome
	if r.outcomeReader != nil {
		readOutcome = r.outcomeReader
	}
	outcome, outcomeErr := readOutcome(outcomeCtx, job)
	if outcomeErr != nil {
		terminal = append(terminal, "error", outcomeErr)
		if executionErr != nil {
			terminal = append(terminal, "execution_error", executionErr)
		}
		r.logger.WarnContext(ctx, "job processing outcome unavailable", terminal...)
	} else {
		terminal = append(terminal, "job_status", outcome.status)
		if result.ingest != nil && (outcome.status == "succeeded" || outcome.status == "partially_succeeded" ||
			((outcome.status == "failed" || outcome.status == "cancelled") && result.ingest.hasPartialData())) {
			terminal = append(terminal, "results", result.ingest)
		}
		switch outcome.status {
		case "succeeded", "partially_succeeded":
			if executionErr != nil {
				terminal = append(terminal, "error", executionErr)
			}
			r.logger.InfoContext(ctx, "job processing completed", terminal...)
		case "failed":
			failureErr := errors.New("job failed")
			if outcome.failureCode != nil {
				failureErr = fmt.Errorf("job failed: %s", *outcome.failureCode)
				terminal = append(terminal, "failure_code", *outcome.failureCode)
			}
			if executionErr != nil {
				failureErr = executionErr
			}
			terminal = append(terminal, "error", failureErr)
			r.logger.ErrorContext(ctx, "job processing failed", terminal...)
		case "cancelled":
			if executionErr != nil {
				terminal = append(terminal, "error", executionErr)
			}
			r.logger.InfoContext(ctx, "job processing cancelled", terminal...)
		default:
			if executionErr != nil {
				terminal = append(terminal, "error", executionErr)
			}
			r.logger.WarnContext(ctx, "job processing outcome unavailable", terminal...)
		}
	}
	if executionErr != nil {
		return loggedExecutionError{err: executionErr}
	}
	return nil
}

type jobOutcome struct {
	status      string
	failureCode *string
}

func (r *Runner) readJobOutcome(ctx context.Context, job claimedJob) (jobOutcome, error) {
	if r.db == nil {
		return jobOutcome{}, errors.New("job outcome reader unavailable")
	}
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return jobOutcome{}, err
	}
	defer tx.Rollback(ctx)
	var outcome jobOutcome
	err = tx.QueryRow(ctx, `SELECT status,failure_code FROM app.jobs WHERE id=$1`, job.id).
		Scan(&outcome.status, &outcome.failureCode)
	return outcome, err
}

func jobLogAttributes(job claimedJob) []any {
	attributes := []any{"job_id", job.id, "job_type", job.kind, "owner", job.owner}
	if isSourceJob(job.kind) {
		attributes = append(attributes,
			"source_name", job.sourceName, "source_type", job.sourceType)
	}
	return attributes
}

func isSourceJob(kind string) bool {
	return kind == "source_connection_check" || kind == "manual_ingest_source" || kind == "scheduled_ingest_source"
}

func (r *Runner) claimNext(ctx context.Context) (claimedJob, error) {
	lease := uuid.New()
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return claimedJob{}, err
	}
	defer tx.Rollback(ctx)
	var job claimedJob
	err = tx.QueryRow(ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_workout_deletion($1,$2,$3,$4)`, r.workerID, lease, r.leaseDuration,
		database.SupportedSchemaVersion).Scan(&job.id, &job.accountID, &job.kind)
	if err == nil {
		job.lease = lease
		if err := tx.Commit(ctx); err != nil {
			return claimedJob{}, err
		}
		return job, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return claimedJob{}, err
	}
	err = tx.QueryRow(ctx, `SELECT job_id,account_id,kind
		FROM app.claim_next_worker_job($1,$2,$3,$4)`, r.workerID, lease, r.leaseDuration,
		database.SupportedSchemaVersion).Scan(&job.id, &job.accountID, &job.kind)
	if err != nil {
		return claimedJob{}, err
	}
	job.lease = lease
	if job.kind == "manual_ingest_source" || job.kind == "scheduled_ingest_source" {
		var parameters []byte
		if err := tx.QueryRow(ctx, `SELECT parameters FROM app.jobs WHERE id=$1 AND account_id=$2`, job.id, job.accountID).Scan(&parameters); err != nil {
			return claimedJob{}, err
		}
		job.mode, job.startDate, job.endDate, job.paramsErr = decodeIngestParameters(parameters)
	}
	if err := tx.Commit(ctx); err != nil {
		return claimedJob{}, err
	}
	return job, nil
}

func decodeIngestParameters(raw []byte) (ingestMode, *time.Time, *time.Time, error) {
	var value struct {
		SourceID      string  `json:"sourceId"`
		Generation    int64   `json:"generation"`
		Mode          string  `json:"mode"`
		StartDate     *string `json:"startDate"`
		EndDate       *string `json:"endDate"`
		LegacySchema6 *bool   `json:"legacySchema6"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		len(value.SourceID) != 32 || value.Generation < 1 {
		return "", nil, nil, errors.New("invalid ingest parameters")
	}
	for _, character := range value.SourceID {
		if !(character >= '0' && character <= '9') && !(character >= 'A' && character <= 'F') {
			return "", nil, nil, errors.New("invalid ingest parameters")
		}
	}
	if value.Mode == string(ingestIncremental) && value.StartDate == nil && value.EndDate == nil && value.LegacySchema6 == nil {
		return ingestIncremental, nil, nil, nil
	}
	if value.Mode != string(ingestBounded) || value.StartDate == nil || value.EndDate == nil ||
		(value.LegacySchema6 != nil && (!*value.LegacySchema6 || *value.StartDate != "0001-01-01" || *value.EndDate != "9999-12-31")) {
		return "", nil, nil, errors.New("invalid ingest parameters")
	}
	start, startErr := time.Parse(time.DateOnly, *value.StartDate)
	end, endErr := time.Parse(time.DateOnly, *value.EndDate)
	if startErr != nil || endErr != nil || end.Before(start) {
		return "", nil, nil, errors.New("invalid ingest parameters")
	}
	return ingestBounded, &start, &end, nil
}

func (r *Runner) execute(ctx context.Context, job claimedJob) (executionResult, error) {
	switch job.kind {
	case "manual_ingest_source", "scheduled_ingest_source":
		return r.executeIngest(ctx, job)
	case "source_connection_check":
		return executionResult{}, r.executeConnectionCheck(ctx, job)
	case "workout_deletion":
		return executionResult{}, r.executeWorkoutDeletion(ctx, job)
	default:
		return executionResult{}, fmt.Errorf("unsupported claimed job kind %q", job.kind)
	}
}

func (r *Runner) executeWorkoutDeletion(ctx context.Context, job claimedJob) error {
	opCtx, cancelOperation := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go r.runHeartbeat(opCtx, cancelOperation, job, heartbeatDone)
	purgeErr := r.purgeWorkoutDeletion(opCtx, job)
	cancelOperation()
	heartbeatErr := <-heartbeatDone
	if errors.Is(purgeErr, errLeaseLost) || errors.Is(heartbeatErr, errLeaseLost) {
		return errJobInterrupted
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
		return heartbeatErr
	}
	if ctx.Err() != nil || errors.Is(purgeErr, context.Canceled) {
		return errJobInterrupted
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if purgeErr != nil {
		failureCode, failureSummary := "workout-delete-failed", "The workout could not be deleted."
		_, finishErr := r.finishWorkoutDeletion(cleanupCtx, job, "failed", &failureCode, &failureSummary)
		if finishErr != nil {
			return finishErr
		}
		return purgeErr
	}
	finished, err := r.finishWorkoutDeletion(cleanupCtx, job, "succeeded", nil, nil)
	if err != nil {
		return err
	}
	if !finished {
		return errJobInterrupted
	}
	return nil
}

func (r *Runner) purgeWorkoutDeletion(ctx context.Context, job claimedJob) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var fenced int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM app.fence_workout_deletion($1,$2,$3)`, job.id, r.workerID, job.lease).Scan(&fenced); err != nil {
		return err
	}
	if fenced == 0 {
		return errLeaseLost
	}
	var targetsCompleted, totalCompleted int
	if err = tx.QueryRow(ctx, `SELECT targets_completed,total_completed FROM app.purge_workout_deletion($1,$2,$3)`, job.id, r.workerID, job.lease).Scan(&targetsCompleted, &totalCompleted); errors.Is(err, pgx.ErrNoRows) {
		return errLeaseLost
	} else if err != nil {
		return err
	}
	if totalCompleted != fenced || targetsCompleted < 0 {
		return errLeaseLost
	}
	var reconciled int
	if err = tx.QueryRow(ctx, `SELECT app.reconcile_account_workout_timezones($1,$2,$3,$4)`,
		job.accountID, job.id, r.workerID, job.lease).Scan(&reconciled); err != nil {
		return fmt.Errorf("reconcile workout timezones after deletion: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Runner) finishWorkoutDeletion(ctx context.Context, job claimedJob, status string, code, summary *string) (bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var finished bool
	if err = tx.QueryRow(ctx, `SELECT app.finish_job($1,$2,$3,$4,$5,$6)`, job.id, r.workerID, job.lease, status, code, summary).Scan(&finished); err != nil {
		return false, err
	}
	return finished, tx.Commit(ctx)
}

func (r *Runner) executeConnectionCheck(ctx context.Context, job claimedJob) error {
	opCtx, cancelOperation := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go r.runHeartbeat(opCtx, cancelOperation, job, heartbeatDone)

	snapshot, cancellationPending, err := r.readSnapshot(opCtx, job)
	var result connectionResult
	if err == nil && !cancellationPending {
		switch snapshot.sourceType {
		case sourceconfig.LocalType:
			result = checkLocalConnection(opCtx, r.keys, r.localRoots, job.accountID, job.id, snapshot.envelope)
		case sourceconfig.ICloudType:
			result, err = r.checkICloudConnection(opCtx, job, snapshot)
		default:
			result = connectionFailed("source-config-invalid", "Source configuration could not be read.")
		}
	}
	cancelOperation()
	heartbeatErr := <-heartbeatDone

	if errors.Is(err, errPrivateCleanup) {
		return err
	}
	if errors.Is(err, errLeaseLost) || errors.Is(heartbeatErr, errLeaseLost) {
		return errJobInterrupted
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
		if errors.Is(heartbeatErr, errCancellationRequested) {
			_, finishErr := r.finishCancelled(ctx, job)
			return finishErr
		}
		return heartbeatErr
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return errJobInterrupted
	}
	if cancellationPending {
		result = connectedResult()
	}
	finished, err := r.complete(ctx, job, result)
	if err == nil && !finished {
		return errJobInterrupted
	}
	return err
}

func (r *Runner) runHeartbeat(ctx context.Context, cancelOperation context.CancelFunc, job claimedJob, done chan<- error) {
	runHeartbeatOperation(cancelOperation, done, func() error { return r.heartbeatLoop(ctx, job) })
}

func runHeartbeatOperation(cancelOperation context.CancelFunc, done chan<- error, heartbeat func() error) {
	err := heartbeat()
	if err != nil && !errors.Is(err, context.Canceled) {
		cancelOperation()
	}
	done <- err
}

type jobSourceSnapshot struct {
	sourceID   uuid.UUID
	generation int64
	sourceType string
	envelope   []byte
}

func (r *Runner) readSnapshot(ctx context.Context, job claimedJob) (jobSourceSnapshot, bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return jobSourceSnapshot{}, false, err
	}
	defer tx.Rollback(ctx)
	var snapshot jobSourceSnapshot
	err = tx.QueryRow(ctx, `SELECT source_id,source_generation,source_type,config_envelope
		FROM app.read_typed_job_source_snapshot($1,$2,$3)`, job.id, r.workerID, job.lease).
		Scan(&snapshot.sourceID, &snapshot.generation, &snapshot.sourceType, &snapshot.envelope)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobSourceSnapshot{}, false, errLeaseLost
	}
	if err != nil {
		return jobSourceSnapshot{}, false, err
	}
	var cancellationPending bool
	if err := tx.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM app.jobs WHERE id=$1`, job.id).Scan(&cancellationPending); err != nil {
		return jobSourceSnapshot{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return jobSourceSnapshot{}, false, err
	}
	return snapshot, cancellationPending, nil
}

func checkLocalConnection(ctx context.Context, keys *sourcecrypto.Keyring, roots []string, accountID, jobID uuid.UUID, envelope []byte) connectionResult {
	if ctx.Err() != nil {
		return connectionFailed("connection-check-cancelled", "Source connection check was cancelled.")
	}
	plaintext, err := keys.Decrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: jobID,
	}, envelope)
	if err != nil {
		return connectionFailed("source-config-invalid", "Source configuration could not be read.")
	}
	config, _, err := sourceconfig.DecodeLocal(plaintext, roots)
	if err != nil {
		return connectionFailed("source-config-invalid", "Source configuration could not be read.")
	}
	if ctx.Err() != nil {
		return connectionFailed("connection-check-cancelled", "Source connection check was cancelled.")
	}
	directory, err := sourceconfig.OpenDirectory(config.Path, roots)
	if err != nil {
		return connectionFailed("source-unavailable", "Source directory is not accessible.")
	}
	if err := directory.Close(); err != nil {
		return connectionFailed("source-unavailable", "Source directory is not accessible.")
	}
	return connectedResult()
}

func connectedResult() connectionResult {
	return connectionResult{status: "connected"}
}

func connectionFailed(code, summary string) connectionResult {
	return connectionResult{status: "connection-failed", code: &code, summary: &summary}
}

func (r *Runner) heartbeatLoop(ctx context.Context, job claimedJob) error {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			alive, err := r.heartbeat(ctx, job)
			if err != nil {
				return err
			}
			if !alive {
				return errLeaseLost
			}
			if isSourceJob(job.kind) {
				cancelled, err := r.observeIngest(ctx, job)
				if err != nil {
					return err
				}
				if cancelled {
					return errCancellationRequested
				}
			}
		}
	}
}

func (r *Runner) heartbeat(ctx context.Context, job claimedJob) (bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var alive bool
	if err := tx.QueryRow(ctx, `SELECT app.heartbeat_job($1,$2,$3,$4)`, job.id, r.workerID, job.lease, r.leaseDuration).Scan(&alive); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return alive, nil
}

func (r *Runner) complete(ctx context.Context, job claimedJob, result connectionResult) (bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var refreshed any
	if result.refreshedEncrypted != nil {
		refreshed = json.RawMessage(result.refreshedEncrypted)
	}
	var finished bool
	if err := tx.QueryRow(ctx, `SELECT app.finish_connection_check_with_refresh($1,$2,$3,$4,$5,$6,$7)`,
		job.id, r.workerID, job.lease, result.status, result.code, result.summary, refreshed).Scan(&finished); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return finished, nil
}

func (r *Runner) finishCancelled(ctx context.Context, job claimedJob) (bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var finished bool
	if err := tx.QueryRow(ctx, `SELECT app.finish_job($1,$2,$3,'cancelled')`, job.id, r.workerID, job.lease).Scan(&finished); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return finished, nil
}

func beginAccount(ctx context.Context, db *pgxpool.Pool, accountID uuid.UUID) (pgx.Tx, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
