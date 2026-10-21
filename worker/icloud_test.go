package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/rclonesource"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
)

// Only synthetic files under the test directory are used by this CLI fixture.
func TestWorkerRcloneHelper(t *testing.T) {
	if os.Getenv("WORKER_FAKE_RCLONE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	configPath := ""
	command := ""
	for index, arg := range args {
		if arg == "--config" {
			configPath = args[index+1]
		}
		if arg == "version" || arg == "lsjson" || arg == "cat" {
			command = arg
		}
	}
	root := os.Getenv("WORKER_FAKE_ROOT")
	call, err := os.OpenFile(filepath.Join(root, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(30)
	}
	fmt.Fprintln(call, command)
	_ = call.Close()
	modeBytes, _ := os.ReadFile(filepath.Join(root, "mode"))
	mode := string(modeBytes)
	if command == "version" {
		if mode == "constructor-cleanup-failure" {
			if os.Chmod(filepath.Dir(filepath.Dir(configPath)), 0500) != nil {
				os.Exit(37)
			}
			fmt.Println("rclone v0.0.0")
			os.Exit(0)
		}
		fmt.Println("rclone v1.75.2")
		os.Exit(0)
	}
	profile, err := os.ReadFile(configPath)
	if err != nil {
		os.Exit(31)
	}
	if strings.HasPrefix(mode, "refresh") {
		profile = bytes.ReplaceAll(profile, []byte("cookies = session=fixture-cookies"), []byte("cookies = session=refreshed-cookies"))
		if os.WriteFile(configPath, profile, 0600) != nil {
			os.Exit(32)
		}
	}
	if mode == "auth" || mode == "adp" || mode == "transient" || ((mode == "download-auth" || mode == "refresh-download-auth" || mode == "refresh-transient") && command == "cat") {
		message := "private-payload https://private.example.test/path fixture-password"
		if mode == "auth" || mode == "download-auth" || mode == "refresh-download-auth" {
			message = "trust token expired, please reauth"
		} else if mode == "adp" {
			message = "Missing PCS cookies from the request"
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
	if mode == "cleanup-failure" {
		// RemoveAll may remove the job's contents, but cannot unlink its directory.
		if os.Chmod(filepath.Dir(filepath.Dir(configPath)), 0500) != nil {
			os.Exit(37)
		}
	}
	if command == "cat" {
		if mode == "hang" {
			_ = os.WriteFile(filepath.Join(root, "started"), []byte("started"), 0600)
			time.Sleep(time.Minute)
			os.Exit(33)
		}
		name := ""
		for index, arg := range args {
			if arg == "--include" {
				name = strings.TrimPrefix(args[index+1], "/")
			}
		}
		data, err := os.ReadFile(filepath.Join(root, "exports", name))
		if err != nil {
			os.Exit(34)
		}
		if mode == "partial-cleanup-failure" {
			_, _ = os.Stdout.Write(data[:len(data)/2])
			if os.Chmod(filepath.Dir(configPath), 0500) != nil {
				os.Exit(37)
			}
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(data)
		os.Exit(0)
	}
	metadata := func(name string) map[string]any {
		info, err := os.Stat(filepath.Join(root, "exports", name))
		if err != nil {
			os.Exit(35)
		}
		return map[string]any{"Name": name, "Path": name, "ID": "fixture-id-" + name,
			"Size": info.Size(), "ModTime": info.ModTime().UTC().Format(time.RFC3339Nano), "IsDir": false}
	}
	if slices.Contains(args, "--stat") {
		remote := args[len(args)-1]
		if remote == "source:fixture-exports" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"Name": "fixture-exports", "Path": "fixture-exports", "IsDir": true})
		} else {
			name := filepath.Base(remote)
			if name == "HealthAutoExport-2026-08-01.json" && mode == "first-unavailable" {
				fmt.Fprintln(os.Stderr, "fixture file disappeared")
				os.Exit(1)
			}
			item := metadata(name)
			if name == "HealthAutoExport-2026-08-01.json" && mode == "first-mutated" {
				item["ID"] = "changed-fixture-id"
			}
			_ = json.NewEncoder(os.Stdout).Encode(item)
		}
	} else {
		entries, err := os.ReadDir(filepath.Join(root, "exports"))
		if err != nil {
			os.Exit(36)
		}
		items := make([]map[string]any, 0)
		for _, entry := range entries {
			items = append(items, metadata(entry.Name()))
		}
		_ = json.NewEncoder(os.Stdout).Encode(items)
	}
	os.Exit(0)
}

func fakeWorkerRclone(t *testing.T) (binary, root, staging string) {
	t.Helper()
	root, staging = t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "exports"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_FAKE_RCLONE", "1")
	t.Setenv("WORKER_FAKE_ROOT", root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(root, "rclone-fixture")
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec "+quoted+" -test.run=^TestWorkerRcloneHelper$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return binary, root, staging
}

func syntheticICloudConfig() sourceconfig.ICloud {
	return sourceconfig.ICloud{Version: 1, Path: "fixture-exports", AppleID: "fixture@example.test",
		Password: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		Cookies:  "session=fixture-cookies", TrustToken: "fixture-trust", ClientID: "fixture-client"}
}

func syntheticICloudSnapshot(t *testing.T, r *Runner, job claimedJob, config sourceconfig.ICloud) jobSourceSnapshot {
	t.Helper()
	_, canonical, err := sourceconfig.CanonicalizeICloud(config)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := r.keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot,
		AccountID: job.accountID, RecordID: job.id}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	return jobSourceSnapshot{sourceID: uuid.New(), generation: 1, sourceType: sourceconfig.ICloudType, envelope: envelope}
}

func assertEmptyStaging(t *testing.T, staging string) {
	t.Helper()
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private staging not empty: count=%d err=%v", len(entries), err)
	}
}

func setFakeMode(t *testing.T, root, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestICloudConnectionCleanupAndRefresh(t *testing.T) {
	binary, root, staging := fakeWorkerRclone(t)
	r := NewRunnerWithOptions(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), testKeyring(t), nil,
		RunnerOptions{RcloneBinary: binary, StagingRoot: staging})
	job := claimedJob{id: uuid.New(), accountID: uuid.New()}
	config := syntheticICloudConfig()
	snapshot := syntheticICloudSnapshot(t, r, job, config)
	for _, mode := range []string{"", "refresh", "auth", "adp", "transient"} {
		t.Run(mode, func(t *testing.T) {
			setFakeMode(t, root, mode)
			result, err := r.checkICloudConnection(context.Background(), job, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			assertEmptyStaging(t, staging)
			if mode == "" || mode == "refresh" {
				if result.status != "connected" {
					t.Fatalf("result=%+v", result)
				}
			} else if result.status != "connection-failed" || result.refreshedEncrypted != nil {
				t.Fatalf("failed check returned refresh or success: %+v", result)
			}
			if mode == "auth" && *result.code != "source-auth-expired" {
				t.Fatalf("code=%s", *result.code)
			}
			if mode == "adp" && *result.code != "source-adp-approval-required" {
				t.Fatalf("code=%s", *result.code)
			}
			if mode == "refresh" {
				plain, err := r.keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig,
					AccountID: job.accountID, RecordID: snapshot.sourceID}, result.refreshedEncrypted)
				if err != nil {
					t.Fatal(err)
				}
				updated, _, err := sourceconfig.DecodeICloud(plain)
				config.Cookies = "session=refreshed-cookies"
				if err != nil || updated != config {
					t.Fatal("refresh changed fields other than cookies")
				}
				if _, err := r.keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.JobConfigSnapshot,
					AccountID: job.accountID, RecordID: job.id}, result.refreshedEncrypted); err == nil {
					t.Fatal("source envelope usable as job snapshot")
				}
			} else if result.refreshedEncrypted != nil {
				t.Fatal("unchanged or failed check generated refresh")
			}
			encoded := []byte(result.status + " " + stringValue(result.code) + " " + stringValue(result.summary) + " " + string(result.refreshedEncrypted))
			for _, secret := range []string{root, staging, config.Password, config.Cookies, config.TrustToken, "private.example.test"} {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Fatal("connection result leaked private fixture")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.checkICloudConnection(ctx, job, snapshot); err != context.Canceled {
		t.Fatalf("cancelled check=%v", err)
	}
	assertEmptyStaging(t, staging)
}

func TestICloudStagedParserParityAndIdentity(t *testing.T) {
	binary, root, staging := fakeWorkerRclone(t)
	name := "HealthAutoExport-2026-08-01.json"
	path := filepath.Join(root, "exports", name)
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			writeSyntheticExport(t, path, "60.125", 2)
			if invalid {
				if err := os.WriteFile(path, []byte("private-invalid-payload"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			remote, err := rclonesource.New(context.Background(), binary, staging, syntheticICloudConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer remote.Close()
			objects, err := remote.List(context.Background())
			if err != nil || len(objects) != 1 {
				t.Fatalf("objects=%d err=%v", len(objects), err)
			}
			candidate := remoteSourceFiles(objects)[0]
			staged, err := remote.Download(context.Background(), objects[0])
			if err != nil {
				t.Fatal(err)
			}
			parsedRemote, failureRemote := readStagedSourceFile(context.Background(), staged, candidate)
			directory, err := sourceconfig.OpenDirectory(filepath.Dir(path), []string{root})
			if err != nil {
				t.Fatal(err)
			}
			files, err := discoverSourceFiles(directory)
			if err != nil {
				t.Fatal(err)
			}
			parsedLocal, failureLocal := readSourceFile(context.Background(), directory, files[0])
			_ = directory.Close()
			if !reflect.DeepEqual(failureRemote, failureLocal) || !reflect.DeepEqual(parsedRemote.document, parsedLocal.document) || parsedRemote.checksum != parsedLocal.checksum {
				t.Fatal("remote and local normalization differ")
			}
			if !invalid && (parsedRemote.observedIdentity != objects[0].Identity || parsedRemote.inode != 0 || parsedRemote.device != 0 || parsedRemote.ctimeSec != 0 || parsedRemote.name != name) {
				t.Fatal("staging inode or filename leaked into remote checkpoint")
			}
			if sourceIdentity(candidate) != objects[0].Identity || sourceIdentity(candidate) == sourceIdentity(files[0]) {
				t.Fatal("remote identity is not opaque")
			}
			if candidate.modified != databaseTimestamp(objects[0].Modified) {
				t.Fatal("checkpoint lost remote modification time")
			}
			if err := remote.Remove(staged); err != nil {
				t.Fatal(err)
			}
			if _, err := remote.Download(context.Background(), objects[0]); err != nil {
				t.Fatal("removed file did not release download slot")
			}
			if err := remote.Close(); err != nil {
				t.Fatal(err)
			}
			assertEmptyStaging(t, staging)
		})
	}
}

func TestICloudRefreshOnlyCookiesAndNoop(t *testing.T) {
	r := NewRunner(nil, slog.Default(), testKeyring(t), nil)
	job := claimedJob{id: uuid.New(), accountID: uuid.New()}
	snapshot := jobSourceSnapshot{sourceID: uuid.New()}
	original := syntheticICloudConfig()
	updated := original
	updated.Password = "untrusted-password"
	if envelope, err := r.encryptICloudRefresh(job, snapshot, original, updated); err != nil || envelope != nil {
		t.Fatal("non-cookie changes caused refresh")
	}
	updated.Cookies = "new-cookies"
	envelope, err := r.encryptICloudRefresh(job, snapshot, original, updated)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := r.keys.Decrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: job.accountID, RecordID: snapshot.sourceID}, envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := sourceconfig.DecodeICloud(plain)
	original.Cookies = "new-cookies"
	if err != nil || decoded != original {
		t.Fatal("non-cookie changes persisted")
	}
	updated.Cookies = "invalid\ncookies"
	if _, err := r.encryptICloudRefresh(job, snapshot, original, updated); err != errCredentialRefresh {
		t.Fatalf("invalid refresh=%v", err)
	}
}

func TestICloudConnectionCleanupFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission fences")
	}
	binary, root, staging := fakeWorkerRclone(t)
	t.Cleanup(func() { _ = os.Chmod(staging, 0700) })
	setFakeMode(t, root, "cleanup-failure")
	r := NewRunnerWithOptions(nil, slog.Default(), testKeyring(t), nil, RunnerOptions{RcloneBinary: binary, StagingRoot: staging})
	job := claimedJob{id: uuid.New(), accountID: uuid.New()}
	snapshot := syntheticICloudSnapshot(t, r, job, syntheticICloudConfig())
	if _, err := r.checkICloudConnection(context.Background(), job, snapshot); err != errPrivateCleanup {
		t.Fatalf("cleanup error=%v", err)
	}
}

