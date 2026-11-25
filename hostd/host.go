// Package hostd is tw-hostd's process tier: it registers with tw-mgmt and
// heartbeats, pulls assignments, and for each deployment unpacks the
// bundle, generates the workerd config (platform wrapper + tenant service),
// supervises one workerd process per tenant workload, and drives the netd
// library so that the workload and every Durable Object instance get their
// own tailnet node (async, on first use).
//
// Data flow (see gen for the workerd side):
//
//	caller ─WG─▶ node (netd: WhoIs, strip, cf blob) ─▶ hostd ingress (adds cf.tw routing)
//	       ─▶ workerd ingress.sock ─▶ platform entry ─▶ Host_<Class> DO ─▶ tenant facet
//	tenant fetch() ─▶ platform Egress (props: class,name; MAC) ─▶ hostd egress-obj.sock
//	       ─▶ netd egress (object's own node, ADR-019 guard) ─WG─▶ destination
package hostd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
	"github.com/rosskukulinski/tailworks/internal/version"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/image"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/internal/procsb"
	"github.com/rosskukulinski/tailworks/netd"
	"tailscale.com/tsnet"
)

// DefaultControlURL is used when a tenant record names no control server.
const DefaultControlURL = "https://controlplane.tailscale.com"

// Config configures a Host.
type Config struct {
	HostID string
	Mgmt   *mgmtapi.Client
	// DataDir holds node state, DO storage, objects.json and the audit key.
	DataDir string
	// RunDir holds unix sockets and generated instance dirs; keep it short
	// (macOS limits socket paths to 104 bytes). Default: a new /tmp dir.
	RunDir     string
	WorkerdBin string
	// HeartbeatEvery is the heartbeat (and assignment poll) period (3 s).
	HeartbeatEvery time.Duration
	// FenceAfter: with no successful heartbeat for this long (monotonic
	// clock), the host stops serving its *stateful* workloads (assignment
	// "fence"; ADR-028 §3); stateless Workers keep serving. It must stay
	// below mgmt's NamespaceLeaseTTL (45 s) minus a margin, so a host has
	// stopped serving before its lease can be taken over: at most
	// MaxFenceAfter. 0 = DefaultFenceAfter (30 s); negative disables.
	FenceAfter time.Duration
	// HostnamePrefix is prepended to every node hostname (e2e runs use a
	// unique per-run prefix).
	HostnamePrefix string
	// Ephemeral joins nodes as ephemeral (tests and dev).
	Ephemeral bool
	// PoolNodes is how many pre-joined nodes to keep per hot class (a root
	// create here in the last hour): a create of that class then takes one
	// and renames it (~0.2 s) instead of joining a new node (ADR-031).
	// 0 disables the pool.
	PoolNodes     int
	CapacityNodes int
	// RegistrationSecret is the one-time secret from the join file
	// (TW_REGISTRATION_SECRET); mgmt needs it on this host ID's first
	// tailnet registration. Empty for local-dev hosts. Never logged.
	RegistrationSecret string
	// ReadyTimeout bounds how long requests wait for an object's node
	// (identity_pending after; default 30 s).
	ReadyTimeout time.Duration
	// DrainTimeout bounds the blue/green drain of the old workerd (10 s).
	DrainTimeout time.Duration
	// KillTombstone keeps a killed node up (RST-ing new connections) this
	// long before closing it (default 10 s; negative disables).
	KillTombstone time.Duration
	// ControlURL overrides every tenant's control URL (tests: testcontrol).
	ControlURL string
	// JoinParallel bounds concurrent node joins per tenant (netd default 16).
	JoinParallel int
	Logger       *slog.Logger
	TSNetLogs    bool
	// PublicListen overrides how public nodes open their Funnel listener
	// (netd.Config.PublicListen). Tests only: testcontrol issues no
	// certificates, so they inject a listener of *ipn.FunnelConn.
	PublicListen func(srv *tsnet.Server) (net.Listener, error)

	// TailnetHTTPSCert overrides the certificate of a workload node's
	// tailnet HTTPS listener (netd.Config.HTTPSCert). Tests only.
	TailnetHTTPSCert netd.HTTPSCertFunc
	// TailnetHTTPSOff ignores tailworks.jsonc tailnet.https: the workload
	// is served on http:// only (tw dev, whose local control issues no
	// certificates). Never set it on a host that serves tenants.
	TailnetHTTPSOff bool

	// VM boots microVM sandboxes (hostd/vm: Firecracker on Linux + KVM).
	// Nil means this host offers the process tier only: a microvm
	// deployment is refused (tier_unavailable), never downgraded.
	VM sandbox.Backend
	// ForceProcessTier is explicit local development (tw dev): every
	// workload runs in the process tier whatever its tailworks.jsonc or tenant
	// asks for, and the host registers the process tier only. Never set it
	// on a host that serves real tenants (ADR-032 D5b).
	ForceProcessTier bool

	// WorkerdInspector (tw dev --inspector-port; only with
	// ForceProcessTier) starts each workerd with its V8 inspector on a free
	// 127.0.0.1 port (inspector.go). It gives full control of the isolate:
	// never on a host serving tenants.
	WorkerdInspector bool

	// PyodideDir holds the pinned Pyodide bundles Python Workers run on
	// (pyodide.go; default <DataDir>/pyodide). Missing ones are fetched
	// from PyodideURL ("" = workerd's own source) on the first Python
	// deployment; workerd only reads them.
	PyodideDir string
	PyodideURL string

	// Capacity sets the capacity vector's VM dimensions (nil: measured; see
	// capacity.go). Its nodes are always CapacityNodes; without a microVM
	// backend every VM dimension is 0.
	Capacity *mgmtapi.HostCapacity
	// WorkerdHeapMiB caps each V8 isolate's heap in a workerd, and
	// NodeHeapMiB a Node-tier app's (limits.go; 0 = the default, negative
	// = no cap).
	WorkerdHeapMiB int
	NodeHeapMiB    int

	// VMMemMiB and VMVCPUs are a microVM DO instance's guest size, for usage
	// accounting (defaults 512 and 2, tw-hostd's -vm-mem-mib / -vm-vcpus).
	VMMemMiB, VMVCPUs int
	// ContainerQuota reserves container quota at ctx.container.start()
	// (default: mgmt's /v1/container-reservations through Mgmt; tests: a
	// fake).
	ContainerQuota ContainerQuota
	// VMDebug enables guest diagnostics (connectivity probes; tests).
	VMDebug bool
	// VMVolumeMiB sizes new DO volumes (microVM tier; sparse; default 1024).
	VMVolumeMiB int
	// Sandbox confines process-tier workerds and Node apps: "none"
	// (default), "seatbelt" (macOS), "landlock" (Linux) or "auto".
	Sandbox string
	// SandboxLauncher is the binary that applies Landlock profiles
	// (default: this executable, which imports internal/procsb).
	SandboxLauncher string
	// InsecureSharedHost lets a host without a process sandbox run
	// process-tier workloads of more than one tenant. Without it such a host
	// refuses the second tenant's deployment (typed sandbox_unavailable), and
	// a Node-tier deployment there with egress_unenforced (RT-08: a hostile
	// Node app can open raw sockets past the egress proxy).
	InsecureSharedHost bool
	// AllowExperimentalFlags lets deployments set compatibility flags
	// workerd marks experimental (unsafe_module, rtti_api, precise_timers,
	// …; gen/compatflags.go, #337). Off, such a deployment is refused
	// (experimental_flag). The host advertises it as the
	// experimental-flags feature, so mgmt places such deployments here.
	AllowExperimentalFlags bool
	// HostSockets overrides the Linux risky-host-socket scan
	// (procsb.ScanHostSockets; tests).
	HostSockets func() procsb.HostSockets
	// SandboxDeny are extra paths no workerd may read, write or connect to
	// (tw up: its whole state dir, which holds mgmt's custody and socket).
	SandboxDeny []string
	// NodeBin is the node binary for runtime "node" deployments (default:
	// node from PATH).
	NodeBin string
	// AllowSiblingEgress lets an object's egress reach other objects hosted
	// by this host over the tailnet (a hairpin). Off by default (ADR-019
	// rule 2: same-host object calls go through the platform); single-host
	// tests and demos turn it on so that objects can exercise each other's
	// tailnet-facing authorization (e.g. e2e check (d), RT-01).
	AllowSiblingEgress bool
	// PushListener, when set, also serves the push API (mgmt → hostd:
	// kills, assignment changes) on it: the host's own tsnet node in
	// normal operation. PushPort is the port it listens on; it is sent
	// to mgmt at registration so pushes go to the WhoIs node's address.
	// The unix push socket (local-dev) is always served too. The tsnet
	// listener serves only the pushes, and only to mgmt's node: WhoIs is
	// required with it (the node must carry MgmtTag; hostd pins the node
	// the -mgmt address resolves to). TailnetStatus (optional) resolves a
	// -mgmt MagicDNS name to that node through the netmap.
	PushListener  net.Listener
	WhoIs         WhoIsFunc
	TailnetStatus StatusFunc
	// PushInProcess: this host agent runs inside mgmt's own process
	// (single-process tw up). It registers with the push URL
	// EmbeddedPushURL and mgmt serves its pushes through PushHandler
	// directly; no push listener is opened besides the local socket.
	PushInProcess bool
	// ServiceCheckEvery is how often a workload with a Tailscale Service is
	// health-probed (advertised only while healthy). 0 = 2 s.
	ServiceCheckEvery time.Duration
	PushPort          int

	// Containers boots container guests (ADR-032; hostd/vm on Linux +
	// KVM). Nil: this host offers no containers, and ctx.container.start()
	// fails with tier_unavailable (no fallback, D5).
	Containers sandbox.ContainerBackend
	// Images converts and caches container images (hostd/image).
	Images *image.Store

	// ContainersOff is why tw-hostd offers no containers when it doesn't
	// (found once at startup; tw doctor and tw up show it, containersoff.go).
	ContainersOff string

	// ContainerCapacity optionally bounds this host's running containers
	// further, on top of the capacity vector (Capacity) they share with
	// microVM DO instances (0 = no extra bound).
	ContainerCapacity ContainerCapacity
	// ContainerIdle is the default inactivity timeout (DefaultContainerIdle);
	// setInactivityTimeout changes it per container, up to 6 h.
	ContainerIdle time.Duration
	// ContainerMaxLife is the operator's hard max lifetime of a container
	// (DefaultContainerMaxLife, 24 h).
	ContainerMaxLife time.Duration
	// ContainerGrace caps destroy()'s SIGTERM → SIGKILL grace (10 s).
	ContainerGrace time.Duration
	// ContainerInternet is the sandbox internet path (ADR-032 D4): how a
	// container started with enableInternet reaches the hosts on its
	// workload's egress.internet allowlist. Nil: none; such flows are
	// refused. tw-hostd sets DirectInternet unless -container-internet=off.
	ContainerInternet *ContainerInternet

	// WorkerInternet is the Worker internet egress path
	// (ADR-049, internet.go): how fetch() from Worker
	// and Node app code reaches destinations that are not on the tailnet,
	// subject to each workload's tailnet.egress.internet and the SSRF
	// guard. Nil: none; such fetches are refused. tw-hostd and tw up set
	// DirectInternet unless -worker-internet=off.
	WorkerInternet *ContainerInternet

	// PathEvery is how often hostd measures and reports the tailnet paths
	// to its assignments' placement targets (smart placement, ADR-033).
	// 0 = DefaultPathEvery (60 s); negative disables measuring.
	PathEvery time.Duration
	// CronTimeout bounds one cron trigger's scheduled() run (wall clock;
	// default DefaultCronTimeout, 15 min).
	CronTimeout time.Duration
	// CronClock replaces the wall clock of the cron scheduler's own loop
	// (tests that drive the scheduler's ticks themselves; nil = real time).
	CronClock func() time.Time

	// QueueVisibility is how long a claimed queue batch stays hidden in
	// mgmt (default DefaultQueueVisibility, 60 s; extended while queue()
	// runs). It bounds how soon a crashed consumer's batch comes back.
	QueueVisibility time.Duration
	// Observability (ADR-045, telemetry.go): MetricsListener,
	// when set, serves GET /metrics (the host's own tailnet node; who may
	// scrape is the tailnet policy's call). Telemetry tunes the request
	// records and the OTLP export.
	MetricsListener net.Listener
	Telemetry       TelemetryOptions

	// R2 holds the bytes of R2 buckets (ADR-036; nil: files under
	// <DataDir>/r2). Only hostd talks to it.
	R2 r2.Backend

	// Upgrades (ADR-041, upgrade.go): Version overrides
	// the reported version (tests; "" = internal/version's). SelfUpgrade
	// lets mgmt's upgrade runs install a release on this host and restart
	// it: InstallPrefix is the install.sh prefix to install into, and
	// Restart is called once the new release is installed (tw-hostd exits
	// for its service manager to start the new binary).
	Version       string
	SelfUpgrade   bool
	InstallPrefix string
	Restart       func()
}

