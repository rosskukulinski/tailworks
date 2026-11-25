// Package mgmtd runs tw-mgmt (the management plane) as a library: the
// standalone tw-mgmt binary is a flag parser over Start, and the
// single-process `tw up` embeds it next to the host agent (internal/
// hostdrun) in one process.
//
// Start opens the store and custody, joins (or re-opens) mgmt's own tsnet
// node when TSNetHostname is set, and serves the /v1 API on it behind
// WhoIs (TailnetHandler), optionally as a replica of a Tailscale Service
// (TSNetService), publishes the node's loopback proxy for the local tailworks
// CLI. mgmt serves nothing over Funnel. With LocalDev it also serves the
// unix socket guarded by the per-host token.
package mgmtd

import (
	"context"
	"crypto/tls"
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

	"github.com/rosskukulinski/tailworks/internal/approvecap"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/internal/registry"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/internal/tsnetlc"
	"github.com/rosskukulinski/tailworks/internal/tssvc"
	"github.com/rosskukulinski/tailworks/mgmt"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg/nodecap"
	"tailscale.com/tsnet"
)

// Flag defaults tw-mgmt and the single-process tw up share (an Options
// zero value means "off" for these, so callers pass the default explicitly).
const (
	// DefaultWarmKeys is the warm-key pool cap (tw-mgmt -warm-keys).
	DefaultWarmKeys = 5
	// DefaultReapInterval is the reaper interval (tw-mgmt -reap-interval).
	DefaultReapInterval = 10 * time.Second
)

// Options mirrors tw-mgmt's flags, one field per flag (see cmd/tw-mgmt).
type Options struct {
	// StateDir (0700) holds the db, custody and the tsnet state.
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
	// TSNetHostname names mgmt's own tailnet node (required unless
	// LocalDev); TSNetControlURL is required with it. TSNetAuthKeyFile
	// holds TS_AUTHKEY=… for the first join (else the node state must be
	// logged in already; tw up hands the key over: TSNetAuthKeyOnce). TSNetTags are
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

	// TSNetAuthKeyOnce makes TSNetAuthKeyFile a single-use handoff (`tw
	// up`, joinkey.go): the key is read and the file deleted before the
	// join, a missing file is no key, and a failed Start deletes the file
	// and records why in StartErrFile.
	TSNetAuthKeyOnce bool

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
	// RevokeWarmKeysOnClose: Close revokes the unused pooled keys
	// (mgmt.Config; tw-mgmt -revoke-warm-keys-on-exit).
	RevokeWarmKeysOnClose bool

	// APILimits rate-limit tailnet callers of the API (zero: the defaults).
	APILimits mgmt.APILimits

	// RegistryDir is the OCI registry blob store ("" = <StateDir>/registry,
	// "off" = no registry); RegistryInsecure lists upstream registry hosts
	// mirrored over plain HTTP; NoMirror refuses external image refs at
	// deploy; ImageAliasesFile is a JSON file [{from, to, arch, source}] replacing
	// the default image alias map.
	RegistryDir      string
	RegistryInsecure []string
	NoMirror         bool
	ImageAliasesFile string
	// Log (default: discard).
	Log *slog.Logger

	// MetricsPort serves GET /metrics on mgmt's own tailnet node
	// (ADR-045; 0 = off; flag default obs.DefaultMetricsPort).
	MetricsPort int

	// BundleRetention is how many bundles per workload mgmt keeps for
	// rollback (mgmt.Config; 0 = mgmt.DefaultBundleRetention, < 0 = all).
	BundleRetention int

	// Audit retention (mgmt.Config, mgmt/retention.go): 0 = keep every
	// record (the default).
	AuditMaxAge     time.Duration
	AuditMaxRecords int64
}

