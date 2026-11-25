package hostd_test

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestInspectorOffOnHost (security ledger C-inspector): a hostd configured
// like a real host (hostdtest builds its Config as tw-hostd does: neither
// ForceProcessTier nor WorkerdInspector) runs a deployed Worker's workerd
// without --inspector-addr and reports no inspector address for it. The
// deployment and the tenant record are what mgmt sends; the inspector is a
// host-side Config knob that only tw dev sets
// (TestInspectorArgsNeedBothDevFlags has the matrix).
func TestInspectorOffOnHost(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	s := startSuite(t, fakemgmt.Config{})
	e := s.e
	e.WaitActive("probe", e.Deploy(hostdtest.Fixture("probe"), "probe", nil), 60*time.Second)
	var pid int
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == "probe" && w.PID > 0 {
			pid = w.PID
		}
	}
	if pid == 0 {
		t.Fatal("no running workerd for probe in status")
	}
	// The real process's command line (the sandbox execs workerd in place,
	// so the pid is workerd's).
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps %d: %v", pid, err)
	}
	args := string(out)
	if !strings.Contains(args, "serve") {
		t.Fatalf("pid %d is not a workerd serve: %q", pid, args)
	}
	if strings.Contains(args, "--inspector-addr") {
		t.Fatalf("a host's workerd runs with an inspector: %q", args)
	}
	if a := e.Host.InspectorAddr(e.TenantID, "probe"); a != "" {
		t.Fatalf("InspectorAddr on a host: %q, want none", a)
	}
}
