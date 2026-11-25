package netd

import (
	"net/http"
	"testing"
)

// RT3-18 (H18, fix F11): client copies of every Tailscale-* header and the
// underscore spellings of the platform's own headers are stripped by
// prefix, not by a list of exact names.
func TestRT3_H18_StripByPrefix(t *testing.T) {
	spoofed := []string{
		"Tailscale-Ingress-Src", "Tailscale-Node-Caps", "Tailscale-User-Login",
		"Tailscale-App-Capabilities", "tailscale-anything-new",
		"X_TW_CF", "x_tw_object", "TW_ERROR", "tw_actor",
	}
	kept := []string{"tailscaled-version", "twilio-signature", "x-twitter-token", "content-type"}
	h := http.Header{}
	for _, k := range append(append([]string{}, spoofed...), kept...) {
		// Raw keys: underscore names aren't canonicalized by the server.
		h[k] = []string{"v"}
	}
	stripIdentityHeaders(h)
	for _, k := range spoofed {
		if _, ok := h[k]; ok {
			t.Errorf("%s survived the strip", k)
		}
	}
	for _, k := range kept {
		if _, ok := h[k]; !ok {
			t.Errorf("%s was stripped", k)
		}
	}
}
