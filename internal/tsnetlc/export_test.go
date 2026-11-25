package tsnetlc

import (
	"net"

	"tailscale.com/tsnet"
)

// SetDialHook makes every LocalAPI dial call h with the real dial (nil:
// dial directly).
func SetDialHook(h func(dial func() (net.Conn, error)) (net.Conn, error)) {
	if h == nil {
		dialHook.Store(nil)
		return
	}
	dialHook.Store(&h)
}

// CloseIdle closes ts's pooled LocalAPI connections, as Close does first.
func CloseIdle(ts *tsnet.Server) {
	if v, ok := transports.Load(ts); ok {
		v.(interface{ CloseIdleConnections() }).CloseIdleConnections()
	}
}

// Forget drops ts's transport (after a test closed ts by hand).
func Forget(ts *tsnet.Server) { transports.Delete(ts) }
