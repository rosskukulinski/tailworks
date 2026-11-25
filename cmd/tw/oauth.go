package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/tailscale/hujson"
)

// OAuth mint mode (M12; the only one for Tailscale tailnets): a Tailscale
// OAuth client scoped to auth_keys with tag tag:tw (the same mechanism as
// the Kubernetes operator and the GitHub Action). Its secret never expires
// and stays in a 0600 file on the host; `tw up` and tw-mgmt read it at
// use and exchange it for short-lived access tokens. There is no browser
// login and no Funnel or HTTPS requirement: the OAuth client mints mgmt's own node key (tag:tw-mgmt), the host's node key
// (tag:tw-host) and every object's key, all tags tag:tw owns.

const (
	consoleACLURL   = "https://login.tailscale.com/admin/acls/file"
	consoleOAuthURL = "https://login.tailscale.com/admin/settings/oauth"
)

// oauthConsoleSteps are the admin-console steps to create the client
// (issue #442), shared by the first-run prompt (promptOAuthSecret) and
// mint_mode_required: each caller appends how the secret reaches tw. No
// browser is opened: tw up often runs over SSH on a headless machine.
func oauthConsoleSteps() []string {
	return []string{
		"1. Access controls (" + consoleACLURL + "): inside the existing \"tagOwners\" section, add",
		"       \"tag:tw\": [\"autogroup:admin\"],",
		"   the one line tw can't write (the console only offers existing tags for the client); tw up --apply-policy writes the rest after showing it, or tw up prints what is missing",
		"2. Settings › Trust credentials › Credential › OAuth (" + consoleOAuthURL + ")",
		"3. Scopes (they can't be changed on an existing client, so pick them now):",
		"     Auth Keys › Write, tag tag:tw (required)",
		"     Policy File › Write lets tw up --apply-policy write the policy; Read: you paste it yourself",
		"     optional: Devices › Core › Write, tag tag:tw (tw kill deletes the device); Services › Write (apps that use a Tailscale Service)",
	}
}

// tagOwnersNeeded is what the mint credential (tag:tw) needs to mint
// mgmt's and the host's node keys: tag:tw among their owners.
func tagOwnersNeeded() policy.Snippet {
	return policy.Snippet{TagOwners: map[string][]string{mgmt.MgmtTag: {"tag:tw"}, mgmt.HostTag: {"tag:tw"}}}
}

// tagOwnersPart is the tagOwners section of the operator snippet.
func tagOwnersPart() policy.Snippet {
	return policy.Snippet{TagOwners: operatorSnippet().TagOwners}
}

// snippetLines renders s indented for the terminal, led by a HuJSON
// comment: pasted as is next to an existing "tagOwners" or "grants"
// section, it would be a second key and the policy would not parse
// (issue #389).
func snippetLines(s policy.Snippet) []string {
	return pasteLines(s, nil, "    ") // policysnippet.go
}

// policyMissingTagOwners is the typed error for "the control plane refused
// (or would refuse) to mint keys for tailworks' own tags": before any login URL
// is minted, with the exact snippet.
//
// pol is the tailnet policy when tw could read it (nil otherwise): the fix
// then names only the entries to add or replace (tagOwnersFix).
func policyMissingTagOwners(why string, cause error, pol []byte) *cliErr {
	lines := []string{why}
	if cause != nil {
		lines = append(lines, "control plane: "+netd.Redact(cause.Error()))
	}
	return fail("policy_missing_tag_owners", "the tailnet policy does not let tag:tw own "+mgmt.MgmtTag+" and "+mgmt.HostTag, lines...).withFix(tagOwnersFix(pol)...)
}

// tagOwnersFix is the paste-safe fix for missing tag ownership (issue
// #389). A tailnet with an OAuth client already has a "tagOwners" section
// (tag:tw must be in it before the client can be created), so a second
// "tagOwners" block pasted next to it breaks the policy. With the policy
// known, the fix lists only the entries to add, each with its complete
// owner list: a tag that is already there keeps its owners and gains
// tag:tw, so its line is replaced rather than duplicated.
func tagOwnersFix(pol []byte) []string {
	const rerun = "then run tw up again (nothing was minted)"
	var have struct {
		TagOwners map[string][]string `json:"tagOwners"`
	}
	std, err := hujson.Standardize(slices.Clone(pol))
	if pol == nil || err != nil || json.Unmarshal(std, &have) != nil || len(have.TagOwners) == 0 {
		return append([]string{"add to your tailnet policy (admin console › Access controls), " + rerun + ":"}, snippetLines(tagOwnersPart())...)
	}
	want := tagOwnersPart().TagOwners
	lines := []string{`in your tailnet policy's existing "tagOwners" section (admin console › Access controls), add these entries (replacing any line for the same tag), ` + rerun + ":"}
	for _, tag := range slices.Sorted(maps.Keys(want)) {
		owners := want[tag]
		if cur, ok := have.TagOwners[tag]; ok {
			if tag == policy.TwTagPrefix || slices.Contains(cur, policy.TwTagPrefix) {
				continue
			}
			owners = append(slices.Clone(cur), policy.TwTagPrefix)
		}
		lines = append(lines, fmt.Sprintf("    %q: %s,", tag, policy.ListHuJSON(owners)))
	}
	return lines
}

