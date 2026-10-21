package rclonesource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseRetriesFailedRemoval(t *testing.T) {
	s := newTestSource(t)
	parent, root := s.parent, s.root
	attempts := 0
	s.removeAll = func(directory *os.Root, name string) error {
		attempts++
		if directory != parent || name != s.jobName {
			t.Fatal("cleanup lost pinned ownership")
		}
		return &os.PathError{Op: "remove", Path: contentCanary, Err: os.ErrPermission}
	}
	for i := 0; i < 2; i++ {
		assertCode(t, s.Close(), "source-cleanup-failed")
		if !s.closed || s.cleanupDone {
			t.Fatal("failed cleanup marked complete or left operations enabled")
		}
		if _, err := parent.Stat(s.jobName); err != nil {
			t.Fatal("pinned cleanup descriptor was closed")
		}
		if _, err := root.ReadFile(configName); err != nil {
			t.Fatal("fixture did not retain private config")
		}
	}
	if attempts != 2 {
		t.Fatal("repeated Close did not retry")
	}
	assertCode(t, s.Check(context.Background()), "source-unavailable")
	_, err := s.List(context.Background())
	assertCode(t, err, "source-unavailable")
	_, err = s.Download(context.Background(), Object{})
	assertCode(t, err, "source-unavailable")
	assertCode(t, s.Remove(filepath.Join(s.dir, "export.json")), "source-cleanup-failed")
	// Replacing the pathname must not redirect a retry away from the pinned root.
	staging := filepath.Dir(s.dir)
	moved := filepath.Join(t.TempDir(), "moved-staging")
	if err := os.Rename(staging, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(s.dir, "unowned-canary")
	if err := os.WriteFile(canary, []byte(contentCanary), 0600); err != nil {
		t.Fatal(err)
	}
	s.removeAll = func(directory *os.Root, name string) error {
		attempts++
		if directory != parent {
			t.Fatal("cleanup reopened staging pathname")
		}
		return directory.RemoveAll(name)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !s.cleanupDone || attempts != 3 {
		t.Fatal("successful cleanup not recorded")
	}
	if _, err := os.Lstat(filepath.Join(moved, s.jobName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned files survived retry")
	}
	data, err := os.ReadFile(canary)
	if err != nil || string(data) != contentCanary {
		t.Fatal("retry removed unowned files")
	}
	if _, err := parent.Stat("."); err == nil {
		t.Fatal("parent descriptor leaked after success")
	}
	if _, err := root.Stat("."); err == nil {
		t.Fatal("job descriptor leaked after success")
	}
	if err := s.Close(); err != nil || attempts != 3 {
		t.Fatal("completed cleanup was not idempotent")
	}
}

func TestCloseRetriesActualPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem mode restrictions; injected permission test still runs")
	}
	s := newTestSource(t)
	t.Cleanup(func() {
		_ = os.Chmod(s.dir, 0700)
	})
	if err := os.Chmod(s.dir, 0500); err != nil {
		t.Fatal(err)
	}
	assertCode(t, s.Close(), "source-cleanup-failed")
	assertCode(t, s.Close(), "source-cleanup-failed")
	if _, err := os.Stat(filepath.Join(s.dir, configName)); err != nil {
		t.Fatal("private config was not retained by fixture")
	}
	if err := os.Chmod(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("permission recovery did not remove job")
	}
}

func TestConstructorCleanupFailureIsRecoverableAndSafe(t *testing.T) {
	t.Setenv(helperMarker, "1")
	t.Setenv(helperScenario, "wrong-version")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	attempts := 0
	s, err := newSource(context.Background(), binary, staging, testConfig(), func(directory *os.Root, name string) error {
		attempts++
		if _, err := directory.Stat(filepath.Join(name, configName)); err != nil {
			t.Fatal("constructor fixture failed before writing private config")
		}
		return &os.PathError{Op: "remove", Path: appleCanary, Err: os.ErrPermission}
	})
	assertCode(t, err, "source-cleanup-failed")
	assertCode(t, errors.Unwrap(err), "source-unavailable")
	if s == nil {
		t.Fatal("constructor discarded cleanup-only source")
	}
	t.Cleanup(func() {
		s.removeAll = (*os.Root).RemoveAll
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if !s.closed || s.cleanupDone || attempts != 1 {
		t.Fatal("constructor cleanup state is invalid")
	}
	assertCode(t, s.Check(context.Background()), "source-unavailable")
	assertCode(t, s.Close(), "source-cleanup-failed")
	if attempts != 2 {
		t.Fatal("constructor cleanup cannot be retried")
	}
	data, readErr := s.root.ReadFile(configName)
	if readErr != nil || len(data) == 0 {
		t.Fatal("private config fixture was lost")
	}
	s.removeAll = (*os.Root).RemoveAll
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor recovery left private files")
	}
}

func TestConstructorCleanupFailurePreservesCancellation(t *testing.T) {
	t.Setenv(helperMarker, "1")
	t.Setenv(helperScenario, "version-hang")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type constructorResult struct {
		source *Source
		err    error
	}
	result := make(chan constructorResult, 1)
	go func() {
		s, err := newSource(ctx, binary, staging, testConfig(), func(*os.Root, string) error {
			return &os.PathError{Op: "remove", Path: cookieCanary, Err: os.ErrPermission}
		})
		result <- constructorResult{s, err}
	}()
	waitForFile(t, filepath.Join(staging, "fake-version-started"))
	cancel()
	var s *Source
	select {
	case completed := <-result:
		s, err = completed.source, completed.err
	case <-time.After(3 * time.Second):
		t.Fatal("constructor child survived cancellation")
	}
	assertCode(t, err, "source-cleanup-failed")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("constructor cleanup hid cancellation")
	}
	assertCode(t, errors.Unwrap(err), "source-unavailable")
	if s == nil {
		t.Fatal("constructor discarded cleanup-only source")
	}
	t.Cleanup(func() {
		s.removeAll = (*os.Root).RemoveAll
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, readErr := s.root.ReadFile(configName); readErr != nil {
		t.Fatal("constructor fixture did not retain private config")
	}
}

func TestPublicConstructorReportsActualCleanupPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem mode restrictions; injected constructor test still runs")
	}
	t.Setenv(helperMarker, "1")
	t.Setenv(helperScenario, "wrong-version-cleanup-denied")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), binary, t.TempDir(), testConfig())
	assertCode(t, err, "source-cleanup-failed")
	assertCode(t, errors.Unwrap(err), "source-config-invalid")
	if s == nil {
		t.Fatal("constructor discarded cleanup-only source")
	}
	t.Cleanup(func() {
		_ = os.Chmod(s.dir, 0700)
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	assertCode(t, s.Close(), "source-cleanup-failed")
	if err := os.Chmod(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor recovery left private files")
	}
}
