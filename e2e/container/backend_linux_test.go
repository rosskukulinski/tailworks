//go:build e2econtainer && linux

package container

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/vm"
)

// memoryDumpRequired: the strict keyscan must cover guest RAM (Firecracker
// snapshots it).
const memoryDumpRequired = true

// newBackend is Firecracker: one jailed VM per container.
func newBackend(t *testing.T) (sandbox.ContainerBackend, []string) {
	t.Helper()
	kernel, ckernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_CONTAINER_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || ckernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL, TW_VM_CONTAINER_KERNEL and TW_VM_ROOTFS must be set (make e2e-container)")
	}
	// tw-hostd's defaults: warm pool (auto) and snapshots on, for the
	// parents' DO VMs; containers cold-boot next to them.
	be, err := vm.New(vm.Config{Kernel: kernel, ContainerKernel: ckernel, Rootfs: rootfs, BootArgs: os.Getenv("TW_VM_BOOTARGS"), HugePages: os.Getenv("TW_VM_HUGEPAGES"), Helper: os.Getenv("TW_VM_HELPER"),
		Snapshot: os.Getenv("TW_VM_E2E_SNAPSHOT") != "0"})
	if err != nil {
		t.Fatalf("microVM backend unavailable: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be, nil
}

// checkConfinement: the VMM runs jailed (own uid) under its cgroup's cpu.max.
func checkConfinement(t *testing.T, j *sandbox.Jail, cpuMax string) {
	t.Helper()
	if j == nil || j.UID < 1000 || !strings.HasPrefix(j.CPUMax, cpuMax) {
		t.Fatalf("container not jailed / cpu.max %s: %+v", cpuMax, j)
	}
}

// hostPids counts the host's processes (a container's fork bomb must not
// move it).
func hostPids() int {
	ents, _ := os.ReadDir("/proc")
	n := 0
	for _, d := range ents {
		if d.Name()[0] >= '0' && d.Name()[0] <= '9' {
			n++
		}
	}
	return n
}

func processGone(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return os.IsNotExist(err)
}
