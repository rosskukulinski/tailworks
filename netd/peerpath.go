package netd

import (
	"context"
	"net/netip"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// peerPathPoll is how often WaitPeerPath re-reads the node's status.
const peerPathPoll = 20 * time.Millisecond

// HasPath reports whether this node knows any way to send to a peer: a
// home DERP region, an endpoint or an active direct address. A peer that
// has just registered is in the netmap before it has any of these (they
// arrive after its first netcheck); a packet sent to it then is dropped,
// and WireGuard retries the handshake only after REKEY_TIMEOUT (5 s).
func HasPath(p *ipnstate.PeerStatus) bool {
	return p != nil && (p.Relay != "" || p.CurAddr != "" || len(p.Addrs) > 0 || p.PeerRelay != "")
}

// WaitPeerPath waits until the peer with tailnet address ip has a path
// (HasPath) as seen by lc's node, or ctx ends. A peer that isn't in the
// netmap at all also waits (it may be about to appear). Callers bound ctx
// and dial anyway on timeout: this only avoids the 5 s first-handshake
// penalty, it never decides reachability.
func WaitPeerPath(ctx context.Context, lc *local.Client, ip netip.Addr) error {
	for {
		st, err := lc.Status(ctx)
		if err == nil {
			for _, p := range st.Peer {
				for _, a := range p.TailscaleIPs {
					if a == ip && HasPath(p) {
						return nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(peerPathPoll):
		}
	}
}

// WaitPeerReady is WaitPeerPath, then one disco pong from the peer. A path
// in this node's view doesn't mean the peer is ready for us: a peer that
// has just joined may not have this node in its own netmap yet, and drops
// our first handshake initiation; WireGuard then retries only after 5 s.
// The peer answers a disco ping only once it knows this node, so a pong
// means the handshake that follows is answered. Each attempt has a short
// timeout of its own (a lost ping would otherwise wait out magicsock's 5 s
// ping timeout) and is retried until ctx ends.
func WaitPeerReady(ctx context.Context, lc *local.Client, ip netip.Addr) error {
	if err := WaitPeerPath(ctx, lc, ip); err != nil {
		return err
	}
	attempt := peerPingAttempt
	for {
		actx, cancel := context.WithTimeout(ctx, attempt)
		res, err := lc.Ping(actx, ip, tailcfg.PingDisco)
		cancel()
		if err == nil && res != nil && res.Err == "" {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < time.Second {
			attempt *= 2
		}
	}
}

// peerPingAttempt is WaitPeerReady's first per-ping timeout; it doubles
// on each retry, up to 1 s (a far peer reached over DERP).
const peerPingAttempt = 250 * time.Millisecond
