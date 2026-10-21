package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/rclonesource"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/erhhung/workouts-explorer/internal/workouttimezone"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/sys/unix"
)

const (
	discoveryBatchSize     = 256
	maxDirectoryEntries    = 100_000
	maxEligibleSourceFiles = 10_000
	maxFailedProcessing    = 100
	maxWorkoutWarnings     = 4096
	maxWorkoutWarningBytes = 262144
)

var (
	exportNamePattern    = regexp.MustCompile(`^HealthAutoExport-[0-9]{4}-[0-9]{2}-[0-9]{2}\.json$`)
	errDiscoveryLimit    = errors.New("source directory limit exceeded")
	errDiscoveryFileOpen = errors.New("source file discovery failed")
)

type ingestFailure struct {
	code    string
	summary string
}

func (f *ingestFailure) Error() string { return f.code + ": " + f.summary }

type ingestResults struct {
	FilesProcessed           int      `json:"files_processed"`
	WorkoutsProcessed        int      `json:"workouts_processed"`
	WorkoutsIngested         int      `json:"workouts_ingested"`
	FailedProcessing         []string `json:"failed_processing"`
	filesDiscovered          int
	filesSkipped             int
	filesSucceeded           int
	filesFailed              int
	workoutsCreated          int
	workoutsUpdated          int
	workoutsUnchanged        int
	workoutsCreatedWithRoute *int
	workoutsUpdatedWithRoute *int
}

func newIngestResults() ingestResults {
	return ingestResults{FailedProcessing: make([]string, 0), workoutsCreatedWithRoute: new(int), workoutsUpdatedWithRoute: new(int)}
}

func (r *ingestResults) recordFailedFile(name string) {
	r.filesFailed++
	r.syncTotals()
	if len(r.FailedProcessing) < maxFailedProcessing {
		r.FailedProcessing = append(r.FailedProcessing, filepath.Base(name))
	}
}

func (r *ingestResults) syncTotals() {
	r.FilesProcessed = r.filesSucceeded + r.filesFailed
	r.WorkoutsProcessed = r.workoutsCreated + r.workoutsUpdated + r.workoutsUnchanged
	r.WorkoutsIngested = r.workoutsCreated + r.workoutsUpdated
}

func (r ingestResults) hasPartialData() bool {
	return r.FilesProcessed != 0 || r.WorkoutsProcessed != 0 || r.WorkoutsIngested != 0 || len(r.FailedProcessing) != 0
}

type persistedFileResult struct {
	created          int
	updated          int
	unchanged        int
	createdWithRoute int
	updatedWithRoute int
}

type sourceFile struct {
	name             string
	size             int64
	modified         time.Time
	observedModified time.Time
	device           uint64
	inode            uint64
	ctimeSec         int64
	ctimeNS          int64
	exportDate       time.Time
	action           string
	recoveredSkip    bool
	remoteObject     *rclonesource.Object
	observedIdentity string
}

type fileCheckpoint struct {
	id    uuid.UUID
	state string
}

type discoveryLimits struct {
	maxEntries int
	maxFiles   int
}

type regularFileOpener func(*os.File, string) (*os.File, os.FileInfo, error)
type sourceFileStat func(int, string, *unix.Stat_t, int) error

type discoveryFileError struct {
	name string
}

func (e *discoveryFileError) Error() string { return errDiscoveryFileOpen.Error() }
func (e *discoveryFileError) Unwrap() error { return errDiscoveryFileOpen }

type parsedSourceFile struct {
	sourceFile
	checksum [sha256.Size]byte
	document healthautoexport.Document
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func (r *Runner) executeIngest(ctx context.Context, job claimedJob) (executionResult, error) {
	opCtx, cancelOperation := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go r.runHeartbeat(opCtx, cancelOperation, job, heartbeatDone)

	results := newIngestResults()
	err := ingestCommitError(r.ingest(opCtx, job, &results))
	execution := executionResult{ingest: &results}
	cancelOperation()
	heartbeatErr := <-heartbeatDone
	if errors.Is(err, errPrivateCleanup) {
		return execution, err
	}
	if errors.Is(err, errLeaseLost) || errors.Is(heartbeatErr, errLeaseLost) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		finished, finishErr := r.finishCancellationPending(cleanupCtx, job)
		if finishErr != nil {
			return execution, finishErr
		}
		if finished {
			return execution, nil
		}
		return execution, errJobInterrupted
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
		if !errors.Is(heartbeatErr, errCancellationRequested) {
			return execution, heartbeatErr
		}
		err = errCancellationRequested
	}
	if ctx.Err() != nil || (errors.Is(err, context.Canceled) && !errors.Is(heartbeatErr, errCancellationRequested)) {
		if cause := context.Cause(ctx); cause != nil {
			return execution, cause
		}
		return execution, errJobInterrupted
	}
	if errors.Is(err, errCancellationRequested) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		finished, finishErr := r.finishCancellationPending(cleanupCtx, job)
		if finishErr != nil {
			return execution, finishErr
		}
		if !finished {
			return execution, errJobInterrupted
		}
		return execution, nil
	}
	if err != nil {
		return execution, err
	}
	return execution, nil
}

func (r *Runner) reconcileAccountTimezones(ctx context.Context, job claimedJob) error {
	var changed int
	if err := r.db.QueryRow(ctx, `SELECT app.reconcile_account_workout_timezones($1,$2,$3,$4)`,
		job.accountID, job.id, r.workerID, job.lease).Scan(&changed); err != nil {
		return fmt.Errorf("reconcile workout timezones: %w", err)
	}
	return nil
}

