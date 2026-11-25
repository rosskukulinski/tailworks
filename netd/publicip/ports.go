package publicip

import (
	"net/netip"
	"strings"
)

// The default port policy for internet egress (ADR-052 follow-up,
// 2026-11-24; issue #339). Internet flows leave from the host's own
// address, which the destination can't tell from the operator's. TCP 25 is
// SMTP relay between mail servers, so a workload that reaches it (a
// prompt-injected agent, say) sends mail as the operator. It is refused
// unless the workload's tailnet.egress.internet names the destination
// with that port ("smtp.example.com:25"). Authenticated submission (465,
// 587) is not relay and stays open. Both internet paths (Workers in netd,
// containers in netd/sbxnet) apply this, like Blocked.

// PortSMTP is the one port the default policy refuses.
const PortSMTP uint16 = 25

// RefusedPort returns why internet egress to port is refused by default,
// or "" for a port the policy allows.
func RefusedPort(port uint16) string {
	if port == PortSMTP {
		return "TCP port 25 (SMTP relay) is refused by default: mail sent from a workload would come from this host's own address"
	}
	return ""
}

// SplitEntryPort splits an allowlist entry's optional ":port" suffix:
// "smtp.example.com:25", "*:25", "203.0.113.7:25", "203.0.113.0/24:25",
// "[2001:db8::1]:25", "2001:db8::/32:25". A bare IPv6 address
// ("2001:db8::25") names no port. port is 0 when the entry names none.
// An entry whose suffix isn't a port in 1..65535 is returned whole, so a
// typo matches nothing (ParseAllowlist).
func SplitEntryPort(entry string) (string, uint16) {
	if strings.HasPrefix(entry, "[") {
		end := strings.IndexByte(entry, ']')
		if end < 0 {
			return entry, 0
		}
		addr, rest := entry[1:end], entry[end+1:]
		if rest == "" {
			return addr, 0
		}
		if port, ok := parsePort(strings.TrimPrefix(rest, ":")); ok && strings.HasPrefix(rest, ":") {
			return addr, port
		}
		return entry, 0
	}
	i := strings.LastIndexByte(entry, ':')
	if i <= 0 {
		return entry, 0
	}
	if strings.Count(entry, ":") > 1 {
		// IPv6: only "prefix/len:port" carries a port, and only when the
		// part before the last colon is a prefix on its own.
		if _, err := netip.ParsePrefix(entry[:i]); err != nil {
			return entry, 0
		}
	}
	port, ok := parsePort(entry[i+1:])
	if !ok {
		return entry, 0
	}
	return entry[:i], port
}

// EntryPortOK reports whether entry's ":port" suffix, if it has one, is a
// port (1..65535). It is for validation at deploy; ParseAllowlist itself
// keeps a bad entry as a name that matches nothing.
func EntryPortOK(entry string) bool {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if _, port := SplitEntryPort(entry); port != 0 {
		return true
	}
	// Nothing was split off: is there a suffix that was meant as a port?
	if strings.HasPrefix(entry, "[") {
		return strings.HasSuffix(entry, "]")
	}
	i := strings.LastIndexByte(entry, ':')
	if i <= 0 {
		return true
	}
	if strings.Count(entry, ":") > 1 {
		_, err := netip.ParsePrefix(entry[:i])
		return err != nil // "2001:db8::/32:x" is a typo; "2001:db8::1" is an address
	}
	return false // "host:x"
}

func parsePort(s string) (uint16, bool) {
	if s == "" || len(s) > 5 || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > 65535 {
		return 0, false
	}
	return uint16(n), true
}