// ContainerInternet dials allowlisted internet hosts for containers (and,
// as Config.WorkerInternet, for Workers). The sandbox egress proxy
// (netd/sbxnet) and netd's internet path apply the allowlist and the
// ADR-019 blocks (netd/publicip) to every resolved address before Dial is
// called.
type ContainerInternet struct {
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// HostAddrs are never dialled (nil: the host's interface addresses).
	HostAddrs []netip.Addr
	// Mode names the path for status ("": "direct"); Via names it in
	// audit notes ("": "this host's network"); Problem, when set, says why
	// the path is unusable right now ("" when it is). An exit-node path
	// (exitnode.go) sets all three.
	Mode    string
	Via     string
	Problem func() string
}

// DirectInternet is the host network and resolver (tailworks has no tenant
// exit-node or gateway path yet; RFC §4.8).
func DirectInternet() *ContainerInternet {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &ContainerInternet{Dial: d.DialContext, Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
}

// EmbeddedPushURL is the push URL an in-process host registers with
// (mgmt.EmbeddedPushURL; hostd does not import mgmt).
const EmbeddedPushURL = "inproc://"

// Host is a running tw-hostd.
type Host struct {
	cfg    Config
	log    *slog.Logger
	mgmt   *mgmtapi.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	tenants   map[string]*tenant
	workloads map[string]*workload // tenantID/workload
	objects   map[string]*object   // mgmt object ID
	byNetd    map[string]*object   // netd object ID
	byKey     map[string]*object   // tenantID|namespace|name (live only)
	creating  map[string]*createCall
	// pre-joined node pool (poolnodes.go)
	poolDemand map[string]time.Time // tenant/workload/class → last root create here
	poolBusy   map[string]bool      // a pool node of that class is being created
	instSeq    int
	sandbox    string // resolved Config.Sandbox
	lastHB     time.Time
	fenced     bool

	// children: workerd/Node process groups this hostd started, so the
	// next run can reap any a crash left behind (childreap.go, #432).
	children *childLedger

	// cronLasts: each workload's last cron minute, kept across restarts (cronlast.go, #594).
	cronLasts *cronLastStore

	// consume: queue consumers' context, cancelled on self-fence (queuefence.go).
	consume consumeGate

	// teardown gate (teardown.go): POST /v1/teardown, tw down --purge.
	tearingDown     bool
	createsInFlight int

	capOnce    sync.Once
	capVec     mgmtapi.HostCapacity // capacity(): measured once
	containers containerLedger      // admitted containers and held reservations
	quota      ContainerQuota

	syncMu     sync.Mutex
	syncNow    chan struct{}
	pushSrv    *http.Server // local socket: pushes + operator routes
	tsPushSrv  *http.Server // Config.PushListener: WhoIs-authenticated pushes
	pushAuth   *pushAuth
	pushSock   string
	audit      *auditLog
	public     map[string]*publicState // tenantID/workload (ADR-029)
	pubCreates *publicCreates
	// admits: objKey → expiry of a pending create admission (RT3-06/07):
	// a tailnet request's ticket or an attested object admitted the name;
	// ensure consumes it when it creates. See hostd/gen CreateTicket.
	admits      map[string]time.Time
	wg          sync.WaitGroup
	runDirOwned bool
	ctr         *ctrManager
	nowFn       func() time.Time // wall clock for cron triggers (tests)

	// telemetry.go (ADR-045)
	tel        *telemetry
	metricsSrv *http.Server

	// approvals.go: the Workflows' approval waits on this host.
	apprOnce sync.Once
	appr     *approvalStore

	upg upgrader // upgrade.go

	// tenantpurge.go: deleted tenants' local state.
	tenantPurge tenantPurgeState
}

// DefaultFenceAfter rides out typical mgmt restarts and database failovers;
// MaxFenceAfter keeps it below mgmt's 45 s namespace lease with a 5 s margin
// for heartbeat RTT and clock-rate drift (ADR-028 §3; checked against
// mgmt.NamespaceLeaseTTL in hostd's tests).
const (
	DefaultFenceAfter = 30 * time.Second
	MaxFenceAfter     = 40 * time.Second
)

// New validates cfg and prepares (but does not start) a Host.
func New(cfg Config) (*Host, error) {
	if cfg.HostID == "" || cfg.Mgmt == nil || cfg.DataDir == "" || cfg.WorkerdBin == "" {
		return nil, errors.New("hostd: HostID, Mgmt, DataDir and WorkerdBin are required")
	}
	if cfg.Mgmt.HostID != cfg.HostID {
		cfg.Mgmt = cfg.Mgmt.WithHostID(cfg.HostID) // never write the caller's client: it may be in use (#135)
	}
	if cfg.PushListener != nil && cfg.WhoIs == nil {
		return nil, errors.New("hostd: PushListener requires WhoIs (pushes over the tailnet are authenticated)")
	}
	if cfg.PushInProcess && (cfg.PushListener != nil || cfg.PushPort > 0) {
		return nil, errors.New("hostd: PushInProcess excludes a tailnet push listener")
	}
	if cfg.HeartbeatEvery == 0 {
		cfg.HeartbeatEvery = 3 * time.Second
	}
	if cfg.FenceAfter == 0 {
		cfg.FenceAfter = DefaultFenceAfter
	}
	if cfg.FenceAfter > MaxFenceAfter {
		return nil, fmt.Errorf("hostd: FenceAfter %v must be ≤ %v (below mgmt's namespace lease, so a fenced host has stopped before its lease can be taken over)", cfg.FenceAfter, MaxFenceAfter)
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 30 * time.Second
	}
	if cfg.VMVolumeMiB == 0 {
		cfg.VMVolumeMiB = 1024
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = 10 * time.Second
	}
	if cfg.KillTombstone == 0 {
		cfg.KillTombstone = 10 * time.Second
	}
	if cfg.R2 == nil {
		cfg.R2 = &r2.FS{Root: filepath.Join(cfg.DataDir, "r2")}
	}
	if f, ok := cfg.R2.(*r2.FS); ok {
		// No workload may read bucket bytes but through the binding.
		cfg.SandboxDeny = append(cfg.SandboxDeny, f.Root)
	}
	if cfg.CapacityNodes == 0 {
		total, _ := memInfo()
		cfg.CapacityNodes = DefaultCapacityNodes(total) // capacitynodes.go
	}
	if cfg.VMMemMiB == 0 {
		cfg.VMMemMiB = 512
	}
	if cfg.VMVCPUs == 0 {
		cfg.VMVCPUs = 2
	}
	if cfg.ContainerQuota == nil {
		cfg.ContainerQuota = mgmtQuota{cfg.Mgmt}
	}
	if cfg.PathEvery == 0 {
		cfg.PathEvery = DefaultPathEvery
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	sb, err := ResolveSandbox(cfg.Sandbox)
	if err != nil {
		return nil, err
	}
	if err := checkSandboxLayout(sb, cfg); err != nil { // sandboxlayout.go (#342)
		return nil, err
	}
	// One field per line: features that add a map here should not collide.
	h := &Host{
		cfg:        cfg,
		sandbox:    sb,
		log:        cfg.Logger,
		mgmt:       cfg.Mgmt,
		nowFn:      cfg.CronClock,
		tenants:    map[string]*tenant{},
		workloads:  map[string]*workload{},
		objects:    map[string]*object{},
		byNetd:     map[string]*object{},
		byKey:      map[string]*object{},
		creating:   map[string]*createCall{},
		poolDemand: map[string]time.Time{},
		poolBusy:   map[string]bool{},
		public:     map[string]*publicState{},
		syncNow:    make(chan struct{}, 1),
		quota:      cfg.ContainerQuota,
		containers: containerLedger{held: map[ContainerKey]*ctrEntry{}},
	}
	h.ctr = newCtrManager(h)
	h.tel = newTelemetry(h)
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	h.children = openChildLedger(cfg.DataDir, h.log) // childreap.go: before anything is spawned
	h.cronLasts = openCronLast(cfg.DataDir, h.log)
	h.pubCreates = newPublicCreates(h.publicCreatesPath())
	h.admits = map[string]time.Time{}
	os.Chmod(cfg.DataDir, 0o700)
	if h.cfg.RunDir == "" {
		d, err := os.MkdirTemp("/tmp", "tw-h-")
		if err != nil {
			return nil, err
		}
		h.cfg.RunDir, h.runDirOwned = d, true
	}
	if err := os.MkdirAll(h.cfg.RunDir, 0o700); err != nil {
		return nil, err
	}
	os.Chmod(h.cfg.RunDir, 0o700)
	seed, err := loadOrCreateKey(filepath.Join(cfg.DataDir, "audit.key"))
	if err != nil {
		return nil, err
	}
	key := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	h.audit = newAuditLog(key)
	if err := h.audit.openSpill(filepath.Join(cfg.DataDir, "audit-spill.jsonl"), h.log); err != nil {
		return nil, err
	}
	h.audit.loadResume(filepath.Join(cfg.DataDir, auditHeadFile), h.log)
	if err := h.loadObjects(); err != nil {
		return nil, err
	}
	return h, nil
}

// PushSocket is the push API socket ("unix://" + path is registered as pushURL).
func (h *Host) PushSocket() string { return h.pushSock }

func (h *Host) closePushServers() {
	if h.pushSrv != nil {
		h.pushSrv.Close()
	}
	if h.tsPushSrv != nil {
		h.tsPushSrv.Close()
	}
	if h.metricsSrv != nil {
		h.metricsSrv.Close()
	}
}

// Start registers with mgmt and starts the heartbeat/assignment loop.
func (h *Host) Start(ctx context.Context) error {
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.pushSock = filepath.Join(h.cfg.RunDir, "hostd.sock")
	os.Remove(h.pushSock)
	ln, err := net.Listen("unix", h.pushSock)
	if err != nil {
		return err
	}
	os.Chmod(h.pushSock, 0o600)
	// No sandboxed workload may use the push API (Linux: Landlock < ABI 9
	// can't refuse the connect itself).
	ln = procsb.Guard(ln, nil)
	h.pushSrv = &http.Server{Handler: h.pushAPI(), ReadHeaderTimeout: 10 * time.Second}
	go h.pushSrv.Serve(ln)
	if h.cfg.PushListener != nil {
		// Pushes over the tailnet come from mgmt's node only (H1). Pin it
		// now from the -mgmt address; if that is a MagicDNS name not yet
		// in the netmap, the first tag:tw-mgmt pusher pins it instead.
		h.pushAuth = newPushAuth(h.mgmt.Base, h.cfg.WhoIs, h.cfg.TailnetStatus, h.log)
		if err := h.pushAuth.pin(ctx); err != nil {
			h.log.Warn("push API: mgmt's node not resolved yet; pinning to the first tagged pusher", "err", err)
		} else {
			h.log.Info("push API pinned to mgmt's node", "node", h.pushAuth.Pinned())
		}
		h.tsPushSrv = &http.Server{Handler: h.tsnetPushAPI(), ReadHeaderTimeout: 10 * time.Second}
		go h.tsPushSrv.Serve(h.cfg.PushListener)
	}
	if h.cfg.MetricsListener != nil {
		h.metricsSrv = &http.Server{Handler: h.MetricsHandler(), ReadHeaderTimeout: 10 * time.Second}
		go h.metricsSrv.Serve(h.cfg.MetricsListener)
	}
	if err := h.register(ctx); err != nil {
		h.closePushServers()
		return fmt.Errorf("hostd: register: %w", err)
	}
	h.tel.start()
	h.mu.Lock()
	h.lastHB = time.Now()
	h.mu.Unlock()
	h.wg.Add(3) // loop, serviceLoop, cronLoop
	go h.loop()
	go h.serviceLoop()
	if h.cfg.PathEvery > 0 {
		h.wg.Add(1)
		go h.pathLoop()
	}
	go h.cronLoop()
	h.wg.Add(1)
	go h.queueLoop() // queue consumers (queues.go)
	return nil
}

func (h *Host) register(ctx context.Context) error {
	capVec := h.capacity()
	req := mgmtapi.RegisterRequest{HostID: h.cfg.HostID, CapacityNodes: h.cfg.CapacityNodes, Tiers: h.tiers(),
		Arch: runtime.GOARCH, Capacity: &capVec,
		AuditPubKey:        base64.StdEncoding.EncodeToString(h.audit.key.Public().(ed25519.PublicKey)),
		RegistrationSecret: h.cfg.RegistrationSecret,
		Version:            h.version(), OS: runtime.GOOS, Protocols: version.Protocols(helperapi.Version), Features: h.features()}
	switch {
	case h.cfg.PushInProcess:
		// embedded in mgmt's process: pushes are served through PushHandler.
		req.PushURL = EmbeddedPushURL
	case h.cfg.PushPort > 0:
		// tsnet transport: mgmt pushes to this host's node at PushPort.
		req.PushPort = h.cfg.PushPort
	default:
		// local-dev: the unix push socket.
		req.PushURL = "unix://" + h.pushSock
	}
	reg, err := h.mgmt.RegisterHost(ctx, req)
	if err == nil && h.audit.resume(reg.AuditHead, h.log) { // auditresume.go
		h.log.Info("audit chain continued from mgmt's head", "head", reg.AuditHead)
	}
	return err
}

// tiers lists what this host offers: the process tier always, the microVM
// tier with a backend (not under ForceProcessTier), and "containers" when
// it can run them (ADR-032).
func (h *Host) tiers() []string {
	tiers := []string{"process"}
	if h.cfg.VM != nil && !h.cfg.ForceProcessTier {
		tiers = append(tiers, "microvm")
	}
	if h.containersOffered() {
		tiers = append(tiers, "containers")
	}
	return tiers
}

// Close stops every workload and node (node state is kept for rejoin).
func (h *Host) Close() error {
	cl := newCloseLog(h.log) // closelog.go
	defer cl.done()
	if h.cancel != nil {
		h.cancel()
	}
	h.wg.Wait()
	cl.lap("loops")
	h.closePushServers()
	cl.lap("push")
	h.mu.Lock()
	wls := make([]*workload, 0, len(h.workloads))
	for _, w := range h.workloads {
		wls = append(wls, w)
	}
	ts := make([]*tenant, 0, len(h.tenants))
	for _, t := range h.tenants {
		ts = append(ts, t)
	}
	h.mu.Unlock()
	h.ctr.destroyWhere(func(ctrKey) bool { return true }, "host shutdown", 0)
	for _, w := range wls {
		w.stopAll()
	}
	cl.lap("workloads")
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func() { defer wg.Done(); t.reg.Close() }()
	}
	wg.Wait()
	cl.lap("nodes")
	h.tel.close()
	h.flushAudit(context.Background())
	if err := h.audit.saveHead(filepath.Join(h.cfg.DataDir, auditHeadFile)); err != nil { // auditresume.go
		h.log.Warn("audit: saving the chain head failed; the next start begins a new chain segment", "err", err)
	}
	h.saveObjects()
	if h.runDirOwned {
		os.RemoveAll(h.cfg.RunDir)
	}
	return nil
}

// TriggerSync asks the loop to poll assignments now.
func (h *Host) TriggerSync() {
	select {
	case h.syncNow <- struct{}{}:
	default:
	}
}

func (h *Host) loop() {
	defer h.wg.Done()
	t := time.NewTicker(h.cfg.HeartbeatEvery)
	defer t.Stop()
	h.tick()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
		case <-h.syncNow:
		}
		h.tick()
	}
}

