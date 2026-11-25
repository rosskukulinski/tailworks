package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// What tw tenant add asks of a tenant tailnet's policy (issue #475). A
// tenant tailnet never hosts mgmt or a host node (those live on the
// operator's tailnet), so the operator's InfraSnippet (tag:tw-mgmt,
// tag:tw-host and the host ⇄ mgmt grants) is not wanted there. All a
// tenant needs is its owner tag (tag:tw, or a team's --owner-tag) in
// tagOwners, since tailworks' tags are owned by it:
//
//   - Tailscale: tw writes nothing. The tag must already exist (the admin
//     console only scopes an OAuth client to an existing tag), so this is a
//     read-only check: a missing entry is reported with the line to paste.
//     An existing entry is never touched, so a key is never widened.
//   - Headscale: "<owner>": [] (only the API key mints it), written with
//     --apply-policy, or after a y on a terminal (#481), behind the same
//     confirm as tw up.

// tenantOwnerTag is the tag that owns a tenant's tags.
func tenantOwnerTag(ownerTag string) string {
	if ownerTag != "" {
		return ownerTag
	}
	return policy.TwTagPrefix
}

// tenantPolicySnippet is what the tenant's policy needs: its owner tag in
// tagOwners. On Tailscale, the owners shown are the suggested paste line
// (the tailnet's admins); presence of the key is all that is checked.
func tenantPolicySnippet(profile, owner string) (policy.Snippet, policy.Options) {
	if profile == controlHeadscale {
		return policy.Snippet{TagOwners: map[string][]string{owner: {}}}, policy.Options{Headscale: true}
	}
	return policy.Snippet{TagOwners: map[string][]string{owner: {"autogroup:admin"}}}, policy.Options{}
}

// tenantPolicy fills res.PolicyOK, res.PolicyDiff and res.Applied for the
// tenant's policy pol (readable false: the OAuth client can't read it, and
// the first mint proves ownership instead). Only Headscale with
// --apply-policy writes.
func tenantPolicy(ctx context.Context, f *flags, res *tenantAddResult, api controlAPI, pol []byte, readable bool, profile, owner string, apply, yes bool) error {
	if !readable {
		res.PolicyOK = true
		return nil
	}
	want, popts := tenantPolicySnippet(profile, owner)
	// Presence only: an existing entry keeps the owners the admin chose.
	miss, err := policy.Missing(pol, policy.Snippet{TagOwners: map[string][]string{owner: {}}})
	if err != nil {
		return err
	}
	if miss.Empty() {
		res.PolicyOK = true
		return nil
	}
	res.PolicyDiff = want.HuJSON()
	if profile != controlHeadscale {
		return nil
	}
	// On a terminal it asks without the flag too (policy_ask.go, #481).
	write, err := bootstrapPolicyGate(ctx, api, f, policyModeOf(f, apply, yes), yes, "tw tenant add", profile, pol, want, popts, want, nil)
	if err != nil || !write {
		return err
	}
	ar, err := policy.ApplyWith(ctx, api, want, popts)
	if err != nil {
		return fail("policy_apply_failed", "could not apply the policy snippet", err.Error()).withFix("add it in "+policyHome(profile)+":", res.PolicyDiff)
	}
	res.Applied, res.PolicyOK = ar.Changed, true
	return nil
}

// tenantPolicyReport prints the policy line of tw tenant add.
func tenantPolicyReport(res tenantAddResult, readable bool, profile, owner string, apply bool) {
	switch {
	case res.Applied:
		fmt.Printf("✓ policy   applied %s tagOwners (validated, If-Match)\n", owner)
	case res.PolicyOK && !readable:
		fmt.Printf("✓ policy   not checked (the OAuth client can't read the policy); the first mint proves %s ownership\n", owner)
	case res.PolicyOK:
		fmt.Printf("✓ policy   %s tagOwners present\n", owner)
	default:
		how := "re-run with --apply-policy, or add it in " + policyHome(profile)
		if profile != controlHeadscale {
			how = "tw writes nothing to a tenant's Tailscale policy; add it in " + policyHome(profile)
			if apply {
				how = "--apply-policy writes nothing on a Tailscale tenant; add it in " + policyHome(profile)
			}
		}
		fmt.Printf("› policy   %s is not in tagOwners (%s):\n", owner, how)
		for _, l := range strings.Split(strings.TrimSpace(res.PolicyDiff), "\n") {
			fmt.Printf("    %s\n", l)
		}
	}
}
