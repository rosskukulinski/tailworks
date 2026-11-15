package netd_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rosskukulinski/twd/internal/publicspec"
	"github.com/rosskukulinski/twd/netd"
)

const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

func TestWantsHTML(t *testing.T) {
	for accept, want := range map[string]bool{
		browserAccept:                          true,
		"text/html":                            true,
		"text/*;q=0.5, application/json;q=0.2": true,
		"":                                     false,
		"*/*":                                  false,
		"application/json":                     false,
		"application/json, text/html":          false, // a tie keeps JSON
		"text/html;q=0.5, application/json":    false,
		"text/html;q=0":                        false,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		if got := netd.WantsHTML(r); got != want {
			t.Errorf("Accept %q: %v, want %v", accept, got, want)
		}
	}
}

// TestPublicErrorsHTML (issue #50): a browser on a public route gets an
// HTML page with the status, code and docs link, and nothing of the
// request or the workload; other clients keep JSON.
func TestPublicErrorsHTML(t *testing.T) {
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/webhooks/*"}, Limits: limits(func(l *publicspec.Limits) { l.IPRPS, l.IPBurst = 0.001, 1 }), Enabled: true}, nil)
	c := e.client(defaultSrc())
	path := "/secret-admin-<script>alert(1)</script>"
	resp, body := do(t, c, "GET", e.url(path), nil, map[string]string{"Accept": browserAccept})
	if resp.StatusCode != 404 || resp.Header.Get("twd-error") != "not_public" || !strings.HasPrefix(resp.Header.Get("content-type"), "text/html") {
		t.Fatalf("html 404: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if !strings.Contains(body, "404 Not Found") || !strings.Contains(body, "<code>not_public</code>") || !strings.Contains(body, netd.DocBase+"not_public") ||
		strings.Contains(body, "secret-admin") || strings.Contains(body, "<script>") || strings.Contains(body, "this path is not public") {
		t.Fatalf("html body: %s", body)
	}
	if csp := resp.Header.Get("content-security-policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("csp: %q", csp)
	}
	// The rate limit: HTML with Retry-After shown; JSON for curl.
	resp, body = do(t, c, "GET", e.url("/webhooks/x"), nil, map[string]string{"Accept": browserAccept})
	if resp.StatusCode != 429 || !strings.Contains(body, "<code>rate_limited</code>") || !strings.Contains(body, "retry after") {
		t.Fatalf("html 429: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, c, "GET", e.url("/nope"), nil, map[string]string{"Accept": "*/*"})
	var je map[string]any
	if resp.StatusCode/100 != 4 || !strings.HasPrefix(resp.Header.Get("content-type"), "application/json") || json.Unmarshal([]byte(body), &je) != nil || je["code"] == nil {
		t.Fatalf("json default: %d %v %s", resp.StatusCode, resp.Header, body)
	}
}
