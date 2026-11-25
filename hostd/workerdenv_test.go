package hostd_test

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestWorkerdEnvNotInherited (#336, ledger H-workerd-env): a process-tier
// workerd gets the environment hostd builds for it (hostd.WorkerdEnv) and
// nothing of hostd's own. A canary set in hostd's environment must not reach
// the workerd process, and neither may any other variable hostd has, under
// no sandbox and under this platform's sandbox (Seatbelt through
// sandbox-exec; Landlock through the procsb launcher, which execs workerd
// itself). Read from the live process: /proc/<pid>/environ on Linux, `ps
// -E` on macOS.
func TestWorkerdEnvNotInherited(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const canary = "TW_TEST_CANARY_336"
	t.Setenv(canary, "leaked-from-hostd") // so this test is not parallel
	sandboxes := []string{hostd.SandboxNone}
	if sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto); sb != hostd.SandboxNone {
		sandboxes = append(sandboxes, sb)
	}
	for _, sb := range sandboxes {
		t.Run(sb, func(t *testing.T) {
			s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{
				nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
			}, func(o *hostdtest.Options) { o.Sandbox = sb })
			e := s.e
			e.WaitActive("probe", e.Deploy(hostdtest.Fixture("probe"), "probe", nil), 60*time.Second)
			var ws hostd.WorkloadStatus
			for _, w := range e.Host.Status().Workloads {
				if w.Workload == "probe" && w.PID > 0 {
					ws = w
				}
			}
			if ws.PID == 0 {
				t.Fatal("no running probe workerd in status")
			}
			if ws.Sandbox != sb {
				t.Fatalf("workerd runs under sandbox %q, want %q", ws.Sandbox, sb)
			}
			got := processEnv(t, ws.PID)
			names := make([]string, 0, len(got)) // names only: hostd's values may be secrets
			for k := range got {
				names = append(names, k)
			}
			sort.Strings(names)
			t.Logf("workerd pid %d (%s) has %d environment variables: %v", ws.PID, sb, len(got), names)
			if _, ok := got[canary]; ok {
				t.Errorf("hostd's %s reached the workerd process", canary)
			}
			want := map[string]string{}
			for _, kv := range hostd.WorkerdEnv() {
				k, v, _ := strings.Cut(kv, "=")
				want[k] = v
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("workerd env %s=%q, want %q", k, got[k], v)
				}
			}
			// Nothing of hostd's own: every variable the test process has that
			// WorkerdEnv doesn't pass is absent.
			for _, kv := range os.Environ() {
				k, _, _ := strings.Cut(kv, "=")
				if _, passed := want[k]; passed {
					continue
				}
				if _, ok := got[k]; ok {
					t.Errorf("hostd's %s reached the workerd process", k)
				}
			}
			if runtime.GOOS == "linux" && len(got) != len(want) {
				t.Errorf("workerd env has %d variables %v, want exactly %d (WorkerdEnv)", len(got), names, len(want))
			}
		})
	}
}

// processEnv is the live environment of pid, as a map. Linux reads
// /proc/<pid>/environ (exact: NUL-separated). macOS parses `ps -E`, whose
// output is space-separated, so a value with a space in it is cut; the
// names are right, which is what the test checks.
func processEnv(t *testing.T, pid int) map[string]string {
	t.Helper()
	env := map[string]string{}
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil {
			t.Fatalf("read pid %d environ: %v", pid, err)
		}
		for _, kv := range strings.Split(string(b), "\x00") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				env[k] = v
			}
		}
		return env
	}
	out, err := exec.Command("ps", "-E", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -E -p %d: %v", pid, err)
	}
	for _, f := range strings.Fields(string(out)) {
		if k, v, ok := strings.Cut(f, "="); ok && k != "" && !strings.ContainsAny(k, "/-.") {
			env[k] = v
		}
	}
	return env
}
