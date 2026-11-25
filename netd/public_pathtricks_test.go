package netd_test

import (
	"path"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// TestPublicPathTricksNeverReachPlatformPaths pins ledger claim C13 ("a
// public request never reaches /_tw or a DO directly") against encoding
// and normalisation tricks, under a catch-all route. Each trick is refused
// or, if it reaches the app, its path does not name a platform path once
// decoded and cleaned (what reaches the app goes to the workload's own
// default export, never the platform's /_tw addressing).
// hostd.TestPublicIngress §2 checks the plain cases through a real hostd
// and workerd.
//
// Control bytes (#185, ledger H13/F9 input half): a decoded path with a
// C0 control or DEL is refused bad_request, counted in the reject summary
// like other bad paths, and never reaches the app.
func TestPublicPathTricksNeverReachPlatformPaths(t *testing.T) {
	restore := netd.SetPublicRejectWindow(300 * time.Millisecond)
	defer restore()
	e := newPubEnv(t, netd.PublicOptions{Routes: []string{"/*"}, Limits: limits(nil), Enabled: true}, nil)
	c := e.client(defaultSrc())
	tricks := []string{
		"/_tw",
		"/_tw/",
		"/_tw/o/Room/lobby",
		"/%5Ftw/o/Room/lobby", // encoded "_"
		"/%5ftw",
		"/_t%77/o/Room/lobby", // encoded "w"
		"/%5F%74%77/o/Room/lobby",
		"/_tw%2Fo%2FRoom%2Flobby", // encoded slashes
		"/x/../_tw/o/Room/lobby",
		"/x/%2e%2e/_tw/o/Room/lobby",
		"/x/%2E%2E%2F_tw",
		"/./_tw",
		"/%2e/_tw",
		"//_tw/o/Room/lobby",
		"/_tw;/o/Room/lobby",
		"/%255Ftw/o/Room/lobby", // double-encoded: once-decoded it is "/%5Ftw", which is the app's to refuse (H21)
	}
	// Every one of these is refused bad_request (#185).
	controls := []string{
		"/_tw%00/o",
		"/x%00",
		"/webhooks/%1b%5b31m",
		"/%1B",
		"/x%0d%0ay",
		"/x%0Ay",
		"/x%09y",
		"/x%7f",
		"/x%7F/y",
	}
	for _, p := range tricks {
		resp, _ := do(t, c, "GET", e.url(p), nil, nil)
		t.Logf("%-32s %d %s", p, resp.StatusCode, resp.Header.Get("tw-error"))
	}
	for _, p := range controls {
		resp, body := do(t, c, "GET", e.url(p), nil, nil)
		if resp.StatusCode != 400 || resp.Header.Get("tw-error") != "bad_request" {
			t.Errorf("%s: %d %s %s, want 400 bad_request", p, resp.StatusCode, resp.Header.Get("tw-error"), body)
		}
	}
	// Refused paths are summarized with the other bad_request refusals.
	select {
	case s := <-e.rejs:
		if s.BadRequest < len(controls) {
			t.Errorf("summary %+v: want at least %d bad_request", s, len(controls))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reject summary")
	}
	// netd hands the app the once-decoded path; the platform wrapper
	// matches "/_tw" and "/_tw/..." on it. A request that reached the app
	// must not be one of those, nor carry a control byte.
	e.upMu.Lock()
	defer e.upMu.Unlock()
	for _, r := range e.seen {
		if strings.ContainsFunc(r.URL.Path, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			t.Errorf("a public request reached the app with a control byte in its path: %q", r.URL.Path)
		}
		if clean := path.Clean("/" + r.URL.Path); clean == "/_tw" || strings.HasPrefix(clean, "/_tw/") {
			t.Errorf("a public request reached the app at a platform path: %q (raw %q)", r.URL.Path, r.URL.EscapedPath())
		}
	}
}
