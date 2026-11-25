package mgmt

import (
	"context"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// policyWritable is the dryRun plan's canWrite (issue #481): read the
// tenant's policy, merge snip and ask the control plane to validate it.
// Tailscale answers 403 to an OAuth client without Policy File write, so
// a client that may only read is found out before tw deploy asks. Any
// failure is false: tw deploy then prints the snippet instead of asking.
func (m *Mgmt) policyWritable(ctx context.Context, t Tenant, snip policy.Snippet, o policy.Options) bool {
	api, err := m.broker.API(ctx, t, purposePolicy)
	if err != nil {
		return false
	}
	pa, ok := api.(policy.API)
	if !ok {
		return false
	}
	cur, _, err := pa.GetPolicyHuJSON(ctx)
	if err != nil {
		return false
	}
	next, err := policy.MergeWith(cur, snip, o)
	if err != nil {
		return false
	}
	return pa.ValidatePolicy(ctx, next) == nil
}
