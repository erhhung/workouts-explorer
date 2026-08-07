package worker

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestScavengeStagingRejectsHostileChildrenAndSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "staging")
	if err := os.MkdirAll(filepath.Join(root, "ingest-0123456789abcdef0123456789abcdef"), 0o700); err != nil {
		t.Fatal(err)
	}
	nestedTarget := t.TempDir()
	if err := os.WriteFile(filepath.Join(nestedTarget, "preserved"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nestedTarget, filepath.Join(root, "ingest-0123456789abcdef0123456789abcdef", "link")); err != nil {
		t.Fatal(err)
	}
	hostile := filepath.Join(root, "not-owned")
	if err := os.Mkdir(hostile, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	link := filepath.Join(root, "ingest-abcdefabcdefabcdefabcdefabcdefab")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ScavengeStaging(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ingest-0123456789abcdef0123456789abcdef")); !os.IsNotExist(err) {
		t.Fatalf("owned child remains: %v", err)
	}
	if _, err := os.Stat(hostile); err != nil {
		t.Fatalf("hostile child was removed: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink changed: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nestedTarget, "preserved")); err != nil {
		t.Fatalf("nested symlink target changed: %v", err)
	}
}

func TestScavengeStagingRejectsUnsafeRoot(t *testing.T) {
	for _, root := range []string{"relative", "/", "/tmp/../tmp/staging"} {
		if err := ScavengeStaging(root); err == nil {
			t.Fatalf("unsafe root %q accepted", root)
		}
	}
	parent := t.TempDir()
	link := filepath.Join(parent, "staging-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := ScavengeStaging(link); err == nil {
		t.Fatal("symlink staging root accepted")
	}
	ancestor := filepath.Join(t.TempDir(), "ancestor-link")
	if err := os.Symlink(t.TempDir(), ancestor); err != nil {
		t.Fatal(err)
	}
	if err := ScavengeStaging(filepath.Join(ancestor, "staging")); err == nil {
		t.Fatal("symlink staging ancestor accepted")
	}
}

func TestStagingDescriptorSurvivesRootReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "staging")
	owned := filepath.Join(root, "ingest-0123456789abcdef0123456789abcdef")
	if err := os.MkdirAll(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owned, "payload"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootFD, err := openStagingRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rootFD)
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	entries, err := readDirectoryEntries(rootFD)
	if err != nil || len(entries) != 1 {
		t.Fatalf("held root entries=%v err=%v", entries, err)
	}
	childFD, err := openChildDirectory(rootFD, entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(childFD, &opened); err != nil {
		_ = unix.Close(childFD)
		t.Fatal(err)
	}
	if err := removeDirectoryContents(childFD); err != nil {
		_ = unix.Close(childFD)
		t.Fatal(err)
	}
	_ = unix.Close(childFD)
	if err := unlinkOpenedDirectory(rootFD, entries[0].Name(), opened); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "payload")); !os.IsNotExist(err) {
		t.Fatalf("replacement target was modified: %v", err)
	}
}
