package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// The in-process tests never see a terminal, whatever runs go test: tw
// deploy and tw up ask by default on one (issue #481). Tests of the prompt
// set confirmIsTerminal themselves.
func init() { confirmIsTerminal = func() bool { return false } }

// policyAskTerminal makes stdin a terminal that answers input.
func policyAskTerminal(t *testing.T, terminal bool, input string) {
	t.Helper()
	oldT, oldIn := confirmIsTerminal, confirmIn
	t.Cleanup(func() { confirmIsTerminal, confirmIn = oldT, oldIn })
	confirmIsTerminal = func() bool { return terminal }
	confirmIn = strings.NewReader(input)
}

// TestPolicyModeOf (issue #481): ask by default only on a terminal without
// --json or --yes; --apply-policy=false never asks; --apply-policy always
// goes through the flag's confirmation.
func TestPolicyModeOf(t *testing.T) {
	for _, c := range []struct {
		terminal bool
		args     []string
		want     policyWriteMode
	}{
		{true, nil, policyAskFirst},
		{false, nil, policyPrintOnly},
		{true, []string{"--yes"}, policyPrintOnly},
		{true, []string{"--json"}, policyPrintOnly},
		{false, []string{"--json", "--yes"}, policyPrintOnly},
		{true, []string{"--apply-policy=false"}, policyPrintOnly},
		{true, []string{"--apply-policy"}, policyApplyFlag},
		{false, []string{"--apply-policy", "--yes"}, policyApplyFlag},
		{false, []string{"--apply-policy"}, policyApplyFlag},
	} {
		policyAskTerminal(t, c.terminal, "")
		f := newFlags("up")
		apply := f.Bool("apply-policy", false, "")
		yes := f.Bool("yes", false, "")
		if _, err := f.parse(c.args); err != nil {
			t.Fatal(err)
		}
		if got := policyModeOf(f, *apply, *yes); got != c.want {
			t.Errorf("terminal=%v %v: mode %d, want %d", c.terminal, c.args, got, c.want)
		}
	}
}

// askUp is tw up's Tailscale policy step on a terminal without the flag.
func (e *upApplyEnv) askUp(t *testing.T, input string, readable bool) (applied bool, out string, err error) {
	t.Helper()
	policyAskTerminal(t, true, input)
	ctx := context.Background()
	before, _, gerr := e.c.GetPolicyHuJSON(ctx)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if !readable {
		before = nil
	}
	needed := policy.Snippet{TagOwners: map[string][]string{mgmt.MgmtTag: {"tag:tw"}}}
	var b bytes.Buffer
	_, applied, err = upApplyPolicy(ctx, e.c, newFlags("up"), before, readable, needed, policyAskFirst, false, &b)
	return applied, b.String(), err
}

