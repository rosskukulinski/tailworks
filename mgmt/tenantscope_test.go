package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/registry"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Tenant admins (ADR-043 D2, D3): github.com/rosskukulinski/tailworks/cap/tenant-admin lets a
// team act in the tenants its grant names, and nowhere else.

func rawVals(vals ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(vals))
	for i, v := range vals {
		out[i] = json.RawMessage(v)
	}
	return out
}

func TestTenantGrantParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		val   string
		valid bool
	}{
		{`{"tenants": ["a"], "actions": ["read"]}`, true},
		{`{"tenants": ["a", "b"], "actions": ["read", "deploy", "kill", "secrets", "data"]}`, true},
		{`{}`, false},                                                      // the operator shape: never full access here
		{`{"tenants": ["a"]}`, false},                                      // no actions
		{`{"actions": ["read"]}`, false},                                   // no tenants: no "all tenants"
		{`{"tenants": ["*"], "actions": ["read"]}`, false},                 // no wildcard
		{`{"tenants": ["a"], "actions": ["admin"]}`, false},                // unknown action
		{`{"tenants": ["a"], "actions": ["read"], "hosts": ["h"]}`, false}, // unknown field: refuse, don't ignore
		{`"payments"`, false},
	}
	for _, c := range cases {
		g, bad := parseTenantGrants(rawVals(c.val))
		if (len(g) == 1) != c.valid || (len(bad) == 1) == c.valid {
			t.Errorf("%s: grants %v, invalid %v; want valid=%v", c.val, g, bad, c.valid)
		}
	}
}

// TestTenantGrantPerValue: values are matched one at a time, never merged.
func TestTenantGrantPerValue(t *testing.T) {
	t.Parallel()
	p := Principal{Kind: "user", CapValues: map[string][]json.RawMessage{TenantAdminCap: rawVals(
		`{"tenants": ["a"], "actions": ["read"]}`,
		`{"tenants": ["b"], "actions": ["deploy"]}`,
	)}}
	for _, c := range []struct {
		tenant, action string
		want           bool
	}{{"a", "read", true}, {"b", "deploy", true}, {"a", "deploy", false}, {"b", "read", false}, {"c", "read", false}} {
		if got := p.tenantAllows(c.tenant, c.action); got != c.want {
			t.Errorf("%s/%s = %v, want %v", c.tenant, c.action, got, c.want)
		}
	}
	if got := p.tenantsWith("read"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("tenantsWith(read) = %v", got)
	}
	// Only user principals are ever narrowed or widened by the cap.
	p.Kind = "host"
	if p.tenantAllows("a", "read") {
		t.Error("a host principal used tenant-admin values")
	}
}

// tenantOperatorRoutes are the operations under /v1/tenants/{id} that stay
// admin-only (no "tenant:" part), by decision, not by omission.
var tenantOperatorRoutes = map[string]bool{
	"DELETE /v1/tenants/{id}":                                                    true, // removing a tenant
	"DELETE /v1/tenants/{id}/objects":                                            true, // purging every object
	"GET /v1/tenants/{id}/approvals":                                             true, // approvers (github.com/rosskukulinski/tailworks/cap/approve)
	"GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}":       true, // hosts
	"PATCH /v1/tenants/{id}":                                                     true, // limits, pool, tier, credentials
	"POST /v1/tenants/{id}/workloads/{workload}/approvals/{workflow}/{instance}": true, // approvers
	"POST /v1/tenants/{id}/workloads/{workload}/policy":                          true, // writes the tailnet policy (issue #116)
	"POST /v1/tenants/{id}/workloads/{workload}/secret-grants":                   true, // hosts
	"POST /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}/status":     true, // hosts (a store object's degraded report)
	"POST /v1/tenants/{id}/workloads/{workload}/telemetry-grants":                true, // hosts
}

