//go:build sdk

package sdk

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestWorkflowApprovals runs a real Agents SDK AgentWorkflow that calls
// waitForApproval (ADR-040) on workerd's built-in Workflows
// engine, in both object modes, and checks the approval surface:
//
//   - the wait is listed (GET /_tw/approvals on the workload node) only to
//     a caller holding github.com/rosskukulinski/tailworks/cap/approve;
//   - without the grant (or outside its limits) the decision is refused
//     (cap_missing) and the instance keeps waiting; app code approving its
//     own Workflow (approveWorkflow) is refused too;
//   - hostd restarts while the instances wait; they still wait afterwards;
//   - with the grant, the approval resumes the Workflow, which sees the
//     approver's WireGuard identity, not the forged one in the request;
//   - a failed step retried from itself: completed steps did not run again;
//   - every request for approval, refusal and decision is audited.
func TestWorkflowApprovals(t *testing.T) {
	s := startSuite(t)
	dep := s.a.Deploy(app("approvals"), "approvals", nil)
	wl := s.a.WaitActive("approvals", dep, 90*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP
	modes := []struct{ class, kebab, id string }{
		{"RefundAgent", "refund-agent", "wf-facet"},
		{"PlainRefundAgent", "plain-refund-agent", "wf-plain"},
	}
	grant := func(grants ...string) {
		caps := tailcfg.PeerCapMap{
			nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
			mcpCap:          []tailcfg.RawMessage{`{"tools":["lookup_order","whoami"]}`},
		}
		for _, g := range grants {
			caps[hostd.ApproveCap] = append(caps[hostd.ApproveCap], tailcfg.RawMessage(g))
		}
		s.ctl.Server.SetGlobalAppCaps(caps)
	}
	status := func(kebab, id string) map[string]any {
		_, r := s.getJSON(base + "/agents/" + kebab + "/r1/status?id=" + id)
		return r
	}
	decide := func(id, body string) (int, map[string]any) {
		code, _, b := s.do("POST", base+"/_tw/approvals/refund/"+id, []byte(body), "content-type", "application/json")
		return code, parse(b)
	}

	// Start one instance per mode; each fails its "quote" step once.
	for _, m := range modes {
		code, r := s.getJSON(base + "/agents/" + m.kebab + "/r1/start?amount=50&failQuote=1&id=" + m.id)
		if code != 200 || r["id"] != m.id {
			t.Fatalf("%s start: %d %v", m.class, code, r)
		}
	}
	for _, m := range modes {
		eventually(t, 30*time.Second, m.class+" waits for approval", func() (bool, any) {
			r := status(m.kebab, m.id)
			return get(r, "status", "status") == "waiting" && get(r, "steps", "quote-attempt") == float64(2), r
		})
	}

	t.Run("RetryFromFailedStep", func(t *testing.T) {
		for _, m := range modes {
			r := status(m.kebab, m.id)
			if get(r, "steps", "lookup") != float64(1) || get(r, "steps", "quote-attempt") != float64(2) {
				t.Fatalf("%s: lookup must run once, quote twice: %v", m.class, r["steps"])
			}
		}
	})

	t.Run("NoGrantRefused", func(t *testing.T) {
		code, _, b := s.do("GET", base+"/_tw/approvals", nil)
		if code != 403 || parse(b)["code"] != "cap_missing" {
			t.Fatalf("list without grant: %d %s", code, b)
		}
		for _, m := range modes {
			code, r := decide(m.id, `{"decision":"approve"}`)
			if code != 403 || r["code"] != "cap_missing" {
				t.Fatalf("%s approve without grant: %d %v", m.class, code, r)
			}
			// A form post (not JSON) is refused before anything else.
			code, _, b = s.do("POST", base+"/_tw/approvals/refund/"+m.id, []byte("decision=approve"), "content-type", "application/x-www-form-urlencoded")
			if code != 400 {
				t.Fatalf("form post: %d %s", code, b)
			}
			// App code approving its own Workflow is refused.
			_, r = s.getJSON(base + "/agents/" + m.kebab + "/r1/self-approve?id=" + m.id)
			if !strings.Contains(js(r), "approval_requires_identity") {
				t.Fatalf("%s self-approve: %v", m.class, r)
			}
			if st := status(m.kebab, m.id); get(st, "status", "status") != "waiting" {
				t.Fatalf("%s moved on: %v", m.class, st)
			}
		}
	})

	t.Run("GrantLimits", func(t *testing.T) {
		// Granted, but only up to amount 10 (the instance's param is 50).
		grant(`{"workflows":["refund"],"limits":{"amount":10}}`)
		eventually(t, 20*time.Second, "limit refusal", func() (bool, any) {
			code, r := decide(modes[0].id, `{"decision":"approve"}`)
			return code == 403 && r["code"] == "cap_missing" && strings.Contains(js(r), "limits.amount"), r
		})
		if st := status(modes[0].kebab, modes[0].id); get(st, "status", "status") != "waiting" {
			t.Fatalf("moved on: %v", st)
		}
	})

	t.Run("SurvivesHostdRestart", func(t *testing.T) {
		grant(`{"workflows":["refund"]}`)
		s.a.Restart()
		w2 := s.a.WaitActive("approvals", dep, 90*time.Second)
		s.waitPeer(w2.IP)
		base = "http://" + w2.IP
		eventually(t, 30*time.Second, "both waits listed after the restart", func() (bool, any) {
			code, _, b := s.do("GET", base+"/_tw/approvals", nil)
			var r struct {
				Approvals []hostd.Approval `json:"approvals"`
			}
			json.Unmarshal(b, &r)
			n := 0
			for _, a := range r.Approvals {
				if a.Workflow == "refund" && a.Step == "wait-for-approval" && a.Status == hostd.ApprovalPending && a.Facts["amount"] == 50 {
					n++
				}
			}
			return code == 200 && n == 2, string(b)
		})
		for _, m := range modes {
			if st := status(m.kebab, m.id); get(st, "status", "status") != "waiting" {
				t.Fatalf("%s after restart: %v", m.class, st)
			}
		}
	})

	t.Run("ApproveWithIdentity", func(t *testing.T) {
		for _, m := range modes {
			// The request names a forged approver twice; tailworks ignores both.
			code, r := decide(m.id, `{"decision":"approve","comment":"ok by me","approver":{"user":"forged@example.com"},"metadata":{"approver":{"user":"forged@example.com"},"note":"n1"}}`)
			if code != 200 || get(r, "approval", "status") != "approved" {
				t.Fatalf("%s approve: %d %v", m.class, code, r)
			}
			var out map[string]any
			eventually(t, 30*time.Second, m.class+" completes", func() (bool, any) {
				out = status(m.kebab, m.id)
				return get(out, "status", "status") == "complete" && out["outcome"] != nil, out
			})
			res, _ := get(out, "outcome", "result").(map[string]any)
			if get(res, "approver", "nodeId") != s.clientID || get(res, "approver", "via") != "tailnet" ||
				!strings.HasPrefix(get(res, "approver", "node").(string), "ttest-client.") || res["comment"] != "ok by me" || res["note"] != "n1" {
				t.Fatalf("%s: the Workflow saw %v (%v)", m.class, res, out)
			}
			if strings.Contains(js(out), "forged") {
				t.Fatalf("%s: forged approver reached the Workflow: %v", m.class, out)
			}
			if get(out, "steps", "lookup") != float64(1) || get(out, "steps", "refund") != float64(1) {
				t.Fatalf("%s: steps ran again: %v", m.class, out["steps"])
			}
			// Decided: a second decision is refused.
			if code, r := decide(m.id, `{"decision":"reject"}`); code != 409 || r["code"] != "approval_not_pending" {
				t.Fatalf("%s second decision: %d %v", m.class, code, r)
			}
		}
	})

	t.Run("Reject", func(t *testing.T) {
		code, r := s.getJSON(base + "/agents/refund-agent/r1/start?amount=7&id=wf-rej")
		if code != 200 {
			t.Fatalf("start: %d %v", code, r)
		}
		eventually(t, 30*time.Second, "waits", func() (bool, any) {
			r := status("refund-agent", "wf-rej")
			return get(r, "status", "status") == "waiting", r
		})
		eventually(t, 20*time.Second, "reject", func() (bool, any) {
			code, r := decide("wf-rej", `{"decision":"reject","comment":"not this one"}`)
			return code == 200, r
		})
		eventually(t, 30*time.Second, "rejected run reports the reason", func() (bool, any) {
			r := status("refund-agent", "wf-rej")
			return strings.Contains(js(r["outcome"]), "not this one"), r
		})
		if st := status("refund-agent", "wf-rej"); get(st, "steps", "refund") != nil {
			t.Fatalf("a rejected refund ran: %v", st)
		}
	})

	t.Run("ViaMgmtPushAPI", func(t *testing.T) {
		// What mgmt sends for `tw approve` / the admin UI: the approver's
		// WhoIs identity and their grants toward mgmt, to the host's push API.
		code, r := s.getJSON(base + "/agents/plain-refund-agent/r1/start?amount=5&id=wf-mgmt")
		if code != 200 {
			t.Fatalf("start: %d %v", code, r)
		}
		push := s.a.Host.PushHandler()
		pushDo := func(method, path, body string) (int, map[string]any) {
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			w := httptest.NewRecorder()
			push.ServeHTTP(w, req)
			return w.Code, parse(w.Body.Bytes())
		}
		eventually(t, 30*time.Second, "listed on the push API", func() (bool, any) {
			code, r := pushDo("GET", "/v1/approvals?tenant="+s.a.TenantID, "")
			return code == 200 && strings.Contains(js(r), `"instance":"wf-mgmt"`), r
		})
		bob := `"approver":{"user":"bob@example.com","node":"bob-laptop.ttest.ts.net.","nodeId":"nBOB","tags":[]}`
		body := `{"tenant":"` + s.a.TenantID + `","workload":"approvals","workflow":"refund","instance":"wf-mgmt","approve":true,` + bob + `,"grants":[]}`
		if code, r := pushDo("POST", "/v1/approvals/decide", body); code != 403 || r["code"] != "cap_missing" {
			t.Fatalf("no grants: %d %v", code, r)
		}
		body = strings.Replace(body, `"grants":[]`, `"grants":[{"workflows":["refund"],"decisions":["approve"]}]`, 1)
		if code, r := pushDo("POST", "/v1/approvals/decide", body); code != 200 || get(r, "approval", "approver", "via") != "mgmt" {
			t.Fatalf("decide: %d %v", code, r)
		}
		eventually(t, 30*time.Second, "completes", func() (bool, any) {
			r := status("plain-refund-agent", "wf-mgmt")
			return get(r, "outcome", "result", "approver", "user") == "bob@example.com" && get(r, "outcome", "result", "approver", "via") == "mgmt", r
		})
	})

	t.Run("Audit", func(t *testing.T) {
		eventually(t, 20*time.Second, "approval audit records", func() (bool, any) {
			recs := s.a.Audit(wl.ID).Items
			kinds := map[string]int{}
			decided := 0
			for _, r := range recs {
				if !strings.HasPrefix(r.Kind, "approval_") {
					continue
				}
				kinds[r.Kind]++
				var d map[string]any
				json.Unmarshal(r.Detail, &d)
				if r.Kind == "approval_decision" && get(d, "approver", "nodeId") == s.clientID && d["workflow"] == "refund" && d["instance"] != nil {
					decided++
				}
			}
			return kinds["approval_requested"] >= 4 && kinds["approval_denied"] >= 4 && decided == 3, kinds
		})
	})
}
