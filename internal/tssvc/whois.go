package tssvc

import (
	"context"
	"net"
	"net/http"
	"net/netip"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
)

// WhoIs is lc.WhoIs with the CapMap scoped to the address the caller
// dialed: the connection's local address, which net/http stores in the
// request context (http.LocalAddrContextKey).
//
// Plain WhoIs computes app capabilities for flows to the node's own address,
// whatever the flow's real destination. On a flow through a Service VIP that
// is wrong both ways (H22, measured on a real tailnet): a grant with a `svc:`
// destination is missing, and a grant to the node's tags shows up although
// the caller did not dial the node. WhoIsForIP with the VIP gives exactly the
// grants whose destination covers the VIP; on the node's own address it gives
// the same caps as plain WhoIs.
//
// Without a local address in ctx (not an http.Server connection) it falls
// back to plain WhoIs.
func WhoIs(ctx context.Context, lc *local.Client, remoteAddr string) (*apitype.WhoIsResponse, error) {
	if dst, ok := dialedAddr(ctx); ok {
		return lc.WhoIsForIP(ctx, remoteAddr, dst)
	}
	return lc.WhoIs(ctx, remoteAddr)
}

// dialedAddr is the connection's local IP from an http.Server request context.
func dialedAddr(ctx context.Context) (netip.Addr, bool) {
	la, ok := ctx.Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || la == nil {
		return netip.Addr{}, false
	}
	ap, err := netip.ParseAddrPort(la.String())
	if err != nil || !ap.Addr().IsValid() || ap.Addr().IsUnspecified() {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}
