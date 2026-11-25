package netd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"tailscale.com/envknob"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsnetlc"
	"github.com/rosskukulinski/tailworks/internal/tssvc"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

// Header names shared with the platform wrapper running in workerd.
const (
	// HeaderCF carries the JSON cf blob; workerd's socket is configured with
	// http.cfBlobHeader = "x-tw-cf", strips it, and exposes it as request.cf.
	HeaderCF = "x-tw-cf"
	// HeaderObject names the object on ingress (set by netd) and on egress
	// (set by the trusted platform Egress entrypoint, never by tenant code).
	HeaderObject = "x-tw-object"
	// HeaderError carries the error code on typed netd refusals.
	HeaderError = "tw-error"
)

// State is an object's node lifecycle state.
type State string

const (
	StatePending State = "pending" // queued or joining
	StateReady   State = "ready"   // node up, ingress listening
	StateFailed  State = "failed"  // join failed
	StateKilled  State = "killed"  // killed (terminal)
	StateClosed  State = "closed"  // node closed, state kept for rejoin
)

// Config configures a Registry.
type Config struct {
	// ControlURL is the coordination server. Required, so that a test or a
	// host can never silently join Tailscale's default control plane:
	// pass "https://controlplane.tailscale.com" explicitly for SaaS.
	ControlURL string

	// KeyTagsOnly: nodes take their tags from their auth key alone and
	// advertise none. Headscale (ADR-015) refuses a pre-auth-key
	// registration that also requests tags.
	KeyTagsOnly bool

	// StateRoot holds one 0700 directory per object (tsnet state, object.json).
	StateRoot string
	// Keys supplies auth keys for objects without saved state.
	Keys KeySource
	// Upstream receives attributed ingress requests (e.g. UnixUpstream to the
	// workerd ingress socket). Nil answers 503 upstream_unavailable.
	Upstream http.Handler
	// CallerCerts resolves callers' node certificates (RT-01, ADR-010): a
	// tailworks object's WhoIs CapMap is narrowed to CapMap ∩ cert.lim before it
	// reaches Upstream; a lookup failure withholds the caps. Nil: callers'
	// policy caps apply unchanged (no spawn narrowing is enforced).
	CallerCerts CertSource
	// IngressPort is the TCP port each object node listens on. Default 80.
	IngressPort int
	// JoinParallel bounds concurrent node joins. Default 16.
	JoinParallel int
	// JoinTimeout bounds one node's Up(). Default 2 minutes.
	JoinTimeout time.Duration
	// ReadyTimeout bounds how long egress waits for a pending object before
	// failing with identity_pending. Default 30 s.
	ReadyTimeout time.Duration

	// EgressGate, if set, is asked before each egress once the node is up
	// (egressgate.go, issue #331); an error refuses it.
	EgressGate func(ctx context.Context, objectID, stableNodeID string) error
	// Ephemeral and Tags are defaults for objects that don't override them.
	Ephemeral bool
	Tags      []string
	// Hostname maps an object ID to its node hostname. Default "tw-"+id.
	Hostname func(objectID string) string
	// AllowSiblings dials other objects hosted by this registry over
	// WireGuard (a hairpin). Default false: a flow to a sibling is delivered
	// in process to its attributed ingress, after the same netmap and packet
	// filter checks (samehost.go, RFC §4.8).
	AllowSiblings bool
	// ExtraForbidden prefixes are always refused on egress (platform endpoints).
	ExtraForbidden []netip.Prefix

	// Internet is the internet egress path for destinations that are not
	// on the tailnet (internet.go, ADR-049). Nil: there
	// is none; internet names are refused with internet_egress_unavailable
	// and non-tailnet IPs with forbidden_destination.
	Internet *InternetConfig

	// OnDelete, if set, is called after a killed object's node is closed
	// (kill tier 2: delete the device at the coordination server).
	OnDelete func(ctx context.Context, info ObjectInfo) error
	// KillFlushDelay is how long Kill keeps a node up after aborting its flows
	// so the RSTs are transmitted before Close. Default 50 ms; negative disables.
	KillFlushDelay time.Duration
	// Logger receives structured logs; it is always wrapped in a
	// RedactingHandler. Nil discards.
	Logger *slog.Logger
	// TSNetLogs forwards tsnet's internal logs to Logger at debug level.
	TSNetLogs bool

	// PublicListen opens a public node's listener (ADR-029). Nil: tsnet
	// ListenFunnel(":443", FunnelOnly()). Tests inject a listener whose
	// conns are *ipn.FunnelConn (testcontrol issues no certificates).
	PublicListen func(srv *tsnet.Server) (net.Listener, error)

	// HTTPSCert supplies the certificate of a node's tailnet HTTPS
	// listener (tailnethttps.go). Nil: the node's own ts.net certificate
	// from its LocalAPI, refused when the tailnet issues none. Tests
	// inject a self-signed one (testcontrol issues no certificates).
	HTTPSCert HTTPSCertFunc
	// OnPublicRequest is called after each public request (audit, D7).
	OnPublicRequest func(PublicRequestRecord)
	// OnPublicRejected is called at most once per PublicRejectWindow per
	// public node with the requests the limits refused.
	OnPublicRejected func(PublicRejectSummary)

	// OnRejected is called after control rejected a READY node (issue
	// #200, rejected.go): the object is failed and its node state dropped.
	// The owner restarts it with Create, which re-mints under the same
	// hostname. Nil: the object stays failed.
	OnRejected func(objectID string)
}