// mintModeRequired is `tw up` with no credential on a fresh state dir:
// how to create the OAuth client (or, on Headscale, which flags to use).
func mintModeRequired() *cliErr {
	lines := []string{
		"tw mints a Tailscale auth key for every node it runs (mgmt, this host, each object) with an OAuth client of your tailnet;",
		"the secret stays in a 0600 file on this machine. Create one:",
	}
	for _, s := range oauthConsoleSteps() {
		lines = append(lines, "  "+s)
	}
	lines = append(lines, "  4. run tw up on a terminal and paste the Client Secret when asked (saved to ~/.config/tailworks/oauth.env, 0600)",
		"     no terminal? put TS_OAUTH_CLIENT_SECRET=tskey-client-… in ~/.config/tailworks/oauth.env, or pass tw up --oauth-client-file <file>",
		"",
		"Headscale instead of Tailscale: tw up --control-url <server_url> --tailnet <base_domain> (the API key in ~/.config/tailworks/headscale.env, or --api-key-file <file with HEADSCALE_API_KEY=…>)",
		"",
		"policy:")
	lines = append(lines, snippetLines(operatorSnippet())...)
	return fail("mint_mode_required", "tw up needs an OAuth client (--oauth-client-file)", lines...).
		withFix("tw up --oauth-client-file <file>")
}

// oauthPreflight runs before anything is minted: the client exchanges, the
// tailnet is named (devices endpoint, if the client may list devices),
// and, if the client may read the policy, tag ownership is verified.
// readable reports whether the policy could be read; pol is it then.
//
// needed is the tag ownership the layout requires (tagOwnersNeeded for the
// two-process layout; single-process: tag:tw-mgmt only).
func oauthPreflight(ctx context.Context, api *tsapi.Client, src *tsapi.OAuthSource, clientID, file string, out io.Writer, needed policy.Snippet) (tailnet string, pol []byte, readable bool, err error) {
	if _, err := src.Token(ctx); err != nil {
		return "", nil, false, fail("oauth_exchange_failed", "Tailscale refused the OAuth client "+clientID+" from "+file, netd.Redact(err.Error())).
			withFix("check TS_OAUTH_CLIENT_SECRET in "+file+" (admin console › Settings › Trust credentials); a revoked client must be regenerated",
				"the client needs the Auth Keys (write) scope with tag tag:tw")
	}
	if api.Tailnet == "-" {
		if tn, err := api.DiscoverTailnet(ctx); err == nil {
			tailnet = tn
		}
	}
	p, _, perr := api.GetPolicyHuJSON(ctx)
	if perr != nil {
		fmt.Fprintf(out, "› policy   the OAuth client can't read the policy (%s); tag ownership is checked by the first mint instead\n", firstLine(netd.Redact(perr.Error())))
		return tailnet, nil, false, nil
	}
	return tailnet, p, true, oauthNeededCheck(p, true, needed)
}

// oauthNeededCheck is tw up's hard stop when the policy it could read
// (readable) lacks the tag ownership needed for this layout.
func oauthNeededCheck(p []byte, readable bool, needed policy.Snippet) error {
	if !readable {
		return nil
	}
	miss, merr := policy.Missing(p, needed)
	if merr != nil {
		return fail("bad_request", "can't parse the tailnet policy", merr.Error())
	}
	if !miss.Empty() {
		return policyMissingTagOwners("the policy (read with the OAuth client) lacks tag:tw as an owner of: "+strings.Join(slices.Sorted(maps.Keys(miss.TagOwners)), ", "), nil, p).
			withFix("or let tw write it: tw up --apply-policy (needs an OAuth client with the Policy File › Write scope)")
	}
	return nil
}

// mintNodeKey mints a single-use, pre-authorized node key for tailworks' own
// nodes with the OAuth client (or Headscale API key), translating a tag refusal.
func mintNodeKey(ctx context.Context, api controlAPI, tag, desc string) (string, error) {
	k, err := api.CreateKey(ctx, tsapi.KeyOptions{Tags: []string{tag}, Preauthorized: true, ExpirySeconds: 300, Description: desc})
	if err != nil {
		if tsapi.IsTagsNotPermitted(err) {
			return "", policyMissingTagOwners("the control plane refused a "+tag+" key for the credential's tag", err, nil)
		}
		return "", fail("bootstrap_failed", "could not mint "+tag+"'s node key", netd.Redact(err.Error()))
	}
	return k.Secret, nil
}
