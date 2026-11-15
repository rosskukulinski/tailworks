package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake /v1: canned JSON per "METHOD path" (query stripped), typed errors
// otherwise. The package cannot import mgmt (mgmt mounts it), so the
// templates are exercised here against the JSON contract directly; the
// real store is covered by mgmt/ui_integration_test.go.
type fakeAPI struct {
	routes map[string]any // "GET /v1/objects" → body (status 200), or *apiError
	mu     sync.Mutex
	calls  []string
	reqs   []*http.Request // every request as received, for TestUsesOnlyPublicAPI
}

func (f *fakeAPI) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// serve runs f as a real HTTP server and returns the UI pointed at it: the
// UI reaches the API only over HTTP, as it would from its own process.
func serve(t *testing.T, f *fakeAPI) http.Handler {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return Handler(ts.Client(), ts.URL)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	f.reqs = append(f.reqs, r.Clone(context.Background()))
	f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	v, ok := f.routes[key]
	if !ok {
		w.Header().Set("Twd-Error", "not_found")
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "message": key + " is not a route", "doc_url": "https://example/errors#not_found", "trace_id": "t1"})
		return
	}
	if e, ok := v.(*apiError); ok {
		body := map[string]any{"code": e.Code, "message": e.Message, "doc_url": e.DocURL, "trace_id": "t1"}
		if e.Tombstone != nil {
			body["tombstone"] = e.Tombstone
		}
		w.Header().Set("Twd-Error", e.Code)
		w.WriteHeader(e.Status)
		json.NewEncoder(w).Encode(body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func pageOf(items ...any) map[string]any {
	if items == nil {
		items = []any{}
	}
	return map[string]any{"items": items, "nextCursor": nil}
}

// emptyAPI: a fresh mgmt with nothing in it.
func emptyAPI() *fakeAPI {
	return &fakeAPI{routes: map[string]any{
		"GET /v1/whoami":             map[string]any{"kind": "user", "login": "a@x", "admin": true, "actor": "a@x", "mgmt": map[string]any{"dnsName": "twd-mgmt.t.ts.net", "tailnet": "t.ts.net"}},
		"GET /v1/status":             map[string]any{"ok": true, "transport": "tsnet", "listeners": []string{"tsnet:80"}, "hosts": map[string]int{"total": 0, "live": 0}, "tenants": 0, "objects": 0, "version": "v0"},
		"GET /v1/hosts":              pageOf(),
		"GET /v1/tenants":            pageOf(),
		"GET /v1/deployments":        pageOf(),
		"GET /v1/objects":            pageOf(),
		"GET /v1/audit":              map[string]any{"items": []any{}, "nextCursor": nil, "chains": []any{}, "verified": true},
		"GET /v1/audit/verification": map[string]any{"ok": true, "chains": []any{}, "verifiedAt": 1},
	}}
}

// populatedAPI: one of everything, with a root/child lineage, a killed
// object (410 on GET) and a tenant-written audit note.
func populatedAPI() *fakeAPI {
	f := emptyAPI()
	now := time.Now().UnixMilli()
	hostRec := map[string]any{"hostID": "h1", "capacityNodes": 10, "liveNodes": 2, "tiers": []string{"process", "vm"}, "lastSeen": now - 4000, "createdAt": now - 100000,
		"status": "live", "nodeID": "nHOST1", "dnsName": "twd-host-h1.t.ts.net", "push": map[string]any{"port": 9443}, "leases": []any{map[string]any{"namespace": "tn_1/w", "holder": "h1", "epoch": 1, "expiresAt": now + 5000}}}
	tenantRec := map[string]any{"tenantID": "tn_1", "tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientID": "kabc", "oauthClientFile": "/etc/twd/oauth.env",
		"registrationsPerMinute": 30, "maxObjects": 100, "createdAt": now - 200000, "mintQueue": 0, "effectiveRegistrationsPerMinute": 30, "oboPublicKey": "pk"}
	dep := map[string]any{"deploymentID": "dep_1", "tenantID": "tn_1", "workload": "w", "host": "h1", "epoch": 1, "status": "running", "deployedBy": "ross@x",
		"createdAt": now - 90000, "namespaces": []any{map[string]any{"class": "A", "tags": []string{"tag:a"}, "agent": true, "ttl": "10m"}},
		"endpoint": map[string]any{"objectID": "obj_w", "node": "w-h1", "magicDNS": "w-h1.t.ts.net", "status": "ready"}}
	root := map[string]any{"objectID": "obj_root", "tenantID": "tn_1", "workload": "w", "namespace": "A", "objectName": "root", "hostname": "root-abc",
		"deploymentID": "dep_1", "lease": "tn_1/w", "tags": []string{"tag:a"}, "root": "obj_root", "depth": 0, "status": "ready", "nodeID": "7", "stableNodeID": "nROOTSTABLE0001",
		"magicDNS": "root-abc.t.ts.net", "actingFor": "ross@x", "createdAt": now - 80000, "updatedAt": now - 70000, "lineage": []string{"obj_root"}, "children": []string{"obj_kid", "obj_dead"},
		"caps": map[string]any{"twd.dev/cap/x": []any{map[string]any{"maxUsd": 1}}}}
	kid := map[string]any{"objectID": "obj_kid", "tenantID": "tn_1", "workload": "w", "namespace": "A", "objectName": "kid", "hostname": "kid-def",
		"deploymentID": "dep_1", "lease": "tn_1/w", "tags": []string{"tag:a"}, "parent": "obj_root", "root": "obj_root", "depth": 1, "status": "pending", "ttl": 600,
		"createdAt": now - 60000, "updatedAt": now - 60000, "lineage": []string{"obj_root", "obj_kid"}}
	dead := map[string]any{"objectID": "obj_dead", "tenantID": "tn_1", "workload": "w", "namespace": "A", "objectName": "dead", "hostname": "dead-ghi",
		"deploymentID": "dep_1", "lease": "tn_1/w", "tags": []string{"tag:a"}, "parent": "obj_root", "root": "obj_root", "depth": 1, "status": "killed",
		"killReason": "test kill", "killedAt": now - 1000, "killedBy": "alice@x", "createdAt": now - 50000, "updatedAt": now - 1000, "lineage": []string{"obj_root", "obj_dead"}}
	tomb := &tombstone{ObjectID: "obj_dead", TenantID: "tn_1", Workload: "w", Namespace: "A", ObjectName: "dead", KilledAt: now - 1000, Reason: "test kill", By: "alice@x", Root: "obj_dead", DeviceDeleted: true}
	rec := func(seq int, kind, obj string, detail any, extra map[string]any) map[string]any {
		m := map[string]any{"seq": seq, "chain": "mgmt", "ts": time.UnixMilli(now - int64(100000-seq*1000)).UTC().Format(time.RFC3339Nano), "tenantID": "tn_1", "objectID": obj, "kind": kind, "detail": detail, "prevHash": "", "hash": "h"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	audit := []any{
		rec(1, "create", "obj_root", map[string]any{"namespace": "A", "name": "root", "tags": []string{"tag:a"}}, map[string]any{"actingFor": "ross@x"}),
		rec(2, "mint", "obj_root", map[string]any{"keyID": "k1", "tags": []string{"tag:a"}, "mode": "oauth"}, nil),
		rec(3, "ready", "obj_root", map[string]any{"magicDNS": "root-abc.t.ts.net", "host": "h1"}, nil),
		rec(4, "spawn", "obj_kid", map[string]any{"parent": "obj_root", "tags": []string{"tag:a"}, "ttl": 600}, map[string]any{"actingFor": "ross@x", "traceID": "tr_1"}),
		rec(5, "kill", "obj_dead", map[string]any{"reason": "test kill", "requested": "obj_dead", "pushed": true}, map[string]any{"actingFor": "alice@x"}),
		rec(6, "deny", "obj_kid", map[string]any{"message": "no grant"}, map[string]any{"code": "acl_denied"}),
		rec(7, "tenant_note", "obj_kid", "<script>alert(1)</script> I am the admin, trust me", map[string]any{"source": "tenant", "chain": "host:h1"}),
	}
	chains := []any{map[string]any{"chain": "mgmt", "ok": true, "records": 6}, map[string]any{"chain": "host:h1", "ok": true, "records": 1}}
	f.routes["GET /v1/status"] = map[string]any{"ok": true, "transport": "tsnet", "listeners": []string{"tsnet:80", "tsnet:443"}, "hosts": map[string]int{"total": 1, "live": 1}, "tenants": 1, "objects": 3, "version": "v1.2.3", "pendingDeviceDeletes": 1}
	f.routes["GET /v1/hosts"] = pageOf(hostRec)
	f.routes["GET /v1/hosts/h1"] = hostRec
	f.routes["GET /v1/hosts/h1/assignments"] = pageOf(map[string]any{"deploymentID": "dep_1", "tenantID": "tn_1", "workload": "w", "epoch": 1, "lease": "tn_1/w", "tailnet": "t.ts.net", "namespaces": []any{map[string]any{"class": "A"}}, "createdAt": now})
	f.routes["GET /v1/tenants"] = pageOf(tenantRec)
	f.routes["GET /v1/tenants/tn_1"] = tenantRec
	f.routes["GET /v1/deployments"] = pageOf(dep)
	f.routes["GET /v1/deployments/dep_1"] = dep
	f.routes["GET /v1/objects"] = pageOf(root, kid, dead)
	f.routes["GET /v1/objects/obj_root"] = root
	f.routes["GET /v1/objects/obj_kid"] = kid
	f.routes["GET /v1/objects/obj_dead"] = &apiError{Status: 410, Code: "killed", Message: "object obj_dead was killed (test kill)", Tombstone: tomb}
	f.routes["GET /v1/objects/obj_root/certificate"] = map[string]any{"stableNodeID": "nROOTSTABLE0001", "status": "ready", "cert": "c", "payload": map[string]any{"exp": 1}, "oboPublicKey": "pk"}
	f.routes["GET /v1/audit"] = map[string]any{"items": audit, "nextCursor": "c2", "chains": chains, "verified": true}
	f.routes["GET /v1/audit/verification"] = map[string]any{"ok": false, "chains": []any{map[string]any{"chain": "mgmt", "ok": false, "records": 6, "brokenAt": 4, "error": "hash mismatch"}}, "verifiedAt": now}
	f.routes["DELETE /v1/objects/obj_root"] = map[string]any{"killed": []string{"obj_root", "obj_kid"}, "devicesDeleted": []string{}}
	f.routes["DELETE /v1/deployments/dep_1"] = map[string]any{"killed": []string{"obj_root"}, "devicesDeleted": []string{}}
	f.routes["DELETE /v1/hosts/h1"] = &apiError{Status: 409, Code: "host_holds_leases", Message: "host h1 holds 1 lease", DocURL: "https://example/errors#host_holds_leases"}
	return f
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "http://twd-mgmt.t.ts.net"+path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var pages = []string{"/ui/", "/ui/objects", "/ui/objects?tenant=tn_1&workload=w&status=ready&tag=a&caller=ross&page=2", "/ui/approvals", "/ui/approvals?tenant=tn_1&all=1", "/ui/audit?tenant=tn_1&kind=kill&workload=w&cursor=c1", "/ui/objects/obj_root", "/ui/objects/obj_kid", "/ui/objects/obj_dead", "/ui/deployments/dep_1", "/ui/hosts/h1", "/ui/tenants/tn_1", "/ui/audit", "/ui/audit?object=obj_root", "/ui/audit?human=ross@x", "/ui/audit?trace=tr_1&limit=5"}

// TestPagesRenderEmpty: every page template executes over an empty mgmt
// (the overview and audit list are 200; records that do not exist are the
// typed 404 page, also from a template).
func TestPagesRenderEmpty(t *testing.T) {
	h := serve(t, emptyAPI())
	for _, p := range pages {
		w := get(t, h, p)
		wantStatus := 200
		if strings.Contains(p, "/objects/") || strings.Contains(p, "/deployments/") || strings.Contains(p, "/hosts/") || strings.Contains(p, "/tenants/") {
			wantStatus = 404
		}
		if w.Code != wantStatus || !strings.Contains(w.Body.String(), "</html>") || strings.Contains(w.Body.String(), "template ") {
			t.Errorf("%s: %d (want %d)\n%s", p, w.Code, wantStatus, w.Body.String())
		}
		if w.Code == 404 && (w.Header().Get("Twd-Error") != "not_found" || !strings.Contains(w.Body.String(), "not_found")) {
			t.Errorf("%s: 404 page lacks the typed code", p)
		}
	}
	for _, want := range []string{"no hosts", "no tenants", "no deployments", "no objects", "no audit records", "no tenants"} {
		if body := get(t, h, "/ui/").Body.String(); !strings.Contains(body, want) {
			t.Errorf("empty overview lacks %q", want)
		}
	}
}

func TestPagesRenderPopulated(t *testing.T) {
	h := serve(t, populatedAPI())
	for _, p := range pages {
		w := get(t, h, p)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "</html>") {
			t.Errorf("%s: %d\n%s", p, w.Code, w.Body.String())
		}
	}
	body := get(t, h, "/ui/").Body.String()
	for _, want := range []string{
		"twd-mgmt.t.ts.net", "v1.2.3", "tsnet:80, tsnet:443", // mgmt self
		`href="/ui/hosts/h1"`, "tsnet:9443", "process, vm", "2/10", // hosts
		`href="/ui/tenants/tn_1"`, "oauth", // tenants
		`href="/ui/deployments/dep_1"`, "ross@x", `href="http://w-h1.t.ts.net/"`, // deployments
		`href="/ui/objects/obj_root"`, "root-abc", "kid-def", `class="elbow"`, "10m0s", "nROOTSTABLE0…", // objects tree
		"NOT VERIFIED", "BROKEN at 4 (hash mismatch)", // verification
		"key k1 tags [&#34;tag:a&#34;] (oauth)", "node root-abc.t.ts.net on h1", // audit describe
		"tenant note (unverified, written by tenant code): &lt;script&gt;", // RT-02: escaped, labelled
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("tenant note rendered unescaped")
	}
	// The tree indents the children under the root (one indent span each).
	rootAt, kidAt := strings.Index(body, `title="obj_root"`), strings.Index(body, `title="obj_kid"`)
	if rootAt < 0 || kidAt < rootAt {
		t.Errorf("tree order: root %d kid %d", rootAt, kidAt)
	}
	// Object page: lineage, caps, cert, and the kill form (confirm, then POST to the UI).
	ob := get(t, h, "/ui/objects/obj_kid").Body.String()
	for _, want := range []string{`href="/ui/objects/obj_root"`, "(this object)", `data-confirm="Kill kid-def (obj_kid)?`, `action="/ui/objects/obj_kid/kill"`, `name="cascade"`, `name="reason"`} {
		if !strings.Contains(ob, want) {
			t.Errorf("object page lacks %q", want)
		}
	}
	rb := get(t, h, "/ui/objects/obj_root").Body.String()
	for _, want := range []string{"twd.dev/cap/x", "Node certificate", "nROOTSTABLE0001", "kid-def", "dead-ghi"} {
		if !strings.Contains(rb, want) {
			t.Errorf("root page lacks %q", want)
		}
	}
	// Killed object: tombstone, no kill form.
	db := get(t, h, "/ui/objects/obj_dead").Body.String()
	for _, want := range []string{"killed", "test kill", "alice@x", "deleted"} {
		if !strings.Contains(db, want) {
			t.Errorf("tombstone page lacks %q", want)
		}
	}
	if strings.Contains(db, `action="/ui/objects/obj_dead/kill"`) {
		t.Error("tombstone page offers a kill form")
	}
	// Deployment, host and tenant pages carry their mutation forms.
	if b := get(t, h, "/ui/deployments/dep_1").Body.String(); !strings.Contains(b, `action="/ui/deployments/dep_1/undeploy"`) || !strings.Contains(b, "w-h1.t.ts.net") {
		t.Error("deployment page lacks the undeploy form or endpoint")
	}
	if b := get(t, h, "/ui/hosts/h1").Body.String(); !strings.Contains(b, `action="/ui/hosts/h1/remove"`) || !strings.Contains(b, `name="drain"`) || !strings.Contains(b, "dep_1") {
		t.Error("host page lacks the remove form or assignments")
	}
	if b := get(t, h, "/ui/tenants/tn_1").Body.String(); !strings.Contains(b, `action="/ui/tenants/tn_1/delete"`) || !strings.Contains(b, `name="force"`) || !strings.Contains(b, "/etc/twd/oauth.env") {
		t.Error("tenant page lacks the delete form or config")
	}
	// Audit page: filters echo back, paging link carries them, chains shown.
	ab := get(t, h, "/ui/audit?object=obj_root&limit=5").Body.String()
	for _, want := range []string{`value="obj_root"`, "cursor=c2", "object=obj_root", "limit=5", "signature ok", "~tenant_note"} {
		if !strings.Contains(ab, want) {
			t.Errorf("audit page lacks %q", want)
		}
	}
}

// TestFormFallbacks: the no-JS POSTs run the /v1 call in-process and
// redirect back with the outcome (success and a typed refusal).
func TestFormFallbacks(t *testing.T) {
	api := populatedAPI()
	h := serve(t, api)
	post := func(path, form string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://twd-mgmt.t.ts.net"+path, strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := post("/ui/objects/obj_root/kill", "cascade=true&reason=why+not")
	if w.Code != 303 || w.Header().Get("Location") != "/ui/objects/obj_root?ok=kill+accepted" {
		t.Fatalf("kill form: %d %q", w.Code, w.Header().Get("Location"))
	}
	if last := api.last(); last != "DELETE /v1/objects/obj_root?cascade=true&reason=why+not" {
		t.Fatalf("kill call: %s", last)
	}
	w = post("/ui/deployments/dep_1/undeploy", "")
	if w.Code != 303 || api.last() != "DELETE /v1/deployments/dep_1" {
		t.Fatalf("undeploy form: %d %s", w.Code, api.last())
	}
	w = post("/ui/hosts/h1/remove", "")
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "err=host_holds_leases") {
		t.Fatalf("remove form refusal: %d %q", w.Code, w.Header().Get("Location"))
	}
	// The flash renders on the next page.
	if b := get(t, h, "/ui/?err=host_holds_leases%3A+host+h1+holds+1+lease").Body.String(); !strings.Contains(b, `class="flash bad"`) || !strings.Contains(b, "host_holds_leases: host h1 holds 1 lease") {
		t.Error("flash not rendered")
	}
}

func TestStaticAndHeaders(t *testing.T) {
	h := serve(t, emptyAPI())
	for _, p := range []string{"/ui/static/app.css", "/ui/static/app.js", "/ui/"} {
		w := get(t, h, p)
		if w.Code != 200 {
			t.Fatalf("%s: %d", p, w.Code)
		}
		for k, want := range map[string]string{
			"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'",
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
			"X-Frame-Options":         "DENY",
			"Cache-Control":           "no-store",
		} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s %s = %q, want %q", p, k, got, want)
			}
		}
	}
	if ct := get(t, h, "/ui/static/app.js").Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("app.js content type %q", ct)
	}
	if ct := get(t, h, "/ui/static/app.css").Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Errorf("app.css content type %q", ct)
	}
	// The CSP forbids inline script and style: the templates must not use any.
	for _, p := range []string{"/ui/", "/ui/audit"} {
		b := get(t, h, p).Body.String()
		if strings.Contains(b, "<script>") || strings.Contains(b, " style=\"") || strings.Contains(b, "onclick=") {
			t.Errorf("%s has inline script/style (blocked by the CSP)", p)
		}
	}
	if w := get(t, h, "/ui/static/../ui.go"); w.Code == 200 && strings.Contains(w.Body.String(), "package ui") {
		t.Fatal("static server escaped its directory")
	}
	if w := get(t, h, "/ui"); w.Code != 301 || w.Header().Get("Location") != "/ui/" {
		t.Errorf("/ui: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestHelpers(t *testing.T) {
	now := time.UnixMilli(1_000_000_000)
	for ms, want := range map[int64]string{0: "never", now.UnixMilli() - 5_000: "5s", now.UnixMilli() - 90_000: "1m", now.UnixMilli() - 7_200_000: "2h", now.UnixMilli() - 3*86_400_000: "3d"} {
		if got := ago(ms, now); got != want {
			t.Errorf("ago(%d) = %q, want %q", ms, got, want)
		}
	}
	// since: the instant goes in datetime= (app.js recomputes the text), so a
	// live refresh doesn't see a change just because time passed.
	if got := since(now.UnixMilli()-90_000, now); got != `<time datetime="1970-01-12T13:45:10Z" title="1970-01-12 13:45:10Z" data-ago>1m ago</time>` {
		t.Errorf("since = %s", got)
	}
	if since(0, now) != "never" {
		t.Error("since(0)")
	}
	if short("abcdefghijklmnopqrstuvwxyz") != "abcdefghijkl…" || short("short") != "short" {
		t.Error("short")
	}
	for st, want := range map[string]string{"ready": "ok", "live": "ok", "pending": "warn", "minted": "warn", "stale": "warn", "killed": "off", "stopped": "off", "superseded": "off", "failed": "bad", "crashloop": "bad", "other": "neutral"} {
		if badge(st) != want {
			t.Errorf("badge(%s) = %s", st, badge(st))
		}
	}
	objs := []object{{ObjectID: "c", Parent: "a", CreatedAt: 3}, {ObjectID: "a", CreatedAt: 1}, {ObjectID: "b", Parent: "a", CreatedAt: 2}, {ObjectID: "orphan", Parent: "gone", CreatedAt: 0}, {ObjectID: "d", Parent: "b", CreatedAt: 4}}
	var order []string
	var levels []int
	for _, o := range buildTree(objs) {
		order = append(order, o.ObjectID)
		levels = append(levels, o.Level)
	}
	if strings.Join(order, ",") != "orphan,a,b,d,c" || levels[0] != 0 || levels[1] != 0 || levels[2] != 1 || levels[3] != 2 || levels[4] != 1 {
		t.Errorf("tree %v %v", order, levels)
	}
	if sub := subtree(objs, "b"); len(sub) != 2 || sub[0].ObjectID != "b" || sub[1].ObjectID != "d" || sub[1].Level != 1 {
		t.Errorf("subtree %+v", sub)
	}
}