// ObjectOptions override registry defaults for one object.
type ObjectOptions struct {
	Hostname  string
	Tags      []string
	Ephemeral *bool
	// Public makes this a public (Funnel) node: see public.go.
	Public *PublicOptions

	// HTTPS also serves the node's ingress on :443 with its ts.net
	// certificate once it is ready (tailnethttps.go, SetTailnetHTTPS).
	HTTPS bool
}

// Registry holds every object node for one tenant on this host.
type Registry struct {
	cfg  Config
	log  *slog.Logger
	sem  *joinSlots
	ctx  context.Context
	stop context.CancelFunc

	mu       sync.Mutex
	objects  map[string]*Object
	localIPs map[netip.Addr]string // tailnet IPs of nodes hosted here -> object ID
	hostIPs  map[netip.Addr]bool
	closed   bool
	joins    sync.WaitGroup
}

var objectIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidObjectID reports whether id can be used as an object ID: a lowercase
// DNS label (it becomes part of the node hostname and the state dir name).
func ValidObjectID(id string) bool { return objectIDRE.MatchString(id) }

// New creates a Registry. It does not start any node.
func New(cfg Config) (*Registry, error) {
	if cfg.ControlURL == "" {
		return nil, errors.New("netd: Config.ControlURL is required")
	}
	if cfg.StateRoot == "" {
		return nil, errors.New("netd: Config.StateRoot is required")
	}
	if cfg.IngressPort == 0 {
		cfg.IngressPort = 80
	}
	if cfg.JoinParallel <= 0 {
		cfg.JoinParallel = 16
	}
	if cfg.JoinTimeout <= 0 {
		cfg.JoinTimeout = 2 * time.Minute
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 30 * time.Second
	}
	if cfg.KillFlushDelay == 0 {
		cfg.KillFlushDelay = 50 * time.Millisecond
	}
	if cfg.Hostname == nil {
		cfg.Hostname = func(id string) string { return "tw-" + id }
	}
	var h slog.Handler = slog.DiscardHandler
	if cfg.Logger != nil {
		h = cfg.Logger.Handler()
	}
	if err := os.MkdirAll(cfg.StateRoot, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.StateRoot, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registry{
		cfg:      cfg,
		log:      slog.New(NewRedactingHandler(h)),
		sem:      newJoinSlots(cfg.JoinParallel),
		ctx:      ctx,
		stop:     cancel,
		objects:  map[string]*Object{},
		localIPs: map[netip.Addr]string{},
	}
	r.RefreshHostIPs()
	return r, nil
}

// RefreshHostIPs re-reads the host's interface addresses, which egress always
// refuses.
func (r *Registry) RefreshHostIPs() {
	m := map[netip.Addr]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				m[p.Addr().Unmap()] = true
			}
		}
	}
	r.mu.Lock()
	r.hostIPs = m
	r.mu.Unlock()
}

