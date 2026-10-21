package rclonesource

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
)

func testConfig() sourceconfig.ICloud {
	return sourceconfig.ICloud{Version: 1, Path: "exports", AppleID: appleCanary,
		Password: base64.RawURLEncoding.EncodeToString([]byte("PRIVATE-OBSCURED-PASSWORD-CANARY")),
		Cookies:  "sid=" + cookieCanary, TrustToken: trustCanary, ClientID: "client-canary"}
}

func newTestSource(t *testing.T) *Source {
	t.Helper()
	t.Setenv(helperMarker, "1")
	t.Setenv(helperScenario, "")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := New(context.Background(), binary, t.TempDir(), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	return source
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var safe *Error
	if !errors.As(err, &safe) || safe.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
	for _, canary := range []string{appleCanary, trustCanary, cookieCanary, testConfig().Password, contentCanary, "private.invalid"} {
		if strings.Contains(fmt.Sprintf("%+v %#v", err, err), canary) {
			t.Fatal("unsafe error")
		}
	}
}

func TestPrivateJobCLIContract(t *testing.T) {
	t.Setenv("RCLONE_CONFIG", "/PRIVATE-HOST-CONFIG-CANARY")
	t.Setenv("RCLONE_CONFIG_SOURCE_PASSWORD", "PRIVATE-HOST-PASSWORD-CANARY")
	t.Setenv("RCLONE_RC", "true")
	t.Setenv("rclone_hostile", "PRIVATE-HOST-CANARY")
	s := newTestSource(t)
	if !regexp.MustCompile(`^ingest-[0-9a-f]{32}$`).MatchString(s.jobName) {
		t.Fatal("unexpected job name")
	}
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects, err := s.List(context.Background())
	if err != nil || len(objects) != 2 {
		t.Fatalf("list: %d, %v", len(objects), err)
	}
	if objects[0].Name != "HealthAutoExport-2026-01-01.json" {
		t.Fatal("list not sorted")
	}
	path, err := s.Download(context.Background(), objects[0])
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(s.dir, "export.json") {
		t.Fatal("remote name used locally")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 5 {
		t.Fatal("invalid staged file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hello" {
		t.Fatal("download contents")
	}
	_, err = s.Download(context.Background(), objects[1])
	assertCode(t, err, "source-file-unavailable")
	if err := s.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Download(context.Background(), objects[1]); err != nil {
		t.Fatal(err)
	}
	assertCode(t, s.Remove(filepath.Join(s.dir, configName)), "source-file-invalid")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(s.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("job survived Close")
	}
	if s.Config() != testConfig() {
		t.Fatal("original profile changed")
	}
	assertCode(t, s.Check(context.Background()), "source-unavailable")
}

func TestDownloadFailuresRemovePartial(t *testing.T) {
	for _, test := range []struct{ scenario, code string }{
		{"cat-short", "source-file-mutated"}, {"cat-long", "source-file-mutated"},
		{"cat-large", "source-file-mutated"}, {"cat-exit", "source-file-unavailable"},
		{"pre-mutation", "source-file-mutated"}, {"post-mutation", "source-file-mutated"},
		{"stat-missing", "source-file-unavailable"},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			s := newTestSource(t)
			objects, err := s.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(helperScenario, test.scenario)
			path, err := s.Download(context.Background(), objects[0])
			assertCode(t, err, test.code)
			if path != "" {
				t.Fatal("failed download published")
			}
			for _, name := range []string{"export.partial", "export.json"} {
				if _, err := s.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed download left file")
				}
			}
			if test.scenario == "pre-mutation" {
				if _, err := s.root.Lstat("fake-cat"); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("cat ran after mutation")
				}
			}
		})
	}
}

func TestForgedDownloadMetadataRejected(t *testing.T) {
	s := newTestSource(t)
	objects, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*Object){
		func(o *Object) { o.Name = "../private.json" }, func(o *Object) { o.ID = "" },
		func(o *Object) { o.Size = maxFile + 1 }, func(o *Object) { o.Size = -1 },
		func(o *Object) { o.Modified = time.Time{} }, func(o *Object) { o.Identity = "forged" },
	} {
		object := objects[0]
		alter(&object)
		_, err := s.Download(context.Background(), object)
		assertCode(t, err, "source-file-invalid")
	}
}

func TestDownloadHardCapAndEmptyFile(t *testing.T) {
	for _, scenario := range []string{"max-file", "max-file-over", "zero-file"} {
		t.Run(scenario, func(t *testing.T) {
			s := newTestSource(t)
			t.Setenv(helperScenario, scenario)
			objects, err := s.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			path, err := s.Download(context.Background(), objects[0])
			if scenario == "max-file-over" {
				assertCode(t, err, "source-file-mutated")
				if path != "" {
					t.Fatal("oversized stream published")
				}
				if _, err := s.root.Lstat("export.partial"); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("oversized partial survived")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() != objects[0].Size {
				t.Fatal("size mismatch")
			}
		})
	}
}

func TestCancellationRemovesActiveDownload(t *testing.T) {
	s := newTestSource(t)
	objects, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperScenario, "cat-hang")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		path, err := s.Download(ctx, objects[0])
		if path != "" {
			result <- errors.New("cancelled download published")
			return
		}
		result <- err
	}()
	waitForFile(t, filepath.Join(s.dir, "fake-cat"))
	if _, err := s.root.Lstat("export.partial"); err != nil {
		t.Fatal("download did not create a partial")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download child survived cancellation")
	}
	for _, name := range []string{"export.partial", "export.json"} {
		if _, err := s.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled file survived")
		}
	}
}

