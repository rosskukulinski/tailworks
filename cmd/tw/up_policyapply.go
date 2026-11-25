package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/tailscale/hujson"
)

// tw up --apply-policy on Tailscale (issue #320): instead of pasting the
// bootstrap snippet (README quick start, step 2) into the admin console,
// tw up writes it with the OAuth client, before anything is minted. It is
// the same write as tw policy --apply (policyapply.go): print the lines,
// ask (or take --yes), merge additively, validate, write with If-Match and
// retry once on a conflict. mgmt isn't running yet, so tw does it itself
// through internal/policy.
//
// It needs the client's Policy File write scope. Without it nothing is
// written: tw up says why and goes on as it does without the flag (the
// snippet is printed for pasting).
//
// One line can't be written this way: tag:tw must already be in tagOwners,
// because the admin console only offers existing tags when the OAuth
// client is created.

// confirmBootstrapWrite is the one rule wherever tw writes its own
// bootstrap snippet (tw up --apply-policy on Tailscale and on Headscale,
// tw tenant add --apply-policy): show the lines as a diff, then ask;
// --yes skips the question and is required without a terminal or with
// --json (confirmation_required). A refusal is cancelled. Nothing has been
// written or minted when it returns an error. out nil: stdout, or stderr
// with --json.
func confirmBootstrapWrite(f *flags, yes bool, cmdline, profile string, miss policy.Snippet, out io.Writer) error {
	if out == nil {
		out = os.Stdout
		if *f.json {
			out = os.Stderr
		}
	}
	fmt.Fprintf(out, "› policy   tailworks will add to the tailnet policy (additive: only tailworks' own tags and grants, nothing removed or changed):\n")
	for _, l := range miss.Diff() {
		fmt.Fprintf(out, "    %s\n", l)
	}
	err := confirmPolicyWrite(f, yes, cmdline)
	if err == nil {
		return nil
	}
	if ce := asCLIErr(err); ce != nil && ce.Code == "confirmation_required" {
		ce.Fix = append([]string{"run it again with --yes (" + cmdline + " --yes)", "or add them yourself in " + policyHome(profile) + ", then run it again:"}, snippetLines(miss)...)
	}
	return err
}

// upPolicyOptions lets the bootstrap snippet's admin grant through Check:
// it is the one grant tailworks writes whose source is not a tag:tw* tag.
func upPolicyOptions() policy.Options {
	return policy.Options{AllowSrcs: []string{"autogroup:admin"}}
}

// operatorSnippetFor is operatorSnippet for the tailnet policy pol (issue
// #389). When pol already allows all traffic (a new tailnet's default
// allow-all grant or ACL), the host ⇄ mgmt network grants add nothing, so
// only the tagOwners and the admin grant (the console's app capability)
// are wanted. A policy that narrows the allow-all later needs the network
// grants back: tw up reports them as missing then.
func operatorSnippetFor(pol []byte) policy.Snippet {
	s := operatorSnippet()
	if policy.AllowsAll(pol) {
		s.Grants = slices.DeleteFunc(s.Grants, func(g map[string]any) bool { return g["app"] == nil })
	}
	return s
}

