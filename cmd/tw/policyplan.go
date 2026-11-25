package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw policy [dir] (issue #73): everything an app needs in the tailnet
// policy, before it is deployed, as one snippet. It bundles the app and
// runs the same dry-run preflight as tw deploy (POST
// /v1/deployments?dryRun=true; nothing is created), then prints what is
// missing: tagOwners for its tags, grants for tailnet.connects, the
// Tailscale Service auto-approval and the public-ingress nodeAttrs. It adds
// the grant that lets people call the app, which tailworks can't check (it
// doesn't know who you want to allow). It exits with acl_denied when a
// checked part is missing, so CI can run it before a deploy.

type policyResult struct {
	Workload    string               `json:"workload"`
	Tenant      string               `json:"tenant"`
	TenantID    string               `json:"tenantID"`
	OK          bool                 `json:"ok"`
	Checked     bool                 `json:"checked"`
	Preflight   mgmt.PreflightResult `json:"preflight"`
	CallerGrant string               `json:"callerGrant,omitempty"`
	Snippet     string               `json:"snippet,omitempty"`

	// ServiceCallerGrant: for a workload behind a Tailscale Service, the
	// grant to its svc: name (callers' app caps go on this one, #155).
	ServiceCallerGrant string `json:"serviceCallerGrant,omitempty"`

	// Applied: --apply's result (policyapply.go).
	Applied *mgmt.PolicyApplyResult `json:"applied,omitempty"`

	// Nodes: the tailnet nodes the app costs and the tenant's live count
	// (nodebudget.go).
	Nodes *nodeBudget `json:"nodes,omitempty"`
}

func cmdPolicy(args []string) error {
	f := newFlags("policy")
	name := f.String("name", "", "workload name (default: tailworks.jsonc name, then wrangler name, then the directory)")
	tenantF := tenantFlag(f)
	vars := varFlags{}
	f.Var(vars, "var", "override a wrangler var (KEY=VALUE, repeatable)")
	ao := addAppFlags(f) // wranglerapp.go: --env, --wrangler, --from-wrangler-out
	apply := f.Bool("apply", false, "write the missing tagOwners, grants, funnel nodeAttrs and Service auto-approval to the tailnet policy (additive, tag:tw* only, If-Match; needs Policy File write)")
	yes := yesFlagBefore(f, "--apply writes the policy")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return fail("bad_request", "tw policy takes at most one app directory", "usage: tw "+usageOf("policy"))
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	a, err := loadAppWith(dir, *name, *ao)
	if err != nil {
		return err
	}
	tb, _, err := a.build(vars)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res := policyResult{Workload: a.Name, Tenant: tn.Tailnet, TenantID: tn.TenantID, Checked: true, CallerGrant: callerGrant(a.Cfg.Tailnet.Tags, a.Cfg.Tailnet.HTTPS)}
	err = s.mgmt().Do(ctx, "POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": tn.TenantID, "workload": a.Name, "bundle": tb}, &res.Preflight)
	if me, ok := mgmtapi.AsError(err); ok && me.Code == "policy_unavailable" {
		// mgmt can't read the policy (an OAuth client without Policy File ›
		// Read): nothing is checked; the caller grant is still worth showing.
		res.Checked, err = false, nil
	}
	if err != nil {
		return err
	}
	res.OK = res.Checked && res.Preflight.OK
	res.ServiceCallerGrant = serviceCallerGrant(res.Preflight.Service)
	res.Snippet = policySnippet(res.Preflight, callerGrantEntry(a.Cfg.Tailnet.Tags, a.Cfg.Tailnet.HTTPS), serviceCallerGrantEntry(res.Preflight.Service))
	res.Nodes = s.nodeBudgetOf(ctx, tn, a.Cfg, false) // nodebudget.go
	if *apply {
		if err := policyApplyStep(ctx, s, f, a, tn, tb, *yes, &res); err != nil {
			return err
		}
	}
	if *f.json {
		if res.Checked && !res.OK {
			return policyFailure(res, true)
		}
		emit(f, res, func() {})
		return nil
	}
	printPolicyPlan(a, res)
	if res.Checked && !res.OK {
		return policyFailure(res, false) // the snippet is printed above
	}
	return nil
}

// policySnippet is one block to paste into the tailnet policy
// (policysnippet.go): what the preflight found missing, then the caller
// grants (nil: none), each grant with a comment saying why.
func policySnippet(pf mgmt.PreflightResult, caller, svcCaller map[string]any) string {
	s := preflightPolicy(pf)
	s.Grants = slices.Clone(s.Grants)
	notes := make([]string, len(s.Grants))
	for i, g := range s.Grants {
		notes[i] = "tailnet.connects"
		for _, d := range entryStrs(g, "dst") {
			if !policy.IsTwTag(d) && strings.HasPrefix(d, "tag:") {
				notes[i] += " (" + d + " must already be in tagOwners)"
			}
		}
	}
	if caller != nil {
		s.Grants = append(s.Grants, caller)
		notes = append(notes, "who may call it (not checked: narrow src to a user, group or tag)")
	}
	if svcCaller != nil {
		s.Grants = append(s.Grants, svcCaller)
		notes = append(notes, "calls through "+pf.Service+": callers' app caps go on this grant")
	}
	return s.Paste(notes)
}

// printPolicyHeader is the report's first lines: what is checked, and the
// app's tags (an object class with its own nodes has a tag of its own).
func printPolicyHeader(a *app, res policyResult) {
	fmt.Printf("tw policy: what %s needs in tenant %s's tailnet policy (nothing is deployed)\n", a.Name, res.Tenant)
	if d := describeTags(a); d != "" {
		fmt.Printf("  tags       %s\n", d)
	}
}