// TestUpPolicyAskFirst (issue #481): plain tw up on a terminal shows the
// bootstrap lines and asks. "y" writes exactly them; "n" goes on as
// print-only tw up (the tag:tw-mgmt hard stop stays); a client that can't
// write is never asked.
func TestUpPolicyAskFirst(t *testing.T) {
	e := newUpApplyEnv(t, freshPolicy)
	applied, out, err := e.askUp(t, "y\n", true)
	if err != nil || !applied || !strings.Contains(out, "--apply-policy=false never asks") {
		t.Fatalf("y: applied=%v err=%v\n%s", applied, err, out)
	}
	pol, writes, _ := e.now()
	if writes != 1 {
		t.Fatalf("y: %d writes", writes)
	}
	operatorMissing(t, pol)

	e = newUpApplyEnv(t, freshPolicy)
	applied, out, err = e.askUp(t, "n\n", true)
	if cliCode(err) != "policy_missing_tag_owners" || applied || !strings.Contains(out, "tailworks will add") {
		t.Fatalf("n, tag:tw-mgmt unowned: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, writes, posts := e.now(); writes != 0 || posts != 0 {
		t.Fatalf("n: %d POST /acl", posts)
	}

	// tag:tw-mgmt owned, grants missing: "n" just goes on.
	owned := strings.Replace(freshPolicy, `"tag:tw": ["autogroup:admin"],`, `"tag:tw": ["autogroup:admin"], "tag:tw-mgmt": ["tag:tw"],`, 1)
	e = newUpApplyEnv(t, owned)
	if applied, out, err = e.askUp(t, "n\n", true); err != nil || applied {
		t.Fatalf("n, grants missing: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, _, posts := e.now(); posts != 0 {
		t.Fatalf("n: %d POST /acl", posts)
	}

	// Policy File › Read only: the control plane refuses the validate, so
	// tw doesn't ask.
	e = newUpApplyEnv(t, owned)
	e.api.denyValidate = true
	if applied, out, err = e.askUp(t, "y\n", true); err != nil || applied || strings.Contains(out, "tailworks will add") {
		t.Fatalf("read-only client: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, _, posts := e.now(); posts != 0 {
		t.Fatalf("read-only client: %d POST /acl", posts)
	}

	// Can't read the policy: nothing to ask about.
	e = newUpApplyEnv(t, freshPolicy)
	if applied, out, err = e.askUp(t, "y\n", false); err != nil || applied || out != "" {
		t.Fatalf("unreadable: applied=%v err=%v\n%s", applied, err, out)
	}
}

// TestHeadscaleUpPolicyAskFirst: on Headscale "n" keeps the hard stop
// (Headscale refuses a node with undefined tags); "y" writes.
func TestHeadscaleUpPolicyAskFirst(t *testing.T) {
	const pol = `{"tagOwners": {}}`
	e := newUpApplyEnv(t, pol)
	run := func(input string) (bool, string, error) {
		policyAskTerminal(t, true, input)
		var b bytes.Buffer
		_, applied, err := headscaleUpPolicy(context.Background(), e.c, newFlags("up"), []byte(pol), policyAskFirst, false, &b)
		return applied, b.String(), err
	}
	if applied, out, err := run("n\n"); cliCode(err) != "policy_missing_tag_owners" || applied {
		t.Fatalf("n: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, _, posts := e.now(); posts != 0 {
		t.Fatalf("n: %d POST /acl", posts)
	}
	if applied, out, err := run("y\n"); err != nil || !applied {
		t.Fatalf("y: applied=%v err=%v\n%s", applied, err, out)
	}
	// Print-only (no terminal, --yes alone, --apply-policy=false): the
	// hard stop, no prompt, no write.
	e = newUpApplyEnv(t, pol)
	var b bytes.Buffer
	if _, applied, err := headscaleUpPolicy(context.Background(), e.c, newFlags("up"), []byte(pol), policyPrintOnly, true, &b); cliCode(err) != "policy_missing_tag_owners" || applied || b.Len() != 0 {
		t.Fatalf("print-only: applied=%v err=%v\n%s", applied, err, b.String())
	}
}

// TestDeployPolicyAskFirst (issue #481): plain tw deploy on a terminal
// shows the lines mgmt would add and asks; "n" and a client that can't
// write fall back to acl_denied's snippet; --yes alone and
// --apply-policy=false never ask or write.
func TestDeployPolicyAskFirst(t *testing.T) {
	e := newPubEnv(t)
	app := writeSupportApp(t)
	e.setPolicy(operatorPolicy)

	policyAskTerminal(t, true, "n\n")
	out, err := e.call(t, cmdDeploy, app, "--no-wait")
	if cliCode(err) != "acl_denied" || strings.Contains(cliText(err), letTwAddFix) || !strings.Contains(out, "tailworks will add") || !strings.Contains(out, "not applied: cancelled") {
		t.Fatalf("n: %v\n%s\n%s", err, cliText(err), out)
	}
	for _, args := range [][]string{{"--yes"}, {"--apply-policy=false"}, {"--json"}} {
		policyAskTerminal(t, true, "y\n")
		out, err := e.call(t, cmdDeploy, append([]string{app, "--no-wait"}, args...)...)
		if cliCode(err) != "acl_denied" || strings.Contains(out, "tailworks will add") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if _, n := e.policyNow(); n != 0 {
		t.Fatalf("%d policy writes before any y", n)
	}

	e.denyWrites(true)
	policyAskTerminal(t, true, "y\n")
	out, err = e.call(t, cmdDeploy, app, "--no-wait")
	if cliCode(err) != "acl_denied" || strings.Contains(out, "tailworks will add") || !strings.Contains(out, "not applied: policy_write_denied") || !strings.Contains(cliText(err), "Policy File › Write") {
		t.Fatalf("read-only client: %v\n%s\n%s", err, cliText(err), out)
	}
	if _, n := e.policyNow(); n != 0 {
		t.Fatalf("read-only client: %d policy writes", n)
	}

	e.denyWrites(false)
	policyAskTerminal(t, true, "y\n")
	out, err = e.call(t, cmdDeploy, app, "--no-wait")
	if err != nil || !strings.Contains(out, "✓ policy     applied") {
		t.Fatalf("y: %v\n%s\n%s", err, cliText(err), out)
	}
	if _, n := e.policyNow(); n != 1 {
		t.Fatalf("y: %d policy writes", n)
	}
}

// TestTenantPolicyAskFirst (issues #481, #475): on a terminal without the
// flag, tw tenant add asks before writing a Headscale tenant's owner tag
// ("n" leaves it to paste); a Tailscale tenant's policy is never written
// and never asked about.
func TestTenantPolicyAskFirst(t *testing.T) {
	const pol = `{"tagOwners": {"tag:db": []}}`
	e := newUpApplyEnv(t, pol)
	policyAskTerminal(t, true, "n\n")
	var res tenantAddResult
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(pol), true, controlHeadscale, "tag:tw", false, false); err != nil || res.Applied || res.PolicyOK {
		t.Fatalf("headscale, n: %v %+v", err, res)
	}
	if _, _, posts := e.now(); posts != 0 {
		t.Fatalf("headscale, n: %d POST /acl", posts)
	}
	policyAskTerminal(t, true, "y\n")
	res = tenantAddResult{}
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(pol), true, controlHeadscale, "tag:tw", false, false); err != nil || !res.Applied {
		t.Fatalf("headscale, y: %v %+v", err, res)
	}

	e = newUpApplyEnv(t, pol)
	policyAskTerminal(t, true, "y\n")
	res = tenantAddResult{}
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(pol), true, controlTailscale, "tag:tw", false, false); err != nil || res.Applied {
		t.Fatalf("tailscale: %v %+v", err, res)
	}
	if _, _, posts := e.now(); posts != 0 {
		t.Fatalf("tailscale tenant: %d POST /acl", posts)
	}
}
