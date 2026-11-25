package mgmt

import (
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// A tailnet.connects target that is another tailworks app's tag (say
// tag:tw-wl-mcp, before mcp is deployed) has no tagOwners entry yet, and
// the control plane rejects a grant to an undefined tag. tailworks owns the
// tag:tw* namespace (RT-06), so the preflight adds the entry deploying that
// app would add, and tw policy --apply writes it with the grants. Tags
// outside tag:tw* (tag:db) stay the admin's to define, and so do all of a
// team tenant's (one with its own owner tag): the target may be another
// team's app on the same tailnet, and defining it here would claim it.

// undefinedConnectsTags are the tag:tw* connects targets the policy has no
// tagOwners entry for, in connects order, without the app's own tags
// (srcTags; MissingTagOwners covers those). Nil for a team tenant.
func undefinedConnectsTags(t Tenant, pol *Policy, connects, srcTags []string) []string {
	if t.OwnerTag != "" {
		return nil
	}
	var out []string
	for _, dst := range ConnectTargets(connects) {
		if !strings.HasPrefix(dst, "tag:") || !policy.IsTwTag(dst) {
			continue
		}
		if _, ok := pol.TagOwners[dst]; ok || slices.Contains(srcTags, dst) || slices.Contains(out, dst) {
			continue
		}
		out = append(out, dst)
	}
	return out
}
