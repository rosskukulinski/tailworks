// Package mgmtd runs twd-mgmt (the management plane) as a library: the
// standalone twd-mgmt binary is a flag parser over Start, and the
// single-process `twd up` embeds it next to the host agent (internal/
// hostdrun) in one process.
//
// Start opens the store and custody, joins (or re-opens) mgmt's own tsnet
// node when TSNetHostname is set, and serves the /v1 API on it behind
// WhoIs (TailnetHandler), optionally as a replica of a Tailscale Service
// (TSNetService), publishes the node's loopback proxy for the local twd
// CLI, and in WIF mode serves the OIDC issuer over Funnel on the issuer's
// OWN node (IssuerTSNetHostname; mgmt's node never serves Funnel). With
// LocalDev it also serves the unix socket guarded by the per-host token.
package mgmtd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/twd/internal/approvecap"
	"github.com/rosskukulinski/twd/internal/obs"
	"github.com/rosskukulinski/twd/internal/tsapi"
	"github.com/rosskukulinski/twd/internal/tssvc"
	"github.com/rosskukulinski/twd/mgmt"
	"github.com/rosskukulinski/twd/netd"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// Flag defaults twd-mgmt and the single-process twd up share (an Options
// zero value means "off" for these, so callers pass the default explicitly).
const (
	// DefaultWarmKeys is the warm-key pool cap (twd-mgmt -warm-keys).
	DefaultWarmKeys = 5
	// DefaultReapInterval is the reaper interval (twd-mgmt -reap-interval).
	DefaultReapInterval = 10 * time.Second
)

// Options mirrors twd-mgmt's flags, one field per flag (see cmd/twd-mgmt).
type Options struct {
	// StateDir (0700) holds the db, custody, issuer key and the tsnet state.
	StateDir string
	// LocalDev also serves the API on Socket, guarded by the per-host token
	// in HostTokenFile (rotated at start with RotateHostToken).
	LocalDev        bool
	Socket          string
	HostTokenFile   string
	RotateHostToken bool
	// DB is the store DSN (SQLite path or postgres://…; default
	// <StateDir>/mgmt.db); DBFile reads it from a file instead.
	DB     string
	DBFile string
	// TSAPIURL is the Tailscale API base URL (tsapi.DefaultBaseURL).
	TSAPIURL string
	// WIF: IssuerURL is the public https issuer origin (default with
	// Funnel: https://<mgmt node>); IssuerListen also serves the issuer on
	// a local address (plain HTTP); Funnel serves it publicly on the
	// issuer's own tsnet node IssuerTSNetHostname (tag mgmt.IssuerTag,
	// state in IssuerTSNetDir, default <StateDir>/issuer-tsnet; first join
	// with TS_AUTHKEY=… from IssuerTSNetAuthKeyFile, else the state must be
	// logged in already). mgmt's own node never uses Funnel.
	IssuerURL              string
	IssuerListen           string
	Funnel                 bool
	IssuerTSNetHostname    string
	IssuerTSNetDir         string
	IssuerTSNetAuthKeyFile string
	// TSNetHostname names mgmt's own tailnet node (required unless
	// LocalDev); TSNetControlURL is required with it. TSNetAuthKeyFile
	// holds TS_AUTHKEY=… for the first join (else the node state must be
	// logged in already: twd up does the interactive login). TSNetTags are
	// the tags the node advertises (nil = mgmt.MgmtTag; empty = none).
	// TSNetService also hosts a Tailscale Service (svc:…) on :80,
	// advertised only while this replica's store answers. TSNetTLS also
	// serves :443 with the node's certificate when the tailnet has HTTPS.
	TSNetHostname    string
	TSNetControlURL  string
	TSNetAuthKeyFile string
	TSNetTags        []string
	TSNetService     string
	TSNetTLS         bool
	// NoLoopback skips publishing <StateDir>/loopback.json for the local CLI.
	NoLoopback bool
	// Version is reported by GET /v1/status.
	Version string
	// Admins are tailnet logins admitted as admins without the cap.
	Admins []string
	// Custody is "file" (default) or "setec" (with SetecURL).
	Custody  string
	SetecURL string
	// ReapInterval (0 = DefaultReapInterval) and DeviceDeleteDelay
	// (mgmt.Config).
	ReapInterval      time.Duration
	DeviceDeleteDelay time.Duration
	// WarmKeys caps the warm-key pool (0 = off; the flag default is
	// DefaultWarmKeys); WarmKeyTTL is a pooled key's expiry (0 =
	// mgmt.DefaultWarmKeyTTL).
	WarmKeys   int
	WarmKeyTTL time.Duration
	// RegistryDir is the OCI registry blob store ("" = <StateDir>/registry,
	// "off" = no registry); RegistryInsecure lists upstream registry hosts
	// mirrored over plain HTTP; NoMirror refuses external image refs at
	// deploy; ImageAliasesFile is a JSON file [{from, to, arch}] replacing
	// the default image alias map.
	RegistryDir      string
	RegistryInsecure []string
	NoMirror         bool
	ImageAliasesFile string
	// Log (default: discard).
	Log *slog.Logger

	// MetricsPort serves GET /metrics on mgmt's own tailnet node
	// (ADR-observability; 0 = off; flag default obs.DefaultMetricsPort).
	MetricsPort int
}

