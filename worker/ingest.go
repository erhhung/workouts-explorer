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
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
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

type ingestResults struct {
	FilesProcessed    int      `json:"files_processed"`
	WorkoutsProcessed int      `json:"workouts_processed"`
	WorkoutsIngested  int      `json:"workouts_ingested"`
	FailedProcessing  []string `json:"failed_processing"`
	filesDiscovered   int
	filesSkipped      int
	filesSucceeded    int
	filesFailed       int
	workoutsCreated   int
	workoutsUpdated   int
	workoutsUnchanged int
}

func newIngestResults() ingestResults {
	return ingestResults{FailedProcessing: make([]string, 0)}
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
	created   int
	updated   int
	unchanged int
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
	err := r.ingest(opCtx, job, &results)
	execution := executionResult{ingest: &results}
	cancelOperation()
	heartbeatErr := <-heartbeatDone
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
		return execution, heartbeatErr
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
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

func (r *Runner) ingest(ctx context.Context, job claimedJob, results *ingestResults) error {
	if job.paramsErr != nil {
		return r.failIngest(ctx, job, ingestFailure{"ingest-parameters-invalid", "Ingest parameters were invalid."})
	}
	sourceID, envelope, cancellationPending, err := r.readSnapshot(ctx, job)
	if err != nil {
		return err
	}
	if cancellationPending {
		_, err = r.finishCancelled(ctx, job)
		return err
	}
	r.recordDiagnosticsBestEffort(ctx, job, "ingest-started", map[string]any{"operation": "discover"})
	plaintext, err := r.keys.Decrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: job.accountID, RecordID: job.id,
	}, envelope)
	if err != nil {
		return r.failIngest(ctx, job, ingestFailure{"source-config-invalid", "Source configuration could not be read."})
	}
	config, canonical, err := sourceconfig.DecodeLocal(plaintext, r.localRoots)
	if err != nil || !bytes.Equal(plaintext, canonical) {
		return r.failIngest(ctx, job, ingestFailure{"source-config-invalid", "Source configuration could not be read."})
	}
	directory, err := sourceconfig.OpenDirectory(config.Path, r.localRoots)
	if err != nil {
		return r.failIngest(ctx, job, ingestFailure{"source-unavailable", "Source directory is not accessible."})
	}
	files, err := discoverSourceFiles(directory)
	_ = directory.Close()
	if err != nil {
		recordDiscoveryFailure(results, err)
		failure := ingestFailure{"source-unavailable", "Source directory could not be read."}
		if errors.Is(err, errDiscoveryLimit) {
			failure = ingestFailure{"source-directory-limit", "Source directory contains too many entries."}
		}
		return r.failIngest(ctx, job, failure)
	}
	files = filterSourceFiles(files, job.startDate, job.endDate)
	if err := r.resolveCandidateActions(ctx, job, sourceID, files); err != nil {
		return err
	}
	manifestState, err := r.recordFileManifest(ctx, job, files)
	if err != nil {
		return err
	}
	if manifestState == "mismatch" {
		return r.failIngest(ctx, job, ingestFailure{"source-files-changed", "Source files changed while ingest was recovering."})
	}
	if err := r.loadIngestProgress(ctx, job, results); err != nil {
		return err
	}
	if manifestState == "created" {
		if results.filesDiscovered != 0 || results.filesSkipped != 0 || results.filesSucceeded != 0 || results.filesFailed != 0 {
			return r.failIngest(ctx, job, ingestFailure{"source-files-changed", "Source files changed while ingest was recovering."})
		}
	}
	if results.filesDiscovered == 0 {
		results.filesDiscovered = len(files)
	}
	markRecoveredSkips(files, results.filesSkipped)
	if results.filesDiscovered != len(files) {
		return r.failIngest(ctx, job, ingestFailure{"source-files-changed", "Source files changed while ingest was recovering."})
	}
	if err := r.recordIngestProgress(ctx, job, *results); err != nil {
		return err
	}
	r.recordDiagnosticsBestEffort(ctx, job, "ingest-progress", map[string]any{"current": 0, "total": len(files)})

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome := r.processSourceFile(ctx, job, sourceID, config.Path, file)
		if outcome.fatal != nil {
			return outcome.fatal
		}
		if outcome.alreadyCounted {
			continue
		}
		results.applyFileOutcome(file.name, outcome)
		if err := r.recordIngestProgress(ctx, job, *results); err != nil {
			return err
		}
	}
	status := "succeeded"
	var finalFailure *ingestFailure
	if results.filesFailed > 0 {
		status = "failed"
		failure := ingestFailure{"source-file-invalid", "One or more source files could not be processed."}
		finalFailure = &failure
	}
	finished, err := r.finishJob(ctx, job, status, finalFailure)
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
	r.syncTotals()
}

func filterSourceFiles(files []sourceFile, start, end *time.Time) []sourceFile {
	if start == nil || end == nil {
		return files
	}
	return slices.DeleteFunc(files, func(file sourceFile) bool { return file.exportDate.Before(*start) || file.exportDate.After(*end) })
}

