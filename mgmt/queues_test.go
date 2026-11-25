package mgmt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const queueSecret = "queue-plaintext-0123456789abcdef"

const queueWrangler = `{
  // producer of jobs and later; consumer of jobs (DLQ jobs-dlq) and fragile
  "name": "support", "main": "dist/index.js",
  "queues": {
    "producers": [{ "binding": "JOBS", "queue": "jobs" }, { "binding": "FRAGILE", "queue": "fragile" }],
    "consumers": [
      { "queue": "jobs", "max_retries": 1, "dead_letter_queue": "jobs-dlq", "retry_delay": 0 },
      { "queue": "fragile", "max_retries": 0 },
      { "queue": "pulled", "type": "http_pull" },
    ],
  },
}`

type queueEnv struct {
	e      *testEnv
	tenant string
	epoch  int64
}

func newQueueEnv(t *testing.T) *queueEnv {
	e := newTestEnv(t)
	c, h1 := e.c, e.asHost("h1")
	var tn struct{ TenantID string }
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "wrangler.jsonc": queueWrangler, "dist/index.js": "export default {}"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "support", "bundle": bundle}, &dep)
	return &queueEnv{e: e, tenant: tn.TenantID, epoch: dep.Epoch}
}

// path is a queue route; as a host when epoch > 0 (acting for support).
func (q *queueEnv) path(queue, sub string, epoch int64) string {
	p := "/v1/tenants/" + q.tenant + "/queues/" + queue + sub
	if epoch > 0 {
		p += fmt.Sprintf("?workload=support&epoch=%d", epoch)
	}
	return p
}

func qmsg(body, ct string) map[string]any {
	return map[string]any{"body": []byte(body), "contentType": ct}
}

func (q *queueEnv) pull(c *apiClient, queue string, epoch int64, o map[string]any) QueuePullResult {
	c.t.Helper()
	var r QueuePullResult
	if o == nil {
		o = map[string]any{}
	}
	c.ok("POST", q.path(queue, "/messages/pull", epoch), o, &r)
	return r
}

