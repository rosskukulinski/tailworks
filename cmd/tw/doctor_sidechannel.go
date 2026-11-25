package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// tw doctor's side_channels check (issue #334). tailworks separates tenants
// with a process or a VM per workload; what keeps one tenant's code from
// reading another's secrets (or tw-hostd's and tw-mgmt's node keys, the
// custody key and the mint credential) off a shared core is the host
// kernel's and CPU's side-channel mitigations. Nothing in tailworks turns
// them on, so doctor reports what the kernel says about them. The reader
// takes the sysfs/proc root as a parameter so a test can drive it from a
// fixture tree (testdata/sidechannel); the Linux check passes "/".
//
// It is a warning, never a failure: on a single-tenant host that runs
// workerd only, nothing is at stake, and the kernel's wording varies by CPU
// (docs/security-model.md, "Side channels", says why there is no gate).

// sideChannelPosture is what the kernel reports about the host's
// side-channel mitigations.
type sideChannelPosture struct {
	// Vulnerable lists the /sys/devices/system/cpu/vulnerabilities entries
	// whose status starts with "Vulnerable", as "name: status".
	Vulnerable []string
	// Reported is how many vulnerability entries the kernel reports (0 with
	// NoReport: the directory is missing, kernels before 4.15).
	Reported int
	NoReport bool
	// SMT is "on", "off" or "" when the kernel doesn't report it
	// (/sys/devices/system/cpu/smt/active; absent before 4.17 and on CPUs
	// without SMT).
	SMT string
	// MitigationsOff and NoSMT are the kernel command line's
	// mitigations=off and nosmt (/proc/cmdline).
	MitigationsOff bool
	NoSMT          bool
	// KSM is "on", "off" or "" when kernel samepage merging is not built in
	// (/sys/kernel/mm/ksm/run).
	KSM string
}

// readSideChannels reads the posture from the sysfs and proc tree under root.
func readSideChannels(root string) sideChannelPosture {
	var p sideChannelPosture
	vdir := filepath.Join(root, "sys", "devices", "system", "cpu", "vulnerabilities")
	ents, err := os.ReadDir(vdir)
	if err != nil {
		p.NoReport = true
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(vdir, e.Name()))
		if err != nil {
			continue
		}
		p.Reported++
		st := strings.TrimSpace(string(b))
		if strings.HasPrefix(st, "Vulnerable") {
			p.Vulnerable = append(p.Vulnerable, e.Name()+": "+st)
		}
	}
	sort.Strings(p.Vulnerable)
	switch readTrim(filepath.Join(root, "sys", "devices", "system", "cpu", "smt", "active")) {
	case "1":
		p.SMT = "on"
	case "0":
		p.SMT = "off"
	}
	for _, f := range strings.Fields(readTrim(filepath.Join(root, "proc", "cmdline"))) {
		switch f {
		case "mitigations=off":
			p.MitigationsOff = true
		case "nosmt", "nosmt=force":
			p.NoSMT = true
		}
	}
	switch readTrim(filepath.Join(root, "sys", "kernel", "mm", "ksm", "run")) {
	case "0":
		p.KSM = "off"
	case "1", "2":
		p.KSM = "on"
	}
	return p
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// sideChannelMatters says when the posture matters; it ends every
// side_channels detail.
const sideChannelMatters = "this matters when the host serves more than one tenant, runs a Node-runtime workload or offers the microVM tier: those run native code, and the kernel's and CPU's mitigations are then what keeps a tenant from reading another tenant's or tailworks' secrets off a shared core (docs/security-model.md, Side channels)"

// report turns the posture into a check result: ok when everything the
// kernel reports is mitigated, SMT is off and KSM is off; otherwise a
// warning that lists each problem and its fix.
func (p sideChannelPosture) report() (status, detail, fix string) {
	var problems, fixes, fine []string
	switch {
	case p.NoReport:
		problems = append(problems, "the kernel doesn't report CPU vulnerabilities (no /sys/devices/system/cpu/vulnerabilities: older than 4.15), so the posture is unknown")
		fixes = append(fixes, "use a kernel >= 5.13 (what the process-tier sandbox needs anyway)")
	case len(p.Vulnerable) > 0:
		verb := "are"
		if len(p.Vulnerable) == 1 {
			verb = "is"
		}
		problems = append(problems, fmt.Sprintf("%d of the %d CPU vulnerabilities the kernel reports %s Vulnerable (%s)", len(p.Vulnerable), p.Reported, verb, strings.Join(p.Vulnerable, "; ")))
		fixes = append(fixes, "update the CPU microcode and the kernel, and keep the kernel's default mitigations")
	default:
		fine = append(fine, fmt.Sprintf("the %d CPU vulnerabilities the kernel reports are mitigated or not affected", p.Reported))
	}
	if p.MitigationsOff {
		problems = append(problems, "mitigations=off on the kernel command line: every CPU side-channel mitigation is disabled")
		fixes = append(fixes, "remove mitigations=off from the kernel command line and reboot")
	}
	switch p.SMT {
	case "on":
		problems = append(problems, "SMT (hyper-threading) is on: a sibling thread can observe this one's cache and execution-port use")
		fixes = append(fixes, "turn SMT off (nosmt on the kernel command line, or echo off > /sys/devices/system/cpu/smt/control), or accept it for the tenants this host serves")
	case "off":
		if p.NoSMT {
			fine = append(fine, "SMT off (nosmt)")
		} else {
			fine = append(fine, "SMT off")
		}
	default:
		fine = append(fine, "SMT state not reported")
	}
	switch p.KSM {
	case "on":
		problems = append(problems, "KSM (kernel samepage merging) is on: merged pages let one process time another's memory contents")
		fixes = append(fixes, "echo 0 > /sys/kernel/mm/ksm/run, and keep it off at boot")
	case "off":
		fine = append(fine, "KSM off")
	default:
		fine = append(fine, "KSM not built in")
	}
	if len(problems) == 0 {
		return "ok", strings.Join(fine, ", ") + "; " + sideChannelMatters, ""
	}
	detail = strings.Join(problems, "; ")
	if len(fine) > 0 {
		detail += " (" + strings.Join(fine, ", ") + ")"
	}
	return "warn", detail + "; " + sideChannelMatters, strings.Join(fixes, "; ")
}
