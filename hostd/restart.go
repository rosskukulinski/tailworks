package hostd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/netd"
)

// Restart policy for a workerd (or microVM) that exits on its own
// (issue #43).
//
// A deployment that has served (its instance became healthy once) is a
// long-running service: hostd restarts it with exponential backoff
// (restartBase doubling to restartCap) and never gives up, like systemd
// Restart=always or a Kubernetes CrashLoopBackOff. From the second exit in
// a row the deployment reports statusCrashloop, with the attempt count and
// the last log lines as its error, and it goes back to "active" as soon as
// a restarted workerd answers its health check.
//
// Before the instance was ever healthy (a deploy), an exit means a bad
// bundle or config far more often than a transient fault: a few quick
// restarts, then the deploy fails ("failed") without waiting for the
// health timeout.
const (
	statusCrashloop = "crashloop"

	restartBase = 200 * time.Millisecond
	restartCap  = 5 * time.Minute
	// restartResetAfter: a workerd that ran this long before exiting
	// starts the count (and the backoff) over.
	restartResetAfter = 30 * time.Second
	// startupRestarts is how many restarts a deploy gets before it fails.
	startupRestarts = 3
	// restartHealthWait bounds how long hostd waits for a restarted
	// workerd to answer its health check before it reports "active" again.
	restartHealthWait = 30 * time.Second
	// groupReapWait bounds the wait for a dead workerd's process group.
	groupReapWait = 5 * time.Second
	// exitPipeWait (exec.Cmd.WaitDelay): the log is a pipe (a rotating
	// file), so without it cmd.Wait would block for as long as any child
	// of the exited process still holds the pipe, and hostd would never
	// see the exit, reap the group or restart.
	exitPipeWait = time.Second
)

// restartBackoff is the delay before restart attempt n (1-based):
// 200 ms, 400 ms, 800 ms, … capped at 5 min.
func restartBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := restartBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= restartCap {
			return restartCap
		}
	}
	return d
}

// liveStatus: a deployment status in which the workload is still this
// host's to run (it holds the lease, its nodes stay up, an unassignment
// stops it).
func liveStatus(s string) bool {
	return s == "active" || s == "starting" || s == statusCrashloop
}

// crashloop marks depID as crash-looping: n exits in a row, restarting in
// next.
func (w *workload) crashloop(depID string, n int, next time.Duration, tail string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.deploymentID != depID || !liveStatus(w.status) {
		return
	}
	if tail == "" {
		tail = "(empty log)"
	}
	w.status = statusCrashloop
	w.err = netd.Redact(fmt.Sprintf("workerd exited %d times in a row; restarting in %v: %s", n, next.Round(time.Millisecond), tail))
}

// recovered: inst answered its health check after a restart.
func (w *workload) recovered(inst *instance) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != inst || w.deploymentID != inst.depID || w.status != statusCrashloop {
		return false
	}
	w.status, w.err = "active", ""
	return true
}

// reapGroup makes sure nothing of a workerd that exited is left: it
// SIGKILLs the process group (the workerd was its leader: Setpgid) and
// waits until the group is empty, so the restart can't race a survivor
// still holding the ingress socket. A sandbox-exec or Landlock launcher
// execs workerd in place, so the group is workerd and anything it forked.
func reapGroup(pgid int) error {
	if pgid <= 0 {
		return nil
	}
	syscall.Kill(-pgid, syscall.SIGKILL)
	deadline := time.Now().Add(groupReapWait)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process group %d still has members after SIGKILL", pgid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// onExit handles a workerd (or microVM) that exited without stop(): see
// the restart policy above.
func (inst *instance) onExit() {
	if inst.stopping.Load() || inst.h.ctx.Err() != nil {
		return
	}
	inst.mu.Lock()
	if time.Since(inst.started) > restartResetAfter {
		inst.restarts = 0
	}
	inst.restarts++
	n := inst.restarts
	pid := 0
	if inst.cmd != nil && inst.cmd.Process != nil {
		pid = inst.cmd.Process.Pid
	}
	inst.mu.Unlock()
	tail := inst.logTail(2048)
	ready := inst.ready.Load()
	if !ready && n > startupRestarts {
		inst.h.log.Error("workerd keeps exiting during startup; giving up", "workload", inst.w.name, "pid", pid, "log", tail)
		inst.giveUp(fmt.Errorf("workerd exited %d times during startup: %s", n, tail))
		return
	}
	d := restartBackoff(n)
	if ready && n >= 2 {
		inst.w.crashloop(inst.depID, n, d, tail)
	}
	inst.h.log.Warn("workerd exited; restarting", "workload", inst.w.name, "pid", pid, "attempt", n, "backoff", d, "log", tail)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-inst.h.ctx.Done():
		return
	}
	if inst.stopping.Load() {
		return
	}
	if inst.tier != bundle.TierMicroVM && inst.ingressLn == nil {
		// workerd bound the ingress socket itself; a crash leaves the file
		// behind, and the restart's bind would fail (EADDRINUSE). The old
		// process group is gone by now (reapGroup ran before exited closed),
		// so nothing can still be listening on it.
		if err := os.Remove(inst.res.Sockets.Ingress); err != nil && !errors.Is(err, fs.ErrNotExist) {
			inst.h.log.Warn("removing the old ingress socket", "workload", inst.w.name, "err", err)
		}
	}
	if err := inst.spawn(); err != nil {
		if !ready {
			inst.giveUp(err)
			return
		}
		// A spawn error (fork limit, disk full) counts as one more exit.
		inst.h.log.Error("workerd restart failed", "workload", inst.w.name, "attempt", n, "err", err)
		inst.mu.Lock()
		inst.started = time.Now()
		inst.mu.Unlock()
		go inst.onExit()
		return
	}
	if ready {
		go inst.watchRecovery()
	}
}

// giveUp ends a deploy whose workerd keeps exiting before it was ever
// healthy: waitHealthy returns err now instead of at its deadline.
func (inst *instance) giveUp(err error) {
	inst.mu.Lock()
	if inst.startErr == nil {
		inst.startErr = err
	}
	inst.mu.Unlock()
	inst.gaveUp.Store(true)
}

// startupError is the giveUp error, if any.
func (inst *instance) startupError() error {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.startErr
}

// watchRecovery reports the deployment "active" again once the restarted
// workerd answers its health check.
func (inst *instance) watchRecovery() {
	inst.mu.Lock()
	ex := inst.exited
	inst.mu.Unlock()
	deadline := time.Now().Add(restartHealthWait)
	for time.Now().Before(deadline) && !inst.stopping.Load() && inst.h.ctx.Err() == nil {
		select {
		case <-ex:
			return // exited again: onExit has it
		default:
		}
		if code, _ := inst.admin(map[string]any{"kind": "admin", "op": "health"}); code == 200 {
			if inst.w.recovered(inst) {
				inst.h.log.Info("workerd recovered", "workload", inst.w.name, "deployment", inst.depID)
				inst.h.syncService(inst.w, false)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