// TestQueuesSendPullAck: a producer's host sends, the consumer's host
// claims (attempts counted, hidden while in flight), acks and retries by
// lease; a lease that is no longer current is stale; bodies are sealed at
// rest; and a host may only use the queues its deployment declares.
func TestQueuesSendPullAck(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	h1 := q.e.asHost("h1")

	var sr QueueSendResult
	if st := h1.okStatus("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{
		qmsg(`{"k":"`+queueSecret+`"}`, "json"), qmsg("plain text", "text"), qmsg("\x00\x01\x02", "bytes"),
	}}, &sr); st != 201 || len(sr.IDs) != 3 || sr.Backlog != 3 {
		t.Fatalf("send: %d %+v", st, sr)
	}
	var info QueueInfo
	q.e.c.ok("GET", q.path("jobs", "", 0), nil, &info)
	if info.Backlog != 3 || info.InFlight != 0 || info.OldestAt == 0 || info.Bytes != int64(len(`{"k":"`+queueSecret+`"}`)+len("plain text")+3) {
		t.Fatalf("info: %+v", info)
	}

	r := q.pull(h1, "jobs", q.epoch, map[string]any{"batchSize": 2})
	if len(r.Messages) != 2 || r.Messages[0].ID != sr.IDs[0] || r.Messages[0].Attempts != 1 || r.Messages[0].ContentType != "json" ||
		string(r.Messages[1].Body) != "plain text" || r.Messages[0].LeaseID == "" || r.Messages[0].Timestamp == 0 {
		t.Fatalf("pull: %+v", r.Messages)
	}
	q.e.c.ok("GET", q.path("jobs", "", 0), nil, &info)
	if info.InFlight != 2 || info.Consumer != "support" {
		t.Fatalf("info in flight: %+v", info)
	}
	// In flight: the next claim gets only the third.
	r2 := q.pull(h1, "jobs", q.epoch, map[string]any{"batchSize": 10})
	if len(r2.Messages) != 1 || r2.Messages[0].ID != sr.IDs[2] || string(r2.Messages[0].Body) != "\x00\x01\x02" {
		t.Fatalf("second pull: %+v", r2.Messages)
	}
	var ar QueueAckResult
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{
		"acks":    []string{r.Messages[0].LeaseID, r2.Messages[0].LeaseID},
		"retries": []any{map[string]any{"leaseId": r.Messages[1].LeaseID}},
	}, &ar)
	if ar.Acked != 2 || ar.Retried != 1 || ar.Stale != 0 {
		t.Fatalf("ack: %+v", ar)
	}
	// The retried message comes back (attempt 2) with a new lease; the old
	// lease is stale now.
	r3 := q.pull(h1, "jobs", q.epoch, nil)
	if len(r3.Messages) != 1 || r3.Messages[0].ID != sr.IDs[1] || r3.Messages[0].Attempts != 2 || r3.Messages[0].LeaseID == r.Messages[1].LeaseID {
		t.Fatalf("retried: %+v", r3.Messages)
	}
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"acks": []string{r.Messages[1].LeaseID, "nope", "qm_x:y"}}, &ar)
	if ar.Stale != 3 || ar.Acked != 0 {
		t.Fatalf("stale acks: %+v", ar)
	}
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"extend": []string{r3.Messages[0].LeaseID}, "visibilityTimeoutMs": 60000}, &ar)
	if ar.Extended != 1 {
		t.Fatalf("extend: %+v", ar)
	}
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"acks": []string{r3.Messages[0].LeaseID}}, &ar)
	q.e.c.ok("GET", q.path("jobs", "", 0), nil, &info)
	if info.Backlog != 0 || info.Bytes != 0 || info.InFlight != 0 {
		t.Fatalf("drained: %+v", info)
	}

	// The deploy is the grant: a host must act for a workload, and that
	// workload must declare the queue in the role it uses.
	directFail(t, q.e, "h1", "POST", q.path("jobs", "/messages/pull", 0), 400, "bad_request")
	h1.fail("POST", q.path("other", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(`1`, "json")}}, 403, "forbidden")
	h1.fail("POST", q.path("jobs-dlq", "/messages/pull", q.epoch), map[string]any{}, 403, "forbidden")
	h1.fail("POST", q.path("pulled", "/messages/pull", q.epoch), map[string]any{}, 403, "forbidden") // http_pull: not a host's
	h1.fail("GET", q.path("other", "", q.epoch), nil, 403, "forbidden")
	var hi QueueInfo
	h1.ok("GET", q.path("jobs", "", q.epoch), nil, &hi) // a producer's metrics()

	// Validation.
	h1.fail("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg("not json", "json")}}, 400, "bad_request")
	h1.fail("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(strings.Repeat("x", 128_001), "text")}}, 400, "queue_limit")
	h1.fail("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{
		qmsg(strings.Repeat("x", 100_000), "text"), qmsg(strings.Repeat("x", 100_000), "text"), qmsg(strings.Repeat("x", 100_000), "text")}}, 400, "queue_limit")

	assertNoValue(t, q.e, queueSecret)
}

