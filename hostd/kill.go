package hostd

import (
	"fmt"
	"time"
)

// killRun is one run of killLocal's steps for an object (#94).
//
// Several paths kill the same object on this host: mgmt's kill push, the
// heartbeat's RevokeObjects, the workload's own kill op, a kill during a
// join. Only the first one runs the steps; it marks the object killed
// before it starts them. Without killRun the others returned as soon as
// they saw the killed status, so mgmt's push could answer 200 (and
// `tw kill` return) while a heartbeat-started run was still between the
// netd kill and the in-process kill: the tailnet path already answered
// 410 while the workload's in-process path to the object still answered
// 200. Every caller now waits for the run, and gets its result.
type killRun struct {
	done chan struct{} // closed when the run has finished
	err  error         // the in-process step's error; set (under h.mu) before done closes
}

func newKillRun() *killRun { return &killRun{done: make(chan struct{})} }

// endKill records run's result and releases its waiters.
func (h *Host) endKill(run *killRun, err error) {
	h.mu.Lock()
	run.err = err
	h.mu.Unlock()
	close(run.done)
}

// awaitKill is killLocal for an object that is already killed: wait for
// the run that killed it. If that run's in-process kill failed, try it
// again, so a later kill (mgmt's retry, the next heartbeat) can still
// stop the object in workerd.
func (h *Host) awaitKill(o *object, reason string) error {
	for {
		h.mu.Lock()
		run := o.kill
		h.mu.Unlock()
		if run == nil {
			return nil // a tombstone from an earlier hostd process
		}
		<-run.done
		h.mu.Lock()
		if o.kill != run {
			h.mu.Unlock()
			continue // another caller started a retry meanwhile: wait for that
		}
		if run.err == nil {
			h.mu.Unlock()
			return nil
		}
		retry := newKillRun()
		o.kill = retry
		h.mu.Unlock()
		err := h.killInProcess(o, reason)
		h.endKill(retry, err)
		return err
	}
}

// killInProcessBackoff: the waits between in-process kill attempts.
var killInProcessBackoff = []time.Duration{
	50 * time.Millisecond,
	250 * time.Millisecond,
}

// killInProcess stops the object in its workload's workerd: the platform DO
// sets its killed marker (so every in-process path answers 410 killed) and
// aborts the tenant facet. It returns only when workerd has acknowledged
// that, or with an error when it could not (workerd busy or restarting).
func (h *Host) killInProcess(o *object, reason string) error {
	if o.Class == "" || o.isPublicNode() || o.isPool() { // workerd never had a pool node's class
		return nil
	}
	h.mu.Lock()
	w := h.workloads[o.TenantID+"/"+o.Workload]
	h.mu.Unlock()
	if w == nil {
		return nil
	}
	var err error
	for i := 0; ; i++ {
		inst := w.current()
		if inst == nil || inst.node != nil {
			return nil // not running, or a Node app (no platform DOs): nothing in workerd to stop
		}
		code, body := inst.admin(map[string]any{"kind": "admin", "op": "kill", "class": o.Class, "name": o.Name, "reason": reason})
		if code == 200 {
			return nil
		}
		err = fmt.Errorf("in-process kill of %s: workerd answered %d %s", o.ID, code, trimReason(string(body)))
		if i >= len(killInProcessBackoff) {
			h.log.Error("object not stopped in workerd", "object", o.ID, "err", err)
			return err
		}
		time.Sleep(killInProcessBackoff[i])
	}
}
