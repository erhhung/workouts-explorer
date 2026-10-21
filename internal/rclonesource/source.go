// Package rclonesource provides a job-scoped, noninteractive rclone 1.75.2
// iCloud Drive source on Linux. Source is not safe for concurrent use. The worker
// owns filtering and parsing exports, and must Close each Source even on failure.
package rclonesource

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"golang.org/x/sys/unix"
)

const (
	maxOutput   = 32 << 20
	maxFile     = 64 << 20
	maxEntries  = 100000
	maxEligible = 10000
	configName  = "rclone.conf"
)

// Object contains only validated flat export metadata. ID is private backend
// metadata and must not be logged. Identity is safe for persistence/logging.
// Identity deliberately does not hash file contents: a replacement preserving
// the same ID, size and modification time cannot be detected by this policy.
type Object struct {
	Name     string
	ID       string
	Size     int64
	Modified time.Time
	Identity string
}

// Source owns one private job directory, one source profile and one download slot.
type Source struct {
	ctx             context.Context
	binary          string
	parent          *os.Root
	root            *os.Root
	jobName         string
	dir             string
	config          sourceconfig.ICloud
	namespace       string
	metadataTimeout time.Duration
	downloadTimeout time.Duration
	closed          bool
	cleanupDone     bool
	removeAll       func(*os.Root, string) error
	removeFile      func(*os.Root, string) error
}

// New creates an owned ingest-<32 lowercase hex digits> directory under an
// existing, operator-controlled staging root. No host rclone config is used.
// It checks the exact CLI version without contacting any remote service.
// If constructor cleanup fails, New returns a closed, cleanup-only Source and
// source-cleanup-failed. The owner must retain the job for recovery and retry
// Close; an abandoned Source's os.Root handles are reclaimed by the runtime,
// but its files still require startup staging scavenging.
func New(ctx context.Context, binary, stagingRoot string, config sourceconfig.ICloud) (*Source, error) {
	return newSource(ctx, binary, stagingRoot, config, (*os.Root).RemoveAll)
}