func (h *Host) tick() {
	defer h.maintainPools()
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	// Freshness counts from when the heartbeat left, not when its answer came
	// back: an answer that waited out a stall says nothing about now (#501).
	sent := time.Now()
	if err := h.heartbeat(ctx); err != nil {
		if h.ctx.Err() != nil {
			return
		}
		h.log.Warn("heartbeat failed", "err", err)
		if e, ok := mgmtapi.AsError(err); ok && e.Status == 404 {
			h.register(ctx)
		}
		h.mu.Lock()
		stale := h.cfg.FenceAfter > 0 && time.Since(h.lastHB) > h.cfg.FenceAfter && !h.fenced
		if stale {
			h.fenced = true
		}
		h.mu.Unlock()
		if stale {
			h.log.Error("no successful heartbeat; self-fencing stateful workloads (stateless keep serving)", "after", h.cfg.FenceAfter)
			h.stopConsumers() // queuefence.go: no claim while fenced (#140)
			h.fenceStateful("heartbeat lost")
		}
		return
	}
	h.mu.Lock()
	h.lastHB, h.fenced = sent, false
	h.mu.Unlock()
	if err := h.syncAssignments(ctx); err != nil && h.ctx.Err() == nil {
		h.log.Warn("assignment sync failed", "err", err)
	}
	h.flushAudit(ctx)
	h.retryReleases(ctx)
}

