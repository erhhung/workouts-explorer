package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"golang.org/x/sys/unix"
)

const minimalExport = `{"data":{"workouts":[{"id":"provider-1","name":"Walk","start":"2026-01-02 03:04:05 +0000","end":"2026-01-02 03:05:05 +0000","duration":60}]}}`

func TestDiscoverAndReadSourceFiles(t *testing.T) {
	root := t.TempDir()
	if !exportNamePattern.MatchString("HealthAutoExport-2026-01-01.json") {
		t.Fatal("export file pattern does not match a canonical name")
	}
	writeExport := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeExport("HealthAutoExport-2026-02-02.json", minimalExport)
	writeExport("HealthAutoExport-2026-01-01.json", minimalExport)
	writeExport("private.json", "private payload")
	if err := os.Mkdir(filepath.Join(root, "HealthAutoExport-2026-03-03.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "HealthAutoExport-2026-01-01.json"), filepath.Join(root, "HealthAutoExport-2026-04-04.json")); err != nil {
		t.Fatal(err)
	}
	directory, err := sourceconfig.OpenDirectory(root, []string{filepath.Dir(root)})
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	files, err := discoverSourceFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(files))
	for index := range files {
		names[index] = files[index].name
	}
	want := []string{"HealthAutoExport-2026-01-01.json", "HealthAutoExport-2026-02-02.json"}
	if !slices.Equal(names, want) {
		t.Fatalf("discovered %v, want %v", names, want)
	}
	parsed, failure := readSourceFile(context.Background(), directory, files[0])
	if failure != nil || len(parsed.document.Workouts) != 1 {
		t.Fatalf("failure=%+v workouts=%d", failure, len(parsed.document.Workouts))
	}
	wantChecksum := sha256.Sum256([]byte(minimalExport))
	if parsed.checksum != wantChecksum {
		t.Fatal("source checksum differs from exact file bytes")
	}
}

