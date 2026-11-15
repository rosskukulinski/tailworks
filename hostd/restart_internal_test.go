package hostd

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRestartBackoff: 200 ms doubling to a 5 min cap, and it stays there
// (a long-running deployment is never given up on).
func TestRestartBackoff(t *testing.T) {
	ms := time.Millisecond
	want := []time.Duration{200 * ms, 400 * ms, 800 * ms, 1600 * ms, 3200 * ms, 6400 * ms, 12800 * ms, 25600 * ms, 51200 * ms, 102400 * ms, 204800 * ms, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := restartBackoff(i + 1); got != w {
			t.Errorf("restartBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	for _, n := range []int{50, 1000, 1 << 20} {
		if got := restartBackoff(n); got != restartCap {
			t.Errorf("restartBackoff(%d) = %v, want the cap", n, got)
		}
	}
	if restartBackoff(0) != restartBase || restartBackoff(-3) != restartBase {
		t.Error("restartBackoff(n < 1) is the base delay")
	}
	// The whole startup budget (a deploy that never got healthy) stays a
	// few seconds, so a bad bundle ends "failed" quickly.
	var sum time.Duration
	for n := 1; n <= startupRestarts; n++ {
		sum += restartBackoff(n)
	}
	if sum > 5*time.Second {
		t.Errorf("startup restarts take %v", sum)
	}
}

// TestLiveStatus: a crash-looping deployment is still this host's (lease,
// nodes, unassignment); failed and stopped ones are not.
func TestLiveStatus(t *testing.T) {
	for st, want := range map[string]bool{"active": true, "starting": true, statusCrashloop: true, "failed": false, "stopped": false, "": false} {
		if liveStatus(st) != want {
			t.Errorf("liveStatus(%q) = %v", st, !want)
		}
	}
}

// TestReapGroup: after the group leader died, a member it forked (here a
// sleep standing in for a workerd child, or a workerd under a wrapper) is
// killed and waited for before reapGroup returns.
func TestReapGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() { syscall.Kill(-pgid, syscall.SIGKILL) })
	// Wait until the child is in the group.
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
		if len(strings.Fields(string(out))) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the group never had two members: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cmd.Process.Kill() // the leader only, like kill -9 <workerd pid>
	cmd.Wait()
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("the child should have survived its leader: %v", err)
	}
	t0 := time.Now()
	if err := reapGroup(pgid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("group still has members after reapGroup: %v", err)
	}
	t.Logf("group reaped in %v", time.Since(t0))
	if err := reapGroup(pgid); err != nil { // idempotent: nothing left
		t.Fatal(err)
	}
}
