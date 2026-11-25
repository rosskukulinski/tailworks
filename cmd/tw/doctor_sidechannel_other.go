//go:build !linux

package main

import "runtime"

// doctorSideChannels on macOS (and any other non-Linux OS): the kernel
// doesn't expose its CPU side-channel mitigations, SMT state or page
// merging the way Linux does, so the posture can't be read. A macOS host
// is development-grade for tenants that don't trust each other.
func doctorSideChannels(_ *state) (string, string, string) {
	osn := runtime.GOOS
	if osn == "darwin" {
		osn = "macOS"
	}
	return "skip", "the CPU side-channel posture (kernel mitigations, SMT, page merging) can't be read on " + osn + ", so a " + osn + " host is development-grade for tenants that don't trust each other; " + sideChannelMatters,
		"for tenants that don't trust each other, use a Linux host with its kernel mitigations on and SMT off (docs/INSTALL.md, the microVM tier)"
}
