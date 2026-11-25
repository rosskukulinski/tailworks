package hostd

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestEgressMACReplayRefusedByHandler pins ledger claim C11 (RT-05) to the
// wiring: the handler hostd serves on a workload's facet-egress socket
// (egressObjHandler) refuses a replayed (class, name, nonce, ts, mac)
// tuple and a MAC made under another workload's key, with the instance's
// own nonce cache. security/attacks/rt05_replay_test.go tests the verify
// function alone; this drives the handler that calls it.
//
// The class is a plain-DO class, so a request that passes the MAC check
// stops at "no facet class" before it could create anything: the two
// refusals are told apart by their detail.
func TestEgressMACReplayRefusedByHandler(t *testing.T) {
	t.Parallel()
	const root = "deadbeefdeadbeefdeadbeefdeadbeef"
	inst := &instance{
		res: &gen.Result{MACKey: root},
		app: &bundle.App{Classes: map[string]*bundle.Class{"Agent": {Name: "Agent", Egress: bundle.EgressWorkload}}},
	}
	h := (&Host{}).egressObjHandler(inst)

	const target = "http://peer.example.ts.net/x"
	send := func(key, nonce string, ts time.Time) string {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		tss := strconv.FormatInt(ts.UnixMilli(), 10)
		r.Header.Set(gen.HeaderClass, "Agent")
		r.Header.Set(gen.HeaderName, "a1")
		r.Header.Set(gen.HeaderTS, tss)
		r.Header.Set(gen.HeaderNonce, nonce)
		r.Header.Set(gen.HeaderMAC, gen.ComputeMAC(gen.ObjectMACKey(key, "Agent", "a1"), "Agent", "a1", "GET", r.RequestURI, tss, nonce))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var body struct{ Code, Detail string }
		json.Unmarshal(w.Body.Bytes(), &body)
		if body.Code != "unknown_object" {
			t.Fatalf("want unknown_object, got %d %s", w.Code, w.Body.String())
		}
		return body.Detail
	}
	const authFailed = "failed platform authentication"
	now := time.Now()
	if d := send(root, "n-1", now); strings.Contains(d, authFailed) || !strings.Contains(d, "no facet class") {
		t.Fatalf("a fresh, valid MAC was refused at authentication: %q", d)
	}
	if d := send(root, "n-1", now); !strings.Contains(d, authFailed) {
		t.Fatalf("RT-05: a replayed egress MAC got past the handler's check: %q", d)
	}
	if d := send(root, "n-2", now); strings.Contains(d, authFailed) {
		t.Fatalf("a fresh nonce was refused: %q", d)
	}
	if d := send(root, "n-3", now.Add(-20*time.Minute)); !strings.Contains(d, authFailed) {
		t.Fatalf("RT-05: an out-of-skew MAC got past the handler's check: %q", d)
	}
	if d := send("cafebabecafebabecafebabecafebabe", "n-4", now); !strings.Contains(d, authFailed) {
		t.Fatalf("RT-04/05: a MAC under another workload's key got past the handler's check: %q", d)
	}
}
