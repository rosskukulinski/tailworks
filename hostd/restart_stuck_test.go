package hostd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// stuckExits is how many exits in a row the old policy survived: it gave
// up for good on the 6th ("workerd exited 6 times", issue #43).
const stuckExits = 6

// TestWorkerdStuckSocketCrashLoop is the regression test for issue #43: a
// workerd that served crashes, and every restart then exits at once with
// EADDRINUSE on its ingress socket. Before the fix hostd gave up after the
// 6th exit (about 4 s) and the deployment stayed "failed" for good.
//
// workerd is a wrapper that execs the real one in place, except while a
// gate file exists: then it prints workerd's bind error and exits 1, like
// a workerd whose socket is stuck. The gate, not a timer, decides how long
// the trouble lasts, so the test can't race the recovery: while the gate
// is in place the only possible statuses are "crashloop" (fixed) or
// "failed" (the bug).
func TestWorkerdStuckSocketCrashLoop(t *testing.T) {
	real, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	gate := filepath.Join(dir, "stuck")
	wrapper := filepath.Join(dir, "workerd-wrapper")
	script := fmt.Sprintf(`#!/bin/sh
if [ -e %q ]; then
  echo 'Socket "ingress": bind(): Address already in use (os error 48)' >&2
  exit 1
fi
exec %q "$@"
`, gate, real)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TW_WORKERD", wrapper)
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) { o.Sandbox = "none" })
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	wl := e.WaitActive("probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	serves := func() {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			code, m := s.getJSON("http://" + wl.IP + "/")
			if code == 200 && m["kind"] == "worker" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("GET /: %d %v", code, m)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	serves()

	// The crash, then a stuck socket for every restart.
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	pid := workloadStatus(t, e, "probe").PID
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// Past the old give-up point. Backoff 200 ms doubling: the 6th exit
	// comes about 6 s after the kill.
	var seen []string
	w := waitWorkload(t, e, "probe", 60*time.Second, "exits past the old limit", func(w hostd.WorkloadStatus) bool {
		if n := len(seen); n == 0 || seen[n-1] != w.Status {
			seen = append(seen, w.Status)
		}
		return w.Status == "failed" || (w.Status == "crashloop" && w.Restarts >= stuckExits)
	})
	t.Logf("statuses %v; %s", seen, w.Error)
	if w.Status != "crashloop" {
		t.Fatalf("after %d exits with a stuck socket the deployment is %q, want crashloop (issue #43): %s", w.Restarts, w.Status, w.Error)
	}
	for _, want := range []string{"times in a row; restarting in", "Address already in use"} {
		if !strings.Contains(w.Error, want) {
			t.Errorf("crashloop error lacks %q: %q", want, w.Error)
		}
	}

	// The socket comes free: the next restart serves and the deployment
	// is active again by itself (the backoff is at most 12.8 s here).
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	waitWorkload(t, e, "probe", 60*time.Second, "active again", func(w hostd.WorkloadStatus) bool {
		if w.Status == "failed" {
			t.Fatalf("failed after the socket came free: %s", w.Error)
		}
		return w.Status == "active" && w.Error == ""
	})
	serves()
}
