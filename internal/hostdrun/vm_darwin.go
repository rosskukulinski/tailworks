//go:build darwin

package hostdrun

import (
	"log/slog"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/vm"
)

// vmBackend enables the microVM tier on macOS when this host can run it:
// Virtualization.framework, the guest kernel and image, and a tw-vz that
// carries the virtualization entitlement (ADR-032 D7). No root, no helper
// service: each VM is one tw-vz process. The fast-VM options (pool,
// snapshots) are Firecracker only; vz ignores them.
func vmBackend(o VMOptions, log *slog.Logger) sandbox.Backend {
	b, err := vm.New(vm.Config{VMM: o.VzVMM, Kernel: o.Kernel, Rootfs: o.Rootfs, MemMiB: o.MemMiB, VCPUs: o.VCPUs,
		Rosetta: o.Rosetta, Log: log})
	if err != nil {
		log.Info("microVM tier unavailable; process tier only", "reason", err)
		return nil
	}
	log.Info("microVM tier available", "backend", b.Name(), "via", b.Via(), "kernel", o.Kernel, "rootfs", o.Rootfs)
	return b
}