func TestICloudConstructorError(t *testing.T) {
	for _, test := range []struct {
		name      string
		code      string
		cancelled bool
		cleanup   bool
	}{
		{"cleanup without handle", "source-cleanup-failed", false, true},
		{"cancelled cleanup", "source-cleanup-failed", true, true},
		{"cancelled auth classification", "source-auth-expired", true, false},
		{"ordinary constructor failure", "source-unavailable", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancelled {
				cancel()
			}
			original := &rclonesource.Error{Code: test.code, Summary: "Safe fixture failure."}
			r := &Runner{}
			err := r.icloudConstructorError(ctx, nil, original)
			if errors.Is(err, errPrivateCleanup) != test.cleanup || errors.Is(err, context.Canceled) != test.cancelled {
				t.Fatalf("constructor error=%v", err)
			}
			if test.cancelled && isRemoteAuthFailure(err) {
				t.Fatal("parent cancellation became an authentication failure")
			}
			if !test.cancelled && !test.cleanup && err != original {
				t.Fatal("ordinary constructor error changed")
			}
			if r.StagingCleanupPending() != test.cleanup {
				t.Fatal("cleanup failure did not gate readiness")
			}
		})
	}
}

func TestICloudConstructorCleanupRetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission fences")
	}
	binary, root, staging := fakeWorkerRclone(t)
	t.Cleanup(func() { _ = os.Chmod(staging, 0700) })
	setFakeMode(t, root, "constructor-cleanup-failure")
	remote, constructorErr := rclonesource.New(context.Background(), binary, staging, syntheticICloudConfig())
	var safe *rclonesource.Error
	if remote == nil || !errors.As(constructorErr, &safe) || safe.Code != "source-cleanup-failed" {
		t.Fatalf("constructor cleanup contract: source present=%t err=%v", remote != nil, constructorErr)
	}
	t.Cleanup(func() {
		_ = os.Chmod(staging, 0700)
		_ = remote.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{stagingRoot: staging}
	if err := r.icloudConstructorError(ctx, remote, constructorErr); !errors.Is(err, errPrivateCleanup) || !errors.Is(err, context.Canceled) {
		t.Fatalf("failed retry lost recoverability or cancellation: %v", err)
	}
	if err := os.Chmod(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.icloudConstructorError(context.Background(), remote, constructorErr); err != errPrivateCleanup {
		t.Fatalf("successful retry converted cleanup failure to terminal result: %v", err)
	}
	assertEmptyStaging(t, staging)
	if err := r.recoverICloudStaging(); err != nil || len(r.pendingCleanup) != 0 || r.StagingCleanupPending() {
		t.Fatalf("cleanup handle was not drained: %v", err)
	}
}

func TestICloudCleanupGateBeforeClaim(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission fences")
	}
	binary, root, staging := fakeWorkerRclone(t)
	t.Cleanup(func() { _ = os.Chmod(staging, 0700) })
	// An orphan without a live source handle must be scavenged as well.
	orphan := filepath.Join(staging, "ingest-"+strings.Repeat("a", 32))
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "rclone.conf"), []byte("synthetic-private-config"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewRunnerWithOptions(nil, slog.Default(), testKeyring(t), nil, RunnerOptions{RcloneBinary: binary, StagingRoot: staging})
	job := claimedJob{id: uuid.New(), accountID: uuid.New()}
	setFakeMode(t, root, "cleanup-failure")
	if _, err := r.checkICloudConnection(context.Background(), job, syntheticICloudSnapshot(t, r, job, syntheticICloudConfig())); !errors.Is(err, errPrivateCleanup) {
		t.Fatalf("cleanup failure=%v", err)
	}
	if len(r.pendingCleanup) != 1 || !r.StagingCleanupPending() {
		t.Fatal("failed handle was lost or readiness remained true")
	}
	// nil DB is a canary: failed cleanup must stop RunOnce before any claim.
	if worked, err := r.RunOnce(context.Background()); worked || !errors.Is(err, errPrivateCleanup) {
		t.Fatalf("unsafe claim: worked=%t err=%v", worked, err)
	}
	if err := os.Chmod(staging, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if worked, err := r.RunOnce(ctx); worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery ignored parent cancellation: worked=%t err=%v", worked, err)
	}
	assertEmptyStaging(t, staging)
	if len(r.pendingCleanup) != 0 || r.StagingCleanupPending() {
		t.Fatal("successful cleanup did not release handles and readiness gate")
	}
}

func TestICloudConnectionConstructorCleanupFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission fences")
	}
	binary, root, staging := fakeWorkerRclone(t)
	t.Cleanup(func() { _ = os.Chmod(staging, 0700) })
	setFakeMode(t, root, "constructor-cleanup-failure")
	r := NewRunnerWithOptions(nil, slog.Default(), testKeyring(t), nil, RunnerOptions{RcloneBinary: binary, StagingRoot: staging})
	job := claimedJob{id: uuid.New(), accountID: uuid.New()}
	snapshot := syntheticICloudSnapshot(t, r, job, syntheticICloudConfig())
	result, err := r.checkICloudConnection(context.Background(), job, snapshot)
	if !errors.Is(err, errPrivateCleanup) || result.status != "" {
		t.Fatalf("constructor cleanup became a connection result: status=%s err=%v", result.status, err)
	}
}

func TestManifestIdentityOptional(t *testing.T) {
	for _, identity := range []string{"", "v2:rclone:" + strings.Repeat("a", 64)} {
		encoded, err := json.Marshal(manifestCandidate{ObservedIdentity: identity})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		_ = json.Unmarshal(encoded, &fields)
		want := 10
		if identity != "" {
			want++
		}
		if len(fields) != want {
			t.Fatalf("manifest fields=%d want=%d", len(fields), want)
		}
	}
}

func TestRunnerTransportOptions(t *testing.T) {
	runner := NewRunner(nil, slog.Default(), nil, nil)
	if runner.rcloneBinary != "rclone" || runner.stagingRoot != "/var/lib/workouts/staging" {
		t.Fatal("transport defaults changed")
	}
	runner = NewRunnerWithOptions(nil, slog.Default(), nil, nil, RunnerOptions{RcloneBinary: "/synthetic/rclone", StagingRoot: "/synthetic/staging"})
	if runner.rcloneBinary != "/synthetic/rclone" || runner.stagingRoot != "/synthetic/staging" {
		t.Fatal("transport options were ignored")
	}
}
