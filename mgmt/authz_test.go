package mgmt

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Regression tests for the 2026-11 security review findings (H2, H3, M1,
// M2, M3, M4): host principals over the WhoIs gate (not the local-dev
// socket), two hosts, two workloads, every exchange spec-validated.

type authzEnv struct {
	*testEnv
	t    *testing.T
	h    http.Handler
	tn   string
	tn2  string
	objA string // workload w (host h1), class A
	objB string // workload v (host h2), class B
}

const (
	nodeH1  = "100.64.0.1:1"
	nodeH2  = "100.64.0.2:1"
	nodeAdm = "100.64.0.6:1"
)

func newAuthzEnv(t *testing.T) *authzEnv {
	e := newTestEnv(t)
	srv := &Server{M: e.m, Self: SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net"}}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case nodeH1:
			return WhoIsInfo{NodeID: "nHOST1", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case nodeH2:
			return WhoIsInfo{NodeID: "nHOST2", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case nodeAdm:
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	val, err := v1.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	a := &authzEnv{testEnv: e, t: t, h: srv.TailnetHandler(whois, val.Middleware(specReporter(t))(srv.Handler()))}
	a.tn = v1Tenant(t, e, "t.ts.net", nil)
	a.tn2 = v1Tenant(t, e, "other.ts.net", nil)
	a.addHost("h1", nodeH1)
	a.addHost("h2", nodeH2)
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": a.tn, "workload": "w", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, nil)
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": a.tn, "workload": "v", "host": "h2", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "B", Tags: []string{"tag:ttest"}}}}, nil)
	var o map[string]any
	if st, _ := a.call(nodeH1, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "workload": "w", "namespace": "A", "objectName": "a"}, &o); st != 202 {
		t.Fatalf("create a: %d %v", st, o)
	}
	a.objA = o["objectID"].(string)
	if st, _ := a.call(nodeH2, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "workload": "v", "namespace": "B", "objectName": "b"}, &o); st != 202 {
		t.Fatalf("create b: %d %v", st, o)
	}
	a.objB = o["objectID"].(string)
	return a
}

// call sends a request as the WhoIs identity at from. 2xx bodies decode
// into out; the typed error body is returned otherwise.
func (a *authzEnv) call(from, method, path string, body any, out any) (int, map[string]any) {
	a.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "http://tw-mgmt"+path, rd)
	r.RemoteAddr = from
	if body != nil {
		ct := "application/json"
		if method == "PATCH" {
			ct = "application/merge-patch+json"
		}
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, r)
	var eb map[string]any
	if w.Code/100 == 2 {
		if out != nil {
			json.Unmarshal(w.Body.Bytes(), out)
		}
		return w.Code, nil
	}
	json.Unmarshal(w.Body.Bytes(), &eb)
	return w.Code, eb
}

func (a *authzEnv) fail(from, method, path string, body any, status int, code string) map[string]any {
	a.t.Helper()
	st, eb := a.call(from, method, path, body, nil)
	if st != status || eb["code"] != code {
		a.t.Fatalf("%s %s as %s: got %d %v, want %d %s", method, path, from, st, eb, status, code)
	}
	return eb
}

// addHost is tw host add + the host's first registration with the secret.
func (a *authzEnv) addHost(id, from string) string {
	a.t.Helper()
	var hj struct{ RegistrationSecret string }
	a.c.ok("POST", "/v1/hosts", map[string]any{"hostID": id, "tenantID": a.tn}, &hj)
	if st, eb := a.call(from, "PUT", "/v1/hosts/"+id, map[string]any{"capacityNodes": 10, "tiers": []string{"process"}, "registrationSecret": hj.RegistrationSecret}, nil); st != 201 {
		a.t.Fatalf("bind %s: %d %v", id, st, eb)
	}
	return hj.RegistrationSecret
}

// TestAuthzH2ReadyIsFenced: the ready report is fenced on the lease
// (holder and epoch) for every caller, and epoch is mandatory. A host
// that does not hold the lease cannot mark another host's object ready,
// pick its node IDs, or make mgmt delete a device.
func TestAuthzH2ReadyIsFenced(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	ready := func(epoch any, node string) map[string]any {
		b := map[string]any{"status": "ready", "nodeID": "1", "stableNodeID": node, "magicDNS": a.objA + ".t.ts.net"}
		if epoch != nil {
			b["epoch"] = epoch
		}
		return b
	}
	// Another host: outside its leases, before any fence.
	a.fail(nodeH2, "PATCH", "/v1/objects/"+a.objA, ready(1, "nEVIL"), 403, "forbidden")
	// The holder with a stale epoch.
	a.fail(nodeH1, "PATCH", "/v1/objects/"+a.objA, ready(2, "nA"), 409, "lease_lost")
	// The local-dev socket naming another host: fenced on the holder with
	// an epoch, and even without one (RT3-02: a host that omits the epoch
	// is still bound to the holder).
	a.asHost("h2").fail("PATCH", "/v1/objects/"+a.objA, ready(1, "nEVIL"), 409, "lease_lost")
	a.asHost("h2").fail("PATCH", "/v1/objects/"+a.objA, ready(nil, "nEVIL"), 409, "lease_lost")
	if o, _ := a.m.store.GetObject(context.Background(), a.objA); o.Status == StatusReady || o.StableNodeID != "" {
		t.Fatalf("object touched by a refused ready: %+v", o)
	}
	// The holder at the right epoch.
	if st, eb := a.call(nodeH1, "PATCH", "/v1/objects/"+a.objA, ready(1, "nA"), nil); st != 200 {
		t.Fatalf("holder ready: %d %v", st, eb)
	}
	// Killed while joining: only the lease holder's report deletes the
	// device that appeared; a non-holder's report (even with the right
	// epoch) is lease_lost and deletes nothing.
	var o map[string]any
	if st, _ := a.call(nodeH1, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "workload": "w", "namespace": "A", "objectName": "late"}, &o); st != 202 {
		t.Fatal("create late")
	}
	late := o["objectID"].(string)
	a.c.ok("DELETE", "/v1/objects/"+late+"?reason=test", nil, nil)
	a.asHost("h2").fail("PATCH", "/v1/objects/"+late, map[string]any{"status": "ready", "epoch": 1, "stableNodeID": "nEVIL"}, 409, "lease_lost")
	if d := a.ts.devicesDeleted(); len(d) != 0 {
		t.Fatalf("a non-holder made mgmt delete devices: %v", d)
	}
	a.fail(nodeH1, "PATCH", "/v1/objects/"+late, map[string]any{"status": "ready", "epoch": 1, "stableNodeID": "nLATE"}, 410, "killed")
	if d := a.ts.devicesDeleted(); len(d) != 1 || d[0] != "nLATE" {
		t.Fatalf("holder's late device not deleted: %v", d)
	}
	// Remint is fenced the same way.
	a.fail(nodeH2, "POST", "/v1/objects/"+a.objA+"/credentials?epoch=1&reason=rejoin_rejected", nil, 403, "forbidden")
	a.asHost("h2").fail("POST", "/v1/objects/"+a.objA+"/credentials?epoch=1&reason=rejoin_rejected", nil, 409, "lease_lost")
}