// TestQueuesDelayRetriesDLQ: delaySeconds hides a message until it is
// due (a waiting claim gets it then); retries count deliveries, and after
// max_retries the message goes to the dead-letter queue, from a failed
// delivery or from a lease that expired (a consumer crash); a consumer
// without a DLQ drops it.
//
// Delays and leases run on a step clock (the store's, which every queue
// decision reads), so "not due yet" holds however slow the machine is
// (#392: on a loaded runner the pull right after a 1 s delay found the
// message already due).
func TestQueuesDelayRetriesDLQ(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	clk := &stepClock{now: time.Now()}
	q.e.store.SetClock(clk)
	h1 := q.e.asHost("h1")
	send := func(queue, body string, delay int) string {
		t.Helper()
		var sr QueueSendResult
		h1.ok("POST", q.path(queue, "/messages", q.epoch), map[string]any{"messages": []any{qmsg(body, "text")}, "delaySeconds": delay}, &sr)
		return sr.IDs[0]
	}

	// Delay.
	send("jobs", "later", 1)
	if r := q.pull(h1, "jobs", q.epoch, nil); len(r.Messages) != 0 {
		t.Fatalf("a delayed message was delivered early: %+v", r)
	}
	var info QueueInfo
	q.e.c.ok("GET", q.path("jobs", "", 0), nil, &info)
	if info.Delayed != 1 {
		t.Fatalf("delayed: %+v", info)
	}
	// A claim already waiting gets it once it is due: a delay that runs
	// out wakes nothing, the claim's once-a-second re-check finds it.
	got := make(chan QueuePullResult, 1)
	go func() {
		var r QueuePullResult
		h1.do("POST", q.path("jobs", "/messages/pull", q.epoch), map[string]any{"waitMs": 15000}, &r)
		got <- r
	}()
	time.Sleep(200 * time.Millisecond)
	clk.advance(time.Second)
	var r QueuePullResult
	select {
	case r = <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("a waiting claim did not get the delayed message once it was due")
	}
	if len(r.Messages) != 1 {
		t.Fatalf("delayed message: %+v", r)
	}

	// Retries: max_retries 1 is two deliveries; the second failure goes to
	// jobs-dlq, body and send time intact.
	var ar QueueAckResult
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"retries": []any{map[string]any{"leaseId": r.Messages[0].LeaseID}}}, &ar)
	if ar.Retried != 1 {
		t.Fatalf("first retry: %+v", ar)
	}
	r = q.pull(h1, "jobs", q.epoch, nil)
	if len(r.Messages) != 1 || r.Messages[0].Attempts != 2 {
		t.Fatalf("second delivery: %+v", r)
	}
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"retries": []any{map[string]any{"leaseId": r.Messages[0].LeaseID, "delaySeconds": 5}}}, &ar)
	if ar.DeadLettered != 1 || ar.Retried != 0 {
		t.Fatalf("second retry: %+v", ar)
	}
	var peek struct{ Messages []QueueMessage }
	q.e.c.ok("GET", q.path("jobs-dlq", "/messages", 0), nil, &peek)
	if len(peek.Messages) != 1 || string(peek.Messages[0].Body) != "later" || peek.Messages[0].ID != r.Messages[0].ID || peek.Messages[0].Attempts != 0 {
		t.Fatalf("DLQ: %+v", peek.Messages)
	}

	// A retry with a delay stays hidden for it.
	send("jobs", "retry-later", 0)
	r = q.pull(h1, "jobs", q.epoch, nil)
	h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"retries": []any{map[string]any{"leaseId": r.Messages[0].LeaseID, "delaySeconds": 1}}}, &ar)
	if r2 := q.pull(h1, "jobs", q.epoch, nil); len(r2.Messages) != 0 {
		t.Fatalf("retry delay ignored: %+v", r2)
	}
	clk.advance(time.Second)
	if r2 := q.pull(h1, "jobs", q.epoch, nil); len(r2.Messages) != 1 || r2.Messages[0].Attempts != 2 {
		t.Fatalf("after the retry delay: %+v", r2)
	} else {
		h1.ok("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"acks": []string{r2.Messages[0].LeaseID}}, &ar)
	}

	// A consumer that crashes: its leases expire, the message is delivered
	// again, and once it is out of retries it goes to the DLQ at the next
	// claim instead of a third delivery.
	id := send("jobs", "crashy", 0)
	for i := 1; i <= 2; i++ {
		r = q.pull(h1, "jobs", q.epoch, map[string]any{"visibilityTimeoutMs": 1000})
		if len(r.Messages) != 1 || r.Messages[0].ID != id || r.Messages[0].Attempts != int64(i) {
			t.Fatalf("delivery %d after a crash: %+v", i, r)
		}
		if r2 := q.pull(h1, "jobs", q.epoch, nil); len(r2.Messages) != 0 {
			t.Fatalf("delivery %d: delivered again while its lease holds: %+v", i, r2)
		}
		clk.advance(1100 * time.Millisecond) // the consumer crashed: its lease expires
	}
	r = q.pull(h1, "jobs", q.epoch, nil)
	if len(r.Messages) != 0 || r.DeadLettered != 1 {
		t.Fatalf("expired past max_retries: %+v", r)
	}
	q.e.c.ok("GET", q.path("jobs-dlq", "/messages", 0), nil, &peek)
	if len(peek.Messages) != 2 || peek.Messages[1].ID != id {
		t.Fatalf("DLQ after the crash: %+v", peek.Messages)
	}

	// max_retries 0 and no DLQ: the first failure drops it.
	send("fragile", "drop me", 0)
	r = q.pull(h1, "fragile", q.epoch, nil)
	h1.ok("POST", q.path("fragile", "/messages/ack", q.epoch), map[string]any{"retries": []any{map[string]any{"leaseId": r.Messages[0].LeaseID}}}, &ar)
	q.e.c.ok("GET", q.path("fragile", "", 0), nil, &info)
	if ar.DeadLettered != 1 || info.Backlog != 0 {
		t.Fatalf("dropped: %+v %+v", ar, info)
	}

	// An admin pull consumer gives its own settings.
	q.e.c.ok("POST", q.path("pulled", "/messages", 0), map[string]any{"messages": []any{qmsg("p", "text")}}, nil)
	r = q.pull(q.e.c, "pulled", 0, map[string]any{"maxRetries": 0, "deadLetterQueue": "pulled-dlq"})
	q.e.c.ok("POST", q.path("pulled", "/messages/ack", 0), map[string]any{"retries": []any{map[string]any{"leaseId": r.Messages[0].LeaseID}}, "maxRetries": 0, "deadLetterQueue": "pulled-dlq"}, &ar)
	if ar.DeadLettered != 1 {
		t.Fatalf("admin DLQ: %+v", ar)
	}
	q.e.c.ok("GET", q.path("pulled-dlq", "/messages", 0), nil, &peek)
	if len(peek.Messages) != 1 {
		t.Fatalf("admin DLQ peek: %+v", peek)
	}
}

