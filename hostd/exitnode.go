package hostd

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"golang.org/x/net/dns/dnsmessage"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

// Internet egress through a tailnet exit node (ADR-054).
//
// With -worker-internet=exit-node=<node>, every internet flow from every
// workload on this host (Workers through netd/internet.go, containers
// through netd/sbxnet) is dialled by the host's own tsnet node, which has
// <node> selected as its exit node (ipn.Prefs.ExitNodeID). On the tailnet
// the flow is the host's (tag:tw-host); past the exit node it carries the
// exit node's address. The SSRF guard is unchanged: netd still resolves
// the name, checks every answer and dials the checked IP literal; the exit
// node forwards packets and never resolves anything.
//
// Names resolve through the exit node too, never through this host's
// resolver: by default over the exit node's peer API (DNS over HTTPS inside
// the tunnel, as tailscaled does for its own proxies), or, with
// dns=<ip[:port]>, as plain DNS over TCP to that resolver through the exit
// node.
//
// Fail closed. tsnet.Server.Dial of a public address falls back to the
// host's network when no exit node owns the default route, so Dial first
// checks (against a status snapshot at most a second old) that the exit
// node is in the netmap, offered, selected and online, and refuses with
// exit_node_unavailable otherwise. It never falls back to the host's
// network.

// ExitNodeSpec is the parsed value of exit-node=<spec>: the node, and an
// optional resolver.
type ExitNodeSpec struct {
	// Node is a MagicDNS name (short or full), a host name, a tailnet IP
	// or a stable node ID, matched against the host node's netmap.
	Node string
	// DNS is dns=<ip[:port]>: a resolver to send plain DNS over TCP to,
	// through the exit node (port 53 by default). Zero: the exit node's
	// peer API answers DNS.
	DNS netip.AddrPort
}

// ParseExitNodeSpec parses "<node>[,dns=<ip[:port]>]".
func ParseExitNodeSpec(s string) (ExitNodeSpec, error) {
	var sp ExitNodeSpec
	for i, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		switch {
		case i == 0:
			if part == "" {
				return sp, errors.New("exit-node=<node>: the node is missing")
			}
			sp.Node = part
		case strings.HasPrefix(part, "dns="):
			v := strings.TrimPrefix(part, "dns=")
			ap, err := netip.ParseAddrPort(v)
			if err != nil {
				a, err2 := netip.ParseAddr(strings.Trim(v, "[]"))
				if err2 != nil {
					return sp, fmt.Errorf("exit-node dns=%q: want an IP or IP:port", v)
				}
				ap = netip.AddrPortFrom(a, 53)
			}
			sp.DNS = ap
		default:
			return sp, fmt.Errorf("exit-node option %q: want dns=<ip[:port]>", part)
		}
	}
	return sp, nil
}

// String is the canonical spec: "<node>" or "<node>,dns=<ip:port>".
func (s ExitNodeSpec) String() string {
	if s.DNS.IsValid() {
		return s.Node + ",dns=" + s.DNS.String()
	}
	return s.Node
}

// exitNodeState is what the host node's status says about the exit node.
type exitNodeState struct {
	ID       tailcfg.StableNodeID
	Name     string
	Online   bool
	Offered  bool   // ExitNodeOption: advertises exit routes, approved, and this host may use it
	Selected bool   // ExitNode: this host's node has it as its exit node
	PeerAPI  string // the exit node's peer API base URL ("" when it has none)
	Problem  string // "" when the path is usable
}

// ExitNodeInternet is the host's internet path through an exit node.
type ExitNodeInternet struct {
	spec   ExitNodeSpec
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	status func(ctx context.Context) (*ipnstate.Status, error)
	edit   func(ctx context.Context, mp *ipn.MaskedPrefs) error
	hc     *http.Client
	log    *slog.Logger

	mu      sync.Mutex
	st      exitNodeState
	checked time.Time

	closeOnce sync.Once
	closed    chan struct{}
}

// exitNodeStatusTTL bounds how old the status snapshot Dial and Resolve
// act on may be; exitNodePoll is how often the background check runs.
const (
	exitNodeStatusTTL = time.Second
	exitNodePoll      = 2 * time.Second
	exitNodeDialWait  = 10 * time.Second
)

