package main

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// Whether this host can run containers (ADR-032), issue #496: tw doctor's
// "containers" row, the note on tw up's tier line, and the host-side
// reason in tw deploy's no_host fix. Each reads the running hostd's answer
// (StatusView.ContainersOff, found once when it started); only when hostd
// isn't running does doctor probe this machine itself. None of them ever
// fails: a host without containers is a supported setup.

const containersErofsFix = "install erofs-utils (macOS: brew install erofs-utils; Debian/Ubuntu: sudo apt install erofs-utils), then tw down && tw up"

// containersHostStatus is the running hostd's status (ok false: not running
// or not answering).
func containersHostStatus(s *state) (hostd.StatusView, bool) {
	var hv hostd.StatusView
	if s == nil || !s.configured() || !s.supervisorAlive() {
		return hv, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return hv, s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv) == nil
}

// containersFix is the fix line for a hostd reason ("" = none to give).
func containersFix(reason string) string {
	switch {
	case strings.Contains(reason, "mkfs.erofs"):
		return containersErofsFix
	case strings.Contains(reason, "-no-containers"):
		return "tw-hostd runs with -no-containers: drop the flag to offer them"
	}
	return ""
}

func doctorContainers(s *state) (string, string, string) {
	if s != nil && s.remote() {
		return "skip", "this machine is linked to " + s.Link.Endpoint + ": each host says whether it runs containers (tw host ls: the containers tier; tw doctor on the host)", ""
	}
	hv, ok := containersHostStatus(s)
	if !ok {
		// hostd isn't running: check what this machine can, never fail.
		if _, err := exec.LookPath("mkfs.erofs"); err != nil {
			return "skip", "tw is not running here; mkfs.erofs not found, which containers need (their images are converted to erofs)", containersErofsFix
		}
		return "skip", "tw is not running here (tw up, then tw doctor says whether this host runs containers)", ""
	}
	switch {
	case slices.Contains(hv.Tiers, mgmt.CapContainers):
		return "ok", "offered: ctx.container runs here, one microVM per container", ""
	case hv.ContainersOff == "":
		return "skip", "not offered (this tw-hostd doesn't say why: tw logs --component hostd)", ""
	case hv.ContainersOff == "needs the microVM tier":
		return "skip", "needs the microVM tier, which is off here", ""
	}
	return "skip", "not offered: " + hv.ContainersOff, containersFix(hv.ContainersOff)
}

// containersUpNote is appended to tw up's tier line: ", containers" when
// the host offers them, "; containers off: see tw doctor" when it has the
// microVM tier but no containers ("" otherwise: the line already says the
// microVM tier is off).
func containersUpNote(hv hostd.StatusView) string {
	switch {
	case slices.Contains(hv.Tiers, mgmt.CapContainers):
		return ", containers"
	case slices.Contains(hv.Tiers, "microvm"):
		return "; containers off: see tw doctor"
	}
	return ""
}

// containersNoHostFix is the first fix line of tw deploy's no_host when no
// live host in the pool offers containers: the live hosts and their tiers
// (from mgmt), with the reason this machine's own hostd gave when it is
// one of them. Never a guess from the client's OS.
func containersNoHostFix(s *state, hosts []containersHost) string {
	var parts []string
	var local hostd.StatusView
	var haveLocal bool
	if s != nil && !s.remote() {
		local, haveLocal = containersHostStatus(s)
	}
	for _, h := range hosts {
		if h.Status != "live" {
			continue
		}
		p := h.HostID + " (" + strings.Join(h.Tiers, ",")
		if haveLocal && h.HostID == local.HostID && local.ContainersOff != "" {
			p += "; containers off: " + local.ContainersOff
		}
		parts = append(parts, p+")")
	}
	if len(parts) == 0 {
		return "no live host in the pool: tw host ls"
	}
	return fmt.Sprintf("live hosts: %s; tw doctor on a host says why it has no containers", strings.Join(parts, ", "))
}

// containersLocalState is this machine's default state, for the local
// hostd's reason in the no_host fix (nil: none).
func containersLocalState() *state {
	s, err := openState("")
	if err != nil {
		return nil
	}
	return s
}

// containersHost is the part of mgmt's Host the no_host fix reads.
type containersHost struct {
	HostID string   `json:"hostID"`
	Status string   `json:"status"`
	Tiers  []string `json:"tiers"`
	Arch   string   `json:"arch"`
	Pool   string   `json:"pool"`
}