// TestTenantRolesConsistent: every "tenant:" part names a known action and
// sits where something checks the tenant; every tenant-path operation is
// either open to tenant admins or deliberately admin-only.
func TestTenantRolesConsistent(t *testing.T) {
	t.Parallel()
	roles, err := v1.OperationRoles()
	if err != nil {
		t.Fatal(err)
	}
	for pattern, role := range roles {
		_, path, _ := strings.Cut(pattern, " ")
		action := tenantAction(role)
		underTenant := path == tenantPathPrefix || strings.HasPrefix(path, tenantPathPrefix+"/")
		if action == "" {
			if underTenant && !tenantOperatorRoutes[pattern] {
				t.Errorf("%s (%s): under %s but neither tenant:<action> nor in tenantOperatorRoutes", pattern, role, tenantPathPrefix)
			}
			if tenantCheckedInHandler[pattern] || tenantAnyTenant[pattern] {
				t.Errorf("%s (%s): listed as tenant-checked but has no tenant: part", pattern, role)
			}
			continue
		}
		if !slices.Contains(TenantActions, action) {
			t.Errorf("%s: unknown tenant action %q", pattern, action)
		}
		if tenantOperatorRoutes[pattern] {
			t.Errorf("%s: in tenantOperatorRoutes but has tenant:%s", pattern, action)
		}
		if !underTenant && !tenantCheckedInHandler[pattern] && !tenantAnyTenant[pattern] && !strings.HasPrefix(path, "/v2/") {
			t.Errorf("%s: tenant:%s, but the tenant is not in the path and no handler checks it (tenantCheckedInHandler)", pattern, action)
		}
	}
	for _, set := range []map[string]bool{tenantCheckedInHandler, tenantAnyTenant, tenantOperatorRoutes} {
		for pattern := range set {
			if _, ok := roles[pattern]; !ok {
				t.Errorf("%s: listed but not an operation in api/openapi.yaml", pattern)
			}
		}
	}
}

const (
	nodeTeamA   = "100.64.0.20:1" // tenant admin of tn (read, deploy, kill)
	nodeTeamRO  = "100.64.0.21:1" // read in tn, deploy in tn2: two values, never merged
	nodeTeamBad = "100.64.0.22:1" // only an invalid value
	nodeBoth    = "100.64.0.23:1" // admin cap and a tenant-admin value: an operator
	nodeAppr    = "100.64.0.24:1" // approver only
	nodeTeamMCP = "100.64.0.25:1" // tn: read+kill with mcp naming kill; tn2: read without mcp
	nodeTeamApp = "100.64.0.26:1" // approve in tn only, no actions
	nodeTeamTwo = "100.64.0.27:1" // read in tn and tn2 (one value)
)

type tenantEnv struct {
	*authzEnv
	raw        http.Handler // without the /v1 spec validator: /mcp and /ui aren't /v1
	depA, depB string       // deployments in tn (workload w) and tn2 (workload z)
	objZ       string       // object in tn2
}