// NewExitNodeInternet selects spec's exit node on srv, this host's own
// node, and returns the internet path through it. A node that can't be
// used right now is not an error: the path starts refusing with
// exit_node_unavailable (Problem says why), and keeps checking.
func NewExitNodeInternet(ctx context.Context, srv *tsnet.Server, spec ExitNodeSpec, log *slog.Logger) (*ExitNodeInternet, error) {
	lc, err := srv.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("exit node: host node local client: %w", err)
	}
	x := newExitNodeInternet(spec, srv.Dial, lc.Status, func(ctx context.Context, mp *ipn.MaskedPrefs) error {
		_, err := lc.EditPrefs(ctx, mp)
		return err
	}, log)
	st := x.refresh(ctx)
	if st.Problem != "" {
		log.Warn("exit node unavailable; internet egress is refused until it is", "exit_node", spec.Node, "problem", st.Problem)
	} else {
		log.Info("internet egress through exit node", "exit_node", st.Name, "id", st.ID, "dns", x.dnsVia())
	}
	go x.poll()
	return x, nil
}

func newExitNodeInternet(spec ExitNodeSpec, dial func(context.Context, string, string) (net.Conn, error),
	status func(context.Context) (*ipnstate.Status, error), edit func(context.Context, *ipn.MaskedPrefs) error, log *slog.Logger) *ExitNodeInternet {
	if log == nil {
		log = slog.Default()
	}
	x := &ExitNodeInternet{spec: spec, dial: dial, status: status, edit: edit, log: log, closed: make(chan struct{})}
	x.hc = &http.Client{Transport: &http.Transport{DialContext: dial, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}}
	return x
}

// ExitNode reports whether c is an exit-node path (nil: no).
func (c *ContainerInternet) ExitNode() bool {
	return c != nil && strings.HasPrefix(c.Mode, "exit-node")
}

// Internet is the path as hostd.Config.WorkerInternet / ContainerInternet.
func (x *ExitNodeInternet) Internet() *ContainerInternet {
	return &ContainerInternet{Dial: x.Dial, Resolve: x.Resolve, Mode: "exit-node=" + x.spec.String(), Via: "exit node " + x.spec.Node, Problem: x.Problem}
}

// Problem is why the path is unusable right now ("" when it is), from the
// last check.
func (x *ExitNodeInternet) Problem() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.st.Problem
}

// Close stops the background check.
func (x *ExitNodeInternet) Close() { x.closeOnce.Do(func() { close(x.closed) }) }

func (x *ExitNodeInternet) poll() {
	t := time.NewTicker(exitNodePoll)
	defer t.Stop()
	for {
		select {
		case <-x.closed:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			x.refresh(ctx)
			cancel()
		}
	}
}

// current is the state, refreshed when the snapshot is older than
// exitNodeStatusTTL.
func (x *ExitNodeInternet) current(ctx context.Context) exitNodeState {
	x.mu.Lock()
	st, fresh := x.st, time.Since(x.checked) < exitNodeStatusTTL
	x.mu.Unlock()
	if fresh {
		return st
	}
	return x.refresh(ctx)
}

func (x *ExitNodeInternet) refresh(ctx context.Context) exitNodeState {
	st := x.compute(ctx)
	x.mu.Lock()
	prev := x.st
	x.st, x.checked = st, time.Now()
	x.mu.Unlock()
	switch {
	case st.Problem != "" && st.Problem != prev.Problem:
		x.log.Warn("exit node unavailable", "exit_node", x.spec.Node, "problem", st.Problem)
	case st.Problem == "" && prev.Problem != "":
		x.log.Info("exit node available", "exit_node", st.Name, "id", st.ID)
	}
	return st
}

