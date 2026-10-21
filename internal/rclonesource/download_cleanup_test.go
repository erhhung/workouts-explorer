package rclonesource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDownloadPartialCleanupFailureAndDiscardRecovery(t *testing.T) {
	s := newTestSource(t)
	objects, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	removals := 0
	s.removeFile = func(root *os.Root, name string) error {
		if root != s.root || name != "export.partial" {
			t.Fatal("partial cleanup lost pinned ownership")
		}
		removals++
		return &os.PathError{Op: "remove", Path: contentCanary, Err: os.ErrPermission}
	}
	t.Setenv(helperScenario, "cat-short")
	path, err := s.Download(context.Background(), objects[0])
	assertCode(t, err, "source-cleanup-failed")
	var filesystem *os.PathError
	if errors.As(err, &filesystem) {
		t.Fatal("cleanup exposed filesystem error")
	}
	if path != "" || removals != 1 || s.closed {
		t.Fatal("failed partial cleanup changed download ownership")
	}
	if _, err := s.root.Lstat("export.partial"); err != nil {
		t.Fatal("partial was not retained by fixture")
	}
	s.removeFile = (*os.Root).Remove
	if err := s.DiscardDownload(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"export.partial", "export.json"} {
		if _, err := s.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("discard left staged file")
		}
	}
	t.Setenv(helperScenario, "")
	path, err = s.Download(context.Background(), objects[1])
	if err != nil || path == "" {
		t.Fatalf("next download after discard: %v", err)
	}
	if err := s.DiscardDownload(); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal("discard made source unusable")
	}
}

func TestDownloadCancellationCleanupFailurePreservesContext(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "deadline"}[deadline], func(t *testing.T) {
			s := newTestSource(t)
			objects, err := s.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			s.removeFile = func(*os.Root, string) error {
				return &os.PathError{Op: "remove", Path: cookieCanary, Err: os.ErrPermission}
			}
			t.Setenv(helperScenario, "cat-hang")
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				want = context.DeadlineExceeded
			}
			defer cancel()
			type downloadResult struct {
				path string
				err  error
			}
			result := make(chan downloadResult, 1)
			go func() { path, err := s.Download(ctx, objects[0]); result <- downloadResult{path, err} }()
			waitForFile(t, filepath.Join(s.dir, "fake-cat"))
			if !deadline {
				cancel()
			}
			select {
			case completed := <-result:
				if completed.path != "" {
					t.Fatal("cancelled download published")
				}
				assertCode(t, completed.err, "source-cleanup-failed")
				if !errors.Is(completed.err, want) {
					t.Fatal("cleanup failure hid context error")
				}
				var filesystem *os.PathError
				if errors.As(completed.err, &filesystem) {
					t.Fatal("cleanup exposed filesystem error")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("download child survived cancellation")
			}
			if _, err := s.root.Lstat("export.partial"); err != nil {
				t.Fatal("failed cleanup did not retain partial")
			}
			s.removeFile = (*os.Root).Remove
			if err := s.DiscardDownload(); err != nil {
				t.Fatal(err)
			}
			t.Setenv(helperScenario, "")
			if _, err := s.Download(context.Background(), objects[0]); err != nil {
				t.Fatal("source unusable after cancelled download discard")
			}
		})
	}
}

func TestDownloadActualPermissionCleanupFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode restrictions; injected permission tests still run")
	}
	s := newTestSource(t)
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0700) })
	objects, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperScenario, "cat-short-cleanup-denied")
	path, err := s.Download(context.Background(), objects[0])
	assertCode(t, err, "source-cleanup-failed")
	if path != "" || s.closed {
		t.Fatal("failed cleanup published a file or closed source")
	}
	if _, err := s.root.Lstat("export.partial"); err != nil {
		t.Fatal("permission fixture did not retain partial")
	}
	assertCode(t, s.DiscardDownload(), "source-cleanup-failed")
	if err := os.Chmod(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardDownload(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperScenario, "")
	if _, err := s.Download(context.Background(), objects[1]); err != nil {
		t.Fatal("next download after permission recovery failed")
	}
}

func TestDiscardDownloadAttemptsBothAndRemoveReportsCleanupFailure(t *testing.T) {
	s := newTestSource(t)
	for _, name := range []string{"export.partial", "export.json"} {
		if err := s.root.WriteFile(name, []byte(contentCanary), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var attempted []string
	s.removeFile = func(root *os.Root, name string) error {
		attempted = append(attempted, name)
		if root != s.root {
			t.Fatal("discard lost pinned root")
		}
		if name == "export.partial" {
			return &os.PathError{Op: "remove", Path: contentCanary, Err: os.ErrPermission}
		}
		return root.Remove(name)
	}
	assertCode(t, s.DiscardDownload(), "source-cleanup-failed")
	if !slices.Equal(attempted, []string{"export.partial", "export.json"}) {
		t.Fatal("discard stopped after first removal error")
	}
	if _, err := s.root.Lstat("export.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("discard did not remove second file")
	}
	s.removeFile = func(*os.Root, string) error {
		return &os.PathError{Op: "remove", Path: appleCanary, Err: os.ErrPermission}
	}
	assertCode(t, s.Remove(filepath.Join(s.dir, "export.json")), "source-cleanup-failed")
	s.removeFile = (*os.Root).Remove
	if err := s.DiscardDownload(); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardDownload(); err != nil {
		t.Fatal("discard did not ignore missing files")
	}
}

func TestDiscardDownloadPinnedRootNoLinksOrRemoteIO(t *testing.T) {
	s := newTestSource(t)
	parent := filepath.Dir(s.dir)
	moved := filepath.Join(t.TempDir(), "moved-staging")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(external, []byte(contentCanary), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"export.partial", "export.json"} {
		if err := s.root.Symlink(external, name); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dir, name), []byte(contentCanary), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx, s.binary = ctx, filepath.Join(s.dir, "nonexistent-cli")
	if err := s.DiscardDownload(); err != nil {
		t.Fatal("local discard used context or remote I/O")
	}
	for _, name := range []string{"export.partial", "export.json"} {
		if _, err := s.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("pinned staged link survived")
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil || string(data) != contentCanary {
			t.Fatal("discard removed unowned replacement file")
		}
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != contentCanary {
		t.Fatal("discard followed a staged symlink")
	}
	if s.closed || s.cleanupDone {
		t.Fatal("discard closed whole source")
	}
	if _, err := s.root.Stat(configName); err != nil {
		t.Fatal("discard touched private config")
	}
}

func TestDiscardDownloadRefusesPendingCloseCleanup(t *testing.T) {
	s := newTestSource(t)
	s.removeAll = func(*os.Root, string) error { return os.ErrPermission }
	t.Cleanup(func() { s.removeAll = (*os.Root).RemoveAll })
	assertCode(t, s.Close(), "source-cleanup-failed")
	removals := 0
	s.removeFile = func(*os.Root, string) error { removals++; return nil }
	assertCode(t, s.DiscardDownload(), "source-cleanup-failed")
	if removals != 0 {
		t.Fatal("discard used closed pending-cleanup source")
	}
	s.removeAll = (*os.Root).RemoveAll
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardDownload(); err != nil || removals != 0 {
		t.Fatal("discard after successful Close is not a no-op")
	}
}