// Object is one object's node.
type Object struct {
	id, hostname string
	dir          string
	ephemeral    bool
	tags         []string
	reg          *Registry
	ready        chan struct{} // closed when state leaves pending

	mu       sync.Mutex
	state    State
	err      error
	srv      *tsnet.Server
	lc       *local.Client
	ip4, ip6 netip.Addr
	nodeID   tailcfg.StableNodeID
	dnsName  string
	queued   time.Time
	spans    *spanRec            // the current join's stages (JoinSpans)
	pathOK   map[netip.Addr]bool // peers seen with a path (awaitPeerPath)
	joinDur  time.Duration
	rejoined bool
	reminted bool // saved state was rejected by control; joined with a fresh key
	ln       net.Listener
	httpSrv  *http.Server
	tombSrv  *http.Server // after a kill with Tombstone: answers object_killed
	// upCancel/upDone guard the in-flight start(): tsnet.Server.Close must not
	// run concurrently with Up (data race in tsnet init), so teardown cancels
	// the join and waits for start to return first.
	upCancel context.CancelFunc
	upDone   chan struct{}
	tr       *http.Transport
	flows    map[*flow]struct{}
	// svc hosts Tailscale Services on this node (in-process delivery, so
	// WhoIs sees the real caller); svcLn feeds those flows to svcHTTP (the
	// same handler as httpSrv; its own server so a withdraw can stop
	// keep-alives on Service connections only).
	svc      *tssvc.Host
	svcLn    *tssvc.ConnListener
	svcHTTP  *http.Server
	services []string // Services this node advertises

	// hopLn feeds same-host flows from sibling objects to hopHTTP (the
	// same handler as httpSrv; samehost.go).
	hopLn   *tssvc.ConnListener
	hopHTTP *http.Server
	// pub is non-nil for a public (Funnel) node (ADR-029); set at Create
	// and never changed.
	pub *publicState

	// rejectedCancel stops the ready node's watchRejected (rejected.go).
	rejectedCancel context.CancelFunc

	// tomb ends a kill's tombstone once (tombstone_end.go).
	tomb tombstoneEnd

	// https is the tailnet HTTPS listener on :443 (tailnethttps.go).
	https httpsState
}

