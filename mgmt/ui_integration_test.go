package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
	"github.com/rosskukulinski/tailworks/mgmt/ui"
)

// The admin UI (mgmt/ui) mounted in Server.Handler: admission by principal,
// the / redirect for browsers, the browser-origin guard, and the pages
// rendered from the real store through in-process /v1 calls.

// uiEnv is a Server over the SQLite store with a fake WhoIs: 100.64.0.1 is
// a registered tag:tw-host node, .6 an admin (AdminCap), .4 a plain user.
type uiEnv struct {
	e   *testEnv
	srv *Server
	h   http.Handler // TailnetHandler(whois, Handler())
	tn  string
}

const (
	fromHost  = "100.64.0.1:1"
	fromAdmin = "100.64.0.6:1"
	fromUser  = "100.64.0.4:1"
	fromSelf  = "100.64.0.9:1" // mgmt's own node: the local operator
)

func newUIEnv(t *testing.T) *uiEnv {
	t.Helper()
	e := newTestEnv(t)
	srv := &Server{M: e.m, Self: SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net", NodeID: "nSELF"}, Version: "test-ui", Listeners: []string{"tsnet:80"}}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case fromHost:
			return WhoIsInfo{NodeID: "nHOST1", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case fromAdmin:
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		case fromUser:
			return WhoIsInfo{NodeID: "nEVE", Login: "eve@example.com"}, nil
		case fromSelf:
			return WhoIsInfo{NodeID: "nSELF", Login: "tagged-devices", Tags: []string{MgmtTag}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown %s", addr)
	}
	u := &uiEnv{e: e, srv: srv, h: srv.TailnetHandler(whois, srv.Handler())}
	// Data: a tenant, a host bound to the .1 node, a deployment and a
	// parent/child pair of objects, the child ready.
	u.tn = v1Tenant(t, e, "t.ts.net", nil)
	var hj struct{ RegistrationSecret string }
	e.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h1", "tenantID": u.tn}, &hj)
	if st, _ := u.call(fromHost, "PUT", "/v1/hosts/h1", `{"capacityNodes":10,"tiers":["process"],"registrationSecret":"`+hj.RegistrationSecret+`"}`, nil); st != 201 {
		t.Fatalf("register host: %d", st)
	}
	return u
}

// call drives the tailnet handler from addr with optional browser-ish
// headers; it returns the status and the body.
func (u *uiEnv) call(from, method, path, body string, hdr map[string]string) (int, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, "http://tw-mgmt.t.ts.net"+path, strings.NewReader(body))
	r.RemoteAddr = from
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	u.h.ServeHTTP(w, r)
	return w.Code, w
}

func errCode(w *httptest.ResponseRecorder) string {
	var out struct {
		Code string `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	return out.Code
}

// seed deploys a workload and creates root + child objects as the host;
// it returns their IDs.
func (u *uiEnv) seed(t *testing.T) (dep, root, kid string) {
	t.Helper()
	var dr DeployResponse
	u.e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": u.tn, "workload": "w", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, &dr)
	h1 := u.e.asHost("h1")
	var ro, ko CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": u.tn, "workload": "w", "namespace": "A", "objectName": "root"}, &ro)
	h1.ok("POST", "/v1/objects/"+ro.ObjectID+"/credentials?epoch=1", nil, &Credential{})
	h1.ok("PATCH", "/v1/objects/"+ro.ObjectID, map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": "nROOT", "magicDNS": "root-node.t.ts.net", "epoch": 1}, nil)
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": u.tn, "workload": "w", "namespace": "A", "objectName": "kid", "parent": map[string]string{"objectID": ro.ObjectID}}, &ko)
	return dr.DeploymentID, ro.ObjectID, ko.ObjectID
}

func TestUIAdmission(t *testing.T) {
	t.Parallel()
	u := newUIEnv(t)
	dep, root, kid := u.seed(t)
	// An admin user gets HTML with everything it may see.
	st, w := u.call(fromAdmin, "GET", "/ui/", "", map[string]string{"Accept": "text/html"})
	if st != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("admin /ui/: %d %s %s", st, w.Header().Get("Content-Type"), w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"h1", u.tn, dep, root, kid, "root-node", "alice@example.com", "test-ui", "tw-mgmt.t.ts.net", `href="/ui/static/app.css"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin overview lacks %q", want)
		}
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// The local operator (mgmt's own node) gets the same.
	if st, w := u.call(fromSelf, "GET", "/ui/", "", nil); st != 200 || !strings.Contains(w.Body.String(), root) {
		t.Fatalf("local /ui/: %d", st)
	}
	// A non-admin user is refused by WhoIs with the grant to add.
	if st, w := u.call(fromUser, "GET", "/ui/", "", nil); st != 403 || errCode(w) != "not_admin" {
		t.Fatalf("user /ui/: %d %s", st, w.Body.String())
	}
	// A registered host is never admitted to the UI (nor to its assets).
	for _, p := range []string{"/ui/", "/ui/objects/" + root, "/ui/static/app.js"} {
		if st, w := u.call(fromHost, "GET", p, "", nil); st != 403 || errCode(w) != "forbidden" {
			t.Fatalf("host %s: %d %s", p, st, w.Body.String())
		}
	}
	// Every page renders for the admin.
	for _, p := range []string{"/ui/objects/" + root, "/ui/objects/" + kid, "/ui/deployments/" + dep, "/ui/hosts/h1", "/ui/tenants/" + u.tn, "/ui/audit", "/ui/audit?object=" + root, "/ui/audit?human=nobody", "/ui/audit?trace=x"} {
		st, w := u.call(fromAdmin, "GET", p, "", nil)
		if st != 200 {
			t.Errorf("%s: %d %s", p, st, w.Body.String())
		}
	}
	// Unknown records are typed 404 pages; unknown UI paths too.
	if st, w := u.call(fromAdmin, "GET", "/ui/objects/obj_nope", "", nil); st != 404 || w.Header().Get("Tw-Error") != "not_found" {
		t.Fatalf("unknown object: %d %q", st, w.Header().Get("Tw-Error"))
	}
	if st, _ := u.call(fromAdmin, "GET", "/ui/nope", "", nil); st != 404 {
		t.Fatalf("unknown page: %d", st)
	}
	// Static assets carry the security headers.
	for _, p := range []string{"/ui/static/app.css", "/ui/static/app.js"} {
		st, w := u.call(fromAdmin, "GET", p, "", nil)
		if st != 200 || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: %d %v", p, st, w.Header())
		}
	}
}

