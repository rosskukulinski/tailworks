//go:build e2evm && (linux || darwin)

package vm

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestE2EVMQueues: Queues (ADR-039) on the microVM tier, with the process
// tier's fixture (hostd/testdata/apps/queues). The producer binding in the
// guest's workerd sends through hostd to mgmt (the broker); hostd's push
// consumer runs the Worker's queue() in the guest, which records each
// delivery in a Durable Object. Covers send, sendBatch, a send from a
// facet, retry() with a redelivery, and a message sent before a VM kill
// delivered by the new VM.
func TestE2EVMQueues(t *testing.T) {
	s := startVMSuite(t, func(o *hostdtest.Options) { o.QueueVisibility = 3 * time.Second })
	base := s.deploy(hostdtest.Fixture("queues"), "queues", nil)

	type rec struct {
		K        string `json:"k"`
		From     string `json:"from"`
		Attempts int    `json:"attempts"`
		Batch    int    `json:"batch"`
		Queue    string `json:"queue"`
		Retried  bool   `json:"retried"`
	}
	recs := func() []rec {
		code, v := s.do("GET", base+"/log", nil)
		b, _ := json.Marshal(v)
		var out struct{ Recs []rec }
		if code != 200 || json.Unmarshal(b, &out) != nil {
			return nil
		}
		return out.Recs
	}
	wait := func(k string, attempt int, timeout time.Duration) rec {
		t.Helper()
		var got rec
		s.eventually(timeout, "delivery of "+k, func() bool {
			for _, r := range recs() {
				if r.K == k && r.Attempts == attempt {
					got = r
					return true
				}
			}
			return false
		})
		return got
	}

	if m := s.must("GET", base+"/env", nil); m["JOBS"] != "object" {
		t.Fatalf("env.JOBS in the guest: %v", m)
	}
	t0 := time.Now()
	s.must("GET", base+"/send?k=one", nil)
	if r := wait("one", 1, 30*time.Second); r.Queue != "jobs" {
		t.Fatalf("one: %+v", r)
	}
	t.Logf("send → queue() in the guest: %v", time.Since(t0).Round(time.Millisecond))

	s.must("GET", base+"/batch?ks=b1,b2,b3", nil)
	for _, k := range []string{"b1", "b2", "b3"} {
		if r := wait(k, 1, 30*time.Second); r.Batch != 3 {
			t.Fatalf("%s: %+v (want one batch of 3)", k, r)
		}
	}

	code, v := s.do("GET", base+"/do/send?k=fromfacet", nil)
	if code != 200 {
		t.Fatalf("send from a facet: %d %v", code, v)
	}
	if r := wait("fromfacet", 1, 30*time.Second); r.From != "facet" {
		t.Fatalf("fromfacet: %+v", r)
	}

	s.must("GET", base+"/send?k=r1&do=retry-once&rd=1", nil)
	if first := wait("r1", 1, 30*time.Second); !first.Retried {
		t.Fatalf("r1 attempt 1: %+v", first)
	}
	wait("r1", 2, 30*time.Second)

	// A message sent with a delay, then the VM is killed before it is due:
	// the broker holds it and the new VM's queue() gets it.
	s.must("GET", base+"/send?k=across-kill&delay=3", nil)
	s.killVM("queues", base+"/env")
	wait("across-kill", 1, 60*time.Second)
}
