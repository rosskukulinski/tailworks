package ui

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func (f *fakeAPI) called(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// TestObjectsSearch: /ui/objects passes tenant, workload and status to
// /v1 and filters tag and acting-for itself, newest first, 50 a page.
func TestObjectsSearch(t *testing.T) {
	api := populatedAPI()
	now := time.Now().UnixMilli()
	var objs []any
	for i := range 120 {
		tag := "tag:a"
		if i%2 == 1 {
			tag = "tag:b"
		}
		objs = append(objs, map[string]any{"objectID": fmt.Sprintf("obj_%03d", i), "tenantID": "tn_1", "workload": "w", "namespace": "A", "objectName": fmt.Sprintf("n%03d", i),
			"hostname": fmt.Sprintf("host-%03d", i), "tags": []string{tag}, "status": "ready", "actingFor": "ross@x", "createdAt": now - int64(120-i)*1000})
	}
	api.routes["GET /v1/objects"] = pageOf(objs...)
	h := serve(t, api)
	body := get(t, h, "/ui/objects?tenant=tn_1&workload=w&status=ready&tag=b&caller=ROSS").Body.String()
	if !api.called("state=ready") || !api.called("workload=w") || !api.called("tenant=tn_1") {
		t.Fatalf("server-side filters not sent: %v", api.calls)
	}
	if !strings.Contains(body, `<span class="count">60</span>`) || !strings.Contains(body, "Showing 1–50 of 60") || strings.Contains(body, "host-118") ||
		!strings.Contains(body, "Older →") || strings.Contains(body, "← Newer") {
		t.Fatalf("page 1: %s", body)
	}
	if i, j := strings.Index(body, "host-119"), strings.Index(body, "host-117"); i < 0 || j < 0 || i > j {
		t.Fatalf("not newest first")
	}
	body = get(t, h, "/ui/objects?tenant=tn_1&workload=w&status=ready&tag=b&caller=ROSS&page=2").Body.String()
	if !strings.Contains(body, "Showing 51–60 of 60") || !strings.Contains(body, "← Newer") || strings.Contains(body, "Older →") || !strings.Contains(body, "host-001") {
		t.Fatalf("page 2: %s", body)
	}
}

// TestAuditFilters: /ui/audit asks for newest first with every filter /v1
// has, keeps them in the paging links, and filters the app itself.
func TestAuditFilters(t *testing.T) {
	api := populatedAPI()
	h := serve(t, api)
	body := get(t, h, "/ui/audit?tenant=tn_1&kind=kill&human=alice@x&workload=nope").Body.String()
	if !api.called("order=desc") || !api.called("kind=kill") || !api.called("tenant=tn_1") || !api.called("human=alice%40x") {
		t.Fatalf("filters not sent: %v", api.calls)
	}
	if !strings.Contains(body, "it hid 7 records on this page") || !strings.Contains(body, "Older →") || !strings.Contains(body, "cursor=c2") ||
		!strings.Contains(body, "workload=nope") || strings.Contains(body, "← Newest") {
		t.Fatalf("audit page: %s", body)
	}
	body = get(t, h, "/ui/audit?cursor=c1&workload=w").Body.String()
	if !strings.Contains(body, "← Newest") || !strings.Contains(body, ">kill<") {
		t.Fatalf("audit page 2: %s", body)
	}
}

// TestApprovalsDefaults: an approver who is not an admin sees the tenants
// whoami names, without a tenant box when there is one; rows carry an
// anchor and the facts as a definition list.
func TestApprovalsDefaults(t *testing.T) {
	api := populatedAPI()
	api.routes["GET /v1/whoami"] = map[string]any{"kind": "user", "login": "bob@x", "admin": false, "actor": "bob@x", "mgmt": map[string]any{},
		"tailnet": "t.ts.net", "tenants": []any{map[string]any{"tenantID": "tn_1", "tailnet": "t.ts.net"}},
		"approveGrants": []any{map[string]any{"workflows": []string{"refund"}, "limits": map[string]float64{"amount": 100}}}}
	api.routes["GET /v1/tenants/tn_1/approvals"] = map[string]any{"approvals": []any{
		map[string]any{"tenantID": "tn_1", "workload": "refunds", "workflow": "refund", "instance": "inst-7", "step": "wait-for-approval", "type": "approval",
			"status": "pending", "requestedAt": time.Now().Add(-time.Hour), "facts": map[string]float64{"amount": 42, "items": 3}, "host": "h1"}}}
	h := serve(t, api)
	body := get(t, h, "/ui/approvals").Body.String()
	for _, want := range []string{`<tr id="inst-7">`, `href="#inst-7"`, `<dl class="facts"><dt>amount</dt><dd class="mono">42</dd><dt>items</dt>`,
		"amount ≤ 100", "Workflows refund", `title="tn_1">t.ts.net</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("approver page lacks %q", want)
		}
	}
	if strings.Contains(body, `list="approval-tenants"`) || api.called("GET /v1/tenants?") || strings.Contains(body, `href="/ui/audit"`) {
		t.Errorf("approver page: tenant box, tenant listing or admin links:\n%s", body)
	}
	// The form fallback returns to the row.
	r := httptest.NewRequest("POST", "http://twd-mgmt.t.ts.net/ui/approvals/decide", strings.NewReader("tenant=tn_1&workload=refunds&workflow=refund&instance=inst-7&decision=approve"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if loc := w.Header().Get("Location"); w.Code != 303 || !strings.HasSuffix(loc, "#inst-7") {
		t.Errorf("decide redirect: %d %q", w.Code, loc)
	}

	// An admin with two tenants: the box, with the tenants to pick from.
	api = populatedAPI()
	api.routes["GET /v1/tenants"] = pageOf(map[string]any{"tenantID": "tn_1", "tailnet": "t.ts.net"}, map[string]any{"tenantID": "tn_2", "tailnet": "u.ts.net"})
	body = get(t, serve(t, api), "/ui/approvals").Body.String()
	if !strings.Contains(body, `list="approval-tenants"`) || !strings.Contains(body, `<option value="tn_2">u.ts.net</option>`) || !api.called("/v1/tenants/tn_2/approvals") {
		t.Errorf("admin page: %s", body)
	}
}
