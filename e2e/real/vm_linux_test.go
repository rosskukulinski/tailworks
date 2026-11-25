//go:build e2ereal && linux

package real

import (
	"os"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/vm"
)

type vmBackendT = sandbox.Backend

func realVMBackend(t *testing.T) sandbox.Backend {
	b, err := vm.New(vm.Config{Kernel: os.Getenv("TW_VM_KERNEL"), Rootfs: os.Getenv("TW_VM_ROOTFS")})
	if err != nil {
		t.Fatalf("TW_E2E_TIER=microvm: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}