// ObjectInfo is a snapshot of an object, safe to serialize.
type ObjectInfo struct {
	ID        string   `json:"id"`
	Hostname  string   `json:"hostname"`
	State     State    `json:"state"`
	Error     string   `json:"error,omitempty"`
	NodeID    string   `json:"nodeId,omitempty"`
	DNSName   string   `json:"dnsName,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Ephemeral bool     `json:"ephemeral"`
	Rejoined  bool     `json:"rejoined"`
	Reminted  bool     `json:"reminted,omitempty"`
	JoinMS    float64  `json:"joinMs,omitempty"`
	// Services are the Tailscale Services this node advertises.
	Services []string `json:"services,omitempty"`
	// Spans is the last join's stage breakdown (offsets from Create).
	Spans JoinSpans `json:"spans"`
	Flows int       `json:"flows"`
	// Public is set for a public (Funnel) node; PublicListening reports
	// whether its Funnel listener is open (ADR-029).
	Public          bool `json:"public,omitempty"`
	PublicListening bool `json:"publicListening,omitempty"`
}

// objectMeta is persisted next to the tsnet state so Rejoin can restore it.
type objectMeta struct {
	// V is the format version (ADR-041 §4): 0 = written
	// before versioning (the same layout as 1). readMeta refuses newer.
	V         int      `json:"v"`
	ID        string   `json:"id"`
	Hostname  string   `json:"hostname"`
	Tags      []string `json:"tags,omitempty"`
	Ephemeral bool     `json:"ephemeral"`
	// Public restores a public node as public on rejoin (never as a
	// tailnet listener). Its Enabled is not trusted on restore.
	Public *PublicOptions `json:"public,omitempty"`
}

// ErrClosed is returned after Close.
var ErrClosed = errors.New("netd: registry closed")

// ErrNotFound is returned for unknown object IDs.
var ErrNotFound = errors.New("netd: no such object")

// Create registers an object and queues its node join. It returns at once;
// use Object.Ready to wait. Creating an existing, non-terminal object returns
// it unchanged.
func (r *Registry) Create(id string, opts ObjectOptions) (*Object, error) {
	if !ValidObjectID(id) {
		return nil, fmt.Errorf("netd: invalid object id %q (want a lowercase DNS label)", id)
	}
	r.endTombstoneOf(id) // tombstone_end.go (#364): before the new node takes the dir
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	if o := r.objects[id]; o != nil {
		switch o.State() {
		case StatePending, StateReady:
			return o, nil
		}
	}
	o := &Object{
		id:        id,
		hostname:  opts.Hostname,
		dir:       filepath.Join(r.cfg.StateRoot, id),
		ephemeral: r.cfg.Ephemeral,
		tags:      r.cfg.Tags,
		reg:       r,
		ready:     make(chan struct{}),
		state:     StatePending,
		queued:    time.Now(),
		flows:     map[*flow]struct{}{},
	}
	o.spans = newSpanRec(o.queued)
	if opts.Public != nil {
		o.pub = newPublicState(*opts.Public)
	}
	o.https.want = opts.HTTPS && opts.Public == nil // tailnethttps.go
	if o.hostname == "" {
		o.hostname = r.cfg.Hostname(id)
	}
	if opts.Tags != nil {
		o.tags = opts.Tags
	}
	if opts.Ephemeral != nil {
		o.ephemeral = *opts.Ephemeral
	}
	// A saved object.json wins: a rejoin must come back as the same node.
	if m, err := readMeta(o.dir); err == nil {
		o.hostname, o.tags, o.ephemeral = m.Hostname, m.Tags, m.Ephemeral
		if opts.Public == nil && m.Public != nil {
			// Restored from saved state alone: public, but closed until the
			// owner calls SetPublicEnabled (fail closed).
			po := *m.Public
			po.Enabled = false
			o.pub = newPublicState(po)
		}
	}
	r.objects[id] = o
	r.joins.Add(1)
	slot := r.sem.enqueue() // in line now, so objects join in Create order
	go func() {
		defer r.joins.Done()
		r.join(o, slot)
	}()
	return o, nil
}

// Rejoin creates every object that has saved state under StateRoot. No auth
// key is requested for them. It returns the IDs it queued.
func (r *Registry) Rejoin() ([]string, error) {
	ents, err := os.ReadDir(r.cfg.StateRoot)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if !e.IsDir() || !ValidObjectID(e.Name()) || !hasState(filepath.Join(r.cfg.StateRoot, e.Name())) {
			continue
		}
		if _, err := r.Create(e.Name(), ObjectOptions{}); err != nil {
			return ids, err
		}
		ids = append(ids, e.Name())
	}
	return ids, nil
}

func hasState(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "tailscaled.state"))
	return err == nil && fi.Size() > 2
}

func readMeta(dir string) (*objectMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "object.json"))
	if err != nil {
		return nil, err
	}
	return decodeMeta(b)
}

// ObjectMetaVersion is the object.json format version this netd writes.
const ObjectMetaVersion = 1

// decodeMeta parses an object.json, refusing a version newer than this
// netd knows (a newer tailworks wrote it: rolling back past a format change).
func decodeMeta(b []byte) (*objectMeta, error) {
	var m objectMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return &m, err
	}
	if m.V > ObjectMetaVersion {
		return &m, fmt.Errorf("object.json format v%d was written by a newer tailworks (this one reads up to v%d); upgrade tw", m.V, ObjectMetaVersion)
	}
	return &m, nil
}

func (r *Registry) join(o *Object, slot *slotTicket) {
	if r.sem.wait(r.ctx, slot) != nil {
		o.fail(ErrClosed)
		return
	}
	defer r.sem.release()
	if o.State() != StatePending {
		return // killed while queued
	}
	o.spans.at(func(s *JoinSpans) *float64 { return &s.Slot })
	ctx, cancel := context.WithTimeout(r.ctx, r.cfg.JoinTimeout)
	defer cancel()
	done := make(chan struct{})
	o.mu.Lock()
	o.upCancel, o.upDone = cancel, done
	o.mu.Unlock()
	err := o.start(ctx)
	close(done) // before any teardown below, which waits on it
	if err != nil {
		err = RedactError(err)
		r.log.Warn("object join failed", "object", o.id, "err", err)
		o.fail(err)
		o.teardown()
		return
	}
}

// errSavedStateRejected: a rejoin from saved node state left the backend in
// NeedsLogin, or failing to log in (control expired or removed the device).
var errSavedStateRejected = errors.New("saved node state rejected by control (NeedsLogin or a login error)")

// needsLoginGrace is how long NeedsLogin, or a login error, must persist
// during a rejoin before it counts as a rejection (the backend may pass
// through either briefly).
var needsLoginGrace = 2 * time.Second

func (o *Object) start(ctx context.Context) error {
	r := o.reg
	if err := os.MkdirAll(o.dir, 0o700); err != nil {
		return err
	}
	rejoin := hasState(o.dir)
	var key string
	reminted := false
	if !rejoin {
		if r.cfg.Keys == nil {
			return errors.New("no saved state and no KeySource")
		}
		// Control rejected this object's last node after it was ready.
		reminted = hasRemintMark(o.dir) // rejected.go
		k, err := r.cfg.Keys.AuthKey(ctx, KeyRequest{ObjectID: o.id, Hostname: o.hostname, Tags: o.tags, Ephemeral: o.ephemeral, Remint: reminted})
		if err != nil {
			return fmt.Errorf("auth key: %w", err)
		}
		key = k
		o.spans.at(func(s *JoinSpans) *float64 { return &s.Key })
	}
	meta := objectMeta{V: ObjectMetaVersion, ID: o.id, Hostname: o.hostname, Tags: o.tags, Ephemeral: o.ephemeral}
	if o.pub != nil {
		o.pub.mu.Lock()
		po := o.pub.opts
		o.pub.mu.Unlock()
		meta.Public = &po
	}
	mb, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(o.dir, "object.json"), mb, 0o600); err != nil {
		return err
	}
	st := time.Now()
	lc, sst, err := o.up(ctx, key, rejoin)
	if errors.Is(err, errSavedStateRejected) {
		// The device is gone from control (an ephemeral node that was
		// offline during a restart, or one an admin removed). Drop the
		// saved state and join again as the same hostname with a fresh key
		// (KeyRequest.Remint lets mgmt-backed sources reopen the object).
		if r.cfg.Keys == nil {
			return err
		}
		r.log.Warn("saved node state rejected by control; re-minting under the same hostname", "object", o.id, "hostname", o.hostname, "ephemeral", o.ephemeral)
		o.dropSavedState()
		k, kerr := r.cfg.Keys.AuthKey(ctx, KeyRequest{ObjectID: o.id, Hostname: o.hostname, Tags: o.tags, Ephemeral: o.ephemeral, Remint: true})
		if kerr != nil {
			return fmt.Errorf("re-mint after %v: %w", err, kerr)
		}
		rejoin, reminted = false, true
		lc, sst, err = o.up(ctx, k, false)
	}
	if err != nil {
		return err
	}
	if reminted {
		clearRemintMark(o.dir)
	}
	joinDur := time.Since(st)
	o.mu.Lock()
	srv := o.srv
	o.mu.Unlock()
	ip4, ip6 := srv.TailscaleIPs()
	// A public node never listens on the tailnet: its only listener is
	// the Funnel one, opened below once the node is ready (ADR-029).
	var ln net.Listener
	if o.pub == nil {
		l, err := srv.Listen("tcp", fmt.Sprintf(":%d", r.cfg.IngressPort))
		if err != nil {
			return err
		}
		ln = l
	}
	tr := &http.Transport{
		DialContext:         o.dialEgress,
		Proxy:               nil, // never an environment proxy
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   false,
	}
	// limits.md#object-ingress-server
	hs := &http.Server{
		Handler:           http.HandlerFunc(o.serveIngress),
		ReadHeaderTimeout: 30 * time.Second, // limits.md#object-ingress-header-timeout
		ErrorLog:          slog.NewLogLogger(r.log.Handler(), slog.LevelDebug),
	}

	o.mu.Lock()
	if o.state != StatePending {
		o.mu.Unlock()
		if ln != nil {
			ln.Close()
		}
		return errors.New("killed while joining")
	}
	o.lc, o.ip4, o.ip6, o.joinDur, o.rejoined, o.reminted = lc, ip4, ip6, joinDur, rejoin, reminted
	if sst.Self != nil {
		o.nodeID, o.dnsName = sst.Self.ID, sst.Self.DNSName
	}
	if ln != nil {
		o.ln = &trackedListener{Listener: ln, o: o}
	}
	o.httpSrv, o.tr = hs, tr
	// Tailscale Services: flows to a Service VIP on the ingress port reach
	// the same attributed ingress (WhoIs on the real source address).
	o.svc, o.services = tssvc.NewHost(srv, lc), nil // tsnet.Up cleared any old advertisement
	o.svcLn = tssvc.NewConnListener(tssvc.Addr{Name: "service:" + o.hostname})
	o.svc.Route(uint16(r.cfg.IngressPort), o.svcLn.Deliver)
	svcLn := &trackedListener{Listener: o.svcLn, o: o}
	o.svcHTTP = &http.Server{Handler: hs.Handler, ReadHeaderTimeout: hs.ReadHeaderTimeout, ErrorLog: hs.ErrorLog}
	o.svcHTTP.SetKeepAlivesEnabled(false) // until a Service is advertised
	svcHS := o.svcHTTP
	hopHS, hopLn := o.startHopLocked(hs) // samehost.go
	o.state = StateReady
	// Control can still reject the node now that it is ready (#200,
	// rejected.go).
	rctx, rcancel := context.WithCancel(r.ctx)
	o.rejectedCancel = rcancel
	go o.watchRejected(rctx, lc)
	hostname := o.hostname // SetHostname may change it once ready
	if o.pub == nil {
		close(o.ready)
	}
	// A public node signals ready once its Funnel listener was attempted
	// (below), so Ready means "listening" when Enabled. Only start closes
	// o.ready once the state has left pending.
	o.mu.Unlock()
	o.spans.at(func(s *JoinSpans) *float64 { return &s.Listening })

	r.mu.Lock()
	for _, a := range []netip.Addr{ip4, ip6} {
		if a.IsValid() {
			r.localIPs[a] = o.id
		}
	}
	r.mu.Unlock()
	if o.pub != nil {
		o.startPublicFlusher()
		if err := o.openPublic(); err != nil {
			// The node is up; only the Funnel listener failed (HTTPS
			// certificates or the funnel nodeAttr missing). Report it and
			// let the owner retry with SetPublicEnabled.
			o.mu.Lock()
			o.err = fmt.Errorf("public listener: %w", err)
			o.mu.Unlock()
			r.log.Warn("public listener failed", "object", o.id, "err", RedactError(err))
		}
		close(o.ready)
	} else {
		go hs.Serve(o.ln)
		o.openHTTPSIfWanted() // tailnethttps.go
	}
	go svcHS.Serve(svcLn)
	go hopHS.Serve(hopLn)
	r.log.Info("object ready", "object", o.id, "hostname", hostname, "ip", ip4.String(),
		"node", string(o.nodeID), "join_ms", joinDur.Milliseconds(), "rejoined", rejoin, "reminted", reminted, "spans", o.spans.get())
	return nil
}

// up builds the tsnet server (stored in o.srv so teardown can close it) and
// brings it up. With no key (a rejoin), it also watches the IPN bus:
// tsnet.Up waits for Running and never reacts to NeedsLogin, which is where
// a rejected saved state ends up for good; that returns errSavedStateRejected.
func (o *Object) up(ctx context.Context, key string, rejoin bool) (*local.Client, *ipnstate.Status, error) {
	r := o.reg
	logf := func(string, ...any) {}
	if r.cfg.TSNetLogs {
		logf = tsnetLogf(r.log, o.id)
	}
	logf = o.spans.logf(logf)
	srv := &tsnet.Server{
		Hostname:      o.hostname,
		Dir:           o.dir,
		ControlURL:    r.cfg.ControlURL,
		AuthKey:       key,
		Ephemeral:     o.ephemeral,
		AdvertiseTags: o.tags,
		Logf:          logf,
		UserLogf:      logf,
	}
	if r.cfg.KeyTagsOnly {
		srv.AdvertiseTags = nil
	}
	o.mu.Lock()
	if o.state != StatePending {
		o.mu.Unlock()
		return nil, nil, errors.New("killed while joining")
	}
	o.srv = srv
	o.mu.Unlock()

	upCtx, upCancel := context.WithCancel(ctx)
	defer upCancel()
	rejected := make(chan struct{})
	// Start explicitly (tsnet.Up would) so the backend start is its own span
	// and the IPN bus can be watched from the first transition.
	if err := srv.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", o.hostname, err)
	}
	o.spans.at(func(s *JoinSpans) *float64 { return &s.Start })
	lc, err := tsnetlc.Client(srv) // Close drains it (#245)
	if err != nil {
		return nil, nil, err
	}
	// ADR-019 rule 4: without this every LocalClient call on macOS execs lsof
	// and sends the *system* Tailscale.app's token (~25 ms/call). tsnet
	// returns the same *local.Client on every call, so set it once, here,
	// before the watchers below start reading it (issue #134).
	lc.OmitAuth = true
	go o.spans.watch(upCtx, lc)
	if rejoin && key == "" {
		var once sync.Once
		go watchNeedsLogin(upCtx, lc, func() { once.Do(func() { close(rejected); upCancel() }) })
	}
	if _, err := srv.Up(upCtx); err != nil {
		select {
		case <-rejected:
			return nil, nil, errSavedStateRejected
		default:
		}
		return nil, nil, fmt.Errorf("up %s: %w", o.hostname, err)
	}
	o.spans.at(func(s *JoinSpans) *float64 { return &s.Running })
	sst, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		return nil, nil, err
	}
	return lc, sst, nil
}

// watchNeedsLogin calls onRejected once the backend has sat in NeedsLogin,
// or kept reporting a login error, for needsLoginGrace without reaching
// Starting/Running. A node whose saved key control has expired doesn't
// always reach NeedsLogin: it can stay in NoState, retrying its login and
// reporting "NodeKeyExpired" errors, forever. Without the error case that
// rejoin never ends, and it holds a join slot (JoinParallel) while it waits
// (issue #75). Those errors arrive as a health warning, not an ErrMessage
// (loginRejectedNotify, issue #182).
func watchNeedsLogin(ctx context.Context, lc *local.Client, onRejected func()) {
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialHealthState)
	if err != nil {
		return
	}
	defer w.Close()
	var timer *time.Timer
	up := false // reached Starting/Running: errors after that aren't a rejection
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		n, err := w.Next()
		if err != nil {
			return
		}
		if loginRejectedNotify(&n) && !up && timer == nil { // rejoin.go
			timer = time.AfterFunc(needsLoginGrace, onRejected)
		}
		if n.State == nil {
			continue
		}
		switch *n.State {
		case ipn.NeedsLogin:
			if timer == nil {
				timer = time.AfterFunc(needsLoginGrace, onRejected)
			}
		case ipn.Starting, ipn.Running:
			up = true
			if timer != nil {
				timer.Stop()
				timer = nil
			}
		}
	}
}

// dropSavedState closes the rejected server and removes the node state, so
// the next up joins as a new device (object.json and certs are kept).
func (o *Object) dropSavedState() {
	o.mu.Lock()
	srv := o.srv
	o.srv = nil
	o.mu.Unlock()
	if srv != nil {
		tsnetlc.Close(srv)
	}
	os.Remove(filepath.Join(o.dir, "tailscaled.state"))
}

func (o *Object) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state == StatePending {
		o.state, o.err = StateFailed, err
		close(o.ready)
	}
}

// teardown closes the node and its listeners without aborting flows or
// logging out. It is idempotent.
func (o *Object) teardown() {
	o.mu.Lock()
	upCancel, upDone := o.upCancel, o.upDone
	o.mu.Unlock()
	if upDone != nil {
		select {
		case <-upDone:
		default:
			upCancel()
			<-upDone
		}
	}
	o.closePublic()
	o.stopPublicFlusher()
	o.stopCertPrefetch()
	o.stopHTTPS() // tailnethttps.go
	o.mu.Lock()
	srv, hs, tr, ts, shs := o.srv, o.httpSrv, o.tr, o.tombSrv, o.svcHTTP
	hop := o.hopHTTP
	o.services = nil // a closed node advertises nothing
	if o.rejectedCancel != nil {
		o.rejectedCancel() // before srv.Close, which waits on open LocalAPI streams
	}
	o.mu.Unlock()
	if hs != nil {
		hs.Close()
	}
	if shs != nil {
		shs.Close()
	}
	if hop != nil {
		hop.Close()
	}
	if ts != nil {
		ts.Close()
	}
	if tr != nil {
		tr.CloseIdleConnections()
	}
	if srv != nil {
		tsnetlc.Close(srv)
	}
	o.reg.forgetIPs(o)
}

func (r *Registry) forgetIPs(o *Object) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for a, id := range r.localIPs {
		if id == o.id {
			delete(r.localIPs, a)
		}
	}
}

// Get returns the object with the given ID, or nil.
// RemoveState deletes a killed object's saved node state (tw down
// --purge). A kill with a tombstone removes the dir only when the tombstone
// expires; if the host stops before that, the next object of the same ID
// would find the dead node's state and spend a re-mint round trip on it.
// Refused while the object is live (pending or ready).
func (r *Registry) RemoveState(id string) error {
	if !ValidObjectID(id) {
		return fmt.Errorf("netd: invalid object id %q", id)
	}
	r.mu.Lock()
	o := r.objects[id]
	r.mu.Unlock()
	if o != nil {
		switch o.State() {
		case StatePending, StateReady:
			return fmt.Errorf("netd: object %s is live", id)
		}
	}
	return os.RemoveAll(filepath.Join(r.cfg.StateRoot, id))
}

func (r *Registry) Get(id string) *Object {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.objects[id]
}

// Objects returns a snapshot of every object, sorted by ID.
func (r *Registry) Objects() []ObjectInfo {
	r.mu.Lock()
	objs := make([]*Object, 0, len(r.objects))
	for _, o := range r.objects {
		objs = append(objs, o)
	}
	r.mu.Unlock()
	out := make([]ObjectInfo, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ID returns the object ID.
func (o *Object) ID() string { return o.id }

// State returns the current lifecycle state.
func (o *Object) State() State {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state
}

// Server returns the object's tsnet.Server (nil before the join starts).
// Callers must not Close it; use Registry.Kill.
func (o *Object) Server() *tsnet.Server {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.srv
}

// LocalClient returns the node's local API client (nil before it is up),
// for read-only diagnostics such as path probes (Status, disco Ping).
func (o *Object) LocalClient() *local.Client {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != StateReady {
		return nil
	}
	return o.lc
}

// Info returns a snapshot.
func (o *Object) Info() ObjectInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	in := ObjectInfo{ID: o.id, Hostname: o.hostname, State: o.state, NodeID: string(o.nodeID),
		DNSName: o.dnsName, Tags: o.tags, Ephemeral: o.ephemeral, Rejoined: o.rejoined, Reminted: o.reminted,
		JoinMS: float64(o.joinDur.Microseconds()) / 1000, Spans: o.spans.get(), Flows: len(o.flows), Services: slices.Clone(o.services)}
	if o.pub != nil {
		in.Public, in.PublicListening = true, o.PublicListening()
	}
	if o.err != nil {
		in.Error = Redact(o.err.Error())
	}
	for _, a := range []netip.Addr{o.ip4, o.ip6} {
		if a.IsValid() {
			in.IPs = append(in.IPs, a.String())
		}
	}
	return in
}

// Ready waits until the object's node is up. It returns the join error if the
// join failed, or ctx's error.
func (o *Object) Ready(ctx context.Context) error {
	select {
	case <-o.ready:
	case <-ctx.Done():
		return newErr(CodeIdentityPending, "object "+o.id+" node not ready: "+ctx.Err().Error())
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch o.state {
	case StateReady:
		return nil
	case StateKilled:
		return newErr(CodeKilled, "object "+o.id+" was killed")
	case StateClosed:
		return newErr(CodeKilled, "object "+o.id+" is closed")
	default:
		msg := "join failed"
		if o.err != nil {
			msg = Redact(o.err.Error())
		}
		return newErr(CodeBootstrapFailed, msg)
	}
}

// IPv4 returns the object's tailnet IPv4 (zero before ready).
func (o *Object) IPv4() netip.Addr {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ip4
}

// Close closes every node in parallel, keeping saved state so the objects can
// Rejoin later. It waits for in-progress joins to finish or abort.
func (r *Registry) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	objs := make([]*Object, 0, len(r.objects))
	for _, o := range r.objects {
		objs = append(objs, o)
	}
	r.mu.Unlock()
	r.stop() // aborts queued and in-progress joins
	closeParallel(objs, 64, func(o *Object) {
		o.mu.Lock()
		if o.state == StatePending || o.state == StateReady {
			if o.state == StatePending {
				o.err = ErrClosed
				close(o.ready)
			}
			o.state = StateClosed
		}
		o.mu.Unlock()
		o.abortFlows()
		o.teardown()
	})
	r.joins.Wait()
	return nil
}

func closeParallel(objs []*Object, par int, f func(*Object)) {
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for _, o := range objs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			f(o)
		}()
	}
	wg.Wait()
}

var _ io.Closer = (*Registry)(nil)

var portMapOnce sync.Once

// DisablePortMapping turns off UPnP / NAT-PMP / PCP for every tsnet node in
// this process. A fresh node's first netcheck waits for the port-map probe
// (a 250 ms timeout when nothing answers, as on most networks) before it
// picks a home DERP and goes Running: about 250 ms of every join
// (STATUS.md "Fast identity"). It also stops N object nodes from each
// asking the router for a mapping. Cost: no router-assisted direct paths
// through hard NATs (they fall back to DERP).
//
// Call it once at process start, BEFORE any tsnet node exists: it sets
// tailscale's TS_DISABLE_PORTMAPPER envknob, which running nodes read
// without synchronisation.
func DisablePortMapping() {
	portMapOnce.Do(func() { envknob.Setenv("TS_DISABLE_PORTMAPPER", "true") })
}

// SetHostname renames a ready node in place (a pre-joined pool node handed
// to an object; ADR-031). The node changes its own hostname (LocalAPI
// EditPrefs: no control-plane API call), object.json is rewritten so a
// rejoin keeps the name, and SetHostname waits until the node's own
// netmap names it so; control may add a suffix ("-1") if the name is taken.
// It returns the node's MagicDNS name. Measured on production control: a
// peer's netmap shows the new name ~210 ms after the call.
func (o *Object) SetHostname(ctx context.Context, hostname string) (string, error) {
	o.mu.Lock()
	lc, st := o.lc, o.state
	o.mu.Unlock()
	if st != StateReady || lc == nil {
		return "", newErr(CodeIdentityPending, "object "+o.ID()+" is "+string(st)+", not ready")
	}
	if _, err := lc.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{Hostname: hostname}, HostnameSet: true}); err != nil {
		return "", fmt.Errorf("set hostname: %w", err)
	}
	o.mu.Lock()
	o.hostname = hostname
	mb, _ := json.Marshal(objectMeta{V: ObjectMetaVersion, ID: o.id, Hostname: o.hostname, Tags: o.tags, Ephemeral: o.ephemeral})
	o.mu.Unlock()
	if err := os.WriteFile(filepath.Join(o.dir, "object.json"), mb, 0o600); err != nil {
		return "", err
	}
	for {
		if st, err := lc.StatusWithoutPeers(ctx); err == nil && st.Self != nil {
			label, _, _ := strings.Cut(st.Self.DNSName, ".")
			if label == hostname || strings.HasPrefix(label, hostname+"-") {
				o.mu.Lock()
				o.dnsName = st.Self.DNSName
				o.mu.Unlock()
				return st.Self.DNSName, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", newErr(CodeIdentityPending, "object "+o.ID()+": hostname "+hostname+" not in its netmap yet: "+ctx.Err().Error())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
