package netd_test

import (
	"testing"

	"github.com/rosskukulinski/tailworks/netd"
)

// TestRT3_H21_OnceDecoded (#154, ledger H21): netd decodes a public path
// once, checks the decoded form, and hands the app exactly that form. A
// double-encoded traversal or slash reaches the app encoded once (never as
// "/../x" or "/a/b"); an app that decodes again sees what netd did not
// check (docs/public-ingress.md).
func TestRT3_H21_OnceDecoded(t *testing.T) {
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/*"}, Limits: limits(nil), Enabled: true}, nil)
	c := e.client(defaultSrc())
	for _, tc := range []struct{ req, want string }{
		{"/%252e%252e/x", "/%2e%2e/x"},
		{"/a/%252e%252e/%252e%252e/x", "/a/%2e%2e/%2e%2e/x"},
		{"/a%252Fb", "/a%2Fb"},
		{"/a%255Cb", "/a%5Cb"},
		{"/x%2500", "/x%00"},
	} {
		before := e.seenCount()
		resp, body := do(t, c, "GET", e.url(tc.req), nil, nil)
		if resp.StatusCode != 200 || e.seenCount() != before+1 {
			t.Errorf("%s: %d %s, want it to reach the app", tc.req, resp.StatusCode, body)
			continue
		}
		if got := e.lastSeen().URL.Path; got != tc.want {
			t.Errorf("%s: the app saw %q, want %q (decoded exactly once)", tc.req, got, tc.want)
		}
	}
}
