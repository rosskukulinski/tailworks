package netd

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync/atomic"

	"github.com/rosskukulinski/tailworks/internal/tssvc"
	"tailscale.com/wgengine/filter"
)

// The same-host hop (RFC §4.8, ADR-service-bindings).
//
// A destination that is another object hosted by this registry (a sibling)
// is never dialed over WireGuard: that hairpin is what ADR-019 rule 2
// forbids. It is delivered in-process instead, to the sibling's own
// attributed ingress, on a connection whose remote address is the source
// object's tailnet address. Nothing else changes for the request:
//
//   - the destination must already be a peer in the source's netmap (the
//     name or IP was resolved there, as for any tailnet destination), so no
//     peer visibility is added between co-located objects;
//   - the destination node's own packet filter must accept the flow
//     (source address → destination address:port, TCP), which is exactly the
//     check WireGuard would have made: a grant is still required;
//   - the sibling's ingress runs WhoIs on the source address with its own
//     LocalClient, applies the caller's node certificate and the kill list,
//     and stamps the cf blob, as for a flow that arrived over the tailnet.
//
// Both ends are tracked flows, so killing either object resets the hop.
// Config.AllowSiblings keeps the old behaviour (dial over WireGuard).

// hopPorts numbers the source ports of hop connections (display only:
// WhoIs keys on the address).
var hopPorts atomic.Uint32

// hopConn is one end of an in-process hop with tailnet addresses.
type hopConn struct {
	net.Conn
	local, remote net.Addr
}

func (c *hopConn) LocalAddr() net.Addr  { return c.local }
func (c *hopConn) RemoteAddr() net.Addr { return c.remote }

// startHopLocked creates the hop listener and its server from the node's
// ingress server hs (start, once the node is up; the caller holds o.mu and
// serves the returned listener with the returned server).
func (o *Object) startHopLocked(hs *http.Server) (*http.Server, net.Listener) {
	o.hopLn = tssvc.NewConnListener(tssvc.Addr{Name: "hop:" + o.hostname})
	o.hopHTTP = &http.Server{Handler: hs.Handler, ReadHeaderTimeout: hs.ReadHeaderTimeout, ErrorLog: hs.ErrorLog}
	return o.hopHTTP, &trackedListener{Listener: o.hopLn, o: o}
}

// sibling returns the object this registry hosts at ip (a node address),
// or nil.
func (r *Registry) sibling(ip netip.Addr) *Object {
	r.mu.Lock()
	id, ok := r.localIPs[ip]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return r.Get(id)
}

// serviceSibling returns the sibling to deliver a flow to Service VIP vip
// to, when every host the source can reach for it is a sibling (a remote
// host is dialed over the tailnet as usual). It returns nil when vip is not
// a Service VIP in o's netmap.
func (o *Object) serviceSibling(ctx context.Context, vip netip.Addr) *Object {
	if !o.reg.servicesHosted() {
		return nil // no object here advertises a Service: skip the netmap read
	}
	o.mu.Lock()
	lc := o.lc
	o.mu.Unlock()
	if lc == nil {
		return nil
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return nil
	}
	var sib *Object
	for _, p := range st.Peer {
		if p.AllowedIPs == nil {
			continue
		}
		routes := false
		for _, pfx := range p.AllowedIPs.All() {
			if pfx.Contains(vip) && pfx.Bits() == vip.BitLen() {
				routes = true
				break
			}
		}
		if !routes {
			continue
		}
		var s *Object
		for _, a := range p.TailscaleIPs {
			if s = o.reg.sibling(a); s != nil {
				break
			}
		}
		if s == nil {
			return nil // a remote host serves it too: use the tailnet
		}
		if sib == nil {
			sib = s
		}
	}
	return sib
}

// servicesHosted reports whether any object of r advertises a Service.
func (r *Registry) servicesHosted() bool {
	r.mu.Lock()
	objs := make([]*Object, 0, len(r.objects))
	for _, o := range r.objects {
		objs = append(objs, o)
	}
	r.mu.Unlock()
	for _, o := range objs {
		o.mu.Lock()
		n := len(o.services)
		o.mu.Unlock()
		if n > 0 {
			return true
		}
	}
	return false
}

// hopTarget reports where an egress dial to ap must be delivered in
// process: the sibling and whether ap is a Service VIP it hosts. ok is false
// for a destination that is dialed over the tailnet.
func (o *Object) hopTarget(ctx context.Context, ap netip.AddrPort) (dst *Object, service, ok bool) {
	if o.reg.cfg.AllowSiblings {
		return nil, false, false
	}
	if s := o.reg.sibling(ap.Addr()); s != nil {
		return s, false, true
	}
	if s := o.serviceSibling(ctx, ap.Addr()); s != nil {
		return s, true, true
	}
	return nil, false, false
}

// dialHop delivers a flow from o to dst (a sibling) at ap in process.
func (o *Object) dialHop(dst *Object, ap netip.AddrPort, service bool) (net.Conn, error) {
	refuse := func(code, detail string) (net.Conn, error) {
		e := newErr(code, detail)
		e.Object, e.Destination = o.id, ap.String()
		return nil, e
	}
	o.mu.Lock()
	src4, src6, srcState := o.ip4, o.ip6, o.state
	o.mu.Unlock()
	if srcState != StateReady {
		return refuse(CodeKilled, "object "+o.id+" is "+string(srcState))
	}
	src := src4
	if ap.Addr().Is6() {
		src = src6
	}
	if !src.IsValid() {
		return refuse(CodeDialFailed, "this object's node has no address of the destination's family")
	}
	dst.mu.Lock()
	dstState, dsrv := dst.state, dst.srv
	ln := dst.hopLn
	if service {
		ln = dst.svcLn
	}
	dst.mu.Unlock()
	if dstState != StateReady || dsrv == nil || ln == nil {
		return refuse(CodeKilled, "destination object "+dst.id+" is "+string(dstState))
	}
	if dst.pub != nil {
		// A public (Funnel) node has no tailnet listener (ADR-029).
		return refuse(CodeDialFailed, "connection refused: the destination is a public node, which listens only on Funnel")
	}
	if int(ap.Port()) != o.reg.cfg.IngressPort {
		return refuse(CodeDialFailed, "connection refused: the destination listens on port "+strconv.Itoa(o.reg.cfg.IngressPort)+" only")
	}
	// The destination's packet filter is the grant check WireGuard would
	// have made on this flow.
	var f *filter.Filter
	if sys := dsrv.Sys(); sys != nil {
		if eng, ok := sys.Engine.GetOK(); ok && eng != nil {
			f = eng.GetFilter()
		}
	}
	if f == nil {
		return refuse(CodeIdentityPending, "the destination's packet filter is not loaded yet")
	}
	if f.CheckTCP(src, ap.Addr(), ap.Port()) != filter.Accept {
		return refuse(CodePeerNotVisible, "the destination's packet filter refuses this object (a grant from its tags to the destination is missing)")
	}
	c, s := net.Pipe()
	srcAddr := &net.TCPAddr{IP: src.AsSlice(), Port: 32768 + int(hopPorts.Add(1)%28000)}
	dstAddr := &net.TCPAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port())}
	client := &hopConn{Conn: c, local: srcAddr, remote: dstAddr}
	f2, ok := o.track(client)
	if !ok {
		s.Close()
		return refuse(CodeKilled, "object "+o.id+" was killed")
	}
	// The destination's listener tracks (and on a kill resets) its end.
	go ln.Deliver(&hopConn{Conn: s, local: dstAddr, remote: srcAddr})
	return f2, nil
}