// TestAuthzH3ReservedTags: a bundle or namespace may not ask for tailworks' own
// tags, and every tag must be well-formed; deploy and preflight agree.
func TestAuthzH3ReservedTags(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	deploy := func(tag string, dryRun bool) (int, map[string]any) {
		path := "/v1/deployments"
		if dryRun {
			path += "?dryRun=true"
		}
		return a.c.do("POST", path, map[string]any{"tenantID": a.tn, "workload": "x", "bundle": noConfigBundle(t),
			"namespaces": []ClassConfig{{Class: "X", Tags: []string{tag}}}}, nil)
	}
	for _, tag := range []string{"tag:tw", "tag:tw-mgmt", "tag:tw-host", "tag:TW-HOST"} {
		for _, dry := range []bool{false, true} {
			if st, eb := deploy(tag, dry); st != 400 || eb["code"] != "reserved_tag" {
				t.Fatalf("%s dryRun=%v: %d %v", tag, dry, st, eb)
			}
		}
	}
	for _, tag := range []string{"tag:", "tag:tw-host ", "tag: x", "tag:a\tb", "tag:a\x00", "notatag", "tag:-x", "tag:x/y", "tag:ünï"} {
		if st, eb := deploy(tag, false); st != 400 || eb["code"] != "bad_request" {
			t.Fatalf("%q: %d %v", tag, st, eb)
		}
	}
	// Through the bundle's tailworks.jsonc too (the CLI path).
	for _, cfg := range []string{
		`{"tailnet": {"tags": ["tag:tw-host"]}}`,
		`{"tailnet": {"objects": {"Evil": {"tags": ["tag:tw-mgmt"]}}}}`,
	} {
		b := makeBundle(t, map[string]string{"tailworks.jsonc": cfg, "dist/index.js": "export default {}"})
		for _, path := range []string{"/v1/deployments", "/v1/deployments?dryRun=true"} {
			if st, eb := a.c.do("POST", path, map[string]any{"tenantID": a.tn, "workload": "x", "bundle": b}, nil); st != 400 || eb["code"] != "reserved_tag" {
				t.Fatalf("bundle %s via %s: %d %v", cfg, path, st, eb)
			}
		}
	}
	// A legitimate tag of the tw-… family is still fine.
	if st, eb := deploy("tag:tw-wl-x", true); st != 200 {
		t.Fatalf("tag:tw-wl-x preflight: %d %v", st, eb)
	}
	if d, err := a.m.store.ListDeployments(context.Background(), DeploymentFilter{Workload: "x"}, Page{}); err != nil || len(d) != 0 {
		t.Fatalf("a refused deploy was stored: %v %v", d, err)
	}
}

