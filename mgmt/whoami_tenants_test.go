package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/twd/internal/approvecap"
)

// TestWhoAmITenants (issue #50): whoami names the caller's own tailnet,
// the tenants on it and its approve grants, so an approver who is not an
// admin never needs a tenant ID; the approvals page defaults to them.
func TestWhoAmITenants(t *testing.T) {
	e := newTestEnv(t, nil)
	tnA := v1Tenant(t, e, "a.ts.net", nil)
	v1Tenant(t, e, "b.ts.net", nil)
	approvalHost(t, e, "h1", tnA)
	if _, err := e.store.AcquireLease(context.Background(), LeaseName(tnA, "refunds"), "h1", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := &Server{M: e.m}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case "100.64.0.5:1": // an approver up to 100 on a.ts.net, not an admin
			return WhoIsInfo{NodeID: "nBOB", Name: "bob-laptop.A.ts.net.", Login: "bob@example.com", Caps: []string{approvecap.Cap},
				CapValues: map[string][]json.RawMessage{approvecap.Cap: {json.RawMessage(`{"workflows":["refund"],"limits":{"amount":100}}`)}}}, nil
		case "100.64.0.7:1": // an approver on a tailnet with no tenant
			return WhoIsInfo{NodeID: "nCAR", Name: "carol.c.ts.net.", Login: "carol@example.com", Caps: []string{approvecap.Cap}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	h := srv.TailnetHandler(whois, srv.Handler())
	get := func(from, path string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = from
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	var who WhoAmI
	c, body := get("100.64.0.5:1", "/v1/whoami")
	if c != 200 || json.Unmarshal([]byte(body), &who) != nil {
		t.Fatalf("whoami: %d %s", c, body)
	}
	if who.Tailnet != "a.ts.net" || len(who.Tenants) != 1 || who.Tenants[0].TenantID != tnA || who.Admin ||
		len(who.ApproveGrants) != 1 || who.ApproveGrants[0].Limits["amount"] != 100 || fmt.Sprint(who.ApproveGrants[0].Workflows) != "[refund]" {
		t.Fatalf("approver whoami: %s", body)
	}
	if c, body := get("100.64.0.7:1", "/v1/whoami"); c != 200 || !strings.Contains(body, `"tenants":[]`) || !strings.Contains(body, `"tailnet":"c.ts.net"`) {
		t.Fatalf("no-tenant whoami: %d %s", c, body)
	}
	// The local operator acts in every tenant.
	var local WhoAmI
	e.c.ok("GET", "/v1/whoami", nil, &local)
	if len(local.Tenants) != 2 || local.Tailnet != "" || len(local.ApproveGrants) != 0 {
		t.Fatalf("local whoami: %+v", local)
	}

	// The approvals page without ?tenant: bob's one tenant, no tenant box.
	c, page := get("100.64.0.5:1", "/ui/approvals")
	if c != 200 || !strings.Contains(page, ">small<") || strings.Contains(page, ">big<") || strings.Contains(page, `name="tenant" class="mono"`) ||
		!strings.Contains(page, `id="small"`) || !strings.Contains(page, "amount ≤ 100") || !strings.Contains(page, `<dl class="facts">`) {
		t.Fatalf("approver page: %d %s", c, page)
	}
	// No tenant on carol's tailnet: the box, and words saying why.
	c, page = get("100.64.0.7:1", "/ui/approvals")
	if c != 200 || !strings.Contains(page, `name="tenant"`) || !strings.Contains(page, "No tenant uses c.ts.net") {
		t.Fatalf("no-tenant page: %d %s", c, page)
	}
}

// TestAuditOrderDesc: GET /v1/audit?order=desc lists newest first and
// pages backwards with the cursor.
func TestAuditOrderDesc(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, tn := range []string{"a.ts.net", "b.ts.net", "c.ts.net"} {
		v1Tenant(t, e, tn, nil)
	}
	type pg struct {
		Items      []AuditRecord `json:"items"`
		NextCursor *string       `json:"nextCursor"`
	}
	var asc, d1, d2 pg
	e.c.ok("GET", "/v1/audit?kind=tenant_create", nil, &asc)
	e.c.ok("GET", "/v1/audit?kind=tenant_create&order=desc&limit=2", nil, &d1)
	if len(asc.Items) != 3 || len(d1.Items) != 2 || d1.NextCursor == nil || d1.Items[0].Hash != asc.Items[2].Hash || d1.Items[1].Hash != asc.Items[1].Hash {
		t.Fatalf("desc page 1: %+v", d1)
	}
	e.c.ok("GET", "/v1/audit?kind=tenant_create&order=desc&limit=2&cursor="+*d1.NextCursor, nil, &d2)
	if len(d2.Items) != 1 || d2.Items[0].Hash != asc.Items[0].Hash || d2.NextCursor != nil {
		t.Fatalf("desc page 2: %+v", d2)
	}
}
