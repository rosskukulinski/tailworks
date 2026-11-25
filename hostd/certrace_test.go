package hostd_test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// Issue #331: a freshly spawned object's first call must reach the callee
// with its caps narrowed by its certificate's lim and with getCaller().tw,
// although its node can send a moment before mgmt has its ready report
// (and so its certificate). The ready reports are delayed here so the
// child's first egress always comes before mgmt knows its node: hostd
// must hold it (egressgate.go). A ready report that fails refuses the
// egress (identity_pending) instead of letting it through un-narrowed,
// and a later egress re-sends the report.
func TestFirstCallNarrowedBeforeReadyReport(t *testing.T) {
	for _, backend := range []string{hostdtest.BackendReal, hostdtest.BackendFake} {
		t.Run(backend, func(t *testing.T) {
			testFirstCallNarrowed(t, backend)
		})
	}
}

func testFirstCallNarrowed(t *testing.T, backend string) {
	s := startSuiteBackend(t, fakemgmt.Config{}, backend)
	e := s.e
	d := e.Deploy(hostdtest.Fixture("probe"), "probe", nil)
	probe := e.WaitActive("probe", d, 60*time.Second)
	s.waitPeer(probe.IP)
	objIP := map[string]string{}
	for _, n := range []string{"a", "b"} {
		if code, r := s.getJSON("http://" + probe.IP + "/probe/" + n + "/"); code != 200 {
			t.Fatalf("probe %s: %d %v", n, code, r)
		}
		o := e.WaitObject("probe", "Probe", n, 60*time.Second)
		s.waitPeer(o.IP)
		objIP[n] = o.IP
	}
	// Every node's policy caps are the test cap and mcpCap; the child's lim
	// keeps only mcpCap.
	lim := `{"` + mcpCap + `":{"tools":["lookup_order"]}}`
	callee := "http://" + objIP["b"] + "/"
	spawnCall := func(name string) (int, map[string]any) {
		return s.getJSON("http://" + objIP["a"] + "/spawn-call?name=" + name + "&caps=" + url.QueryEscape(lim) + "&url=" + url.QueryEscape(callee))
	}

	t.Run("HeldUntilReported", func(t *testing.T) {
		e.Hooks.SetReadyFault(fakemgmt.ReadyFault{Delay: 1500 * time.Millisecond})
		defer e.Hooks.SetReadyFault(fakemgmt.ReadyFault{})
		code, r := spawnCall("kid1")
		if code != 200 || r["kind"] != "probe" || r["name"] != "b" {
			t.Fatalf("child's first call: %d %v", code, r)
		}
		kid := e.WaitObject("probe", "Probe", "kid1", 30*time.Second)
		checkNarrowed(t, r, kid.ID)
	})

	t.Run("RefusedWhenReportFails", func(t *testing.T) {
		e.Hooks.SetReadyFault(fakemgmt.ReadyFault{Fail: true})
		code, r := spawnCall("kid2")
		if code != 299 || r["code"] != "identity_pending" {
			e.Hooks.SetReadyFault(fakemgmt.ReadyFault{})
			t.Fatalf("a call before mgmt accepted the ready report must be refused with identity_pending: %d %v", code, r)
		}
		// mgmt recovers: a later call re-sends the report (at most every
		// 5 s) and goes through, narrowed.
		e.Hooks.SetReadyFault(fakemgmt.ReadyFault{})
		kid := e.WaitObject("probe", "Probe", "kid2", 30*time.Second)
		s.waitPeer(kid.IP)
		deadline := time.Now().Add(30 * time.Second)
		for {
			code, r = s.getJSON("http://" + kid.IP + "/tegress?url=" + url.QueryEscape(callee))
			if code == 200 {
				break
			}
			if code != 299 || r["code"] != "identity_pending" || time.Now().After(deadline) {
				t.Fatalf("after mgmt recovers: %d %v", code, r)
			}
			time.Sleep(500 * time.Millisecond)
		}
		checkNarrowed(t, r, kid.ID)
	})
}

// checkNarrowed checks the callee's view of a call from object objectID
// whose lim keeps mcpCap only.
func checkNarrowed(t *testing.T, r map[string]any, objectID string) {
	t.Helper()
	if get(r, "caller", "tw", "objectId") != objectID {
		t.Fatalf("callee must see the child's certificate (getCaller().tw) on its first call: %v", js(r["caller"]))
	}
	capMap, _ := get(r, "caller", "capMap").(map[string]any)
	if _, ok := capMap[nettest.TestCap]; ok {
		t.Fatalf("the child's caps must be narrowed by its lim (no %s): %v", nettest.TestCap, js(capMap))
	}
	var tools struct{ Tools []string }
	if vs, _ := capMap[mcpCap].([]any); len(vs) != 1 || json.Unmarshal([]byte(js(vs[0])), &tools) != nil || len(tools.Tools) != 1 || tools.Tools[0] != "lookup_order" {
		t.Fatalf("the child keeps %s within its lim: %v", mcpCap, js(capMap))
	}
}
