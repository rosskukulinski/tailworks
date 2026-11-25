package mgmt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Browser error pages on the admin UI's entry points (errors_html.go,
// issue #497): same status and Tw-Error, an escaped HTML page for a
// browser's GET of /, /ui, /ui/...; JSON exactly as before for everyone
// else.

const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

func newErrHTMLEnv(t *testing.T) func(from, method, url string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := newTestEnv(t)
	self := SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net", IP: "100.64.0.9", NodeID: "nSELF"}
	srv := &Server{M: e.m, Self: self, Version: "test-errhtml", Listeners: []string{"tsnet:80"}}
	srv.Hosts = NewHostAllowlist(MgmtHostNames(self, "tw-mgmt", "")...)
	h := srv.TailnetHandler(func(_ context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case fromAdmin:
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		case fromUser:
			return WhoIsInfo{NodeID: "nEVE", Login: "eve@example.com"}, nil
		case "100.64.0.7:1": // a login that would be markup if unescaped
			return WhoIsInfo{NodeID: "nMAL", Login: `<script>alert(1)</script>@example.com`}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown %s", addr)
	}, srv.Handler())
	return func(from, method, url string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, nil)
		r.RemoteAddr = from
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		if host := hdr["Host"]; host != "" {
			r.Host = host // a Host that is not one of mgmt's names
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}

func assertErrHTML(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) string {
	t.Helper()
	if w.Code != status || w.Header().Get("Tw-Error") != code {
		t.Fatalf("%s = %d Tw-Error %q, want %d %s; body %s", what, w.Code, w.Header().Get("Tw-Error"), status, code, w.Body)
	}
	for k, want := range map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Vary":                    "Accept",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s: %s = %q, want %q", what, k, got, want)
		}
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "<code>"+code+"</code>") ||
		!strings.Contains(body, `href="`+DocBase+code+`" rel="noreferrer"`) {
		t.Errorf("%s: page lacks the doctype, code or doc link:\n%s", what, body)
	}
	return body
}

func assertErrJSON(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || errCode(w) != code || w.Header().Get("Tw-Error") != code ||
		w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("%s = %d %s %q, want JSON %d %s", what, w.Code, w.Header().Get("Content-Type"), w.Body, status, code)
	}
}

func TestErrHTMLNotAdmin(t *testing.T) {
	t.Parallel()
	call := newErrHTMLEnv(t)
	base := "http://tw-mgmt.t.ts.net"
	for _, path := range []string{"/", "/ui", "/ui/", "/ui/objects?tenant=x", "/ui/static/app.css"} {
		body := assertErrHTML(t, "user GET "+path, call(fromUser, "GET", base+path, map[string]string{"Accept": browserAccept}), 403, "not_admin")
		// The grant snippet, in a <pre> (its quotes escaped), and the fix.
		if !strings.Contains(body, "<pre>") || !strings.Contains(body, "&#34;"+AdminCap+"&#34;") ||
			!strings.Contains(body, "eve@example.com") || !strings.Contains(body, "autogroup:admin already have it") {
			t.Errorf("user GET %s: page lacks the snippet, caller or fix:\n%s", path, body)
		}
	}
	// HEAD: same status and headers.
	assertErrHTML(t, "user HEAD /ui/", call(fromUser, "HEAD", base+"/ui/", map[string]string{"Accept": "text/html"}), 403, "not_admin")

	// Escaping: a hostile login is shown as text.
	body := assertErrHTML(t, "hostile GET /ui/", call("100.64.0.7:1", "GET", base+"/ui/", map[string]string{"Accept": "text/html"}), 403, "not_admin")
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("hostile login not escaped:\n%s", body)
	}

	// Everyone else keeps the JSON body exactly as before.
	for _, c := range []struct {
		method, path, accept string
	}{
		{"GET", "/ui/", ""},
		{"GET", "/ui/", "*/*"},
		{"GET", "/ui/", "application/json"},
		{"GET", "/ui/", "application/json, text/html"}, // a tie stays JSON
		{"GET", "/ui/", "text/html;q=0.5, application/json"},
		{"GET", "/", ""},
		{"GET", "/v1/tenants", browserAccept},
		{"GET", "/v1/whoami/x", "text/html"},
		{"GET", "/uix", "text/html"},
		{"POST", "/ui/tenants/x/delete", "text/html"},
		{"POST", MCPPath, "text/html"},
		{"GET", MCPPath, "text/html"},
	} {
		w := call(fromUser, c.method, base+c.path, map[string]string{"Accept": c.accept})
		assertErrJSON(t, fmt.Sprintf("user %s %s Accept %q", c.method, c.path, c.accept), w, 403, "not_admin")
	}
	// An admin is unaffected: the redirect and the UI.
	if w := call(fromAdmin, "GET", base+"/", map[string]string{"Accept": browserAccept}); w.Code != http.StatusFound {
		t.Errorf("admin GET / = %d", w.Code)
	}
}

