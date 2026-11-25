package mgmt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretValue = "sk-live-0123456789abcdef"

// TestSecretsStore: put (created, then replaced), list without values,
// delete, limits, and the custody entry is sealed (the value never appears
// in plaintext in mgmt's directory, its logs or its audit).
func TestSecretsStore(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c := e.c
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	base := "/v1/tenants/" + tn.TenantID + "/workloads/support/secrets"

	var m SecretMeta
	if st := c.okStatus("PUT", base+"/API_KEY", map[string]string{"value": secretValue}, &m); st != 201 {
		t.Fatalf("first put: %d", st)
	}
	if m.Name != "API_KEY" || m.Workload != "support" || m.CreatedAt == 0 || m.UpdatedBy == "" {
		t.Fatalf("meta %+v", m)
	}
	if st := c.okStatus("PUT", base+"/API_KEY", map[string]string{"value": secretValue + "-2"}, &m); st != 200 {
		t.Fatalf("replace: %d", st)
	}
	c.ok("PUT", base+"/OTHER", map[string]string{"value": ""}, nil) // empty values are allowed
	for _, n := range []string{"B", "C", "D"} {
		c.ok("PUT", base+"/"+n, map[string]string{"value": "x"}, nil)
	}
	var items []SecretMeta
	if next := c.items(base, &items); next != nil || len(items) != 5 || items[0].Name != "API_KEY" || items[4].Name != "OTHER" {
		t.Fatalf("list: %+v next %v", items, next)
	}
	// Paged by name.
	var p1, p2 []SecretMeta
	next := c.items(base+"?limit=3", &p1)
	if next == nil || len(p1) != 3 || p1[2].Name != "C" {
		t.Fatalf("page 1: %+v %v", p1, next)
	}
	if n2 := c.items(base+"?limit=3&cursor="+*next, &p2); n2 != nil || len(p2) != 2 || p2[0].Name != "D" {
		t.Fatalf("page 2: %+v %v", p2, n2)
	}
	// Refusals.
	c.fail("PUT", base+"/TW_X", map[string]string{"value": "x"}, 400, "bad_request")
	c.fail("PUT", base+"/__TW_X", map[string]string{"value": "x"}, 400, "bad_request")
	// The spec refuses these too; the server must as well (direct: no validator).
	for _, body := range []any{map[string]any{"value": strings.Repeat("x", SecretMaxValueBytes+1)}, map[string]any{}, map[string]any{"value": 1}, map[string]any{"value": "x", "extra": 1}} {
		if st, eb := direct(t, e, "", "PUT", base+"/A", body); st != 400 || eb["code"] != "bad_request" {
			t.Fatalf("PUT %v: %d %v", body, st, eb)
		}
	}
	c.fail("PUT", "/v1/tenants/tn_nope/workloads/support/secrets/A", map[string]string{"value": "x"}, 404, "unknown_tenant")
	c.fail("DELETE", base+"/NOPE", nil, 404, "unknown_secret")

	c.ok("DELETE", base+"/B", nil, nil)
	items = nil
	c.items(base, &items)
	if len(items) != 4 {
		t.Fatalf("after delete: %+v", items)
	}
	// Another workload's secrets are separate.
	var other []SecretMeta
	c.items("/v1/tenants/"+tn.TenantID+"/workloads/other/secrets", &other)
	if len(other) != 0 {
		t.Fatalf("other workload: %+v", other)
	}

	// Audited by name, never by value.
	recs, err := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn.TenantID, Kinds: []string{"secret_put", "secret_delete"}})
	if err != nil || len(recs) != 7 {
		t.Fatalf("audit: %d %v", len(recs), err)
	}
	for _, r := range recs {
		if !strings.Contains(string(r.Detail), `"workload":"support"`) || r.ActingFor == "" {
			t.Fatalf("audit record %s: %s", r.Kind, r.Detail)
		}
	}
	assertNoValue(t, e, secretValue)
}

