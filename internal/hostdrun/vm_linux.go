//go:build linux

package hostdrun

import (
	"log/slog"
	"os"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/vm"
)

// vmBackend enables the microVM tier when this host can run it: KVM, the
// guest kernel and image, and the tw-vmhelper service (`sudo tw host
// setup --microvm`; as root without it, an in-process helper). Otherwise
// microvm deployments are refused (tier_unavailable), never downgraded.
func vmBackend(o VMOptions, log *slog.Logger) sandbox.Backend {
	if os.Geteuid() == 0 {
		if _, err := os.Stat(o.Helper); err != nil {
			o.Helper = "" // root without the service: in-process helper
		}
	}
	b, err := vm.New(vm.Config{Helper: o.Helper, Firecracker: o.Firecracker, Jailer: o.Jailer, Kernel: o.Kernel, Rootfs: o.Rootfs,
		MemMiB: o.MemMiB, VCPUs: o.VCPUs, HugePages: o.HugePages, ContainerKernel: o.ContainerKernel,
		Pool: o.Pool, Snapshot: !o.NoSnapshot, SnapshotRefill: o.SnapRefill, BaseRotate: o.Rotate, Log: log})
	if err != nil {
		log.Info("microVM tier unavailable; process tier only", "reason", err)
		return nil
	}
	log.Info("microVM tier available", "backend", b.Name(), "via", b.Via(), "kernel", o.Kernel, "rootfs", o.Rootfs)
	return b
}
