package mgmt

import (
	"slices"
	"strconv"
	"strings"
)

// tailnet.connects entries (ADR-052). An entry names a target (a
// tag, autogroup, "*" or a name) and, optionally, the TCP port the workload
// dials it on: "tag:db:5432". Without a port the preflight keeps its
// tcp:80 default (fetch()).

// SplitConnect splits a tailnet.connects entry into its target and the
// grant "ip" it needs ("tcp:<port>"; "" when it names no port).
func SplitConnect(s string) (target, ip string) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || strings.HasSuffix(s[:i], ":") {
		return s, ""
	}
	p, err := strconv.ParseUint(s[i+1:], 10, 16)
	if err != nil || p == 0 {
		return s, ""
	}
	// "tag:db" alone is a target, not "tag" on port db; a bare name with
	// a port ("db.example.ts.net:5432") splits too.
	return s[:i], "tcp:" + strconv.FormatUint(p, 10)
}

// ConnectTargets are the entries' targets (ports dropped, deduplicated, in
// order).
func ConnectTargets(connects []string) []string {
	var out []string
	for _, c := range connects {
		t, _ := SplitConnect(c)
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// HasIPGrantPort is HasIPGrant for one port ("tcp:5432"): some grant's
// "ip" must cover it ("*", "5432", "tcp:5432", "tcp:*", a range
// "tcp:5000-6000"). An empty ip is HasIPGrant.
func (p *Policy) HasIPGrantPort(src, dst, ip string) bool {
	if ip == "" {
		return p.HasIPGrant(src, dst)
	}
	port, _ := strconv.Atoi(strings.TrimPrefix(ip, "tcp:"))
	for _, g := range p.Grants {
		if len(g.IP) == 0 || !srcMatches(g.Src, []string{src}) {
			continue
		}
		if !slices.Contains(g.Dst, "*") && !slices.Contains(g.Dst, dst) {
			continue
		}
		for _, e := range g.IP {
			if ipCoversTCP(e, port) {
				return true
			}
		}
	}
	return false
}

// ipCoversTCP reports whether a grant "ip" entry covers TCP port.
func ipCoversTCP(e string, port int) bool {
	e = strings.TrimSpace(e)
	if e == "*" {
		return true
	}
	ports := e
	if proto, rest, ok := strings.Cut(e, ":"); ok {
		if proto != "tcp" && proto != "6" {
			return false
		}
		ports = rest
	}
	if ports == "*" {
		return true
	}
	lo, hi, isRange := strings.Cut(ports, "-")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return false
	}
	b := a
	if isRange {
		if b, err = strconv.Atoi(hi); err != nil {
			return false
		}
	}
	return a <= port && port <= b
}
