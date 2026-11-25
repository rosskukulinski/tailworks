package hostd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Children left by a previous hostd (#432).
//
// Each workerd (and Node process) runs in a process group of its own
// (Setpgid: a sandboxed workerd may signal only itself and its group). So
// when hostd dies without stopping them (SIGKILL, an OOM kill, a panic),
// they keep running: a signal to hostd's group doesn't reach them, and
// reapGroup only runs for a child the live hostd is watching. systemd's
// KillMode=mixed cleans up the cgroup, but launchd, `tw up
// --separate-host`'s supervisor and `tw dev` don't. A restarted hostd then
// starts new workerds next to the old ones, which still hold the
// workloads' Durable Object storage open and run their alarms.
//
// hostd records every child process group it starts in
// <DataDir>/children.json, with the process's start time so a reused PID
// is never mistaken for one of ours, and removes it once reapGroup has
// run. At startup (New), before anything is spawned, every group still
// listed whose leader is the same process is SIGKILLed and waited for.

const childLedgerFile = "children.json"

type childRec struct {
	PID   int    `json:"pid"`   // also the process group (Setpgid)
	Start string `json:"start"` // procStartTime at spawn: guards against PID reuse
	What  string `json:"what"`  // workload / runtime, for the log
}

type childLedger struct {
	path string
	log  *slog.Logger
	mu   sync.Mutex
	recs map[int]childRec
}

// openChildLedger reaps what a previous run left and returns an empty ledger.
func openChildLedger(dataDir string, log *slog.Logger) *childLedger {
	l := &childLedger{path: filepath.Join(dataDir, childLedgerFile), log: log, recs: map[int]childRec{}}
	b, err := os.ReadFile(l.path)
	if err != nil {
		return l
	}
	var old []childRec
	if err := json.Unmarshal(b, &old); err != nil {
		log.Warn("children left by a previous run: unreadable ledger", "path", l.path, "err", err)
		os.Remove(l.path)
		return l
	}
	for _, c := range old {
		if c.PID <= 1 || c.Start == "" {
			continue
		}
		if st, err := procStartTime(c.PID); err != nil || st != c.Start {
			continue // gone, or the PID now belongs to someone else
		}
		err := reapGroup(c.PID)
		log.Warn("killed a child process left running by a previous hostd", "pid", c.PID, "what", c.What, "err", err)
	}
	os.Remove(l.path)
	return l
}

// add records a child group the live hostd just started.
func (l *childLedger) add(pid int, what string) {
	if l == nil || pid <= 1 {
		return
	}
	st, err := procStartTime(pid)
	if err != nil {
		return // already gone
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs[pid] = childRec{PID: pid, Start: st, What: what}
	l.saveLocked()
}

// remove forgets a child once its group has been reaped.
func (l *childLedger) remove(pid int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.recs[pid]; !ok {
		return
	}
	delete(l.recs, pid)
	l.saveLocked()
}

func (l *childLedger) saveLocked() {
	recs := make([]childRec, 0, len(l.recs))
	for _, r := range l.recs {
		recs = append(recs, r)
	}
	b, _ := json.Marshal(recs)
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		l.log.Warn("child ledger", "err", err)
		return
	}
	if err := os.Rename(tmp, l.path); err != nil {
		l.log.Warn("child ledger", "err", err)
	}
}

// alive reports whether pid exists (tests).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
