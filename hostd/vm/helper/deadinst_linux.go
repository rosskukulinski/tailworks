//go:build linux

package helper

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// In-process helpers (hostd as root without the tw-vmhelper service,
// hostd/vm startInProcessHelper) name everything after their hostd's pid:
// instance "p<pid>", state and jails under /var/lib/tw-vm-p<pid>, the VM
// cgroup parent tw-vmp<pid>, netns and jail ids twp<pid>-…. A hostd that
// crashed (SIGKILL, OOM, panic) leaves its VMs running and all of that
// behind, and its successor has another pid, so its own cleanupStale never
// sees them (#587). SweepDeadInProcess removes the state of every such
// instance whose hostd is gone; the service helper is unaffected (fixed
// instance, and it stops a connection's VMs when the connection closes).

// InProcessDir is the state dir of the in-process helper of hostd pid.
func InProcessDir(pid int) string { return "/var/lib/tw-vm-p" + strconv.Itoa(pid) }

// SweepDeadInProcess kills the VMs and removes the netns, jails, mounts,
// volumes, cgroups and state of in-process helper instances whose hostd
// process no longer exists (other than self). It returns the pids swept.
func SweepDeadInProcess(self int, log *slog.Logger) []int {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	pids := map[int]bool{}
	add := func(name, prefix string) {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			rest, _, _ = strings.Cut(rest, "-")
			if n, err := strconv.Atoi(rest); err == nil && n > 0 {
				pids[n] = true
			}
		}
	}
	if ents, err := os.ReadDir("/var/lib"); err == nil {
		for _, e := range ents {
			add(e.Name(), "tw-vm-p")
		}
	}
	if ents, err := os.ReadDir("/sys/fs/cgroup"); err == nil {
		for _, e := range ents {
			add(e.Name(), "tw-vmp")
		}
	}
	if ents, err := os.ReadDir("/run/netns"); err == nil {
		for _, e := range ents {
			add(e.Name(), "twp")
		}
	}
	var swept []int
	for pid := range pids {
		if pid == self || processExists(pid) {
			continue // a live hostd (or a reused pid: left alone, never wrongly killed)
		}
		inst := "p" + strconv.Itoa(pid)
		dir := InProcessDir(pid)
		s := &Server{cfg: Config{Instance: inst, JailBase: filepath.Join(dir, "jail"), StateDir: dir}, log: log, cgroup: "tw-vm" + inst}
		s.cleanupStale()
		os.Remove(filepath.Join("/sys/fs/cgroup", s.cgroup))
		os.RemoveAll(dir)
		swept = append(swept, pid)
		log.Info("removed a dead hostd's microVM state", "pid", pid)
	}
	return swept
}

// Sweep removes everything under this helper's own prefix (VMs, netns,
// jails, mounts, volumes). For an in-process helper being closed, after
// its connections are gone.
func (s *Server) Sweep() { s.cleanupStale() }

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
