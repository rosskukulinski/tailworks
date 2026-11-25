//go:build sdk

package sdk

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestRefundApprovalTemplate: the create-tailworks `refund-approval` template. A
// support user asks for a refund (the Workflow records who asked); a
// different tailnet identity holding github.com/rosskukulinski/tailworks/cap/approve approves it on the
// workload's node, and the refund completes naming the approver. The
// requester approving their own refund passes tailworks' grant check but is
// refused by the Workflow's own rule.
func TestRefundApprovalTemplate(t *testing.T) {
	s := startSuite(t)
	s.ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
		nettest.TestCap:  []tailcfg.RawMessage{nettest.TestCapValue},
		hostd.ApproveCap: []tailcfg.RawMessage{`{"workflows":["refund"],"limits":{"amount":100}}`},
	})
	d := s.a.Deploy(hostdtest.Template("refund-approval"), "refunds", nil)
	wl := s.a.WaitActive("refunds", d, 90*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP

	approverNode, err := nettest.NewNode(context.Background(), s.ctl.URL, nettest.DummyKey, "ttest-approver", filepath.Join(s.a.Dir, "approver"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { approverNode.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := approverNode.WaitPeers(ctx, wl.IP); err != nil {
		t.Fatal(err)
	}
	approver := approverNode.HTTPClient(30 * time.Second)
	decideAs := func(c *http.Client, id, body string) (int, map[string]any) {
		resp, err := c.Post(base+"/_tw/approvals/refund/"+id, "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			return 0, map[string]any{"err": err.Error()}
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, parse(b)
	}
	refund := func(order string, amount string) string {
		code, _, b := s.do("POST", base+"/agents/refund-agent/cust1/refund?order="+order+"&amount="+amount, nil)
		r := parse(b)
		id, _ := r["id"].(string)
		if code != 200 || id == "" {
			t.Fatalf("refund %s: %d %s", order, code, b)
		}
		// The hint names the workload, read from ctx.tailnet.self().workload.
		if r["approve"] != "tw approve refunds "+id {
			t.Errorf("refund %s: approve hint %v", order, r["approve"])
		}
		return id
	}
	state := func(id string) map[string]any {
		_, r := s.getJSON(base + "/agents/refund-agent/cust1/refunds")
		rows, _ := r["refunds"].([]any)
		for _, row := range rows {
			if m, _ := row.(map[string]any); m["id"] == id {
				return m
			}
		}
		return nil
	}

	// The page is the app's; /_tw/approvals on the same node is tailworks'.
	if code, _, b := s.do("GET", base+"/", nil); code != 200 || !strings.Contains(string(b), "/_tw/approvals") {
		t.Fatalf("page: %d", code)
	}

	id := refund("o-1", "42")
	eventually(t, 30*time.Second, "approver sees the refund", func() (bool, any) {
		resp, err := approver.Get(base + "/_tw/approvals")
		if err != nil {
			return false, err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode == 200 && strings.Contains(string(b), id) && strings.Contains(string(b), `"amount":42`), string(b)
	})
	if code, r := decideAs(approver, id, `{"decision":"approve","comment":"customer called"}`); code != 200 {
		t.Fatalf("approve: %d %v", code, r)
	}
	eventually(t, 30*time.Second, "refunded", func() (bool, any) {
		st := state(id)
		return st["state"] == "refunded" && strings.Contains(js(st["detail"]), "customer called") && strings.Contains(js(st["detail"]), "approvedBy"), st
	})

	// Above the grant's limit: refused by tailworks, still waiting.
	big := refund("o-2", "500")
	eventually(t, 30*time.Second, "over the limit", func() (bool, any) {
		code, r := decideAs(approver, big, `{"decision":"approve"}`)
		return code == 403 && r["code"] == "cap_missing", r
	})

	// The requester approving their own: tailworks delivers it (the grant covers
	// it), the Workflow's own rule refuses it.
	own := refund("o-3", "10")
	eventually(t, 30*time.Second, "self-approval delivered", func() (bool, any) {
		code, r := decideAs(s.hc, own, `{"decision":"approve"}`)
		return code == 200, r
	})
	eventually(t, 30*time.Second, "self-approval refused by the Workflow", func() (bool, any) {
		st := state(own)
		return st["state"] == "not refunded" && strings.Contains(js(st["detail"]), "cannot approve"), st
	})
}
