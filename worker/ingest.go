package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
}

func newIngestResults() ingestResults {
	return ingestResults{FailedProcessing: make([]string, 0)}
}

func (r *ingestResults) recordFailedFile(name string) {
	r.FilesProcessed++
	if len(r.FailedProcessing) < maxFailedProcessing {
		r.FailedProcessing = append(r.FailedProcessing, filepath.Base(name))
	}
}

func (r ingestResults) hasPartialData() bool {
	return r.FilesProcessed != 0 || r.WorkoutsProcessed != 0 || r.WorkoutsIngested != 0 || len(r.FailedProcessing) != 0
}

type persistedFileResult struct {
	workoutsProcessed int
	workoutsIngested  int
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
		return execution, nil
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
		return execution, heartbeatErr
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		_, finishErr := r.finishCancelled(cleanupCtx, job)
		return execution, finishErr
	}
	if err != nil {
		return execution, err
	}
	return execution, nil
}

func (r *Runner) ingest(ctx context.Context, job claimedJob, results *ingestResults) error {
	_, envelope, cancellationPending, err := r.readSnapshot(ctx, job)
	if err != nil {
		return err
	}
	if cancellationPending {
		_, err = r.finishCancelled(ctx, job)
		return err
	}
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
	defer directory.Close()
	files, err := discoverSourceFiles(directory)
	if err != nil {
		recordDiscoveryFailure(results, err)
		failure := ingestFailure{"source-unavailable", "Source directory could not be read."}
		if errors.Is(err, errDiscoveryLimit) {
			failure = ingestFailure{"source-directory-limit", "Source directory contains too many entries."}
		}
		return r.failIngest(ctx, job, failure)
	}

	for _, file := range files {
		cancelled, err := r.observeIngest(ctx, job)
		if err != nil {
			return err
		}
		if cancelled {
			_, err = r.finishCancelled(ctx, job)
			return err
		}
		checkpoint, skip, checkpointFailure, err := r.checkpointSourceFile(ctx, job, file)
		if err != nil {
			results.recordFailedFile(file.name)
			return err
		}
		if checkpointFailure != nil {
			results.recordFailedFile(file.name)
			if checkpoint.id != uuid.Nil && (checkpoint.state == "discovered" || checkpoint.state == "processing") {
				if err := r.persistFailedFile(ctx, job, checkpoint, *checkpointFailure); err != nil {
					return err
				}
			}
			return r.failIngest(ctx, job, *checkpointFailure)
		}
		if skip {
			continue
		}
		parsed, failure := readSourceFile(ctx, directory, file)
		cancelled, err = r.observeIngest(ctx, job)
		if err != nil {
			return err
		}
		if cancelled {
			_, err = r.finishCancelled(ctx, job)
			return err
		}
		if failure != nil {
			results.recordFailedFile(file.name)
			if err := r.persistFailedFile(ctx, job, checkpoint, *failure); err != nil {
				return err
			}
			return r.failIngest(ctx, job, *failure)
		}
		fileResult, err := r.persistSourceFile(ctx, job, checkpoint, parsed)
		if err != nil {
			results.recordFailedFile(file.name)
			return err
		}
		results.FilesProcessed++
		results.WorkoutsProcessed += fileResult.workoutsProcessed
		results.WorkoutsIngested += fileResult.workoutsIngested
	}
	finished, err := r.finishJob(ctx, job, "succeeded", nil)
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
			file, info, err := openRegularFile(directory, entry.Name())
			if err != nil {
				return nil, &discoveryFileError{name: entry.Name()}
			}
			if err := file.Close(); err != nil {
				return nil, &discoveryFileError{name: entry.Name()}
			}
			device, inode, ctimeSec, ctimeNS := fileIdentity(info)
			files = append(files, sourceFile{
				name: entry.Name(), size: info.Size(), modified: databaseTimestamp(info.ModTime()), observedModified: info.ModTime(),
				device: device, inode: inode, ctimeSec: ctimeSec, ctimeNS: ctimeNS,
			})
			if len(files) > limits.maxFiles {
				return nil, errDiscoveryLimit
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	slices.SortFunc(files, func(left, right sourceFile) int { return bytes.Compare([]byte(left.name), []byte(right.name)) })
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
	result := persistedFileResult{workoutsProcessed: len(file.document.Workouts)}
	for _, workout := range file.document.Workouts {
		kind, err := persistWorkout(ctx, tx, job, sourceID, checkpoint.id, workout)
		if err != nil {
			return persistedFileResult{}, err
		}
		if kind == "created" || kind == "updated" {
			result.workoutsIngested++
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE app.source_files SET state='succeeded',checksum_sha256=$2,
		processed_at=transaction_timestamp() WHERE id=$1 AND state='processing'`, checkpoint.id, file.checksum[:]); err != nil {
		return persistedFileResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistedFileResult{}, ingestCommitError(err)
	}
	return result, nil
}

func persistWorkout(ctx context.Context, tx pgx.Tx, job claimedJob, sourceID, fileID uuid.UUID, workout healthautoexport.Workout) (string, error) {
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
		var providerID *string
		var fallbackVersion *string
		var fallbackHash []byte
		if workout.ProviderID != "" {
			providerID = &workout.ProviderID
		} else {
			version := healthautoexport.FallbackFingerprintVersion
			fallbackVersion, fallbackHash = &version, workout.FallbackSHA256[:]
		}
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

func ingestCommitError(err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && postgres.Code == "40001" {
		return errLeaseLost
	}
	return err
}
