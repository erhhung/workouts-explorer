package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPrivateRegularFileSafety(t *testing.T) {
	directory := t.TempDir()
	private := filepath.Join(directory, "private")
	want := []byte("exact password bytes\n")
	if err := os.WriteFile(private, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPrivateRegularFile(private, 512)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("exact private read mismatch: err=%v", err)
	}

	public := filepath.Join(directory, "public")
	if err := os.WriteFile(public, []byte("password material"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateRegularFile(public, 512); err == nil {
		t.Fatal("group/world-readable file was accepted")
	}

	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink(private, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateRegularFile(symlink, 512); err == nil {
		t.Fatal("symlink password file was followed")
	}

	oversized := filepath.Join(directory, "oversized")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), 513), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateRegularFile(oversized, 512); err == nil {
		t.Fatal("oversized password file was accepted")
	}

	if _, err := readPrivateRegularFile(directory, 512); err == nil {
		t.Fatal("directory was accepted as password file")
	}
}

func TestBootstrapRejectsInvalidPasswordMinimumBeforeDatabaseAccess(t *testing.T) {
	for _, minimum := range []int{0, 11, 65} {
		err := BootstrapAdmin(context.Background(), nil, BootstrapAdminOptions{PasswordMin: minimum})
		if err == nil || !strings.Contains(err.Error(), "minimum") {
			t.Fatalf("minimum=%d err=%v", minimum, err)
		}
	}
}