func newTenantEnv(t *testing.T) *tenantEnv {
	e := newTestEnvWith(t, func(c *Config) { c.RegistryDir = filepath.Join(t.TempDir(), "registry") })
	srv := &Server{M: e.m, Self: SelfInfo{DNSName: "tw-mgmt.t.ts.net", Tailnet: "t.ts.net"}}
	te := &tenantEnv{}
	tv := func(v string) map[string][]json.RawMessage {
		return map[string][]json.RawMessage{TenantAdminCap: rawVals(v)}
	}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		tn, tn2 := te.tn, te.tn2
		switch addr {
		case nodeH1:
			return WhoIsInfo{NodeID: "nHOST1", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case nodeH2:
			return WhoIsInfo{NodeID: "nHOST2", Tags: []string{HostTag}, Login: "tagged-devices"}, nil
		case nodeAdm:
			return WhoIsInfo{NodeID: "nADM", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		case nodeTeamA:
			return WhoIsInfo{NodeID: "nTA", Name: "pat.t.ts.net.", Login: "pat@example.com", Caps: []string{TenantAdminCap},
				CapValues: tv(fmt.Sprintf(`{"tenants": [%q], "actions": ["read", "deploy", "kill"]}`, tn))}, nil
		case nodeTeamRO:
			return WhoIsInfo{NodeID: "nTR", Name: "ro.t.ts.net.", Login: "ro@example.com", Caps: []string{TenantAdminCap},
				CapValues: map[string][]json.RawMessage{TenantAdminCap: rawVals(
					fmt.Sprintf(`{"tenants": [%q], "actions": ["read"]}`, tn),
					fmt.Sprintf(`{"tenants": [%q], "actions": ["deploy"]}`, tn2))}}, nil
		case nodeTeamBad:
			return WhoIsInfo{NodeID: "nTB", Login: "bad@example.com", Caps: []string{TenantAdminCap},
				CapValues: tv(fmt.Sprintf(`{"tenants": [%q]}`, tn))}, nil
		case nodeBoth:
			return WhoIsInfo{NodeID: "nBO", Login: "ops@example.com", Caps: []string{AdminCap, TenantAdminCap},
				CapValues: tv(fmt.Sprintf(`{"tenants": [%q], "actions": ["read"]}`, tn))}, nil
		case nodeTeamMCP:
			return WhoIsInfo{NodeID: "nTM", Name: "mcp.t.ts.net.", Login: "agent@example.com", Caps: []string{TenantAdminCap},
				CapValues: map[string][]json.RawMessage{TenantAdminCap: rawVals(
					fmt.Sprintf(`{"tenants": [%q], "actions": ["read", "kill"], "mcp": {"tools": ["kill"]}}`, tn),
					fmt.Sprintf(`{"tenants": [%q], "actions": ["read"]}`, tn2))}}, nil
		case nodeTeamApp:
			return WhoIsInfo{NodeID: "nTP", Name: "app.t.ts.net.", Login: "lead@example.com", Caps: []string{TenantAdminCap},
				CapValues: tv(fmt.Sprintf(`{"tenants": [%q], "approve": [{"workflows": ["refund"]}]}`, tn))}, nil
		case nodeTeamTwo:
			return WhoIsInfo{NodeID: "nT2", Name: "two.t.ts.net.", Login: "two@example.com", Caps: []string{TenantAdminCap},
				CapValues: tv(fmt.Sprintf(`{"tenants": [%q, %q], "actions": ["read"]}`, tn, tn2))}, nil
		case nodeAppr:
			return WhoIsInfo{NodeID: "nAP", Name: "ap.t.ts.net.", Login: "ap@example.com", Caps: []string{"github.com/rosskukulinski/tailworks/cap/approve"},
				CapValues: map[string][]json.RawMessage{"github.com/rosskukulinski/tailworks/cap/approve": rawVals(`{}`)}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	val, err := v1.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	a := &authzEnv{testEnv: e, t: t, h: srv.TailnetHandler(whois, val.Middleware(specReporter(t))(srv.Handler()))}
	te.authzEnv = a
	te.raw = srv.TailnetHandler(whois, srv.Handler())
	// Two teams on one tailnet: each its own owner tag; the second, a prefix.
	a.tn = v1Tenant(t, e, "t.ts.net", map[string]any{"ownerTag": "tag:tw-t-a"})
	a.tn2 = v1Tenant(t, e, "t.ts.net", map[string]any{"ownerTag": "tag:tw-t-b", "namePrefix": "b"})
	a.addHost("h1", nodeH1)
	a.addHost("h2", nodeH2)
	var d struct{ DeploymentID string }
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": a.tn, "workload": "w", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, &d)
	te.depA = d.DeploymentID
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": a.tn2, "workload": "z", "host": "h2", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "Z", Tags: []string{"tag:ttest"}}}}, &d)
	te.depB = d.DeploymentID
	var o map[string]any
	if st, eb := a.call(nodeH1, "POST", "/v1/objects", map[string]any{"tenantID": a.tn, "workload": "w", "namespace": "A", "objectName": "a"}, &o); st != 202 {
		t.Fatalf("create a: %d %v", st, eb)
	}
	a.objA = o["objectID"].(string)
	if st, eb := a.call(nodeH2, "POST", "/v1/objects", map[string]any{"tenantID": a.tn2, "workload": "z", "namespace": "Z", "objectName": "z"}, &o); st != 202 {
		t.Fatalf("create z: %d %v", st, eb)
	}
	te.objZ = o["objectID"].(string)
	return te
}

func TestTenantAdminScope(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	tn, tn2 := e.tn, e.tn2

	// whoami: the grants, and only the granted tenant (not the other team's
	// on the same tailnet).
	var who WhoAmI
	if st, eb := e.call(nodeTeamA, "GET", "/v1/whoami", nil, &who); st != 200 {
		t.Fatalf("whoami: %d %v", st, eb)
	}
	if who.Admin || who.Kind != "user" || len(who.TenantGrants) != 1 || who.TenantGrants[0].Tenants[0] != tn {
		t.Fatalf("whoami = %+v", who)
	}
	if len(who.Tenants) != 1 || who.Tenants[0].TenantID != tn {
		t.Fatalf("whoami tenants = %+v, want only %s", who.Tenants, tn)
	}

	// Lists are limited to the granted tenant.
	var ts struct{ Items []Tenant }
	if st, eb := e.call(nodeTeamA, "GET", "/v1/tenants", nil, &ts); st != 200 || len(ts.Items) != 1 || ts.Items[0].ID != tn {
		t.Fatalf("list tenants: %d %v %+v", st, eb, ts.Items)
	}
	var ds struct{ Items []map[string]any }
	if st, eb := e.call(nodeTeamA, "GET", "/v1/deployments", nil, &ds); st != 200 || len(ds.Items) != 1 || ds.Items[0]["tenantID"] != tn {
		t.Fatalf("list deployments: %d %v %+v", st, eb, ds.Items)
	}
	var objs struct{ Items []map[string]any }
	if st, eb := e.call(nodeTeamA, "GET", "/v1/objects", nil, &objs); st != 200 || len(objs.Items) != 1 || objs.Items[0]["objectID"] != e.objA {
		t.Fatalf("list objects: %d %v %+v", st, eb, objs.Items)
	}
	e.fail(nodeTeamA, "GET", "/v1/deployments?tenant="+tn2, nil, 403, "tenant_forbidden")
	e.fail(nodeTeamA, "GET", "/v1/objects?tenant="+tn2, nil, 403, "tenant_forbidden")
	e.fail(nodeTeamA, "GET", "/v1/audit?tenant="+tn2, nil, 403, "tenant_forbidden")
	var aud struct{ Chains []any }
	if st, eb := e.call(nodeTeamA, "GET", "/v1/audit", nil, &aud); st != 200 || len(aud.Chains) != 0 {
		t.Fatalf("audit: %d %v, chains %v (host chains are the operator's)", st, eb, aud.Chains)
	}

	// Own tenant: read, and the routes the actions name.
	if st, eb := e.call(nodeTeamA, "GET", "/v1/tenants/"+tn, nil, nil); st != 200 {
		t.Fatalf("get own tenant: %d %v", st, eb)
	}
	if st, eb := e.call(nodeTeamA, "GET", "/v1/deployments/"+e.depA, nil, nil); st != 200 {
		t.Fatalf("get own deployment: %d %v", st, eb)
	}
	if st, eb := e.call(nodeTeamA, "GET", "/v1/objects/"+e.objA, nil, nil); st != 200 {
		t.Fatalf("get own object: %d %v", st, eb)
	}

	// The other team's tenant: 403 where the caller named it, 404 where a
	// resource would have revealed it.
	eb := e.fail(nodeTeamA, "GET", "/v1/tenants/"+tn2, nil, 403, "tenant_forbidden")
	if s, _ := eb["snippet"].(string); !strings.Contains(s, TenantAdminCap) || !strings.Contains(s, tn2) {
		t.Fatalf("snippet = %q", s)
	}
	e.fail(nodeTeamA, "GET", "/v1/tenants/"+tn2+"/workloads/z/deployments", nil, 403, "tenant_forbidden")
	e.fail(nodeTeamA, "GET", "/v1/deployments/"+e.depB, nil, 404, "not_found")
	e.fail(nodeTeamA, "GET", "/v1/deployments/"+e.depB+"/bundle", nil, 404, "not_found")
	e.fail(nodeTeamA, "DELETE", "/v1/deployments/"+e.depB, nil, 404, "not_found")
	e.fail(nodeTeamA, "GET", "/v1/objects/"+e.objZ, nil, 404, "not_found")
	e.fail(nodeTeamA, "PATCH", "/v1/objects/"+e.objZ, map[string]any{"ttl": 60}, 404, "not_found")
	e.fail(nodeTeamA, "DELETE", "/v1/objects/"+e.objZ, nil, 404, "not_found")
	e.fail(nodeTeamA, "POST", "/v1/deployments", map[string]any{"tenantID": tn2, "workload": "z", "host": "h2", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "Z", Tags: []string{"tag:ttest"}}}}, 403, "tenant_forbidden")

	// Actions it doesn't hold, in its own tenant.
	e.fail(nodeTeamA, "PUT", "/v1/tenants/"+tn+"/workloads/w/secrets/K", map[string]any{"value": "v"}, 403, "tenant_forbidden")
	e.fail(nodeTeamA, "GET", "/v1/tenants/"+tn+"/workloads/w/stores/kv/K/keys", nil, 403, "tenant_forbidden")

	// Operator routes: hosts, tenants' settings, releases, upgrades, status.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/hosts", nil},
		{"GET", "/v1/hosts/h1", nil},
		{"PUT", "/v1/hosts/h1/cordon", nil},
		{"POST", "/v1/hosts/h1/drain", map[string]any{}},
		{"DELETE", "/v1/hosts/h1", nil},
		{"POST", "/v1/hosts", map[string]any{"hostID": "h9", "tenantID": tn}},
		{"POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth"}},
		{"PATCH", "/v1/tenants/" + tn, map[string]any{"maxObjects": 100000}},
		{"DELETE", "/v1/tenants/" + tn, nil},
		{"DELETE", "/v1/tenants/" + tn + "/objects", nil},
		{"GET", "/v1/status", nil},
		{"GET", "/v1/releases", nil},
		{"GET", "/v1/upgrades", nil},
		{"POST", "/v1/device-delete-runs", nil},
		{"GET", "/v1/container-reservations", nil},
	} {
		if st, eb := e.call(nodeTeamA, c.method, c.path, c.body, nil); st != 403 || eb["code"] != "tenant_forbidden" {
			t.Errorf("%s %s as tenant admin: %d %v, want 403 tenant_forbidden", c.method, c.path, st, eb)
		}
	}

	// Deploy in its own tenant works.
	var dv map[string]any
	if st, eb := e.call(nodeTeamA, "POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "w2", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, &dv); st != 202 {
		t.Fatalf("deploy own tenant: %d %v", st, eb)
	}

	// Kill in its own tenant works (and is the last call: it changes state).
	if st, eb := e.call(nodeTeamA, "DELETE", "/v1/objects/"+e.objA, nil, nil); st != 202 {
		t.Fatalf("kill own object: %d %v", st, eb)
	}
}

