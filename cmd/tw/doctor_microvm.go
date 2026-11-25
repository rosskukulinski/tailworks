package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/vm"
	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
)

// doctorMicroVM (Linux): can this user run the microVM tier? KVM, the
// guest kernel and image from the release, and the tw-vmhelper service
// (installed by `sudo tw host setup --microvm`) answering this uid. If
// not, it's a warning, not a failure: microvm deployments are refused (not
// run in the process tier).
func doctorMicroVM(s *state) (string, string, string) {
	const setup = "sudo ~/.tailworks/bin/tw host setup --microvm   (one time; then tailworks runs as you)"
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return "warn", "no /dev/kvm: microvm deployments are refused here (tier_unavailable)", "run on a host with KVM (bare metal, or a VM with nested virtualization)"
	}
	if in, npm := npmSelf(); npm { // npm_install.go
		return "warn", "the npm package (" + in.Package + ") has no microVM guest: microvm deployments are refused here (tier_unavailable)", "install the release tarball for the microVM tier: scripts/get.sh (docs/INSTALL.md)"
	}
	share := filepath.Join(filepath.Dir(exeDir()), "share", "tailworks", "vm")
	for _, a := range []string{"vmlinux", "rootfs.ext4"} {
		if !isFile(filepath.Join(share, a)) {
			return "warn", "this release has no microVM guest (" + filepath.Join(share, a) + " missing): process tier only", "install a linux release built with the microVM assets"
		}
	}
	if err := vm.CheckArch(filepath.Join(share, "vmlinux"), filepath.Join(share, "rootfs.ext4"), runtime.GOARCH); err != nil {
		return "warn", err.Error() + ": process tier only", "install the linux-" + runtime.GOARCH + " release"
	}
	r, err := helperapi.Ping(helperapi.DefaultSocket)
	if err != nil {
		why := err.Error()
		if os.IsNotExist(err) || strings.Contains(why, "no such file") {
			why = "the tw-vmhelper service is not installed"
		} else if helperapi.IsCode(err, helperapi.CodeUnauthorized) {
			why = fmt.Sprintf("the tw-vmhelper service does not allow uid %d", os.Getuid())
		}
		return "warn", why + ": microvm deployments are refused here (tier_unavailable)", setup
	}
	if err := helperapi.Check(r); err != nil {
		return "warn", err.Error(), setup
	}
	detail := fmt.Sprintf("KVM ok; guest kernel + image in %s; tw-vmhelper answers uid %d (jailer %s, %s)", share, r.UID, r.Jailer, r.Crypt)
	if s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var hv hostd.StatusView
		if s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv) == nil {
			n, vms := 0, 0
			for _, w := range hv.Workloads {
				if w.Tier == "microvm" {
					n++
					if w.VM != nil && w.VM.Jail != nil {
						vms++
					}
				}
			}
			if !strings.Contains(strings.Join(hv.Tiers, ","), "microvm") {
				return "warn", detail + "; but the running tw-hostd has no microVM tier (restart it: tw down && tw up …)", "tw logs --component hostd"
			}
			detail += fmt.Sprintf("; running: %d/%d microvm workloads jailed (%s)", vms, n, hv.VMVia)
		}
	}
	return "ok", detail, ""
}
