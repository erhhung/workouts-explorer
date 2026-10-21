package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/erhhung/workouts-explorer/internal/rclonesource"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
)

var (
	errPrivateCleanup    = errors.New("source-private-cleanup-failed: Private source files could not be removed.")
	errCredentialRefresh = errors.New("source-credential-refresh-failed: Source credentials could not be saved.")
)

func privateCleanupError(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.Join(errPrivateCleanup, ctx.Err())
	}
	return errPrivateCleanup
}

func (r *Runner) checkICloudConnection(ctx context.Context, job claimedJob, snapshot jobSourceSnapshot) (result connectionResult, err error) {
	plaintext, err := r.keys.Decrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: job.accountID, RecordID: job.id,
	}, snapshot.envelope)
	if err != nil {
		return connectionFailed("source-config-invalid", "Source configuration could not be read."), nil
	}
	config, canonical, err := sourceconfig.DecodeICloud(plaintext)
	if err != nil || !bytes.Equal(plaintext, canonical) {
		return connectionFailed("source-config-invalid", "Source configuration could not be read."), nil
	}
	remote, err := rclonesource.New(ctx, r.rcloneBinary, r.stagingRoot, config)
	if err != nil {
		err = r.icloudConstructorError(ctx, remote, err)
		if errors.Is(err, errPrivateCleanup) || ctx.Err() != nil {
			return connectionResult{}, err
		}
		failure := remoteFailure(err)
		return connectionFailed(failure.code, failure.summary), nil
	}
	defer func() {
		if r.closeICloudSource(remote) != nil {
			err = privateCleanupError(ctx)
		}
	}()
	checkErr := remote.Check(ctx)
	if ctx.Err() != nil {
		return connectionResult{}, ctx.Err()
	}
	if checkErr != nil {
		failure := remoteFailure(checkErr)
		return connectionFailed(failure.code, failure.summary), nil
	}
	refreshed, err := r.encryptICloudRefresh(job, snapshot, config, remote.Config())
	if err != nil {
		return connectionResult{}, err
	}
	result = connectedResult()
	result.refreshedEncrypted = refreshed
	return result, nil
}