// assertNoValue: v appears nowhere under mgmt's directory (custody is
// sealed; the store holds no values), in its logs or in any audit record.
func assertNoValue(t *testing.T, e *testEnv, v string) {
	t.Helper()
	dir := filepath.Dir(filepath.Dir(e.sock))
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeType != 0 {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), v) {
			t.Errorf("secret value in plaintext in %s", p)
		}
		return nil
	})
	if strings.Contains(e.logs.String(), v) {
		t.Error("secret value in mgmt's log")
	}
	recs, _ := e.store.ListAudit(context.Background(), AuditFilter{})
	for _, r := range recs {
		if strings.Contains(string(r.Detail), v) || strings.Contains(string(r.Raw), v) {
			t.Errorf("secret value in audit record %s", r.Kind)
		}
	}
}

// TestSecretsRelease: values are released only to the host holding the
// workload's lease at its epoch (as volume keys), with no-store.
func TestSecretsRelease(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1, h2 := e.c, e.asHost("h1"), e.asHost("h2")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	c.ok("PUT", "/v1/tenants/"+tn.TenantID+"/workloads/support/secrets/API_KEY", map[string]string{"value": secretValue}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, &dep)
	q := func(workload string, epoch int64) string {
		return fmt.Sprintf("/v1/tenants/%s/workloads/%s/secret-grants?epoch=%d", tn.TenantID, workload, epoch)
	}
	var g SecretGrant
	st, _, hdr := h1.doRaw("POST", q("support", dep.Epoch), nil, &g)
	if st != 201 || g.Secrets["API_KEY"] != secretValue || g.Epoch != dep.Epoch || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("grant: %d %v cache %q", st, g, hdr.Get("Cache-Control"))
	}
	if s := fmt.Sprint(g); strings.Contains(s, secretValue) {
		t.Fatal("SecretGrant prints its values")
	}
	h1.fail("POST", q("support", dep.Epoch+1), nil, 409, "lease_lost")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	h2.fail("POST", q("support", dep.Epoch), nil, 409, "lease_lost")
	h1.fail("POST", q("never-deployed", 1), nil, 409, "lease_lost")
	directFail(t, e, "h1", "POST", q("support", 0), 400, "bad_request")
	// A workload with no secrets gets an empty map, and no audit record.
	var dep2 DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "plain", "bundle": bundle}, &dep2)
	var g2 SecretGrant
	h1.ok("POST", q("plain", dep2.Epoch), nil, &g2)
	if len(g2.Secrets) != 0 {
		t.Fatalf("plain: %v", g2.Secrets)
	}
	recs, _ := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn.TenantID, Kinds: []string{"secret_release"}})
	if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"names":["API_KEY"]`) || !strings.Contains(string(recs[0].Detail), `"host":"h1"`) {
		t.Fatalf("release audit: %+v", recs)
	}
	assertNoValue(t, e, secretValue)
}

// TestSecretsRoles: over the tailnet only admins put, list and delete
// secrets; hosts may only take a grant (and admins may not).
func TestSecretsRoles(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	var tn struct{ TenantID string }
	e.c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	srv := &Server{M: e.m, Self: SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net"}}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case "100.64.0.1:1":
			return WhoIsInfo{NodeID: "nHOST1", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case "100.64.0.6:1":
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		case "100.64.0.4:1":
			return WhoIsInfo{NodeID: "nEVE", Login: "eve@example.com"}, nil
		case "100.64.0.5:1": // a tailworks object node (tenant code)
			return WhoIsInfo{NodeID: "nAGENT", Tags: []string{"tag:tw-agent-support"}, Login: "tagged-devices"}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	h := srv.TailnetHandler(whois, srv.Handler())
	call := func(from, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = from
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), secretValue) && !strings.Contains(path, "secret-grants") {
			t.Fatalf("%s %s answered with the value", method, path)
		}
		var out struct {
			Code string `json:"code"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Code
	}
	// tw host add (admin), then the host's first registration with its
	// secret (hosts are bound to a join since the review fixes).
	var hj struct{ RegistrationSecret string }
	e.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h1", "tenantID": tn.TenantID}, &hj)
	if c, code := call("100.64.0.1:1", "PUT", "/v1/hosts/h1", `{"capacityNodes":1,"tiers":["process"],"registrationSecret":"`+hj.RegistrationSecret+`"}`); c != 201 {
		t.Fatalf("register: %d %s", c, code)
	}
	base := "/v1/tenants/" + tn.TenantID + "/workloads/support/secrets"
	put := `{"value":"` + secretValue + `"}`
	if c, code := call("100.64.0.6:1", "PUT", base+"/API_KEY", put); c != 201 {
		t.Fatalf("admin put: %d %s", c, code)
	}
	if c, _ := call("100.64.0.6:1", "GET", base, ""); c != 200 {
		t.Fatalf("admin list: %d", c)
	}
	for _, from := range []string{"100.64.0.4:1", "100.64.0.5:1"} {
		for _, m := range []string{"PUT", "GET", "DELETE"} {
			p, body := base+"/API_KEY", put
			if m == "GET" {
				p, body = base, ""
			} else if m == "DELETE" {
				body = ""
			}
			if c, code := call(from, m, p, body); c != 403 || code != "not_admin" {
				t.Fatalf("%s %s by %s: %d %s", m, p, from, c, code)
			}
		}
		if c, code := call(from, "POST", "/v1/tenants/"+tn.TenantID+"/workloads/support/secret-grants?epoch=1", ""); c != 403 || code != "not_admin" {
			t.Fatalf("grant by %s: %d %s", from, c, code)
		}
	}
	for _, m := range []string{"PUT", "GET", "DELETE"} {
		p, body := base+"/API_KEY", put
		if m == "GET" {
			p, body = base, ""
		} else if m == "DELETE" {
			body = ""
		}
		if c, code := call("100.64.0.1:1", m, p, body); c != 403 || code != "forbidden" {
			t.Fatalf("host %s: %d %s", m, c, code)
		}
	}
	if c, code := call("100.64.0.6:1", "POST", "/v1/tenants/"+tn.TenantID+"/workloads/support/secret-grants?epoch=1", ""); c != 403 || code != "forbidden" {
		t.Fatalf("admin grant: %d %s", c, code)
	}
	// The host may ask; without the lease it gets nothing.
	if c, code := call("100.64.0.1:1", "POST", "/v1/tenants/"+tn.TenantID+"/workloads/support/secret-grants?epoch=1", ""); c != 409 || code != "lease_lost" {
		t.Fatalf("host grant without a lease: %d %s", c, code)
	}
	if c, _ := call("100.64.0.6:1", "DELETE", base+"/API_KEY", ""); c != 204 {
		t.Fatalf("admin delete: %d", c)
	}
	recs, _ := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn.TenantID, Kinds: []string{"secret_put", "secret_delete"}})
	if len(recs) != 2 || recs[0].ActingFor != "alice@example.com" {
		t.Fatalf("audit actor: %+v", recs)
	}
}

