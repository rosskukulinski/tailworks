package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/headscale"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// Control-plane profiles (`--control`, ADR-015).
const (
	controlTailscale = "tailscale"
	controlHeadscale = "headscale"
)

// controlAPI is what `tw up` and `tw tenant add` need from the tenant's
// control-plane API: mint tailworks' own node keys and read (and, with
// --apply-policy, write) the policy. *tsapi.Client and *headscale.Client
// both implement it.
type controlAPI interface {
	policy.API
	CreateKey(ctx context.Context, o tsapi.KeyOptions) (tsapi.Key, error)
}

var (
	_ controlAPI = (*tsapi.Client)(nil)
	_ controlAPI = (*headscale.Client)(nil)
)

// checkControlProfile validates --control and, for headscale, that
// --control-url names the Headscale server (not Tailscale's).
func checkControlProfile(profile, controlURL, cmd string) error {
	switch profile {
	case controlTailscale:
		return nil
	case controlHeadscale:
	default:
		return fail("bad_request", "--control must be "+controlTailscale+" or "+controlHeadscale+", not "+profile)
	}
	u, err := url.Parse(controlURL)
	if controlURL == prodControl || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fail("bad_request", cmd+" --control=headscale needs --control-url <your Headscale server_url>",
			"the Headscale profile mints through the server's REST API (/api/v1), served on the same URL as its control protocol").
			withFix("tw " + cmd + " --control-url https://headscale.example.net --api-key-file <file with " + headscale.APIKeyEnv + "=…> …")
	}
	return nil
}

// headscaleClient loads the Headscale API key from keyFile (0600) and
// checks it against the server: the API key must be valid, and the policy
// readable. It returns the client and the policy as stored.
func headscaleClient(ctx context.Context, keyFile, controlURL, baseDomain string) (*headscale.Client, []byte, error) {
	key, err := tsapi.LoadSecretEnvFile(keyFile, headscale.APIKeyEnv)
	if err != nil {
		var ins *tsapi.InsecureFileError
		if errors.As(err, &ins) {
			return nil, nil, fail("credential_file_insecure", keyFile+" is readable by other users", err.Error()).withFix("chmod 600 " + keyFile)
		}
		return nil, nil, fail("bad_request", "can't read "+headscale.APIKeyEnv+" from "+keyFile, err.Error()).
			withFix("headscale apikeys create --expiration 90d   then write " + headscale.APIKeyEnv + "=<key> to " + keyFile + " (mode 0600)")
	}
	c := &headscale.Client{BaseURL: controlURL, APIKey: key, BaseDomain: baseDomain}
	if err := c.CheckVersion(ctx); errors.Is(err, headscale.ErrTooOld) {
		return nil, nil, fail("profile_unsupported", "the Headscale server at "+controlURL+" is older than "+headscale.MinVersion,
			err.Error()).withFix("upgrade Headscale to " + headscale.MinVersion + " or later (the profile's caps travel in grants, which older releases don't have)")
	}
	pol, _, err := c.GetPolicyHuJSON(ctx)
	if err != nil {
		fix := "check --control-url and that the key in " + keyFile + " is a valid, unexpired Headscale API key"
		if tsapi.IsUnauthorized(err) {
			fix = "the API key was refused: create a new one (headscale apikeys create) and write it to " + keyFile
		}
		return nil, nil, fail("bootstrap_failed", "the Headscale API at "+controlURL+" rejected the request",
			"step:  GET "+strings.TrimSuffix(controlURL, "/")+"/api/v1/policy → "+err.Error()).withFix(fix)
	}
	return c, pol, nil
}

// headscaleSnippet is baseSnippet in the Headscale profile: no
// autogroup:admin (Headscale rejects it), so tag:tw has no owners and owns
// tailworks' other tags.
func headscaleSnippet() policy.Snippet { return policy.Headscale(baseSnippet()) }

// profileSnippet is the base policy snippet for the profile, with the
// matching apply options.
func profileSnippet(profile string) (policy.Snippet, policy.Options) {
	if profile == controlHeadscale {
		return headscaleSnippet(), policy.Options{Headscale: true}
	}
	return baseSnippet(), policy.Options{}
}