func (r *Runner) icloudConstructorError(ctx context.Context, remote *rclonesource.Source, err error) error {
	var safe *rclonesource.Error
	cleanupFailed := errors.As(err, &safe) && safe.Code == "source-cleanup-failed"
	if remote != nil && r.closeICloudSource(remote) != nil {
		cleanupFailed = true
	}
	if cleanupFailed {
		r.stagingDirty.Store(true)
		// Even a successful retry must not convert this attempt into a terminal
		// failure. RunOnce must drain handles and scavenge before another claim.
		return privateCleanupError(ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (r *Runner) closeICloudSource(remote *rclonesource.Source) error {
	if remote.Close() == nil {
		return nil
	}
	r.stagingDirty.Store(true)
	if !slices.Contains(r.pendingCleanup, remote) {
		r.pendingCleanup = append(r.pendingCleanup, remote)
	}
	return errPrivateCleanup
}

// Only the serial runner calls this, with no active CLI or staged file. Keep
// pinned handles until cleanup succeeds, and cover orphan files on its private
// root as well. A failure prevents even cancelled jobs from being claimed.
func (r *Runner) recoverICloudStaging() error {
	if !r.stagingDirty.Load() {
		return nil
	}
	r.pendingCleanup = slices.DeleteFunc(r.pendingCleanup, func(remote *rclonesource.Source) bool {
		return remote.Close() == nil
	})
	if ScavengeStaging(r.stagingRoot) != nil {
		return errPrivateCleanup
	}
	r.pendingCleanup = slices.DeleteFunc(r.pendingCleanup, func(remote *rclonesource.Source) bool {
		return remote.Close() == nil
	})
	if len(r.pendingCleanup) != 0 {
		return errPrivateCleanup
	}
	r.stagingDirty.Store(false)
	return nil
}

func remoteFailure(err error) ingestFailure {
	var safe *rclonesource.Error
	if errors.As(err, &safe) {
		switch safe.Code {
		case "source-auth-expired", "source-auth-failed", "source-adp-approval-required",
			"source-file-unavailable", "source-file-mutated", "source-directory-limit",
			"source-file-invalid", "source-config-invalid", "source-unavailable":
			return ingestFailure{safe.Code, safe.Summary}
		}
	}
	return ingestFailure{"source-unavailable", "Source is unavailable."}
}

func isRemoteAuthFailure(err error) bool {
	var failure *ingestFailure
	if errors.As(err, &failure) {
		return failure.code == "source-auth-expired" || failure.code == "source-auth-failed" || failure.code == "source-adp-approval-required"
	}
	var safe *rclonesource.Error
	return errors.As(err, &safe) && (safe.Code == "source-auth-expired" || safe.Code == "source-auth-failed" || safe.Code == "source-adp-approval-required")
}

func remoteSourceFiles(objects []rclonesource.Object) []sourceFile {
	files := make([]sourceFile, len(objects))
	for index := range objects {
		object := &objects[index]
		date, _ := time.Parse(time.DateOnly, object.Name[len("HealthAutoExport-"):len("HealthAutoExport-YYYY-MM-DD")])
		files[index] = sourceFile{
			name: object.Name, size: object.Size, modified: databaseTimestamp(object.Modified),
			observedModified: object.Modified, exportDate: date,
			remoteObject: object, observedIdentity: object.Identity,
		}
	}
	return files
}

// Staging inode/ctime validate only the bytes read here. Checkpoints and source
// inventory retain the original remote metadata and opaque transport identity.
func readStagedSourceFile(ctx context.Context, path string, remote sourceFile) (parsedSourceFile, *ingestFailure) {
	directory, err := sourceconfig.OpenDirectory(filepath.Dir(path), []string{filepath.Dir(path)})
	if err != nil {
		return parsedSourceFile{}, &ingestFailure{"source-file-unavailable", "A source file could not be read."}
	}
	defer directory.Close()
	file, info, err := sourceconfig.OpenRegularFile(directory, filepath.Base(path))
	if err != nil {
		return parsedSourceFile{}, &ingestFailure{"source-file-unavailable", "A source file could not be read."}
	}
	_ = file.Close()
	if !info.Mode().IsRegular() || info.Size() != remote.size {
		return parsedSourceFile{}, &ingestFailure{"source-file-mutated", "A source file changed while it was being read."}
	}
	local := sourceFile{name: filepath.Base(path), size: info.Size(), observedModified: info.ModTime()}
	local.device, local.inode, local.ctimeSec, local.ctimeNS = fileIdentity(info)
	parsed, failure := readSourceFile(ctx, directory, local)
	if failure == nil {
		parsed.sourceFile = remote
	}
	return parsed, failure
}

func (r *Runner) encryptICloudRefresh(job claimedJob, snapshot jobSourceSnapshot, original, updated sourceconfig.ICloud) ([]byte, error) {
	if original.Cookies == updated.Cookies {
		return nil, nil
	}
	// Only cookies are mutable; no subprocess changes to identity or credentials
	// can be promoted into the owner's current source configuration.
	original.Cookies = updated.Cookies
	_, canonical, err := sourceconfig.CanonicalizeICloud(original)
	if err != nil {
		return nil, errCredentialRefresh
	}
	envelope, err := r.keys.Encrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.SourceConfig, AccountID: job.accountID, RecordID: snapshot.sourceID,
	}, canonical)
	if err != nil {
		return nil, errCredentialRefresh
	}
	return envelope, nil
}

func (r *Runner) refreshICloudCredentials(ctx context.Context, job claimedJob, snapshot jobSourceSnapshot, original, updated sourceconfig.ICloud) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	envelope, err := r.encryptICloudRefresh(job, snapshot, original, updated)
	if err != nil || envelope == nil {
		return err
	}
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return errCredentialRefresh
	}
	defer tx.Rollback(ctx)
	var refreshed bool
	if err = tx.QueryRow(ctx, `SELECT app.refresh_source_credentials($1,$2,$3,$4,$5)`,
		job.id, r.workerID, job.lease, snapshot.generation, json.RawMessage(envelope)).Scan(&refreshed); err != nil {
		return errCredentialRefresh
	}
	if tx.Commit(ctx) != nil {
		return errCredentialRefresh
	}
	// A false fence is intentionally harmless: owner updates, cancellation,
	// deletion and first-writer-wins refreshes make these cookies obsolete.
	return nil
}

func (r *Runner) failSourceConnection(ctx context.Context, job claimedJob, snapshot jobSourceSnapshot, failure ingestFailure) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return errors.New("source-status-write-failed: Source connection status could not be saved.")
	}
	defer tx.Rollback(ctx)
	var updated bool
	if err = tx.QueryRow(ctx, `SELECT app.fail_source_connection($1,$2,$3,$4,$5,$6)`,
		job.id, r.workerID, job.lease, snapshot.generation, failure.code, failure.summary).Scan(&updated); err != nil {
		return errors.New("source-status-write-failed: Source connection status could not be saved.")
	}
	if tx.Commit(ctx) != nil {
		return errors.New("source-status-write-failed: Source connection status could not be saved.")
	}
	return nil
}
