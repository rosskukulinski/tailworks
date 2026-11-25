package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

const otlpToken = "Bearer otlp-s3cret-0123456789"

// TestTelemetryConfig (ADR-045): PUT/GET/DELETE of a tenant's
// OTLP export. Header values go to custody only: never returned, logged
// or audited.
func TestTelemetryConfig(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c := e.c
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	path := "/v1/tenants/" + tn.TenantID + "/telemetry"

	var v TelemetryView
	c.ok("GET", path, nil, &v)
	if v.TenantID != tn.TenantID || v.OTLP != nil {
		t.Fatalf("empty config: %+v", v)
	}
	c.ok("PUT", path, map[string]any{"otlp": map[string]any{"endpoint": "http://otel-collector:4318", "headers": map[string]string{"Authorization": otlpToken, "x-scope-orgid": "acme"}}}, &v)
	if v.OTLP == nil || v.OTLP.Endpoint != "http://otel-collector:4318" || fmt.Sprint(v.OTLP.HeaderNames) != "[authorization x-scope-orgid]" ||
		fmt.Sprint(v.OTLP.Signals) != "[logs metrics traces]" || v.UpdatedBy == "" {
		t.Fatalf("put: %+v", v.OTLP)
	}
	var got TelemetryView
	st, raw, _ := c.doRaw("GET", path, nil, &got)
	if st != 200 || strings.Contains(fmt.Sprint(raw), "s3cret") || fmt.Sprint(got.OTLP) != fmt.Sprint(v.OTLP) {
		t.Fatalf("get: %d %s", st, raw)
	}
	c.ok("PUT", path, map[string]any{"otlp": map[string]any{"endpoint": "https://otel.t.ts.net/otlp", "signals": []string{"traces", "logs", "traces"}}}, &v)
	if fmt.Sprint(v.OTLP.Signals) != "[logs traces]" || len(v.OTLP.HeaderNames) != 0 {
		t.Fatalf("signals: %+v", v.OTLP)
	}
	// Refusals (direct: no spec validator in front).
	for _, body := range []any{
		map[string]any{},
		map[string]any{"otlp": map[string]any{"endpoint": "otel:4318"}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://u:p@otel:4318"}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318?x=1"}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "signals": []string{"profiles"}}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "headers": map[string]string{"content-type": "x"}}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "headers": map[string]string{"bad name": "x"}}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "headers": map[string]string{"a": "line\nbreak " + otlpToken}}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "headers": map[string]string{"a": strings.Repeat("x", TelemetryMaxHeaderValue+1)}}},
		map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318"}, "extra": 1},
	} {
		st, eb := direct(t, e, "", "PUT", path, body)
		if st != 400 || eb["code"] != "bad_request" || strings.Contains(fmt.Sprint(eb), "s3cret") {
			t.Fatalf("PUT %v: %d %v", body, st, eb)
		}
	}
	c.fail("PUT", "/v1/tenants/tn_nope/telemetry", map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318"}}, 404, "unknown_tenant")
	c.ok("DELETE", path, nil, nil)
	c.ok("DELETE", path, nil, nil) // idempotent
	got = TelemetryView{}
	c.ok("GET", path, nil, &got)
	if got.OTLP != nil {
		t.Fatalf("after delete: %+v", got)
	}
	recs, err := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn.TenantID, Kinds: []string{"telemetry_put", "telemetry_delete"}})
	if err != nil || len(recs) != 4 || !strings.Contains(string(recs[0].Detail), `"headers":["authorization","x-scope-orgid"]`) {
		t.Fatalf("audit: %d %v", len(recs), err)
	}
	assertNoValue(t, e, "otlp-s3cret")
}

