//go:build linux

package helper

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// TestSweepDeadInProcess (#587, found by the e2e/dataplane containers
// loop): the in-process helper of a hostd that crashed left its VMs, netns,
// jails and cgroups behind, and its successor (another pid) never removed
// them. SweepDeadInProcess kills the dead instance's VMMs (here a stand-in
// process in one of its VM cgroups) and removes its netns, jail and state;
// a live hostd's instance is left alone. Needs root and cgroup v2.
func TestSweepDeadInProcess(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root (netns, cgroups)")
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("needs cgroup v2")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("needs ip (iproute2)")
	}
	// A pid that is gone: a process that ran and was reaped.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dead := gone.Process.Pid
	// A live "hostd": its instance must survive the sweep.
	liveCmd := exec.Command("sleep", "300")
	if err := liveCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveCmd.Process.Kill(); liveCmd.Wait() })
	live := liveCmd.Process.Pid

	type inst struct{ netns, cg, vmcg, jail, dir string }
	mk := func(pid int) inst {
		t.Helper()
		p := "twp" + strconv.Itoa(pid) + "-0"
		in := inst{netns: p, cg: "/sys/fs/cgroup/tw-vmp" + strconv.Itoa(pid), dir: InProcessDir(pid)}
		in.vmcg = filepath.Join(in.cg, p)
		in.jail = filepath.Join(in.dir, "jail", "firecracker", p, "root")
		if b, err := exec.Command("ip", "netns", "add", in.netns).CombinedOutput(); err != nil {
			t.Fatalf("ip netns add: %v %s", err, b)
		}
		if err := os.MkdirAll(in.vmcg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(in.jail, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			exec.Command("ip", "netns", "del", in.netns).Run()
			os.WriteFile(filepath.Join(in.vmcg, "cgroup.kill"), []byte("1"), 0o644)
			time.Sleep(50 * time.Millisecond)
			os.Remove(in.vmcg)
			os.Remove(in.cg)
			os.RemoveAll(in.dir)
		})
		return in
	}
	d, l := mk(dead), mk(live)
	// The dead instance's "VMM": a process in its VM cgroup.
	vmm := exec.Command("sleep", "300")
	if err := vmm.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vmm.Process.Kill() })
	if err := os.WriteFile(filepath.Join(d.vmcg, "cgroup.procs"), []byte(strconv.Itoa(vmm.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { vmm.Wait(); close(exited) }()

	swept := SweepDeadInProcess(os.Getpid(), nil)
	if !slices.Contains(swept, dead) || slices.Contains(swept, live) {
		t.Fatalf("swept %v, want %d and not %d", swept, dead, live)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the dead instance's VMM is still running")
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists("/run/netns/"+d.netns) || exists(d.dir) || exists(d.cg) {
		t.Fatalf("dead instance left: netns %v, dir %v, cgroup %v", exists("/run/netns/"+d.netns), exists(d.dir), exists(d.cg))
	}
	if !exists("/run/netns/"+l.netns) || !exists(l.jail) || !exists(l.vmcg) {
		t.Fatal("the live instance's state was removed")
	}
}
