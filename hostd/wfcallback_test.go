package hostd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestWorkflowCallsBackItsObject (#359): an object starts a Workflow and the
// Workflow calls back into that object through its binding, as the Agents
// SDK's AgentWorkflow does (getAgentByName before run()). The object's
// class has no node of its own: either it is missing from tailnet.objects
// (e2e/compat's overlay gives only the workload's tags) or it says "node":
// false (tw deploy's default without a tailworks.jsonc).
//
// On the real mgmt the callback never arrived: hostd gave the unlisted
// class a node, mgmt (which declares only tailnet.objects) refused every
// create with unknown_namespace, and hostd retried the create, and a pool
// refill of the class, on every call to the object. The object was never
// one hostd knew, so the Workflow (which has no create ticket) was refused
// it as for a public request (public_create_denied), as it was for every
// "node": false class on any mgmt. fakemgmt declares every class, so tw dev
// and the fakemgmt suites saw the unlisted class work.
func TestWorkflowCallsBackItsObject(t *testing.T) {
	for _, backend := range hostdtest.Backends {
		t.Run(backend, func(t *testing.T) {
			s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}},
				onBackend(backend, nil))
			e := s.e
			dep := e.Deploy(hostdtest.Fixture("wfcallback"), "wfcallback", nil)
			wl := e.WaitActive("wfcallback", dep, 90*time.Second)
			s.waitPeer(wl.IP)
			base := "http://" + wl.IP
			for _, class := range []string{"Unlisted", "Internal"} {
				q := "?class=" + class + "&name=a1"
				code, r := s.getJSON(base + "/start" + q)
				id, _ := r["id"].(string)
				if code != 200 || id == "" {
					t.Fatalf("%s: start: %d %v", class, code, r)
				}
				var last map[string]any
				deadline := time.Now().Add(45 * time.Second)
				for {
					_, last = s.getJSON(base + "/pings" + q)
					if last["from"] == id {
						break
					}
					if time.Now().After(deadline) {
						_, st := s.getJSON(base + "/status" + q + "&id=" + id)
						t.Fatalf("%s: the Workflow's callback never reached the object: pings %v, instance %v", class, last, st)
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			// The unlisted class has no node here: hostd never asked mgmt
			// for one (nor for a pool of them).
			if backend == hostdtest.BackendReal {
				for _, o := range e.Host.Status().Objects {
					if o.Class == "Unlisted" || strings.HasPrefix(o.Name, "Unlisted/") {
						t.Errorf("object %s (%s %s): a class mgmt doesn't declare got a node", o.ID, o.Class, o.Name)
					}
				}
			}
		})
	}
}
