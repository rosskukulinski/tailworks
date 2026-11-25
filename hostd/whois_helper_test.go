package hostd_test

import (
	"context"
	"errors"
	"time"

	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/client/tailscale/apitype"
)

// whoisNode is n.LC.WhoIs for test servers that name their caller. WhoIs
// can answer with no error and no Node while the caller is not yet in n's
// netmap, which used to panic serveWhoisTCP's goroutine (and with it the
// whole hostd test binary). It asks again until the node is there or 10 s
// pass.
func whoisNode(ctx context.Context, n *nettest.Node, addr string) (*apitype.WhoIsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		who, err := n.LC.WhoIs(ctx, addr)
		if err == nil && who != nil && who.Node != nil {
			return who, nil
		}
		if err == nil {
			err = errors.New("whois returned no node")
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(100 * time.Millisecond):
		}
	}
}