// TestQueuesLongPoll: a claim waiting on an empty queue returns as soon as
// a message is sent.
func TestQueuesLongPoll(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	h1 := q.e.asHost("h1")
	q.pull(h1, "jobs", q.epoch, nil) // creates the queue
	done := make(chan QueuePullResult, 1)
	st := time.Now()
	go func() {
		var r QueuePullResult
		h1.do("POST", q.path("jobs", "/messages/pull", q.epoch), map[string]any{"waitMs": 15000}, &r)
		done <- r
	}()
	time.Sleep(300 * time.Millisecond)
	h1.ok("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(`"x"`, "json")}}, nil)
	select {
	case r := <-done:
		if len(r.Messages) != 1 || time.Since(st) > 5*time.Second {
			t.Fatalf("long poll: %+v after %v", r, time.Since(st))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("long poll not woken by a send")
	}
}

// TestQueuesFence: only the lease holder at its epoch sends or claims;
// after the lease moves, the old holder is refused and the new holder
// gets the old holder's in-flight messages once their leases expire.
func TestQueuesFence(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	h1, h2 := q.e.asHost("h1"), q.e.asHost("h2")
	h2.ok("PUT", "/v1/hosts/h2", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	h1.ok("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(`1`, "json"), qmsg(`2`, "json")}}, nil)
	h2.fail("POST", q.path("jobs", "/messages", q.epoch), map[string]any{"messages": []any{qmsg(`3`, "json")}}, 409, "lease_lost")
	h2.fail("POST", q.path("jobs", "/messages/pull", q.epoch), map[string]any{}, 409, "lease_lost")
	h1.fail("POST", q.path("jobs", "/messages/pull", q.epoch+1), map[string]any{}, 409, "lease_lost")
	r := q.pull(h1, "jobs", q.epoch, map[string]any{"visibilityTimeoutMs": 1000})
	if len(r.Messages) != 2 {
		t.Fatalf("pull: %+v", r)
	}

	ctx := context.Background()
	l, err := q.e.store.MoveLease(ctx, LeaseName(q.tenant, "support"), "h1", q.epoch, "h2", NamespaceLeaseTTL, q.e.m.now())
	if err != nil {
		t.Fatal(err)
	}
	h1.fail("POST", q.path("jobs", "/messages/ack", q.epoch), map[string]any{"acks": []string{r.Messages[0].LeaseID}}, 409, "lease_lost")
	got := q.pull(h2, "jobs", l.Epoch, map[string]any{"waitMs": 5000, "batchSize": 10})
	for len(got.Messages) < 2 {
		more := q.pull(h2, "jobs", l.Epoch, map[string]any{"waitMs": 3000})
		if len(more.Messages) == 0 {
			t.Fatalf("new holder got %d of 2 in-flight messages", len(got.Messages))
		}
		got.Messages = append(got.Messages, more.Messages...)
	}
	if got.Messages[0].Attempts != 2 {
		t.Fatalf("redelivered: %+v", got.Messages)
	}
}

// TestQueuesLifecycle: create (201/200), list with counters and paging,
// peek, purge, delete (with its data key; a later send makes a new
// queue), audit; and a bad queues config fails the deploy.
func TestQueuesLifecycle(t *testing.T) {
	t.Parallel()
	q := newQueueEnv(t)
	c := q.e.c
	var qi QueueInfo
	if st := c.okStatus("PUT", q.path("alpha", "", 0), nil, &qi); st != 201 || qi.Incarnation == "" {
		t.Fatalf("create: %d %+v", st, qi)
	}
	if st := c.okStatus("PUT", q.path("alpha", "", 0), nil, nil); st != 200 {
		t.Fatalf("create again: %d", st)
	}
	c.ok("POST", q.path("beta", "/messages", 0), map[string]any{"messages": []any{qmsg("one", "text"), qmsg("two", "text")}}, nil)
	var items []QueueInfo
	if next := c.items("/v1/tenants/"+q.tenant+"/queues", &items); next != nil || len(items) != 2 || items[1].Name != "beta" || items[1].Backlog != 2 {
		t.Fatalf("list: %+v", items)
	}
	var p1 []QueueInfo
	if next := c.items("/v1/tenants/"+q.tenant+"/queues?limit=1", &p1); next == nil || len(p1) != 1 || p1[0].Name != "alpha" {
		t.Fatalf("page 1: %+v", p1)
	}
	var peek struct{ Messages []QueueMessage }
	c.ok("GET", q.path("beta", "/messages", 0)+"?limit=1", nil, &peek)
	if len(peek.Messages) != 1 || string(peek.Messages[0].Body) != "one" {
		t.Fatalf("peek: %+v", peek)
	}
	var pr struct{ Purged int64 }
	c.ok("POST", q.path("beta", "/purge", 0), nil, &pr)
	if pr.Purged != 2 {
		t.Fatalf("purge: %+v", pr)
	}
	c.ok("GET", q.path("beta", "", 0), nil, &qi)
	if qi.Backlog != 0 {
		t.Fatalf("after purge: %+v", qi)
	}
	c.ok("DELETE", q.path("beta", "", 0), nil, nil)
	c.fail("DELETE", q.path("beta", "", 0), nil, 404, "unknown_queue")
	c.fail("GET", q.path("beta", "", 0), nil, 404, "unknown_queue")
	if _, err := q.e.m.custody.Get(context.Background(), queueKeyCustodyName(q.tenant, qi.Incarnation)); err == nil {
		t.Fatal("the deleted queue's data key is still in custody")
	}
	var sr QueueSendResult
	c.ok("POST", q.path("beta", "/messages", 0), map[string]any{"messages": []any{qmsg("three", "text")}}, &sr)
	c.ok("GET", q.path("beta", "", 0), nil, &qi)
	if qi.Backlog != 1 {
		t.Fatalf("recreated: %+v", qi)
	}
	for kind, n := range map[string]int{"queue_create": 3, "queue_purge": 1, "queue_delete": 1} {
		recs, _ := q.e.store.ListAudit(context.Background(), AuditFilter{TenantID: q.tenant, Kinds: []string{kind}})
		if len(recs) != n {
			t.Errorf("audit %s: %d records, want %d", kind, len(recs), n)
		}
	}
	c.fail("GET", "/v1/tenants/tn_nope/queues", nil, 404, "unknown_tenant")

	bad := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "wrangler.jsonc": `{"queues":{"consumers":[{"queue":"Bad"}]}}`, "dist/index.js": "export default {}"})
	c.fail("POST", "/v1/deployments", map[string]any{"tenantID": q.tenant, "workload": "other", "bundle": bad}, 400, "bad_bundle")
}
