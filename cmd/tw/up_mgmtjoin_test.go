package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMgmtJoinKeyFileMode: tw up hands mgmt its join key in a file only
// the owner can read, in mgmt's 0700 state dir (up_mgmtjoin.go). That
// the file is gone after a successful and a failed tw up is
// TestUpMgmtJoinKeyRemoved; that mgmt deletes it is internal/mgmtd
// TestJoinKeyOnce.
func TestMgmtJoinKeyFileMode(t *testing.T) {
	t.Parallel()
	s := &state{Dir: t.TempDir()}
	for range 2 { // the second write replaces the first
		if err := s.writeMgmtJoinKey("tskey-auth-test"); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(s.mgmtJoinFile())
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("join key file mode %o, want 600", m)
	}
	if di, _ := os.Stat(filepath.Dir(s.mgmtJoinFile())); di.Mode().Perm() != 0o700 {
		t.Errorf("join key dir mode %o, want 700", di.Mode().Perm())
	}
	if b, _ := os.ReadFile(s.mgmtJoinFile()); string(b) != "TS_AUTHKEY=tskey-auth-test\n" {
		t.Errorf("join key file = %q", b)
	}
}
