package gen_test

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestTenantClocks pins ledger claim C-clocks (security/THREAT-MODEL.md
// "Side channels inside a workload's process", #333): what tenant code can
// see of time and shared memory in the pinned workerd, under the config
// hostd generates and with no flag tailworks sets. It records current
// behaviour, it does not defend anything:
//
//   - Date.now() and performance.now() advance during a CPU loop (more than
//     one distinct value each). Cloudflare's production runtime freezes
//     them while code runs; open-source workerd does not.
//   - SharedArrayBuffer exists (there is no second thread to build a
//     counting timer with: node:worker_threads' Worker throws).
//
// From the Worker and from a facet object, which is its own isolate in the
// same process as the platform wrapper and MAC_KEY. A failure here means a
// workerd bump changed what tenant code can measure: re-read that section
// of the threat model (and docs/security-model.md "Side channels inside a
// workload") and bring it, the ledger row and this test into line, in
// either direction. Don't loosen the assertions to make it pass.
func TestTenantClocks(t *testing.T) {
	in := start(t, hostdtest.Fixture("clocks"), nil)

	run := func(t *testing.T, tw map[string]any, from string) {
		code, _, r := in.get("/", tw)
		if code != 200 || r["from"] != from {
			t.Fatalf("probe: %d %v", code, r)
		}
		clocks, _ := r["clocks"].(map[string]any)
		if clocks == nil {
			t.Fatalf("no clocks in %v", r)
		}
		t.Logf("%s: %d iterations in %.1f ms; Date.now() %v distinct, performance.now() %v distinct (min step %v ms); SharedArrayBuffer %v, Atomics.wait %v, shared WebAssembly.Memory %v; worker_threads %v",
			from, int(clocks["iterations"].(float64)), clocks["elapsedMs"], clocks["dateNowDistinct"], clocks["performanceNowDistinct"], clocks["performanceNowMinStepMs"],
			r["sharedArrayBuffer"], r["atomicsWait"], r["sharedWasmMemory"], r["workerThreads"])

		// The clocks are not frozen: both moved while the loop ran.
		if n, _ := clocks["dateNowDistinct"].(float64); n <= 1 {
			t.Errorf("Date.now() returned %v distinct value(s) during a CPU loop; the threat model says workerd does not freeze it", n)
		}
		if n, _ := clocks["performanceNowDistinct"].(float64); n <= 1 {
			t.Errorf("performance.now() returned %v distinct value(s) during a CPU loop; the threat model says workerd does not freeze it", n)
		}
		// Shared memory exists; a second thread does not.
		if r["sharedArrayBuffer"] != "function" {
			t.Errorf("SharedArrayBuffer is %v; the threat model says it exists", r["sharedArrayBuffer"])
		}
		wt, _ := r["workerThreads"].(string)
		if !strings.HasPrefix(wt, "throws:") && !strings.HasPrefix(wt, "import throws:") {
			t.Errorf("node:worker_threads Worker: %q; the threat model says there is no second thread", wt)
		}
	}
	t.Run("Worker", func(t *testing.T) { run(t, wl, "worker") })
	t.Run("FacetObject", func(t *testing.T) { run(t, obj("Clock", "c1"), "facet") })
}