// TestSecretsReplicas: each replica of a workload ("web--r2", its own
// lease) gets the secrets set on the workload, fenced by its own lease.
func TestSecretsReplicas(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.ts.policy = svcPolicy
	tn := v1Tenant(t, e, "ttest.ts.net", nil)
	hosts := map[string]*apiClient{}
	for _, id := range []string{"h1", "h2"} {
		hosts[id] = v1Host(t, e, id)
	}
	c := e.c
	c.ok("PUT", "/v1/tenants/"+tn+"/workloads/web/secrets/API_KEY", map[string]string{"value": secretValue}, nil)
	b := base64.StdEncoding.EncodeToString(makeBundle(t, map[string]string{"tailworks.jsonc": `{ "replicas": 2, "tailnet": { "tags": ["tag:tw-web"], "service": true } }`, "dist/index.js": "export default {}"}))
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "web", "bundle": b}, nil)
	got := map[string]bool{}
	for id, h := range hosts {
		var as []Assignment
		h.items("/v1/hosts/"+id+"/assignments", &as)
		for _, a := range as {
			grant := fmt.Sprintf("/v1/tenants/%s/workloads/%s/secret-grants?epoch=%d", tn, a.Workload, a.Epoch)
			var g SecretGrant
			h.ok("POST", grant, nil, &g)
			if g.Secrets["API_KEY"] != secretValue {
				t.Fatalf("%s on %s: %v", a.Workload, id, g)
			}
			got[a.Workload] = true
			other := "h1"
			if id == "h1" {
				other = "h2"
			}
			hosts[other].fail("POST", grant, nil, 409, "lease_lost")
		}
	}
	if !got["web"] || !got["web--r2"] {
		t.Fatalf("replicas granted: %v", got)
	}
}