func (r *Runner) finishCancellationPending(ctx context.Context, job claimedJob) (bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM app.jobs
		WHERE id=$1 AND status='running' AND worker_id=$2 AND lease_token=$3 AND lease_expires_at >= clock_timestamp()`,
		job.id, r.workerID, job.lease).Scan(&pending)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return false, nil
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}
	return r.finishCancelled(ctx, job)
}

func (r *Runner) ingest(ctx context.Context, job claimedJob, results *ingestResults) (err error) {
	if job.paramsErr != nil {
		return r.failIngest(ctx, job, ingestFailure{"ingest-parameters-invalid", "Ingest parameters were invalid."})
	}
	snapshot, cancellationPending, err := r.readSnapshot(ctx, job)
	if err != nil {
		return err
	}
	if cancellationPending {
		_, err = r.finishCancelled(ctx, job)
		return err
	}
	// A prior lease may have committed a file before transport cleanup or progress.
	// Staging recovery precedes claims; replay durable work before touching source I/O.
	if err := r.drainIngestCoverage(ctx, job, false); err != nil {
		return err
	}
	r.recordDiagnosticsBestEffort(ctx, job, "ingest-started", map[string]any{"operation": "discover"})
	plaintext, err := r.keys.Decrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: job.accountID, RecordID: job.id,
	}, snapshot.envelope)
	if err != nil {
		return r.failIngest(ctx, job, ingestFailure{"source-config-invalid", "Source configuration could not be read."})
	}
	canonical, err := sourceconfig.Canonicalize(snapshot.sourceType, plaintext, r.localRoots)
	if err != nil || !bytes.Equal(plaintext, canonical) {
		return r.failIngest(ctx, job, ingestFailure{"source-config-invalid", "Source configuration could not be read."})
	}
	var files []sourceFile
	var sourcePath string
	var remote *rclonesource.Source
	var original sourceconfig.ICloud
	if snapshot.sourceType == sourceconfig.LocalType {
		config, _, _ := sourceconfig.DecodeLocal(plaintext, r.localRoots)
		sourcePath = config.Path
		directory, openErr := sourceconfig.OpenDirectory(config.Path, r.localRoots)
		if openErr != nil {
			return r.failIngest(ctx, job, ingestFailure{"source-unavailable", "Source directory is not accessible."})
		}
		files, err = discoverSourceFiles(directory)
		_ = directory.Close()
		if err != nil {
			recordDiscoveryFailure(results, err)
			failure := ingestFailure{"source-unavailable", "Source directory could not be read."}
			if errors.Is(err, errDiscoveryLimit) {
				failure = ingestFailure{"source-directory-limit", "Source directory contains too many entries."}
			}
			return r.failIngest(ctx, job, failure)
		}
	} else {
		original, _, _ = sourceconfig.DecodeICloud(plaintext)
		remote, err = rclonesource.New(ctx, r.rcloneBinary, r.stagingRoot, original)
		if err != nil {
			err = r.icloudConstructorError(ctx, remote, err)
			if errors.Is(err, errPrivateCleanup) || ctx.Err() != nil {
				return err
			}
			remote = nil
		} else {
			defer func() {
				if r.closeICloudSource(remote) != nil {
					err = privateCleanupError(ctx)
				}
			}()
			var objects []rclonesource.Object
			objects, err = remote.List(ctx)
			files = remoteSourceFiles(objects)
		}
		if err != nil && ctx.Err() == nil {
			failure := remoteFailure(err)
			err = &failure
		}
	}
	if err == nil {
		err = r.ingestFiles(ctx, job, snapshot.sourceID, sourcePath, remote, files, results)
	}
	if remote != nil {
		updated := remote.Config()
		if r.closeICloudSource(remote) != nil {
			return privateCleanupError(ctx)
		}
		if ctx.Err() == nil && !errors.Is(err, errPrivateCleanup) && !isRemoteAuthFailure(err) {
			if refreshErr := r.refreshICloudCredentials(ctx, job, snapshot, original, updated); refreshErr != nil {
				return refreshErr
			}
		}
	}
	if errors.Is(err, errPrivateCleanup) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *ingestFailure
	if errors.As(err, &failure) {
		if isRemoteAuthFailure(err) {
			if failErr := r.failSourceConnection(ctx, job, snapshot, *failure); failErr != nil {
				return failErr
			}
		}
		return r.failIngest(ctx, job, *failure)
	}
	if err != nil {
		return err
	}
	status := "succeeded"
	if results.filesFailed > 0 {
		status = "failed"
		failure = &ingestFailure{"source-file-invalid", "One or more source files could not be processed."}
	}
	finished, err := r.finishJob(ctx, job, status, failure)
	if err != nil {
		return err
	}
	if !finished {
		if cancelled, cancelErr := r.finishCancelled(ctx, job); cancelErr != nil {
			return cancelErr
		} else if cancelled {
			return nil
		}
		return errLeaseLost
	}
	return nil
}

// No terminal writes here: the caller must remove private transport files and
// persist final cookies before clearing the immutable job snapshot.
func (r *Runner) ingestFiles(ctx context.Context, job claimedJob, sourceID uuid.UUID, sourcePath string, remote *rclonesource.Source, files []sourceFile, results *ingestResults) error {
	files = filterSourceFiles(files, job.startDate, job.endDate)
	if err := r.resolveCandidateActions(ctx, job, sourceID, files); err != nil {
		return err
	}
	manifestState, err := r.recordFileManifest(ctx, job, files)
	if err != nil {
		return err
	}
	if manifestState == "mismatch" {
		return &ingestFailure{"source-files-changed", "Source files changed while ingest was recovering."}
	}
	if err := r.loadIngestProgress(ctx, job, results); err != nil {
		return err
	}
	if manifestState == "created" {
		if results.filesDiscovered != 0 || results.filesSkipped != 0 || results.filesSucceeded != 0 || results.filesFailed != 0 {
			return &ingestFailure{"source-files-changed", "Source files changed while ingest was recovering."}
		}
	}
	if results.filesDiscovered == 0 {
		results.filesDiscovered = len(files)
	}
	markRecoveredSkips(files, results.filesSkipped)
	if results.filesDiscovered != len(files) {
		return &ingestFailure{"source-files-changed", "Source files changed while ingest was recovering."}
	}
	if err := r.recordIngestProgress(ctx, job, *results); err != nil {
		return err
	}
	r.recordDiagnosticsBestEffort(ctx, job, "ingest-progress", map[string]any{"current": 0, "total": len(files)})

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome := r.processSourceFile(ctx, job, sourceID, sourcePath, file, remote)
		if outcome.fatal != nil {
			return outcome.fatal
		}
		// processSourceFile has returned, including transport and file-slot cleanup.
		if err := r.drainIngestCoverage(ctx, job, false); err != nil {
			return err
		}
		if outcome.alreadyCounted {
			continue
		}
		results.applyFileOutcome(file.name, outcome)
		if err := r.recordIngestProgress(ctx, job, *results); err != nil {
			return err
		}
	}
	// EOF does not make this source job or its root terminal. Only account
	// recovery, after the terminal commit, may seal an underfull cohort.
	if err := r.drainIngestCoverage(ctx, job, false); err != nil {
		return err
	}
	if err := r.repairCoverageReadiness(ctx, job); err != nil {
		return fmt.Errorf("repair workout coverage readiness: %w", err)
	}
	if err := r.reconcileAccountTimezones(ctx, job); err != nil {
		return err
	}
	return nil
}

type fileOutcome struct {
	skipped, succeeded, failed, alreadyCounted bool
	workouts                                   persistedFileResult
	fatal                                      error
}

func (r *ingestResults) applyFileOutcome(name string, outcome fileOutcome) {
	if outcome.skipped {
		r.filesSkipped++
	}
	if outcome.succeeded {
		r.filesSucceeded++
	}
	if outcome.failed {
		r.filesFailed++
		if len(r.FailedProcessing) < maxFailedProcessing {
			r.FailedProcessing = append(r.FailedProcessing, filepath.Base(name))
		}
	}
	r.workoutsCreated += outcome.workouts.created
	r.workoutsUpdated += outcome.workouts.updated
	r.workoutsUnchanged += outcome.workouts.unchanged
	if r.workoutsCreatedWithRoute != nil {
		*r.workoutsCreatedWithRoute += outcome.workouts.createdWithRoute
	}
	if r.workoutsUpdatedWithRoute != nil {
		*r.workoutsUpdatedWithRoute += outcome.workouts.updatedWithRoute
	}
	r.syncTotals()
}

func filterSourceFiles(files []sourceFile, start, end *time.Time) []sourceFile {
	if start == nil || end == nil {
		return files
	}
	return slices.DeleteFunc(files, func(file sourceFile) bool { return file.exportDate.Before(*start) || file.exportDate.After(*end) })
}

func (r *Runner) processSourceFile(ctx context.Context, job claimedJob, sourceID uuid.UUID, sourcePath string, file sourceFile, remote *rclonesource.Source) (outcome fileOutcome) {
	if file.action == "skip" {
		if file.recoveredSkip {
			return fileOutcome{alreadyCounted: true}
		}
		return fileOutcome{skipped: true}
	}
	if r.beforeProcessFile != nil {
		r.beforeProcessFile(file)
	}
	checkpoint, skip, checkpointFailure, err := r.checkpointSourceFile(ctx, job, file)
	if err != nil {
		return fileOutcome{fatal: err}
	}
	if skip {
		return fileOutcome{alreadyCounted: true}
	}
	if checkpoint.state == "failed" {
		return fileOutcome{alreadyCounted: true}
	}
	if checkpointFailure != nil {
		if checkpoint.id != uuid.Nil && (checkpoint.state == "discovered" || checkpoint.state == "processing") {
			if err := r.persistFailedFile(ctx, job, checkpoint, *checkpointFailure); err != nil {
				return fileOutcome{fatal: err}
			}
		}
		return fileOutcome{failed: true}
	}
	select {
	case r.fileSlots <- struct{}{}:
	case <-ctx.Done():
		return fileOutcome{fatal: ctx.Err()}
	}
	defer func() { <-r.fileSlots }()
	slot, err := r.acquireFileSlot(ctx, job)
	if err != nil {
		return fileOutcome{fatal: err}
	}
	releaseSlot := true
	defer func() {
		if releaseSlot {
			r.releaseFileSlot(job, slot)
		}
	}()
	var parsed parsedSourceFile
	var failure *ingestFailure
	if remote != nil {
		// Installed before Download: even failed/cancelled downloads may retain
		// plaintext. Confirm pinned-root cleanup before releasing the DB slot.
		defer func() {
			if remote.DiscardDownload() != nil {
				r.stagingDirty.Store(true)
				releaseSlot = r.closeICloudSource(remote) == nil
				outcome = fileOutcome{fatal: privateCleanupError(ctx)}
			}
		}()
		path, downloadErr := remote.Download(ctx, *file.remoteObject)
		if downloadErr != nil {
			if remote.DiscardDownload() != nil {
				r.stagingDirty.Store(true)
				releaseSlot = r.closeICloudSource(remote) == nil
				return fileOutcome{fatal: privateCleanupError(ctx)}
			}
			var safe *rclonesource.Error
			if errors.As(downloadErr, &safe) && safe.Code == "source-cleanup-failed" {
				r.stagingDirty.Store(true)
				return fileOutcome{fatal: privateCleanupError(ctx)}
			}
			if ctx.Err() != nil {
				return fileOutcome{fatal: ctx.Err()}
			}
			if errors.Is(downloadErr, context.Canceled) || errors.Is(downloadErr, context.DeadlineExceeded) {
				return fileOutcome{fatal: downloadErr}
			}
			failure := remoteFailure(downloadErr)
			switch failure.code {
			case "source-file-unavailable", "source-file-mutated", "source-file-invalid":
				if err := r.persistFailedFile(ctx, job, checkpoint, failure); err != nil {
					return fileOutcome{fatal: err}
				}
				return fileOutcome{failed: true}
			}
			return fileOutcome{fatal: &failure}
		}
		parsed, failure = readStagedSourceFile(ctx, path, file)
	} else {
		directory, err := sourceconfig.OpenDirectory(sourcePath, r.localRoots)
		if err != nil {
			failure := ingestFailure{"source-unavailable", "Source directory is not accessible."}
			if persistErr := r.persistFailedFile(ctx, job, checkpoint, failure); persistErr != nil {
				return fileOutcome{fatal: persistErr}
			}
			return fileOutcome{failed: true}
		}
		parsed, failure = readSourceFile(ctx, directory, file)
		_ = directory.Close()
	}
	if ctx.Err() != nil {
		return fileOutcome{fatal: ctx.Err()}
	}
	if failure != nil {
		if err := r.persistFailedFile(ctx, job, checkpoint, *failure); err != nil {
			return fileOutcome{fatal: err}
		}
		return fileOutcome{failed: true}
	}
	result, err := r.persistSourceFile(ctx, job, checkpoint, parsed)
	if err != nil {
		return fileOutcome{fatal: err}
	}
	return fileOutcome{succeeded: true, workouts: result}
}

func markRecoveredSkips(files []sourceFile, recorded int) {
	for index := range files {
		if recorded == 0 {
			return
		}
		if files[index].action == "skip" {
			files[index].recoveredSkip = true
			recorded--
		}
	}
}

func (r *Runner) resolveCandidateActions(ctx context.Context, job claimedJob, _ uuid.UUID, files []sourceFile) error {
	for index := range files {
		files[index].action = "process"
	}
	encoded, err := encodeManifestCandidates(files)
	if err != nil {
		return err
	}
	tx, _, err := r.beginIngestWrite(ctx, job)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT relative_name,action FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`,
		job.id, r.workerID, job.lease, string(encoded))
	if err != nil {
		return err
	}
	actions := make(map[string]string, len(files))
	for rows.Next() {
		var name, action string
		if err := rows.Scan(&name, &action); err != nil {
			rows.Close()
			return err
		}
		if action != "process" && action != "skip" {
			rows.Close()
			return errors.New("invalid ingest candidate action")
		}
		actions[name] = action
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range files {
		action, ok := actions[files[index].name]
		if !ok {
			return errLeaseLost
		}
		files[index].action = action
	}
	return tx.Commit(ctx)
}

