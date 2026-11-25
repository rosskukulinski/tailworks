package hostd

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

func approvalsHost(t *testing.T, dir string) (*Host, *instance) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(nil)
	h := &Host{cfg: Config{DataDir: dir}, audit: newAuditLog(key), workloads: map[string]*workload{}}
	wl := &workload{h: h, tenantID: "tn_a", name: "refunds"}
	h.workloads["tn_a/refunds"] = wl
	app := &bundle.App{Workflows: []bundle.Workflow{{Name: "refund", Binding: "REFUND", Class: "RefundWorkflow"}}}
	return h, &instance{h: h, w: wl, app: app}
}

func report(t *testing.T, h *Host, inst *instance, op, instance string, amount float64) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"op": op, "workflow": "refund", "instance": instance, "step": "wait-for-approval", "type": "approval", "facts": map[string]float64{"amount": amount}})
	if _, e := h.approvalReport(inst, raw); e != nil {
		t.Fatalf("%s %s: %v", op, instance, e)
	}
}

func auditKinds(h *Host) map[string]int {
	h.audit.mu.Lock()
	defer h.audit.mu.Unlock()
	out := map[string]int{}
	for _, r := range h.audit.pending {
		out[r.Kind]++
	}
	return out
}

// Reports are idempotent per wait, persist across a hostd restart, and a
// timeout settles the wait (rejected by default).
func TestApprovalRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, inst := approvalsHost(t, dir)
	report(t, h, inst, "requested", "wf1", 50)
	report(t, h, inst, "requested", "wf1", 50) // a retried report
	report(t, h, inst, "requested", "wf2", 5)
	if k := auditKinds(h); k[kindApprovalRequested] != 2 {
		t.Fatalf("audit %v", k)
	}
	raw, _ := json.Marshal(map[string]any{"op": "requested", "workflow": "other", "instance": "x", "step": "s", "type": "approval"})
	if _, e := h.approvalReport(inst, raw); e == nil || e.Code != "bad_request" {
		t.Fatalf("unknown Workflow: %v", e)
	}

	h2, inst2 := approvalsHost(t, dir) // a restarted hostd
	if l := h2.ListApprovals("tn_a", true); len(l) != 2 || l[0].Instance != "wf1" || l[0].Facts["amount"] != 50 {
		t.Fatalf("after restart: %+v", l)
	}
	report(t, h2, inst2, "timeout", "wf2", 5)
	if l := h2.ListApprovals("tn_a", true); len(l) != 1 {
		t.Fatalf("pending after timeout: %+v", l)
	}
	if l := h2.ListApprovals("tn_a", false); len(l) != 2 || l[1].Status != ApprovalTimedOut {
		t.Fatalf("all after timeout: %+v", l)
	}
	if k := auditKinds(h2); k[kindApprovalTimeout] != 1 {
		t.Fatalf("audit %v", k)
	}
	if l := h2.ListApprovals("tn_other", false); len(l) != 0 {
		t.Fatalf("another tenant sees %v", l)
	}
}

func TestAuthorizeApproval(t *testing.T) {
	t.Parallel()
	a := &Approval{Workload: "refunds", Workflow: "refund", Step: "wait-for-approval", Facts: map[string]float64{"amount": 700}}
	g := func(s ...string) []json.RawMessage {
		var out []json.RawMessage
		for _, x := range s {
			out = append(out, json.RawMessage(x))
		}
		return out
	}
	for _, tc := range []struct {
		grants   []json.RawMessage
		decision string
		ok       bool
	}{
		{nil, "approve", false},
		{g(`{}`), "approve", true},
		{g(`{"workflows":["refund"]}`), "approve", true},
		{g(`{"workflows":["other"]}`), "approve", false},
		{g(`{"workloads":["*"],"steps":["wait-for-approval"]}`), "approve", true},
		{g(`{"steps":["manager"]}`), "approve", false},
		{g(`{"decisions":["reject"]}`), "approve", false},
		{g(`{"decisions":["reject"]}`), "reject", true},
		{g(`{"limits":{"amount":500}}`), "approve", false},
		{g(`{"limits":{"amount":500}}`, `{"limits":{"amount":1000}}`), "approve", true}, // union
		{g(`{"limits":{"usd":1000}}`), "approve", false},                                // no such param: fail closed
		{g(`not json`, `{}`), "approve", true},
	} {
		e := authorizeApproval(tc.grants, a, tc.decision, "bob@example.com")
		if (e == nil) != tc.ok {
			t.Errorf("%s %s: %v, want ok=%v", tc.grants, tc.decision, e, tc.ok)
		}
		if e != nil && (e.Code != "cap_missing" || !strings.Contains(e.Extra["snippet"].(string), ApproveCap)) {
			t.Errorf("refusal %v", e)
		}
	}
}

