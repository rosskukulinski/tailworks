package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd"
	"tailscale.com/tsnet"
)

// Link is <state>/link.json: a developer machine bound to a remote
// tw-mgmt (`tw link`). Every CLI command then talks to mgmt over this
// machine's own Tailscale connection; mgmt authenticates the caller by
// WhoIs (ADR-026), so there is no token or key on this machine.
type Link struct {
	Endpoint string `json:"endpoint"` // https://tw-mgmt.<tailnet>.ts.net (or http:// when the tailnet has no HTTPS)
	// Scheme is how the link reached mgmt ("https" or "http"); http is the
	// fallback when HTTPS certificates are unavailable (the traffic is
	// still WireGuard-encrypted and WhoIs-authenticated).
	Scheme   string `json:"scheme,omitempty"`
	Tailnet  string `json:"tailnet,omitempty"` // mgmt's tailnet (MagicDNS suffix)
	MgmtNode string `json:"mgmtNode,omitempty"`
	Login    string `json:"login,omitempty"` // who mgmt said we are at link time
	LinkedAt string `json:"linkedAt"`
	// Tsnet, if set, makes the CLI itself a tsnet node for the connection
	// (tests and machines without Tailscale): state in Dir, joined with
	// the key in AuthKeyFile (TS_AUTHKEY=…, only needed for the first
	// join). The product path is nil: the system Tailscale carries the
	// connection and its identity.
	Tsnet *LinkTsnet `json:"tsnet,omitempty"`
}

// LinkTsnet is the CLI's own tsnet node (test transport).
type LinkTsnet struct {
	Dir         string `json:"dir"`
	Hostname    string `json:"hostname"`
	ControlURL  string `json:"controlURL"`
	AuthKeyFile string `json:"authKeyFile,omitempty"`
	// Tags the node advertises (e.g. tag:ttest for the e2e admin client).
	Tags []string `json:"tags,omitempty"`
	// Ephemeral: the node was joined interactively by `tw link --tsnet`;
	// it registers as ephemeral so it disappears when the state is removed.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

func loadLink(path string) (*Link, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Link
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Endpoint == "" {
		return nil, fmt.Errorf("%s: no endpoint", path)
	}
	return &l, nil
}

func (l *Link) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(l, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// client is the remote mgmt client. HTTPS with the system roots: mgmt's
// certificate is the node's own (Let's Encrypt via Tailscale HTTPS).
func (l *Link) client() *mgmtapi.Client {
	c, err := mgmtapi.NewClient(l.Endpoint)
	if err != nil {
		c = &mgmtapi.Client{Base: l.Endpoint}
	}
	c.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DialContext: l.dial, MaxIdleConnsPerHost: 4}}
	return c
}

// dial connects through the CLI's tsnet node when configured, else the
// machine's network (the system Tailscale routes *.ts.net names).
func (l *Link) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if l.Tsnet == nil {
		return (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, network, addr)
	}
	ts, err := l.tsnetNode(ctx)
	if err != nil {
		return nil, err
	}
	// A MagicDNS name resolves only once the peer is in this node's
	// netmap, which can lag the first seconds after a fresh start: wait
	// for it instead of falling back to the system resolver.
	if host, port, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host) == nil {
		ip, err := l.peerIP(ctx, ts, host)
		if err != nil {
			return nil, err
		}
		addr = net.JoinHostPort(ip, port)
	}
	// A just-started peer (a fresh mgmt node) is in the netmap before it
	// has a path; a first packet then is dropped and WireGuard retries only
	// after 5 s. Wait for the path (bounded), then dial regardless.
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		if lc, err := ts.LocalClient(); err == nil {
			wctx, cancel := context.WithTimeout(ctx, netd.PeerPathWait)
			netd.WaitPeerPath(wctx, lc, ap.Addr())
			cancel()
		}
	}
	return ts.Dial(ctx, network, addr)
}