type manifestCandidate struct {
	Name             string `json:"name"`
	ExportDate       string `json:"export_date"`
	Size             int64  `json:"size"`
	ModifiedSec      int64  `json:"modified_sec"`
	ModifiedNS       int64  `json:"modified_ns"`
	Device           uint64 `json:"device"`
	Inode            uint64 `json:"inode"`
	CTimeSec         int64  `json:"ctime_sec"`
	CTimeNS          int64  `json:"ctime_ns"`
	Action           string `json:"action"`
	ObservedIdentity string `json:"observed_identity,omitempty"`
}

func encodeManifestCandidates(files []sourceFile) ([]byte, error) {
	manifest := make([]manifestCandidate, len(files))
	for index, file := range files {
		manifest[index] = manifestCandidate{
			Name: file.name, ExportDate: file.exportDate.Format(time.DateOnly), Size: file.size,
			ModifiedSec: file.observedModified.Unix(), ModifiedNS: int64(file.observedModified.Nanosecond()),
			Device: file.device, Inode: file.inode, CTimeSec: file.ctimeSec, CTimeNS: file.ctimeNS, Action: file.action,
			ObservedIdentity: file.observedIdentity,
		}
	}
	return json.Marshal(manifest)
}

func (r *Runner) recordFileManifest(ctx context.Context, job claimedJob, files []sourceFile) (string, error) {
	encoded, err := encodeManifestCandidates(files)
	if err != nil {
		return "", err
	}
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var state *string
	if err := tx.QueryRow(ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`,
		job.id, r.workerID, job.lease, string(encoded)).Scan(&state); err != nil {
		return "", err
	}
	if state == nil {
		return "", errLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return *state, nil
}

func sourceIdentity(file sourceFile) string {
	if file.observedIdentity != "" {
		return file.observedIdentity
	}
	return fmt.Sprintf("v1:%d:%d:%d:%d:%d:%d:%d", file.size, file.observedModified.Unix(), file.observedModified.Nanosecond(), file.device, file.inode, file.ctimeSec, file.ctimeNS)
}

func (r *Runner) acquireFileSlot(ctx context.Context, job claimedJob) (uuid.UUID, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		tx, err := beginAccount(ctx, r.db, job.accountID)
		if err != nil {
			return uuid.Nil, err
		}
		var token *uuid.UUID
		err = tx.QueryRow(ctx, `SELECT app.acquire_ingest_file_slot($1,$2,$3)`, job.id, r.workerID, job.lease).Scan(&token)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return uuid.Nil, err
		}
		if token != nil {
			return *token, nil
		}
		cancelled, err := r.observeIngest(ctx, job)
		if err != nil {
			return uuid.Nil, err
		}
		if cancelled {
			return uuid.Nil, errCancellationRequested
		}
		select {
		case <-ctx.Done():
			return uuid.Nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runner) releaseFileSlot(job claimedJob, token uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT app.release_ingest_file_slot($1,$2,$3,$4)`, job.id, r.workerID, job.lease, token); err == nil {
		_ = tx.Commit(ctx)
	}
}

