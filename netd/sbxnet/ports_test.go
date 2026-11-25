package sbxnet

import (
	"net/netip"
	"slices"
	"testing"
)

// TestInternetPortPolicy: containers' internet path applies the same port
// policy as Workers' (ADR-052 follow-up, issue #339): port 25 is refused
// unless the allowlist entry names it, and an entry that names a port
// covers that port only.
func TestInternetPortPolicy(t *testing.T) {
	res, dial, _ := standIn(t)
	p, _ := New(Config{Object: "tn/wl/Box/a", Internet: true, Resolve: res, Dial: dial,
		Allow: []string{"allowed.test", "api.allowed.test:25", "203.0.113.0/24", "198.51.100.0/24:25"}})
	allowed := resolve(t, p, "allowed.test")
	api := resolve(t, p, "api.allowed.test")
	for _, c := range []struct {
		dst  netip.AddrPort
		want bool
		code string
	}{
		{netip.AddrPortFrom(allowed, 25), false, "allowed.test:25 internet_port_refused"},
		{netip.AddrPortFrom(allowed, 587), true, ""},
		{netip.AddrPortFrom(allowed, 80), true, ""},
		{netip.AddrPortFrom(api, 25), true, ""},                                       // the entry names the port
		{netip.AddrPortFrom(api, 443), false, "api.allowed.test:443 not_allowlisted"}, // and only that port
		{netip.MustParseAddrPort("203.0.113.7:25"), false, "203.0.113.7:25 internet_port_refused"},
		{netip.MustParseAddrPort("203.0.113.7:587"), true, ""},
		{netip.MustParseAddrPort("198.51.100.9:25"), true, ""},
		{netip.MustParseAddrPort("198.51.100.9:443"), false, "198.51.100.9:443 not_allowlisted"},
	} {
		if got := p.Accept(c.dst); got != c.want {
			t.Errorf("Accept(%s) = %v, want %v", c.dst, got, c.want)
		}
		if c.code != "" && !slices.Contains(p.Refused(), c.code) {
			t.Errorf("Accept(%s): refusals %v lack %q", c.dst, p.Refused(), c.code)
		}
	}
}