// peerIP finds host (a MagicDNS name) among the CLI node's peers.
func (l *Link) peerIP(ctx context.Context, ts *tsnet.Server, host string) (string, error) {
	lc, err := ts.LocalClient()
	if err != nil {
		return "", err
	}
	want := strings.TrimSuffix(strings.ToLower(host), ".") + "."
	deadline := time.Now().Add(45 * time.Second)
	for {
		if st, err := lc.Status(ctx); err == nil {
			for _, p := range st.Peer {
				if strings.ToLower(p.DNSName) == want && len(p.TailscaleIPs) > 0 {
					return p.TailscaleIPs[0].String(), nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s is not a peer of the CLI's tsnet node (no grant to it, or not in the tailnet)", host)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

var linkTsnetNode *tsnet.Server

// tsnetNode starts (once per process) the CLI's own tsnet node.
func (l *Link) tsnetNode(ctx context.Context) (*tsnet.Server, error) {
	if linkTsnetNode != nil {
		return linkTsnetNode, nil
	}
	t := l.Tsnet
	ts := &tsnet.Server{Hostname: t.Hostname, Dir: t.Dir, ControlURL: t.ControlURL, AdvertiseTags: t.Tags, Ephemeral: t.Ephemeral,
		Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	if t.AuthKeyFile != "" {
		if k, err := tsapi.LoadEnvFile(t.AuthKeyFile, "TS_AUTHKEY"); err == nil {
			ts.AuthKey = k
		}
	}
	uctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := ts.Up(uctx); err != nil {
		ts.Close()
		return nil, fmt.Errorf("link tsnet node: %w", err)
	}
	linkTsnetNode = ts
	return ts, nil
}

// whoami asks the linked mgmt who we are; a non-admin gets mgmt's
// not_admin error with the grant to add.
func (s *state) whoami(ctx context.Context) (*mgmt.WhoAmI, error) {
	var w mgmt.WhoAmI
	if err := s.mgmt().Do(ctx, "GET", "/v1/whoami", nil, &w); err != nil {
		return nil, linkErr(s.Link, err)
	}
	return &w, nil
}

// linkErr turns a transport or auth error against a linked mgmt into a
// typed CLI error with the right fix.
func linkErr(l *Link, err error) error {
	if me, ok := mgmtapi.AsError(err); ok {
		e := asCLIErr(me)
		if me.Code == "not_admin" {
			e.Lines = append(e.Lines, "mgmt identified this connection by WhoIs (your Tailscale login or node) and found no "+mgmt.AdminCap+" capability.")
			e.Fix = append([]string{"ask the tailnet admin to add this grant to the tailnet policy (autogroup:admin members already have it from tw up's base snippet):", "  " + me.Snippet,
				"then re-run; or log in to the host and use tw there"}, e.Fix...)
		}
		if me.Code == "no_identity" {
			e.Fix = append(e.Fix, "mgmt could not WhoIs this connection: is this machine on the same tailnet as "+l.Endpoint+"?")
		}
		return e
	}
	var ne net.Error
	if errors.As(err, &ne) || strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "connection refused") {
		ep := "<endpoint>"
		if l != nil {
			ep = l.Endpoint
		}
		return fail("mgmt_unreachable", "can't reach the linked tw-mgmt at "+ep, err.Error()).
			withFix("is Tailscale connected on this machine, and on the tailnet that runs tw?", "`tailscale status` should list the mgmt node; a grant from you (or autogroup:admin) to tag:tw-mgmt tcp:443 is needed",
				"tw link <tw-mgmt.<tailnet>.ts.net> to re-link")
	}
	return err
}

type linkResult struct {
	Endpoint string      `json:"endpoint"`
	Scheme   string      `json:"scheme,omitempty"`
	Tailnet  string      `json:"tailnet,omitempty"`
	MgmtNode string      `json:"mgmtNode,omitempty"`
	Login    string      `json:"login"`
	Actor    string      `json:"actor"`
	NodeID   string      `json:"nodeID,omitempty"` // our node, as mgmt saw it (WhoIs)
	Admin    bool        `json:"admin"`
	Tenants  []TenantRef `json:"tenants"`
	Path     string      `json:"path"`
	Unlinked bool        `json:"unlinked,omitempty"`

	// Console is the admin console URL (console.go); omitted under --tsnet.
	Console string `json:"console,omitempty"`
}

// cmdLink binds this machine to a remote mgmt: `tw link
// tw-mgmt.<tailnet>.ts.net` (or no argument: find `tw-mgmt` through
// MagicDNS). It asks mgmt who we are over our own Tailscale connection,
// refuses with the grant snippet if we are not an admin, and stores only
// the endpoint in <state>/link.json. No key, no token.
func cmdLink(args []string) error {
	f := newFlags("link")
	unlink := f.Bool("unlink", false, "forget the linked mgmt")
	tsnetF := f.Bool("tsnet", false, "this machine has no Tailscale: the CLI runs its own ephemeral tsnet node (state under <state>/cli, interactive login the first time; your login is the identity mgmt sees)")
	tsDir := f.String("tsnet-dir", "", "--tsnet: node state dir (default <state>/cli); tests pass one explicitly")
	tsHost := f.String("tsnet-hostname", "", "--tsnet: the CLI node's hostname (default tw-cli-<user>)")
	tsCtl := f.String("control-url", prodControl, "--tsnet: control URL for the CLI node")
	tsKey := f.String("tsnet-authkey-file", "", "--tsnet: TS_AUTHKEY=… file for an unattended first join (automation, CI); default: interactive login")
	tsTags := f.String("tsnet-tags", "", "--tsnet: comma-separated tags the CLI node advertises (an unattended join with a tagged key)")
	loginTO := f.Duration("login-timeout", 5*time.Minute, "--tsnet: how long to wait for the browser login")
	// --help lists these under Advanced (helpflags.go).
	f.group(advancedFlags,
		"tsnet-authkey-file",
		"tsnet-tags",
	)
	f.hidden("for the test harness", "tsnet-dir")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if *unlink {
		had := s.Link != nil
		os.Remove(s.linkPath())
		emit(f, linkResult{Unlinked: true, Path: s.linkPath(), Tenants: []TenantRef{}}, func() {
			if had {
				fmt.Println("✓ unlinked  removed " + s.linkPath())
			} else {
				fmt.Println("nothing to unlink in " + s.Dir)
			}
		})
		return nil
	}
	if s.configured() {
		return fail("state_conflict", s.Dir+" runs tw itself (tw up); commands already work here without a link").
			withFix("use another --state-dir for a link from this machine")
	}
	if len(pos) > 1 {
		return fail("bad_request", "tw link takes one endpoint", "usage: tw "+usageOf("link"))
	}
	ep := ""
	if len(pos) == 1 {
		ep = pos[0]
	}
	l := &Link{LinkedAt: time.Now().UTC().Format(time.RFC3339)}
	if *tsnetF || *tsDir != "" {
		dir := *tsDir
		if dir == "" {
			dir = filepath.Join(s.Dir, "cli")
		}
		hn := *tsHost
		if hn == "" {
			hn = "tw-cli-" + strings.ToLower(actor())
			hn = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.Split(hn, "@")[0], "-")
		}
		l.Tsnet = &LinkTsnet{Dir: dir, Hostname: hn, ControlURL: *tsCtl, AuthKeyFile: *tsKey, Ephemeral: *tsKey == "" && *tsTags == ""}
		if *tsTags != "" {
			l.Tsnet.Tags = strings.Split(*tsTags, ",")
		}
		if *tsKey == "" && !isFile(filepath.Join(dir, "tailscaled.state")) {
			// Interactive login in this terminal: the URL is printed, the
			// node state is kept under dir so later commands need no login.
			out := io.Writer(os.Stdout)
			if *f.json {
				out = os.Stderr
			}
			lctx, lcancel := context.WithTimeout(context.Background(), *loginTO)
			n, err := joinMgmtNode(lctx, joinOpts{Dir: dir, Hostname: hn, ControlURL: *tsCtl, Tags: l.Tsnet.Tags, Timeout: *loginTO, Out: out})
			lcancel()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ node     %s joined as you (%s); the CLI dials mgmt through it\n", n.DNSName, strings.Join(n.Tags, ","))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if ep == "" {
		// Auto-discover: the plain-HTTP listener on `tw-mgmt` (MagicDNS
		// search domain) tells us its full name and whether it has HTTPS.
		name := "tw-mgmt"
		if n := os.Getenv("TW_TEST_LINK_NAME"); n != "" {
			name = n // hermetic tests: never resolve the real tw-mgmt on this machine's own tailnet
		}
		l.Endpoint = "http://" + name
		s.Link = l
		w, err := s.whoami(ctx)
		if err != nil {
			return fail("mgmt_not_found", "no tw-mgmt found through MagicDNS").withFix("tw link tw-mgmt.<tailnet>.ts.net", "tw link --help")
		}
		if w.Mgmt.DNSName == "" {
			return fail("mgmt_not_found", "tw-mgmt answered without a DNS name").withFix("tw link <its address>")
		}
		ep = w.Mgmt.DNSName
		if !w.Mgmt.HTTPS {
			ep = "http://" + ep
		}
	}
	ep = strings.TrimSuffix(ep, "/")
	explicit := strings.Contains(ep, "://")
	if !explicit {
		ep = "https://" + ep
	}
	l.Endpoint = ep
	s.Link = l
	w, err := s.whoami(ctx)
	if err != nil && !explicit && !isMgmtAPIErr(err) {
		// No scheme given: HTTPS first, then plain HTTP on :80 (mgmt
		// serves both; :443 only when the tailnet issues certificates).
		l.Endpoint = "http://" + strings.TrimPrefix(ep, "https://")
		if w2, err2 := s.whoami(ctx); err2 == nil {
			ep, w, err = l.Endpoint, w2, nil
		} else {
			l.Endpoint = ep
		}
	}
	if err != nil {
		return err
	}
	l.Scheme = strings.SplitN(ep, "://", 2)[0]
	if !w.Admin && len(w.TenantGrants) == 0 {
		// A tenant admin (mgmt.TenantAdminCap) links too: mgmt limits it
		// to the tenants its grants name.
		c := fail("not_admin", w.Actor+" is not a tailworks admin at "+ep).withFix("ask the tailnet admin for the "+mgmt.AdminCap+" grant to "+mgmt.MgmtTag,
			"or, to manage only your team's tenants, a "+mgmt.TenantAdminCap+" grant naming them")
		for _, bad := range w.InvalidTenantGrants {
			c = c.withFix("your " + mgmt.TenantAdminCap + " value grants nothing: " + bad)
		}
		return c
	}
	l.Tailnet, l.MgmtNode, l.Login = w.Mgmt.Tailnet, w.Mgmt.DNSName, w.Actor
	if err := l.save(s.linkPath()); err != nil {
		return err
	}
	res := linkResult{Endpoint: ep, Scheme: l.Scheme, Tailnet: l.Tailnet, MgmtNode: l.MgmtNode, Login: w.Login, Actor: w.Actor, NodeID: w.NodeID, Admin: w.Admin, Tenants: s.tenants(), Path: s.linkPath()}
	res.Console = consoleURL(l, w.Admin, len(w.TenantGrants) > 0, false)
	emit(f, res, func() {
		role := "admin"
		if !w.Admin {
			role = "tenant admin"
		}
		fmt.Printf("✓ linked    %s · tailnet %s · you are %s (%s, by WhoIs: no key or token stored)\n", ep, l.Tailnet, w.Actor, role)
		for _, g := range w.TenantGrants {
			if !w.Admin {
				fmt.Printf("  may %s in %s\n", strings.Join(g.Actions, ", "), strings.Join(g.Tenants, ", "))
			}
		}
		if l.Scheme == "http" {
			fmt.Printf("  over plain HTTP on :80 (no HTTPS certificate on mgmt's node): WireGuard encrypts it and WhoIs authenticates it\n")
		}
		for _, t := range res.Tenants {
			fmt.Printf("✓ tenant    %s (%s, mint mode %s)\n", t.Tailnet, t.TenantID, mintModeName(t.MintMode))
		}
		if res.Console != "" {
			fmt.Printf("✓ console   %s (admin UI in your browser)\n", res.Console)
		}
		fmt.Printf("  saved %s · `tw init hello --template agent && cd hello && tw deploy` · `tw ls` · `tw audit` · `tw link --unlink`\n", s.linkPath())
	})
	return nil
}

func mintModeName(m string) string {
	switch m {
	case "":
		return "apikey (removed: switch to oauth)"
	case "apikey", "wif":
		return m + " (removed: switch to oauth)"
	}
	return m
}

// isMgmtAPIErr reports an error that mgmt itself answered (a typed API
// error, e.g. not_admin): the transport worked, so no scheme fallback.
func isMgmtAPIErr(err error) bool {
	var ce *cliErr
	if errors.As(err, &ce) {
		return ce.Code != "mgmt_unreachable" && ce.Code != "internal"
	}
	_, ok := mgmtapi.AsError(err)
	return ok
}
