package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// Prompt by default (issue #481): tw up, tw tenant add and tw deploy offer
// to write the policy lines tailworks needs when they run on a terminal.
// The write itself is the unchanged additive / validated / If-Match path
// (up_policyapply.go, policyapply.go); only who decides moves from a flag
// to the y/N prompt that already guarded it.
//
//   - --apply-policy: show the lines and ask (--yes skips it; without a
//     terminal or with --json, --yes is required). A "no" is cancelled.
//   - neither flag, on a terminal, without --json or --yes: when lines are
//     missing and the control plane would accept the write, show them and
//     ask. "n" goes on as print-only did (the lines are printed; the hard
//     stops for lines tailworks can't start without stay).
//   - --apply-policy=false, no terminal, --json, or a bare --yes:
//     print-only. Nothing asks and nothing is written; automation keeps
//     --apply-policy --yes.

// policyWriteMode is how a command treats missing policy lines.
type policyWriteMode int

const (
	policyPrintOnly policyWriteMode = iota // print the lines; never ask, never write
	policyAskFirst                         // terminal, no flag: offer to write; "n" prints
	policyApplyFlag                        // --apply-policy: ask (or --yes); "n" is cancelled
)

// policyModeOf is the mode for a parsed command line with an
// --apply-policy flag (apply) and --yes (yes).
func policyModeOf(f *flags, apply, yes bool) policyWriteMode {
	switch {
	case apply:
		return policyApplyFlag
	case flagGiven(f.FlagSet, "apply-policy"): // --apply-policy=false: never ask
		return policyPrintOnly
	case yes || *f.json || !confirmIsTerminal():
		return policyPrintOnly
	}
	return policyAskFirst
}

// policyDeclined: the prompt was answered with anything but y.
func policyDeclined(err error) bool {
	ce := asCLIErr(err)
	return ce != nil && ce.Code == "cancelled"
}

// policyAskHint is printed before the default prompt: how to never see it.
func policyAskHint(out io.Writer, cmdline string) {
	fmt.Fprintf(out, "  (tw asks because this is a terminal; %s --apply-policy=false never asks, and without a terminal tw only prints the lines)\n", cmdline)
}

// bootstrapPolicyGate decides whether a bootstrap snippet (snip; miss is
// what pol lacks of it) is written: tw up on Headscale, tw tenant add, and
// the Headscale profile check of tw up. It returns true to write.
// policyApplyFlag asks as confirmBootstrapWrite always has.
// policyAskFirst first validates the merged policy with the control plane
// (a refusal, such as a client without write access, skips the prompt) and
// then asks; "n" is false with no error.
func bootstrapPolicyGate(ctx context.Context, api policy.API, f *flags, mode policyWriteMode, yes bool, cmdline, profile string, pol []byte, snip policy.Snippet, popts policy.Options, miss policy.Snippet, out io.Writer) (bool, error) {
	switch mode {
	case policyPrintOnly:
		return false, nil
	case policyApplyFlag:
		if err := confirmBootstrapWrite(f, yes, cmdline+" --apply-policy", profile, miss, out); err != nil {
			return false, err
		}
		return true, nil
	}
	if api == nil || !bootstrapWritable(ctx, api, pol, snip, popts) {
		return false, nil
	}
	if out == nil {
		out = os.Stdout
	}
	policyAskHint(out, cmdline)
	if err := confirmBootstrapWrite(f, false, cmdline+" --apply-policy", profile, miss, out); err != nil {
		if policyDeclined(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// bootstrapWritable: the control plane accepts pol with snip merged in
// (Tailscale answers 403 to a client without Policy File › Write).
func bootstrapWritable(ctx context.Context, api policy.API, pol []byte, snip policy.Snippet, popts policy.Options) bool {
	next, err := policy.MergeWith(slices.Clone(pol), snip, popts)
	if err != nil {
		return false
	}
	return api.ValidatePolicy(ctx, next) == nil
}
