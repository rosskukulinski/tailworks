package netd

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/rosskukulinski/tailworks/netd/publicip"
	"tailscale.com/ipn/ipnstate"
)

// RecipeInternetAPI is the gateway recipe: how a Worker whose internet
// egress is off (or that needs a fixed source address) calls an internet
// API through the tailnet.
const RecipeInternetAPI = "https://github.com/rosskukulinski/tailworks/blob/master/docs/recipes.md#call-an-internet-api"

// InternetConfig is the Worker internet egress path
// (ADR-049). A flow to a destination that is not on the
// tailnet leaves from this host's own network, never from the object's
// tailnet node, after the SSRF guard (publicip) has checked the address
// that is actually dialled.
type InternetConfig struct {
	// Dial opens a TCP flow on the host network to an IP:port that has
	// already passed the guard. Resolve looks a name up (the host
	// resolver in production). Tests substitute hermetic stand-ins.
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// HostAddrs are refused in addition to the host's interface
	// addresses (RefreshHostIPs).
	HostAddrs []netip.Addr
	// Policy is the object's tailnet.egress.internet. Nil: every object
	// may reach any public host.
	Policy func(objectID string) InternetPolicy
	// OnFlow is called for each internet flow opened (audit and logs).
	OnFlow func(InternetFlow)
}

// InternetPolicy is one object's internet egress setting.
type InternetPolicy struct {
	// Off refuses every internet destination (tailworks.jsonc
	// tailnet.egress.internet: false or []).
	Off bool
	// Allow limits internet destinations to these hosts ("*.suffix", IPs,
	// CIDRs); nil allows any public host.
	Allow []string
}

// InternetFlow is one internet flow netd opened for an object.
type InternetFlow struct {
	Object string         // netd object ID
	Host   string         // the name (or IP literal) the object asked for, with port
	Addr   netip.AddrPort // the checked address that was dialled
}

// internetDst is a destination resolveEgress routed to the internet path:
// the name (lowercase) or IP literal, and the port.
type internetDst struct {
	host string
	port uint16
}

func (d *internetDst) hostport() string {
	return net.JoinHostPort(d.host, strconv.Itoa(int(d.port)))
}

// internetEgressDetail explains an internet destination refused because
// internet egress is off for this object or this host.
func internetEgressDetail(host, why string) string {
	return host + " is an internet destination, and " + why + ". " +
		"Set tailnet.egress.internet in tailworks.jsonc, or put a gateway for it on your tailnet and call that: " + RecipeInternetAPI
}

// internetRoute decides whether (host, port), which is not a tailnet
// destination, may go out the internet path. ipLiteral is set for an IP
// destination (already known not to be a tailnet address).
func (o *Object) internetRoute(host string, port uint16, ipLiteral bool) (*internetDst, *Error) {
	refuse := func(code, detail string) (*internetDst, *Error) {
		e := newErr(code, detail)
		e.Object, e.Destination = o.id, net.JoinHostPort(host, strconv.Itoa(int(port)))
		return nil, e
	}
	ic := o.reg.cfg.Internet
	if ic == nil {
		if ipLiteral {
			return refuse(CodeForbiddenDestination, "non-tailnet destination (this host has no internet egress path)")
		}
		return refuse(CodeInternetEgressUnavailable, internetEgressDetail(host, "this host has internet egress turned off (tw up --worker-internet=off, or tw-hostd -worker-internet=off)"))
	}
	if ipLiteral {
		ip, _ := netip.ParseAddr(host)
		if why := o.reg.internetBlocked(ip); why != "" {
			return refuse(CodeForbiddenDestination, host+" is "+why)
		}
	} else if publicip.SpecialName(host) {
		return refuse(CodeForbiddenDestination, host+" is a special-use or private name, never looked up on the host")
	}
	var pol InternetPolicy
	if ic.Policy != nil {
		pol = ic.Policy(o.id)
	}
	if pol.Off {
		return refuse(CodeInternetEgressUnavailable, internetEgressDetail(host, "this workload has internet egress turned off (tailnet.egress.internet)"))
	}
	if code, detail := internetPortPolicy(host, port, pol); code != "" { // internetports.go
		return refuse(code, detail)
	}
	return &internetDst{host: host, port: port}, nil
}

// internetBlocked is the full SSRF guard for one address: publicip's
// ranges plus this host's own addresses and the platform's endpoints.
func (r *Registry) internetBlocked(a netip.Addr) string {
	a = a.Unmap()
	if why := publicip.Blocked(a); why != "" {
		return why
	}
	r.mu.Lock()
	host := r.hostIPs[a]
	r.mu.Unlock()
	if !host && r.cfg.Internet != nil {
		for _, h := range r.cfg.Internet.HostAddrs {
			host = host || h.Unmap() == a
		}
	}
	if host {
		return "an address of this host"
	}
	for _, p := range r.cfg.ExtraForbidden {
		if p.Contains(a) {
			return "a platform endpoint"
		}
	}
	return ""
}