func TestUIRootRedirect(t *testing.T) {
	t.Parallel()
	u := newUIEnv(t)
	st, w := u.call(fromAdmin, "GET", "/", "", map[string]string{"Accept": "text/html,application/xhtml+xml"})
	if st != 302 || w.Header().Get("Location") != "/ui/" {
		t.Fatalf("browser GET /: %d %q", st, w.Header().Get("Location"))
	}
	// API clients hitting / keep today's typed 404.
	if st, w := u.call(fromAdmin, "GET", "/", "", nil); st != 404 || errCode(w) != "not_found" {
		t.Fatalf("API GET /: %d %s", st, w.Body.String())
	}
	if st, w := u.call(fromAdmin, "GET", "/", "", map[string]string{"Accept": "application/json"}); st != 404 || errCode(w) != "not_found" {
		t.Fatalf("API GET / (json): %d %s", st, w.Body.String())
	}
	// Hosts are not redirected into a UI they cannot use... they get the 404 as before.
	if st, _ := u.call(fromHost, "GET", "/", "", map[string]string{"Accept": "text/html"}); st != 302 {
		t.Fatalf("host GET / with Accept html: %d (the redirect is cheap; the UI refuses)", st)
	}
}

func TestUIBrowserGuard(t *testing.T) {
	t.Parallel()
	u := newUIEnv(t)
	_, root, kid := u.seed(t)
	kill := func(id string) string { return "/v1/objects/" + id + "?" + url.Values{"reason": {"ui test"}}.Encode() }
	sameOrigin := map[string]string{"Origin": "http://tw-mgmt.t.ts.net", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "X-Tw-UI": "1"}
	// Cross-site variants are refused before the handler runs: the object
	// stays live.
	for name, hdr := range map[string]map[string]string{
		"cross-site fetch":         {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors", "X-Tw-UI": "1"},
		"origin mismatch only":     {"Origin": "http://evil.example", "X-Tw-UI": "1"},
		"null origin":              {"Origin": "null", "Sec-Fetch-Site": "cross-site"},
		"rebound host":             {"Origin": "http://100.64.0.9", "Sec-Fetch-Site": "same-origin", "X-Tw-UI": "1"},
		"same-origin, no UI hdr":   {"Origin": "http://tw-mgmt.t.ts.net", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors"},
		"same-site (subdomain)":    {"Origin": "http://other.t.ts.net", "Sec-Fetch-Site": "same-site", "X-Tw-UI": "1"},
		"navigate form, no UI hdr": {"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate"},
	} {
		st, w := u.call(fromAdmin, "DELETE", kill(root), "", hdr)
		if st != 403 || errCode(w) != "forbidden" || !strings.Contains(w.Body.String(), "cross-site request refused") {
			t.Errorf("%s: %d %s", name, st, w.Body.String())
		}
		if st, _ := u.call(fromAdmin, "GET", "/v1/objects/"+root, "", nil); st != 200 {
			t.Fatalf("%s: object no longer 200 (%d)", name, st)
		}
	}
	// Browser GETs are never guarded (pages, assets, API reads).
	if st, _ := u.call(fromAdmin, "GET", "/v1/objects/"+root, "", map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors"}); st != 200 {
		t.Fatalf("cross-site GET: %d", st)
	}
	// Same-origin with the opt-in header succeeds.
	st, w := u.call(fromAdmin, "DELETE", kill(kid), "", sameOrigin)
	if st != 202 {
		t.Fatalf("same-origin kill: %d %s", st, w.Body.String())
	}
	// A CLI-style DELETE with no browser headers still succeeds (regression
	// guard: hostd and the tailworks CLI send none of these).
	if st, w := u.call(fromAdmin, "DELETE", kill(root), "", nil); st != 202 {
		t.Fatalf("CLI-style kill: %d %s", st, w.Body.String())
	}
	// Killed objects render the tombstone on their page.
	if st, w := u.call(fromAdmin, "GET", "/ui/objects/"+kid, "", nil); st != 200 || !strings.Contains(w.Body.String(), "ui test") || !strings.Contains(w.Body.String(), "alice@example.com") {
		t.Fatalf("tombstone page: %d %s", st, w.Body.String())
	}
	// The UI's mutation path: a same-origin form POST to /ui/... (no X-Tw-UI
	// needed off /v1) runs the call in-process and redirects back with
	// the outcome; a cross-site one is refused.
	form := map[string]string{"Origin": "http://tw-mgmt.t.ts.net", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Content-Type": "application/x-www-form-urlencoded"}
	r := httptest.NewRequest("POST", "http://tw-mgmt.t.ts.net/ui/hosts/h1/remove", strings.NewReader("drain=true"))
	r.RemoteAddr = fromAdmin
	for k, v := range form {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	u.h.ServeHTTP(rec, r)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/ui/?ok=") {
		t.Fatalf("form remove host: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if st, w := u.call(fromAdmin, "GET", "/v1/hosts/h1", "", nil); st != 404 {
		t.Fatalf("host after form remove: %d %s", st, w.Body.String())
	}
	if st, w := u.call(fromAdmin, "POST", "/ui/tenants/"+u.tn+"/delete", "", map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"}); st != 403 || errCode(w) != "forbidden" {
		t.Fatalf("cross-site form: %d %s", st, w.Body.String())
	}
}

// TestUISpecConformance drives every /v1 call the UI makes through the
// kin-openapi validator (the UI is a /v1 client, so the test gives it a
// transport onto a validated API) with populated data, as the admin:
// every page, then every form mutation.
func TestUISpecConformance(t *testing.T) {
	t.Parallel()
	u := newUIEnv(t)
	dep, root, kid := u.seed(t)
	u.e.c.ok("DELETE", "/v1/objects/"+kid+killQuery(false, "spec"), nil, nil)
	val, err := v1.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	api := val.Middleware(specReporter(t))(u.srv.Handler())
	uiH := ui.Handler(&http.Client{Transport: uiTransport{api}}, uiBaseURL)
	admin := Principal{Kind: "user", Login: "alice@example.com", NodeID: "nADM", Caps: []string{AdminCap}}
	for _, p := range []string{"/ui/", "/ui/objects/" + root, "/ui/objects/" + kid, "/ui/deployments/" + dep, "/ui/hosts/h1", "/ui/tenants/" + u.tn, "/ui/tenants/" + u.tn + "/workloads/w/logs", "/ui/tenants/" + u.tn + "/workloads/w/logs?source=console", "/ui/objects/" + root + "/logs?errors=1&status=5xx", "/ui/audit", "/ui/audit?object=" + root + "&limit=2", "/ui/audit?tenant=" + u.tn + "&kind=create&workload=w", "/ui/objects", "/ui/objects?status=ready&tag=x", "/ui/approvals"} {
		r := httptest.NewRequest("GET", "http://tw-mgmt.t.ts.net"+p, nil)
		r = r.WithContext(WithPrincipal(r.Context(), admin))
		w := httptest.NewRecorder()
		uiH.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	for _, f := range []struct{ path, form string }{
		{"/ui/objects/" + root + "/kill", "cascade=true&reason=spec"},
		{"/ui/deployments/" + dep + "/undeploy", "reason=spec"},
		{"/ui/hosts/h1/remove", "drain=true"},
		{"/ui/tenants/" + u.tn + "/delete", "force=true"},
	} {
		r := httptest.NewRequest("POST", "http://tw-mgmt.t.ts.net"+f.path, strings.NewReader(f.form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(WithPrincipal(r.Context(), admin))
		w := httptest.NewRecorder()
		uiH.ServeHTTP(w, r)
		if loc := w.Header().Get("Location"); w.Code != 303 || !strings.Contains(loc, "ok=") {
			t.Errorf("POST %s: %d %q", f.path, w.Code, loc)
		}
	}
}