// TestAuthzM1RegistrationSecret: the join key proves tag:tw-host, the
// one-time secret proves the host ID; it rotates with each host add and
// never appears in the API or the audit log.
func TestAuthzM1RegistrationSecret(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	var hj1, hj2 struct {
		RegistrationSecret string
		AuthKey            string
	}
	a.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h3", "tenantID": a.tn}, &hj1)
	a.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h3", "tenantID": a.tn}, &hj2) // pending again: a new secret
	if hj1.RegistrationSecret == hj2.RegistrationSecret || len(hj2.RegistrationSecret) < 40 {
		t.Fatalf("secret not rotated: %q %q", hj1.RegistrationSecret, hj2.RegistrationSecret)
	}
	// h2's node is bound to h2 already: the gate refuses it as h3 before
	// any secret is looked at; an unbound node needs the current secret.
	a.fail(nodeH2, "PUT", "/v1/hosts/h3", map[string]any{"capacityNodes": 1, "tiers": []string{"process"}, "registrationSecret": hj2.RegistrationSecret}, 403, "forbidden")
	srv := &Server{M: a.m}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		return WhoIsInfo{NodeID: "nHOST3", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
	}
	h3 := srv.TailnetHandler(whois, srv.Handler())
	put := func(secret string) (int, string) {
		b := map[string]any{"capacityNodes": 1, "tiers": []string{"process"}}
		if secret != "" {
			b["registrationSecret"] = secret
		}
		j, _ := json.Marshal(b)
		r := httptest.NewRequest("PUT", "/v1/hosts/h3", bytes.NewReader(j))
		r.RemoteAddr = "100.64.0.3:1"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h3.ServeHTTP(w, r)
		var eb struct{ Code string }
		json.Unmarshal(w.Body.Bytes(), &eb)
		return w.Code, eb.Code
	}
	if st, code := put(hj1.RegistrationSecret); st != 403 || code != "host_identity_mismatch" {
		t.Fatalf("stale secret: %d %s", st, code)
	}
	if st, code := put(""); st != 403 || code != "host_identity_mismatch" {
		t.Fatalf("no secret: %d %s", st, code)
	}
	if st, _ := put(hj2.RegistrationSecret); st != 201 {
		t.Fatalf("bind: %d", st)
	}
	if st, _ := put(""); st != 200 {
		t.Fatalf("re-register once bound: %d", st)
	}
	// Nothing secret in the resource, the list, the join key answer's
	// host, or the audit log.
	var raw json.RawMessage
	a.c.ok("GET", "/v1/hosts/h3", nil, &raw)
	hash := hashRegistrationSecret(hj2.RegistrationSecret)
	for _, needle := range []string{hj1.RegistrationSecret, hj2.RegistrationSecret, hash, "ecret"} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("host resource leaks %q: %s", needle, raw)
		}
	}
	var aud struct{ Items []json.RawMessage }
	a.c.ok("GET", "/v1/audit", nil, &aud)
	for _, rec := range aud.Items {
		for _, needle := range []string{hj1.RegistrationSecret, hj2.RegistrationSecret, hash, hj1.AuthKey, hj2.AuthKey} {
			if strings.Contains(string(rec), needle) {
				t.Fatalf("audit leaks %q: %s", needle, rec)
			}
		}
	}
	if h, _ := a.m.store.GetHost(context.Background(), "h3"); h.RegistrationSecretHash != "" || h.NodeID != "nHOST3" {
		t.Fatalf("after bind: %+v", h)
	}
}

