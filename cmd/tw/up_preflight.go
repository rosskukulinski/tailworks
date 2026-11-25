package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/procsb"
)

// upPreflight runs before tw up mints anything (#480): the workerd it
// found must run (else hostd fails minutes later, after mgmt's node key is
// spent), and on Linux without Landlock the "NO PROCESS SANDBOX" warning
// is printed up front. Only a workerd that does not run stops tw up; a
// missing sandbox is a supported single-tenant setup (--insecure-shared-host
// for more tenants), so it only warns. `tw doctor` stays the full check.
func upPreflight(wd string, out io.Writer) error {
	if _, fix, err := workerdRuns(wd, "tw doctor checks glibc"); err != nil {
		return fail("workerd_broken", err.Error()).withFix(fix, "tw doctor   (the full check)")
	}
	if runtime.GOOS == "linux" {
		if sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto); sb == hostd.SandboxNone {
			detail, fix := noLandlock()
			fmt.Fprintf(out, "› warning  %s\n           fix: %s\n", detail, fix)
		}
	}
	return nil
}

// workerdRuns runs `wd --version` once: its version line, or why it does
// not run and the fix (tw doctor's "workerd" check and tw up's preflight).
// linuxHint is added to the fix on Linux (where a missing glibc is the
// usual cause).
func workerdRuns(wd, linuxHint string) (version, fix string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, rerr := exec.CommandContext(ctx, wd, "--version").CombinedOutput()
	v := strings.TrimSpace(string(out))
	if rerr == nil {
		return v, "", nil
	}
	fix = "check the binary runs: " + wd + " --version"
	if runtime.GOOS == "linux" {
		fix += " (" + linuxHint + ")"
	}
	if runtime.GOOS == "darwin" {
		fix += " (unsigned? `codesign -f -s - " + wd + "`; quarantined? `xattr -d com.apple.quarantine " + wd + "`)"
	}
	return v, fix, fmt.Errorf("%s does not run: %v %s", wd, rerr, v)
}

// noLandlock is the "NO PROCESS SANDBOX" warning for Linux without
// Landlock, and its fix (tw doctor's "sandbox" check and tw up's preflight).
func noLandlock() (detail, fix string) {
	_, err := procsb.ABI()
	return fmt.Sprintf("NO PROCESS SANDBOX: Landlock is unavailable on this kernel (%v). Process-tier workerd and Node apps of all tenants share one uid and are kept apart by 0700 dirs only, so tw-hostd refuses a second tenant's process-tier deployments (unless started with -insecure-shared-host)", err),
		"use a kernel with Landlock (5.13+, CONFIG_SECURITY_LANDLOCK=y, \"landlock\" in the lsm= boot parameter; Ubuntu 22.04+, Debian 12+, Fedora have it), or deploy with tier \"microvm\""
}
