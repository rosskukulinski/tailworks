//go:build !linux && !darwin

package hostdrun

import (
	"log/slog"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// vmBackend: the microVM tier needs Linux + KVM or macOS; this OS offers the
// process tier only, and microvm deployments are refused (tier_unavailable),
// never downgraded.
func vmBackend(VMOptions, *slog.Logger) sandbox.Backend { return nil }