func (r *Runner) loadIngestProgress(ctx context.Context, job claimedJob, results *ingestResults) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT files_discovered,files_skipped,files_succeeded,files_failed,
		workouts_created,workouts_updated,workouts_unchanged FROM app.job_progress WHERE job_id=$1`, job.id).Scan(
		&results.filesDiscovered, &results.filesSkipped, &results.filesSucceeded, &results.filesFailed,
		&results.workoutsCreated, &results.workoutsUpdated, &results.workoutsUnchanged)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var succeeded, failed, created, updated, unchanged int
	var knownCreated, knownUpdated, routedCreated, routedUpdated int
	if err := tx.QueryRow(ctx, `SELECT
		count(DISTINCT file.id) FILTER (WHERE file.state='succeeded'),
		count(DISTINCT file.id) FILTER (WHERE file.state='failed'),
		count(event.id) FILTER (WHERE event.kind='created'),
		count(event.id) FILTER (WHERE event.kind IN ('updated','adopted')),
		count(event.id) FILTER (WHERE event.kind IN ('matched_unchanged','skipped_live_owner')),
		count(event.id) FILTER (WHERE event.kind='created' AND event.has_route IS NOT NULL),
		count(event.id) FILTER (WHERE event.kind IN ('updated','adopted') AND event.has_route IS NOT NULL),
		count(event.id) FILTER (WHERE event.kind='created' AND event.has_route),
		count(event.id) FILTER (WHERE event.kind IN ('updated','adopted') AND event.has_route)
		FROM app.source_files file LEFT JOIN app.workout_import_events event ON event.source_file_id=file.id
		WHERE file.job_id=$1`, job.id).Scan(&succeeded, &failed, &created, &updated, &unchanged,
		&knownCreated, &knownUpdated, &routedCreated, &routedUpdated); err != nil {
		return err
	}
	results.filesSucceeded = max(results.filesSucceeded, succeeded)
	results.filesFailed = max(results.filesFailed, failed)
	results.workoutsCreated = max(results.workoutsCreated, created)
	results.workoutsUpdated = max(results.workoutsUpdated, updated)
	results.workoutsUnchanged = max(results.workoutsUnchanged, unchanged)
	// Retained totals can outlive their events; gaps are unknown, not zero routes.
	results.workoutsCreatedWithRoute, results.workoutsUpdatedWithRoute = nil, nil
	if results.workoutsCreated == knownCreated {
		results.workoutsCreatedWithRoute = &routedCreated
	}
	if results.workoutsUpdated == knownUpdated {
		results.workoutsUpdatedWithRoute = &routedUpdated
	}
	if results.filesFailed > 0 && len(results.FailedProcessing) == 0 {
		rows, err := tx.Query(ctx, `SELECT relative_name FROM app.source_files
			WHERE job_id=$1 AND state='failed' ORDER BY relative_name LIMIT $2`, job.id, maxFailedProcessing)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			results.FailedProcessing = append(results.FailedProcessing, filepath.Base(name))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	results.syncTotals()
	return tx.Commit(ctx)
}

func (r *Runner) recordIngestProgress(ctx context.Context, job claimedJob, results ingestResults) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var recorded bool
	err = tx.QueryRow(ctx, `SELECT app.record_ingest_progress($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,$11,$12)`,
		job.id, r.workerID, job.lease, results.filesDiscovered, results.filesSkipped, results.filesSucceeded,
		results.filesFailed, results.workoutsCreated, results.workoutsUpdated, results.workoutsUnchanged,
		results.workoutsCreatedWithRoute, results.workoutsUpdatedWithRoute).Scan(&recorded)
	if err != nil {
		return err
	}
	if !recorded {
		return errLeaseLost
	}
	return tx.Commit(ctx)
}

func recordDiscoveryFailure(results *ingestResults, err error) {
	var fileError *discoveryFileError
	if errors.As(err, &fileError) {
		results.recordFailedFile(fileError.name)
	}
}

func discoverSourceFiles(directory *os.File) ([]sourceFile, error) {
	return discoverSourceFilesWith(directory, discoveryLimits{
		maxEntries: maxDirectoryEntries,
		maxFiles:   maxEligibleSourceFiles,
	}, unix.Fstatat, sourceconfig.OpenRegularFile)
}

func discoverSourceFilesWith(directory *os.File, limits discoveryLimits, statFile sourceFileStat, openRegularFile regularFileOpener) ([]sourceFile, error) {
	_ = openRegularFile
	files := make([]sourceFile, 0)
	entryCount := 0
	for {
		entries, readErr := directory.ReadDir(discoveryBatchSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		entryCount += len(entries)
		if entryCount > limits.maxEntries {
			return nil, errDiscoveryLimit
		}
		for _, entry := range entries {
			if !exportNamePattern.MatchString(entry.Name()) {
				continue
			}
			exportDate, err := time.Parse(time.DateOnly, entry.Name()[len("HealthAutoExport-"):len("HealthAutoExport-YYYY-MM-DD")])
			if err != nil {
				continue
			}
			var stat unix.Stat_t
			if err := statFile(int(directory.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return nil, &discoveryFileError{name: entry.Name()}
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFLNK:
				continue
			case unix.S_IFREG:
			default:
				continue
			}
			modified := time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
			files = append(files, sourceFile{
				name: entry.Name(), size: stat.Size, modified: databaseTimestamp(modified), observedModified: modified,
				device: uint64(stat.Dev), inode: stat.Ino, ctimeSec: stat.Ctim.Sec, ctimeNS: stat.Ctim.Nsec, exportDate: exportDate,
			})
			if len(files) > limits.maxFiles {
				return nil, errDiscoveryLimit
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	slices.SortFunc(files, func(left, right sourceFile) int {
		if order := left.exportDate.Compare(right.exportDate); order != 0 {
			return order
		}
		return bytes.Compare([]byte(left.name), []byte(right.name))
	})
	return files, nil
}

func databaseTimestamp(value time.Time) time.Time { return value.Truncate(time.Microsecond) }

func readSourceFile(ctx context.Context, directory *os.File, discovered sourceFile) (parsedSourceFile, *ingestFailure) {
	file, initial, err := sourceconfig.OpenRegularFile(directory, discovered.name)
	if err != nil {
		return parsedSourceFile{}, &ingestFailure{"source-file-unavailable", "A source file could not be read."}
	}
	defer file.Close()
	if !sameFileVersion(discovered, initial) {
		return parsedSourceFile{}, &ingestFailure{"source-file-mutated", "A source file changed while it was being read."}
	}
	digest := sha256.New()
	document, err := healthautoexport.Parse(io.TeeReader(contextReader{ctx: ctx, reader: file}, digest))
	if err != nil {
		final, statErr := file.Stat()
		if statErr != nil || !sameFileVersion(discovered, final) || final.Size() != initial.Size() {
			return parsedSourceFile{}, &ingestFailure{"source-file-mutated", "A source file changed while it was being read."}
		}
		code := "source-file-invalid"
		summary := "The file could not be parsed."
		var parsed *healthautoexport.ParseError
		if errors.As(err, &parsed) {
			if parsed.Code == healthautoexport.ErrorReadFailure {
				code = "source-file-unavailable"
				summary = "The file could not be read completely."
			} else {
				summary = parseFailureSummary(parsed)
			}
		}
		return parsedSourceFile{}, &ingestFailure{code, summary}
	}
	final, err := file.Stat()
	if err != nil || !sameFileVersion(discovered, final) || final.Size() != initial.Size() {
		return parsedSourceFile{}, &ingestFailure{"source-file-mutated", "A source file changed while it was being read."}
	}
	for workoutIndex, workout := range document.Workouts {
		warnings, err := encodeWarnings(workout.Warnings)
		if err != nil {
			return parsedSourceFile{}, &ingestFailure{"source-file-invalid", fmt.Sprintf("Workout %d warnings could not be encoded.", workoutIndex+1)}
		}
		warningBytes := warningsDatabaseTextSize(warnings, len(workout.Warnings))
		if len(workout.Warnings) > maxWorkoutWarnings || warningBytes > maxWorkoutWarningBytes {
			return parsedSourceFile{}, warningLimitFailure(workoutIndex, workout.Warnings, warningBytes)
		}
		if workout.Location != nil && *workout.Location == "" {
			return parsedSourceFile{}, &ingestFailure{"source-file-invalid", fmt.Sprintf("Workout %d has an empty location value.", workoutIndex+1)}
		}
	}
	return parsedSourceFile{sourceFile: discovered, checksum: hashSum(digest), document: document}, nil
}

func sameFileVersion(discovered sourceFile, info os.FileInfo) bool {
	device, inode, ctimeSec, ctimeNS := fileIdentity(info)
	return info.Mode().IsRegular() && discovered.size == info.Size() && discovered.observedModified.Equal(info.ModTime()) &&
		discovered.device == device && discovered.inode == inode && discovered.ctimeSec == ctimeSec && discovered.ctimeNS == ctimeNS
}

func fileIdentity(info os.FileInfo) (uint64, uint64, int64, int64) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, 0
	}
	return uint64(stat.Dev), stat.Ino, stat.Ctim.Sec, stat.Ctim.Nsec
}

func hashSum(digest hash.Hash) (sum [sha256.Size]byte) {
	copy(sum[:], digest.Sum(nil))
	return sum
}

func (r *Runner) observeIngest(ctx context.Context, job claimedJob) (bool, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var cancelled bool
	err = tx.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL
		FROM app.jobs WHERE id=$1 AND status='running' AND worker_id=$2 AND lease_token=$3
		AND lease_expires_at >= clock_timestamp()`, job.id, r.workerID, job.lease).Scan(&cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errLeaseLost
	}
	if err != nil {
		return false, err
	}
	return cancelled, tx.Commit(ctx)
}

