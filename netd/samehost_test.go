package netd_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/wgengine/filter"
)

// testSameHostHop: a flow from object a to a sibling on the same registry
// is delivered in process to the sibling's attributed ingress (RFC §4.8,
// ADR-service-bindings): the sibling sees a as its WhoIs-verified caller,
// by IP, bare name or FQDN. The destination's packet filter still decides:
// a source it does not accept is refused, as WireGuard would have dropped
// it (ADR-019 rule 2 is unchanged: no peer is added to any netmap).
func testSameHostHop(t *testing.T, e *env) {
	for _, c := range []struct{ name, url, want string }{
		{"sibling-ip", "http://" + e.objIP["b"] + "/", "b"},
		{"sibling-name", "http://tw-b/", "b"},
		{"sibling-fqdn", "http://tw-c." + nettest.MagicDNSDomain + "/", "c"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, b := e.get("http://"+e.objIP["a"]+"/egress?url="+c.url, nil)
			var r objResp
			if err := json.Unmarshal(b, &r); err != nil || code != 200 {
				t.Fatalf("%s: %d %s", c.url, code, b)
			}
			if r.ObjectID != c.want || r.Caller == nil || r.Caller.Hostname != "tw-a" || len(r.Caller.Addrs) == 0 || r.Caller.Addrs[0] != e.objIP["a"] {
				t.Fatalf("%s: sibling saw %s", c.url, b)
			}
			if len(r.Caller.CapMap[nettest.TestCap]) != 1 {
				t.Fatalf("%s: caller caps missing: %s", c.url, b)
			}
		})
	}

	t.Run("OtherPortRefused", func(t *testing.T) {
		code, h, b := e.get("http://"+e.objIP["a"]+"/egress?url=http://"+e.objIP["b"]+":8080/", nil)
		if code == 200 || h.Get("tw-error") != netd.CodeDialFailed {
			t.Fatalf("hop to a port nothing listens on: %d %q %s", code, h.Get("tw-error"), b)
		}
	})

	t.Run("PacketFilterRefuses", func(t *testing.T) {
		// testcontrol's packet filter accepts every signed node; an
		// UnsignedPeerAPIOnly node is left out of every filter's sources.
		// Like WireGuard's filter (which checks SYNs only), the hop checks
		// new flows: wait for b's filter, then use a name a's egress has no
		// pooled connection to.
		ctx := context.Background()
		st, err := e.reg.Get("a").LocalClient().StatusWithoutPeers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		k := st.Self.PublicKey
		bf := func() filter.Response {
			return e.reg.Get("b").Server().Sys().Engine.Get().GetFilter().CheckTCP(netip.MustParseAddr(e.objIP["a"]), netip.MustParseAddr(e.objIP["b"]), 80)
		}
		waitFilter := func(want filter.Response) {
			deadline := time.Now().Add(20 * time.Second)
			for bf() != want {
				if time.Now().After(deadline) {
					t.Fatalf("b's filter never answered %v for a", want)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		e.ctl.Server.SetUnsignedPeerAPIOnly(k, true)
		t.Cleanup(func() { e.ctl.Server.SetUnsignedPeerAPIOnly(k, false) })
		waitFilter(filter.Drop)
		u := "http://tw-b." + nettest.MagicDNSDomain + "/"
		code, h, b := e.get("http://"+e.objIP["a"]+"/egress?url="+u, nil)
		if code != 403 || h.Get("tw-error") != netd.CodePeerNotVisible || !strings.Contains(string(b), "packet filter") {
			t.Fatalf("hop past b's packet filter: %d %q %s", code, h.Get("tw-error"), b)
		}
		e.ctl.Server.SetUnsignedPeerAPIOnly(k, false)
		waitFilter(filter.Accept)
		if code, _, b := e.get("http://"+e.objIP["a"]+"/egress?url="+u, nil); code != 200 {
			t.Fatalf("hop after the grant is back: %d %s", code, b)
		}
	})
}
