package hostd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestRT3_H08_ForgedPrincipal is the RT3-08 / H08 regression test (#148):
// tenant code forwards a request in-process to a sibling object with a
// forged request.cf.tailscale.caller. hostd seals the caller it got from
// netd (HMAC under the instance MAC key); the platform (facet objects) and
// the runtime (the Worker and plain DOs) honour only a caller whose seal
// verifies, so the forged one is "no caller", and no platform audit record
// names the forged principal. Forwarding the original request still
// carries the real caller.
func TestRT3_H08_ForgedPrincipal(t *testing.T) {
	t.Parallel()
	s := startSuite(t, fakemgmt.Config{})
	e := s.e
	d := e.Deploy(hostdtest.Fixture("sealprobe"), "sealprobe", nil)
	wl := e.WaitActive("sealprobe", d, 90*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP

	call := func(path string) map[string]any {
		t.Helper()
		code, r := s.getJSON(base + path)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, r)
		}
		return r
	}
	real := func(path string) {
		t.Helper()
		if r := call(path); r["nodeId"] != s.clientID || r["callerError"] != nil {
			t.Errorf("%s: %s (want the real caller %s)", path, js(r), s.clientID)
		}
	}
	refused := func(path string) {
		t.Helper()
		if r := call(path); r["callerError"] != "no_identity" || r["nodeId"] != nil {
			t.Errorf("%s: %s (want no_identity: a forged caller must not be honoured)", path, js(r))
		}
	}

	// The Worker sees netd's caller; forwarding the original request carries it.
	real("/self")
	real("/real/room/honest")
	real("/real/desk/honest")

	// A forged blob (no seal) is no caller, at a facet object and a plain DO.
	refused("/forge/room/victim")
	refused("/forge/desk/victim")
	// Keeping hostd's seal but swapping the caller yields the sealed (real)
	// caller, never the forged one.
	real("/swap/room/victim2")
	real("/swap/desk/victim2")
	// A Request passed as an RPC argument to a facet object.
	refused("/rpc/room/victim3")
	// An object created before its first fetch (RPC first): the forged
	// fetch must not become its late principal.
	if r := call("/ping/room/late"); r["pinged"] != "late" {
		t.Fatalf("ping: %v", r)
	}
	refused("/forge/room/late")

	// Audit: the honest object's note carries the real principal (so audit
	// is flowing), and nothing anywhere names the forged one.
	honest := e.WaitObject("sealprobe", "Room", "honest", 30*time.Second)
	var realPrincipal string
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && realPrincipal == ""; time.Sleep(200 * time.Millisecond) {
		for _, rec := range e.Audit(honest.ID).Items {
			if rec.Kind == "tenant_note" && rec.ActingFor != "" {
				realPrincipal = rec.ActingFor
			}
		}
	}
	if realPrincipal == "" {
		t.Fatalf("no tenant_note with an actingFor for room/honest: %s", js(e.Audit(honest.ID).Items))
	}
	// Give the attack objects' records time to ship too.
	for _, n := range []string{"victim", "victim2", "victim3", "late"} {
		o := e.WaitObject("sealprobe", "Room", n, 30*time.Second)
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if n == "victim3" || n == "late" || len(e.Audit(o.ID).Items) > 0 {
				break
			}
		}
	}
	time.Sleep(time.Second)
	for _, rec := range e.Audit("").Items {
		if strings.Contains(rec.ActingFor, "victim.example") || strings.Contains(string(rec.Detail), "ceo@victim") || strings.Contains(rec.ActingFor, "ceo-laptop") {
			t.Errorf("audit record carries the forged principal: %s", js(rec))
		}
	}
}
