package mgmt

import (
	"context"
	"testing"
)

// TestQueuesStaleRelease (#431): after the lease moves, the previous
// holder can still hand back a batch it never ran. The messages are
// visible again at once and their attempt is taken back, so the new holder
// gets them as first deliveries. Acks, retries and extends from the old
// epoch stay refused, and a release from an epoch that never existed is
// refused.
func TestQueuesStaleRelease(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	h1, h2 := q.e.asHost("h1"), q.e.asHost("h2")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	h1.ok("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(`1`, "json"), qmsg(`2`, "json")}}, nil)
	// A long visibility timeout: without the release, the new holder would
	// wait a minute for these.
	r := q.pull(h1, "jobs", q.epoch, map[string]any{"visibilityTimeoutMs": 60000, "batchSize": 10})
	if len(r.Messages) != 2 || r.Messages[0].Attempts != 1 {
		t.Fatalf("pull: %+v", r)
	}
	l, err := q.e.store.MoveLease(context.Background(), LeaseName(q.tenant, "support"), "h1", q.epoch, "h2", NamespaceLeaseTTL, q.e.m.now())
	if err != nil {
		t.Fatal(err)
	}
	leases := []string{r.Messages[0].LeaseID, r.Messages[1].LeaseID}

	// Not a hand-back: still fenced.
	h1.fail("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"acks": leases[:1]}, 409, "lease_lost")
	h1.fail("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"release": leases, "extend": leases}, 409, "lease_lost")
	// An epoch from the future, or the current one from the wrong host.
	h1.fail("POST", q.path("jobs", "/messages/ack", l.Epoch+1), map[string]any{"release": leases}, 409, "lease_lost")
	h1.fail("POST", q.path("jobs", "/messages/ack", l.Epoch), map[string]any{"release": leases}, 409, "lease_lost")

	var res QueueAckResult
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"release": leases}, &res)
	if res.Released != 2 || res.Stale != 0 {
		t.Fatalf("stale holder's release: %+v", res)
	}
	got := q.pull(h2, "jobs", l.Epoch, map[string]any{"batchSize": 10})
	if len(got.Messages) != 2 {
		t.Fatalf("new holder got %d of 2 released messages at once", len(got.Messages))
	}
	for _, m := range got.Messages {
		if m.Attempts != 1 {
			t.Fatalf("released message counted an attempt it never ran: %+v", m)
		}
	}
	// A second release of the same (now superseded) leases does nothing.
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"release": leases}, &res)
	if res.Released != 0 || res.Stale != 2 {
		t.Fatalf("release of superseded leases: %+v", res)
	}
}