// compute reads the host node's status and, when the exit node is offered
// but not selected, selects it.
func (x *ExitNodeInternet) compute(ctx context.Context) exitNodeState {
	s := exitNodeState{}
	p, err := x.peer(ctx)
	if err != nil {
		s.Problem = err.Error()
		return s
	}
	s.ID, s.Name, s.Online, s.Offered, s.Selected = p.ID, peerName(p), p.Online, p.ExitNodeOption, p.ExitNode
	for _, u := range p.PeerAPIURL {
		if s.PeerAPI == "" || !strings.Contains(u, "[") { // prefer the IPv4 URL
			s.PeerAPI = u
		}
	}
	if !s.Offered {
		s.Problem = s.Name + " is not offered to this host as an exit node: it must advertise exit routes (approved by the tailnet admin), and the policy must grant tag:tw-host autogroup:internet"
		return s
	}
	if !s.Selected {
		if err := x.edit(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{ExitNodeID: p.ID}, ExitNodeIDSet: true}); err != nil {
			s.Problem = "selecting exit node " + s.Name + ": " + err.Error()
			return s
		}
		// Confirm from a fresh status rather than assume: Dial relies on it.
		if q, err := x.peer(ctx); err == nil {
			s.Selected, s.Online = q.ExitNode, q.Online
		}
		if !s.Selected {
			s.Problem = "this host's node did not select " + s.Name + " as its exit node"
			return s
		}
	}
	if !s.Online {
		s.Problem = s.Name + " is offline"
		return s
	}
	return s
}

// peer finds the configured exit node in the host node's status.
func (x *ExitNodeInternet) peer(ctx context.Context) (*ipnstate.PeerStatus, error) {
	st, err := x.status(ctx)
	if err != nil {
		return nil, fmt.Errorf("this host's node: %w", err)
	}
	for _, p := range st.Peer {
		if matchExitNode(p, x.spec.Node) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("exit node %q is not in this host's netmap (is it on the tailnet, and does the policy let tag:tw-host reach it?)", x.spec.Node)
}

// matchExitNode: node is p's stable ID, MagicDNS name (short or full),
// host name or a tailnet IP.
func matchExitNode(p *ipnstate.PeerStatus, node string) bool {
	if p == nil || node == "" {
		return false
	}
	if string(p.ID) == node {
		return true
	}
	dns := strings.ToLower(strings.TrimSuffix(p.DNSName, "."))
	n := strings.ToLower(node)
	if dns != "" && (dns == n || strings.HasPrefix(dns, n+".")) {
		return true
	}
	if strings.EqualFold(p.HostName, node) {
		return true
	}
	for _, ip := range p.TailscaleIPs {
		if ip.String() == node {
			return true
		}
	}
	return false
}

func peerName(p *ipnstate.PeerStatus) string {
	if n := strings.TrimSuffix(p.DNSName, "."); n != "" {
		return n
	}
	if p.HostName != "" {
		return p.HostName
	}
	return string(p.ID)
}

func (x *ExitNodeInternet) dnsVia() string {
	if x.spec.DNS.IsValid() {
		return "tcp " + x.spec.DNS.String()
	}
	return "exit node peer API"
}

func (x *ExitNodeInternet) unavailable(detail string) *netd.Error {
	return &netd.Error{Code: netd.CodeExitNodeUnavailable, Detail: detail, DocURL: netd.DocBase + netd.CodeExitNodeUnavailable}
}

// Dial opens a TCP flow to addr (an IP:port the guard has passed) through
// the exit node, via this host's own node. It refuses, rather than falls
// back to the host's network, when the exit node isn't usable.
func (x *ExitNodeInternet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	s := x.current(ctx)
	if s.Problem != "" {
		return nil, x.unavailable(s.Problem)
	}
	dctx, cancel := context.WithTimeout(ctx, exitNodeDialWait)
	defer cancel()
	c, err := x.dial(dctx, network, addr)
	if err != nil {
		return nil, x.unavailable(fmt.Sprintf("dialing %s through exit node %s: %v", addr, s.Name, err))
	}
	return c, nil
}

// Resolve looks host up through the exit node (A and AAAA). The host's
// own resolver is never used.
func (x *ExitNodeInternet) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	s := x.current(ctx)
	if s.Problem != "" {
		return nil, x.unavailable(s.Problem)
	}
	ctx, cancel := context.WithTimeout(ctx, exitNodeDialWait)
	defer cancel()
	var addrs []netip.Addr
	for _, qt := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		q, err := dnsQuery(host, qt)
		if err != nil {
			return nil, err
		}
		var resp []byte
		if x.spec.DNS.IsValid() {
			resp, err = x.queryTCP(ctx, s, q)
		} else {
			resp, err = x.queryDoH(ctx, s, q)
		}
		if err != nil {
			return nil, err
		}
		as, err := dnsAnswers(resp)
		if err != nil {
			return nil, x.unavailable(fmt.Sprintf("resolving %s through exit node %s: %v", host, s.Name, err))
		}
		addrs = append(addrs, as...)
	}
	return addrs, nil
}

