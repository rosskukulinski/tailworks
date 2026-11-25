//go:build linux

package helper

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMakeScratchNeverReusesAFile (security ledger C-scratch): the
// container scratch disk the helper makes for a session (session start,
// step 2) is a new file: O_EXCL, so an existing file at the path fails the
// boot instead of being handed to the guest, and it is left as it was. A
// fresh path gets a sparse ext4 of the asked size labelled tw-scratch.
// Runs without KVM or root; mkfs.ext4 is needed for the second half.
func TestMakeScratchNeverReusesAFile(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "scratch.ext4")
	const old = "bytes an earlier VM wrote"
	if err := os.WriteFile(stale, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	err := makeScratch(stale, 16, os.Getuid())
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("existing file at the scratch path: got %v, want ErrExist", err)
	}
	if b, _ := os.ReadFile(stale); string(b) != old {
		t.Fatalf("the existing file was changed: %q", b)
	}

	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not found")
	}
	fresh := filepath.Join(dir, "fresh.ext4")
	if err := makeScratch(fresh, 16, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 16<<20 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("fresh scratch: size %d mode %v, want 16 MiB 0600", fi.Size(), fi.Mode().Perm())
	}
	// The ext4 superblock is at 1024: magic 0xEF53 at +0x38, the volume
	// label at +0x78.
	f, err := os.Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sb := make([]byte, 1024)
	if _, err := f.ReadAt(sb, 1024); err != nil {
		t.Fatal(err)
	}
	if magic := binary.LittleEndian.Uint16(sb[0x38:]); magic != 0xEF53 {
		t.Fatalf("superblock magic %#x, want 0xef53", magic)
	}
	if label := string(sb[0x78 : 0x78+len("tw-scratch")]); label != "tw-scratch" {
		t.Fatalf("label %q, want tw-scratch", label)
	}
}
