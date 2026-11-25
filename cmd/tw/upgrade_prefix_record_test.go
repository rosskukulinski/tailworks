package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tw.json's installPrefix (issue #532): with tailworks stopped and a state
// dir that isn't itself the install, the recorded prefix names the install.
func TestResolvePrefixRecorded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inst := mkPrefix(t, filepath.Join(root, "opt", "tailworks"), "0.4.0")
	extracted := mkPrefix(t, filepath.Join(root, "tmp", "tailworks-0.5.0-linux-amd64"), "0.5.0")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &state{Dir: stateDir, Cfg: Config{InstallPrefix: inst}}

	cands := statePrefixCandidates(s)
	if len(cands) != 1 || cands[0].Prefix != inst || !strings.Contains(cands[0].From, "tw.json") {
		t.Fatalf("candidates = %+v, want the recorded prefix", cands)
	}
	// The installed tw: no mismatch.
	pc, err := resolvePrefix("", inst, cands)
	if err != nil || pc.Mismatch != "" || !samePath(pc.Prefix, inst) {
		t.Fatalf("installed tw: %+v, %v", pc, err)
	}
	// An extracted tarball's tw: a mismatch that names tw.json.
	pc, err = resolvePrefix("", extracted, cands)
	if err != nil || !samePath(pc.Prefix, inst) || !strings.Contains(pc.Mismatch, "tw.json") {
		t.Fatalf("extracted tw: %+v, %v", pc, err)
	}

	// A recorded prefix that was removed is skipped: the binary's own prefix, as before.
	s.Cfg.InstallPrefix = filepath.Join(root, "gone")
	if cands := statePrefixCandidates(s); len(cands) != 0 {
		t.Fatalf("removed prefix: candidates = %+v, want none", cands)
	}
	// No record (state from before the field, npm, a dev build): unchanged.
	s.Cfg.InstallPrefix = ""
	if cands := statePrefixCandidates(s); len(cands) != 0 {
		t.Fatalf("no record: candidates = %+v, want none", cands)
	}
	// The recorded prefix ranks before "the state dir is the install prefix".
	home := mkPrefix(t, filepath.Join(root, "home", ".tailworks"), "0.4.0")
	s = &state{Dir: home, Cfg: Config{InstallPrefix: inst}}
	if cands := statePrefixCandidates(s); len(cands) != 2 || cands[0].Prefix != inst || cands[1].Prefix != home {
		t.Fatalf("order: candidates = %+v", cands)
	}
}
