//go:build e2ereal && !linux

package real

import (
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

type vmBackendT = sandbox.Backend

func realVMBackend(t *testing.T) sandbox.Backend {
	t.Fatal("TW_E2E_TIER=microvm needs Linux + KVM")
	return nil
}