// dialInternet resolves d on the host, refuses the whole name if any
// answer is a forbidden address (no DNS rebinding: the addresses checked
// are the ones dialled, and a name that mixes public and private answers
// is refused outright), and dials from the host network. The flow is
// tracked, so a kill RSTs it.
func (o *Object) dialInternet(ctx context.Context, d *internetDst) (net.Conn, error) {
	ic := o.reg.cfg.Internet
	refuse := func(code, detail string) (net.Conn, error) {
		e := newErr(code, detail)
		e.Object, e.Destination = o.id, d.hostport()
		return nil, e
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(d.host); err == nil {
		addrs = []netip.Addr{ip.Unmap()}
	} else {
		as, err := ic.Resolve(ctx, d.host)
		if err != nil {
			if e, ok := AsError(err); ok {
				return nil, o.internetErr(e, d)
			}
			return refuse(CodeDialFailed, "resolving "+d.host+": "+err.Error())
		}
		addrs = as
	}
	if len(addrs) == 0 {
		return refuse(CodeDialFailed, "no addresses for "+d.host)
	}
	for _, a := range addrs {
		if why := o.reg.internetBlocked(a); why != "" {
			return refuse(CodeForbiddenDestination, d.host+" resolves to "+a.Unmap().String()+", which is "+why)
		}
	}
	var last error
	for _, a := range addrs {
		ap := netip.AddrPortFrom(a.Unmap(), d.port)
		c, err := ic.Dial(ctx, "tcp", ap.String())
		if err != nil {
			if e, ok := AsError(err); ok {
				return nil, o.internetErr(e, d)
			}
			last = err
			continue
		}
		f, ok := o.track(c)
		if !ok {
			return nil, &Error{Code: CodeKilled, Detail: "object " + o.id + " was killed", Object: o.id}
		}
		o.reg.log.Debug("internet egress", "object", o.id, "dst", d.hostport(), "addr", ap.String())
		if ic.OnFlow != nil {
			ic.OnFlow(InternetFlow{Object: o.id, Host: d.hostport(), Addr: ap})
		}
		return f, nil
	}
	return nil, last
}

// internetErr is a typed refusal from the internet path itself (for
// example exit_node_unavailable), attributed to this object and
// destination. The path's error is copied, never mutated.
func (o *Object) internetErr(e *Error, d *internetDst) *Error {
	c := *e
	c.Object, c.Destination = o.id, d.hostport()
	if c.DocURL == "" {
		c.DocURL = DocBase + c.Code
	}
	return &c
}

// internetOnce dedups per-object, per-destination events (the first flow
// to each destination is audited, like containers' sandbox egress).
type internetOnce struct {
	mu   sync.Mutex
	seen map[string]bool
}

// First reports whether key is new; it remembers at most 4096 keys, after
// which every key is reported as new (more records, never fewer).
func (s *internetOnce) First(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[key] {
		return false
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if len(s.seen) < 4096 {
		s.seen[key] = true
	}
	return true
}

// FirstFlowOnly wraps an OnFlow callback so it sees only the first flow
// per object and destination.
func FirstFlowOnly(f func(InternetFlow)) func(InternetFlow) {
	var s internetOnce
	return func(fl InternetFlow) {
		if s.First(fl.Object + "|" + fl.Host) {
			f(fl)
		}
	}
}

// tailnetShaped reports whether want (lowercase, no trailing dot) looks like
// a name on a tailnet, so a miss means a missing peer or grant rather than an
// internet destination: a bare MagicDNS short name, a name under this node's
// MagicDNS suffix or any visible peer's, or any *.ts.net name. Such a name
// is never looked up on the host.
func tailnetShaped(want string, st *ipnstate.Status) bool {
	if !strings.Contains(want, ".") {
		return true
	}
	under := func(suffix string) bool {
		suffix = strings.Trim(strings.ToLower(suffix), ".")
		return suffix != "" && strings.HasSuffix(want, "."+suffix)
	}
	if under("ts.net") || under("beta.tailscale.net") {
		return true
	}
	if st == nil {
		return false
	}
	if st.CurrentTailnet != nil && under(st.CurrentTailnet.MagicDNSSuffix) {
		return true
	}
	//lint:ignore SA1019 fallback for a node whose status has no CurrentTailnet (checked first, above)
	if under(st.MagicDNSSuffix) {
		return true
	}
	if st.Self != nil {
		if _, s, ok := strings.Cut(strings.TrimSuffix(st.Self.DNSName, "."), "."); ok && under(s) {
			return true
		}
	}
	for _, p := range st.Peer {
		if _, s, ok := strings.Cut(strings.TrimSuffix(p.DNSName, "."), "."); ok && under(s) {
			return true
		}
	}
	return false
}