// queryDoH sends q to the exit node's peer API (RFC 8484 POST).
func (x *ExitNodeInternet) queryDoH(ctx context.Context, s exitNodeState, q []byte) ([]byte, error) {
	if s.PeerAPI == "" {
		return nil, x.unavailable("exit node " + s.Name + " has no peer API, so it can't answer DNS; add dns=<resolver> to the exit-node setting")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.PeerAPI+"/dns-query", strings.NewReader(string(q)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	res, err := x.hc.Do(req)
	if err != nil {
		return nil, x.unavailable(fmt.Sprintf("DNS through exit node %s: %v", s.Name, err))
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return nil, x.unavailable(fmt.Sprintf("DNS through exit node %s: %v", s.Name, err))
	}
	if res.StatusCode != http.StatusOK {
		return nil, x.unavailable(fmt.Sprintf("exit node %s refuses DNS for this host's node (HTTP %d: %s); add dns=<resolver> to the exit-node setting to use a resolver through it instead",
			s.Name, res.StatusCode, strings.TrimSpace(string(body))))
	}
	return body, nil
}

// queryTCP sends q as DNS over TCP to spec.DNS through the exit node.
func (x *ExitNodeInternet) queryTCP(ctx context.Context, s exitNodeState, q []byte) ([]byte, error) {
	c, err := x.dial(ctx, "tcp", x.spec.DNS.String())
	if err != nil {
		return nil, x.unavailable(fmt.Sprintf("DNS to %s through exit node %s: %v", x.spec.DNS, s.Name, err))
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	msg := make([]byte, 2+len(q))
	binary.BigEndian.PutUint16(msg, uint16(len(q)))
	copy(msg[2:], q)
	if _, err := c.Write(msg); err != nil {
		return nil, x.unavailable(fmt.Sprintf("DNS to %s through exit node %s: %v", x.spec.DNS, s.Name, err))
	}
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		return nil, x.unavailable(fmt.Sprintf("DNS to %s through exit node %s: %v", x.spec.DNS, s.Name, err))
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, x.unavailable(fmt.Sprintf("DNS to %s through exit node %s: %v", x.spec.DNS, s.Name, err))
	}
	return resp, nil
}

// dnsQuery is one wire-format question for name.
func dnsQuery(name string, typ dnsmessage.Type) ([]byte, error) {
	n, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", name, err)
	}
	var id [2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(make([]byte, 0, 64), dnsmessage.Header{ID: binary.BigEndian.Uint16(id[:]), RecursionDesired: true})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: n, Type: typ, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// dnsAnswers is every A and AAAA in a wire-format response. NXDOMAIN is
// no addresses; other failures are errors.
func dnsAnswers(resp []byte) ([]netip.Addr, error) {
	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil {
		return nil, err
	}
	switch h.RCode {
	case dnsmessage.RCodeSuccess, dnsmessage.RCodeNameError:
	default:
		return nil, fmt.Errorf("DNS %v", h.RCode)
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, err
	}
	var out []netip.Addr
	for {
		rr, err := p.Answer()
		if err == dnsmessage.ErrSectionDone {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch r := rr.Body.(type) {
		case *dnsmessage.AResource:
			out = append(out, netip.AddrFrom4(r.A))
		case *dnsmessage.AAAAResource:
			out = append(out, netip.AddrFrom16(r.AAAA))
		}
	}
}
