package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

type queueRec struct {
	ID       string  `json:"id"`
	K        string  `json:"k"`
	From     string  `json:"from"`
	Attempts int     `json:"attempts"`
	Batch    int     `json:"batch"`
	Queue    string  `json:"queue"`
	At       int64   `json:"at"`
	TS       float64 `json:"ts"`
	Retried  bool    `json:"retried"`
}

// TestQueues runs Queues end to end on real workerd with the fake mgmt as
// the broker (ADR-039): workerd's own queue binding (send, sendBatch,
// metrics, delaySeconds, delivery_delay, content types, from a facet too)
// through hostd to mgmt, and hostd's push consumer running the default
// export's queue() with workerd's MessageBatch: batching by size and by
// max_batch_timeout, retry() with a delay, max_retries landing in the
// dead-letter queue, a throw retrying the batch but not an explicit ack,
// queue() creating a Durable Object (the batch's create ticket), a slow
// heartbeat cancelling a run and the batch handed back uncounted, a
// workerd crash mid-batch losing nothing, and a fenced host consuming
// nothing until it is back.
func TestQueues(t *testing.T) {
	t.Parallel()
	testQueues(t, hostdtest.BackendFake)
}

// testQueues is TestQueues against backend (realmgmt_test.go).
func testQueues(t *testing.T, backend string) {
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, onBackend(backend, func(o *hostdtest.Options) {
		// FenceAfter is short so FencedHostConsumesNothing fences quickly,
		// but not 1 s: on a loaded runner a heartbeat round trip or an
		// assignment sync now and then takes over a second, and every such
		// moment stopped the consumer loops. A loop stopped just as its
		// claim landed lost the claim, counted as an attempt (#392).
		o.FenceAfter, o.HeartbeatEvery = 3*time.Second, 100*time.Millisecond
		o.QueueVisibility = 3 * time.Second
	}))
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("queues"), "queues", nil)
	wl := e.WaitActive("queues", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	call := func(path string, kv ...string) map[string]any {
		t.Helper()
		q := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		code, m := s.getJSON("http://" + wl.IP + path + "?" + q.Encode())
		if code != 200 {
			t.Fatalf("%s %v: %d %v", path, kv, code, m)
		}
		return m
	}
	// recs is the consumer's log (nil while workerd restarts).
	recs := func() []queueRec {
		code, _, b := s.do("GET", "http://"+wl.IP+"/log", nil)
		var out struct{ Recs []queueRec }
		if code != 200 || json.Unmarshal(b, &out) != nil {
			return nil
		}
		return out.Recs
	}
	// wait returns the delivery of k with attempts n, waiting for it.
	wait := func(k string, n int, timeout time.Duration) queueRec {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			for _, r := range recs() {
				if r.K == k && r.Attempts == n {
					return r
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no delivery of %s attempt %d within %v; log: %+v", k, n, timeout, recs())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	count := func(k string) int {
		n := 0
		for _, r := range recs() {
			if r.K == k {
				n++
			}
		}
		return n
	}
	ms := func() int64 { return time.Now().UnixMilli() }

	t.Run("Bindings", func(t *testing.T) {
		m := call("/env")
		if m["JOBS"] != "object" || m["OTHER"] != "undefined" || fmt.Sprint(m["keys"]) != "[]" {
			t.Fatalf("env: %v", m)
		}
	})

	t.Run("BatchBySize", func(t *testing.T) {
		call("/batch", "ks", "s1,s2,s3")
		for _, k := range []string{"s1", "s2", "s3"} {
			if r := wait(k, 1, 15*time.Second); r.Batch != 3 || r.Queue != "jobs" || r.TS == 0 {
				t.Fatalf("%s: %+v (want one batch of 3)", k, r)
			}
		}
	})

	t.Run("BatchByTimeout", func(t *testing.T) {
		st := ms()
		call("/send", "k", "t1")
		r := wait("t1", 1, 15*time.Second)
		if r.Batch != 1 || r.At-st < 900 {
			t.Fatalf("t1: %+v after %d ms (want a batch of 1 after max_batch_timeout 1 s)", r, r.At-st)
		}
	})

	t.Run("RetryWithDelay", func(t *testing.T) {
		call("/send", "k", "r1", "do", "retry-once", "rd", "2")
		first := wait("r1", 1, 15*time.Second)
		second := wait("r1", 2, 20*time.Second)
		if !first.Retried || second.At-first.At < 1800 || second.ID != first.ID {
			t.Fatalf("retry: %+v then %+v (want attempt 2 ≥ 2 s later)", first, second)
		}
	})

	t.Run("DelaySeconds", func(t *testing.T) {
		st := ms()
		call("/send", "k", "d1", "delay", "2")
		if r := wait("d1", 1, 20*time.Second); r.At-st < 1900 {
			t.Fatalf("delaySeconds 2 delivered after %d ms", r.At-st)
		}
		st = ms()
		call("/slow", "k", "sl1") // the SLOW binding's delivery_delay: 2
		if r := wait("sl1", 1, 20*time.Second); r.At-st < 1900 {
			t.Fatalf("delivery_delay 2 delivered after %d ms", r.At-st)
		}
	})

	t.Run("MaxRetriesDeadLetter", func(t *testing.T) {
		call("/send", "k", "f1", "do", "fail")
		wait("f1", 1, 15*time.Second)
		wait("f1", 2, 15*time.Second)
		deadline := time.Now().Add(15 * time.Second)
		for {
			var peek struct {
				Messages []struct {
					Body        []byte
					ContentType string
				}
			}
			err := e.Mgmt.Do(context.Background(), "GET", "/v1/tenants/"+e.TenantID+"/queues/jobs-dlq/messages", nil, &peek)
			if err == nil && len(peek.Messages) == 1 {
				if !strings.Contains(string(peek.Messages[0].Body), `"k":"f1"`) || peek.Messages[0].ContentType != "json" {
					t.Fatalf("DLQ message: %s %s", peek.Messages[0].Body, peek.Messages[0].ContentType)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("f1 not in jobs-dlq: %v %+v", err, peek)
			}
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(time.Second)
		if n := count("f1"); n != 2 {
			t.Fatalf("f1 delivered %d times, want 2 (max_retries 1)", n)
		}
	})

	t.Run("ThrowRetriesBatch", func(t *testing.T) {
		call("/batch", "ks", "x1,x2", "do", "throw-once")
		wait("x1", 2, 20*time.Second)
		wait("x2", 2, 20*time.Second)
	})

	t.Run("ExplicitAckSurvivesThrow", func(t *testing.T) {
		call("/send", "k", "a1", "do", "ack-explicit")
		wait("a1", 1, 15*time.Second)
		time.Sleep(3 * time.Second)
		if n := count("a1"); n != 1 {
			t.Fatalf("an explicitly acked message was delivered %d times", n)
		}
	})

	t.Run("ContentTypes", func(t *testing.T) {
		call("/types")
		for _, k := range []string{"text:hello", "bytes:1,2,3", "v8:5:two"} {
			wait(k, 1, 15*time.Second)
		}
	})

	t.Run("FromFacet", func(t *testing.T) {
		call("/do/send", "k", "fb1")
		if r := wait("fb1", 1, 15*time.Second); r.From != "facet" {
			t.Fatalf("facet send: %+v", r)
		}
	})

	t.Run("MetricsAndLimits", func(t *testing.T) {
		call("/send", "k", "m1", "delay", "30") // stays in the backlog
		m := call("/metrics")
		if n, _ := m["backlogCount"].(float64); n < 1 || m["backlogBytes"].(float64) < 1 || m["oldest"] != true {
			t.Fatalf("metrics: %v", m)
		}
		if m := call("/big"); !strings.Contains(fmt.Sprint(m["error"]), "queue_limit") {
			t.Fatalf("a 130 KB message: %v", m)
		}
	})

	t.Run("SlowHeartbeatReleasesBatch", func(t *testing.T) {
		// A heartbeat later than FenceAfter (3 s here: a loaded host, a slow
		// mgmt) makes the host's lease stale, and its consumer loops stop
		// until the next good one. The run they were in is cancelled; hostd
		// hands the batch back uncounted, so the message comes again as
		// attempt 1. (#387: it was retried as attempt 2, and a wait for
		// attempt 1 never ended.)
		call("/send", "k", "sh1", "do", "slow-once") // its first run takes 8 s
		time.Sleep(1500 * time.Millisecond)          // past max_batch_timeout: queue() is running
		e.Hooks.DelayHeartbeat(4 * time.Second)      // stale 3 s in, fresh again at 4 s
		wait("sh1", 1, 20*time.Second)
		time.Sleep(time.Second)
		for _, r := range recs() {
			if r.K == "sh1" && r.Attempts != 1 {
				t.Fatalf("a delivery hostd cancelled counted as an attempt: %+v", r)
			}
		}
	})

	t.Run("ConsumerCrash", func(t *testing.T) {
		call("/send", "k", "h1", "do", "hang-once")
		wait("h1", 1, 15*time.Second)
		pid := workloadStatus(t, e, "queues").PID
		if pid == 0 {
			t.Fatal("no workerd pid")
		}
		// The whole process group: workerd (and a sandbox launcher, if any).
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		// workerd restarts; the batch's leases expire (3 s) and it comes back.
		wait("h1", 2, 45*time.Second)
	})

	t.Run("FencedHostConsumesNothing", func(t *testing.T) {
		e.Hooks.SetHeartbeatsDown(true)
		deadline := time.Now().Add(10 * time.Second)
		for workloadStatus(t, e, "queues").PID != 0 {
			if time.Now().After(deadline) {
				t.Fatal("workload not fenced")
			}
			time.Sleep(50 * time.Millisecond)
		}
		// An operator sends while the host is cut off: it waits in mgmt,
		// unclaimed. (A fenced host's long poll used to claim it and hand it
		// back, #140: with a workerd still up it could have consumed it.)
		claims := e.Hooks.QueueClaims(e.TenantID, "jobs")
		if err := e.Mgmt.Do(context.Background(), "POST", "/v1/tenants/"+e.TenantID+"/queues/jobs/messages",
			map[string]any{"messages": []any{map[string]any{"body": []byte(`{"k":"op1"}`), "contentType": "json"}}}, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		if n := e.Hooks.QueueBacklog(e.TenantID, "jobs"); n < 1 {
			t.Fatalf("backlog while fenced: %d", n)
		}
		if n := e.Hooks.QueueClaims(e.TenantID, "jobs") - claims; n != 0 {
			t.Fatalf("the fenced host claimed %d message(s)", n)
		}
		e.Hooks.SetHeartbeatsDown(false)
		wl = e.WaitActive("queues", dep, 60*time.Second)
		s.waitPeer(wl.IP)
		wait("op1", 1, 30*time.Second)
	})
}
