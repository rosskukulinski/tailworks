// Package publicip is the SSRF guard shared by both internet egress paths:
// Workers' fetch() (netd, ADR-049) and containers'
// enableInternet (netd/sbxnet, ADR-032 D4). It decides which addresses an
// internet flow may dial (ADR-019) and matches tailnet.egress.internet
// allowlists, so the two paths can never disagree about either.
//
// The check is always applied to the address that is actually dialled,
// after DNS, never to a name: a name that resolves to a private address is
// refused like the address itself (no DNS rebinding past the check).
package publicip

import (
	"net/netip"
	"strings"
)

// blockedNets are reserved, tailnet and translated ranges that the
// stdlib predicates in Blocked don't cover.
var blockedNets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT: the tailnet range, incl. 100.100.100.100 (ADR-019)
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking; sbxnet's synthetic pool
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),               // IPv4-compatible (deprecated) embeds IPv4
	netip.MustParsePrefix("100::/64"),            // discard
	netip.MustParsePrefix("fec0::/10"),           // site-local (deprecated)
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"), // the tailnet's IPv6 range
	netip.MustParsePrefix("64:ff9b::/96"),        // NAT64 could reach any IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), // 6to4 embeds IPv4
	netip.MustParsePrefix("2001::/32"), // Teredo
}

// Blocked returns why a may not be dialled by internet egress, or "" for
// a public address. It does not know the host's own addresses; callers
// refuse those too.
func Blocked(a netip.Addr) string {
	a = a.Unmap()
	switch {
	case !a.IsValid():
		return "invalid address"
	case a.IsLoopback(), a.IsUnspecified():
		return "loopback/unspecified"
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(), a.IsMulticast(), a.IsInterfaceLocalMulticast():
		return "link-local/multicast (metadata services)"
	case a.IsPrivate():
		return "a private network"
	case !a.IsGlobalUnicast():
		return "not a global unicast address"
	case a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return "broadcast"
	}
	for _, n := range blockedNets {
		if n.Contains(a) {
			return "a reserved, tailnet or translated range (" + n.String() + ")"
		}
	}
	return ""
}

// specialTLDs are suffixes that never name a public internet host: the
// special-use names (RFC 6761, 6762, 8375, 9476) and common private ones.
// A name under one is refused before any DNS lookup, so it never reaches
// the host's resolver (which may well answer it with a LAN address).
var specialTLDs = []string{"alt", "arpa", "corp", "home", "internal", "intranet", "invalid", "lan", "local", "localdomain", "localhost", "onion", "test"}

// SpecialName reports whether name (lowercase, no trailing dot) cannot be
// a public DNS name: a single label, a special-use or private suffix, or a
// numeric last label (a resolver may read "127.1" or "0x7f.1" as an
// address).
func SpecialName(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return true
	}
	tld := name[dot+1:]
	if tld == "" || strings.Trim(tld, "0123456789") == "" || strings.HasPrefix(tld, "0x") {
		return true
	}
	for _, s := range specialTLDs {
		if tld == s {
			return true
		}
	}
	return false
}

// Allowlist is a parsed tailnet.egress.internet list: host names,
// "*.suffix" wildcards, "*" (any name), IPs and CIDRs, each with an
// optional ":port" (ports.go): "smtp.example.com:25", "[2001:db8::1]:25",
// "203.0.113.0/24:25", "*:25". An entry without a port allows every port
// the default port policy allows (RefusedPort); an entry with a port
// allows that port only, and is the only way to allow a refused port.
type Allowlist struct {
	hosts []hostEntry
	nets  []netEntry
}

type hostEntry struct {
	pattern string
	port    uint16 // 0: any port
}

type netEntry struct {
	net  netip.Prefix
	port uint16 // 0: any port
}

// ParseAllowlist parses entries (case and a trailing dot are ignored;
// blanks are skipped). An entry whose ":port" isn't 1..65535 is kept
// whole as a name, which matches nothing: the list form has always been
// lenient, and a bad entry allows nothing rather than failing the deploy.
func ParseAllowlist(entries []string) Allowlist {
	var l Allowlist
	for _, a := range entries {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		a, port := SplitEntryPort(a)
		a = strings.TrimSuffix(a, ".")
		if pf, err := netip.ParsePrefix(a); err == nil {
			l.nets = append(l.nets, netEntry{net: pf.Masked(), port: port})
		} else if ip, err := netip.ParseAddr(a); err == nil {
			l.nets = append(l.nets, netEntry{net: netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()), port: port})
		} else {
			l.hosts = append(l.hosts, hostEntry{pattern: a, port: port})
		}
	}
	return l
}

// Allows reports whether name (lowercase, no trailing dot; a host name or
// an IP literal) is on the list for some port. An IP literal matches only
// IP and CIDR entries; a name matches only host entries. It answers for a
// name alone (a DNS lookup); a flow asks AllowsPort.
func (l Allowlist) Allows(name string) bool {
	return l.match(name, 0, false)
}

// AllowsPort reports whether a flow to name:port is on the list: an entry
// with no port allows every port, an entry with a port allows that port
// only.
func (l Allowlist) AllowsPort(name string, port uint16) bool {
	return l.match(name, port, false)
}

// NamesPort reports whether an entry matching name names port explicitly
// ("smtp.example.com:25", "*:25", "203.0.113.0/24:25"). That is what
// lifts the default port policy (RefusedPort) for a destination.
func (l Allowlist) NamesPort(name string, port uint16) bool {
	return l.match(name, port, true)
}

// match: port 0 is any port; explicit needs an entry that names port.
func (l Allowlist) match(name string, port uint16, explicit bool) bool {
	portOK := func(ep uint16) bool {
		if explicit {
			return ep == port
		}
		return port == 0 || ep == 0 || ep == port
	}
	if ip, err := netip.ParseAddr(name); err == nil {
		for _, n := range l.nets {
			if n.net.Contains(ip.Unmap()) && portOK(n.port) {
				return true
			}
		}
		return false
	}
	for _, h := range l.hosts {
		if HostMatch(h.pattern, name) && portOK(h.port) {
			return true
		}
	}
	return false
}

// HostMatch matches a lowercase host pattern: "*" (any), "*.suffix" (any
// name strictly under suffix) or an exact name.
func HostMatch(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*."):
		return strings.HasSuffix(name, pattern[1:]) && len(name) > len(pattern)-1
	}
	return pattern == name
}