func TestTenantAdminValuesNotMerged(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// read in tn, deploy in tn2: no deploy in tn, no read in tn2.
	if st, eb := e.call(nodeTeamRO, "GET", "/v1/tenants/"+e.tn, nil, nil); st != 200 {
		t.Fatalf("read tn: %d %v", st, eb)
	}
	e.fail(nodeTeamRO, "GET", "/v1/tenants/"+e.tn2, nil, 403, "tenant_forbidden")
	e.fail(nodeTeamRO, "POST", "/v1/tenants/"+e.tn+"/workloads/w/rollbacks", map[string]any{}, 403, "tenant_forbidden")
	e.fail(nodeTeamRO, "POST", "/v1/deployments", map[string]any{"tenantID": e.tn, "workload": "w", "host": "h1", "bundle": noConfigBundle(t),
		"namespaces": []ClassConfig{{Class: "A", Tags: []string{"tag:ttest"}}}}, 403, "tenant_forbidden")
	// Only one tenant has "read", so lists default to it.
	var ds struct{ Items []map[string]any }
	if st, eb := e.call(nodeTeamRO, "GET", "/v1/deployments", nil, &ds); st != 200 || len(ds.Items) != 1 || ds.Items[0]["tenantID"] != e.tn {
		t.Fatalf("list deployments: %d %v %+v", st, eb, ds.Items)
	}
}

