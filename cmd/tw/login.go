package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/mgmtd"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/internal/tsnetlc"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg/nodecap"
	"tailscale.com/tsnet"
)

// mgmtNode describes mgmt's tailnet node after a join.
type mgmtNode struct {
	Tailnet string   // MagicDNS suffix, e.g. tail7906d5.ts.net
	DNSName string   // tw-mgmt.tail7906d5.ts.net
	NodeID  string   // stable node ID
	IP      string   // first tailnet IP
	Tags    []string // the node's tags (control's view)
	HTTPS   bool     // the tailnet issues HTTPS certificates for this node
	Funnel  bool     // the node has the funnel attribute (tag:tw-mgmt nodeAttrs)
	Login   bool     // an interactive login happened in this call
}

// joinOpts configures joinMgmtNode.
type joinOpts struct {
	Dir, Hostname, ControlURL string
	Tags                      []string
	AuthKey                   string // "" = interactive login
	Timeout                   time.Duration
	Out                       io.Writer // where the login URL and progress go
}

// joinMgmtNode joins (or re-opens) a tsnet node from the CLI, so an
// interactive login happens in the operator's terminal: it prints the
// login URL, waits for the login, reads the netmap (tailnet name, DNS
// name, HTTPS, funnel) and closes the node again. `tw link --tsnet` uses
// it for the CLI's own node. `tw up` no longer does: mgmt joins its own
// node with the single-use key tw up hands it in a 0600 file
// (up_mgmtjoin.go). The system tailscaled is never used: this is a
// separate tsnet node whose state lives under Dir.
func joinMgmtNode(ctx context.Context, o joinOpts) (*mgmtNode, error) {
	ts := &tsnet.Server{Hostname: o.Hostname, Dir: o.Dir, ControlURL: o.ControlURL, AdvertiseTags: o.Tags, AuthKey: o.AuthKey,
		Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	defer tsnetlc.Close(ts)
	lc, err := tsnetlc.Client(ts)
	if err != nil {
		return nil, fail("bootstrap_failed", "could not start mgmt's tailnet node", err.Error())
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	wctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	w, err := lc.WatchIPNBus(wctx, ipn.NotifyInitialState)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	res := &mgmtNode{}
	printedURL := ""
	for {
		n, err := w.Next()
		if err != nil {
			if wctx.Err() != nil {
				return nil, fail("login_timeout", fmt.Sprintf("no login after %v", o.Timeout),
					"the login URL was printed above; tw link --tsnet waits for the browser login to complete").
					withFix("run tw link --tsnet again and finish the login, or pass --tsnet-authkey-file to tw link --tsnet for an unattended join")
			}
			return nil, fail("bootstrap_failed", "tailnet join failed", err.Error())
		}
		if n.ErrMessage != nil {
			msg := *n.ErrMessage
			if tsapi.IsTagsNotPermitted(errors.New(msg)) {
				return nil, policyMissingTagOwners("the control server refused the join of "+o.Hostname+" with tags "+strings.Join(o.Tags, ", "), errors.New(msg), nil)
			}
			e := fail("bootstrap_failed", "the control server refused the join", msg)
			if strings.Contains(strings.ToLower(msg), "tag") {
				e.Fix = append(e.Fix, "the account you logged in with must be allowed to own "+strings.Join(o.Tags, ", ")+" (tagOwners); add the snippet printed above, then run tw up again")
			}
			return nil, e
		}
		if n.BrowseToURL != nil && *n.BrowseToURL != printedURL {
			printedURL = *n.BrowseToURL
			res.Login = true
			fmt.Fprintf(o.Out, "\n› login    mgmt's tailnet node %q needs a Tailscale login. Open this URL and sign in as a tailnet admin (a tagOwner of %s):\n\n    %s\n\n  waiting for the login (up to %v) …\n", o.Hostname, strings.Join(o.Tags, ", "), printedURL, o.Timeout)
		}
		if n.State != nil && *n.State == ipn.Running {
			break
		}
		// Logged in with a netmap is all tw up needs (name, tailnet, tags,
		// IP); Running would also wait for netcheck and DERP (#245).
		if n.State != nil || n.SelfChange != nil {
			if _, ok := mgmtd.LoggedIn(wctx, lc); ok {
				break
			}
		}
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	if st.Self == nil {
		return nil, fail("bootstrap_failed", "joined, but no node status")
	}
	res.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
	res.NodeID = string(st.Self.ID)
	if st.CurrentTailnet != nil {
		res.Tailnet = st.CurrentTailnet.MagicDNSSuffix
	}
	if res.Tailnet == "" {
		if _, suf, ok := strings.Cut(res.DNSName, "."); ok {
			res.Tailnet = suf
		}
	}
	if len(st.TailscaleIPs) > 0 {
		res.IP = st.TailscaleIPs[0].String()
	}
	if st.Self.Tags != nil {
		res.Tags = st.Self.Tags.AsSlice()
	}
	res.HTTPS = len(st.CertDomains) > 0
	// Every Capabilities entry is also a CapMap key (ipnstate.PeerStatus docs),
	// so CapMap alone is enough.
	_, res.Funnel = st.Self.CapMap[nodecap.Funnel]
	return res, nil
}
