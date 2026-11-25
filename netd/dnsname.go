package netd

import (
	"context"
	"time"

	"tailscale.com/ipn/ipnstate"
)

// nameWait bounds how long a join waits for the node's own MagicDNS name.
// A control that never sends one (no DNS name configured) still joins, as
// before, with an empty name.
const nameWait = 10 * time.Second

// statusWithName reads status until the node's own MagicDNS name is in its
// netmap, for up to nameWait. A node can be Running before its first
// netmap carries its name. Before #599 such a node became ready with an
// empty name, so hostd reported ready to mgmt with no MagicDNS and
// ctx.tailnet.self() answered "" (ADR-031: the final name is read back
// before ready). After nameWait (or when ctx ends) it returns the last
// status it read, name or not.
func statusWithName(ctx context.Context, status func(context.Context) (*ipnstate.Status, error)) (*ipnstate.Status, error) {
	wctx, cancel := context.WithTimeout(ctx, nameWait)
	defer cancel()
	var last *ipnstate.Status
	for {
		st, err := status(wctx)
		if err == nil {
			last = st
			if st.Self != nil && st.Self.DNSName != "" {
				return st, nil
			}
		}
		select {
		case <-wctx.Done():
			if last != nil {
				return last, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return status(ctx)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