// Runtime is a running mgmt.
type Runtime struct {
	// M is the service, Server the /v1 API over it, TS mgmt's own tsnet
	// node (nil with LocalDev only), Self its identity.
	M      *mgmt.Mgmt
	Server *mgmt.Server
	TS     *tsnet.Server
	Self   mgmt.SelfInfo
	// Issuer is the WIF OIDC issuer (nil unless an issuer URL is set);
	// IssuerTS its own Funnel node (nil unless Funnel).
	Issuer   *mgmt.Issuer
	IssuerTS *tsnet.Server

	log      *slog.Logger
	store    *mgmt.SQLStore
	handler  http.Handler
	errc     chan error
	servers  []*http.Server
	gate     *mgmt.ServiceGate
	cleanups []func()

	// metrics.go: registries served with mgmt's on /metrics.
	metricsMu    sync.Mutex
	extraMetrics obs.Gatherers
}

// Start runs mgmt per o (twd-mgmt's run body up to its wait). Cancel
// ctx-independent work with Close; a failed listener is reported on Errs.
func Start(ctx context.Context, o Options) (rt *Runtime, err error) {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if o.StateDir == "" {
		return nil, errors.New("mgmtd: StateDir is required")
	}
	if o.Custody == "" {
		o.Custody = "file"
	}
	if o.ReapInterval == 0 {
		o.ReapInterval = DefaultReapInterval
	}
	if o.TSNetTags == nil {
		o.TSNetTags = []string{mgmt.MgmtTag}
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return nil, err
	}
	os.Chmod(o.StateDir, 0o700)
	rt = &Runtime{log: log, errc: make(chan error, 4)}
	defer func() {
		if err != nil {
			rt.Close()
		}
	}()

	dsn := o.DB
	if o.DBFile != "" {
		b, err := os.ReadFile(o.DBFile)
		if err != nil {
			return nil, err
		}
		dsn = strings.TrimSpace(string(b))
	}
	if dsn == "" {
		dsn = filepath.Join(o.StateDir, "mgmt.db")
	}
	store, err := mgmt.OpenStore(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	rt.store = store

	var custody mgmt.Custody
	switch o.Custody {
	case "file":
		if custody, err = mgmt.OpenFileCustody(filepath.Join(o.StateDir, "custody")); err != nil {
			return nil, err
		}
	case "setec":
		if o.SetecURL == "" {
			return nil, errors.New("-custody setec needs -setec-url")
		}
		custody = &mgmt.SetecCustody{BaseURL: o.SetecURL}
	default:
		return nil, fmt.Errorf("unknown -custody %q", o.Custody)
	}
	replicaID, err := mgmt.LoadOrCreateReplicaID(filepath.Join(o.StateDir, "replica.id"))
	if err != nil {
		return nil, err
	}
	// Refuse a custody mismatch with other replicas before touching any
	// shared secret (F6 check, run early: F12).
	if err := mgmt.PreflightReplica(ctx, store, custody, replicaID); err != nil {
		return nil, err
	}
	// Shared by every replica via the store and custody (mm-loop F12).
	secret, err := mgmt.LoadSharedIDSecret(ctx, filepath.Join(o.StateDir, "id.secret"), store, custody)
	if err != nil {
		return nil, err
	}

	// Operator-tailnet node (optional).
	var ts *tsnet.Server
	self := mgmt.SelfInfo{}
	issuerURL := o.IssuerURL
	if o.TSNetService != "" {
		if o.TSNetHostname == "" {
			return nil, errors.New("-tsnet-service needs -tsnet-hostname")
		}
		if err := tssvc.Validate(o.TSNetService); err != nil {
			return nil, fmt.Errorf("-tsnet-service: %w", err)
		}
		if strings.EqualFold(tssvc.Label(o.TSNetService), o.TSNetHostname) {
			return nil, fmt.Errorf("-tsnet-service %s has the same MagicDNS name as this replica's node (-tsnet-hostname %s); MagicDNS answers the node first, so name each replica differently (e.g. %s-1)", o.TSNetService, o.TSNetHostname, o.TSNetHostname)
		}
	}
	if o.TSNetHostname != "" {
		if o.TSNetControlURL == "" {
			return nil, errors.New("-tsnet-hostname needs an explicit -tsnet-control-url")
		}
		ts = &tsnet.Server{Hostname: o.TSNetHostname, Dir: filepath.Join(o.StateDir, "tsnet"), ControlURL: o.TSNetControlURL,
			Logf: func(string, ...any) {}}
		if len(o.TSNetTags) > 0 {
			ts.AdvertiseTags = o.TSNetTags
		}
		if o.TSNetAuthKeyFile != "" {
			k, err := tsapi.LoadEnvFile(o.TSNetAuthKeyFile, "TS_AUTHKEY")
			if err != nil {
				return nil, err
			}
			ts.AuthKey = k
		}
		rt.TS = ts
		upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		st, err := tsnetUp(upCtx, ts, ts.AuthKey == "")
		cancel()
		if err != nil {
			return nil, fmt.Errorf("tsnet up: %w", err)
		}
		self.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		self.NodeID = string(st.Self.ID)
		if len(st.TailscaleIPs) > 0 {
			self.IP = st.TailscaleIPs[0].String()
		}
		if st.CurrentTailnet != nil {
			self.Tailnet = st.CurrentTailnet.MagicDNSSuffix
		}
		self.HTTPS = o.TSNetTLS && len(ts.CertDomains()) > 0
		if o.Funnel && issuerURL == "" {
			if doms := ts.CertDomains(); len(doms) > 0 {
				issuerURL = "https://" + doms[0]
			}
		}
		tags := []string{}
		if st.Self.Tags != nil {
			tags = st.Self.Tags.AsSlice()
		}
		log.Info("tailnet node", "name", self.DNSName, "tailnet", self.Tailnet, "tags", tags, "https", self.HTTPS, "ips", st.TailscaleIPs)
	}

	var issuer *mgmt.Issuer
	if issuerURL != "" {
		// Shared by every replica through the store and custody (mm-loop F11b).
		if issuer, err = mgmt.LoadSharedIssuer(ctx, issuerURL, filepath.Join(o.StateDir, "issuer.key"), store, custody); err != nil {
			return nil, err
		}
		log.Info("OIDC issuer", "base", issuer.BaseURL)
	}
	rt.Issuer = issuer

	mcfg := mgmt.Config{Store: store, Custody: custody, Broker: &mgmt.Broker{BaseURL: o.TSAPIURL, Issuer: issuer},
		IDSecret: secret, ReplicaID: replicaID, ReapInterval: o.ReapInterval, DeviceDeleteDelay: o.DeviceDeleteDelay, Log: log,
		OperatorTailnet: self.Tailnet, OperatorControlURL: o.TSNetControlURL, NoMirror: o.NoMirror, WarmKeys: o.WarmKeys, WarmKeyTTL: o.WarmKeyTTL,
		Version: o.Version, ReleaseDir: filepath.Join(o.StateDir, "releases")}
	switch o.RegistryDir {
	case "off":
	case "":
		mcfg.RegistryDir = filepath.Join(o.StateDir, "registry")
	default:
		mcfg.RegistryDir = o.RegistryDir
	}
	if len(o.RegistryInsecure) > 0 {
		mcfg.RegistryInsecure = o.RegistryInsecure
	}
	if o.ImageAliasesFile != "" {
		b, err := os.ReadFile(o.ImageAliasesFile)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &mcfg.ImageAliases); err != nil {
			return nil, fmt.Errorf("-image-aliases %s: %w", o.ImageAliasesFile, err)
		}
	}
	if ts != nil {
		mcfg.TailnetDial = ts.Dial // kill/assignment pushes to hosts' own nodes
	}
	m, err := mgmt.New(mcfg)
	if err != nil {
		return nil, err
	}
	if err := m.Start(); err != nil {
		return nil, err
	}
	rt.M = m
	if issuer != nil {
		self.Issuer = issuer.BaseURL
	}
	m.SetConsoleURL(self.ConsoleURL()) // notification deep links (mgmt/notify.go)
	api := &mgmt.Server{M: m, Self: self, Version: o.Version}
	if len(o.Admins) > 0 {
		api.Admins = o.Admins
	}
	rt.Server, rt.Self = api, self
	rt.handler = api.Handler()
	if ts == nil && !o.LocalDev {
		return nil, errors.New("no listeners: normal operation is tsnet only (-tsnet-hostname); -local-dev serves the unix socket for twd dev and tests")
	}
	serve := func(name string, ln net.Listener, h http.Handler) *http.Server {
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				rt.errc <- fmt.Errorf("%s: %w", name, err)
			}
		}()
		log.Info("serving", "listener", name, "addr", ln.Addr().String())
		rt.servers = append(rt.servers, srv)
		return srv
	}
	loopbackPath := filepath.Join(o.StateDir, "loopback.json")
	os.Remove(loopbackPath)
	if o.LocalDev && o.Socket != "" {
		// RT-03: same-uid peercred is not authorization. Every request on
		// the socket must carry the per-host token (minted here into a 0600
		// file under hostd's state dir; hostd and the CLI read it).
		token, err := mgmt.LoadOrCreateHostToken(o.HostTokenFile, o.RotateHostToken)
		if err != nil {
			return nil, fmt.Errorf("host token: %w", err)
		}
		ln, err := mgmt.ListenUnix(o.Socket)
		if err != nil {
			return nil, err
		}
		sock := o.Socket
		rt.cleanups = append(rt.cleanups, func() { os.Remove(sock) })
		log.Info("host token", "file", o.HostTokenFile, "rotated", o.RotateHostToken)
		api.Listeners = append(api.Listeners, "unix:"+o.Socket)
		serve("unix", ln, mgmt.LocalHandler(rt.handler, token))
	}
	if ts != nil {
		lc, err := ts.LocalClient()
		if err != nil {
			return nil, err
		}
		whois := func(ctx context.Context, addr string) (mgmt.WhoIsInfo, error) {
			wi, err := lc.WhoIs(ctx, addr)
			if err != nil {
				return mgmt.WhoIsInfo{}, err
			}
			info := mgmt.WhoIsInfo{NodeID: string(wi.Node.StableID), Name: wi.Node.Name, Tags: wi.Node.Tags}
			if wi.UserProfile != nil {
				info.Login = wi.UserProfile.LoginName
			}
			for c, vals := range wi.CapMap {
				info.Caps = append(info.Caps, string(c))
				if c == mgmt.MCPCap || c == approvecap.Cap { // approvals.go needs the grants' values too
					if info.CapValues == nil {
						info.CapValues = map[string][]json.RawMessage{}
					}
					for _, v := range vals {
						info.CapValues[string(c)] = append(info.CapValues[string(c)], json.RawMessage(v))
					}
				}
			}
			return info, nil
		}
		if o.MetricsPort > 0 {
			// Tailnet only: who may scrape is a grant to this port.
			mln, err := ts.Listen("tcp", fmt.Sprintf(":%d", o.MetricsPort))
			if err != nil {
				return nil, fmt.Errorf("metrics listener: %w", err)
			}
			serve("tsnet-metrics", mln, rt.MetricsHandler())
		}
		ln, err := ts.Listen("tcp", ":80")
		if err != nil {
			return nil, err
		}
		th := api.TailnetHandler(whois, rt.handler)
		api.Listeners = append(api.Listeners, "tsnet:80")
		serve("tsnet", ln, th)
		if o.TSNetService != "" {
			// The replica VIP (H12): flows to the Service reach the same
			// WhoIs-authenticated handler (tssvc delivers them in-process,
			// so WhoIs sees the real caller).
			sh := tssvc.NewHost(ts, lc)
			sl := tssvc.NewConnListener(tssvc.Addr{Name: o.TSNetService})
			sh.Route(80, sl.Deliver)
			api.Listeners = append(api.Listeners, "service:"+o.TSNetService)
			ssrv := serve("service", sl, th)
			ssrv.SetKeepAlivesEnabled(false) // until advertised
			rt.gate = &mgmt.ServiceGate{Adv: sh, Name: o.TSNetService, Ports: []uint16{80}, Log: log,
				Check:    func(ctx context.Context) error { _, err := m.Status(ctx); return err },
				OnChange: ssrv.SetKeepAlivesEnabled}
			go rt.gate.Run(ctx)
		}
		if !o.NoLoopback {
			// The operator console: the twd CLI on this machine dials mgmt's
			// own tailnet IP through the node's SOCKS5 loopback proxy, so
			// its requests carry mgmt's identity (WhoIs → operator). The
			// credential lives in a 0600 file next to the state; nothing
			// listens on a unix socket.
			addr, cred, _, err := ts.Loopback()
			if err != nil {
				return nil, fmt.Errorf("tsnet loopback: %w", err)
			}
			lb := mgmt.Loopback{Addr: addr, ProxyCred: cred, MgmtIP: self.IP, DNSName: self.DNSName, HTTPS: self.HTTPS, PID: os.Getpid()}
			b, _ := json.Marshal(lb)
			if err := os.WriteFile(loopbackPath+".tmp", b, 0o600); err != nil {
				return nil, err
			}
			if err := os.Rename(loopbackPath+".tmp", loopbackPath); err != nil {
				return nil, err
			}
			rt.cleanups = append(rt.cleanups, func() { os.Remove(loopbackPath) })
			api.Listeners = append(api.Listeners, "loopback:"+addr)
			log.Info("operator console", "loopback", addr, "file", loopbackPath)
		}
		if self.HTTPS {
			// The API over the node's own HTTPS certificate: what `twd
			// link` uses from a developer machine. tailnet-only (ListenTLS
			// never serves Funnel traffic).
			tln, err := ts.ListenTLS("tcp", ":443")
			if err != nil {
				return nil, fmt.Errorf("tsnet tls: %w", err)
			}
			api.Listeners = append(api.Listeners, "tsnet:443")
			serve("tsnet-tls", tln, th)
			// Fetch the node's certificate now, in the background: a new
			// node name's first certificate takes 30–100 s, and without
			// Funnel (oauth mode) nothing else would request it before the
			// first `twd link`, whose TLS handshake would time out.
			go prefetchCert(ts, self.DNSName, log)
		} else if o.TSNetTLS {
			log.Warn("HTTPS is not enabled on this tailnet: the API is served on :80 only; `twd link` uses plain HTTP (WireGuard-encrypted, WhoIs-authenticated)")
		}
	}
	if o.Funnel {
		// The WIF issuer is public, so it runs on its OWN node: mgmt's node
		// (the API, WhoIs admins, the signing key's process identity) is
		// never Funnel-capable and never listens on Funnel.
		if issuer == nil {
			return nil, errors.New("-funnel needs an issuer URL")
		}
		if o.IssuerTSNetHostname == "" || o.TSNetControlURL == "" {
			return nil, errors.New("-funnel needs -issuer-tsnet-hostname and -tsnet-control-url (the issuer runs on its own node, never on mgmt's)")
		}
		if o.IssuerTSNetHostname == o.TSNetHostname {
			return nil, errors.New("-issuer-tsnet-hostname must differ from -tsnet-hostname: mgmt's node never serves Funnel")
		}
		dir := o.IssuerTSNetDir
		if dir == "" {
			dir = filepath.Join(o.StateDir, "issuer-tsnet")
		}
		if filepath.Clean(dir) == filepath.Clean(filepath.Join(o.StateDir, "tsnet")) {
			return nil, errors.New("-issuer-tsnet-dir must not be mgmt's own node state")
		}
		its := &tsnet.Server{Hostname: o.IssuerTSNetHostname, Dir: dir, ControlURL: o.TSNetControlURL, AdvertiseTags: []string{mgmt.IssuerTag},
			Logf: func(string, ...any) {}}
		if o.IssuerTSNetAuthKeyFile != "" {
			k, err := tsapi.LoadEnvFile(o.IssuerTSNetAuthKeyFile, "TS_AUTHKEY")
			if err != nil {
				return nil, err
			}
			its.AuthKey = k
		}
		rt.IssuerTS = its
		rt.cleanups = append(rt.cleanups, func() { its.Close() })
		uctx, ucancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err := its.Up(uctx)
		ucancel()
		if err != nil {
			return nil, fmt.Errorf("issuer node %s: %w (twd up --wif joins it)", o.IssuerTSNetHostname, err)
		}
		fl, err := its.ListenFunnel("tcp", ":443", tsnet.FunnelOnly())
		if err != nil {
			return nil, fmt.Errorf("funnel on the issuer node %s: %w", o.IssuerTSNetHostname, err)
		}
		if ilc, err := its.LocalClient(); err == nil {
			if st, err := ilc.StatusWithoutPeers(ctx); err == nil && st.Self != nil {
				// A new name's first certificate takes longer than a Funnel
				// client's TLS handshake: warm it now, or every early
				// handshake (Tailscale's token-exchange verifier fetching the
				// JWKS) fails until issuance finishes.
				netd.PrefetchCert(ilc, st.Self.DNSName, log.With("node", o.IssuerTSNetHostname))
			}
		}
		api.Listeners = append(api.Listeners, "issuer-funnel:443")
		log.Info("WIF issuer served over Funnel on its own node", "node", o.IssuerTSNetHostname, "tag", mgmt.IssuerTag)
		serve("issuer-funnel", fl, issuer.Handler())
	}
	if o.IssuerListen != "" {
		if issuer == nil {
			return nil, errors.New("-issuer-listen needs -issuer-url")
		}
		ln, err := net.Listen("tcp", o.IssuerListen)
		if err != nil {
			return nil, err
		}
		serve("issuer", ln, issuer.Handler())
	}
	if len(rt.servers) == 0 {
		return nil, errors.New("no listeners: set -tsnet-hostname (normal operation) or -local-dev")
	}
	return rt, nil
}