// Runtime is a running mgmt.
type Runtime struct {
	// M is the service, Server the /v1 API over it, TS mgmt's own tsnet
	// node (nil with LocalDev only), Self its identity.
	M      *mgmt.Mgmt
	Server *mgmt.Server
	TS     *tsnet.Server
	Self   mgmt.SelfInfo

	log      *slog.Logger
	store    *mgmt.SQLStore
	handler  http.Handler
	errc     chan error
	servers  []*http.Server
	gate     *mgmt.ServiceGate
	cleanups []func()

	// bg is cancelled first by Close: background LocalAPI calls (the
	// certificate prefetch) must not hold tsnet.Server.Close's LocalAPI
	// shutdown for its 5 s grace (#245).
	bg       context.Context
	bgCancel context.CancelFunc

	// metrics.go: registries served with mgmt's on /metrics.
	metricsMu    sync.Mutex
	extraMetrics obs.Gatherers
}

// Start runs mgmt per o (tw-mgmt's run body up to its wait). Cancel
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
	rt.bg, rt.bgCancel = context.WithCancel(context.Background())
	defer func() {
		if err != nil {
			rt.Close()
		}
	}()
	if o.TSNetAuthKeyOnce {
		defer func() { // joinkey.go
			if err == nil {
				os.Remove(StartErrFile(o.StateDir))
				return
			}
			if o.TSNetAuthKeyFile != "" {
				os.Remove(o.TSNetAuthKeyFile) // single use, also when Start fails before reading it
			}
			recordStartErr(o.StateDir, err)
		}()
	}

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
	var selfTags []string // for the loopback descriptor: tw up checks them
	var selfFunnel bool
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
			load := tsapi.LoadEnvFile
			if o.TSNetAuthKeyOnce {
				load = func(path, _ string) (string, error) { return readJoinKeyOnce(path) } // joinkey.go
			}
			k, err := load(o.TSNetAuthKeyFile, "TS_AUTHKEY")
			if err != nil {
				return nil, err
			}
			ts.AuthKey = k
		}
		rt.TS = ts
		// Before anything uses the node's LocalAPI client: Close drains it
		// (internal/tsnetlc). ADR-019 rule 4: OmitAuth skips the LocalAPI's
		// per-call peer-credential check (~25 ms per WhoIs on macOS); it is
		// set here because writing it later races the client's users.
		lc, err := tsnetlc.Client(ts)
		if err != nil {
			return nil, fmt.Errorf("tsnet start: %w", err)
		}
		lc.OmitAuth = true
		upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		upStart := time.Now()
		st, err := tsnetUp(upCtx, rt.bg, ts, ts.AuthKey == "", o.TSNetService == "")
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
		tags := []string{}
		if st.Self.Tags != nil {
			tags = st.Self.Tags.AsSlice()
		}
		selfTags = tags
		_, selfFunnel = st.Self.CapMap[nodecap.Funnel]
		log.Info("tailnet node", "name", self.DNSName, "tailnet", self.Tailnet, "tags", tags, "https", self.HTTPS, "ips", st.TailscaleIPs,
			"state", st.BackendState, "ms", time.Since(upStart).Milliseconds())
	}

	mcfg := mgmt.Config{Store: store, Custody: custody, Broker: &mgmt.Broker{BaseURL: o.TSAPIURL},
		IDSecret: secret, ReplicaID: replicaID, ReapInterval: o.ReapInterval, DeviceDeleteDelay: o.DeviceDeleteDelay, Log: log,
		OperatorTailnet: self.Tailnet, OperatorControlURL: o.TSNetControlURL, NoMirror: o.NoMirror, WarmKeys: o.WarmKeys, WarmKeyTTL: o.WarmKeyTTL,
		RevokeWarmKeysOnClose: o.RevokeWarmKeysOnClose, Version: o.Version, ReleaseDir: filepath.Join(o.StateDir, "releases")}
	switch o.RegistryDir {
	case "off":
	case "":
		mcfg.RegistryDir = filepath.Join(o.StateDir, "registry")
	default:
		mcfg.RegistryDir = o.RegistryDir
	}
	mcfg.BundleRetention = o.BundleRetention // ADR-046
	// Audit retention: mgmt/retention.go.
	mcfg.AuditMaxAge, mcfg.AuditMaxRecords = o.AuditMaxAge, o.AuditMaxRecords
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
		if err := registry.ValidateAliases(mcfg.ImageAliases); err != nil {
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
	m.SetConsoleURL(self.ConsoleURL()) // notification deep links (mgmt/notify.go)
	api := &mgmt.Server{M: m, Self: self, Version: o.Version}
	if len(o.Admins) > 0 {
		api.Admins = o.Admins
	}
	// DNS-rebinding guard: the tailnet listeners answer only to mgmt's own
	// names (and IP literals); set before Handler(), whose Origin check uses it.
	api.Hosts = mgmt.NewHostAllowlist(mgmt.MgmtHostNames(self, o.TSNetHostname, o.TSNetService)...)
	rt.Server, rt.Self = api, self
	rt.handler = api.Handler()
	if ts == nil && !o.LocalDev {
		return nil, errors.New("no listeners: normal operation is tsnet only (-tsnet-hostname); -local-dev serves the unix socket for tw dev and tests")
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
			wi, err := tssvc.WhoIs(ctx, lc, addr) // svc:tw-mgmt flows get svc: grants (H22)
			if err != nil {
				return mgmt.WhoIsInfo{}, err
			}
			info := mgmt.WhoIsInfo{NodeID: string(wi.Node.StableID), Name: wi.Node.Name, Tags: wi.Node.Tags}
			if wi.UserProfile != nil {
				info.Login = wi.UserProfile.LoginName
			}
			for c, vals := range wi.CapMap {
				info.Caps = append(info.Caps, string(c))
				if c == mgmt.MCPCap || c == approvecap.Cap || c == mgmt.TenantAdminCap { // their grants' values narrow them
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
		th := api.TailnetHandler(whois, mgmt.NewAPILimiter(o.APILimits).Wrap(rt.handler))
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
			// The operator console: the tailworks CLI on this machine dials mgmt's
			// own tailnet IP through the node's SOCKS5 loopback proxy, so
			// its requests carry mgmt's identity (WhoIs → operator). The
			// credential lives in a 0600 file next to the state; nothing
			// listens on a unix socket.
			addr, cred, _, err := ts.Loopback()
			if err != nil {
				return nil, fmt.Errorf("tsnet loopback: %w", err)
			}
			lb := mgmt.Loopback{Addr: addr, ProxyCred: cred, MgmtIP: self.IP, DNSName: self.DNSName, HTTPS: self.HTTPS, PID: os.Getpid()}
			lb.NodeID, lb.Tailnet, lb.Tags, lb.Funnel = self.NodeID, self.Tailnet, selfTags, selfFunnel
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
			// The API over the node's own HTTPS certificate: what `tw
			// link` uses from a developer machine. tailnet-only (Listen
			// never serves Funnel traffic). Not ts.ListenTLS: it waits for
			// Running (a DERP connection), which tsnetUp no longer does
			// (#245); self.HTTPS already checked the cert domains.
			ln, err := ts.Listen("tcp", ":443")
			if err != nil {
				return nil, fmt.Errorf("tsnet tls: %w", err)
			}
			tln := tls.NewListener(ln, &tls.Config{GetCertificate: lc.GetCertificate})
			api.Listeners = append(api.Listeners, "tsnet:443")
			serve("tsnet-tls", tln, th)
			// Fetch the node's certificate now, in the background: a new
			// node name's first certificate takes 30–100 s, and nothing
			// else would request it before the first `tw link`, whose TLS
			// handshake would time out.
			go prefetchCert(rt.bg, ts, self.DNSName, log)
		} else if o.TSNetTLS {
			log.Warn("HTTPS is not enabled on this tailnet: the API is served on :80 only; `tw link` uses plain HTTP (WireGuard-encrypted, WhoIs-authenticated)")
		}
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

// Close stops mgmt in tw-mgmt's shutdown order: withdraw the Service
// advertisement, stop the listeners (5 s grace), remove the loopback
// descriptor and socket, close mgmt (flushes pending
// device deletes), then its node and the store. Safe on a partially
// started runtime.
func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	if rt.bgCancel != nil {
		rt.bgCancel()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Each phase's time goes to the log: a `tw down` that takes seconds
	// shows where (#245).
	var phases []any
	t0 := time.Now()
	lap := func(phase string) {
		phases = append(phases, phase+"_ms", time.Since(t0).Milliseconds())
		t0 = time.Now()
	}
	if rt.servers != nil || rt.M != nil || rt.TS != nil {
		defer func() { rt.log.Info("stopped", phases...) }()
	}
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
	lap("listeners")
	for i := len(rt.cleanups) - 1; i >= 0; i-- {
		rt.cleanups[i]()
	}
	rt.cleanups = nil
	if rt.M != nil {
		rt.M.Close()
		rt.M = nil
	}
	lap("mgmt")
	if rt.TS != nil {
		tsnetlc.Close(rt.TS)
		rt.TS = nil
	}
	lap("tsnet")
	if rt.store != nil {
		rt.store.Close()
		rt.store = nil
	}
	lap("store")
	return nil
}

// prefetchCert fetches the node's certificate in the background: a new
// node name's first certificate takes 30–100 s, and nothing else would
// request it before the first `tw link`, whose TLS handshake would time
// out.
func prefetchCert(ctx context.Context, ts *tsnet.Server, dnsName string, log *slog.Logger) {
	lc, err := ts.LocalClient()
	if err != nil || dnsName == "" {
		return
	}
	st := time.Now()
	for attempt := 1; ; attempt++ {
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Minute)
		_, _, err := lc.CertPair(cctx, dnsName)
		ccancel()
		if err == nil {
			log.Info("HTTPS certificate ready", "name", dnsName, "ms", time.Since(st).Milliseconds(), "attempts", attempt)
			return
		}
		if ctx.Err() != nil {
			return // Close
		}
		// ACME orders fail transiently (seen: CreateOrder 404); a TLS
		// handshake retries on its own later, this only warms.
		log.Warn("HTTPS certificate prefetch failed (tw link falls back to http on :80 meanwhile)", "name", dnsName, "attempt", attempt, "err", err)
		if attempt == 4 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt) * 20 * time.Second):
		}
	}
}

