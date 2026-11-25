package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw policy --apply and tw deploy --apply-policy (issue #116): instead of
// pasting the snippet into the admin console, mgmt writes it. mgmt plans
// (POST .../policy?dryRun=true), tw prints the lines it will add, asks (or
// takes --yes), then mgmt merges additively (only tag:tw* entries, nothing
// removed; RT-06), validates and writes with If-Match, retrying once on a
// conflict. It needs the OAuth client's Policy File write scope; without
// it, the printed snippet (tw policy) is the fallback.

// confirmPolicyWrite asks before tw writes the tailnet policy: --yes goes
// ahead; a terminal without --json is asked y/N; otherwise it refuses with
// confirmation_required (confirm.go's rule, for a write instead of a delete).
func confirmPolicyWrite(f *flags, yes bool, cmdline string) error {
	if yes {
		return nil
	}
	if *f.json || !confirmIsTerminal() {
		return fail("confirmation_required", "writing the tailnet policy needs confirmation; tw printed the lines it would add").
			withFix(cmdline+" --yes", "or add the snippet yourself (tw policy prints it)")
	}
	fmt.Fprintf(os.Stderr, "Add these lines to the tailnet policy? [y/N] ")
	ln, _ := bufio.NewReader(confirmIn).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(ln)); a != "y" && a != "yes" {
		return fail("cancelled", "the tailnet policy was not changed")
	}
	return nil
}

// applyAppPolicy plans, confirms and applies the policy additions the
// app's bundle needs. It returns the result (Changed false when there was
// nothing tailworks could write).
func applyAppPolicy(ctx context.Context, mc *mgmtapi.Client, f *flags, tenantID, workload string, tb []byte, yes bool, cmdline string) (*mgmt.PolicyApplyResult, error) {
	plan, err := planAppPolicy(ctx, mc, tenantID, workload, tb)
	if err != nil {
		return nil, err
	}
	if !*f.json {
		printPolicyAdditions(*plan)
	}
	if strings.TrimSpace(plan.Added) == "" {
		return plan, nil
	}
	if err := confirmPolicyWrite(f, yes, cmdline); err != nil {
		return plan, err
	}
	return writeAppPolicy(ctx, mc, f, tenantID, workload, tb, plan)
}

// appPolicyPath is the workload's policy endpoint.
func appPolicyPath(tenantID, workload string) string {
	return "/v1/tenants/" + url.PathEscape(tenantID) + "/workloads/" + url.PathEscape(workload) + "/policy"
}

// planAppPolicy asks mgmt what it would add (dryRun).
func planAppPolicy(ctx context.Context, mc *mgmtapi.Client, tenantID, workload string, tb []byte) (*mgmt.PolicyApplyResult, error) {
	var plan mgmt.PolicyApplyResult
	if err := mc.Do(ctx, "POST", appPolicyPath(tenantID, workload)+"?dryRun=true", map[string]any{"bundle": tb}, &plan); err != nil {
		return nil, policyApplyErr(err)
	}
	return &plan, nil
}

// writeAppPolicy writes what plan showed (after the confirmation).
func writeAppPolicy(ctx context.Context, mc *mgmtapi.Client, f *flags, tenantID, workload string, tb []byte, plan *mgmt.PolicyApplyResult) (*mgmt.PolicyApplyResult, error) {
	var res mgmt.PolicyApplyResult
	if err := mc.Do(ctx, "POST", appPolicyPath(tenantID, workload), map[string]any{"bundle": tb}, &res); err != nil {
		return plan, policyApplyErr(err)
	}
	if !*f.json {
		if res.Changed {
			fmt.Printf("✓ policy     applied (validated by the control plane; tw audit --all shows it as policy_apply)\n")
		} else {
			fmt.Printf("✓ policy     nothing to write: the policy already has it\n")
		}
	}
	return &res, nil
}

// policyApplyStep is tw policy --apply after the check: when something is
// missing, show the check, apply, and re-check (res is updated in place).
func policyApplyStep(ctx context.Context, s *state, f *flags, a *app, tn TenantRef, tb []byte, yes bool, res *policyResult) error {
	if !res.Checked {
		return fail("policy_unavailable", "tw policy --apply: mgmt can't read the tailnet policy, so it can't add to it").
			withFix("give the tenant's OAuth client Policy File read and write, or add the app's tagOwners and grants yourself")
	}
	if res.OK {
		return nil
	}
	if !*f.json {
		printPolicyHeader(a, *res)
		printPolicyChecks(a, res.Preflight)
		fmt.Println()
	}
	ar, err := applyAppPolicy(ctx, s.mgmt(), f, tn.TenantID, a.Name, tb, yes, "tw policy --apply")
	if err != nil {
		return err
	}
	res.Applied, res.Preflight = ar, ar.Preflight
	res.OK = res.Preflight.OK
	caller := callerGrantEntry(a.Cfg.Tailnet.Tags, a.Cfg.Tailnet.HTTPS)
	if ar.Changed && ar.CallerGrant != "" {
		res.CallerGrant, caller = "", nil // written: not in what is left to paste
	}
	res.ServiceCallerGrant = serviceCallerGrant(res.Preflight.Service)
	res.Snippet = policySnippet(res.Preflight, caller, serviceCallerGrantEntry(res.Preflight.Service))
	return nil
}

func printPolicyAdditions(plan mgmt.PolicyApplyResult) {
	if strings.TrimSpace(plan.Added) == "" {
		fmt.Printf("› policy     nothing tailworks writes is missing\n")
	} else {
		only := "only tag:tw* entries"
		if plan.CallerGrant != "" {
			only += " and the grant that lets your tailnet's users call the app"
		}
		fmt.Printf("› policy     tailworks will add to the tailnet policy (additive: %s, nothing removed or changed):\n", only)
		for _, l := range strings.Split(strings.TrimRight(plan.Added, "\n"), "\n") {
			fmt.Printf("    + %s\n", l)
		}
	}
	if plan.CallerGrant != "" {
		fmt.Printf("› policy     the autogroup:member grant lets every user in your tailnet call %s (tailworks adds it with a new app's tag; to choose the callers yourself, say no and add your own grant first: tailworks then writes none)\n", plan.Workload)
	}
	if len(plan.Manual) > 0 {
		fmt.Printf("› policy     tailworks won't write these (they stay in the snippet for you to add):\n")
		for _, m := range plan.Manual {
			fmt.Printf("    - %s\n", m)
		}
	}
}

// policyApplyErr adds the print-mode fallback to mgmt's refusals.
func policyApplyErr(err error) error {
	me, ok := mgmtapi.AsError(err)
	if !ok {
		return err
	}
	e := fail(me.Code, me.Message)
	e.TraceID = me.TraceID
	switch me.Code {
	case "policy_write_denied":
		e.Fix = []string{"give the tenant's OAuth client the Policy File write scope (a new client: scopes can't be added later; tw tenant set --oauth-client-file)",
			"or add the snippet yourself: tw policy prints it"}
	case "policy_conflict":
		e.Fix = []string{"someone else is editing the policy: run it again"}
	case "policy_unavailable":
		e.Fix = []string{"give the OAuth client Policy File › Read (and write, to apply), or add the snippet tw policy prints"}
	case "policy_apply_failed":
		e.Fix = []string{"nothing was written; add the snippet yourself (tw policy prints it) and fix what the control plane reported"}
	default:
		return err
	}
	return e
}
