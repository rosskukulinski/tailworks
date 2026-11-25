# fix-587-reap-dead-vm-helpers

#587 (found by the e2e/dataplane containers loop, #423): with hostd as root
and the in-process microVM helper, a crashed hostd's Firecracker VMs, netns,
jails and cgroups were never reaped (18 of 18 VMMs survived 4 crashes).

Fix: `hostd/vm/helper/deadinst_linux.go` `SweepDeadInProcess` runs the
helper's existing `cleanupStale` for every in-process instance
(`p<pid>`) whose hostd pid no longer exists, then removes its state dir and
cgroup. `hostd/vm/vm_linux.go` calls it before starting the in-process
helper, and sweeps its own prefix after closing it (a clean stop left one
netns behind).

Regression test: `hostd/vm/helper` `TestSweepDeadInProcess` (root, cgroup
v2; skipped otherwise). Run on the twd-amd64 KVM guest: pass. Its first
run also removed 118 netns that earlier loop runs had leaked.
