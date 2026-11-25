package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// transportHeaders are what Go's HTTP client adds on its own (Content-Length:
// 0 on a PUT with no body); they are part of HTTP, not of the tailworks API.
var transportHeaders = map[string]bool{"Accept-Encoding": true, "Content-Length": true, "User-Agent": true}

// documentedHeaders are request headers api/openapi.yaml documents for every
// operation rather than per operation: Accept (content negotiation) and
// Tw-Actor (the actor on the local-dev socket; see the spec's "Audit"
// paragraph).
var documentedHeaders = map[string]bool{"Accept": true, "Tw-Actor": true}

// TestUsesOnlyPublicAPI drives every page and every form, then checks each
// request the UI sent to the API against api/openapi.yaml: the operation
// (method and path) exists, every query parameter is declared for it, and
// every header is declared or documented. This keeps the UI extractable:
// it depends on nothing a separate process couldn't use.
func TestUsesOnlyPublicAPI(t *testing.T) {
	doc, err := v1.Spec()
	if err != nil {
		t.Fatal(err)
	}
	// The spec's operations as ServeMux patterns, so matching a request is
	// the standard library's job.
	ops := http.NewServeMux()
	queryParams := map[string]map[string]bool{}
	headerParams := map[string]map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			pattern := method + " " + path
			ops.Handle(pattern, http.NotFoundHandler())
			queryParams[pattern], headerParams[pattern] = map[string]bool{}, map[string]bool{}
			for _, p := range append(item.Parameters, op.Parameters...) {
				switch p.Value.In {
				case "query":
					queryParams[pattern][p.Value.Name] = true
				case "header":
					headerParams[pattern][http.CanonicalHeaderKey(p.Value.Name)] = true
				}
			}
		}
	}

	api := populatedAPI()
	h := serve(t, api)
	for _, p := range pages {
		if b := get(t, h, p).Body.String(); strings.Contains(b, `"/v1/`) {
			t.Errorf("%s points the browser at the API directly", p)
		}
	}
	for _, f := range [][2]string{
		{"/ui/objects/obj_root/kill", "cascade=true&reason=r"},
		{"/ui/deployments/dep_1/undeploy", "reason=r"},
		{"/ui/hosts/h1/cordon", ""},
		{"/ui/hosts/h1/cordon", "uncordon=true"},
		{"/ui/hosts/h1/remove", "drain=true"},
		{"/ui/tenants/tn_1/delete", "force=true"},
	} {
		r := httptest.NewRequest("POST", "http://tw-mgmt.t.ts.net"+f[0], strings.NewReader(f[1]))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.reqs) < 20 {
		t.Fatalf("only %d API requests recorded; the walk above didn't run", len(api.reqs))
	}
	seen := map[string]bool{}
	for _, r := range api.reqs {
		_, pattern := ops.Handler(r)
		if pattern == "" {
			t.Errorf("%s %s: no such operation in api/openapi.yaml", r.Method, r.URL.Path)
			continue
		}
		seen[pattern] = true
		for name := range r.URL.Query() {
			if !queryParams[pattern][name] {
				t.Errorf("%s: query parameter %q is not declared for %s", r.URL.RequestURI(), name, pattern)
			}
		}
		for name := range r.Header {
			if !transportHeaders[name] && !documentedHeaders[name] && !headerParams[pattern][name] {
				t.Errorf("%s %s: header %q is not documented", r.Method, r.URL.Path, name)
			}
		}
	}
	t.Logf("the UI used %d operations", len(seen))
}

// TestAdmission: the UI admits only admins that are not hosts, judged by
// GET /v1/whoami, with no help from whoever mounts it.
func TestAdmission(t *testing.T) {
	for name, who := range map[string]map[string]any{
		"non-admin user": {"kind": "user", "login": "bob@x", "admin": false, "actor": "bob@x", "mgmt": map[string]any{}},
		"admin host":     {"kind": "host", "hostID": "h1", "admin": true, "actor": "host:h1", "mgmt": map[string]any{}},
	} {
		api := populatedAPI()
		api.routes["GET /v1/whoami"] = who
		h := serve(t, api)
		for _, p := range []string{"/ui/", "/ui/objects/obj_root", "/ui/audit"} {
			if w := get(t, h, p); w.Code != 403 || w.Header().Get("Tw-Error") != "forbidden" || strings.Contains(w.Body.String(), "root-abc") {
				t.Errorf("%s %s: %d %s", name, p, w.Code, w.Header().Get("Tw-Error"))
			}
		}
		r := httptest.NewRequest("POST", "http://tw-mgmt.t.ts.net/ui/objects/obj_root/kill", strings.NewReader("reason=r"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || strings.HasPrefix(api.last(), "DELETE") {
			t.Errorf("%s kill form: %d, last API call %s", name, w.Code, api.last())
		}
		if w := get(t, h, "/ui/static/app.css"); w.Code != 200 {
			t.Errorf("%s static asset: %d", name, w.Code)
		}
	}
	// An API that refuses whoami (no identity) is that refusal, not a page.
	api := populatedAPI()
	api.routes["GET /v1/whoami"] = &apiError{Status: 401, Code: "no_identity", Message: "unauthenticated"}
	if w := get(t, serve(t, api), "/ui/"); w.Code != 401 {
		t.Errorf("no identity: %d", w.Code)
	}
}