// headscaleProfileNote is printed after a Headscale-profile setup.
const headscaleProfileNote = "Headscale profile (ADR-015): no Tailscale Services (workloads are reached at their own node's MagicDNS name), no public routes (no Funnel), no posture (kill Tiers 1 and 2 only). The API key mints every tag: keep its file 0600 and rotate it (docs/headscale.md)"

// headscaleKeyFile resolves --api-key-file for the Headscale profile.
func headscaleKeyFile(keyFile string) (string, error) {
	if keyFile == "" {
		return "", fail("bad_request", "the Headscale profile needs --api-key-file (a 0600 file with "+headscale.APIKeyEnv+"=…)").
			withFix("headscale apikeys create --expiration 90d   then write " + headscale.APIKeyEnv + "=<key> to a 0600 file (tw up, on the default state dir, reads ~/.config/tailworks/headscale.env without the flag)")
	}
	abs, _ := filepath.Abs(keyFile)
	return abs, nil
}

// policyHome says where a human edits the policy in profile.
func policyHome(profile string) string {
	if profile == controlHeadscale {
		return "your Headscale policy (the policy.path file, or `headscale policy set` with policy.mode database)"
	}
	return "the admin console"
}

// doctorHeadscaleKey is `tw doctor`'s api_key check on the Headscale
// profile: the key file is private and the server accepts the key.
func doctorHeadscaleKey(s *state, kf string) (string, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := headscaleClient(ctx, kf, s.Cfg.ControlURL, s.Cfg.Tailnet); err != nil {
		ce, _ := err.(*cliErr)
		if ce == nil {
			return "fail", firstLine(err.Error()), ""
		}
		fix := ""
		if len(ce.Fix) > 0 {
			fix = ce.Fix[0]
		}
		return "fail", ce.Message, fix
	}
	return "ok", "the Headscale API key in " + kf + " can read " + s.Cfg.ControlURL + "'s policy (key not shown)", ""
}

// headscaleUpPolicy makes sure the Headscale policy defines tailworks' tags
// before mgmt joins its node: Headscale refuses a registration
// whose tags the policy doesn't define ("requested tags [...] are invalid
// or not permitted"), even with a key minted by the API key. With apply
// it shows the profile's snippet, asks (or takes --yes) and writes it
// (checked by the server first); without,
// it stops before anything is minted and prints the snippet.
func headscaleUpPolicy(ctx context.Context, api policy.API, f *flags, pol []byte, mode policyWriteMode, yes bool, out io.Writer) ([]byte, bool, error) {
	snip, popts := profileSnippet(controlHeadscale)
	miss, err := policy.Missing(pol, snip)
	if err != nil {
		return pol, false, fail("bad_request", "can't parse the Headscale policy", err.Error())
	}
	if miss.Empty() {
		return pol, false, nil
	}
	write, err := bootstrapPolicyGate(ctx, api, f, mode, yes, "tw up", controlHeadscale, pol, snip, popts, miss, out) // policy_ask.go
	if err != nil {
		return pol, false, err
	}
	if !write {
		return pol, false, fail("policy_missing_tag_owners", "the Headscale policy does not define tailworks' tags, and Headscale refuses to register a node with undefined tags",
			"nothing was minted").withFix(append([]string{"re-run with --apply-policy (needs policy.mode: database), or add to " + policyHome(controlHeadscale) + ":"},
			snippetLines(miss)...)...)
	}
	ar, err := policy.ApplyWith(ctx, api, snip, popts)
	if err != nil {
		return pol, false, fail("policy_apply_failed", "could not apply the policy snippet to Headscale", err.Error()).
			withFix(append([]string{"add it to " + policyHome(controlHeadscale) + ":"}, snippetLines(miss)...)...)
	}
	next, _, err := api.GetPolicyHuJSON(ctx)
	if err != nil {
		return pol, false, fail("bootstrap_failed", "can't re-read the Headscale policy", err.Error())
	}
	return next, ar.Changed, nil
}

// advertisedTags is what one of tailworks' own nodes advertises when it joins:
// its tag, or (Headscale) nothing, because Headscale refuses a pre-auth
// key registration that also requests tags; the key carries them. An
// empty non-nil slice also tells mgmtd to advertise none.
func advertisedTags(headscale bool, tag string) []string {
	if headscale {
		return []string{}
	}
	return []string{tag}
}