func newSource(ctx context.Context, binary, stagingRoot string, config sourceconfig.ICloud, removeAll func(*os.Root, string) error) (source *Source, err error) {
	if ctx.Err() != nil {
		return nil, failure("source-unavailable", ctx.Err())
	}
	if !validConfig(config) {
		return nil, failure("source-config-invalid", nil)
	}
	resolved, err := exec.LookPath(binary)
	if err != nil || binary == "" {
		return nil, failure("source-unavailable", nil)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || !filepath.IsAbs(stagingRoot) || filepath.Clean(stagingRoot) == "/" {
		return nil, failure("source-config-invalid", nil)
	}
	// Open the entire path without symlinks, then transfer the pinned descriptor
	// to os.Root. /proc/self/fd is intentional, not an operator-supplied link.
	fd, err := unix.Openat2(unix.AT_FDCWD, stagingRoot, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, failure("source-unavailable", nil)
	}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0022 != 0 {
		_ = unix.Close(fd)
		return nil, failure("source-unavailable", nil)
	}
	parent, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	_ = unix.Close(fd)
	if err != nil {
		return nil, failure("source-unavailable", nil)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		_ = parent.Close()
		return nil, failure("source-unavailable", nil)
	}
	name := "ingest-" + hex.EncodeToString(random[:])
	if err = parent.Mkdir(name, 0700); err != nil {
		_ = parent.Close()
		return nil, failure("source-unavailable", nil)
	}
	s := &Source{ctx: ctx, binary: resolved, parent: parent, jobName: name,
		dir: filepath.Join(stagingRoot, name), config: config,
		metadataTimeout: time.Minute, downloadTimeout: 5 * time.Minute, removeAll: removeAll, removeFile: (*os.Root).Remove}
	defer func() {
		if err != nil {
			if cleanupErr := s.Close(); cleanupErr != nil {
				// Constructor errors are already sanitized. Retain only that safe
				// cause, never the filesystem error returned by RemoveAll.
				cleanup := failure("source-cleanup-failed", nil)
				cleanup.cause = err
				source, err = s, cleanup
			}
		}
	}()
	s.root, err = parent.OpenRoot(name)
	if err != nil {
		return nil, failure("source-unavailable", nil)
	}
	// JSON framing avoids ambiguous concatenation. Namespace never contains
	// plaintext account identity and intentionally excludes refreshable cookies.
	identity, _ := json.Marshal([]string{strings.ToLower(strings.TrimSpace(config.AppleID)), config.Path, config.ClientID})
	hash := sha256.Sum256(identity)
	s.namespace = hex.EncodeToString(hash[:])
	for _, directory := range []string{"cache", "home", "xdg-config", "xdg-cache", "tmp"} {
		if err = s.root.Mkdir(directory, 0700); err != nil {
			return nil, failure("source-unavailable", nil)
		}
	}
	if err = s.writeConfig(); err != nil {
		return nil, err
	}
	var version bytes.Buffer
	if err = s.run(ctx, s.metadataTimeout, []string{"version"}, &version, 4096, "source-unavailable"); err != nil {
		return nil, err
	}
	if strings.SplitN(version.String(), "\n", 2)[0] != "rclone v1.75.2" {
		return nil, failure("source-unavailable", nil)
	}
	return s, nil
}

// Config returns the original profile with only validated refreshed Cookies
// changed. Call after each operation, including failures, before Close if the
// worker wants to persist a refresh. It remains available after Close.
func (s *Source) Config() sourceconfig.ICloud { return s.config }

// Close removes all owned files, config backups, endpoint caches and partial
// exports through the pinned staging root, without following symlinks. It does
// not depend on either context. Operations are disabled on the first call;
// failed cleanup returns source-cleanup-failed and retains the pinned roots for
// another Close attempt. Only successful cleanup makes later calls a no-op.
func (s *Source) Close() error {
	if s.cleanupDone {
		return nil
	}
	s.closed = true
	if s.parent != nil {
		if err := s.removeAll(s.parent, s.jobName); err != nil {
			return failure("source-cleanup-failed", nil)
		}
	}
	var err error
	if s.root != nil {
		err = errors.Join(err, s.root.Close())
	}
	if s.parent != nil {
		err = errors.Join(err, s.parent.Close())
	}
	if err != nil {
		return failure("source-cleanup-failed", nil)
	}
	s.cleanupDone = true
	return nil
}

func (s *Source) remote(name string) string {
	remote := "source:" + s.config.Path
	if name != "" {
		if s.config.Path != "" {
			remote += "/"
		}
		remote += name
	}
	return remote
}

func (s *Source) stat(ctx context.Context, name, code string) (metadata, error) {
	var out bytes.Buffer
	err := s.run(ctx, s.metadataTimeout, []string{"lsjson", "--stat", "--no-mimetype", "--", s.remote(name)}, &out, maxOutput, code)
	if err != nil {
		return metadata{}, err
	}
	item, err := decodeMetadata(json.RawMessage(out.Bytes()))
	if err != nil {
		return metadata{}, failure(code, nil)
	}
	return item, nil
}

// Check validates directory existence using bounded lsjson --stat. Rclone's
// stat implementation may list the directory internally, but downloads no files.
func (s *Source) Check(ctx context.Context) error {
	item, err := s.stat(ctx, "", "source-unavailable")
	if err != nil {
		return err
	}
	if !item.isDir {
		return failure("source-unavailable", nil)
	}
	return nil
}

// List returns only dated HealthAutoExport JSON files, sorted by date/name.
// Unknown native metadata fields are ignored within the total output bound.
// Missing ID/size/mtime on an eligible file fails closed rather than silently
// skipping it. No recursion, hashes or MIME lookup is requested.
func (s *Source) List(ctx context.Context) ([]Object, error) {
	var out bytes.Buffer
	if err := s.run(ctx, s.metadataTimeout, []string{"lsjson", "--max-depth", "1", "--no-mimetype", "--", s.remote("")}, &out, maxOutput, "source-unavailable"); err != nil {
		return nil, err
	}
	return s.parseList(out.Bytes())
}

// Download stages exactly one previously listed object as export.json (0600).
// The worker must Remove the returned path before using the next download slot.
// Pre/post stat commands add backend lookups and detect metadata changes,
// not replacements preserving all identity metadata. Download plus both stats
// share a five-minute deadline. Failed partial-file cleanup returns
// source-cleanup-failed while preserving context cancellation/deadline detection;
// the worker must confirm DiscardDownload or Close succeeds before releasing its
// durable slot.
func (s *Source) Download(ctx context.Context, object Object) (path string, err error) {
	if s.closed {
		return "", failure("source-unavailable", nil)
	}
	if !eligibleName(object.Name) || !validObject(object) || object.Identity != s.objectIdentity(object) {
		return "", failure("source-file-invalid", nil)
	}
	if _, statErr := s.root.Lstat("export.json"); !errors.Is(statErr, os.ErrNotExist) {
		return "", failure("source-file-unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, s.downloadTimeout)
	defer cancel()
	before, err := s.stat(ctx, object.Name, "source-file-unavailable")
	if err != nil {
		return "", err
	}
	if !matches(before, object) {
		return "", failure("source-file-mutated", nil)
	}
	f, err := s.root.OpenFile("export.partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", failure("source-file-unavailable", nil)
	}
	defer func() {
		_ = f.Close()
		if path == "" {
			if cleanupErr := s.removeStaged("export.partial"); cleanupErr != nil {
				err = failure("source-cleanup-failed", err)
			}
		}
	}()
	// A full file remote can fall back to a directory in rclone. Instead, cat
	// one exact flat filename from the checked directory, without recursion.
	args := []string{"cat", "--include", "/" + object.Name, "--max-depth", "1", "--transfers", "1", "--", s.remote("")}
	if err = s.run(ctx, s.downloadTimeout, args, f, object.Size, "source-file-unavailable"); err != nil {
		return "", err
	}
	info, err := f.Stat()
	if err != nil {
		return "", failure("source-file-unavailable", nil)
	}
	if info.Size() != object.Size {
		return "", failure("source-file-mutated", nil)
	}
	if err = f.Sync(); err != nil {
		return "", failure("source-file-unavailable", nil)
	}
	if err = f.Close(); err != nil {
		return "", failure("source-file-unavailable", nil)
	}
	after, err := s.stat(ctx, object.Name, "source-file-unavailable")
	if err != nil {
		return "", err
	}
	if !matches(after, object) {
		return "", failure("source-file-mutated", nil)
	}
	if err = ctx.Err(); err != nil {
		return "", failure("source-unavailable", err)
	}
	if err = s.ctx.Err(); err != nil {
		return "", failure("source-unavailable", err)
	}
	if err = s.root.Rename("export.partial", "export.json"); err != nil {
		return "", failure("source-file-unavailable", nil)
	}
	return filepath.Join(s.dir, "export.json"), nil
}

// Remove releases the one local download slot. Only the exact path returned by
// Download is accepted. This never removes a remote file and ignores a missing
// staged file; successful Close cleanup also releases the slot.
// After a failed Close, Remove reports source-cleanup-failed until Close succeeds.
// Other removal failures also report source-cleanup-failed, without filesystem details.
func (s *Source) Remove(path string) error {
	if path != filepath.Join(s.dir, "export.json") {
		return failure("source-file-invalid", nil)
	}
	return s.removeStaged("export.json")
}

// DiscardDownload removes both export.partial and export.json through the pinned
// job root, ignoring missing files and attempting both removals even if one
// fails. It performs no remote I/O, ignores cancellation, and leaves an open
// Source usable. After a failed Close it returns source-cleanup-failed; after a
// successful Close it is a no-op. Release a durable slot only after this method
// or Close succeeds.
func (s *Source) DiscardDownload() error {
	return s.removeStaged("export.partial", "export.json")
}

func (s *Source) removeStaged(names ...string) error {
	if s.closed {
		if !s.cleanupDone {
			return failure("source-cleanup-failed", nil)
		}
		return nil
	}
	failed := false
	for _, name := range names {
		if err := s.removeFile(s.root, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = true
		}
	}
	if failed {
		return failure("source-cleanup-failed", nil)
	}
	return nil
}

// boundedWriter kills the process as soon as a stream exceeds its hard cap.
// It writes at most limit bytes, including when the final write crosses it.
type boundedWriter struct {
	dst       io.Writer
	remaining int64
	kill      func()
	exceeded  bool
	failed    bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		w.exceeded = true
		w.kill()
		p = p[:int(w.remaining)]
		n, _ := w.dst.Write(p)
		w.remaining -= int64(n)
		return n, io.ErrShortWrite
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	if err != nil || n != len(p) {
		w.failed = true
		w.kill()
		return n, io.ErrShortWrite
	}
	return n, nil
}
