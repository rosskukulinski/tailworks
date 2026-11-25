package hostd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestProcessTierHeapCaps (#115): with no configuration, a process-tier
// workerd runs with V8's heap capped at hostd.DefaultWorkerdHeapMiB and a
// Node-tier app with --max-old-space-size=hostd.DefaultNodeHeapMiB. What
// the cap does to a Worker that passes it is TestGeneratedHeapCap.
func TestProcessTierHeapCaps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	s := startSuite(t, fakemgmt.Config{})
	e := s.e
	status := func(name string) hostd.WorkloadStatus {
		for _, w := range e.Host.Status().Workloads {
			if w.Workload == name && w.PID > 0 {
				return w
			}
		}
		t.Fatalf("no running %s in status", name)
		return hostd.WorkloadStatus{}
	}

	e.WaitActive("probe", e.Deploy(hostdtest.Fixture("probe"), "probe", nil), 60*time.Second)
	cfg, err := os.ReadFile(filepath.Join(status("probe").InstanceDir, "config.capnp"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"--max-old-space-size=` + strconv.Itoa(hostd.DefaultWorkerdHeapMiB) + `"`; !strings.Contains(string(cfg), want) {
		t.Fatalf("workerd config has no %s:\n%s", want, cfg)
	}

	if _, err := os.Stat(hostdtest.NodeDeps()); err != nil {
		t.Skip("node tier deps not installed (make test-node)")
	}
	e.WaitActive("nodeprobe", e.DeployNode(hostdtest.Fixture("nodeprobe"), "nodeprobe", nil), 60*time.Second)
	pid := status("nodeprobe").PID
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps %d: %v", pid, err)
	}
	if want := "--max-old-space-size=" + strconv.Itoa(hostd.DefaultNodeHeapMiB); !strings.Contains(string(out), want) {
		t.Fatalf("node args %q have no %s", out, want)
	}
}