// describeTags names what carries each of the app's tags, when it has more
// than its workload node's ("" otherwise).
func describeTags(a *app) string {
	cls := a.Cfg.Classes()
	if len(cls) < 2 {
		return ""
	}
	var parts []string
	for _, c := range cls {
		what := "the " + c.Class + " objects' nodes"
		if c.Class == mgmt.WorkloadNamespace {
			what = "the workload node"
		}
		for _, t := range c.Tags {
			parts = append(parts, t+" ("+what+")")
		}
	}
	return strings.Join(parts, ", ")
}

func printPolicyPlan(a *app, res policyResult) {
	pf := res.Preflight
	applied := res.Applied != nil // policyApplyStep printed the header and checks
	if !applied {
		printPolicyHeader(a, res)
		if !res.Checked {
			fmt.Printf("? checked    no: mgmt can't read the tailnet policy (give the OAuth client the Policy File › Read scope)\n")
			fmt.Printf("  tags       %s (each needs a tagOwners entry that tag:tw may use)\n", strings.Join(a.srcTags(), ", "))
		} else {
			printPolicyChecks(a, pf)
		}
	}
	printNodeBudget("  nodes      ", "! nodes      ", res.Tenant, res.Nodes) // nodebudget.go
	if res.Snippet == "" {
		if res.OK && applied && res.Applied.CallerGrant != "" {
			fmt.Printf("\nThe policy now has everything tailworks checks, and every user in your tailnet may call %s; run tw deploy.\n", a.Name)
		}
		return
	}
	switch {
	case res.OK && applied:
		fmt.Printf("\nThe policy now has everything tailworks checks; run tw deploy. If people should call %s, make sure a grant like this exists (tailworks didn't write it):\n\n", a.Name)
	case res.OK:
		fmt.Printf("\nThe policy has everything tailworks checks. If people should call %s, make sure a grant like this exists:\n\n", a.Name)
	case applied:
		fmt.Printf("\nStill missing. Add to your tailnet policy (admin console › Access controls), then run tw deploy:\n\n")
	default:
		fmt.Printf("\nAdd to your tailnet policy (admin console › Access controls), then run tw deploy (or let tailworks add its part: tw policy --apply):\n\n")
	}
	for _, l := range strings.Split(res.Snippet, "\n") {
		fmt.Fprintf(os.Stdout, "  %s\n", l)
	}
}

// printPolicyChecks is one line per check; the snippet comes after, once.
func printPolicyChecks(a *app, pf mgmt.PreflightResult) {
	if len(pf.MissingTagOwners) > 0 {
		fmt.Printf("✗ tagOwners  %s: missing (minting its nodes would fail)\n", strings.Join(pf.MissingTagOwners, ", "))
	} else {
		fmt.Printf("✓ tagOwners  %s\n", strings.Join(a.srcTags(), ", "))
	}
	for _, t := range pf.UndefinedConnects {
		fmt.Printf("✗ tagOwners  %s: missing (no app with this tag is deployed yet; tailworks can define it)\n", t)
	}
	missing := map[string]bool{}
	for _, g := range pf.MissingGrants() {
		missing[g.Dst] = true
		fmt.Printf("✗ connects   %s → %s %s: no grant\n", strings.Join(g.Srcs, ", "), g.Dst, g.IP)
	}
	for _, dst := range a.Cfg.Tailnet.Connects {
		switch {
		case slices.Contains(pf.Unchecked, dst):
			fmt.Printf("? connects   → %s: a name, not a tag (not checked; name it by tag in tailnet.connects)\n", dst)
		case !missing[connectTarget(dst)]:
			fmt.Printf("✓ connects   → %s\n", dst)
		}
	}
	if pf.Service != "" {
		if pf.ServiceUnapproved {
			fmt.Printf("✗ service    %s: not in autoApprovers.services for the workload tags\n", pf.Service)
		} else {
			fmt.Printf("✓ service    %s auto-approved\n", pf.Service)
		}
	}
	if pf.PublicURL != "" {
		if len(pf.MissingNodeAttrs) > 0 {
			fmt.Printf("✗ public     %s: missing\n", strings.Join(pf.MissingNodeAttrs, ", "))
		} else {
			fmt.Printf("✓ public     funnel allowed on %s\n", policy.PublicTag)
		}
	}
	fmt.Printf("? callers    not checked: tailworks doesn't know who you want to allow to call %s\n", a.Name)
	for _, w := range pf.AccessWarnings {
		fmt.Printf("! access     %s\n", w)
	}
}

// policyFailure is tw policy's error when a checked part is missing:
// deploy's preflight failure code, with the combined snippet as its fix
// (withSnippet; on a terminal it was printed just above).
func policyFailure(res policyResult, withSnippet bool) *cliErr {
	e := preflightFailure(res.Preflight) // public.go: acl_denied or funnel_required
	e.Message = strings.Replace(e.Message, "preflight failed", "tw policy", 1)
	if !withSnippet {
		e.Fix = []string{"add the snippet above to your tailnet policy, then run tw deploy"}
		return e
	}
	e.Fix = []string{"add to your tailnet policy (admin console › Access controls):"}
	for _, l := range strings.Split(res.Snippet, "\n") {
		e.Fix = append(e.Fix, "  "+l)
	}
	e.Fix = append(e.Fix, "then run tw policy again, or tw deploy")
	return e
}

// connectTarget is a tailnet.connects entry without its port.
func connectTarget(c string) string {
	t, _ := mgmt.SplitConnect(c)
	return t
}