// upPolicyWant is what tw up --apply-policy adds to pol: operatorSnippet,
// narrowed so that an existing tag's owners are never widened. A tag:tw
// that is already there keeps the owners the admin chose (a group, say),
// and an existing tag:tw-mgmt or tag:tw-host only gains tag:tw.
func upPolicyWant(pol []byte) (policy.Snippet, error) {
	want := operatorSnippetFor(pol)
	std, err := hujson.Standardize(append([]byte(nil), pol...))
	if err != nil {
		return want, fmt.Errorf("policy: %w", err)
	}
	var have struct {
		TagOwners map[string]json.RawMessage `json:"tagOwners"`
	}
	if len(strings.TrimSpace(string(std))) > 0 {
		if err := json.Unmarshal(std, &have); err != nil {
			return want, fmt.Errorf("policy: %w", err)
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(want.TagOwners)) {
		if _, ok := have.TagOwners[tag]; !ok {
			continue
		}
		if tag == policy.TwTagPrefix {
			delete(want.TagOwners, tag)
		} else {
			want.TagOwners[tag] = []string{policy.TwTagPrefix}
		}
	}
	return want, nil
}

// upApplyPolicy is the --apply-policy step of tw up in oauth mode. pol is
// the policy oauthPreflight read (readable false: the client can't read
// it), needed the tag ownership this layout can't start without. It
// returns the policy as it is afterwards and whether tw wrote it.
//
// Errors stop tw up before anything is minted: the prompt was declined
// (cancelled), there is no terminal and no --yes (confirmation_required),
// the policy kept changing (policy_conflict), the control plane refused
// the merged policy (policy_apply_failed), or the write scope is missing
// and the policy lacks needed (policy_missing_tag_owners, as without the
// flag).
//
// mode policyAskFirst (a terminal without the flag, issue #481) asks only
// when the control plane would accept the write, and says nothing when it
// wouldn't; "n", or a write that can't happen, goes on as print-only tw up
// does (needed still missing: policy_missing_tag_owners).
func upApplyPolicy(ctx context.Context, api policy.API, f *flags, pol []byte, readable bool, needed policy.Snippet, mode policyWriteMode, yes bool, out io.Writer) ([]byte, bool, error) {
	const cmdline = "tw up --apply-policy"
	ask := mode == policyAskFirst
	if ask {
		// What print-only tw up does instead (oauthPreflight's check).
		printOnly := func() ([]byte, bool, error) {
			return pol, false, oauthNeededCheck(pol, readable, needed) // oauth.go
		}
		if !readable {
			return printOnly()
		}
		want, err := upPolicyWant(pol)
		if err != nil {
			return printOnly()
		}
		miss, err := policy.Missing(pol, want)
		if err != nil || miss.Empty() {
			return printOnly()
		}
		next, err := policy.MergeWith(slices.Clone(pol), want, upPolicyOptions())
		if err != nil || api.ValidatePolicy(ctx, next) != nil {
			return printOnly()
		}
		policyAskHint(out, "tw up")
		if err := confirmBootstrapWrite(f, false, cmdline, controlTailscale, miss, out); err != nil {
			if policyDeclined(err) {
				return printOnly()
			}
			return pol, false, err
		}
	}
	if !readable {
		fmt.Fprintf(out, "› policy   not applied: --apply-policy needs the OAuth client's Policy File › Write scope, and this client can't even read the policy. Scopes can't be added to an existing client: create a new one with Policy File › Write, or make sure the policy has:\n")
		for _, l := range snippetLines(operatorSnippet()) {
			fmt.Fprintln(out, l)
		}
		return pol, false, nil
	}
	want, err := upPolicyWant(pol)
	if err != nil {
		return pol, false, fail("bad_request", "can't parse the tailnet policy", err.Error())
	}
	opts := upPolicyOptions()
	miss, err := policy.Missing(pol, want)
	if err != nil {
		return pol, false, fail("bad_request", "can't parse the tailnet policy", err.Error())
	}
	if miss.Empty() {
		fmt.Fprintf(out, "✓ policy   nothing to write: the tailnet policy already has tailworks' tagOwners and grants\n")
		return pol, false, nil
	}
	// denied: no write scope. Say so, then behave as without the flag.
	denied := func(cause error) ([]byte, bool, error) {
		fmt.Fprintf(out, "› policy   not applied: the OAuth client lacks the Policy File › Write scope (%s). Scopes can't be added to an existing client: create a new one with Policy File › Write, or paste the lines yourself\n", firstLine(netd.Redact(cause.Error())))
		tm, err := policy.Missing(pol, needed)
		if err != nil {
			return pol, false, fail("bad_request", "can't parse the tailnet policy", err.Error())
		}
		if !tm.Empty() {
			return pol, false, policyMissingTagOwners("--apply-policy could not write it (no Policy File › Write scope); the policy lacks tag:tw as an owner of: "+strings.Join(slices.Sorted(maps.Keys(tm.TagOwners)), ", "), nil, pol)
		}
		return pol, false, nil
	}
	failed := func(err error) ([]byte, bool, error) {
		return pol, false, fail("policy_apply_failed", "could not apply the policy snippet; nothing was written", netd.Redact(err.Error())).
			withFix(append([]string{"add to your tailnet policy (admin console › Access controls), then run tw up again (nothing was minted):"}, snippetLines(miss)...)...)
	}
	// Validate what would be written before asking: a client without the
	// write scope, or a policy the control plane would refuse, is found
	// out before the prompt.
	// (A copy: formatting the merged policy rewrites the parsed bytes.)
	next, err := policy.MergeWith(slices.Clone(pol), want, opts)
	if err != nil {
		return failed(err)
	}
	if err := api.ValidatePolicy(ctx, next); err != nil {
		if policy.IsForbidden(err) {
			return denied(err)
		}
		return failed(err)
	}
	if !ask {
		if err := confirmBootstrapWrite(f, yes, cmdline, controlTailscale, miss, out); err != nil {
			return pol, false, err
		}
	}
	ar, err := policy.ApplyRetry(ctx, api, want, opts)
	switch {
	case errors.Is(err, policy.ErrConflict):
		return pol, false, fail("policy_conflict", err.Error()).withFix("someone else is editing the policy: run " + cmdline + " again (nothing was minted)")
	case err != nil && policy.IsForbidden(err):
		return denied(err)
	case err != nil:
		return failed(err)
	}
	if !ar.Changed {
		// Someone added the lines between the read and the write.
		fmt.Fprintf(out, "✓ policy   nothing to write: the tailnet policy already has tailworks' tagOwners and grants\n")
		return ar.Before, false, nil
	}
	fmt.Fprintf(out, "✓ policy   applied (validated by the control plane, written with If-Match)\n")
	// What was written: the policy ApplyRetry read, plus the snippet.
	if after, err := policy.MergeWith(slices.Clone(ar.Before), want, opts); err == nil {
		return after, true, nil
	}
	if after, _, err := api.GetPolicyHuJSON(ctx); err == nil {
		return after, true, nil
	}
	return pol, true, nil
}