func TestTenantAdminInvalidAndOperator(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// A caller whose only value is invalid gets nothing but whoami, which
	// says why.
	eb := e.fail(nodeTeamBad, "GET", "/v1/tenants/"+e.tn, nil, 403, "tenant_forbidden")
	if m, _ := eb["message"].(string); !strings.Contains(m, "names no actions") {
		t.Fatalf("message = %q", m)
	}
	e.fail(nodeTeamBad, "GET", "/v1/tenants", nil, 403, "tenant_forbidden")
	// AdminCap wins: a tenant-admin value never narrows an operator.
	if st, eb := e.call(nodeBoth, "GET", "/v1/hosts", nil, nil); st != 200 {
		t.Fatalf("operator with a tenant-admin value: %d %v", st, eb)
	}
	if st, eb := e.call(nodeBoth, "GET", "/v1/tenants/"+e.tn2, nil, nil); st != 200 {
		t.Fatalf("operator, other tenant: %d %v", st, eb)
	}
	var who WhoAmI
	if st, _ := e.call(nodeTeamBad, "GET", "/v1/whoami", nil, &who); st != 200 || len(who.InvalidTenantGrants) != 1 {
		t.Fatalf("whoami of an invalid value: %d %+v", st, who)
	}
}

func TestTenantAdminRegistry(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// Approvers were admitted as plain users and reached the registry as if
	// they were admins; now they don't reach it.
	e.fail(nodeAppr, "GET", "/v2/", nil, 403, "forbidden")
	e.fail(nodeAppr, "GET", "/v2/"+e.tn+"/app/tags/list", nil, 403, "forbidden")
	// A tenant admin reaches the registry.
	if st, eb := e.call(nodeTeamA, "GET", "/v2/", nil, nil); st != 200 {
		t.Fatalf("tenant admin /v2/: %d %v", st, eb)
	}
	// The registry's rule: own namespace with read/deploy, public pull only.
	pA := Principal{Kind: "user", CapValues: map[string][]json.RawMessage{TenantAdminCap: rawVals(
		fmt.Sprintf(`{"tenants": [%q], "actions": ["read", "deploy"]}`, e.tn))}}
	pR := Principal{Kind: "user", CapValues: map[string][]json.RawMessage{TenantAdminCap: rawVals(
		fmt.Sprintf(`{"tenants": [%q], "actions": ["read"]}`, e.tn))}}
	for _, c := range []struct {
		p    Principal
		ns   string
		push bool
		ok   bool
	}{
		{pA, e.tn, false, true},
		{pA, e.tn, true, true},
		{pR, e.tn, true, false},
		{pA, e.tn2, false, false},
		{pA, e.tn2, true, false},
		{pA, registry.PublicNamespace, false, true},
		{pA, registry.PublicNamespace, true, false},
		{pA, "", false, true},
	} {
		if err := registryTenantAuthorize(c.p, c.ns, c.push); (err == nil) != c.ok {
			t.Errorf("ns %q push %v: %v, want ok=%v", c.ns, c.push, err, c.ok)
		}
	}
}

