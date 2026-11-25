package hostd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// holdEgress (issue #331): an object's egress goes only once mgmt has
// accepted a ready report for the node it leaves from; otherwise it waits,
// bounded, and is refused with a typed error, never let through.
func TestHoldEgress(t *testing.T) {
	o := &object{ID: "obj-1", NetdID: "n1", TenantID: "t1", Status: statusReady, NodeID: "NODE1"}
	h := &Host{byNetd: map[string]*object{"n1": o}}
	code := func(err error) string {
		if err == nil {
			return ""
		}
		e, ok := netd.AsError(err)
		if !ok {
			t.Fatalf("not a *netd.Error: %v", err)
		}
		return e.Code
	}
	short := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 100*time.Millisecond)
	}

	// Not reported yet: held until the context (the egress's own bound) ends.
	ctx, cancel := short()
	st := time.Now()
	err := h.holdEgress(ctx, "t1", "n1", "NODE1")
	cancel()
	if code(err) != netd.CodeIdentityPending || time.Since(st) < 90*time.Millisecond {
		t.Fatalf("unreported node: %v after %v", err, time.Since(st))
	}

	// A report while it waits lets it go.
	done := make(chan error, 1)
	go func() { done <- h.holdEgress(context.Background(), "t1", "n1", "NODE1") }()
	time.Sleep(50 * time.Millisecond)
	h.noteReport(o, "NODE1", nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("after the report: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the report did not wake the held egress")
	}

	// The node changed (re-minted): held again until the new one is reported.
	ctx, cancel = short()
	err = h.holdEgress(ctx, "t1", "n1", "NODE2")
	cancel()
	if code(err) != netd.CodeIdentityPending {
		t.Fatalf("new node before its report: %v", err)
	}
	// A failed report of the new node refuses at once (a re-send is not due
	// yet), without the old node's report standing in for it.
	o.NodeID = "NODE2"
	h.noteReport(o, "NODE2", errors.New("mgmt down"))
	st = time.Now()
	err = h.holdEgress(context.Background(), "t1", "n1", "NODE2")
	if code(err) != netd.CodeIdentityPending || time.Since(st) > time.Second {
		t.Fatalf("failed report: %v after %v", err, time.Since(st))
	}
	// A failed re-report of a node mgmt already has keeps it.
	h.noteReport(o, "NODE1", errors.New("timeout"))
	if err := h.holdEgress(context.Background(), "t1", "n1", "NODE1"); err != nil {
		t.Fatalf("a node mgmt accepted earlier: %v", err)
	}

	// Unknown node, another tenant's, a killed object: refused.
	if c := code(h.holdEgress(context.Background(), "t1", "n9", "NODE1")); c != netd.CodeUnknownObject {
		t.Fatalf("unknown node: %s", c)
	}
	if c := code(h.holdEgress(context.Background(), "t2", "n1", "NODE1")); c != netd.CodeUnknownObject {
		t.Fatalf("another tenant's node: %s", c)
	}
	o.Status = statusKilled
	if c := code(h.holdEgress(context.Background(), "t1", "n1", "NODE1")); c != netd.CodeKilled {
		t.Fatalf("killed: %s", c)
	}
}