// TestTelemetryRelease: the config, header values included, goes only to
// the host holding the workload's lease at its epoch; the release is
// audited once per host, workload and config version.
func TestTelemetryRelease(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c, h1, h2 := e.c, e.asHost("h1"), e.asHost("h2")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, &dep)
	q := func(workload string, epoch int64) string {
		return fmt.Sprintf("/v1/tenants/%s/workloads/%s/telemetry-grants?epoch=%d", tn.TenantID, workload, epoch)
	}
	// Nothing configured: no otlp, no audit.
	var g TelemetryGrant
	if st, _, hdr := h1.doRaw("POST", q("support", dep.Epoch), nil, &g); st != 201 || g.OTLP != nil || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("empty grant: %d %+v", st, g)
	}
	c.ok("PUT", "/v1/tenants/"+tn.TenantID+"/telemetry", map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "headers": map[string]string{"authorization": otlpToken}}}, nil)
	for i := 0; i < 3; i++ { // hosts poll every minute
		g = TelemetryGrant{}
		h1.ok("POST", q("support", dep.Epoch), nil, &g)
		if g.OTLP == nil || g.OTLP.Endpoint != "http://otel:4318" || g.OTLP.Headers["authorization"] != otlpToken {
			t.Fatalf("grant: %+v", g.OTLP)
		}
	}
	if s := fmt.Sprint(g); strings.Contains(s, "s3cret") {
		t.Fatal("TelemetryGrant prints its header values")
	}
	h1.fail("POST", q("support", dep.Epoch+1), nil, 409, "lease_lost")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	h2.fail("POST", q("support", dep.Epoch), nil, 409, "lease_lost")
	h1.fail("POST", q("never-deployed", 1), nil, 409, "lease_lost")
	directFail(t, e, "h1", "POST", q("support", 0), 400, "bad_request")
	// A new version is released (and audited) again.
	c.ok("PUT", "/v1/tenants/"+tn.TenantID+"/telemetry", map[string]any{"otlp": map[string]any{"endpoint": "http://otel2:4318"}}, nil)
	h1.ok("POST", q("support", dep.Epoch), nil, &g)
	recs, _ := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn.TenantID, Kinds: []string{"telemetry_release"}})
	if len(recs) != 2 || !strings.Contains(string(recs[0].Detail), `"host":"h1"`) || !strings.Contains(string(recs[1].Detail), "otel2") {
		t.Fatalf("release audit: %d %+v", len(recs), recs)
	}
	deny, _ := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn.TenantID, Kinds: []string{"deny"}})
	if len(deny) != 2 { // stale epoch, another host (no lease at all: nothing to audit, as for secrets)
		t.Fatalf("refusals audited: %d", len(deny))
	}
	assertNoValue(t, e, "otlp-s3cret")
}

// TestTelemetryRoles: over the tailnet only admins configure the export
// (any other caller, tenant code included, is not_admin); only hosts take
// a grant.
func TestTelemetryRoles(t *testing.T) {
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
		if strings.Contains(w.Body.String(), "otlp-s3cret") && !strings.Contains(path, "telemetry-grants") {
			t.Fatalf("%s %s answered with a header value", method, path)
		}
		var out struct {
			Code string `json:"code"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Code
	}
	var hj struct{ RegistrationSecret string }
	e.c.ok("POST", "/v1/hosts", map[string]any{"hostID": "h1", "tenantID": tn.TenantID}, &hj)
	if c, code := call("100.64.0.1:1", "PUT", "/v1/hosts/h1", `{"capacityNodes":1,"tiers":["process"],"registrationSecret":"`+hj.RegistrationSecret+`"}`); c != 201 {
		t.Fatalf("register: %d %s", c, code)
	}
	path := "/v1/tenants/" + tn.TenantID + "/telemetry"
	put := `{"otlp":{"endpoint":"http://otel:4318","headers":{"authorization":"` + otlpToken + `"}}}`
	if c, code := call("100.64.0.6:1", "PUT", path, put); c != 200 {
		t.Fatalf("admin put: %d %s", c, code)
	}
	grant := "/v1/tenants/" + tn.TenantID + "/workloads/support/telemetry-grants?epoch=1"
	for _, from := range []string{"100.64.0.4:1", "100.64.0.5:1"} {
		for _, m := range []string{"PUT", "GET", "DELETE"} {
			body := put
			if m != "PUT" {
				body = ""
			}
			if c, code := call(from, m, path, body); c != 403 || code != "not_admin" {
				t.Fatalf("%s by %s: %d %s", m, from, c, code)
			}
		}
		if c, code := call(from, "POST", grant, ""); c != 403 || code != "not_admin" {
			t.Fatalf("grant by %s: %d %s", from, c, code)
		}
	}
	for _, m := range []string{"PUT", "GET", "DELETE"} {
		body := put
		if m != "PUT" {
			body = ""
		}
		if c, code := call("100.64.0.1:1", m, path, body); c != 403 || code != "forbidden" {
			t.Fatalf("host %s: %d %s", m, c, code)
		}
	}
	if c, code := call("100.64.0.6:1", "POST", grant, ""); c != 403 || code != "forbidden" {
		t.Fatalf("admin grant: %d %s", c, code)
	}
	if c, code := call("100.64.0.1:1", "POST", grant, ""); c != 409 || code != "lease_lost" {
		t.Fatalf("host grant without a lease: %d %s", c, code)
	}
}
