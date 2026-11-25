package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
)

// fakeApprovalHost serves hostd's approvals push API: two pending
// approvals of workload "refunds", and decisions it records.
type fakeApprovalHost struct {
	mu        sync.Mutex
	decisions []map[string]any
}

func approvalHost(t *testing.T, e *testEnv, id, tenant string) *fakeApprovalHost {
	t.Helper()
	f := &fakeApprovalHost{}
	sock := filepath.Join(os.TempDir(), "tw-appr-"+id+"-"+filepath.Base(filepath.Dir(filepath.Dir(e.sock)))+".sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/approvals":
			if r.URL.Query().Get("tenant") != tenant {
				json.NewEncoder(w).Encode(map[string]any{"approvals": []any{}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"approvals": []map[string]any{
				{"tenantID": tenant, "workload": "refunds", "workflow": "refund", "instance": "big", "step": "wait-for-approval", "type": "approval", "status": "pending", "requestedAt": at, "facts": map[string]float64{"amount": 900}},
				{"tenantID": tenant, "workload": "refunds", "workflow": "refund", "instance": "small", "step": "wait-for-approval", "type": "approval", "status": "pending", "requestedAt": at.Add(time.Minute), "facts": map[string]float64{"amount": 20}},
			}})
		case r.Method == "POST" && r.URL.Path == "/v1/approvals/decide":
			var d map[string]any
			json.NewDecoder(r.Body).Decode(&d)
			f.mu.Lock()
			f.decisions = append(f.decisions, d)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if d["instance"] == "gone" {
				w.WriteHeader(409)
				io.WriteString(w, `{"code":"approval_not_pending","message":"no approval of refund/gone is waiting here"}`)
				return
			}
			if fmt.Sprint(d["grants"]) == "[]" {
				w.WriteHeader(403)
				io.WriteString(w, `{"code":"cap_missing","message":"no grant","snippet":"{}","missing":{"cap":"github.com/rosskukulinski/tailworks/cap/approve"}}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "approval": map[string]any{
				"tenantID": tenant, "workload": d["workload"], "workflow": d["workflow"], "instance": d["instance"], "step": "wait-for-approval",
				"type": "approval", "status": "approved", "requestedAt": at, "approver": d["approver"]}})
		default:
			w.WriteHeader(404)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); os.Remove(sock) })
	if _, _, err := e.m.RegisterHost(context.Background(), RegisterRequest{HostID: id, CapacityNodes: 100, Tiers: []string{"process"}, PushURL: "unix://" + sock}, "", ""); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestApprovals(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	tn := v1Tenant(t, e, "a.ts.net", nil)
	h1 := approvalHost(t, e, "h1", tn)
	if _, err := e.store.AcquireLease(context.Background(), LeaseName(tn, "refunds"), "h1", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	list := "/v1/tenants/" + tn + "/approvals"
	decide := "/v1/tenants/" + tn + "/workloads/refunds/approvals/refund/small"

	srv := &Server{M: e.m}
	whois := func(ctx context.Context, addr string) (WhoIsInfo, error) {
		switch addr {
		case "100.64.0.4:1": // a tailnet user with no tw grant at all
			return WhoIsInfo{NodeID: "nEVE", Name: "eve-laptop.a.ts.net.", Login: "eve@example.com"}, nil
		case "100.64.0.5:1": // an approver up to 100, not an admin
			return WhoIsInfo{NodeID: "nBOB", Name: "bob-laptop.a.ts.net.", Login: "bob@example.com", Caps: []string{approvecap.Cap},
				CapValues: map[string][]json.RawMessage{approvecap.Cap: {json.RawMessage(`{"workflows":["refund"],"limits":{"amount":100}}`)}}}, nil
		case "100.64.0.6:1": // an admin without an approve grant
			return WhoIsInfo{NodeID: "nADM", Name: "alice-laptop.a.ts.net.", Login: "alice@example.com", Caps: []string{AdminCap}}, nil
		}
		return WhoIsInfo{}, fmt.Errorf("unknown")
	}
	h := srv.TailnetHandler(whois, srv.Handler())
	call := func(from, method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		r.RemoteAddr = from
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// No grant: refused at the door (not_admin names both ways in).
	if c, b := call("100.64.0.4:1", "GET", list, ""); c != 403 || b["code"] != "not_admin" {
		t.Fatalf("no grant: %d %v", c, b)
	}
	// The admin sees everything; the approver only what its grants cover.
	if c, b := call("100.64.0.6:1", "GET", list, ""); c != 200 || len(b["approvals"].([]any)) != 2 {
		t.Fatalf("admin list: %d %v", c, b)
	}
	c, b := call("100.64.0.5:1", "GET", list, "")
	if c != 200 || len(b["approvals"].([]any)) != 1 || b["approvals"].([]any)[0].(map[string]any)["instance"] != "small" ||
		b["approvals"].([]any)[0].(map[string]any)["host"] != "h1" {
		t.Fatalf("approver list: %d %v", c, b)
	}
	// An approver is not an admin elsewhere.
	if c, b := call("100.64.0.5:1", "GET", "/v1/tenants", ""); c != 403 {
		t.Fatalf("approver on an admin route: %d %v", c, b)
	}

	// The approver decides: mgmt vouches for its WhoIs identity and sends
	// its grants; the host decides (and here approves).
	c, b = call("100.64.0.5:1", "POST", decide, `{"decision":"approve","comment":"fine","metadata":{"approver":"forged"}}`)
	if c != 200 || b["approval"].(map[string]any)["status"] != "approved" {
		t.Fatalf("decide: %d %v", c, b)
	}
	h1.mu.Lock()
	d := h1.decisions[len(h1.decisions)-1]
	h1.mu.Unlock()
	ap := d["approver"].(map[string]any)
	if ap["nodeId"] != "nBOB" || ap["user"] != "bob@example.com" || ap["node"] != "bob-laptop.a.ts.net." || ap["via"] != "mgmt" ||
		d["approve"] != true || d["comment"] != "fine" || !strings.Contains(fmt.Sprint(d["grants"]), "amount") {
		t.Fatalf("forwarded decision: %v", d)
	}

	// Being an admin is not a grant: the host refuses (cap_missing, relayed).
	if c, b := call("100.64.0.6:1", "POST", decide, `{"decision":"approve"}`); c != 403 || b["code"] != "cap_missing" || b["snippet"] == nil {
		t.Fatalf("admin without grant: %d %v", c, b)
	}
	// The operator (local socket) has no personal identity.
	e.c.fail("POST", decide, map[string]any{"decision": "approve"}, 403, "no_identity")
	// Nothing waiting: 409; unknown workload: 404; bad decision: 400.
	if c, b := call("100.64.0.5:1", "POST", "/v1/tenants/"+tn+"/workloads/refunds/approvals/refund/gone", `{"decision":"reject"}`); c != 409 || b["code"] != "approval_not_pending" {
		t.Fatalf("not pending: %d %v", c, b)
	}
	if c, b := call("100.64.0.5:1", "POST", "/v1/tenants/"+tn+"/workloads/nope/approvals/refund/small", `{"decision":"approve"}`); c != 404 {
		t.Fatalf("unknown workload: %d %v", c, b)
	}
	if c, b := call("100.64.0.5:1", "POST", decide, `{"decision":"maybe"}`); c != 400 {
		t.Fatalf("bad decision: %d %v", c, b)
	}

	// The admin UI: an approver who is not an admin gets the approvals
	// page (what its grants cover) and nothing else; the form fallback
	// decides as the browser's identity.
	page := func(from, method, path, form string) (int, string, http.Header) {
		r := httptest.NewRequest(method, path, strings.NewReader(form))
		r.RemoteAddr = from
		if form != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String(), w.Header()
	}
	if c, body, _ := page("100.64.0.5:1", "GET", "/ui/approvals?tenant="+tn, ""); c != 200 || !strings.Contains(body, ">small<") || strings.Contains(body, ">big<") || !strings.Contains(body, `action="/ui/approvals/decide"`) {
		t.Fatalf("approver UI: %d %s", c, body)
	}
	if c, body, _ := page("100.64.0.6:1", "GET", "/ui/approvals", ""); c != 200 || !strings.Contains(body, ">small<") || !strings.Contains(body, ">big<") {
		t.Fatalf("admin UI: %d %s", c, body)
	}
	if c, _, _ := page("100.64.0.5:1", "GET", "/ui/", ""); c != 403 {
		t.Fatalf("approver on the overview: %d", c)
	}
	if c, _, _ := page("100.64.0.4:1", "GET", "/ui/approvals", ""); c != 403 {
		t.Fatalf("no grant on the UI: %d", c)
	}
	c, _, hd := page("100.64.0.5:1", "POST", "/ui/approvals/decide", "tenant="+tn+"&workload=refunds&workflow=refund&instance=small&decision=reject&comment=nope")
	if c != 303 || !strings.Contains(hd.Get("Location"), "ok=") {
		t.Fatalf("UI decide: %d %v", c, hd)
	}
	h1.mu.Lock()
	d = h1.decisions[len(h1.decisions)-1]
	h1.mu.Unlock()
	if d["approve"] != false || d["comment"] != "nope" || d["approver"].(map[string]any)["nodeId"] != "nBOB" {
		t.Fatalf("UI decision at the host: %v", d)
	}
}
