package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw deploy's policy step when tw could not write the lines (issue #481):
// the deploy never loses the paste path. tw says why in one line and fails
// with the preflight's acl_denied (or funnel_required) snippet, as without
// --apply-policy.

// letTwAddFix is preflightFailure's last fix line (public.go).
const letTwAddFix = "or let tw add them (shows the lines and asks first): tw deploy --apply-policy"

// policyWriteDenied: the tenant's OAuth client may not write the policy.
func policyWriteDenied(err error) bool {
	if err == nil {
		return false
	}
	ce := asCLIErr(err)
	return ce != nil && ce.Code == "policy_write_denied"
}

// deployPolicyFallback is tw deploy's error when the policy write didn't
// happen (cause): the preflight's snippet to paste, without the hint to let
// tw write it, and what would let tw write it next time.
func deployPolicyFallback(f *flags, pf mgmt.PreflightResult, cause error) *cliErr {
	if !*f.json {
		fmt.Printf("› policy     not applied: %s; add the lines yourself:\n", policyCauseLine(cause))
	}
	e := preflightFailure(pf) // public.go
	if n := len(e.Fix); n > 0 && e.Fix[n-1] == letTwAddFix {
		e.Fix = e.Fix[:n-1]
	}
	if policyWriteDenied(cause) {
		e.Fix = append(e.Fix, "to let tw write them: give the tenant a new OAuth client with Policy File › Write (scopes can't be added later; tw tenant set --oauth-client-file)")
	}
	return e
}

// policyCauseLine is cause as one line for the terminal.
func policyCauseLine(cause error) string {
	if me, ok := mgmtapi.AsError(cause); ok {
		return me.Code + ": " + tsafe(firstLine(me.Message))
	}
	if ce := asCLIErr(cause); ce != nil {
		return ce.Code + ": " + tsafe(firstLine(ce.Message))
	}
	return tsafe(firstLine(strings.TrimSpace(cause.Error())))
}

// deployApplyPolicy is tw deploy --apply-policy's step: plan, ask, write
// (policyapply.go), then preflight again (*pf, res updated). A client that
// may not write the policy falls back to the snippet (acl_denied), or with
// --allow-missing-grants to deploying without the lines.
func deployApplyPolicy(ctx context.Context, mc *mgmtapi.Client, f *flags, tenantID, workload string, tb []byte, yes, allowMissing bool, pf *mgmt.PreflightResult, res *deployResult, ev *events) error {
	ar, err := applyAppPolicy(ctx, mc, f, tenantID, workload, tb, yes, "tw deploy --apply-policy")
	switch {
	case policyWriteDenied(err) && allowMissing:
		if !*f.json {
			fmt.Printf("› policy     not applied: %s; deploying without the lines (--allow-missing-grants)\n", policyCauseLine(err))
		}
		ev.emit("policy", map[string]any{"changed": false, "error": "policy_write_denied", "ok": false})
		return nil
	case policyWriteDenied(err):
		return deployPolicyFallback(f, *pf, err)
	case err != nil:
		return err
	}
	*pf, res.Preflight, res.PolicyApplied = ar.Preflight, ar.Preflight, ar
	ev.emit("policy", map[string]any{"changed": ar.Changed, "added": ar.Added, "manual": ar.Manual, "ok": pf.OK})
	return nil
}

// deployAskPolicy is tw deploy's default on a terminal (issue #481): the
// preflight found lines missing, so when mgmt says the write would go
// through (canWrite), show them and ask. "y" writes as --apply-policy
// does. "n", a plan mgmt refuses (a tenant admin: tenant_forbidden), a
// write it can't make, or a failed write falls back to the snippet
// (acl_denied), as without the flag.
func deployAskPolicy(ctx context.Context, mc *mgmtapi.Client, f *flags, tenantID, workload string, tb []byte, pf *mgmt.PreflightResult, res *deployResult, ev *events) error {
	plan, err := planAppPolicy(ctx, mc, tenantID, workload, tb) // policyapply.go
	switch {
	case err != nil:
		return deployPolicyFallback(f, *pf, err)
	case strings.TrimSpace(plan.Added) == "" || plan.CanWrite == nil:
		return preflightFailure(*pf) // nothing tw writes, or a mgmt that doesn't say
	case !*plan.CanWrite:
		return deployPolicyFallback(f, *pf, fail("policy_write_denied", "the control plane would refuse the write (the tenant's OAuth client needs Policy File › Write)"))
	}
	printPolicyAdditions(*plan)
	policyAskHint(os.Stdout, "tw deploy") // policy_ask.go
	if err := confirmPolicyWrite(f, false, "tw deploy --apply-policy"); err != nil {
		if policyDeclined(err) {
			return deployPolicyFallback(f, *pf, fail("cancelled", "you answered no"))
		}
		return err
	}
	ar, err := writeAppPolicy(ctx, mc, f, tenantID, workload, tb, plan)
	if err != nil {
		return deployPolicyFallback(f, *pf, err)
	}
	*pf, res.Preflight, res.PolicyApplied = ar.Preflight, ar.Preflight, ar
	ev.emit("policy", map[string]any{"changed": ar.Changed, "added": ar.Added, "manual": ar.Manual, "ok": pf.OK})
	return nil
}