func (h *Host) heartbeat(ctx context.Context) error {
	resp, err := h.mgmt.Heartbeat(ctx, h.cfg.HostID, h.heartbeatRequest())
	if err != nil {
		return err
	}
	for _, id := range resp.RevokeObjects {
		go h.killLocal(id, "revoked by mgmt")
	}
	h.purgeTenants(resp.PurgeTenants) // tenantpurge.go
	if len(resp.Revoke) > 0 {
		h.revoke(resp.Revoke)
	}
	return nil
}

// heartbeatRequest is the heartbeat body: usage, live nodes, deployment
// statuses and the leases of the live deployments.
func (h *Host) heartbeatRequest() mgmtapi.HeartbeatRequest {
	usage := h.usage()
	req := mgmtapi.HeartbeatRequest{Leases: []mgmtapi.Lease{}, Usage: &usage, Containers: h.containers.running()}
	req.PurgedTenants = h.tenantPurge.done() // tenantpurge.go
	h.mu.Lock()
	for _, o := range h.objects {
		if o.Status == statusReady {
			req.LiveNodes++
		}
	}
	keys := make([]string, 0, len(h.workloads))
	for k := range h.workloads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	wls := make([]*workload, 0, len(keys))
	for _, k := range keys {
		wls = append(wls, h.workloads[k])
	}
	h.mu.Unlock()
	for _, w := range wls {
		st := w.statusSnapshot()
		if st.DeploymentID == "" {
			continue
		}
		req.Deployments = append(req.Deployments, st)
		if liveStatus(st.Status) { // restart.go: a crash-looping workload keeps its lease
			req.Leases = append(req.Leases, mgmtapi.Lease{Namespace: w.lease(), Epoch: w.epochNow()})
		}
	}
	return req
}