// TestAuthzM2HostObjectScope: host-role object mutations are limited to
// workloads whose lease the host holds.
func TestAuthzM2HostObjectScope(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	// Create in another host's workload (explicit, by class, by parent).
	a.fail(nodeH2, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "workload": "w", "namespace": "A", "objectName": "x"}, 409, "lease_lost")
	a.fail(nodeH2, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "namespace": "A", "objectName": "x"}, 409, "lease_lost")
	a.fail(nodeH2, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "namespace": "A", "objectName": "child", "parent": map[string]string{"objectID": a.objA}}, 409, "lease_lost")
	if objs, _ := a.m.store.ListObjects(context.Background(), ObjectFilter{TenantID: a.tn, Workload: "w"}); len(objs) != 1 {
		t.Fatalf("objects created across the lease: %d", len(objs))
	}
	// Credential, kill, remint, ready on another host's object.
	a.fail(nodeH2, "POST", "/v1/objects/"+a.objA+"/credentials?epoch=1", nil, 403, "forbidden")
	a.fail(nodeH2, "DELETE", "/v1/objects/"+a.objA+"?reason=x", nil, 403, "forbidden")
	a.fail(nodeH2, "PATCH", "/v1/objects/"+a.objA, map[string]any{"status": "ready", "epoch": 1, "stableNodeID": "nX"}, 403, "forbidden")
	// Its own workload works end to end.
	var cred map[string]any
	if st, eb := a.call(nodeH2, "POST", "/v1/objects/"+a.objB+"/credentials?epoch=1", nil, &cred); st != 201 || cred["authKey"] == "" {
		t.Fatalf("own credential: %d %v", st, eb)
	}
	if st, eb := a.call(nodeH2, "PATCH", "/v1/objects/"+a.objB, map[string]any{"status": "ready", "epoch": 1, "nodeID": "2", "stableNodeID": "nB", "magicDNS": "b.t.ts.net"}, nil); st != 200 {
		t.Fatalf("own ready: %d %v", st, eb)
	}
	if st, eb := a.call(nodeH2, "DELETE", "/v1/objects/"+a.objB+"?reason=x", nil, nil); st != 202 {
		t.Fatalf("own kill: %d %v", st, eb)
	}
	// A killed object of another host's workload is not even a tombstone
	// for this host.
	a.fail(nodeH1, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "workload": "v", "namespace": "B", "objectName": "b"}, 403, "forbidden")
}