func TestTenantAdminSubValues(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		val   string
		valid bool
	}{
		{`{"tenants": ["a"], "approve": [{}]}`, true},                                 // approve alone needs no actions
		{`{"tenants": ["a"], "actions": ["read"], "mcp": {"tools": ["kill"]}}`, true}, // the gate checks kill's action later
		{`{"tenants": ["a"], "mcp": {"tools": []}}`, false},                           // mcp needs actions
		{`{"tenants": ["a"], "actions": ["read"], "mcp": {"tools": ["status"]}}`, false},
		{`{"tenants": ["a"], "approve": ["x"]}`, false},
	} {
		g, bad := parseTenantGrants(rawVals(c.val))
		if (len(g) == 1) != c.valid {
			t.Errorf("%s: grants %v, invalid %v; want valid=%v", c.val, g, bad, c.valid)
		}
	}
}

// mcpRPC posts one JSON-RPC request to /mcp as from.
func (e *tenantEnv) mcpRPC(from, method string, params any) map[string]any {
	e.t.Helper()
	validated := e.h
	e.h = e.raw
	defer func() { e.h = validated }()
	var out map[string]any
	st, eb := e.call(from, "POST", MCPPath, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, &out)
	if st != 200 {
		e.t.Fatalf("%s %s: %d %v", from, method, st, eb)
	}
	return out
}