func (r *Runner) checkpointSourceFile(ctx context.Context, job claimedJob, file sourceFile) (fileCheckpoint, bool, *ingestFailure, error) {
	tx, sourceID, err := r.beginIngestWrite(ctx, job)
	if err != nil {
		return fileCheckpoint{}, false, nil, err
	}
	defer tx.Rollback(ctx)
	var checkpoint fileCheckpoint
	var size int64
	var modified *time.Time
	err = tx.QueryRow(ctx, `SELECT id,size_bytes,modified_at,state FROM app.source_files
		WHERE job_id=$1 AND relative_name=$2 FOR UPDATE`, job.id, file.name).Scan(&checkpoint.id, &size, &modified, &checkpoint.state)
	if errors.Is(err, pgx.ErrNoRows) {
		checkpoint = fileCheckpoint{id: uuid.Must(uuid.NewV7()), state: "discovered"}
		_, err = tx.Exec(ctx, `INSERT INTO app.source_files
			(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, checkpoint.id, job.accountID, sourceID, job.id, file.name, file.size, file.modified)
		if err != nil {
			return fileCheckpoint{}, false, nil, err
		}
	} else if err != nil {
		return fileCheckpoint{}, false, nil, err
	} else if failure := checkpointMetadataFailure(size, modified, checkpoint.state, file); failure != nil {
		if err := tx.Commit(ctx); err != nil {
			return fileCheckpoint{}, false, nil, ingestCommitError(err)
		}
		return checkpoint, false, failure, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fileCheckpoint{}, false, nil, ingestCommitError(err)
	}
	return checkpoint, checkpoint.state == "succeeded", nil, nil
}

func checkpointMetadataFailure(size int64, modified *time.Time, state string, file sourceFile) *ingestFailure {
	if modified == nil || size != file.size || !databaseTimestamp(*modified).Equal(file.modified) {
		return &ingestFailure{"source-file-mutated", "A source file changed before it could be processed."}
	}
	if state == "failed" || (state != "discovered" && state != "processing" && state != "succeeded") {
		return &ingestFailure{"source-file-invalid", "A source file could not be processed."}
	}
	return nil
}

func (r *Runner) persistSourceFile(ctx context.Context, job claimedJob, checkpoint fileCheckpoint, file parsedSourceFile) (persistedFileResult, error) {
	zones := make([]workouttimezone.Resolution, len(file.document.Workouts))
	for i, workout := range file.document.Workouts {
		zone, err := workouttimezone.ResolveRoute(ctx, r.osmDB, workout.Start, &workout.StartOffsetMins, workout.Route)
		if err != nil {
			return persistedFileResult{}, fmt.Errorf("resolve workout timezone: %w", database.RedactOSMConnectionError(err))
		}
		zones[i] = zone
	}
	tx, sourceID, err := r.beginIngestWrite(ctx, job)
	if err != nil {
		return persistedFileResult{}, err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE app.source_files SET state='processing',
		processing_started_at=COALESCE(processing_started_at,transaction_timestamp())
		WHERE id=$1 AND source_id=$2 AND job_id=$3 AND state IN ('discovered','processing')`, checkpoint.id, sourceID, job.id)
	if err != nil {
		return persistedFileResult{}, err
	}
	if command.RowsAffected() != 1 {
		return persistedFileResult{}, errLeaseLost
	}
	result := persistedFileResult{}
	for i, workout := range file.document.Workouts {
		kind, err := r.persistWorkout(ctx, tx, job, sourceID, checkpoint.id, workout, zones[i])
		if err != nil {
			return persistedFileResult{}, err
		}
		switch kind {
		case "created":
			result.created++
			if len(workout.Route) > 0 {
				result.createdWithRoute++
			}
		case "updated", "adopted":
			result.updated++
			if len(workout.Route) > 0 {
				result.updatedWithRoute++
			}
		case "matched_unchanged", "skipped_live_owner":
			result.unchanged++
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,
		processed_at=transaction_timestamp() WHERE id=$1 AND state='processing'`, checkpoint.id, file.checksum[:]); err != nil {
		return persistedFileResult{}, err
	}
	var inventoryRecorded bool
	if err = tx.QueryRow(ctx, `SELECT app.record_successful_source_object($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		job.id, r.workerID, job.lease, file.name, file.exportDate, file.size, file.modified,
		sourceIdentity(file.sourceFile), file.checksum[:]).Scan(&inventoryRecorded); err != nil {
		return persistedFileResult{}, err
	}
	if !inventoryRecorded {
		return persistedFileResult{}, errLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return persistedFileResult{}, ingestCommitError(err)
	}
	return result, nil
}

func (r *Runner) persistWorkout(ctx context.Context, tx pgx.Tx, job claimedJob, sourceID, fileID uuid.UUID, workout healthautoexport.Workout, zone workouttimezone.Resolution) (string, error) {
	var providerID *string
	var fallbackVersion *string
	var fallbackHash []byte
	if workout.ProviderID != "" {
		providerID = &workout.ProviderID
	} else {
		version := healthautoexport.FallbackFingerprintVersion
		fallbackVersion, fallbackHash = &version, workout.FallbackSHA256[:]
	}
	var workoutID uuid.UUID
	var priorHash []byte
	var outcome string
	if err := tx.QueryRow(ctx, `SELECT workout_id,prior_content_sha256,outcome
		FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,$6,$7,$8)`,
		job.id, r.workerID, job.lease, fileID, uuid.Must(uuid.NewV7()), providerID, fallbackVersion, fallbackHash).
		Scan(&workoutID, &priorHash, &outcome); err != nil {
		return "", err
	}
	if outcome == "suppressed" {
		return "suppressed", nil
	}
	if outcome != "created" && outcome != "existing" && outcome != "adopted" && outcome != "skipped_live_owner" {
		return "", errors.New("invalid ingest identity outcome")
	}
	kind := "skipped_live_owner"
	if outcome != "skipped_live_owner" {
		var err error
		typeID := uuid.Must(uuid.NewV7())
		displayLabel := healthautoexport.CanonicalActivityLabel(workout.ProviderLabel)
		if err := tx.QueryRow(ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,$3,$4) ON CONFLICT(account_id,type_key) DO UPDATE SET provider_label=EXCLUDED.provider_label
		RETURNING id`, typeID, job.accountID, workout.TypeKey, displayLabel).Scan(&typeID); err != nil {
			return "", err
		}
		kind = "created"
		changed := true
		if outcome == "created" {
			_, err = tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,fallback_fingerprint_version,fallback_sha256,content_sha256,provider_label,started_at,ended_at,
			start_offset_minutes,end_offset_minutes,local_start_date,timezone_name,timezone_source,timezone_dataset_release,
			provider_duration,is_indoor,location)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::numeric,$20,$21)`, workoutID,
				job.accountID, sourceID, fileID, typeID, providerID, fallbackVersion, fallbackHash, workout.ContentSHA256[:],
				displayLabel, workout.Start, workout.End, workout.StartOffsetMins, workout.EndOffsetMins,
				workout.LocalStartDate, zone.Name, zone.Source, zone.Release, workout.ProviderDuration.String(), workout.IsIndoor, workout.Location)
		} else {
			changed = !bytes.Equal(priorHash, workout.ContentSHA256[:])
			if !changed {
				kind = "matched_unchanged"
				// The write capability follows source_file_id to this fenced job.
				// Repair display without replacing provider content or derived state.
				_, err = tx.Exec(ctx, `UPDATE app.workouts SET provider_label=$2,source_file_id=$3,source_id=$4
				WHERE id=$1 AND (provider_label IS DISTINCT FROM $2 OR source_id IS DISTINCT FROM $4)`,
					workoutID, displayLabel, fileID, sourceID)
			} else {
				kind = "updated"
				_, err = tx.Exec(ctx, `UPDATE app.workouts SET source_file_id=$2,workout_type_id=$3,content_sha256=$4,
				provider_label=$5,started_at=$6,ended_at=$7,start_offset_minutes=$8,end_offset_minutes=$9,
				local_start_date=$10,timezone_name=$11,timezone_source=$12,timezone_reference_workout_id=NULL,
				timezone_dataset_release=$13,provider_duration=$14::numeric,is_indoor=$15,location=$16,source_id=$17 WHERE id=$1`, workoutID,
					fileID, typeID, workout.ContentSHA256[:], displayLabel, workout.Start, workout.End,
					workout.StartOffsetMins, workout.EndOffsetMins, workout.LocalStartDate, zone.Name, zone.Source, zone.Release, workout.ProviderDuration.String(),
					workout.IsIndoor, workout.Location, sourceID)
			}
		}
		if err != nil {
			return "", err
		}
		if changed {
			if _, err = tx.Exec(ctx, `DELETE FROM app.workout_aggregates WHERE workout_id=$1`, workoutID); err != nil {
				return "", err
			}
			if _, err = tx.Exec(ctx, `DELETE FROM app.workout_route_points WHERE workout_id=$1`, workoutID); err != nil {
				return "", err
			}
			for _, aggregate := range workout.Aggregates {
				if _, err = tx.Exec(ctx, `INSERT INTO app.workout_aggregates(account_id,workout_id,metric,value,unit,origin)
				VALUES($1,$2,$3,$4::numeric,$5,$6)`, job.accountID, workoutID, aggregate.Key,
					aggregate.Qty.String(), aggregate.Units, aggregate.Origin); err != nil {
					return "", err
				}
			}
			for _, point := range workout.Route {
				if _, err = tx.Exec(ctx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,
				timestamp_offset_minutes,latitude,longitude,altitude,speed,course,horizontal_accuracy,vertical_accuracy,
				speed_accuracy,course_accuracy) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
					job.accountID, workoutID, point.Sequence, point.Timestamp, point.TimestampOffsetMins, point.Latitude,
					point.Longitude, point.Altitude, point.Speed, point.Course, point.HorizontalAccuracy,
					point.VerticalAccuracy, point.SpeedAccuracy, point.CourseAccuracy); err != nil {
					return "", err
				}
			}
			summary := deriveRouteSummary(workout.Route)
			var replaced bool
			if err = tx.QueryRow(ctx, `SELECT app.replace_workout_route_summary($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
				workoutID, summary.pointCount, summary.minimumLongitude, summary.minimumLatitude,
				summary.maximumLongitude, summary.maximumLatitude, summary.minimumAltitude,
				summary.maximumAltitude, summary.elevationGain, summary.hasCompleteAltitude).Scan(&replaced); err != nil {
				return "", err
			}
			if !replaced {
				return "", errors.New("route summary was not replaced")
			}
			// Empty routes remove their summary, so there is no split row to update.
			if len(workout.Route) != 0 {
				var splitsReplaced bool
				if err = tx.QueryRow(ctx, `SELECT app.replace_workout_split_summary($1)`, workoutID).Scan(&splitsReplaced); err != nil {
					return "", err
				}
				if !splitsReplaced {
					return "", errors.New("route split summary was not replaced")
				}
			}
		}
		if outcome == "adopted" {
			kind = "adopted"
		}
	}
	warnings, err := encodeWarnings(workout.Warnings)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.workout_import_events
		(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,warnings,has_route)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, uuid.Must(uuid.NewV7()), job.accountID, sourceID, workoutID,
		fileID, job.id, kind, workout.ContentSHA256[:], warnings, len(workout.Route) > 0)
	if err == nil && kind == "adopted" {
		// Coverage state locks belong to post-ingest repair/reconciliation,
		// outside this file transaction. Only record the durable refresh here.
		_, err = tx.Exec(ctx, `SELECT app.record_adopted_workout_coverage_refresh($1,$2,$3,$4)`,
			job.id, r.workerID, job.lease, workoutID)
	}
	return kind, err
}

type routeSummary struct {
	pointCount                                          int
	minimumLongitude, minimumLatitude, maximumLongitude *float64
	maximumLatitude, minimumAltitude, maximumAltitude   *float64
	elevationGain                                       *float64
	hasCompleteAltitude                                 bool
}

func deriveRouteSummary(points []healthautoexport.RoutePoint) routeSummary {
	if len(points) == 0 {
		return routeSummary{}
	}
	result := routeSummary{pointCount: len(points), hasCompleteAltitude: true}
	minLongitude, maxLongitude := points[0].Longitude, points[0].Longitude
	minLatitude, maxLatitude := points[0].Latitude, points[0].Latitude
	var minAltitude, maxAltitude, gain float64
	var priorAltitude *float64
	hasAltitude := false
	for _, point := range points {
		minLongitude, maxLongitude = min(minLongitude, point.Longitude), max(maxLongitude, point.Longitude)
		minLatitude, maxLatitude = min(minLatitude, point.Latitude), max(maxLatitude, point.Latitude)
		if point.Altitude == nil {
			result.hasCompleteAltitude = false
			priorAltitude = nil
			continue
		}
		if !hasAltitude {
			minAltitude, maxAltitude, hasAltitude = *point.Altitude, *point.Altitude, true
		} else {
			minAltitude, maxAltitude = min(minAltitude, *point.Altitude), max(maxAltitude, *point.Altitude)
		}
		if priorAltitude != nil && *point.Altitude > *priorAltitude {
			gain += *point.Altitude - *priorAltitude
		}
		priorAltitude = point.Altitude
	}
	result.minimumLongitude, result.maximumLongitude = &minLongitude, &maxLongitude
	result.minimumLatitude, result.maximumLatitude = &minLatitude, &maxLatitude
	if hasAltitude {
		result.minimumAltitude, result.maximumAltitude, result.elevationGain = &minAltitude, &maxAltitude, &gain
	}
	return result
}

func encodeWarnings(warnings []healthautoexport.Warning) ([]byte, error) {
	type safeWarning struct {
		Code       healthautoexport.WarningCode  `json:"code"`
		Field      healthautoexport.WarningField `json:"field"`
		RoutePoint int                           `json:"route_point"`
	}
	safe := make([]safeWarning, len(warnings))
	for index, warning := range warnings {
		safe[index] = safeWarning{warning.Code, warning.Field, warning.RoutePoint}
	}
	return json.Marshal(safe)
}

func warningsDatabaseTextSize(encoded []byte, count int) int {
	if count == 0 {
		return len(encoded)
	}
	// PostgreSQL's jsonb text form adds spaces after five object separators and
	// between adjacent array elements, while json.Marshal emits compact JSON.
	return len(encoded) + 6*count - 1
}

func warningLimitFailure(workoutIndex int, warnings []healthautoexport.Warning, databaseBytes int) *ingestFailure {
	type warningGroup struct {
		key   string
		count int
	}
	counts := make(map[string]int)
	for _, warning := range warnings {
		counts[fmt.Sprintf("%s/%s", warning.Code, warning.Field)]++
	}
	groups := make([]warningGroup, 0, len(counts))
	for key, count := range counts {
		groups = append(groups, warningGroup{key, count})
	}
	sort.Slice(groups, func(left, right int) bool {
		return groups[left].count > groups[right].count || (groups[left].count == groups[right].count && groups[left].key < groups[right].key)
	})
	breakdown := ""
	for index, group := range groups[:min(3, len(groups))] {
		if index > 0 {
			breakdown += ", "
		}
		breakdown += fmt.Sprintf("%s=%d", group.key, group.count)
	}
	return &ingestFailure{"source-file-invalid", fmt.Sprintf(
		"Workout %d produced %d warnings (%d database bytes); limits are %d warnings and %d bytes; warning types: %s.",
		workoutIndex+1, len(warnings), databaseBytes, maxWorkoutWarnings, maxWorkoutWarningBytes, breakdown,
	)}
}

func parseFailureSummary(failure *healthautoexport.ParseError) string {
	location := ""
	if failure.Workout >= 0 {
		location = fmt.Sprintf(" at workout %d", failure.Workout+1)
		if failure.RoutePoint >= 0 {
			location += fmt.Sprintf(", route point %d", failure.RoutePoint+1)
		}
	}
	return fmt.Sprintf("The parser rejected the file%s (%s).", location, failure.Code)
}

func (r *Runner) persistFailedFile(ctx context.Context, job claimedJob, checkpoint fileCheckpoint, failure ingestFailure) error {
	tx, _, err := r.beginIngestWrite(ctx, job)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE app.source_files SET state='processing',processing_started_at=transaction_timestamp()
		WHERE id=$1 AND state='discovered'`, checkpoint.id); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE app.source_files SET state='failed',failure_code=$2,failure_summary=$3,
		processed_at=transaction_timestamp() WHERE id=$1 AND state='processing'`, checkpoint.id, failure.code, failure.summary)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestCommitError(err)
	}
	r.recordSourceFileFailureLogBestEffort(ctx, job, checkpoint.id)
	return nil
}

func (r *Runner) recordSourceFileFailureLogBestEffort(ctx context.Context, job claimedJob, fileID uuid.UUID) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err == nil {
		defer tx.Rollback(ctx)
		var recorded bool
		err = tx.QueryRow(ctx, `SELECT app.record_source_file_failure_log($1,$2,$3,$4)`,
			job.id, r.workerID, job.lease, fileID).Scan(&recorded)
		if err == nil {
			err = tx.Commit(ctx)
		}
	}
	if err != nil {
		r.logger.WarnContext(ctx, "durable source file failure log unavailable", "job_id", job.id, "error", err)
	}
}

func (r *Runner) beginIngestWrite(ctx context.Context, job claimedJob) (pgx.Tx, uuid.UUID, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	var sourceID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,$4)`,
		job.id, r.workerID, job.lease, database.SupportedSchemaVersion).Scan(&sourceID); errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return nil, uuid.Nil, errLeaseLost
	} else if err != nil {
		_ = tx.Rollback(ctx)
		return nil, uuid.Nil, err
	}
	return tx, sourceID, nil
}