func TestReadSourceFileRejectsMalformedAndChangedMetadata(t *testing.T) {
	root := t.TempDir()
	name := "HealthAutoExport-2026-01-01.json"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := sourceconfig.OpenDirectory(root, []string{filepath.Dir(root)})
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	files, err := discoverSourceFiles(directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if _, failure := readSourceFile(context.Background(), directory, files[0]); failure == nil || failure.code != "source-file-invalid" {
		t.Fatalf("failure=%+v", failure)
	}
	files[0].size++
	if _, failure := readSourceFile(context.Background(), directory, files[0]); failure == nil || failure.code != "source-file-mutated" {
		t.Fatalf("failure=%+v", failure)
	}
}

func TestEncodeWarningsUsesDatabaseSchema(t *testing.T) {
	encoded, err := encodeWarnings([]healthautoexport.Warning{{
		Code: healthautoexport.WarningInvalidOptionalRouteValue, Field: healthautoexport.WarningFieldRouteSpeed, RoutePoint: 7,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var value []map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 1 || len(value[0]) != 3 || value[0]["code"] != "invalid_optional_route_value" ||
		value[0]["field"] != "route_speed" || value[0]["route_point"] != float64(7) {
		t.Fatalf("warnings=%s", encoded)
	}
}

func TestDiscoverSourceFileBoundsAndOpenFailure(t *testing.T) {
	write := func(t *testing.T, root, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(minimalExport), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	openDirectory := func(t *testing.T, root string) *os.File {
		t.Helper()
		directory, err := sourceconfig.OpenDirectory(root, []string{filepath.Dir(root)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = directory.Close() })
		return directory
	}

	t.Run("directory entries", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "private-a")
		write(t, root, "private-b")
		_, err := discoverSourceFilesWith(openDirectory(t, root), discoveryLimits{maxEntries: 1, maxFiles: 10}, unix.Fstatat, sourceconfig.OpenRegularFile)
		if !errors.Is(err, errDiscoveryLimit) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("eligible files", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "HealthAutoExport-2026-01-01.json")
		write(t, root, "HealthAutoExport-2026-01-02.json")
		_, err := discoverSourceFilesWith(openDirectory(t, root), discoveryLimits{maxEntries: 10, maxFiles: 1}, unix.Fstatat, sourceconfig.OpenRegularFile)
		if !errors.Is(err, errDiscoveryLimit) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("matching regular open failure", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "HealthAutoExport-2026-01-01.json")
		opener := func(*os.File, string) (*os.File, os.FileInfo, error) {
			return nil, nil, errors.New("private open error")
		}
		_, err := discoverSourceFilesWith(openDirectory(t, root), discoveryLimits{maxEntries: 10, maxFiles: 10}, unix.Fstatat, opener)
		if !errors.Is(err, errDiscoveryFileOpen) {
			t.Fatalf("err=%v", err)
		}
		assertDiscoveryFailureName(t, err, "HealthAutoExport-2026-01-01.json")
	})

	t.Run("matching regular stat failure", func(t *testing.T) {
		root := t.TempDir()
		name := "HealthAutoExport-2026-01-01.json"
		write(t, root, name)
		stat := func(int, string, *unix.Stat_t, int) error { return errors.New("private stat error") }
		_, err := discoverSourceFilesWith(openDirectory(t, root), discoveryLimits{maxEntries: 10, maxFiles: 10}, stat, sourceconfig.OpenRegularFile)
		if !errors.Is(err, errDiscoveryFileOpen) {
			t.Fatalf("err=%v", err)
		}
		assertDiscoveryFailureName(t, err, name)
	})

	t.Run("matching regular close failure", func(t *testing.T) {
		root := t.TempDir()
		name := "HealthAutoExport-2026-01-01.json"
		write(t, root, name)
		opener := func(directory *os.File, name string) (*os.File, os.FileInfo, error) {
			file, info, err := sourceconfig.OpenRegularFile(directory, name)
			if err == nil {
				err = file.Close()
			}
			return file, info, err
		}
		_, err := discoverSourceFilesWith(openDirectory(t, root), discoveryLimits{maxEntries: 10, maxFiles: 10}, unix.Fstatat, opener)
		if !errors.Is(err, errDiscoveryFileOpen) {
			t.Fatalf("err=%v", err)
		}
		assertDiscoveryFailureName(t, err, name)
	})
}

func assertDiscoveryFailureName(t *testing.T, err error, want string) {
	t.Helper()
	var failure *discoveryFileError
	if !errors.As(err, &failure) || failure.name != want || filepath.Base(failure.name) != failure.name {
		t.Fatalf("discovery failure=%v name=%q, want basename %q", err, failure.name, want)
	}
	results := newIngestResults()
	recordDiscoveryFailure(&results, err)
	if results.FilesProcessed != 1 || !slices.Equal(results.FailedProcessing, []string{want}) {
		t.Fatalf("discovery results=%+v, want one failed basename %q", results, want)
	}
}

func TestCheckpointMetadataFailure(t *testing.T) {
	modified := databaseTimestamp(time.Date(2026, 1, 2, 3, 4, 5, 678901234, time.UTC))
	file := sourceFile{size: 42, modified: modified}

	for _, state := range []string{"discovered", "processing", "succeeded"} {
		state := state
		t.Run(state+" matches", func(t *testing.T) {
			stored := modified
			if failure := checkpointMetadataFailure(42, &stored, state, file); failure != nil {
				t.Fatalf("failure=%+v", failure)
			}
		})
	}
	t.Run("size mismatch", func(t *testing.T) {
		stored := modified
		failure := checkpointMetadataFailure(43, &stored, "discovered", file)
		if failure == nil || failure.code != "source-file-mutated" {
			t.Fatalf("failure=%+v", failure)
		}
	})
	t.Run("mtime mismatch", func(t *testing.T) {
		stored := modified.Add(time.Microsecond)
		failure := checkpointMetadataFailure(42, &stored, "succeeded", file)
		if failure == nil || failure.code != "source-file-mutated" {
			t.Fatalf("failure=%+v", failure)
		}
	})
	t.Run("failed checkpoint", func(t *testing.T) {
		stored := modified
		failure := checkpointMetadataFailure(42, &stored, "failed", file)
		if failure == nil || failure.code != "source-file-invalid" {
			t.Fatalf("failure=%+v", failure)
		}
	})
}

func TestIngestResultsFailedProcessingBoundsAndBasenames(t *testing.T) {
	results := newIngestResults()
	for index := 0; index < maxFailedProcessing+5; index++ {
		results.recordFailedFile(filepath.Join("private", "nested", fmt.Sprintf("file-%03d.json", index)))
	}
	if results.FilesProcessed != 105 || len(results.FailedProcessing) != maxFailedProcessing {
		t.Fatalf("files=%d failed=%d", results.FilesProcessed, len(results.FailedProcessing))
	}
	for index, name := range results.FailedProcessing {
		want := fmt.Sprintf("file-%03d.json", index)
		if name != want || filepath.Base(name) != name {
			t.Fatalf("failed_processing[%d]=%q, want basename %q", index, name, want)
		}
	}
}

func TestRecordDiscoveryFailureOnlyRecordsKnownFilename(t *testing.T) {
	results := newIngestResults()
	recordDiscoveryFailure(&results, errors.New("directory-wide read failure"))
	if results.FilesProcessed != 0 || len(results.FailedProcessing) != 0 {
		t.Fatalf("directory-wide failure recorded a file: %+v", results)
	}
	recordDiscoveryFailure(&results, &discoveryFileError{name: filepath.Join("private", "HealthAutoExport-2026-01-01.json")})
	if results.FilesProcessed != 1 || !slices.Equal(results.FailedProcessing, []string{"HealthAutoExport-2026-01-01.json"}) {
		t.Fatalf("known file failure results=%+v", results)
	}
}