func (r *Runner) processSourceFile(ctx context.Context, job claimedJob, sourceID uuid.UUID, sourcePath string, file sourceFile) fileOutcome {
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
	defer r.releaseFileSlot(job, slot)
	directory, err := sourceconfig.OpenDirectory(sourcePath, r.localRoots)
	if err != nil {
		failure := ingestFailure{"source-unavailable", "Source directory is not accessible."}
		if persistErr := r.persistFailedFile(ctx, job, checkpoint, failure); persistErr != nil {
			return fileOutcome{fatal: persistErr}
		}
		return fileOutcome{failed: true}
	}
	parsed, failure := readSourceFile(ctx, directory, file)
	_ = directory.Close()
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

func (r *Runner) resolveCandidateActions(ctx context.Context, job claimedJob, sourceID uuid.UUID, files []sourceFile) error {
	for index := range files {
		files[index].action = "process"
		if job.mode != ingestIncremental {
			continue
		}
		tx, err := beginAccount(ctx, r.db, job.accountID)
		if err != nil {
			return err
		}
		var state string
		err = tx.QueryRow(ctx, `SELECT state FROM app.source_files WHERE job_id=$1 AND relative_name=$2`,
			job.id, files[index].name).Scan(&state)
		if err == nil && (state == "succeeded" || state == "failed") {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return err
		}
		var identity string
		err = tx.QueryRow(ctx, `SELECT observed_identity FROM app.source_objects
			WHERE source_id=$1 AND relative_name=$2 AND successful_checksum IS NOT NULL`, sourceID, files[index].name).Scan(&identity)
		if err == nil && identity == sourceIdentity(files[index]) {
			files[index].action = "skip"
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type manifestCandidate struct {
	Name        string `json:"name"`
	ExportDate  string `json:"export_date"`
	Size        int64  `json:"size"`
	ModifiedSec int64  `json:"modified_sec"`
	ModifiedNS  int64  `json:"modified_ns"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	CTimeSec    int64  `json:"ctime_sec"`
	CTimeNS     int64  `json:"ctime_ns"`
	Action      string `json:"action"`
}

func (r *Runner) recordFileManifest(ctx context.Context, job claimedJob, files []sourceFile) (string, error) {
	manifest := make([]manifestCandidate, len(files))
	for index, file := range files {
		manifest[index] = manifestCandidate{
			Name: file.name, ExportDate: file.exportDate.Format(time.DateOnly), Size: file.size,
			ModifiedSec: file.observedModified.Unix(), ModifiedNS: int64(file.observedModified.Nanosecond()),
			Device: file.device, Inode: file.inode, CTimeSec: file.ctimeSec, CTimeNS: file.ctimeNS, Action: file.action,
		}
	}
	encoded, err := json.Marshal(manifest)
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
	if err := tx.QueryRow(ctx, `SELECT
		count(DISTINCT file.id) FILTER (WHERE file.state='succeeded'),
		count(DISTINCT file.id) FILTER (WHERE file.state='failed'),
		count(event.id) FILTER (WHERE event.kind='created'),
		count(event.id) FILTER (WHERE event.kind='updated'),
		count(event.id) FILTER (WHERE event.kind='matched_unchanged')
		FROM app.source_files file LEFT JOIN app.workout_import_events event ON event.source_file_id=file.id
		WHERE file.job_id=$1`, job.id).Scan(&succeeded, &failed, &created, &updated, &unchanged); err != nil {
		return err
	}
	results.filesSucceeded = max(results.filesSucceeded, succeeded)
	results.filesFailed = max(results.filesFailed, failed)
	results.workoutsCreated = max(results.workoutsCreated, created)
	results.workoutsUpdated = max(results.workoutsUpdated, updated)
	results.workoutsUnchanged = max(results.workoutsUnchanged, unchanged)
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
	err = tx.QueryRow(ctx, `SELECT app.record_ingest_progress($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0)`,
		job.id, r.workerID, job.lease, results.filesDiscovered, results.filesSkipped, results.filesSucceeded,
		results.filesFailed, results.workoutsCreated, results.workoutsUpdated, results.workoutsUnchanged).Scan(&recorded)
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
		var parsed *healthautoexport.ParseError
		if errors.As(err, &parsed) && parsed.Code == healthautoexport.ErrorReadFailure {
			code = "source-file-unavailable"
		}
		return parsedSourceFile{}, &ingestFailure{code, "A source file could not be processed."}
	}
	final, err := file.Stat()
	if err != nil || !sameFileVersion(discovered, final) || final.Size() != initial.Size() {
		return parsedSourceFile{}, &ingestFailure{"source-file-mutated", "A source file changed while it was being read."}
	}
	for _, workout := range document.Workouts {
		warnings, err := encodeWarnings(workout.Warnings)
		if err != nil || len(workout.Warnings) > 4096 || len(warnings) > 262144 ||
			(workout.Location != nil && *workout.Location == "") {
			return parsedSourceFile{}, &ingestFailure{"source-file-invalid", "A source file could not be processed."}
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
	for _, workout := range file.document.Workouts {
		kind, err := persistWorkout(ctx, tx, job, sourceID, checkpoint.id, workout)
		if err != nil {
			return persistedFileResult{}, err
		}
		switch kind {
		case "created":
			result.created++
		case "updated":
			result.updated++
		case "matched_unchanged":
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

func persistWorkout(ctx context.Context, tx pgx.Tx, job claimedJob, sourceID, fileID uuid.UUID, workout healthautoexport.Workout) (string, error) {
	var providerID *string
	var fallbackVersion *string
	var fallbackHash []byte
	if workout.ProviderID != "" {
		providerID = &workout.ProviderID
	} else {
		version := healthautoexport.FallbackFingerprintVersion
		fallbackVersion, fallbackHash = &version, workout.FallbackSHA256[:]
	}
	var suppressed bool
	if err := tx.QueryRow(ctx, `SELECT app.workout_deletion_suppressed($1,$2,$3,$4)`, sourceID, providerID, fallbackVersion, fallbackHash).Scan(&suppressed); err != nil {
		return "", err
	}
	if suppressed {
		return "suppressed", nil
	}
	typeID := uuid.Must(uuid.NewV7())
	if err := tx.QueryRow(ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,$3,$4) ON CONFLICT(account_id,type_key) DO UPDATE SET provider_label=EXCLUDED.provider_label
		RETURNING id`, typeID, job.accountID, workout.TypeKey, workout.ProviderLabel).Scan(&typeID); err != nil {
		return "", err
	}
	var workoutID uuid.UUID
	var priorHash []byte
	var err error
	if workout.ProviderID != "" {
		err = tx.QueryRow(ctx, `SELECT id,content_sha256 FROM app.workouts
			WHERE source_id=$1 AND provider_id=$2 FOR UPDATE`, sourceID, workout.ProviderID).Scan(&workoutID, &priorHash)
	} else {
		err = tx.QueryRow(ctx, `SELECT id,content_sha256 FROM app.workouts WHERE source_id=$1
			AND provider_id IS NULL AND fallback_fingerprint_version=$2 AND fallback_sha256=$3 FOR UPDATE`,
			sourceID, healthautoexport.FallbackFingerprintVersion, workout.FallbackSHA256[:]).Scan(&workoutID, &priorHash)
	}
	kind := "created"
	changed := true
	if errors.Is(err, pgx.ErrNoRows) {
		workoutID = uuid.Must(uuid.NewV7())
		_, err = tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,fallback_fingerprint_version,fallback_sha256,content_sha256,provider_label,started_at,ended_at,
			start_offset_minutes,end_offset_minutes,local_start_date,provider_duration,is_indoor,location)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::numeric,$17,$18)`, workoutID,
			job.accountID, sourceID, fileID, typeID, providerID, fallbackVersion, fallbackHash, workout.ContentSHA256[:],
			workout.ProviderLabel, workout.Start, workout.End, workout.StartOffsetMins, workout.EndOffsetMins,
			workout.LocalStartDate, workout.ProviderDuration.String(), workout.IsIndoor, workout.Location)
	} else if err == nil {
		changed = !bytes.Equal(priorHash, workout.ContentSHA256[:])
		if !changed {
			kind = "matched_unchanged"
		} else {
			kind = "updated"
			_, err = tx.Exec(ctx, `UPDATE app.workouts SET source_file_id=$2,workout_type_id=$3,content_sha256=$4,
				provider_label=$5,started_at=$6,ended_at=$7,start_offset_minutes=$8,end_offset_minutes=$9,
				local_start_date=$10,provider_duration=$11::numeric,is_indoor=$12,location=$13 WHERE id=$1`, workoutID,
				fileID, typeID, workout.ContentSHA256[:], workout.ProviderLabel, workout.Start, workout.End,
				workout.StartOffsetMins, workout.EndOffsetMins, workout.LocalStartDate, workout.ProviderDuration.String(),
				workout.IsIndoor, workout.Location)
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
	}
	warnings, err := encodeWarnings(workout.Warnings)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.workout_import_events
		(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,warnings)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, uuid.Must(uuid.NewV7()), job.accountID, sourceID, workoutID,
		fileID, job.id, kind, workout.ContentSHA256[:], warnings)
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
	return nil
}

func (r *Runner) beginIngestWrite(ctx context.Context, job claimedJob) (pgx.Tx, uuid.UUID, error) {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	var sourceID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3)`,
		job.id, r.workerID, job.lease).Scan(&sourceID); errors.Is(err, pgx.ErrNoRows) {
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
	if errors.As(err, &postgres) && postgres.Code == "40001" {
		return errLeaseLost
	}
	return err
}