func (r *Runner) failIngest(ctx context.Context, job claimedJob, failure ingestFailure) error {
	finished, err := r.finishJob(ctx, job, "failed", &failure)
	if err != nil {
		return err
	}
	if !finished {
		if cancelled, cancelErr := r.finishCancelled(ctx, job); cancelErr != nil {
			return cancelErr
		} else if cancelled {
			return nil
		}
		return errLeaseLost
	}
	return nil
}

func (r *Runner) finishJob(ctx context.Context, job claimedJob, status string, failure *ingestFailure) (bool, error) {
	if status == "failed" {
		r.recordDiagnosticsBestEffort(ctx, job, "ingest-failed", map[string]any{
			"operation": "finalize", "reason": safeDiagnosticReason(failure),
		})
	} else if err := r.recordJobLog(ctx, job, "ingest-completed", map[string]any{"operation": "finalize"}); err != nil {
		r.logger.WarnContext(ctx, "durable ingest diagnostic unavailable", "job_id", job.id, "code", "ingest-completed", "error", err)
	}
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var code, summary *string
	if failure != nil {
		code, summary = &failure.code, &failure.summary
	}
	var finished bool
	if err := tx.QueryRow(ctx, `SELECT app.finish_job($1,$2,$3,$4,$5,$6)`, job.id, r.workerID, job.lease,
		status, code, summary).Scan(&finished); err != nil {
		return false, err
	}
	return finished, tx.Commit(ctx)
}

