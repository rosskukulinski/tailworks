package mgmt

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/rosskukulinski/tailworks/internal/headscale"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// MintHeadscale is the Headscale profile (ADR-015, RFC §7.2): the tenant's
// coordination server is a Headscale server at ControlURL, whose REST API
// (/api/v1 on the same listener) mints tagged pre-auth keys with the API
// key in APIKeyFile (HEADSCALE_API_KEY=…, 0600, re-read at every use so a
// rotated key is picked up without a restart). Tailnet is the server's
// dns.base_domain.
//
// What the profile leaves out, because Headscale has no equivalent:
//   - WIF: the API key is a long-lived secret (rotate it; ADR-015);
//   - Tailscale Services: a workload is reached at its own workload
//     node's MagicDNS name, so tailworks.jsonc tailnet.service is refused;
//   - Funnel: public routes (ADR-029) are refused, publicIngress is
//     always deny;
//   - posture: kill Tier 3 doesn't exist; Tiers 1 (netd) and 2 (node
//     delete) work as on Tailscale;
//   - key-level nodeAttributes and nodeAttrs app caps: caps travel in
//     grants only (they already do).
const MintHeadscale = "headscale"

// Headscale reports whether the tenant uses the Headscale profile.
func (t Tenant) Headscale() bool { return t.MintMode == MintHeadscale }

// checkHeadscaleTenant validates a Headscale-profile tenant's settings.
func checkHeadscaleTenant(apiKeyFile, controlURL, publicIngress string) error {
	if controlURL == "" {
		return errf(400, "bad_request", "mintMode headscale needs controlURL (the Headscale server_url; its API is served there too)")
	}
	if u, err := url.Parse(controlURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errf(400, "bad_request", "controlURL %q is not an http(s) URL", controlURL)
	}
	if apiKeyFile == "" {
		return errf(400, "bad_request", "mintMode headscale needs apiKeyFile (a 0600 file with %s=…, from `headscale apikeys create`)", headscale.APIKeyEnv)
	}
	if !filepath.IsAbs(apiKeyFile) {
		return errf(400, "bad_request", "apiKeyFile must be an absolute path")
	}
	f, err := tsapi.OpenSecretFile(apiKeyFile)
	if err != nil {
		return errf(400, "bad_request", "apiKeyFile: %v", err)
	}
	f.Close()
	if publicIngress == PublicAllow {
		return errHeadscaleNoPublic()
	}
	return nil
}

// headscaleAPI builds the tenant's Headscale client. One credential serves
// every purpose (mint, lifecycle, policy), as in apikey mode.
func (b *Broker) headscaleAPI(t Tenant) (TailnetAPI, error) {
	if t.APIKeyFile == "" || t.ControlURL == "" {
		return nil, fmt.Errorf("tenant %s: headscale mode needs apiKeyFile and controlURL", t.ID)
	}
	k, err := tsapi.LoadSecretEnvFile(t.APIKeyFile, headscale.APIKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("tenant %s headscale api key: %w", t.ID, err)
	}
	return &headscale.Client{BaseURL: t.ControlURL, APIKey: k, BaseDomain: t.Tailnet, HTTP: b.HTTP}, nil
}

func errHeadscaleNoPublic() error {
	return errf(400, "profile_unsupported",
		"public routes need Tailscale Funnel, which Headscale doesn't have: a Headscale-profile tenant's publicIngress is always deny (ADR-015)")
}

// errHeadscaleNoService refuses tailworks.jsonc tailnet.service on Headscale.
func errHeadscaleNoService(name string) error {
	return errf(400, "profile_unsupported",
		"tailworks.jsonc tailnet.service %q needs Tailscale Services, which Headscale doesn't have; remove tailnet.service: on the Headscale profile a workload is reached at its own workload node's MagicDNS name (ADR-015)", name)
}

// headscalePreflight refuses, up front, the parts of a deploy the
// Headscale profile can't serve.
func headscalePreflight(t Tenant, r PreflightRequest) error {
	if !t.Headscale() {
		return nil
	}
	if r.Public {
		return errHeadscaleNoPublic()
	}
	if r.Service != "" {
		return errHeadscaleNoService(r.Service)
	}
	return nil
}

// tagOwnersFor is the owners list of a tailworks tag in a preflight snippet.
// Headscale's policy parser rejects autogroup:admin (owners are users,
// groups or tags), so there tailworks' tags are owned by tag:tw alone: the
// API key mints them, no person tags a device with them.
func tagOwnersFor(t Tenant) []string {
	if t.Headscale() {
		return []string{"tag:tw"}
	}
	return []string{"autogroup:admin", t.Owner()} // teams.go
}