// revoke stops serving namespaces whose lease this host lost — unless mgmt
// re-assigned the workload to this host at a newer epoch (a redeploy), in
// which case the new epoch is adopted.
func (h *Host) revoke(nss []string) {
	if as, err := h.mgmt.Assignments(h.ctx, h.cfg.HostID); err == nil {
		h.mu.Lock()
		for _, a := range as {
			if w := h.workloads[a.TenantID+"/"+a.Workload]; w != nil {
				w.mu.Lock()
				if a.Epoch > w.epoch {
					w.epoch = a.Epoch
				}
				w.mu.Unlock()
			}
		}
		h.mu.Unlock()
		var keep []string
		for _, n := range nss {
			adopted := false
			for _, a := range as {
				if n == mgmtapi.LeaseName(a.TenantID, a.Workload) || n == a.Workload {
					adopted = true
				}
			}
			if !adopted {
				keep = append(keep, n)
			}
		}
		nss = keep
		if len(nss) == 0 {
			h.TriggerSync()
			return
		}
	}
	set := map[string]bool{}
	for _, n := range nss {
		set[n] = true
	}
	h.mu.Lock()
	var hit []*workload
	for _, w := range h.workloads {
		if set[w.lease()] || set[w.name] {
			hit = append(hit, w)
		}
	}
	h.mu.Unlock()
	for _, w := range hit {
		h.log.Warn("lease revoked; fencing workload", "workload", w.name, "namespaces", nss)
		h.undeploy(w, "lease revoked")
	}
}