// Handler is the /v1 API without any listener's auth wrapper: the caller
// must attach a principal to each request's context (mgmt.WithPrincipal);
// internal/inproc does so for the embedded host agent.
func (rt *Runtime) Handler() http.Handler { return rt.handler }

// Errs reports a failed listener (the runtime should then be closed).
func (rt *Runtime) Errs() <-chan error { return rt.errc }

// Close stops mgmt in twd-mgmt's shutdown order: withdraw the Service
// advertisement, stop the listeners (5 s grace), close the issuer node and
// remove the loopback descriptor and socket, close mgmt (flushes pending
// device deletes), then its node and the store. Safe on a partially
// started runtime.
func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if rt.gate != nil {
		// Withdraw before closing listeners: control stops routing callers
		// to this replica while its in-flight requests finish.
		if err := rt.gate.Close(sctx); err != nil {
			rt.log.Warn("service withdraw on shutdown failed", "service", rt.gate.Name, "err", err)
		}
		rt.gate = nil
	}
	for _, s := range rt.servers {
		s.Shutdown(sctx)
	}
	rt.servers = nil
	for i := len(rt.cleanups) - 1; i >= 0; i-- {
		rt.cleanups[i]()
	}
	rt.cleanups = nil
	rt.IssuerTS = nil
	if rt.M != nil {
		rt.M.Close()
		rt.M = nil
	}
	if rt.TS != nil {
		rt.TS.Close()
		rt.TS = nil
	}
	if rt.store != nil {
		rt.store.Close()
		rt.store = nil
	}
	return nil
}