func (e *tenantEnv) mcpTools(from string) []string {
	res, _ := e.mcpRPC(from, "tools/list", map[string]any{})["result"].(map[string]any)
	var names []string
	for _, x := range res["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	slices.Sort(names)
	return names
}

func (e *tenantEnv) mcpCall(from, tool string, args map[string]any) (map[string]any, bool) {
	res, _ := e.mcpRPC(from, "tools/call", map[string]any{"name": tool, "arguments": args})["result"].(map[string]any)
	sc, _ := res["structuredContent"].(map[string]any)
	return sc, res["isError"] == true
}

func TestTenantAdminMCP(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// Read tools from the "mcp" value, plus kill (named, and "kill" in the
	// same value). No status (operator), no deploy or undeploy.
	if got, want := e.mcpTools(nodeTeamMCP), []string{"audit", "kill", "logs", "ls", "whoami"}; !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	// No "mcp" entry: no tools at all.
	if got := e.mcpTools(nodeTeamA); len(got) != 0 {
		t.Fatalf("tenant admin without mcp: tools %v", got)
	}
	// ls runs as the caller: its own tenant works...
	if out, isErr := e.mcpCall(nodeTeamMCP, "ls", map[string]any{"tenant": e.tn}); isErr {
		t.Fatalf("ls own tenant: %v", out)
	}
	// ...but tn2, granted "read" only in a value without "mcp", doesn't.
	if out, isErr := e.mcpCall(nodeTeamMCP, "ls", map[string]any{"tenant": e.tn2}); !isErr {
		t.Fatalf("ls tn2 through MCP: %v", out)
	}
	// kill: another tenant's object is invisible; its own works.
	if out, isErr := e.mcpCall(nodeTeamMCP, "kill", map[string]any{"object": e.objZ, "reason": "test"}); !isErr {
		t.Fatalf("kill tn2 object through MCP: %v", out)
	}
	if out, isErr := e.mcpCall(nodeTeamMCP, "kill", map[string]any{"object": e.objA, "reason": "test"}); isErr {
		t.Fatalf("kill own object through MCP: %v", out)
	}
	// A tool it lacks names the tenant-admin value that would add it.
	out, isErr := e.mcpCall(nodeTeamMCP, "undeploy", map[string]any{"workload": "w", "reason": "test"})
	if eb, _ := out["error"].(map[string]any); !isErr || !strings.Contains(fmt.Sprint(eb["snippet"]), TenantAdminCap) {
		t.Fatalf("undeploy: %v", out)
	}
}

func TestTenantAdminApprovals(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// An approve-only value admits the approvals routes for its tenant...
	if st, eb := e.call(nodeTeamApp, "GET", "/v1/tenants/"+e.tn+"/approvals", nil, nil); st != 200 {
		t.Fatalf("approvals in own tenant: %d %v", st, eb)
	}
	// ...not another tenant's, and nothing else.
	e.fail(nodeTeamApp, "GET", "/v1/tenants/"+e.tn2+"/approvals", nil, 403, "tenant_forbidden")
	e.fail(nodeTeamApp, "POST", "/v1/tenants/"+e.tn2+"/workloads/z/approvals/refund/i1", map[string]any{"decision": "approve"}, 403, "tenant_forbidden")
	e.fail(nodeTeamApp, "GET", "/v1/deployments", nil, 403, "tenant_forbidden")
	// The values sent to the host for a decision are the tenant's only.
	p := Principal{Kind: "user", Caps: []string{TenantAdminCap}, CapValues: map[string][]json.RawMessage{TenantAdminCap: rawVals(
		fmt.Sprintf(`{"tenants": [%q], "approve": [{"workflows": ["refund"]}]}`, e.tn))}}
	if g := approveGrantsFor(p, e.tn); len(g) != 1 || !strings.Contains(string(g[0]), "refund") {
		t.Fatalf("grants for tn = %s", g)
	}
	if g := approveGrantsFor(p, e.tn2); len(g) != 0 {
		t.Fatalf("grants for tn2 = %s", g)
	}
	// whoami lists only the tenant its values name.
	var who WhoAmI
	if st, _ := e.call(nodeTeamApp, "GET", "/v1/whoami", nil, &who); st != 200 || !who.Approver || len(who.Tenants) != 1 || who.Tenants[0].TenantID != e.tn {
		t.Fatalf("whoami = %d %+v", st, who)
	}
}

func TestTenantAdminSeveralTenants(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t)
	// read in both tenants, no ?tenant=: both tenants' rows, nobody else's.
	var ds struct{ Items []map[string]any }
	if st, eb := e.call(nodeTeamTwo, "GET", "/v1/deployments", nil, &ds); st != 200 || len(ds.Items) != 2 {
		t.Fatalf("deployments: %d %v %v", st, eb, ds.Items)
	}
	var objs struct{ Items []map[string]any }
	if st, eb := e.call(nodeTeamTwo, "GET", "/v1/objects?limit=1", nil, &objs); st != 200 || len(objs.Items) != 1 {
		t.Fatalf("objects page of 1: %d %v %v", st, eb, objs.Items)
	}
	var aud struct{ Items []map[string]any }
	if st, eb := e.call(nodeTeamTwo, "GET", "/v1/audit", nil, &aud); st != 200 {
		t.Fatalf("audit: %d %v", st, eb)
	}
	for _, r := range aud.Items {
		if tn := r["tenantID"]; tn != e.tn && tn != e.tn2 {
			t.Fatalf("audit record of tenant %v", tn)
		}
	}
	// The console opens, without the hosts panel.
	r := httptest.NewRequest("GET", "http://tw-mgmt/ui/", nil)
	r.RemoteAddr = nodeTeamTwo
	w := httptest.NewRecorder()
	e.raw.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), `id="hosts"`) || !strings.Contains(w.Body.String(), "tenant admin") {
		t.Fatalf("console: %d (hosts panel or tenant-admin note wrong)", w.Code)
	}
}