// fenceStateful stops serving every workload whose assignment says fence
// (it has Durable Objects that must not run on two hosts). They are
// redeployed by the next successful assignment sync.
func (h *Host) fenceStateful(reason string) {
	for _, w := range h.fenceTargets() {
		h.undeploy(w, reason)
	}
}

// fenceTargets are the workloads heartbeat loss must stop.
func (h *Host) fenceTargets() []*workload {
	h.mu.Lock()
	defer h.mu.Unlock()
	wls := make([]*workload, 0, len(h.workloads))
	for _, w := range h.workloads {
		if w.fences() {
			wls = append(wls, w)
		}
	}
	return wls
}

func (h *Host) syncAssignments(ctx context.Context) error {
	h.syncMu.Lock()
	defer h.syncMu.Unlock()
	as, err := h.mgmt.Assignments(ctx, h.cfg.HostID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, a := range as {
		key := a.TenantID + "/" + a.Workload
		want[key] = true
		h.mu.Lock()
		w := h.workloads[key]
		if w == nil {
			w = &workload{h: h, tenantID: a.TenantID, name: a.Workload}
			h.workloads[key] = w
		}
		h.mu.Unlock()
		w.mu.Lock()
		w.placementTargets = a.PlacementTargets
		w.mu.Unlock()
		if w.wantDeploy(a) {
			go h.deploy(w, a)
		} else {
			go h.applyPublic(w, a.Public)
		}
	}
	h.mu.Lock()
	var gone []*workload
	for k, w := range h.workloads {
		if !want[k] {
			gone = append(gone, w)
		}
	}
	h.mu.Unlock()
	for _, w := range gone {
		if st := w.statusSnapshot().Status; st == "active" || st == statusCrashloop {
			h.undeploy(w, "no longer assigned")
		}
	}
	h.forgetUnassignedTombstones(want) // unassignedtombstones.go (#364)
	return nil
}

// ---- helpers ----

func loadOrCreateKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	rand.Read(b)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

func newTraceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
