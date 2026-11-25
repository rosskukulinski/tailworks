//go:build linux

package vm

import (
	"os"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

const guestSeesCPUCap = false

// ctrSmokeBackend is Firecracker (needs root and KVM,
// TW_VM_CONTAINER_KERNEL with erofs, TW_VM_ROOTFS).
func ctrSmokeBackend(t *testing.T) (sandbox.ContainerBackend, []string) {
	t.Helper()
	kernel, rootfs := os.Getenv("TW_VM_CONTAINER_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_CONTAINER_KERNEL / TW_VM_ROOTFS not set")
	}
	// tw-hostd's defaults: the warm pool (auto) and snapshots on. They
	// are for Durable Object guests; containers must still cold-boot next
	// to them (checkCtrColdBoot).
	b, err := New(Config{Kernel: kernel, ContainerKernel: kernel, Rootfs: rootfs, BootArgs: os.Getenv("TW_VM_BOOTARGS"), HugePages: os.Getenv("TW_VM_HUGEPAGES"), Helper: os.Getenv("TW_VM_HELPER"),
		Snapshot: os.Getenv("TW_VM_E2E_SNAPSHOT") != "0"})
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { b.Close() })
	return b, nil
}

// checkCtrConfinement: the VMM is jailed (own uid) and capped (cpu.max on
// its cgroup).
func checkCtrConfinement(t *testing.T, info sandbox.Info, cpuMax string) {
	t.Helper()
	if j := info.Jail; j == nil || j.UID < 1000 || !strings.HasPrefix(j.CPUMax, cpuMax) {
		t.Fatalf("jail/cpu.max: %+v", j)
	}
}

// checkCtrColdBoot (ADR-032 × fast-vm): a container VM is cold-booted (its
// own config drive, image and scratch) even on a backend whose warm pool and
// base snapshot are on: it never claims a pooled VM or restores the base.
func checkCtrColdBoot(t *testing.T, b sandbox.ContainerBackend, info sandbox.Info) {
	t.Helper()
	if info.Source != "cold" || info.Base != "" {
		t.Fatalf("container VM source %q (base %q), want a cold boot", info.Source, info.Base)
	}
	if ps := b.(*Backend).PoolStats(); ps != nil && (ps.Claims != 0 || ps.Misses != 0) {
		t.Fatalf("a container boot touched the warm pool: %+v", ps)
	}
	if len(info.Stages) == 0 {
		t.Fatal("no boot stage timestamps for the container VM")
	}
	t.Logf("container VM: cold boot next to pool %+v; stages %v", b.(*Backend).PoolStats(), info.Stages)
}
