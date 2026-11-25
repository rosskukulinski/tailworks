//go:build e2evm && (linux || darwin)

package vm

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestE2EVMWorkflows: Workflows (workerd's built-in engine) on the
// microVM tier (e2e/vm/testdata/vmflow). An instance is a SQLite-backed
// Durable Object on the guest's encrypted volume. Covers an instance
// created from the Worker and one from a facet running steps (one retried)
// and a sleep to completion, and an instance asleep when its VM is killed
// finishing on the new VM.
func TestE2EVMWorkflows(t *testing.T) {
	s := startVMSuite(t, nil)
	base := s.deploy(vmApp("vmflow"), "vmflow", nil)
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	status := func(prefix, id string) map[string]any {
		_, v := s.do("GET", prefix+"/status?id="+id, nil)
		m, _ := v.(map[string]any)
		return m
	}
	waitStatus := func(prefix, id, want string, timeout time.Duration) map[string]any {
		t.Helper()
		var m map[string]any
		s.eventually(timeout, fmt.Sprintf("%s%s status %s", prefix, id, want), func() bool {
			m = status(prefix, id)
			if m["status"] == "errored" && want != "errored" {
				t.Fatalf("%s: %v", id, m)
			}
			return m["status"] == want
		})
		return m
	}

	for i, p := range []string{base, base + "/do"} {
		id := fmt.Sprintf("quick-%d", i)
		n := 3 + i
		if m := s.must("GET", fmt.Sprintf("%s/create?id=%s&n=%d", p, id, n), nil); m["id"] != id {
			t.Fatalf("%s create: %v", p, m)
		}
		r := waitStatus(p, id, "complete", 60*time.Second)
		if got, want := js(r["output"]), fmt.Sprintf(`{"b":%d,"c":%d}`, n+1, (n+1)*10); got != want {
			t.Fatalf("%s: output %s, want %s (%v)", p, got, want, r)
		}
	}

	// Asleep across a guest crash: the instance resumes on the new VM.
	s.must("GET", base+"/create?id=nap&n=7&sleep=8000", nil)
	waitStatus(base, "nap", "waiting", 30*time.Second)
	st := time.Now()
	s.killVM("vmflow", base+"/status?id=nap")
	r := waitStatus(base, "nap", "complete", 90*time.Second)
	if got := js(r["output"]); got != `{"b":8,"c":80}` {
		t.Fatalf("nap: output %s (%v)", got, r)
	}
	t.Logf("instance asleep at the VM kill completed %v later", time.Since(st).Round(time.Millisecond))
}
