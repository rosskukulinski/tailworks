package netd

import (
	"net/http"
	"testing"
)

// A caller must not be able to spoof the platform's identity headers, and
// the strip must match the x-tw- / tw- prefixes with their dash: a bare "tw"
// prefix would also eat app headers like Twilio's signature.
func TestStripIdentityHeadersPrefixes(t *testing.T) {
	spoofed := []string{
		"x-tw-caller", "X-Tw-Caller", "x-tw-host", "x-tw-as", "x-tw-exec",
		"Tw-Actor", "Tw-Error", "Tw-MCP-Tools", "tw-anything",
		"cf-connecting-ip", "x-forwarded-for", "tailscale-user-login", "tailnet-assertion",
	}
	kept := []string{
		"twilio-signature", "X-Twilio-Signature", "x-twitter-token", "twitch-id", "tweak",
		"content-type", "authorization", "x-request-id",
	}
	h := http.Header{}
	for _, k := range append(append([]string{}, spoofed...), kept...) {
		h.Set(k, "v")
	}
	stripIdentityHeaders(h)
	for _, k := range spoofed {
		if h.Get(k) != "" {
			t.Errorf("%s survived the strip", k)
		}
	}
	for _, k := range kept {
		if h.Get(k) != "v" {
			t.Errorf("%s was stripped; only x-tw-* and tw-* belong to the platform", k)
		}
	}
}