// tsnetUp brings the node up. If it needs an interactive login and no auth
// key was given, it fails fast with the login URL instead of waiting for
// someone to click it: `tw up` owns the interactive flow.
//
// earlyOK returns as soon as the node is logged in with a netmap (state
// Starting): mgmt can serve the local CLI, the embedded host and its own
// listeners then. Waiting for Running would also wait for the first
// netcheck and a live DERP connection, ~3 s on a fresh node and longer
// while the network comes up after a boot (#245); peers reach the node
// once that is done either way. tsnet.Server.Up then runs in the
// background (bg) for its one-time reset of stale serve config. Without
// earlyOK (a Tailscale Service replica, whose advertisement that reset
// would clear) it waits for Running through Up as before.
func tsnetUp(ctx, bg context.Context, ts *tsnet.Server, failOnLogin, earlyOK bool) (*ipnstate.Status, error) {
	if !failOnLogin && !earlyOK {
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
		if n.BrowseToURL != nil && failOnLogin {
			return nil, fmt.Errorf("the mgmt node needs an interactive login (run `tw up` on this host to log in): %s", *n.BrowseToURL)
		}
		if n.State != nil && *n.State == ipn.Running {
			return ts.Up(ctx)
		}
		// The netmap (SelfChange) or a state change: logged in yet?
		if earlyOK && (n.State != nil || n.SelfChange != nil) {
			if st, ok := LoggedIn(ctx, lc); ok {
				go ts.Up(bg)
				return st, nil
			}
		}
	}
}

// LoggedIn is the node's status once it is logged in with a netmap and an
// address (state Starting or Running): enough to read its name, tailnet,
// tags and IPs, and to serve, without waiting for a DERP connection.
func LoggedIn(ctx context.Context, lc *local.Client) (*ipnstate.Status, bool) {
	st, err := lc.Status(ctx)
	if err != nil || st.Self == nil || len(st.TailscaleIPs) == 0 {
		return nil, false
	}
	return st, st.BackendState == ipn.Starting.String() || st.BackendState == ipn.Running.String()
}
