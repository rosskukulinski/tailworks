//go:build linux

package procsb_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/procsb"
)

// TestLauncherEnvNotInherited (#336): a process started through
// procsb.Command gets exactly the environment the caller put in Env, which
// starts empty, never the caller's own environment by inheritance, and the
// launcher adds nothing of its own on the way to syscall.Exec. Read from the
// child itself: /proc/self/environ is on its own-/proc read list.
func TestLauncherEnvNotInherited(t *testing.T) {
	needLandlock(t)
	const canary = "TW_TEST_CANARY_336"
	t.Setenv(canary, "leaked-from-the-caller")
	dir := t.TempDir()
	own := filepath.Join(dir, "own")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	p := writeProfile(t, dir, procsb.Profile{Exec: []string{"/bin/cat"}, Own: []string{own}})
	environ := func(cmd interface{ Output() ([]byte, error) }) []string {
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("cat /proc/self/environ under the launcher: %v", err)
		}
		var vars []string
		for _, kv := range strings.Split(string(out), "\x00") {
			if kv != "" {
				vars = append(vars, kv)
			}
		}
		return vars
	}

	// Default: nothing.
	cmd := procsb.Command(self(t), p, "/bin/cat", "/proc/self/environ")
	if got := environ(cmd); len(got) != 0 {
		t.Errorf("child of a bare procsb.Command has %d variables, want none: %v", len(got), got)
	}

	// With a list: exactly that list, in order.
	cmd = procsb.Command(self(t), p, "/bin/cat", "/proc/self/environ")
	cmd.Env = append(cmd.Env, "TW_KEEP=1", "TZ=UTC")
	got := environ(cmd)
	if strings.Join(got, ",") != "TW_KEEP=1,TZ=UTC" {
		t.Errorf("child environment %v, want exactly [TW_KEEP=1 TZ=UTC]", got)
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, canary+"=") {
			t.Errorf("the caller's %s reached the child", canary)
		}
	}
}
