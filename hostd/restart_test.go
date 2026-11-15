package hostd_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/twd/hostd"
	"github.com/rosskukulinski/twd/hostd/fakemgmt"
	"github.com/rosskukulinski/twd/hostd/hostdtest"
	"github.com/rosskukulinski/twd/netd/nettest"
)

// waitWorkload polls the workload's status until ok, or fails the test.
func waitWorkload(t *testing.T, e *hostdtest.Env, name string, d time.Duration, what string, ok func(hostd.WorkloadStatus) bool) hostd.WorkloadStatus {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		ws := workloadStatus(t, e, name)
		if ok(ws) {
			return ws
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after %v: status=%s pid=%d restarts=%d err=%q", what, d, ws.Status, ws.PID, ws.Restarts, ws.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func groupGone(pgid int) bool { return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) }

// TestWorkerdCrashRecovery (issue #43): a process-tier workerd killed with
// SIGKILL (under the host's sandbox: Seatbelt on macOS, Landlock on Linux)
// comes back by itself and serves; its old process group is gone. Killed
// again and again it reports "crashloop" with the attempt count and keeps
// restarting (never "failed"); left alone it is "active" again.
func TestWorkerdCrashRecovery(t *testing.T) {
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) { o.Sandbox = "auto" })
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	wl := e.WaitActive("probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	// serves: GET / answers from the worker (a restarted workerd may take
	// a moment to bind its socket).
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
	ws := workloadStatus(t, e, "probe")
	t.Logf("workerd pid %d, sandbox %s", ws.PID, ws.Sandbox)
	// The sandbox wrapper (sandbox-exec, the Landlock launcher) execs
	// workerd in place: the pid hostd waits on is workerd's own.
	if out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(ws.PID)).Output(); err != nil || filepath.Base(strings.TrimSpace(string(out))) != "workerd" {
		t.Fatalf("pid %d is %q (%v), not workerd", ws.PID, out, err)
	}

	t.Run("KillWorkerd", func(t *testing.T) {
		old := ws.PID
		if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		got := waitWorkload(t, e, "probe", 20*time.Second, "a new workerd", func(w hostd.WorkloadStatus) bool {
			return w.PID != 0 && w.PID != old && w.Status == "active"
		})
		serves()
		t.Logf("recovered in %v (pid %d → %d)", time.Since(t0), old, got.PID)
		if !groupGone(old) {
			t.Errorf("the old workerd's process group %d is still alive", old)
		}
	})

	t.Run("CrashLoop", func(t *testing.T) {
		// Kill each new workerd as soon as it appears: exits in a row.
		var last hostd.WorkloadStatus
		for i := 0; i < 3; i++ {
			pid := workloadStatus(t, e, "probe").PID
			syscall.Kill(pid, syscall.SIGKILL)
			last = waitWorkload(t, e, "probe", 30*time.Second, "a crashloop restart", func(w hostd.WorkloadStatus) bool {
				return w.PID != 0 && w.PID != pid
			})
		}
		w := waitWorkload(t, e, "probe", 10*time.Second, "crashloop status", func(w hostd.WorkloadStatus) bool {
			return w.Status == "crashloop" || w.Status == "failed"
		})
		if w.Status != "crashloop" || w.Restarts < 2 || !strings.Contains(w.Error, "times in a row; restarting in") {
			t.Fatalf("after %d kills: status=%s restarts=%d err=%q (pid %d)", 3, w.Status, w.Restarts, w.Error, last.PID)
		}
		t.Logf("crashloop: %s", w.Error)
		// Left alone, the restarted workerd answers and it is active again.
		waitWorkload(t, e, "probe", 30*time.Second, "active again", func(w hostd.WorkloadStatus) bool {
			return w.Status == "active" && w.Error == ""
		})
		serves()
	})
}

// TestWorkerdWrapperKilled (issue #43): when the process hostd waits on is
// a wrapper with workerd as its child (here a shell script; the same shape
// as a sandbox launcher that doesn't exec in place), SIGKILL of the
// wrapper must not leave the old workerd running: hostd kills and waits
// for the whole process group before it respawns, and the workload
// serves again from a new one.
func TestWorkerdWrapperKilled(t *testing.T) {
	real, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "workerd-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\n\"$TWD_TEST_REAL_WORKERD\" \"$@\" &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWD_WORKERD", wrapper)
	t.Setenv("TWD_TEST_REAL_WORKERD", real)
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) { o.Sandbox = "none" })
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	wl := e.WaitActive("probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	ws := workloadStatus(t, e, "probe")
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(ws.PID)).Output()
	kids := strings.Fields(string(out))
	if len(kids) != 1 {
		t.Fatalf("wrapper %d children: %q", ws.PID, out)
	}
	child, _ := strconv.Atoi(kids[0])
	syscall.Kill(ws.PID, syscall.SIGKILL) // the wrapper only
	waitWorkload(t, e, "probe", 20*time.Second, "a new wrapper", func(w hostd.WorkloadStatus) bool {
		return w.PID != 0 && w.PID != ws.PID && w.Status == "active"
	})
	if err := syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the old workerd %d (child of the killed wrapper) is still running: %v", child, err)
	}
	if !groupGone(ws.PID) {
		t.Fatalf("the old process group %d is still alive", ws.PID)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, m := s.getJSON("http://" + wl.IP + "/")
		if code == 200 && m["kind"] == "worker" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET / after the restart: %d %v", code, m)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWorkerdDeployTimeFailure: a workerd that exits before it was ever
// healthy (a bad bundle or config) is a deploy failure: the deployment
// ends "failed" within seconds, not after the health timeout, and is not
// reported as a crash loop.
func TestWorkerdDeployTimeFailure(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "workerd-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho 'Error: bad config (test)' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TWD_WORKERD", bad)
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, nil, func(o *hostdtest.Options) { o.Sandbox = "none" })
	e := s.e
	t0 := time.Now()
	e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	var seen []string
	w := waitWorkload(t, e, "probe", 15*time.Second, "failed", func(w hostd.WorkloadStatus) bool {
		if n := len(seen); n == 0 || seen[n-1] != w.Status {
			seen = append(seen, w.Status)
		}
		return w.Status == "failed"
	})
	took := time.Since(t0)
	t.Logf("failed after %v (statuses %v): %s", took, seen, w.Error)
	if took > 10*time.Second {
		t.Errorf("a deploy-time failure took %v to report", took)
	}
	for _, st := range seen {
		if st == "crashloop" {
			t.Errorf("a deploy that never ran reported crashloop: %v", seen)
		}
	}
	if !strings.Contains(w.Error, "during startup") || !strings.Contains(w.Error, "bad config (test)") {
		t.Errorf("error: %q", w.Error)
	}
	// It stays failed: hostd does not restart it any more.
	time.Sleep(2 * time.Second)
	if ws := workloadStatus(t, e, "probe"); ws.Status != "failed" {
		t.Errorf("after giving up: %s", ws.Status)
	}
}