func TestNamespaceUsesCanonicalAccountPathAndClient(t *testing.T) {
	s := newTestSource(t)
	binary := s.binary
	original := testConfig()
	for _, test := range []struct {
		change func(*sourceconfig.ICloud)
		same   bool
	}{
		{func(c *sourceconfig.ICloud) { c.AppleID = strings.ToLower(c.AppleID) }, true},
		{func(c *sourceconfig.ICloud) { c.Cookies = "sid=other-cookie" }, true},
		{func(c *sourceconfig.ICloud) { c.Path = "other-exports" }, false},
		{func(c *sourceconfig.ICloud) { c.ClientID = "other-client" }, false},
		{func(c *sourceconfig.ICloud) { c.ClientID = "" }, false},
	} {
		config := original
		test.change(&config)
		other, err := New(context.Background(), binary, t.TempDir(), config)
		if err != nil {
			t.Fatal(err)
		}
		if (other.namespace == s.namespace) != test.same {
			t.Fatal("namespace omitted identity input or included cookies")
		}
		if len(other.namespace) != 64 || strings.Contains(other.namespace, "PRIVATE") {
			t.Fatal("plaintext namespace")
		}
		if other.Config() != config {
			t.Fatal("namespace canonicalization modified original profile")
		}
		if err = other.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancellationTimeoutAndClose(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprint(parent), func(t *testing.T) {
			s := newTestSource(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if parent {
				s.ctx = ctx
				ctx = context.Background()
			}
			t.Setenv(helperScenario, "hang")
			result := make(chan error, 1)
			go func() { result <- s.Check(ctx) }()
			waitForFile(t, filepath.Join(s.dir, "fake-started"))
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("child survived cancellation")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(s.dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cancelled job survived Close")
			}
		})
	}
	t.Run("deadline", func(t *testing.T) {
		s := newTestSource(t)
		s.metadataTimeout = 50 * time.Millisecond
		t.Setenv(helperScenario, "hang")
		start := time.Now()
		err := s.Check(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
			t.Fatalf("deadline = %v", err)
		}
	})
	t.Run("download-deadline", func(t *testing.T) {
		s := newTestSource(t)
		objects, err := s.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		s.downloadTimeout = 50 * time.Millisecond
		t.Setenv(helperScenario, "hang")
		_, err = s.Download(context.Background(), objects[0])
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline = %v", err)
		}
	})
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fake child did not start")
}

func TestOrphanPipeHoldersKilled(t *testing.T) {
	s := newTestSource(t)
	t.Setenv(helperScenario, "orphan")
	start := time.Now()
	err := s.Check(context.Background())
	assertCode(t, err, "source-unavailable")
	if time.Since(start) > 4*time.Second {
		t.Fatal("orphan pipe blocked Wait")
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "fake-grandchild"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	// A killed child may remain a zombie until container PID 1 reaps it.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) || (err == nil && strings.Contains(string(data), ") Z ")) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("grandchild is still running")
}

func TestBoundedMetadataAndPrivateErrors(t *testing.T) {
	for _, test := range []struct{ scenario, code string }{
		{"stdout-limit", "source-directory-limit"}, {"private-error", "source-unavailable"},
		{"bad-json", "source-file-invalid"}, {"not-directory", "source-unavailable"},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			s := newTestSource(t)
			t.Setenv(helperScenario, test.scenario)
			var err error
			if test.scenario == "not-directory" {
				err = s.Check(context.Background())
			} else {
				_, err = s.List(context.Background())
			}
			assertCode(t, err, test.code)
		})
	}
}

func TestNewRejectsUnsafeRootAndCleansStartupFailure(t *testing.T) {
	t.Setenv(helperMarker, "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "staging-link")
	if err = os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{link, link + "/child", "/", "relative", filepath.Join(root, "missing")} {
		if s, err := New(context.Background(), binary, unsafe, testConfig()); err == nil {
			_ = s.Close()
			t.Fatal("unsafe staging root accepted")
		}
	}
	if err = os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err = New(context.Background(), binary, root, testConfig()); err == nil {
		t.Fatal("writable staging root accepted")
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperScenario, "wrong-version")
	_, err = New(context.Background(), binary, root, testConfig())
	assertCode(t, err, "source-unavailable")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("startup failure left secrets")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New(ctx, binary, root, testConfig())
	if !errors.Is(err, context.Canceled) {
		t.Fatal("New ignores cancellation")
	}
}

func TestCloseDoesNotFollowCacheLinks(t *testing.T) {
	s := newTestSource(t)
	external := t.TempDir()
	canary := filepath.Join(external, "canary")
	if err := os.WriteFile(canary, []byte(contentCanary), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(s.dir, "cache", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(canary)
	if err != nil || string(data) != contentCanary {
		t.Fatal("Close followed cache symlink")
	}
}

func TestCloseUsesPinnedStagingRoot(t *testing.T) {
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
	canary := filepath.Join(s.dir, "unowned-canary")
	if err := os.WriteFile(canary, []byte(contentCanary), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(moved, s.jobName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned directory survived root rename")
	}
	data, err := os.ReadFile(canary)
	if err != nil || string(data) != contentCanary {
		t.Fatal("Close removed unowned replacement directory")
	}
}

func TestActualRcloneVersion(t *testing.T) {
	binary, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--config", "/dev/null", "version")
	private := &Source{dir: t.TempDir()}
	cmd.Env = private.environment()
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("rclone version command failed")
	}
	if strings.SplitN(string(out), "\n", 2)[0] != "rclone v1.75.2" {
		t.Fatal("installed rclone does not match required v1.75.2")
	}
}
