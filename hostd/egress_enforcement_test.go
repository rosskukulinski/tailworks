package hostd

import "testing"

// Node-tier egress is an enforcement boundary only under Landlock with TCP
// rules (ABI ≥ 4). Below that, status must not say "enforced" (issue #100).
func TestEgressEnforcementByABI(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		sandbox string
		abi     int
		want    string
	}{
		{SandboxNone, 0, EgressUnenforced},
		{SandboxSeatbelt, 0, EgressEnforced},
		{SandboxSeatbeltLegacy, 0, EgressUnenforced},
		{SandboxLandlock, 0, EgressUnenforced},
		{SandboxLandlock, 1, EgressUnenforced},
		{SandboxLandlock, 2, EgressUnenforced},
		{SandboxLandlock, 3, EgressUnenforced},
		{SandboxLandlock, 4, EgressEnforced},
		{SandboxLandlock, 6, EgressEnforced},
	} {
		if got := egressEnforcement(c.sandbox, c.abi); got != c.want {
			t.Errorf("egressEnforcement(%q, ABI %d) = %q, want %q", c.sandbox, c.abi, got, c.want)
		}
	}
}
