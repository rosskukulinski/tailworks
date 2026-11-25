package hostd

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripFunc is an http.RoundTripper from a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// One decision per approval (#561, found by e2e/dataplane W3): while one
// decision is being delivered to the Workflow, a second one, from another
// approver or the same approver retrying, is refused, not delivered too.
// A failed delivery leaves the approval pending, so a retry can decide it.
func TestDecideApprovalOnce(t *testing.T) {
	t.Parallel()
	h, inst := approvalsHost(t, t.TempDir())
	report(t, h, inst, "requested", "wf1", 50)
	var deliveries atomic.Int32
	entered := make(chan struct{}, 4)
	hold := make(chan struct{})
	fail := atomic.Bool{}
	inst.direct = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deliveries.Add(1)
		entered <- struct{}{}
		<-hold
		if fail.Load() {
			return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(`{"code":"upstream_unavailable","message":"engine down"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	inst.w.active = inst
	grants := []json.RawMessage{json.RawMessage(`{"workflows":["refund"]}`)}
	decide := func(node string, approve bool) (*Approval, *apiError) {
		return h.decideApproval(approvalDecision{TenantID: "tn_a", Workload: "refunds", Workflow: "refund", Instance: "wf1", Approve: approve,
			Approver: Approver{NodeID: node, Node: node + ".ts.net.", Tags: []string{}, Via: "tailnet"}, Grants: grants})
	}
	type res struct {
		a *Approval
		e *apiError
	}
	// second decides while a delivery is held: it must come back without
	// reaching the Workflow (before #561 it was delivered too).
	second := func(node string, approve bool) res {
		ch := make(chan res, 1)
		go func() { a, e := decide(node, approve); ch <- res{a, e} }()
		select {
		case r := <-ch:
			return r
		case <-entered:
			t.Fatal("a second decision was delivered while the first was being delivered")
		case <-time.After(10 * time.Second):
			t.Fatal("the second decision hung")
		}
		return res{}
	}

	// A delivery that fails leaves it pending, and a concurrent decision
	// meanwhile is refused.
	fail.Store(true)
	first := make(chan res, 1)
	go func() { a, e := decide("nA", true); first <- res{a, e} }()
	<-entered
	t.Cleanup(func() { // unblock held deliveries if the test fails early
		select {
		case <-hold:
		default:
			close(hold)
		}
	})
	if r := second("nB", false); r.e == nil || r.e.Code != "approval_not_pending" || !strings.Contains(r.e.Detail, "being decided") {
		t.Fatalf("a decision during another's delivery: %v", r.e)
	}
	close(hold)
	if r := <-first; r.e == nil || r.e.Code != "upstream_unavailable" {
		t.Fatalf("failed delivery: %+v", r)
	}
	if l := h.ListApprovals("tn_a", true); len(l) != 1 {
		t.Fatalf("not pending after a failed delivery: %+v", l)
	}

	// Two at once: exactly one is delivered and accepted.
	fail.Store(false)
	hold = make(chan struct{})
	deliveries.Store(0)
	out := make(chan res, 2)
	go func() { a, e := decide("nA", true); out <- res{a, e} }()
	<-entered
	if refused := second("nB", false); refused.e == nil || refused.e.Code != "approval_not_pending" {
		t.Fatalf("second decision: %+v", refused)
	}
	close(hold)
	won := <-out
	if won.e != nil || won.a.Status != ApprovalApproved || won.a.Approver.NodeID != "nA" {
		t.Fatalf("first decision: %+v %v", won.a, won.e)
	}
	select {
	case <-entered:
		t.Fatal("the refused decision was delivered too")
	case <-time.After(50 * time.Millisecond):
	}
	if n := deliveries.Load(); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
	if k := auditKinds(h); k[kindApprovalDecision] != 1 {
		t.Fatalf("audit %v", k)
	}
	// And afterwards it says decided.
	if _, e := decide("nB", false); e == nil || e.Code != "approval_not_pending" || !strings.Contains(e.Detail, "approved") {
		t.Fatalf("after: %v", e)
	}
}
