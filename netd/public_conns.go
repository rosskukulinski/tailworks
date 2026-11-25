package netd

// Public listener connection limits (red-team H09): MaxConns per node,
// MaxConnsPerIP per client address, a short deadline for the first
// request, and a bounded lifetime for upgraded connections.

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/internal/publicspec"
)

// publicFirstRequestTimeout is how long a new public connection may take
// to complete its TLS handshake and send its first request's headers
// (shorter than ReadHeaderTimeout, which also applies to later requests).
// Tests shorten it.
var publicFirstRequestTimeout = 5 * time.Second

// connKey is the per-client key of the connection cap: the address for
// IPv4, the /64 for IPv6 (one client usually holds a whole /64).
func connKey(ip netip.Addr) netip.Addr {
	ip = ip.Unmap()
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.Addr()
		}
	}
	return ip
}

// admitConn takes a connection slot for ip, or returns the reject kind
// (rejConns over MaxConns, rejIPConns over MaxConnsPerIP).
func (p *publicState) admitConn(ip netip.Addr) (int, bool) {
	k := connKey(ip)
	p.mu.Lock()
	defer p.mu.Unlock()
	lim := p.opts.Limits
	if lim.MaxConns > 0 && p.conns >= lim.MaxConns {
		return rejConns, false
	}
	if lim.MaxConnsPerIP > 0 && p.ipConns[k] >= lim.MaxConnsPerIP {
		return rejIPConns, false
	}
	p.conns++
	if p.ipConns == nil {
		p.ipConns = map[netip.Addr]int{}
	}
	p.ipConns[k]++
	return 0, true
}

// releaseConn frees ip's slot.
func (p *publicState) releaseConn(ip netip.Addr) {
	k := connKey(ip)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns--
	if n := p.ipConns[k] - 1; n > 0 {
		p.ipConns[k] = n
	} else {
		delete(p.ipConns, k)
	}
}

// withConnDefaults fills the connection limits that predate a node's saved
// options (zero there means "not set", not "unlimited").
func withConnDefaults(l publicspec.Limits) publicspec.Limits {
	if l.MaxConnsPerIP <= 0 {
		l.MaxConnsPerIP = publicspec.Defaults.MaxConnsPerIP
	}
	if l.UpgradeLifetimeSeconds <= 0 {
		l.UpgradeLifetimeSeconds = publicspec.Defaults.UpgradeLifetimeSeconds
	}
	return l
}

// limitedConn releases its connection slot when closed, and holds every
// read deadline to publicFirstRequestTimeout after Accept until the first
// request's headers are in (firstRequestRead).
type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()

	mu    sync.Mutex
	first time.Time // the first-request deadline; zero once it arrived
	want  time.Time // the read deadline last asked for
}

func newLimitedConn(c net.Conn, release func()) *limitedConn {
	lc := &limitedConn{Conn: c, release: release, first: time.Now().Add(publicFirstRequestTimeout)}
	c.SetReadDeadline(lc.first)
	return lc
}

func (c *limitedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func (c *limitedConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.want = t
	if !c.first.IsZero() && (t.IsZero() || t.After(c.first)) {
		t = c.first
	}
	return c.Conn.SetReadDeadline(t)
}

func (c *limitedConn) SetDeadline(t time.Time) error {
	if err := c.Conn.SetWriteDeadline(t); err != nil {
		return err
	}
	return c.SetReadDeadline(t)
}

// firstRequestRead lifts the first-request deadline, restoring the read
// deadline the server last asked for.
func (c *limitedConn) firstRequestRead() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.first.IsZero() {
		return
	}
	c.first = time.Time{}
	c.Conn.SetReadDeadline(c.want)
}

// limitedConnOf unwraps the server's conn (a flow) to its limitedConn.
func limitedConnOf(c net.Conn) *limitedConn {
	for range 4 {
		switch t := c.(type) {
		case *limitedConn:
			return t
		case *flow:
			c = t.Conn
		default:
			return nil
		}
	}
	return nil
}

// publicConnState is the public http.Server's ConnState: net/http reports
// StateActive once a request's headers have been read, before the handler
// runs (and before its background read starts).
func publicConnState(c net.Conn, s http.ConnState) {
	if s != http.StateActive {
		return
	}
	if lc := limitedConnOf(c); lc != nil {
		lc.firstRequestRead()
	}
}

type pubConnCtxKey struct{}

// withPublicConn records the server's conn in a connection's context.
func withPublicConn(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, pubConnCtxKey{}, c)
}

// boundUpgrade bounds an upgraded request to lim's UpgradeLifetime: its
// context is cancelled then (an upstream proxy closes the backend) and the
// client connection is closed (freeing the slot whatever the upstream does).
// Call stop when the request returns.
func boundUpgrade(ctx context.Context, lim publicspec.Limits) (_ context.Context, stop func()) {
	d := lim.UpgradeLifetime()
	if d <= 0 {
		return ctx, func() {}
	}
	c, _ := ctx.Value(pubConnCtxKey{}).(net.Conn)
	ctx, cancel := context.WithTimeout(ctx, d)
	t := time.AfterFunc(d, func() {
		if c != nil {
			c.Close()
		}
	})
	return ctx, func() {
		t.Stop()
		cancel()
	}
}
