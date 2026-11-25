package mgmt

// The Host allowlist (DNS-rebinding guard, issue #105).
//
// A browser on the tailnet sends the Host header of the URL it loaded. An
// attacker who controls a DNS name can rebind it to mgmt's tailnet IP; a
// script on the attacker's page is then same-origin with mgmt and its
// requests carry the admin's WhoIs identity. What gives it away is the Host
// header: it names the attacker's domain, never one of mgmt's own names.
//
// So every request on the tailnet listeners (:80, :443, the -tsnet-service
// VIP, the operator console's loopback proxy) must name mgmt itself:
//   - the node's MagicDNS FQDN and its short name;
//   - -tsnet-hostname, short and under the tailnet's MagicDNS suffix (the
//     control plane may have given the node a suffixed name);
//   - the -tsnet-service name, short and under the suffix;
//   - any IP literal. Rebinding needs a DNS name the attacker controls; a
//     page whose URL is an IP literal is not same-origin with anything the
//     attacker serves. This covers mgmt's tailnet IPv4/IPv6 (the CLI dials
//     mgmt's IP through the loopback proxy, `tw up` points the embedded
//     host at it) and the Service VIP.
//
// The check runs for every method and path, before WhoIs. The unix socket
// (-local-dev) and the in-process host don't pass through it: no browser
// reaches them.

import (
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
)

// HostAllowlist is the set of names mgmt answers to on its tailnet
// listeners. A nil *HostAllowlist checks nothing (tests and embedders that
// build a Server by hand); tw-mgmt always sets one.
type HostAllowlist struct {
	names map[string]bool
}

// NewHostAllowlist returns an allowlist of names (case-insensitive, an
// optional port and trailing dot ignored; empty entries skipped).
func NewHostAllowlist(names ...string) *HostAllowlist {
	a := &HostAllowlist{names: map[string]bool{}}
	for _, n := range names {
		if n = normalizeHost(n); n != "" {
			a.names[n] = true
		}
	}
	return a
}

// MgmtHostNames lists mgmt's own names: self's FQDN and short name, the
// configured -tsnet-hostname and -tsnet-service (short, and under
// self.Tailnet's MagicDNS suffix). service may carry the "svc:" prefix.
func MgmtHostNames(self SelfInfo, hostname, service string) []string {
	var out []string
	suffix := strings.Trim(self.Tailnet, ".")
	add := func(short string) {
		short = strings.Trim(short, ".")
		if short == "" {
			return
		}
		out = append(out, short)
		if suffix != "" && !strings.Contains(short, ".") {
			out = append(out, short+"."+suffix)
		}
	}
	if fq := strings.TrimSuffix(self.DNSName, "."); fq != "" {
		out = append(out, fq)
		short, _, _ := strings.Cut(fq, ".")
		add(short)
	}
	add(hostname)
	add(strings.TrimPrefix(service, "svc:"))
	return out
}

// Allowed reports whether host (a Host header or URL host, with or without
// a port) names mgmt: one of the listed names, or any IP literal.
func (a *HostAllowlist) Allowed(host string) bool {
	if a == nil {
		return true
	}
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	return a.names[h]
}

// Names returns the listed names, sorted (for error messages).
func (a *HostAllowlist) Names() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.names))
	for n := range a.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// guard refuses r with 421 misdirected_request when its Host is not
// allowed, and reports whether the request may proceed.
func (a *HostAllowlist) guard(w http.ResponseWriter, r *http.Request) bool {
	if a.Allowed(r.Host) {
		return true
	}
	want := "its tailnet IP"
	if names := a.Names(); len(names) > 0 {
		want = strings.Join(names, ", ") + " or its tailnet IP"
	}
	e := errf(http.StatusMisdirectedRequest, "misdirected_request",
		"Host %q is not a name of this tw-mgmt (DNS-rebinding guard); address it as %s", r.Host, want)
	e.hostNames = a.Names()
	writeErr(w, r, e)
	return false
}

// normalizeHost lowercases host and strips a port, IPv6 brackets and a
// trailing dot. It returns "" for an empty or malformed host.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || strings.ContainsAny(host, "/@ \t[]") {
		return ""
	}
	return host
}