// TestAuthzM3HostReadScope: hosts list only the tenants and objects of
// their own assignments; admins see everything.
func TestAuthzM3HostReadScope(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	var tenants struct{ Items []Tenant }
	if st, _ := a.call(nodeH1, "GET", "/v1/tenants", nil, &tenants); st != 200 || len(tenants.Items) != 1 || tenants.Items[0].ID != a.tn {
		t.Fatalf("host tenants: %d %+v", st, tenants.Items)
	}
	if st, _ := a.call(nodeAdm, "GET", "/v1/tenants", nil, &tenants); st != 200 || len(tenants.Items) != 2 {
		t.Fatalf("admin tenants: %d %d", st, len(tenants.Items))
	}
	if st, _ := a.call(nodeH1, "GET", "/v1/tenants/"+a.tn, nil, nil); st != 200 {
		t.Fatalf("own tenant: %d", st)
	}
	a.fail(nodeH1, "GET", "/v1/tenants/"+a.tn2, nil, 404, "not_found")
	var objs struct{ Items []Object }
	if st, _ := a.call(nodeH2, "GET", "/v1/objects?tenant="+a.tn, nil, &objs); st != 200 || len(objs.Items) != 1 || objs.Items[0].ID != a.objB {
		t.Fatalf("host objects: %d %+v", st, objs.Items)
	}
	if st, _ := a.call(nodeH2, "GET", "/v1/objects?limit=1", nil, &objs); st != 200 || len(objs.Items) != 1 || objs.Items[0].ID != a.objB {
		t.Fatalf("host objects paged: %d %+v", st, objs.Items)
	}
	if st, _ := a.call(nodeAdm, "GET", "/v1/objects?tenant="+a.tn, nil, &objs); st != 200 || len(objs.Items) != 2 {
		t.Fatalf("admin objects: %d %d", st, len(objs.Items))
	}
	a.fail(nodeH2, "GET", "/v1/objects/"+a.objA, nil, 403, "forbidden")
	if st, _ := a.call(nodeH2, "GET", "/v1/objects/"+a.objB, nil, nil); st != 200 {
		t.Fatalf("own object: %d", st)
	}
	// Certificates stay readable for any peer (receivers verify callers).
	if st, eb := a.call(nodeH2, "GET", "/v1/certificates/nNOBODY?tenant="+a.tn, nil, nil); st != 404 || eb["code"] != "not_found" {
		t.Fatalf("certificate lookup as a host: %d %v", st, eb)
	}
	// A host without any assignment sees no tenant and no object at all.
	var hj struct{ RegistrationSecret string }
	a.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h4", "tenantID": a.tn}, &hj)
	srv := &Server{M: a.m}
	h4 := srv.TailnetHandler(func(context.Context, string) (WhoIsInfo, error) {
		return WhoIsInfo{NodeID: "nHOST4", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
	}, srv.Handler())
	as4 := func(method, path string, body any, out any) int {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		r := httptest.NewRequest(method, "http://tw-mgmt"+path, rd)
		r.RemoteAddr = "100.64.0.4:1"
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h4.ServeHTTP(w, r)
		if out != nil {
			json.Unmarshal(w.Body.Bytes(), out)
		}
		return w.Code
	}
	if st := as4("PUT", "/v1/hosts/h4", map[string]any{"capacityNodes": 1, "tiers": []string{"process"}, "registrationSecret": hj.RegistrationSecret}, nil); st != 201 {
		t.Fatalf("bind h4: %d", st)
	}
	if st := as4("GET", "/v1/tenants", nil, &tenants); st != 200 || len(tenants.Items) != 0 {
		t.Fatalf("idle host tenants: %d %+v", st, tenants.Items)
	}
	if st := as4("GET", "/v1/objects", nil, &objs); st != 200 || len(objs.Items) != 0 {
		t.Fatalf("idle host objects: %d %+v", st, objs.Items)
	}
}

// TestAuthzM4AuditKeyPinned: the host's audit key is pinned at its first
// registration; later ones may repeat or omit it, never change it, and
// records not signed by it are refused at ingest.
func TestAuthzM4AuditKeyPinned(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	k1, k2 := base64.StdEncoding.EncodeToString(pub1), base64.StdEncoding.EncodeToString(pub2)
	reg := func(key string) map[string]any {
		b := map[string]any{"capacityNodes": 10, "tiers": []string{"process"}}
		if key != "" {
			b["auditPubKey"] = key
		}
		return b
	}
	if st, eb := a.call(nodeH1, "PUT", "/v1/hosts/h1", reg(k1), nil); st != 200 {
		t.Fatalf("pin: %d %v", st, eb)
	}
	a.fail(nodeH1, "PUT", "/v1/hosts/h1", reg(k2), 403, "host_identity_mismatch")
	for _, key := range []string{k1, ""} {
		var h Host
		if st, eb := a.call(nodeH1, "PUT", "/v1/hosts/h1", reg(key), &h); st != 200 || h.AuditPubKey != k1 {
			t.Fatalf("re-register with %q: %d %v key %q", key, st, eb, h.AuditPubKey)
		}
	}
	prev := ""
	mk := func(sign ed25519.PrivateKey) map[string]any {
		r := map[string]any{"ts": "2026-11-11T00:00:00Z", "objectID": a.objA, "kind": "allow", "prevHash": prev}
		b, _ := json.Marshal(r)
		h, _ := HashRecord(b)
		r["hash"] = h
		r["hostSig"] = base64.StdEncoding.EncodeToString(ed25519.Sign(sign, []byte(h)))
		return r
	}
	a.fail(nodeH1, "POST", "/v1/audit", map[string]any{"records": []any{mk(priv2)}}, 400, "bad_host_sig")
	var resp struct{ Chain ChainStatus }
	if st, eb := a.call(nodeH1, "POST", "/v1/audit", map[string]any{"records": []any{mk(priv1)}}, &resp); st != 201 || !resp.Chain.OK || resp.Chain.Records != 1 {
		t.Fatalf("signed ingest: %d %v %+v", st, eb, resp.Chain)
	}
	// Rotation is host rm + host add: the new record starts unpinned.
	a.c.ok("DELETE", "/v1/hosts/h1?drain=true", nil, nil)
	secret := ""
	var hj struct{ RegistrationSecret string }
	a.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h1", "tenantID": a.tn}, &hj)
	secret = hj.RegistrationSecret
	b := reg(k2)
	b["registrationSecret"] = secret
	var h Host
	if st, eb := a.call(nodeH1, "PUT", "/v1/hosts/h1", b, &h); st != 201 || h.AuditPubKey != k2 {
		t.Fatalf("after rotation: %d %v %+v", st, eb, h)
	}
}