func (r *Runner) recordDiagnostics(ctx context.Context, job claimedJob, code string, fields map[string]any) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var eventRecorded, logRecorded bool
	if err = tx.QueryRow(ctx, `SELECT app.record_job_event($1,$2,$3,$4,$5)`, job.id, r.workerID, job.lease, code, fields).Scan(&eventRecorded); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT app.record_job_log($1,$2,$3,$4,$5)`, job.id, r.workerID, job.lease, code, fields).Scan(&logRecorded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Runner) recordDiagnosticsBestEffort(ctx context.Context, job claimedJob, code string, fields map[string]any) {
	if err := r.recordDiagnostics(ctx, job, code, fields); err != nil {
		r.logger.WarnContext(ctx, "durable ingest diagnostic unavailable", "job_id", job.id, "code", code, "error", err)
	}
}

func (r *Runner) recordJobLog(ctx context.Context, job claimedJob, code string, fields map[string]any) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var recorded bool
	if err = tx.QueryRow(ctx, `SELECT app.record_job_log($1,$2,$3,$4,$5)`, job.id, r.workerID, job.lease, code, fields).Scan(&recorded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func safeDiagnosticReason(failure *ingestFailure) string {
	if failure == nil {
		return "write-failed"
	}
	switch failure.code {
	case "source-unavailable", "source-config-invalid":
		return "source-unavailable"
	case "source-directory-limit", "source-files-changed":
		return "read-failed"
	default:
		return "invalid-data"
	}
}

func ingestCommitError(err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch postgres.Code {
		case "55P03", "40P01", "40001":
			// Yield the whole transaction to normal lease recovery. A busy owner
			// must not become an invalid file or an identity conflict.
			return errLeaseLost
		}
	}
	return err
}