func TestErrHTMLNoIdentity(t *testing.T) {
	t.Parallel()
	call := newErrHTMLEnv(t)
	body := assertErrHTML(t, "stranger GET /ui/", call("100.64.0.99:1", "GET", "http://tw-mgmt.t.ts.net/ui/", map[string]string{"Accept": browserAccept}), 401, "no_identity")
	if !strings.Contains(body, "from a device on the tailnet") {
		t.Errorf("no_identity page lacks the fix:\n%s", body)
	}
	assertErrJSON(t, "stranger GET /ui/ (CLI)", call("100.64.0.99:1", "GET", "http://tw-mgmt.t.ts.net/ui/", nil), 401, "no_identity")
	assertErrJSON(t, "stranger GET /v1/whoami", call("100.64.0.99:1", "GET", "http://tw-mgmt.t.ts.net/v1/whoami", map[string]string{"Accept": browserAccept}), 401, "no_identity")
}

func TestErrHTMLMisdirected(t *testing.T) {
	t.Parallel()
	call := newErrHTMLEnv(t)
	// A Host that would be markup if it were echoed.
	hostile := `evil.example"><script>alert(1)</script>`
	w := call(fromAdmin, "GET", "http://tw-mgmt.t.ts.net/ui/", map[string]string{"Accept": browserAccept, "Host": hostile})
	body := assertErrHTML(t, "rebound GET /ui/", w, http.StatusMisdirectedRequest, "misdirected_request")
	// The page names mgmt's own names (allowlist only), never the Host.
	if strings.Contains(body, "evil.example") || strings.Contains(body, "<script>") ||
		!strings.Contains(body, "<code>tw-mgmt.t.ts.net</code>") || !strings.Contains(body, "<code>tw-mgmt</code>") {
		t.Errorf("misdirected page echoes the Host or lacks mgmt's names:\n%s", body)
	}
	// The CLI's JSON is unchanged (message names the Host and mgmt).
	w = call(fromAdmin, "GET", "http://tw-mgmt.t.ts.net/ui/", map[string]string{"Host": "evil.example"})
	assertErrJSON(t, "rebound GET /ui/ (CLI)", w, http.StatusMisdirectedRequest, "misdirected_request")
	if !strings.Contains(w.Body.String(), "tw-mgmt.t.ts.net") || strings.Contains(w.Body.String(), "hostNames") {
		t.Errorf("misdirected JSON = %s", w.Body)
	}
}

func TestWantsHTML(t *testing.T) {
	t.Parallel()
	for accept, want := range map[string]bool{
		"":                                    false,
		"*/*":                                 false,
		"application/json":                    false,
		"text/html":                           true,
		"text/*":                              true,
		"application/xhtml+xml":               true,
		browserAccept:                         true,
		"text/html, application/json":         false,
		"text/html;q=0.9, application/json":   false,
		"text/html, application/json;q=0.9":   true,
		"text/html;q=0, */*":                  false,
		"TEXT/HTML ; Q=1":                     true,
		"text/html;q=0.8, */*;q=0.9":          false,
		"application/*;q=0.1, text/html;q=.5": true,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept", accept)
		if got := wantsHTML(r); got != want {
			t.Errorf("wantsHTML(%q) = %v, want %v", accept, got, want)
		}
	}
}