// prefetchCert fetches the node's certificate in the background: a new
// node name's first certificate takes 30–100 s, and without Funnel (oauth
// mode) nothing else would request it before the first `twd link`, whose
// TLS handshake would time out.
func prefetchCert(ts *tsnet.Server, dnsName string, log *slog.Logger) {
	lc, err := ts.LocalClient()
	if err != nil || dnsName == "" {
		return
	}
	st := time.Now()
	for attempt := 1; ; attempt++ {
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Minute)
		_, _, err := lc.CertPair(cctx, dnsName)
		ccancel()
		if err == nil {
			log.Info("HTTPS certificate ready", "name", dnsName, "ms", time.Since(st).Milliseconds(), "attempts", attempt)
			return
		}
		// ACME orders fail transiently (seen: CreateOrder 404); a TLS
		// handshake retries on its own later, this only warms.
		log.Warn("HTTPS certificate prefetch failed (twd link falls back to http on :80 meanwhile)", "name", dnsName, "attempt", attempt, "err", err)
		if attempt == 4 {
			return
		}
		time.Sleep(time.Duration(attempt) * 20 * time.Second)
	}
}

// tsnetUp brings the node up. If it needs an interactive login and no auth
// key was given, it fails fast with the login URL instead of waiting for
// someone to click it: `twd up` owns the interactive flow.
func tsnetUp(ctx context.Context, ts *tsnet.Server, failOnLogin bool) (*ipnstate.Status, error) {
	if !failOnLogin {
		return ts.Up(ctx)
	}
	lc, err := ts.LocalClient()
	if err != nil {
		return nil, err
	}
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	for {
		n, err := w.Next()
		if err != nil {
			return nil, err
		}
		if n.ErrMessage != nil {
			return nil, errors.New(*n.ErrMessage)
		}
		if n.BrowseToURL != nil {
			return nil, fmt.Errorf("the mgmt node needs an interactive login (run `twd up` on this host to log in): %s", *n.BrowseToURL)
		}
		if n.State != nil && *n.State == ipn.Running {
			return ts.Up(ctx)
		}
	}
}
