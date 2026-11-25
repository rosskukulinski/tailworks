package vm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGuestIdentity(t *testing.T) {
	d := t.TempDir()
	k, r, m := filepath.Join(d, "vmlinux"), filepath.Join(d, "rootfs.ext4"), filepath.Join(d, "manifest.json")
	for _, p := range []string{k, r} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	noManifest, err := GuestIdentity(k, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m, []byte(`{"rootfs_sha256":"a","tw_version":"0.2.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ := GuestIdentity(k, r)
	if a == noManifest {
		t.Fatal("manifest did not change the identity")
	}
	if again, _ := GuestIdentity(k, r); again != a {
		t.Fatal("identity is not stable")
	}
	os.WriteFile(m, []byte(`{"rootfs_sha256":"b","tw_version":"0.3.0"}`), 0o644)
	if b, _ := GuestIdentity(k, r); b == a {
		t.Fatal("a new manifest kept the identity")
	}
	// A rootfs replaced without a new manifest changes it too.
	id1, _ := GuestIdentity(k, r)
	os.Chtimes(r, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if id2, _ := GuestIdentity(k, r); id2 == id1 {
		t.Fatal("a touched rootfs kept the identity")
	}
	if _, err := GuestIdentity(filepath.Join(d, "missing"), r); err == nil {
		t.Fatal("missing kernel accepted")
	}
}