// The endpoint on the workload's node: tailnet identities only, JSON posts
// only, grants checked (and refusals audited) before anything is delivered.
func TestServeApprovalsRefusals(t *testing.T) {
	t.Parallel()
	h, inst := approvalsHost(t, t.TempDir())
	report(t, h, inst, "requested", "wf1", 50)
	wl := inst.w
	caller := func(caps map[string]any) map[string]any {
		return map[string]any{"tailscale": map[string]any{"caller": map[string]any{"nodeId": "nBOB", "node": "bob.ts.net.", "user": "bob@example.com", "tags": []string{}, "capMap": caps}}}
	}
	serve := func(method, path, ct, body string, blob map[string]any) (bool, int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		w := httptest.NewRecorder()
		ok := h.serveApprovals(w, r, wl, blob)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return ok, w.Code, out
	}
	if ok, _, _ := serve("GET", "/agents/x", "", "", caller(nil)); ok {
		t.Fatal("other paths are the app's")
	}
	pub := map[string]any{"tailscale": map[string]any{"requester": map[string]any{"kind": "public"}}}
	if _, c, r := serve("GET", "/_tw/approvals", "", "", pub); c != http.StatusForbidden || r["code"] != "public_request" {
		t.Fatalf("public: %d %v", c, r)
	}
	if _, c, r := serve("GET", "/_tw/approvals", "", "", map[string]any{}); c != 401 || r["code"] != "no_identity" {
		t.Fatalf("no caller: %d %v", c, r)
	}
	if _, c, r := serve("GET", "/_tw/approvals", "", "", caller(nil)); c != 403 || r["code"] != "cap_missing" {
		t.Fatalf("list without grant: %d %v", c, r)
	}
	granted := caller(map[string]any{ApproveCap: []any{map[string]any{"workflows": []string{"refund"}}}})
	if _, c, r := serve("GET", "/_tw/approvals", "", "", granted); c != 200 || len(r["approvals"].([]any)) != 1 {
		t.Fatalf("list: %d %v", c, r)
	}
	if _, c, _ := serve("POST", "/_tw/approvals/refund/wf1", "text/plain", `{"decision":"approve"}`, granted); c != 400 {
		t.Fatalf("not JSON: %d", c)
	}
	if _, c, _ := serve("OPTIONS", "/_tw/approvals/refund/wf1", "", "", granted); c != 400 {
		t.Fatalf("preflight: %d", c)
	}
	if _, c, r := serve("POST", "/_tw/approvals/refund/wf1", "application/json", `{"decision":"approve"}`, caller(nil)); c != 403 || r["code"] != "cap_missing" {
		t.Fatalf("approve without grant: %d %v", c, r)
	}
	if _, c, r := serve("POST", "/_tw/approvals/refund/nope", "application/json", `{"decision":"approve"}`, granted); c != 409 || r["code"] != "approval_not_pending" {
		t.Fatalf("nothing waiting: %d %v", c, r)
	}
	if _, c, _ := serve("POST", "/_tw/approvals/refund/wf1", "application/json", `{"decision":"maybe"}`, granted); c != 400 {
		t.Fatalf("bad decision: %d", c)
	}
	if k := auditKinds(h); k[kindApprovalDenied] != 1 {
		t.Fatalf("refusal not audited: %v", k)
	}
}
