package main_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
)

// upApplyPolicyRefusals is tw up --apply-policy (issue #320) through the
// real binary, on a policy in which tag:tw doesn't own tag:tw-mgmt: every
// way it stops is before any key is minted. up is the test's `tw up`
// command line (with --yes); counts reports the keys minted since and the
// fake API's POST /acl requests (it answers each with a 500).
func upApplyPolicyRefusals(t *testing.T, e *cliflow.Env, up []string, counts func() (minted, policyPosts int)) {
	t.Helper()
	// No terminal, no --yes: the lines are shown, nothing is written.
	noYes := slices.DeleteFunc(slices.Clone(up), func(a string) bool { return a == "--yes" })
	for _, flag := range []string{"--apply-policy"} {
		so, se, _, err := e.Tw(append(slices.Clone(noYes), flag)...)
		if err == nil || !strings.Contains(se, "confirmation_required") || !strings.Contains(se, "tw up --apply-policy --yes") {
			t.Fatalf("tw up %s without --yes: %v\n%s\n%s", flag, err, so, se)
		}
		for _, want := range []string{"tailworks will add to the tailnet policy", `+   "tag:tw-mgmt": ["tag:tw"],`, `+   "tag:tw-host": ["autogroup:admin", "tag:tw"],`} {
			if !strings.Contains(so, want) {
				t.Fatalf("tw up %s without --yes: output lacks %q:\n%s\n%s", flag, want, so, se)
			}
		}
		if strings.Contains(so+se, "policy_missing_tag_owners") {
			t.Fatalf("tw up %s stopped at the tagOwners check instead of offering to write them:\n%s", flag, se)
		}
		if minted, posts := counts(); minted != 0 || posts != 0 {
			t.Fatalf("tw up %s without --yes: %d keys minted, %d policy writes", flag, minted, posts)
		}
	}
	// --policy-apply, the flag's name before #320, is gone.
	if _, se, _, err := e.Tw(append(slices.Clone(up), "--policy-apply")...); err == nil || !strings.Contains(se, "bad_request") {
		t.Fatalf("tw up --policy-apply: %v\n%s", err, se)
	}
	// --yes, and the control plane refuses the write: tw up stops with the
	// lines to paste, before minting.
	so, se, _, err := e.Tw(append(slices.Clone(up), "--apply-policy")...)
	if err == nil || !strings.Contains(se, "policy_apply_failed") || !strings.Contains(se, `"tag:tw-mgmt": ["tag:tw"]`) {
		t.Fatalf("tw up --apply-policy --yes, write refused: %v\n%s\n%s", err, so, se)
	}
	if minted, posts := counts(); minted != 0 || posts != 1 {
		t.Fatalf("tw up --apply-policy --yes, write refused: %d keys minted, %d policy writes (want 0 and 1)", minted, posts)
	}
}