func TestTenantAccessWarnings(t *testing.T) {
	t.Parallel()
	e := newTenantEnv(t) // tn and tn2 share t.ts.net
	ctx := context.Background()
	tn, err := e.m.store.GetTenant(ctx, e.tn)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := ParsePolicy([]byte(fmt.Sprintf(`{"grants": [
		{"src": ["group:a"], "dst": ["tag:tw-mgmt"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{"tenants": [%q]}]}},
		{"src": ["group:b"], "dst": ["tag:tw-mgmt"], "app": {"github.com/rosskukulinski/tailworks/cap/tenant-admin": [{"tenants": [%q]}, {"tenants": ["nope"], "actions": ["read"]}]}},
		{"src": ["group:c"], "dst": ["tag:tw-mgmt"], "app": {"github.com/rosskukulinski/tailworks/cap/approve": [{}], "github.com/rosskukulinski/tailworks/cap/mcp": [{}]}},
		{"src": ["group:ops"], "dst": ["tag:tw-mgmt"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}}
	]}`, e.tn, e.tn)))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(e.m.accessWarnings(ctx, tn, pol), "\n")
	for _, want := range []string{
		"group:a → tag:tw-mgmt: values on github.com/rosskukulinski/tailworks/cap/admin don't narrow it",
		"names no actions",
		`names "nope", which is not a tenant`,
		"github.com/rosskukulinski/tailworks/cap/approve applies to every tenant on this tailnet (" + e.tn2,
		"github.com/rosskukulinski/tailworks/cap/mcp applies to every tenant",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "group:ops") {
		t.Errorf("a plain admin grant was warned about:\n%s", got)
	}
}
