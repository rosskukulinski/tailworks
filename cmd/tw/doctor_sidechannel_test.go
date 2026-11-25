package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorSideChannelsFixtures drives the side_channels reader from the
// fixture sysfs/proc trees in testdata/sidechannel (issue #334, ledger
// H-host-sidechannel): every mitigation on and SMT off is ok; a Vulnerable
// entry, SMT on, mitigations=off or KSM on each warn and name the problem
// and its fix; a kernel that reports none of it (before 4.15) warns that
// the posture is unknown. The check is never a failure.
func TestDoctorSideChannelsFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir    string
		status string
		detail []string // each must appear in the detail
		fix    []string // each must appear in the fix
		absent []string // none may appear in the detail
	}{
		{
			dir:    "mitigated",
			status: "ok",
			detail: []string{"the 9 CPU vulnerabilities the kernel reports are mitigated or not affected", "SMT off (nosmt)", "KSM off", "more than one tenant", "Node-runtime", "microVM"},
			absent: []string{"Vulnerable"},
		},
		{
			dir:    "vulnerable",
			status: "warn",
			detail: []string{"1 of the 10 CPU vulnerabilities the kernel reports is Vulnerable", "spec_rstack_overflow: Vulnerable: Safe RET, no microcode", "SMT off (nosmt)", "KSM off"},
			fix:    []string{"microcode"},
		},
		{
			dir:    "smt_on",
			status: "warn",
			detail: []string{"SMT (hyper-threading) is on", "mitigated or not affected", "KSM off"},
			fix:    []string{"nosmt", "/sys/devices/system/cpu/smt/control", "or accept it"},
			absent: []string{"Vulnerable", "mitigations=off"},
		},
		{
			dir:    "mitigations_off",
			status: "warn",
			detail: []string{"mitigations=off on the kernel command line", "SMT off (nosmt)"},
			fix:    []string{"remove mitigations=off"},
		},
		{
			dir:    "ksm_on",
			status: "warn",
			detail: []string{"KSM (kernel samepage merging) is on", "SMT off (nosmt)"},
			fix:    []string{"echo 0 > /sys/kernel/mm/ksm/run"},
			absent: []string{"Vulnerable"},
		},
		{
			dir:    "old_kernel",
			status: "warn",
			detail: []string{"doesn't report CPU vulnerabilities", "older than 4.15", "SMT state not reported", "KSM not built in"},
			fix:    []string{"kernel >= 5.13"},
		},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			t.Parallel()
			st, d, fix := readSideChannels(filepath.Join("testdata", "sidechannel", c.dir)).report()
			if st != c.status {
				t.Errorf("status %q, want %q: %s", st, c.status, d)
			}
			for _, w := range c.detail {
				if !strings.Contains(d, w) {
					t.Errorf("detail is missing %q:\n%s", w, d)
				}
			}
			for _, w := range c.absent {
				if strings.Contains(d, w) {
					t.Errorf("detail should not contain %q:\n%s", w, d)
				}
			}
			for _, w := range c.fix {
				if !strings.Contains(fix, w) {
					t.Errorf("fix is missing %q:\n%s", w, fix)
				}
			}
			if st == "ok" && fix != "" {
				t.Errorf("ok with a fix: %s", fix)
			}
			if !strings.Contains(d, "docs/security-model.md") {
				t.Errorf("detail doesn't point at the docs:\n%s", d)
			}
		})
	}
	// The real root never fails the check, whatever this machine reports.
	if st, _, _ := readSideChannels("/").report(); st == "fail" || st == "" {
		t.Errorf("live posture: status %q", st)
	}
}
