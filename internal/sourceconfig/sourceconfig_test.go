package sourceconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalizeLocal(t *testing.T) {
	config, encoded, err := CanonicalizeLocal(Local{Version: 1, Path: "/archives/workouts/../workouts/samples"}, []string{"/archives/workouts"})
	if err != nil {
		t.Fatal(err)
	}
	if config.Path != "/archives/workouts/samples" || string(encoded) != `{"version":1,"path":"/archives/workouts/samples"}` {
		t.Fatalf("config=%+v encoded=%s", config, encoded)
	}
	decoded, canonical, err := DecodeLocal([]byte("{\n\t\"path\":\"/archives/workouts/samples\",\"version\":1\n}"), []string{"/archives/workouts"})
	if err != nil || decoded != config || string(canonical) != string(encoded) {
		t.Fatalf("decoded=%+v canonical=%s err=%v", decoded, canonical, err)
	}
}

func TestLocalConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		config []byte
	}{
		{"unknown version", []byte(`{"version":2,"path":"/archives/workouts"}`)},
		{"relative path", []byte(`{"version":1,"path":"workouts"}`)},
		{"prefix sibling", []byte(`{"version":1,"path":"/archives/workouts-private"}`)},
		{"unknown field", []byte(`{"version":1,"path":"/archives/workouts","secret":true}`)},
		{"trailing document", []byte(`{"version":1,"path":"/archives/workouts"}{}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := DecodeLocal(test.config, []string{"/archives/workouts"}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	if _, err := ValidatePath("/archives/workouts/\x00/file", []string{"/archives/workouts"}); err == nil {
		t.Fatal("NUL path accepted")
	}
	if _, err := ValidatePath(string([]byte{'/', 0xff}), []string{"/archives/workouts"}); err == nil {
		t.Fatal("invalid UTF-8 path accepted")
	}
}

func TestValidateRoots(t *testing.T) {
	for _, roots := range [][]string{
		nil,
		{"relative"},
		{"/"},
		{"/archives", "/archives"},
		{"/archives", "/archives/workouts"},
	} {
		if _, err := ValidateRoots(roots); err == nil {
			t.Fatalf("invalid roots accepted: %q", roots)
		}
	}
	roots, err := ParseRoots(" /archives/workouts , /mnt/imports ")
	if err != nil || !reflect.DeepEqual(roots, []string{"/archives/workouts", "/mnt/imports"}) {
		t.Fatalf("roots=%q err=%v", roots, err)
	}
}

func TestOpenDirectoryRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	outside := t.TempDir()
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDirectory(inside, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDirectory(filepath.Join(root, "escape"), []string{root}); err == nil || !strings.Contains(err.Error(), "approved") {
		t.Fatalf("symlink escape error=%v", err)
	}
	if _, err := OpenDirectory(filepath.Join(root, "missing"), []string{root}); err == nil {
		t.Fatal("missing path accepted")
	}
}

func TestOpenRegularFileRejectsLinksAndNestedNames(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "export.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "export.json"), filepath.Join(root, "linked.json")); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDirectory(root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	file, info, err := OpenRegularFile(directory, "export.json")
	if err != nil || info.Size() != 2 {
		t.Fatalf("size=%v err=%v", info, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"linked.json", "../export.json", "nested/export.json", "."} {
		if file, _, err := OpenRegularFile(directory, name); err == nil {
			_ = file.Close()
			t.Fatalf("unsafe source file accepted: %q", name)
		}
	}
}
