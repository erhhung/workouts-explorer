package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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

func TestPrivateFileModeAllowsKubernetesSecretGroupProjection(t *testing.T) {
	projected := unix.Stat_t{Uid: 0, Gid: 65532, Mode: unix.S_IFREG | 0o440}
	if !privateFileModeAllowed(&projected, 65532, []int{65532}) {
		t.Fatal("root-owned group-readable Secret projection was rejected")
	}
	for _, mode := range []uint32{0o460, 0o450, 0o444, 0o400} {
		projected.Mode = unix.S_IFREG | mode
		if privateFileModeAllowed(&projected, 65532, []int{65532}) {
			t.Errorf("unsafe or unreadable projected mode %#o was accepted", mode)
		}
	}
	projected.Mode = unix.S_IFREG | 0o440
	if privateFileModeAllowed(&projected, 65532, []int{12345}) {
		t.Fatal("Secret projection owned by an unrelated group was accepted")
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
