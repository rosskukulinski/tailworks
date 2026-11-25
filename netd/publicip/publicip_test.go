package publicip

import (
	"net/netip"
	"testing"
)

func TestBlocked(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "127.1.2.3", "::1", "0.0.0.0", "::",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC 1918
		"fc00::1", "fd12:3456::1", // ULA
		"169.254.169.254", "169.254.0.1", "fe80::1", // link-local, cloud metadata
		"100.64.0.1", "100.100.100.100", "100.127.255.254", // CGNAT / tailnet / quad100
		"fd7a:115c:a1e0::1",                                             // tailnet IPv6
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", // v4-mapped
		"64:ff9b::7f00:1", "2002:7f00:1::", "2001:0:4136:e378::1", "::7f00:1", // translated
		"224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1", "198.18.0.1", "192.0.0.1", "fec0::1",
	} {
		if Blocked(netip.MustParseAddr(s)) == "" {
			t.Errorf("Blocked(%s) = \"\", want a refusal", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "203.0.113.10", "2606:4700:4700::1111", "::ffff:1.1.1.1"} {
		if why := Blocked(netip.MustParseAddr(s)); why != "" {
			t.Errorf("Blocked(%s) = %q, want public", s, why)
		}
	}
	if Blocked(netip.Addr{}) == "" {
		t.Error("zero Addr must be blocked")
	}
}

func TestSpecialName(t *testing.T) {
	for name, want := range map[string]bool{
		"api.github.com": false, "example.com": false, "a.b.co.uk": false,
		"localhost": true, "db": true, "foo.localhost": true, "printer.local": true,
		"metadata.google.internal": true, "1.0.0.127.in-addr.arpa": true, "router.lan": true,
		"127.1": true, "10.0.0.1.": true, "0x7f.0x1": true, "x.test": true, "": true,
	} {
		if got := SpecialName(name); got != want {
			t.Errorf("SpecialName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAllowlist(t *testing.T) {
	l := ParseAllowlist([]string{"API.Example.com.", "*.github.com", "203.0.113.0/24", "2001:db8::1", " "})
	for name, want := range map[string]bool{
		"api.example.com": true, "other.example.com": false, "github.com": false, "api.github.com": true,
		"203.0.113.7": true, "198.51.100.1": false, "2001:db8::1": true, "::ffff:203.0.113.9": true,
	} {
		if got := l.Allows(name); got != want {
			t.Errorf("Allows(%q) = %v, want %v", name, got, want)
		}
	}
	any := ParseAllowlist([]string{"*"})
	if !any.Allows("anything.example") || any.Allows("1.1.1.1") {
		t.Error(`"*" matches every name and no IP literal`)
	}
	if (Allowlist{}).Allows("api.example.com") {
		t.Error("empty list allows nothing")
	}
}
