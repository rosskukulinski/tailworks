package mgmt

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rosskukulinski/twd/internal/features"
	"github.com/rosskukulinski/twd/internal/version"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/twd/internal/container"
	"github.com/rosskukulinski/twd/internal/oci"
	"github.com/rosskukulinski/twd/internal/registry"
	"github.com/rosskukulinski/twd/internal/tsapi"
	"github.com/rosskukulinski/twd/internal/tssvc"
)

// Config configures a Mgmt.
type Config struct {
	Store   Store
	Custody Custody
	Broker  *Broker
	// Policy fetches tenant policies; default: GetPolicy through the broker.
	Policy       PolicySource
	PolicyMaxAge time.Duration
	Clock        Clock
	Log          *slog.Logger
	// ReplicaID is this replica's stable identity (twd-mgmt keeps it in
	// <state-dir>/replica.id). Empty: a fresh per-process ID (tests).
	ReplicaID string
	// IDSecret keys the object-name HMAC (RFC §4.3). Required.
	IDSecret []byte
	// ReapInterval (default 10s); RevocationWindow is how long a kill is
	// repeated in heartbeats (default 10 min).
	ReapInterval     time.Duration
	RevocationWindow time.Duration
	// MintRetries before an object is marked failed (default 5).
	MintRetries int
	// DeviceDeleteDelay defers kill tier 2 (device delete) after the tier-1
	// push, so the killed node's tombstone on the host can answer NEW
	// connections with a typed object_killed instead of peers black-holing
	// once control drops it from their netmaps. 0 deletes immediately.
	// Pending deletes run at once on Close.
	DeviceDeleteDelay time.Duration
	// PushTimeout bounds one mgmt → hostd push (default 2 s).
	PushTimeout time.Duration
	// TailnetDial dials hosts' tsnet nodes for pushes (tsnet.Server.Dial);
	// nil uses the plain network (tests).
	TailnetDial func(ctx context.Context, network, addr string) (net.Conn, error)
	// OperatorTailnet is mgmt's own tailnet (MagicDNS suffix): the tenant
	// whose tailnet this is mints host join keys (POST /v1/hosts).
	OperatorTailnet string
	// OperatorControlURL is passed to hosts with their join key.
	OperatorControlURL string
	// Registry (ADR-032 D3): RegistryDir holds the OCI registry's blobs
	// ("" = no registry: container images are not pinned at deploy).
	// RegistryUpstream returns the client mgmt mirrors an external image
	// through (nil: oci.RegistryBase over HTTPS for every host, except the
	// RegistryInsecure hosts, over plain HTTP). NoMirror refuses external
	// images (only twd.local refs and public bases deploy). ImageAliases
	// default to registry.DefaultAliases.
	RegistryDir      string
	RegistryUpstream func(host string) *oci.Client
	RegistryInsecure []string
	NoMirror         bool
	ImageAliases     []registry.Alias
	// WarmKeys caps the warm-key pool per (tenant, tag set, ephemeral):
	// pre-minted single-use keys that let a create skip CreateKey
	// (warmkeys.go). 0 disables the pool.
	WarmKeys int
	// WarmKeyTTL is a pooled key's expiry at the API (default 30 min); it is
	// revoked unused WarmKeyTakeMargin + 30 s before that.
	WarmKeyTTL time.Duration
	// WarmPoolInterval is how often pools are refilled and expired
	// (default 2 s; a take also triggers a refill at once).
	WarmPoolInterval time.Duration

	// Version is this replica's twd-mgmt version ("" = internal/version's),
	// checked against hosts and other replicas (versions.go). ReleaseDir
	// holds imported release artifacts ("" = none: hosts can't upgrade
	// themselves through this replica; upgrades.go).
	Version    string
	ReleaseDir string
}

// Mgmt is twd-mgmt's core: tenants, deployments, objects, the mint broker
// and its per-tenant throttle, spawn checks, kill, the reaper and audit.
type Mgmt struct {
	cfg      Config
	store    Store
	custody  Custody
	broker   *Broker
	policies *PolicyCache
	clock    Clock
	log      *slog.Logger

	mu           sync.Mutex
	throttles    map[string]*Throttle
	queues       map[string]chan string
	queued       map[string]bool // object IDs in this replica's mint queues (dedupe for the pending sweep)
	replicaID    string          // stable replica ID (Config.ReplicaID): mint claims, the replica registry
	startedAt    int64
	replicaMsg   string // replica-registry conflict ("" = ok)
	liveReplicas int
	waiters      map[string]chan struct{}
	spawnMu      map[string]*sync.Mutex // per tenant: serialises budget checks
	pushURLs     map[string]string      // hostID → push target cache (unix://, http://node:port, inproc://)
	pending      map[string]*pendingDelete
	// inprocPush are the push handlers of hosts embedded in this process
	// (single-process twd up): hostID → the host agent's push API.
	inprocPush map[string]http.Handler
	// reg is the OCI registry (nil without Config.RegistryDir).
	reg      *registry.Registry
	resolver *registry.Resolver
	// noDeviceScope: tenants whose credential got a 403 on a device delete
	// (an OAuth client without devices:core); kills then warn up front.
	noDeviceScope map[string]bool
	deviceKept    map[string]bool               // killed objects whose device delete was refused (403)
	oboKeys       map[string]ed25519.PrivateKey // tenant → OBO root key (loaded from custody)
	clockOK       bool                          // last checkClock result (logs on change)
	poolKick      chan struct{}                 // wakes the warm-key loop
	poolPause     map[string]time.Time          // tenant → no refills until (after an error)
	poolRetried   time.Time                     // last retry of failed warm-key revocations
	volMu         sync.Mutex                    // VolumeKey create-if-absent
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup

	upgradeKick chan struct{} // wakes the upgrade-run driver (upgrades.go)

	// notify.go: the tenants' notification webhooks.
	notifs notifyState
}

// New returns a Mgmt; call Start to run the mint workers and reaper.
func New(cfg Config) (*Mgmt, error) {
	if cfg.Store == nil || cfg.Custody == nil || len(cfg.IDSecret) < 16 {
		return nil, errors.New("mgmt: Store, Custody and a ≥16-byte IDSecret are required")
	}
	if cfg.Broker == nil {
		cfg.Broker = &Broker{}
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.ReapInterval == 0 {
		cfg.ReapInterval = 10 * time.Second
	}
	if cfg.RevocationWindow == 0 {
		cfg.RevocationWindow = 10 * time.Minute
	}
	if cfg.MintRetries == 0 {
		cfg.MintRetries = 5
	}
	m := &Mgmt{cfg: cfg, store: cfg.Store, custody: cfg.Custody, broker: cfg.Broker, clock: cfg.Clock, log: cfg.Log,
		throttles: map[string]*Throttle{}, queues: map[string]chan string{}, queued: map[string]bool{}, replicaID: cfg.ReplicaID, waiters: map[string]chan struct{}{},
		spawnMu: map[string]*sync.Mutex{}, pushURLs: map[string]string{}, inprocPush: map[string]http.Handler{},
		upgradeKick: make(chan struct{}, 1),
		pending:     map[string]*pendingDelete{}, noDeviceScope: map[string]bool{}, deviceKept: map[string]bool{},
		poolKick: make(chan struct{}, 1), poolPause: map[string]time.Time{}}
	if m.cfg.WarmKeyTTL == 0 {
		m.cfg.WarmKeyTTL = DefaultWarmKeyTTL
	}
	if m.cfg.WarmKeyTTL < 2*(WarmKeyTakeMargin+warmKeySafety) {
		return nil, fmt.Errorf("mgmt: WarmKeyTTL %v is too short (≥ %v)", m.cfg.WarmKeyTTL, 2*(WarmKeyTakeMargin+warmKeySafety))
	}
	if m.cfg.WarmPoolInterval == 0 {
		m.cfg.WarmPoolInterval = defaultWarmTick
	}
	if cfg.PushTimeout == 0 {
		m.cfg.PushTimeout = 2 * time.Second
	}
	src := cfg.Policy
	if src == nil {
		src = func(ctx context.Context, t Tenant, etag string) ([]byte, string, bool, error) {
			api, err := m.broker.API(ctx, t, purposePolicy)
			if err != nil {
				return nil, "", false, err
			}
			return api.GetPolicy(ctx, etag)
		}
	}
	m.policies = &PolicyCache{Fetch: src, MaxAge: cfg.PolicyMaxAge, Clock: cfg.Clock}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	if cfg.RegistryDir != "" {
		if err := m.initRegistry(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// LoadOrCreateSecret reads a 32-byte secret file (0600), creating it.
func LoadOrCreateSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		rand.Read(b)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		return b, os.WriteFile(path, b, 0o600)
	}
	return b, err
}

// Start re-queues pending objects and runs the reaper until Close.
func (m *Mgmt) Start() error {
	if err := m.sweepPending(m.ctx); err != nil {
		return err
	}
	if m.replicaID == "" {
		m.replicaID = newID("rp_")
	}
	if msg, err := m.registerReplica(m.ctx); err != nil {
		return fmt.Errorf("replica registry: %w", err)
	} else if msg != "" {
		m.unregisterReplica(m.ctx)
		return fmt.Errorf("refusing to start: %s", msg)
	}
	m.clockOK = true
	m.checkClock()
	if m.warmStore() != nil {
		m.wg.Add(1)
		go m.poolLoop()
	}
	m.wg.Add(1)
	go m.upgradeLoop() // upgrades.go
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-m.clock.After(m.cfg.ReapInterval):
				m.checkClock()
				if _, err := m.registerReplica(m.ctx); err != nil {
					m.log.Warn("replica registry", "err", err)
				}
				m.sweepDeviceDeletes(m.ctx)
				m.drainAuditOutbox(m.ctx)
				if err := m.sweepPending(m.ctx); err != nil {
					m.log.Warn("pending sweep", "err", err)
				}
				if n, err := m.Reap(m.ctx); err != nil {
					m.log.Warn("reaper", "err", err)
				} else if n > 0 {
					m.log.Info("reaper killed objects", "n", n)
				}
			}
		}
	}()
	return nil
}

// Close stops the workers.
func (m *Mgmt) Close() {
	m.FlushDeletes() // never leave a killed object's device behind
	if m.startedAt != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		m.unregisterReplica(ctx)
		cancel()
	}
	m.cancel()
	m.wg.Wait()
}

// now is the shared time source: the store's clock when the store provides
// one (Postgres server time; ADR-028 §1), else the configured clock. Every
// lease, liveness and expiry decision goes through it, so replicas whose
// wall clocks disagree still agree on who holds what.
func (m *Mgmt) now() time.Time {
	if sc, ok := m.store.(StoreClock); ok {
		return sc.Now()
	}
	return m.clock.Now()
}

func newID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func traceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// audit appends to the mgmt chain; failures are logged (and surfaced by
// verify), never swallowed silently.

// ---- tenants ----

// TenantRequest is POST /v0/tenants.
type TenantRequest struct {
	ID                     string `json:"tenantID,omitempty"`
	Tailnet                string `json:"tailnet"`
	ControlURL             string `json:"controlURL,omitempty"`
	MintMode               string `json:"mintMode"`
	APIKeyFile             string `json:"apiKeyFile,omitempty"`
	WIFClientID            string `json:"wifClientID,omitempty"`
	WIFLifecycleClientID   string `json:"wifLifecycleClientID,omitempty"`
	OAuthClientID          string `json:"oauthClientID,omitempty"`
	OAuthClientFile        string `json:"oauthClientFile,omitempty"`
	RegistrationsPerMinute int    `json:"registrationsPerMinute,omitempty"`
	MaxObjects             int    `json:"maxObjects,omitempty"`
	// Container quotas (ADR-032 D9); 0 = the defaults.
	MaxContainers      int     `json:"maxContainers,omitempty"`
	MaxContainerMemMiB int     `json:"maxContainerMemMiB,omitempty"`
	MaxContainerVCPU   float64 `json:"maxContainerVCPU,omitempty"`
	// PublicIngress is the ADR-029 gate: "allow" or "deny" (default).
	PublicIngress string `json:"publicIngress,omitempty"`
}

func (m *Mgmt) CreateTenant(ctx context.Context, r TenantRequest) (Tenant, error) {
	if r.Tailnet == "" || r.Tailnet == "-" {
		return Tenant{}, errf(400, "bad_request", "tailnet must be named explicitly")
	}
	if err := validPublicIngress(r.PublicIngress); err != nil {
		return Tenant{}, err
	}
	switch r.MintMode {
	case MintAPIKey:
		if r.APIKeyFile == "" {
			return Tenant{}, errf(400, "bad_request", "mintMode apikey needs apiKeyFile")
		}
		if !filepath.IsAbs(r.APIKeyFile) {
			return Tenant{}, errf(400, "bad_request", "apiKeyFile must be an absolute path")
		}
		// Same rule as the OAuth client file: a regular 0600 file, no symlink.
		if f, err := tsapi.OpenSecretFile(r.APIKeyFile); err != nil {
			return Tenant{}, errf(400, "bad_request", "apiKeyFile: %v", err)
		} else {
			f.Close()
		}
	case MintWIF:
		if r.WIFClientID == "" {
			return Tenant{}, errf(400, "bad_request", "mintMode wif needs wifClientID")
		}
		if m.broker.Issuer == nil {
			return Tenant{}, errf(400, "bad_request", "mintMode wif needs twd-mgmt to run with an issuer (-issuer-url)")
		}
	case MintOAuth:
		id, err := checkOAuthFile(r.OAuthClientFile, r.OAuthClientID)
		if err != nil {
			return Tenant{}, err
		}
		r.OAuthClientID = id
	case MintHeadscale:
		if err := checkHeadscaleTenant(r.APIKeyFile, r.ControlURL, r.PublicIngress); err != nil {
			return Tenant{}, err
		}
	default:
		return Tenant{}, errf(400, "bad_request", "mintMode must be oauth, apikey, wif or headscale")
	}
	t := Tenant{ID: r.ID, Tailnet: r.Tailnet, ControlURL: r.ControlURL, MintMode: r.MintMode, APIKeyFile: r.APIKeyFile,
		WIFClientID: r.WIFClientID, WIFLifecycleClientID: r.WIFLifecycleClientID, OAuthClientID: r.OAuthClientID, OAuthClientFile: r.OAuthClientFile,
		RegistrationsPerMinute: r.RegistrationsPerMinute, MaxObjects: r.MaxObjects, PublicIngress: r.PublicIngress, CreatedAt: m.now().UnixMilli(),
		MaxContainers: r.MaxContainers, MaxContainerMemMiB: r.MaxContainerMemMiB, MaxContainerVCPU: r.MaxContainerVCPU}
	t.containerDefaults()
	if t.ID == registry.PublicNamespace {
		return Tenant{}, errf(400, "bad_request", "tenant ID %q is reserved (the registry's operator namespace)", t.ID)
	}
	if t.ID == "" {
		t.ID = newID("tn_")[:11]
	}
	if t.RegistrationsPerMinute <= 0 {
		t.RegistrationsPerMinute = DefaultRegistrationsPerMinute
	}
	if t.MaxObjects <= 0 {
		t.MaxObjects = DefaultMaxObjects
	}
	if err := m.store.CreateTenant(ctx, t); err != nil {
		return Tenant{}, err
	}
	m.audit(ctx, AuditRecord{TenantID: t.ID, Kind: "tenant_create"}, map[string]any{"tailnet": t.Tailnet, "mintMode": t.MintMode, "publicIngress": t.PublicIngress})
	return t, nil
}

// IssuerInfo is what a tenant admin needs to create the WIF trust credentials.
func (m *Mgmt) IssuerInfo(t Tenant) map[string]any {
	if m.broker.Issuer == nil {
		return nil
	}
	return map[string]any{"issuer": m.broker.Issuer.TenantIssuer(t.ID), "mintSubject": MintSubject(t.ID),
		"lifecycleSubject": LifecycleSubject(t.ID)}
}

// WIFUpdate is PUT /v0/tenants/{id}/wif: switch a tenant to (or re-key)
// WIF mint mode with new public client IDs. Only client IDs are stored.
type WIFUpdate struct {
	WIFClientID          string `json:"wifClientID"`
	WIFLifecycleClientID string `json:"wifLifecycleClientID,omitempty"`
}

// UpdateTenantWIF sets the tenant's WIF client IDs and makes wif the mint
// mode. The cached access tokens of the old IDs are dropped.
func (m *Mgmt) UpdateTenantWIF(ctx context.Context, tenantID string, u WIFUpdate) (Tenant, error) {
	t, err := m.store.GetTenant(ctx, tenantID)
	if err != nil {
		return Tenant{}, err
	}
	if u.WIFClientID == "" {
		return Tenant{}, errf(400, "bad_request", "wifClientID required")
	}
	if m.broker.Issuer == nil {
		return Tenant{}, errf(400, "bad_request", "mintMode wif needs twd-mgmt to run with an issuer (-issuer-url)")
	}
	t.MintMode, t.WIFClientID, t.WIFLifecycleClientID = MintWIF, u.WIFClientID, u.WIFLifecycleClientID
	if err := m.store.UpdateTenant(ctx, t); err != nil {
		return Tenant{}, err
	}
	m.broker.Forget(t.ID)
	m.audit(ctx, AuditRecord{TenantID: t.ID, Kind: "tenant_update"}, map[string]any{"tailnet": t.Tailnet, "mintMode": t.MintMode, "wifUpdated": true,
		"lifecycle": t.WIFLifecycleClientID != ""})
	return t, nil
}

// WIFCheckResult is POST /v0/tenants/{id}/wif-check: does each trust
// credential exchange, and can the lifecycle one read the policy.
type WIFCheckResult struct {
	MintMode string `json:"mintMode"`
	// Mint: the mint credential's token exchange succeeded (its auth_keys
	// scope is only proven by the first real mint).
	Mint      bool   `json:"mint"`
	MintError string `json:"mintError,omitempty"`
	// Lifecycle: "ok", "absent" (no lifecycle client ID: kill tier 2, the
	// reaper's device deletes, spawn checks and deploy preflight are off)
	// or "error".
	Lifecycle      string `json:"lifecycle"`
	LifecycleError string `json:"lifecycleError,omitempty"`
	// PolicyRead: the lifecycle credential read the tailnet policy
	// (policy_file:read), which preflight and spawn checks need.
	PolicyRead      bool   `json:"policyRead"`
	PolicyReadError string `json:"policyReadError,omitempty"`
	Issuer          string `json:"issuer,omitempty"`
}

// WIFCheck exercises a wif tenant's trust credentials without minting.
func (m *Mgmt) WIFCheck(ctx context.Context, tenantID string) (WIFCheckResult, error) {
	t, err := m.store.GetTenant(ctx, tenantID)
	if err != nil {
		return WIFCheckResult{}, err
	}
	res := WIFCheckResult{MintMode: t.MintMode, Lifecycle: "absent"}
	if t.MintMode != MintWIF {
		return res, errf(400, "bad_request", "tenant %s is in mint mode %s, not wif", t.ID, t.MintMode)
	}
	res.Issuer = m.broker.Issuer.TenantIssuer(t.ID)
	m.broker.Forget(t.ID) // a fresh exchange, not a cached token
	if _, err := m.broker.API(ctx, t, purposeMint); err != nil {
		res.MintError = err.Error()
	} else {
		res.Mint = true
	}
	if t.WIFLifecycleClientID != "" {
		api, err := m.broker.API(ctx, t, purposeLifecycle)
		if err != nil {
			res.Lifecycle, res.LifecycleError = "error", err.Error()
		} else {
			res.Lifecycle = "ok"
			if _, _, _, err := api.GetPolicy(ctx, ""); err != nil {
				res.PolicyReadError = err.Error()
			} else {
				res.PolicyRead = true
				m.policies.Invalidate(t.ID)
			}
		}
	}
	return res, nil
}

// ---- hosts ----

// RegisterRequest is POST /v0/hosts/register.
type RegisterRequest struct {
	HostID        string   `json:"hostID"`
	CapacityNodes int      `json:"capacityNodes"`
	Tiers         []string `json:"tiers"`
	AuditPubKey   string   `json:"auditPubKey,omitempty"`
	Pool          string   `json:"pool,omitempty"`
	// RegistrationSecret is the one-time secret from POST /v1/hosts; a
	// tailnet node must present it to bind itself to a pending host (M1).
	RegistrationSecret string `json:"registrationSecret,omitempty"`
	// PushURL is where mgmt pushes kills and assignment changes on the
	// local-dev socket (unix:///… only), or EmbeddedPushURL for the host
	// agent embedded in mgmt's own process (single-process twd up). Over
	// the tailnet hosts give PushPort instead: mgmt pushes to the WhoIs
	// node's address.
	PushURL  string `json:"pushURL,omitempty"`
	PushPort int    `json:"pushPort,omitempty"`
	// Arch (GOARCH) and Capacity, the capacity vector (ADR-032 D9).
	Arch     string        `json:"arch,omitempty"`
	Capacity *HostCapacity `json:"capacity,omitempty"`

	// Version, Protocols and Features: what the host runs and offers
	// (ADR-041 §7; versions.go).
	Version   string         `json:"version,omitempty"`
	OS        string         `json:"os,omitempty"`
	Protocols map[string]int `json:"protocols,omitempty"`
	Features  []string       `json:"features,omitempty"`
}

// EmbeddedPushURL is the push "URL" of a host agent that runs inside
// mgmt's own process (single-process `twd up`): pushes are served through
// the handler given to SetInProcessPush, nothing is dialled. A
// registration naming it is accepted only when the request's context
// carries the embedded-host marker (WithEmbeddedHost), which only the
// in-process transport sets: no listener (tsnet or unix socket) attaches it.
const EmbeddedPushURL = "inproc://"

type embeddedHostKey struct{}

// WithEmbeddedHost marks ctx as a request from the host agent embedded in
// this process (internal/inproc sets it next to the host principal). The
// marker cannot arrive over the wire: it is a context value, and the
// listeners' auth wrappers attach only a Principal.
func WithEmbeddedHost(ctx context.Context, hostID string) context.Context {
	return context.WithValue(ctx, embeddedHostKey{}, hostID)
}

// embeddedHostOf returns the embedded host ID of ctx ("" if not embedded).
func embeddedHostOf(ctx context.Context) string {
	id, _ := ctx.Value(embeddedHostKey{}).(string)
	return id
}

// IsEmbeddedHost reports whether ctx is a request from the embedded host.
func IsEmbeddedHost(ctx context.Context) bool { return embeddedHostOf(ctx) != "" }

// RegisterHost binds the host record to the calling node (nodeID, nodeAddr
// are the WhoIs identity and tailnet IP of a tsnet host; "" on the
// local-dev socket). It returns true when the record is new.
func (m *Mgmt) RegisterHost(ctx context.Context, r RegisterRequest, nodeID, nodeAddr string) (Host, bool, error) {
	if r.HostID == "" {
		return Host{}, false, errf(400, "bad_request", "hostID required")
	}
	if err := version.CheckHost(r.Version, m.Version()); err != nil {
		m.audit(ctx, AuditRecord{Kind: "deny", Code: "version_skew", ActingFor: "host:" + r.HostID},
			map[string]any{"host": r.HostID, "hostVersion": r.Version, "mgmtVersion": m.Version()})
		return Host{}, false, errf(409, "version_skew", "%v", err)
	}
	if r.Capacity != nil { // capacityNodes is the node dimension
		c := *r.Capacity
		c.Nodes, r.Capacity = r.CapacityNodes, &c
	}
	old, err := m.store.GetHost(ctx, r.HostID)
	created := errors.Is(err, ErrNotFound)
	if err != nil && !created {
		return Host{}, false, err
	}
	secretHash := old.RegistrationSecretHash
	embedded := r.PushURL == EmbeddedPushURL
	if embedded {
		// The host agent embedded in this process: no node to bind, no
		// registration secret (it is the same process). Only the in-process
		// transport can present the marker; a tsnet or local-dev caller
		// naming inproc:// is refused.
		switch {
		case embeddedHostOf(ctx) != r.HostID || nodeID != "":
			m.audit(ctx, AuditRecord{Kind: "deny", Code: "host_identity_mismatch", ActingFor: "host:" + r.HostID},
				map[string]any{"host": r.HostID, "node": nodeID, "reason": "push URL inproc:// from a caller that is not the embedded host"})
			return Host{}, false, errf(403, "host_identity_mismatch", "host %s: push URL %s is reserved for the host agent embedded in twd-mgmt's process", r.HostID, EmbeddedPushURL)
		case old.NodeID != "":
			return Host{}, false, errf(403, "host_identity_mismatch", "host %s is registered to tailnet node %s; `twd host rm %s` before running it embedded", r.HostID, old.NodeID, r.HostID)
		}
	}
	switch {
	case nodeID == "":
		// No node to bind: the --local-dev socket (Twd-Host-ID), where the
		// record may still be created implicitly. A bound node ID is kept.
		nodeID = old.NodeID
	case created:
		// M1: over the tailnet a host ID exists only if an admin added it.
		return Host{}, false, errf(403, "host_not_registered", "host %s was never added: `twd host add %s` mints its join key and registration secret", r.HostID, r.HostID)
	case old.NodeID == "":
		// First bind of an admin-created host: the join key proves
		// tag:twd-host, the one-time secret proves this host ID.
		if secretHash == "" || r.RegistrationSecret == "" ||
			subtle.ConstantTimeCompare([]byte(hashRegistrationSecret(r.RegistrationSecret)), []byte(secretHash)) != 1 {
			m.audit(ctx, AuditRecord{Kind: "deny", Code: "host_identity_mismatch", ActingFor: "host:" + r.HostID},
				map[string]any{"host": r.HostID, "node": nodeID, "reason": "registration secret missing or wrong"})
			return Host{}, false, errf(403, "host_identity_mismatch", "host %s: the registration secret is missing or wrong (`twd host add %s` mints a new join key and secret)", r.HostID, r.HostID)
		}
		secretHash = "" // used once
		created = true  // the record existed (pending); this is the host's first registration
	case old.NodeID != nodeID:
		return Host{}, false, errf(403, "host_identity_mismatch", "host %s is registered to another node", r.HostID)
	}
	// M4: the audit key is pinned at the first registration that carries
	// one; later registrations may repeat or omit it, never change it
	// (rotation is twd host rm + twd host add).
	auditKey := r.AuditPubKey
	if old.AuditPubKey != "" {
		if auditKey != "" && auditKey != old.AuditPubKey {
			return Host{}, false, errf(403, "host_identity_mismatch", "host %s: auditPubKey is pinned to the key of its first registration (rotate with twd host rm %s --drain, then twd host add)", r.HostID, r.HostID)
		}
		auditKey = old.AuditPubKey
	}
	target := ""
	switch {
	case embedded:
		target = EmbeddedPushURL
	case strings.HasPrefix(r.PushURL, "unix:///") && nodeID == "":
		target = r.PushURL
	case r.PushPort > 0 && nodeAddr != "":
		target = "http://" + net.JoinHostPort(nodeAddr, strconv.Itoa(r.PushPort))
	}
	m.mu.Lock()
	if target != "" {
		m.pushURLs[r.HostID] = target
	} else {
		delete(m.pushURLs, r.HostID)
	}
	m.mu.Unlock()
	now := m.now().UnixMilli()
	h := Host{ID: r.HostID, CapacityNodes: r.CapacityNodes, Tiers: r.Tiers, NodeID: nodeID, AuditPubKey: auditKey,
		Pool: r.Pool, PushPort: r.PushPort, PushTarget: target, LastSeen: now, UpdatedAt: now, CreatedAt: old.CreatedAt,
		Arch: r.Arch, Capacity: r.Capacity, Usage: old.Usage, RegistrationSecretHash: secretHash,
		Version: r.Version, OS: r.OS, Protocols: r.Protocols, Features: r.Features, CordonedAt: old.CordonedAt, CordonedBy: old.CordonedBy}
	if h.CreatedAt == 0 {
		h.CreatedAt = now
	}
	if h.Pool == "" {
		h.Pool = old.Pool
	}
	if err := m.store.UpsertHost(ctx, h); err != nil {
		return Host{}, false, err
	}
	detail := map[string]any{"host": r.HostID, "node": nodeID,
		"tiers": r.Tiers, "capacity": r.CapacityNodes, "arch": r.Arch, "capacityVector": r.Capacity, "push": m.pushTarget(r.HostID) != "", "new": created,
		"version": r.Version, "features": r.Features}
	if embedded {
		detail["transport"] = "embedded"
	}
	m.audit(ctx, AuditRecord{Kind: "host_register", ActingFor: "host:" + r.HostID}, detail)
	return h, created, nil
}

// pushTarget returns the host's push URL ("" if none).
// pushTarget is where to push to hostID: this replica's cache, else the
// hosts row (written by whichever replica served the registration: H06).
func (m *Mgmt) pushTarget(hostID string) string {
	m.mu.Lock()
	u, ok := m.pushURLs[hostID]
	m.mu.Unlock()
	if ok {
		return u
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := m.store.GetHost(ctx, hostID)
	if err != nil || h.PushTarget == "" {
		return ""
	}
	m.mu.Lock()
	m.pushURLs[hostID] = h.PushTarget
	m.mu.Unlock()
	return h.PushTarget
}

// SetInProcessPush registers (or with nil forgets) the push API of a host
// agent running in this process: push serves POST /v1/push/… through h
// instead of dialling, once the host has registered with EmbeddedPushURL.
func (m *Mgmt) SetInProcessPush(hostID string, h http.Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h == nil {
		delete(m.inprocPush, hostID)
		return
	}
	m.inprocPush[hostID] = h
}

// HeartbeatLease is one lease a host believes it holds.
type HeartbeatLease struct {
	Namespace string `json:"namespace"`
	Epoch     int64  `json:"epoch"`
}

// HeartbeatResponse is the heartbeat reply. RevokeObjects is an addition to
// the v0 contract: object IDs killed in the last RevocationWindow that this
// host must kill (idempotent; repeated until the window passes).
type HeartbeatResponse struct {
	OK            bool     `json:"ok"`
	Revoke        []string `json:"revoke"`
	RevokeObjects []string `json:"revokeObjects"`
}

func (m *Mgmt) Heartbeat(ctx context.Context, hostID string, liveNodes int, leases []HeartbeatLease, deps ...DeploymentStatus) (HeartbeatResponse, error) {
	return m.ReportHostStatus(ctx, hostID, HostReport{LiveNodes: liveNodes, Leases: leases, Deployments: deps})
}

// HostReport is the heartbeat body (PUT /v1/hosts/{id}/status).
type HostReport struct {
	LiveNodes   int                `json:"liveNodes"`
	Leases      []HeartbeatLease   `json:"leases"`
	Deployments []DeploymentStatus `json:"deployments"`
	// Usage is the live capacity usage (nil: not reported). Containers are
	// the running containers; nil (not reported) skips the reconciliation,
	// an empty list means none run.
	Usage      *HostUsage         `json:"usage"`
	Containers []RunningContainer `json:"containers"`
}

// ReportHostStatus is a heartbeat: host liveness and usage, lease renewals,
// deployment statuses and the container reservation reconciliation.
func (m *Mgmt) ReportHostStatus(ctx context.Context, hostID string, rep HostReport) (HeartbeatResponse, error) {
	liveNodes, leases := rep.LiveNodes, rep.Leases
	for _, d := range rep.Deployments {
		if d.DeploymentID != "" {
			m.setDepStatus(hostID, d.DeploymentID, d)
		}
	}
	now := m.now()
	if err := m.store.TouchHost(ctx, hostID, liveNodes, rep.Usage, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return HeartbeatResponse{}, errf(404, "host_not_registered", "host %s is not registered", hostID)
		}
		return HeartbeatResponse{}, err
	}
	resp := HeartbeatResponse{OK: true, Revoke: []string{}, RevokeObjects: []string{}}
	for _, hl := range leases {
		name := m.resolveLease(ctx, hostID, hl.Namespace)
		if _, err := m.store.RenewLease(ctx, name, hostID, hl.Epoch, NamespaceLeaseTTL, now); err != nil {
			resp.Revoke = append(resp.Revoke, hl.Namespace)
		}
	}
	if rep.Containers != nil {
		m.reconcileContainers(ctx, hostID, rep.Containers)
	}
	objs, err := m.store.Revocations(ctx, hostID, now.Add(-m.cfg.RevocationWindow))
	if err != nil {
		return resp, err
	}
	resp.RevokeObjects = append(resp.RevokeObjects, objs...)
	return resp, nil
}

// resolveLease maps a heartbeat "namespace" to a lease name. Accepted forms:
// the lease name itself ("<tenant>/<workload>"), a workload name, or a DO
// class name of a deployment assigned to this host.
func (m *Mgmt) resolveLease(ctx context.Context, hostID, ns string) string {
	if strings.Contains(ns, "/") {
		return ns
	}
	as, _ := m.store.Assignments(ctx, hostID)
	for _, d := range as {
		if d.Workload == ns {
			return LeaseName(d.TenantID, d.Workload)
		}
		for _, c := range d.Namespaces {
			if c.Class == ns {
				return LeaseName(d.TenantID, d.Workload)
			}
		}
	}
	return ns
}

// ---- deployments ----

// DeployRequest is POST /v0/deployments. Namespaces and Host are additions
// to the v0 contract: explicit class config (instead of twd.jsonc in the
// bundle) and an explicit host choice.
// BuiltImages are one class's images built by twd deploy.
type BuiltImages = registry.Built

type DeployRequest struct {
	TenantID   string        `json:"tenantID"`
	Workload   string        `json:"workload"`
	Bundle     []byte        `json:"bundle"` // base64 in JSON
	Namespaces []ClassConfig `json:"namespaces,omitempty"`
	Host       string        `json:"host,omitempty"`
	// ForceTakeover confirms that the workload's current host, which has
	// stopped heartbeating, is really gone, so a stateful workload may move
	// off it (ADR-028 §3). Audited.
	ForceTakeover bool `json:"forceTakeover,omitempty"`
	// ContainerImages are the images twd deploy built and pushed, by class
	// (twd.local/<tenant>/…@sha256 refs): they replace the class's Dockerfile
	// image / images before every ref is pinned (ADR-032 D3).
	ContainerImages map[string]BuiltImages `json:"containerImages,omitempty"`
	// Replace re-places the workload (`twd deploy --replace`, ADR-033):
	// the current live holder is not kept just because it holds the lease;
	// the workload moves if another eligible host is clearly better (or to
	// Host, when pinned). The lease moves at epoch+1 in one conditional
	// write. A stateful workload also needs ForceTakeover: its Durable
	// Object storage stays on the old host's disk.
	Replace bool `json:"replace,omitempty"`
	// By is the deployer (set by the API layer from the principal, never
	// from the request body): a WhoIs login over the tailnet, the local
	// actor on the unix socket.
	By string `json:"-"`

	// replica is set by deployGroup (workload_replicas.go): which replica
	// of which workload this deploy is. nil only on the outer call.
	replica *replicaCtx
}

// DeployResponse is the deploy reply.
type DeployResponse struct {
	DeploymentID string `json:"deploymentID"`
	Host         string `json:"host"`
	Epoch        int64  `json:"epoch"`
	Endpoint     struct {
		Node     string `json:"node"`
		MagicDNS string `json:"magicDNS"`
		// Service (optional) is the workload's Tailscale Service.
		Service *ServiceEndpoint `json:"service,omitempty"`
	} `json:"endpoint"`
	Placement *PlacementDecision `json:"placement,omitempty"`

	// Replicas (twd.jsonc "replicas" > 1): every copy, this one first.
	Replicas []ReplicaRef `json:"replicas,omitempty"`
}

// ServiceEndpoint is a workload's Tailscale Service: a stable name and VIPs
// that don't change when the workload node is re-minted or moves host.
type ServiceEndpoint struct {
	Name     string   `json:"name"`
	MagicDNS string   `json:"magicDNS"`
	Addrs    []string `json:"addrs,omitempty"`
}

func validName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// reservedTags are twd's own infrastructure tags. The OAuth client (owner
// of tag:twd) can mint keys for them, so a bundle must never be able to
// ask for them: a workload node with tag:twd-host would be admitted as a
// host by mgmt's WhoIs gate.
var reservedTags = []string{"tag:twd", MgmtTag, HostTag}

// validTagSyntax is Tailscale's tag grammar: ^tag:[a-zA-Z0-9][a-zA-Z0-9._-]*$
// (no whitespace, no control characters, nothing after a bare "tag:").
func validTagSyntax(tag string) bool {
	rest, ok := strings.CutPrefix(tag, "tag:")
	if !ok || rest == "" {
		return false
	}
	for i, r := range rest {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// validateObjectTag is the single check every tag a workload asks for goes
// through (deploy, preflight, spawn): well-formed (400 bad_request) and not
// one of twd's own tags (400 reserved_tag).
func validateObjectTag(class, tag string) *Error {
	if !validTagSyntax(tag) {
		return errf(400, "bad_request", "namespace %q: %q is not a valid tag (want tag:[a-zA-Z0-9][a-zA-Z0-9._-]*)", class, tag)
	}
	for _, res := range reservedTags {
		if strings.EqualFold(tag, res) {
			return errf(400, "reserved_tag", "namespace %q: %s is one of twd's own tags (%s) and cannot be given to a workload", class, tag, strings.Join(reservedTags, ", "))
		}
	}
	return nil
}

func (m *Mgmt) Deploy(ctx context.Context, r DeployRequest) (DeployResponse, error) {
	if r.replica == nil {
		return m.deployGroup(ctx, r) // replicas: workload_replicas.go
	}
	var resp DeployResponse
	t, err := m.store.GetTenant(ctx, r.TenantID)
	if err != nil {
		return resp, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", r.TenantID))
	}
	if !validName(r.Workload) {
		return resp, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	classes := slices.Clone(r.Namespaces)
	maxLive, maxDepth := DefaultMaxLive, DefaultMaxDepth
	var svcCfg *TwdConfig
	var pub *DeploymentPublic
	var bcfg *TwdConfig
	if len(r.Bundle) > 0 {
		if _, err := BundleCrons(r.Bundle); err != nil {
			return resp, errf(400, "bad_bundle", "%v", err)
		}
		if _, err := BundleQueues(r.Bundle); err != nil {
			return resp, errf(400, "bad_bundle", "%v", err)
		}
		cfg, err := BundleConfig(r.Bundle)
		svcCfg = cfg
		if err != nil && len(classes) == 0 {
			return resp, errf(400, "bad_bundle", "%v", err)
		}
		if err == nil {
			if pub, err = bundlePublic(cfg); err != nil {
				return resp, err
			}
			if err := checkPublicDeploy(t, r.Workload, pub); err != nil {
				return resp, err
			}
			bcfg = cfg
			bundlePlacement(r.Bundle, cfg)
			if len(classes) == 0 {
				classes = cfg.Classes()
			} else if len(cfg.Tailnet.Tags) > 0 && !slices.ContainsFunc(classes, func(c ClassConfig) bool { return c.Class == WorkloadNamespace }) {
				classes = append(classes, ClassConfig{Class: WorkloadNamespace, Tags: cfg.Tailnet.Tags})
			}
			if cfg.Tailnet.Spawn.MaxLive > 0 {
				maxLive = min(cfg.Tailnet.Spawn.MaxLive, DefaultMaxLive)
			}
			if cfg.Tailnet.Spawn.MaxDepth > 0 {
				maxDepth = min(cfg.Tailnet.Spawn.MaxDepth, DefaultMaxDepth)
			}
		}
	}
	if len(classes) == 0 {
		return resp, errf(400, "bad_request", "no namespaces: the bundle's twd.jsonc declares neither tailnet.tags (the workload node) nor tailnet.objects")
	}
	classes = withPublicNamespace(classes, pub)
	for _, c := range classes {
		if c.Class == "" || len(c.Tags) == 0 {
			return resp, errf(400, "bad_request", "namespace %q needs tags", c.Class)
		}
		for _, tag := range c.Tags {
			if e := validateObjectTag(c.Class, tag); e != nil {
				return resp, e
			}
		}
	}
	svcName, err := applyService(svcCfg, r.replica.owner(r.Workload), classes) // replicas share the group's Service
	if err != nil {
		return resp, errf(400, "bad_bundle", "%v", err)
	}
	r.replica.mark(classes)
	need := placementNeed{}
	var ctrs []container.Class
	if len(r.Bundle) > 0 {
		if ctrs, err = BundleContainers(r.Bundle); err != nil {
			return resp, errf(400, "bad_bundle", "%v", err)
		}
		if need.Arch, err = container.Arch(ctrs); err != nil {
			return resp, errf(400, "bad_bundle", "%v", err)
		}
		need.Containers = len(ctrs) > 0
		need.Features = features.RequiredByBundle(r.Bundle)
	}
	if ctrs, err = registry.ApplyBuilt(t.ID, ctrs, r.ContainerImages); err != nil {
		return resp, errf(400, "bad_request", "%v", err)
	}
	// Pin every image to a digest once, now (ADR-032 D3), for the arches of
	// the tenant's container hosts; placement then needs one of the arches
	// every image resolved for.
	if ctrs, need.Arches, err = m.pinContainers(ctx, t, ctrs, need.Arch, r.By); err != nil {
		return resp, err
	}
	// Placement (ADR-033) keeps ADR-032's eligibility: tier, the
	// "containers" capability and arch, and the capacity vector.
	dec, held, err := m.place(ctx, t, placementInput{Workload: r.Workload, Pinned: r.Host, Replace: r.Replace, Config: bcfg, Need: need,
		Exclude: r.replica.exclude})
	if err != nil {
		return resp, err
	}
	host := dec.Host
	var svcDef *ServiceEndpoint
	if svcName != "" && t.Headscale() {
		return resp, errHeadscaleNoService(svcName)
	}
	if svcName != "" {
		// Before the lease and the record: a deploy whose Service can't
		// exist (name taken, no API scope) fails without side effects.
		def, err := m.ensureService(ctx, t, r.replica.owner(r.Workload), svcName)
		if err != nil {
			return resp, err
		}
		svcDef = &ServiceEndpoint{Name: svcName, MagicDNS: tssvc.Label(svcName) + "." + t.Tailnet, Addrs: def.Addrs}
	}
	takeoverFrom, err := m.guardTakeover(ctx, t, r, host)
	if err != nil {
		return resp, err
	}
	fence := true // no bundle (namespaces given directly): assume stateful
	stateWhy := "no bundle to inspect"
	if len(r.Bundle) > 0 {
		fence, stateWhy = BundleStateful(r.Bundle)
	}
	name := LeaseName(t.ID, r.Workload)
	var lease Lease
	movedFrom := ""
	if held != nil && held.Holder != host {
		// A live host runs the workload and placement chose another one.
		if !r.Replace {
			if dec.Reason == "pinned" {
				return resp, m.leaseHeldError(ctx, name, r.Workload)
			}
			why := ""
			for _, s := range dec.Hosts {
				if s.Host == held.Holder && !s.Eligible {
					why = s.Why
				}
			}
			e := errf(409, "lease_held", "workload %q runs on host %s, which can no longer take it (%s); redeploy with --replace to move it to %s", r.Workload, held.Holder, why, host)
			return resp, e
		}
		if fence && !r.ForceTakeover {
			return resp, errf(409, "move_unconfirmed", "workload %q is stateful (%s): moving it from %s to %s starts its Durable Objects with empty storage on %s (DO storage stays on %s's disk). Redeploy with --replace --force-takeover to move it anyway, or pin it with --host %s. Classes that tolerate this can set \"fence\": \"never\" in twd.jsonc",
				r.Workload, stateWhy, held.Holder, host, host, held.Holder, held.Holder)
		}
		lease, err = m.store.MoveLease(ctx, name, held.Holder, held.Epoch, host, NamespaceLeaseTTL, m.now())
		if errors.Is(err, ErrLeaseLost) {
			return resp, errf(409, "lease_changed", "workload %q: its lease changed while it was being moved (another deploy?); retry", r.Workload)
		}
		if err != nil {
			return resp, err
		}
		movedFrom = held.Holder
	} else {
		lease, err = m.store.AcquireLease(ctx, name, host, NamespaceLeaseTTL, m.now())
		if errors.Is(err, ErrLeaseHeld) {
			return resp, m.leaseHeldError(ctx, name, r.Workload)
		}
		if err != nil {
			return resp, err
		}
	}
	d := Deployment{ID: newID("dp_"), TenantID: t.ID, Workload: r.Workload, HostID: host, Namespaces: classes,
		MaxLive: maxLive, MaxDepth: maxDepth, Epoch: lease.Epoch, Bundle: r.Bundle, DeployedBy: r.By, CreatedAt: m.now().UnixMilli(), Fence: fence,
		Containers: ctrs, Public: pub, Placement: &dec}
	detail := map[string]any{"deploymentID": d.ID, "workload": d.Workload, "host": host, "epoch": lease.Epoch, "by": r.By,
		"placement": map[string]any{"reason": dec.Reason, "summary": dec.Summary, "targets": dec.Targets}}
	if pub != nil {
		detail["publicRoutes"] = pub.Spec.Routes
	}
	if svcName != "" {
		detail["service"] = svcName
	}
	if takeoverFrom != "" {
		detail["takeoverFrom"], detail["forced"] = takeoverFrom, true
	}
	if movedFrom != "" {
		detail["movedFrom"], detail["replace"] = movedFrom, true
		if fence {
			detail["forced"] = true
		}
	}
	actx, ai := m.intend(ctx, AuditRecord{TenantID: t.ID, Kind: "deploy", ActingFor: r.By}, detail)
	if err := m.store.CreateDeployment(actx, d); err != nil { // the deploy record commits with it (H22)
		return resp, err
	}
	m.recordIntent(ctx, ai, nil)
	if pub != nil {
		m.purgeDeadPublicNode(ctx, t.ID, r.Workload)
	}
	if pub == nil {
		// Public routes removed (or never declared): a public node left
		// from an earlier deploy must not keep serving (ADR-029).
		m.killPublicNode(ctx, t.ID, r.Workload, "public routes removed", r.By)
	}
	resp.DeploymentID, resp.Host, resp.Epoch, resp.Placement = d.ID, host, lease.Epoch, &dec
	resp.Endpoint.Node = r.Workload + "-" + host
	resp.Endpoint.MagicDNS = resp.Endpoint.Node + "." + t.Tailnet
	resp.Endpoint.Service = svcDef
	m.setDepStatus(host, d.ID, DeploymentStatus{Status: "assigned"})
	// Tell the host now instead of at its next heartbeat.
	go m.push(host, "/v1/push/assignments", map[string]any{})
	if movedFrom != "" && fence {
		// A stateful move: stop the old copy now rather than at its next
		// heartbeat (its renew at the old epoch fails either way).
		go m.push(movedFrom, "/v1/push/assignments", map[string]any{})
	}
	return resp, nil
}

// DeploymentStatus is what the host last reported for a deployment
// (heartbeat "deployments"); kept in memory, refreshed every heartbeat.
type DeploymentStatus struct {
	DeploymentID string `json:"deploymentID"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

// setDepStatus records a deployment's status in the store, shared by every
// replica (H08). The UPDATE only writes when the stored value differs, so the
// heartbeats that repeat it every few seconds cost a lookup, not a write. A
// per-replica cache can't decide that: another replica may have written a
// different status since.
func (m *Mgmt) setDepStatus(hostID, id string, st DeploymentStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.SetDeploymentStatus(ctx, id, hostID, st.Status, st.Error); err != nil {
		m.log.Warn("deployment status", "deployment", id, "err", err)
	}
}

// DeploymentView is GET /v0/deployments/{id}.
type DeploymentView struct {
	DeploymentID string        `json:"deploymentID"`
	TenantID     string        `json:"tenantID"`
	Workload     string        `json:"workload"`
	Host         string        `json:"host"`
	Epoch        int64         `json:"epoch"`
	Status       string        `json:"status"`
	Error        string        `json:"error,omitempty"`
	Namespaces   []ClassConfig `json:"namespaces"`
	DeployedBy   string        `json:"deployedBy,omitempty"`
	CreatedAt    int64         `json:"createdAt"`
	// Containers are the container classes and their pinned images.
	Containers []container.Class `json:"containers,omitempty"`
	// Public (ADR-029): the public URL and routes; nil when tailnet-only.
	Public *DeploymentPublicView `json:"public,omitempty"`
	// Placement is why the deployment is on its host (ADR-033 draft).
	Placement *PlacementDecision `json:"placement,omitempty"`
	Endpoint  struct {
		ObjectID string           `json:"objectID,omitempty"`
		Node     string           `json:"node"`
		MagicDNS string           `json:"magicDNS"`
		Status   string           `json:"status"`
		Service  *ServiceEndpoint `json:"service,omitempty"`
	} `json:"endpoint"`

	// Replicas (twd.jsonc "replicas" > 1): every copy with its status.
	Replicas []ReplicaRef `json:"replicas,omitempty"`
}

// GetDeployment returns a deployment with the host-reported status and its
// workload entry node. Status is "superseded" once a newer deploy of the
// same workload exists.
func (m *Mgmt) GetDeployment(ctx context.Context, id string) (DeploymentView, error) {
	var v DeploymentView
	d, err := m.store.GetDeployment(ctx, id)
	if err != nil {
		return v, err
	}
	v.DeploymentID, v.TenantID, v.Workload, v.Host, v.Epoch, v.Namespaces, v.CreatedAt = d.ID, d.TenantID, d.Workload, d.HostID, d.Epoch, d.Namespaces, d.CreatedAt
	v.DeployedBy, v.Placement = d.DeployedBy, d.Placement
	v.Containers = d.Containers
	v.Status, v.Error = m.deploymentStatus(ctx, d)
	if svc := workloadService(d.Namespaces); svc != "" {
		t, _ := m.store.GetTenant(ctx, d.TenantID)
		v.Endpoint.Service = &ServiceEndpoint{Name: svc, MagicDNS: tssvc.Label(svc) + "." + t.Tailnet}
	}
	v.Public = m.publicView(ctx, d)
	v.Replicas = m.replicaView(ctx, d, v.Status)
	if o, err := m.store.GetObjectByName(ctx, d.TenantID, d.Workload, WorkloadNamespace, d.Workload); err == nil {
		v.Endpoint.ObjectID, v.Endpoint.MagicDNS, v.Endpoint.Status = o.ID, o.MagicDNS, o.Status
		v.Endpoint.Node, _, _ = strings.Cut(o.MagicDNS, ".")
	}
	return v, nil
}

// guardTakeover refuses to move a stateful workload off a host that has
// stopped heartbeating but is still registered (ADR-028 §3): that host may
// still be serving the workload's Durable Objects from its own disk, and it
// can't be told to stop. Allowed when the old host is live (it is revoked at
// its next heartbeat), was removed (twd host rm), the workload is stateless
// or every DO class is "fence": "never", or the deploy sets ForceTakeover.
// It returns the old host when a forced takeover happens (for the audit).
func (m *Mgmt) guardTakeover(ctx context.Context, t Tenant, r DeployRequest, host string) (string, error) {
	l, err := m.store.GetLease(ctx, LeaseName(t.ID, r.Workload))
	if err != nil || l.Holder == "" || l.Holder == host {
		return "", nil
	}
	old, err := m.store.GetHost(ctx, l.Holder)
	if err != nil {
		return "", nil // removed: nothing left to split with
	}
	now := m.now()
	if old.Live(now) {
		return "", nil
	}
	stateful, why := true, "no bundle to inspect"
	if len(r.Bundle) > 0 {
		stateful, why = BundleStateful(r.Bundle)
	}
	if !stateful {
		return "", nil
	}
	if r.ForceTakeover {
		return old.ID, nil
	}
	seen := "never"
	if old.LastSeen > 0 {
		seen = now.Sub(time.UnixMilli(old.LastSeen)).Round(time.Second).String() + " ago"
	}
	return "", errf(409, "takeover_unconfirmed", "workload %q runs on host %s, which mgmt last heard from %s; it may still be serving the workload's %s from its own disk, so moving it to %s could run two copies. If %s is gone for good: `twd host rm %s --drain`, or redeploy with --force-takeover. If it is coming back, wait for it. Classes that tolerate two instances can set \"fence\": \"never\" in twd.jsonc",
		r.Workload, old.ID, seen, why, host, old.ID, old.ID)
}

// leaseHeldError explains a deploy that placement sent to a new host while
// the workload's lease is still held: the holder has stopped heartbeating
// (dead to placement after HostLeaseTTL) but its lease (NamespaceLeaseTTL)
// has not expired. Retry-After is the time left on the lease.
func (m *Mgmt) leaseHeldError(ctx context.Context, name, workload string) *Error {
	now := m.now()
	l, err := m.store.GetLease(ctx, name)
	if err != nil {
		return errf(409, "lease_held", "workload %q: its lease is held by another host", workload)
	}
	seen := "never"
	h, herr := m.store.GetHost(ctx, l.Holder)
	if herr == nil && h.LastSeen > 0 {
		seen = now.Sub(time.UnixMilli(h.LastSeen)).Round(time.Second).String() + " ago"
	}
	if herr == nil && h.Live(now) {
		// A live holder keeps renewing: its lease won't run out.
		return errf(409, "lease_held", "workload %q runs on host %s (live, epoch %d, last seen %s); it can't be placed elsewhere while %s holds it. To move it, drain that host first (`twd host rm %s --drain`)",
			workload, l.Holder, l.Epoch, seen, l.Holder, l.Holder)
	}
	left := time.UnixMilli(l.ExpiresAt).Sub(now)
	if left < 0 {
		left = 0
	}
	e := errf(409, "lease_held", "workload %q is still leased to host %s (epoch %d); this mgmt last saw it %s (a host is dead to placement after %v), and the lease expires in %v. Redeploy after that, or pin a live host (the deploy request's \"host\" field)", workload, l.Holder, l.Epoch, seen, HostLeaseTTL, left.Round(time.Second))
	e.RetryAfter = max(1, int((left+time.Second-1)/time.Second))
	return e
}

// placementTier is the tier a host must offer to run the deployment: the
// bundle's twd.jsonc "tier", else the tenant's default. "" means any host
// (every host offers the process tier). Node apps always run in the process
// tier (the microVM guest has no Node.js).
func placementTier(t Tenant, cfg *TwdConfig) string {
	tier := t.Tier
	if cfg != nil {
		if cfg.Runtime == "node" {
			return ""
		}
		if cfg.Tier != "" {
			tier = cfg.Tier
		}
	}
	if tier == "process" {
		return ""
	}
	return tier
}

// offersTier reports whether h can run a workload that needs tier ("" =
// any). Hosts list their tiers at registration; one that lists none is
// treated as process-only.
func (h Host) offersTier(tier string) bool {
	return tier == "" || slices.Contains(h.Tiers, tier)
}

// placementNeed is what a host must offer to run a deployment: its tier
// ("" = any) and, when the bundle declares containers, the "containers"
// capability for the images' arch ("" = any). A container always runs on
// its parent DO's host (ADR-032 D9), so this holds for process-tier parents
// too.
type placementNeed struct {
	Tier       string
	Containers bool
	Arch       string
	// Arches, when set, are the arches the deploy's images were pinned for
	// (the host's arch must be one of them).
	Arches []string

	// Features the bundle needs from its host (internal/features).
	Features []string
}

func (h Host) offers(n placementNeed) bool {
	if !h.offersTier(n.Tier) {
		return false
	}
	return !n.Containers || slices.Contains(h.Tiers, CapContainers) && (n.Arch == "" || h.Arch == n.Arch) &&
		(len(n.Arches) == 0 || slices.Contains(n.Arches, h.Arch))
}

// String names the need in errors: `the "microvm" tier`, `containers
// (amd64 images)`, or both.
func (n placementNeed) String() string {
	var parts []string
	if n.Tier != "" {
		parts = append(parts, fmt.Sprintf("the %q tier", n.Tier))
	}
	if n.Containers {
		c := "containers"
		if n.Arch != "" {
			c += " (" + n.Arch + " images)"
		} else if len(n.Arches) > 0 {
			c += " (" + strings.Join(n.Arches, " or ") + " images: the arches every image was pinned for)"
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, " and ")
}

func tierList(h Host) string {
	if len(h.Tiers) == 0 {
		return "none listed (process only)"
	}
	s := strings.Join(h.Tiers, ", ")
	if h.Arch != "" {
		s += "; arch " + h.Arch
	}
	return s
}

// Assignment is one GET /v0/hosts/{id}/assignments entry. Tailnet,
// ControlURL and Lease are additions to the v0 contract.
type Assignment struct {
	DeploymentID string        `json:"deploymentID"`
	TenantID     string        `json:"tenantID"`
	Workload     string        `json:"workload"`
	BundleURL    string        `json:"bundleURL"`
	Namespaces   []ClassConfig `json:"namespaces"`
	Epoch        int64         `json:"epoch"`
	Lease        string        `json:"lease"`
	Tailnet      string        `json:"tailnet"`
	ControlURL   string        `json:"controlURL,omitempty"`
	WorkloadTags []string      `json:"workloadTags,omitempty"`
	// Service: the Tailscale Service the workload node advertises while the
	// deployment is active and healthy ("" = none).
	Service string `json:"service,omitempty"`
	// Fence: stop serving this workload when mgmt is unreachable for
	// FenceAfter (stateful). Hosts treat a missing field as true.
	Fence bool `json:"fence"`
	// Containers are the container classes with their pinned images
	// (ADR-032 D3): hosts pull only these digests.
	Containers []container.Class `json:"containers,omitempty"`
	// Public (ADR-029): the workload's public ingress; nil when it has no
	// twd.jsonc "public" or the tenant's publicIngress is not allow.
	Public *PublicAssignment `json:"public,omitempty"`
	// PlacementTargets are what the host should measure paths to for this
	// workload (smart placement, ADR-033; empty = nothing).
	PlacementTargets []string `json:"placementTargets,omitempty"`
}

func (m *Mgmt) Assignments(ctx context.Context, hostID string) ([]Assignment, error) {
	ds, err := m.store.Assignments(ctx, hostID)
	if err != nil {
		return nil, err
	}
	out := []Assignment{}
	for _, d := range ds {
		t, _ := m.store.GetTenant(ctx, d.TenantID)
		a := Assignment{DeploymentID: d.ID, TenantID: d.TenantID, Workload: d.Workload,
			BundleURL: "/v1/deployments/" + d.ID + "/bundle", Epoch: d.Epoch,
			Lease: LeaseName(d.TenantID, d.Workload), Tailnet: t.Tailnet, ControlURL: t.ControlURL, Namespaces: []ClassConfig{}, Fence: d.Fence, Containers: d.Containers}
		a.Public = m.publicAssignment(ctx, t, d)
		if d.Placement != nil && d.Placement.Mode == PlacementSmart {
			a.PlacementTargets = d.Placement.Targets
		}
		for _, c := range d.Namespaces {
			if c.Class == WorkloadNamespace {
				a.WorkloadTags, a.Service = c.Tags, c.Service
				continue
			}
			if c.Class == PublicNamespace {
				continue // the public node is described by a.Public
			}
			a.Namespaces = append(a.Namespaces, c)
		}
		out = append(out, a)
	}
	return out, nil
}

// ---- objects ----

// CreateObjectRequest is POST /v0/objects. ActingFor is an addition (the
// human principal of the spawning request, for audit).
type CreateObjectRequest struct {
	TenantID string `json:"tenantID"`
	// Workload scopes the object's identity (M7): (tenant, workload,
	// namespace, objectName). Optional for compatibility: if empty, a
	// spawn takes its parent's workload, the "_workload" namespace takes
	// objectName, and otherwise exactly one workload may declare the class.
	Workload   string `json:"workload,omitempty"`
	Namespace  string `json:"namespace"`
	ObjectName string `json:"objectName"`
	Parent     *struct {
		ID string `json:"objectID"`
	} `json:"parent,omitempty"`
	Caps      json.RawMessage `json:"caps,omitempty"`
	TTL       json.RawMessage `json:"ttl,omitempty"`
	ActingFor string          `json:"actingFor,omitempty"`
	TraceID   string          `json:"traceID,omitempty"`
	// PoolObjectID offers a ready pre-joined node of namespace "_pool"
	// (ADR-031) for a new object: if it is this tenant's and workload's,
	// for this class, with exactly the class's tags, and the object is not
	// ephemeral, the object is created "assigned" to that node (no mint)
	// and the pool node is transferred in the same transaction. Otherwise
	// it is ignored and the object is minted as usual.
	PoolObjectID string `json:"poolObjectID,omitempty"`
	// Epoch is the lease epoch the calling host holds for the workload.
	// hostd always sends it; mgmt fences the create on it (RT3-01).
	Epoch int64 `json:"epoch,omitempty"`
	// hostID is the authenticated host making the request (set by the API
	// layer from the principal, never from the body). Non-empty: the
	// create is fenced on the workload's lease being held by this host.
	hostID string
}

// WithHost returns r bound to the authenticated host hostID.
func (r CreateObjectRequest) WithHost(hostID string) CreateObjectRequest { r.hostID = hostID; return r }

// CreateObjectResponse is the reply. Hostname is an addition: the node
// hostname hostd should use.
type CreateObjectResponse struct {
	ObjectID string   `json:"objectID"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
	Hostname string   `json:"hostname"`
	// Created is true for a fresh object (the API answers 202), false when
	// an existing live object was returned (200).
	Created bool `json:"-"`
}

var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// objectID is <class-slug>-<b32(hmac(tenant/workload/ns/name))[:10]> (RFC
// §4.3); it doubles as the node hostname. Every part of the identity is in
// the MAC, so two workloads (or two tenants) with the same class and object
// name get distinct IDs, hostnames, nodes and state dirs.
func (m *Mgmt) objectID(tenantID, workload, ns, name string) string {
	mac := hmac.New(sha256.New, m.cfg.IDSecret)
	fmt.Fprintf(mac, "%s\x00%s\x00%s\x00%s", tenantID, workload, ns, name)
	var slug strings.Builder
	src := ns
	if ns == WorkloadNamespace {
		src = name // the workload entry node is named after the workload
	}
	for _, r := range strings.ToLower(src) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
		} else if slug.Len() > 0 {
			slug.WriteByte('-')
		}
	}
	s := strings.Trim(slug.String(), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "obj"
	}
	return s + "-" + b32.EncodeToString(mac.Sum(nil))[:10]
}

func (m *Mgmt) tenantLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.spawnMu[id]
	if l == nil {
		l = &sync.Mutex{}
		m.spawnMu[id] = l
	}
	return l
}

func (m *Mgmt) CreateObject(ctx context.Context, r CreateObjectRequest) (CreateObjectResponse, error) {
	var resp CreateObjectResponse
	t, err := m.store.GetTenant(ctx, r.TenantID)
	if err != nil {
		return resp, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", r.TenantID))
	}
	if r.ObjectName == "" || len(r.ObjectName) > 256 {
		return resp, errf(400, "bad_request", "objectName required (≤256 bytes)")
	}
	var parent *Object
	if r.Parent != nil && r.Parent.ID != "" {
		p, err := m.store.GetObject(ctx, r.Parent.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return resp, asAPIError(err)
		}
		if err != nil || p.TenantID != t.ID {
			return resp, errf(404, "delegation_invalid", "parent %q not found in tenant %s", r.Parent.ID, t.ID)
		}
		parent = &p
	}
	workload := r.Workload
	classNS := r.Namespace // the class whose config (tags) applies
	if r.Namespace == PoolNamespace {
		cls, suffix, ok := strings.Cut(r.ObjectName, "/")
		if !ok || cls == "" || suffix == "" || workload == "" || parent != nil || strings.HasPrefix(cls, "_") {
			return resp, errf(400, "bad_request", "a %s node needs workload and objectName \"<Class>/<suffix>\" (not a spawn)", PoolNamespace)
		}
		classNS = cls
	}
	switch {
	case r.Namespace == WorkloadNamespace, r.Namespace == PublicNamespace:
		if workload != "" && workload != r.ObjectName {
			return resp, errf(400, "bad_request", "the %s object of workload %q must be named after it (got %q)", r.Namespace, workload, r.ObjectName)
		}
		if parent != nil {
			return resp, errf(400, "bad_request", "the %s object of a workload cannot be spawned", r.Namespace)
		}
		workload = r.ObjectName
	case workload == "" && parent != nil:
		workload = parent.Workload
	}
	var dep Deployment
	var class ClassConfig
	if r.Namespace == WorkloadNamespace {
		dep, class, err = m.findWorkloadClass(ctx, t.ID, workload)
	} else if r.Namespace == PublicNamespace {
		dep, class, err = m.findPublicClass(ctx, t, workload)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return resp, err
		}
	} else {
		dep, class, err = m.store.FindClass(ctx, t.ID, workload, classNS)
	}
	if errors.Is(err, ErrAmbiguous) {
		return resp, errf(409, "namespace_ambiguous", "%v", err)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return resp, asAPIError(err)
	}
	if err != nil {
		in := ""
		if workload != "" {
			in = " of workload " + workload
		}
		return resp, errf(404, "unknown_namespace", "no deployment%s of tenant %s declares namespace %q", in, t.ID, r.Namespace)
	}
	workload = dep.Workload
	// A host may only create objects on a lease it holds (RT3-01). The
	// fence is checked again inside the insert transaction; this early read
	// gives a clear answer and an audit record without touching the row.
	var fence *Fence
	if r.hostID != "" || r.Epoch > 0 {
		fence = &Fence{Lease: LeaseName(t.ID, dep.Workload), Holder: r.hostID, Epoch: r.Epoch}
		if l, lerr := m.store.GetLease(ctx, fence.Lease); lerr != nil || l.Holder == "" || (fence.Holder != "" && l.Holder != fence.Holder) || (fence.Epoch > 0 && l.Epoch != fence.Epoch) {
			m.audit(ctx, AuditRecord{TenantID: t.ID, Kind: "deny", Code: "lease_lost", TraceID: r.TraceID},
				map[string]any{"workload": workload, "namespace": r.Namespace, "name": r.ObjectName, "host": r.hostID, "epoch": r.Epoch, "message": "create on a lease the caller does not hold"})
			return resp, errf(409, "lease_lost", "host %q does not hold the lease of workload %q at epoch %d; only the host serving a workload may create its objects", r.hostID, workload, r.Epoch)
		}
	}
	ttl, err := ParseTTL(r.TTL)
	if err != nil {
		return resp, errf(400, "bad_request", "%v", err)
	}
	if ttl == 0 && class.TTL != "" && r.Namespace != PoolNamespace {
		ttl, _ = time.ParseDuration(class.TTL)
	}
	l := m.tenantLock(t.ID)
	l.Lock()
	defer l.Unlock()

	if o, err := m.store.GetObjectByName(ctx, t.ID, workload, r.Namespace, r.ObjectName); err == nil {
		if !o.Live() {
			return resp, errf(410, "killed", "object %s (%s %s/%s) was %s", o.ID, workload, r.Namespace, r.ObjectName, o.Status)
		}
		return CreateObjectResponse{ObjectID: o.ID, Status: o.Status, Tags: o.Tags, Hostname: o.Hostname}, nil
	}
	if n, err := m.store.CountLive(ctx, t.ID, ""); err != nil {
		return resp, err
	} else if n >= t.MaxObjects {
		return resp, errf(429, "object_quota_exceeded", "tenant %s has %d live objects (maxObjects %d)", t.ID, n, t.MaxObjects)
	}
	id := m.objectID(t.ID, workload, r.Namespace, r.ObjectName)
	if ex, err := m.store.GetObject(ctx, id); err == nil {
		if ex.TenantID == t.ID && ex.Workload == workload && ex.Namespace == r.Namespace && ex.Name == r.ObjectName {
			// The same identity, created by another replica since our by-name
			// lookup (replicas share the ID secret: F12). Idempotent answer.
			if !ex.Live() {
				return resp, errf(410, "killed", "object %s (%s %s/%s) was %s", ex.ID, workload, r.Namespace, r.ObjectName, ex.Status)
			}
			return CreateObjectResponse{ObjectID: ex.ID, Status: ex.Status, Tags: ex.Tags, Hostname: ex.Hostname}, nil
		}
		// An ID collision with another identity (a 50-bit MAC prefix): never
		// hand out an existing object's node.
		return resp, errf(409, "identity_conflict", "object ID %s is already taken by another identity", id)
	}
	hostname := id
	if r.Namespace == PublicNamespace {
		// The public URL is https://<workload>.<tailnet>.ts.net (ADR-029 D2).
		hostname = workload
	}
	o := Object{ID: id, TenantID: t.ID, Workload: workload, Namespace: r.Namespace, Name: r.ObjectName, Hostname: hostname, DeploymentID: dep.ID,
		Lease: LeaseName(t.ID, dep.Workload), Tags: class.Tags, Caps: r.Caps, RootID: id, TTLSeconds: int64(ttl / time.Second),
		Status: StatusPending, ActingFor: r.ActingFor, CreatedAt: m.now().UnixMilli(), UpdatedAt: m.now().UnixMilli()}
	kind := "create"
	if parent != nil {
		kind = "spawn"
		p := *parent
		if !p.Live() {
			return resp, errf(410, "delegation_invalid", "parent %s is %s", p.ID, p.Status)
		}
		if err := m.checkSpawn(ctx, t, dep, p, o); err != nil {
			m.audit(ctx, AuditRecord{TenantID: t.ID, ObjectID: p.ID, Kind: "deny", Code: err.Code, TraceID: r.TraceID,
				ActingFor: r.ActingFor}, map[string]any{"spawn": r.Namespace + "/" + r.ObjectName, "message": err.Message})
			return resp, err
		}
		o.ParentID, o.RootID, o.Depth = p.ID, p.RootID, p.Depth+1
		o.Ephemeral = ttl > 0
	}
	maxLive := dep.MaxLive
	if maxLive <= 0 {
		maxLive = DefaultMaxLive
	}
	spawnLimit := 0
	if parent != nil {
		spawnLimit = maxLive
	}
	detail := map[string]any{"workload": o.Workload, "namespace": o.Namespace, "name": o.Name, "tags": o.Tags, "root": o.RootID}
	if r.ActingFor == ActingForPublic {
		detail["origin"] = "public" // created by a public (Funnel) request (ADR-029 D5)
	}
	if o.ParentID != "" {
		detail["parent"] = o.ParentID
	}
	if len(o.Caps) > 0 {
		detail["lim"] = o.Caps
	}
	if ttl > 0 {
		detail["ttl"] = ttl.String()
	}
	if r.Namespace == PoolNamespace {
		detail["pool"] = classNS
	}
	pool := m.usablePool(ctx, r, o, class, dep)
	if pool != nil {
		o.Status, o.NodeID, o.StableNodeID = StatusAssigned, pool.NodeID, pool.StableNodeID
		detail["fromPool"], detail["stableNodeID"] = pool.ID, pool.StableNodeID
	}
	// The create/spawn record commits in the same transaction as the insert (H22).
	actx, ai := m.intend(ctx, AuditRecord{TenantID: t.ID, ObjectID: o.ID, Kind: kind, TraceID: r.TraceID, ActingFor: r.ActingFor}, detail)
	var err2 error
	var ti *auditIntent
	if pool != nil {
		// …and so does the pool node's transfer record.
		actx, ti = m.intend(actx, AuditRecord{TenantID: t.ID, ObjectID: pool.ID, Kind: "transfer", TraceID: r.TraceID},
			map[string]any{"to": o.ID, "stableNodeID": pool.StableNodeID, "class": r.Namespace, "name": r.ObjectName})
		err2 = m.store.(poolStore).TransferPool(actx, o, pool.ID, pool.StableNodeID, t.MaxObjects, spawnLimit, fence)
		if errors.Is(err2, ErrPoolGone) {
			// Another create took this pool node first: mint as usual.
			r.PoolObjectID = ""
			return m.CreateObject(ctx, r)
		}
	} else {
		err2 = m.store.CreateObjectChecked(actx, o, t.MaxObjects, spawnLimit, fence)
	}
	if err := err2; errors.Is(err, ErrLeaseLost) {
		// The lease moved between the early read and the insert (RT3-01).
		return resp, errf(409, "lease_lost", "host %q lost the lease of workload %q while creating %s/%s: %v", r.hostID, workload, r.Namespace, r.ObjectName, err)
	} else if errors.Is(err, ErrQuota) {
		// The fast-path CountLive saw room, but concurrent creates (possibly
		// through other replicas) filled the quota first (H16).
		return resp, errf(429, "object_quota_exceeded", "tenant %s has reached its maxObjects (%d)", t.ID, t.MaxObjects)
	} else if errors.Is(err, ErrSpawnBudget) {
		// checkSpawn saw room, but a concurrent spawn (possibly through
		// another replica) took the last slot (H04).
		e := errf(429, "spawn_budget_exceeded", "root %s already has its maximum of live descendants (maxLive %d)", o.RootID, maxLive)
		m.audit(ctx, AuditRecord{TenantID: t.ID, ObjectID: o.ParentID, Kind: "deny", Code: e.Code, TraceID: r.TraceID,
			ActingFor: r.ActingFor}, map[string]any{"spawn": r.Namespace + "/" + r.ObjectName, "message": e.Message})
		return resp, e
	} else if errors.Is(err, ErrExists) {
		// Another replica created this name between our lookup and insert
		// (tenantLock is per replica: H14). Answer as the idempotent path.
		ex, gerr := m.store.GetObjectByName(ctx, t.ID, workload, r.Namespace, r.ObjectName)
		switch {
		case gerr == nil && !ex.Live():
			return resp, errf(410, "killed", "object %s (%s %s/%s) was %s", ex.ID, workload, r.Namespace, r.ObjectName, ex.Status)
		case gerr == nil:
			return CreateObjectResponse{ObjectID: ex.ID, Status: ex.Status, Tags: ex.Tags, Hostname: ex.Hostname}, nil
		case errors.Is(gerr, ErrNotFound):
			return resp, errf(409, "identity_conflict", "object ID %s is already taken by another identity", o.ID)
		default:
			return resp, fmt.Errorf("create %s: lookup after a concurrent insert: %w", o.ID, gerr)
		}
	} else if err != nil {
		return resp, err
	}
	m.recordIntent(ctx, ai, nil)
	if pool != nil {
		m.recordIntent(ctx, ti, nil)
		m.log.Info("pool node assigned", "object", o.ID, "pool", pool.ID, "stableNodeID", pool.StableNodeID)
		return CreateObjectResponse{ObjectID: o.ID, Status: StatusAssigned, Tags: o.Tags, Hostname: o.Hostname, Created: true}, nil
	}
	m.enqueue(t.ID, o.ID)
	return CreateObjectResponse{ObjectID: o.ID, Status: StatusPending, Tags: o.Tags, Hostname: o.Hostname, Created: true}, nil
}

var errWrongNode = errors.New("ready on another node than the assigned one")

// poolStore is implemented by stores that can transfer pool nodes.
type poolStore interface {
	TransferPool(ctx context.Context, o Object, poolID, stableID string, maxObjects, maxLive int, fence *Fence) error
}

// usablePool returns the pool node r offers for o if every condition of
// ADR-031 variant B holds, else nil (the object is then minted as usual).
func (m *Mgmt) usablePool(ctx context.Context, r CreateObjectRequest, o Object, class ClassConfig, dep Deployment) *Object {
	// Never for a reserved namespace (_pool, _workload, the public node's).
	if r.PoolObjectID == "" || strings.HasPrefix(r.Namespace, "_") || r.Namespace == PublicNamespace || o.Ephemeral {
		return nil
	}
	if _, ok := m.store.(poolStore); !ok {
		return nil
	}
	p, err := m.store.GetObject(ctx, r.PoolObjectID)
	why := ""
	switch {
	case err != nil:
		why = "not found"
	case p.TenantID != o.TenantID || p.Workload != o.Workload || p.Namespace != PoolNamespace:
		why = "another tenant's, workload's or namespace's"
	case !strings.HasPrefix(p.Name, r.Namespace+"/"):
		why = "for another class"
	case p.Status != StatusReady || p.StableNodeID == "":
		why = "not a ready node (" + p.Status + ")"
	case p.Ephemeral:
		why = "ephemeral"
	case !slices.Equal(canonTags(p.Tags), canonTags(class.Tags)) || !slices.Equal(canonTags(o.Tags), canonTags(class.Tags)):
		why = "tags differ from the class's"
	case r.hostID != "" && !m.poolJoinedBy(ctx, p, dep, r.hostID):
		// The node's state is on the disk of the host that joined it; another
		// host adopting it would strand the object (RT3-04).
		why = "joined by another host"
	}
	if why != "" {
		m.log.Warn("pool node not used", "pool", r.PoolObjectID, "object", o.ID, "why", why)
		return nil
	}
	return &p
}

// poolJoinedBy reports whether pool node p was joined by hostID: it was
// created under a deployment of this workload assigned to that host. The
// current deployment is the common case; an older one of the same
// workload on the same host (pool nodes outlive redeploys) also counts.
func (m *Mgmt) poolJoinedBy(ctx context.Context, p Object, dep Deployment, hostID string) bool {
	if p.DeploymentID == dep.ID {
		return dep.HostID == hostID
	}
	pd, err := m.store.GetDeployment(ctx, p.DeploymentID)
	return err == nil && pd.HostID == hostID && pd.TenantID == dep.TenantID && pd.Workload == dep.Workload
}

// findWorkloadClass finds the latest deployment of the workload named name
// and its "_workload" namespace (the entry node's tags).
func (m *Mgmt) findWorkloadClass(ctx context.Context, tenantID, name string) (Deployment, ClassConfig, error) {
	ds, err := m.store.LatestDeployments(ctx, tenantID)
	if err != nil {
		return Deployment{}, ClassConfig{}, err
	}
	for _, d := range ds {
		if d.Workload != name {
			continue
		}
		for _, c := range d.Namespaces {
			if c.Class == WorkloadNamespace {
				return d, c, nil
			}
		}
	}
	return Deployment{}, ClassConfig{}, fmt.Errorf("workload %q has no %s namespace (twd.jsonc tailnet.tags): %w", name, WorkloadNamespace, ErrNotFound)
}

// checkSpawn enforces depth ≤ maxDepth, live descendants of the root <
// maxLive, child-tag ownership, and caps ⊆ effective(parent) from policy.
func (m *Mgmt) checkSpawn(ctx context.Context, t Tenant, dep Deployment, p, child Object) *Error {
	maxDepth, maxLive := dep.MaxDepth, dep.MaxLive
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	if maxLive <= 0 {
		maxLive = DefaultMaxLive
	}
	if p.Depth+1 > maxDepth {
		return errf(429, "spawn_budget_exceeded", "depth %d would exceed maxDepth %d", p.Depth+1, maxDepth)
	}
	n, err := m.store.CountLive(ctx, t.ID, p.RootID)
	if err != nil {
		return asAPIError(err)
	}
	if n+1 > maxLive {
		return errf(429, "spawn_budget_exceeded", "root %s already has %d live descendants (maxLive %d)", p.RootID, n, maxLive)
	}
	pol, err := m.policies.Get(ctx, t)
	if err != nil {
		return errf(503, "policy_unavailable", "can't read tenant policy to check delegation: %v", err)
	}
	for _, tag := range child.Tags {
		if e := validateObjectTag(child.Namespace, tag); e != nil {
			return e
		}
		if !pol.TagOwnedBy(tag, p.Tags) {
			e := errf(403, "delegation_invalid", "child tag %s is not owned by the parent's tags %v", tag, p.Tags)
			e.Snippet = fmt.Sprintf(`"tagOwners": { %q: ["tag:twd", %q] }`, tag, p.Tags[0])
			return e
		}
	}
	if e := pol.CheckSpawnCaps(p.Tags, child.Caps); e != nil {
		return e
	}
	return nil
}

func (m *Mgmt) GetObject(ctx context.Context, id string) (Object, error) {
	o, err := m.store.GetObject(ctx, id)
	if err != nil {
		return o, err
	}
	kids, _ := m.store.ListObjects(ctx, ObjectFilter{ParentID: id})
	for _, k := range kids {
		o.Children = append(o.Children, k.ID)
	}
	return o, nil
}

// ListObjects lists objects with lineage (children filled in).
func (m *Mgmt) ListObjects(ctx context.Context, tenant, workload, ns string) ([]Object, error) {
	objs, err := m.store.ListObjects(ctx, ObjectFilter{TenantID: tenant, Workload: workload, Namespace: ns})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i := range objs {
		idx[objs[i].ID] = i
	}
	for _, o := range objs {
		if i, ok := idx[o.ParentID]; ok {
			objs[i].Children = append(objs[i].Children, o.ID)
		}
	}
	if objs == nil {
		objs = []Object{}
	}
	return objs, nil
}

// ---- mint queue ----

func (m *Mgmt) throttle(t Tenant) *Throttle {
	m.mu.Lock()
	defer m.mu.Unlock()
	th := m.throttles[t.ID]
	if th != nil {
		// The tenant's limit may have changed (PATCH; any replica): the mint
		// worker re-reads the tenant on every attempt (H15).
		want := t.RegistrationsPerMinute
		if want <= 0 {
			want = DefaultRegistrationsPerMinute
		}
		if l, _ := th.cfg(); l != float64(want) {
			th.SetLimit(want)
		}
	}
	if th == nil {
		if ts, ok := m.store.(throttleStore); ok {
			// Shared across replicas, timed by the store clock (F7, H03).
			id := t.ID
			th = NewSharedThrottle(t.RegistrationsPerMinute, storeTimedClock{m}, func(ctx context.Context, fn func(*ThrottleState)) error {
				return ts.UpdateThrottle(ctx, id, fn)
			})
		} else {
			th = NewThrottle(t.RegistrationsPerMinute, m.clock)
		}
		m.throttles[t.ID] = th
	}
	return th
}

// throttleStore is implemented by stores that can hold shared throttle state.
type throttleStore interface {
	UpdateThrottle(ctx context.Context, tenantID string, fn func(*ThrottleState)) error
}

// storeTimedClock reads time from the shared store clock (m.now) and waits
// with the replica's timers.
type storeTimedClock struct{ m *Mgmt }

func (c storeTimedClock) Now() time.Time                         { return c.m.now() }
func (c storeTimedClock) After(d time.Duration) <-chan time.Time { return c.m.clock.After(d) }

// sweepPending enqueues every pending object this replica isn't already
// holding in a queue. It runs at Start and every reaper tick, so an object
// whose mint claim was held by a replica that died is picked up again once
// the claim goes stale (mint claims make concurrent sweeps safe: H02).
func (m *Mgmt) sweepPending(ctx context.Context) error {
	pending, err := m.store.ListObjects(ctx, ObjectFilter{Status: []string{StatusPending}})
	if err != nil {
		return err
	}
	for _, o := range pending {
		m.enqueue(o.TenantID, o.ID)
	}
	return nil
}

func (m *Mgmt) enqueue(tenantID, objectID string) {
	m.mu.Lock()
	if m.queued[objectID] {
		m.mu.Unlock()
		return
	}
	m.queued[objectID] = true
	q := m.queues[tenantID]
	if q == nil {
		q = make(chan string, 100_000)
		m.queues[tenantID] = q
		m.wg.Add(1)
		go m.mintWorker(tenantID, q)
	}
	m.mu.Unlock()
	select {
	case q <- objectID:
	default:
		m.mu.Lock()
		delete(m.queued, objectID)
		m.mu.Unlock()
		m.log.Error("mint queue full", "tenant", tenantID)
	}
}

// QueueDepth is twd_registration_queue for a tenant.
func (m *Mgmt) QueueDepth(tenantID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queues[tenantID])
}

func (m *Mgmt) mintWorker(tenantID string, q chan string) {
	defer m.wg.Done()
	for {
		var id string
		select {
		case <-m.ctx.Done():
			return
		case id = <-q:
		}
		m.mu.Lock()
		delete(m.queued, id)
		m.mu.Unlock()
		for attempt := 1; ; attempt++ {
			t, err := m.store.GetTenant(m.ctx, tenantID)
			if err != nil {
				break
			}
			th := m.throttle(t)
			// The throttle token is taken inside mintOne, after the claim, so
			// replicas don't spend the shared budget on objects another
			// replica is minting or has minted (S2: up to 2/3 of tokens were
			// wasted with 3 replicas).
			created, err := m.mintOne(m.ctx, t, id, th)
			if m.ctx.Err() != nil {
				return
			}
			if err == nil {
				if created {
					th.Success()
				}
				break
			}
			var ae *tsapi.APIError
			if errors.As(err, &ae) && (ae.Status == http.StatusTooManyRequests || ae.Status >= 500) {
				d := th.Backoff(ae.RetryAfter)
				m.log.Warn("mint rate-limited; backing off", "tenant", tenantID, "status", ae.Status, "backoff", d)
				continue // retry the same object after the pause (not counted)
			}
			if attempt >= m.cfg.MintRetries || m.ctx.Err() != nil {
				m.log.Error("mint failed", "tenant", tenantID, "object", id, "err", err)
				m.store.UpdateObject(m.ctx, id, nil, func(o *Object) error {
					if o.Status == StatusPending {
						o.Status = StatusFailed
						o.UpdatedAt = m.now().UnixMilli()
					}
					return nil
				})
				m.audit(m.ctx, AuditRecord{TenantID: tenantID, ObjectID: id, Kind: "deny", Code: "bootstrap_failed"},
					map[string]any{"message": err.Error()})
				m.notify(id)
				break
			}
			m.log.Warn("mint failed; retrying", "object", id, "attempt", attempt, "err", err)
			select {
			case <-m.ctx.Done():
				return
			case <-m.clock.After(time.Duration(attempt) * time.Second):
			}
		}
	}
}

// custodyName is where an object's auth key lives: one entry per minted key
// (…/authkey/<keyID>), so two replicas minting the same object can never
// overwrite or delete each other's entry (H02). Objects minted before keyed
// names keep the legacy name (…/authkey) until they join or are killed.
func custodyName(o Object) string { return custodyKeyName(o, o.KeyID) }

func custodyKeyName(o Object, keyID string) string {
	if keyID == "" {
		return legacyCustodyName(o)
	}
	return legacyCustodyName(o) + "/" + keyID
}

func legacyCustodyName(o Object) string { return "twd/" + o.TenantID + "/" + o.ID + "/authkey" }

// deleteCustody removes the object's current key entry and any legacy one.
func (m *Mgmt) deleteCustody(ctx context.Context, o Object) {
	if o.KeyID != "" {
		m.custody.Delete(ctx, custodyName(o))
	}
	m.custody.Delete(ctx, legacyCustodyName(o))
}

// MintClaimTTL is how long a replica's claim on minting an object stays
// valid without being refreshed (each attempt refreshes it). It must exceed
// one CreateKey + custody Put; after it, another replica may take over.
const MintClaimTTL = 60 * time.Second

// mintOne mints one single-use, preauthorized, ≤5-min key tagged with the
// object's tags, and puts it in custody. The key is never logged.
func (m *Mgmt) mintOne(ctx context.Context, t Tenant, id string, th *Throttle) (created bool, err error) {
	sp := mintSpans{start: time.Now()}
	o, err := m.store.GetObject(ctx, id)
	if err != nil || o.Status != StatusPending {
		return false, nil // gone, killed or already minted
	}
	// Claim the mint (H02): only one replica calls CreateKey for an object.
	now := m.now()
	sp.queued = now.Sub(time.UnixMilli(o.CreatedAt)) // create → this attempt (store clock; both sides ms)
	sp.mark(&sp.load)
	if ok, err := m.store.ClaimMint(ctx, id, m.replicaID, now, now.Add(-MintClaimTTL)); err != nil {
		return false, err
	} else if !ok {
		return false, nil // another replica holds a fresh claim (or it is no longer pending)
	}
	// The claim covers this attempt only: released on every exit, so a
	// replica backing off (429/5xx) or dying between attempts never blocks
	// the others. Correctness does not rest on the TTL: keyed custody and
	// the KeyID check below make a late duplicate undo only its own key.
	defer m.store.ReleaseMint(context.WithoutCancel(ctx), id, m.replicaID)
	sp.mark(&sp.claim)
	// Fast path: a pooled key (already minted, and its throttle token
	// already spent by the refill).
	if handled, err := m.adoptWarmKey(ctx, t, o, &sp); handled {
		return false, err
	}
	// Only now take a registration token (the claim says this replica will
	// mint), then refresh the claim: the wait may have been long.
	if th != nil {
		if err := th.Wait(ctx); err != nil {
			return false, err
		}
		sp.mark(&sp.throttle)
		now := m.now()
		if ok, err := m.store.ClaimMint(ctx, id, m.replicaID, now, now.Add(-MintClaimTTL)); err != nil || !ok {
			return false, err // lost it meanwhile (killed, minted, or the claim went stale)
		}
		sp.mark(&sp.reclaim)
	}
	api, err := m.broker.API(ctx, t, purposeMint)
	if err != nil {
		return false, err
	}
	sp.mark(&sp.broker)
	k, err := api.CreateKey(ctx, tsapi.KeyOptions{Tags: o.Tags, Ephemeral: o.Ephemeral, Preauthorized: true,
		ExpirySeconds: int(KeyExpiry / time.Second), Description: "twd " + o.ID})
	if err != nil {
		return false, err
	}
	sp.mark(&sp.createKey)
	own := custodyKeyName(o, k.ID)
	if err := m.custody.Put(ctx, own, []byte(k.Secret)); err != nil {
		api.DeleteKey(ctx, k.ID)
		return true, err
	}
	sp.mark(&sp.custody)
	exp := m.now().Add(KeyExpiry - 15*time.Second).UnixMilli() // released only with ≥15 s of life left
	killed := false
	actx, ai := m.intend(ctx, AuditRecord{TenantID: t.ID, ObjectID: id, Kind: "mint"}, map[string]any{"keyID": k.ID, "tags": o.Tags,
		"mode": t.MintMode, "ephemeral": o.Ephemeral})
	_, err = m.store.UpdateObject(actx, id, nil, func(o *Object) error {
		if o.Status != StatusPending || o.KeyID != "" {
			killed = true // killed meanwhile, or (claim expired mid-mint) minted by someone else
			ai.Skip()
			return nil
		}
		o.Status, o.KeyID, o.KeyExpires, o.UpdatedAt = StatusMinted, k.ID, exp, m.now().UnixMilli()
		return nil
	})
	if err != nil || killed {
		// Undo only what this attempt created: its key and its own custody
		// entry. Never the object's (another replica's) credential.
		api.DeleteKey(ctx, k.ID)
		m.custody.Delete(ctx, own)
		return true, err
	}
	sp.mark(&sp.commit)
	m.recordIntent(ctx, ai, nil) // committed with the mint (H22)
	m.notify(id)
	m.log.Info("mint spans", append([]any{"object", id, "tenant", t.ID, "keyID", k.ID}, sp.attrs()...)...)
	sp.observe(t.ID, false) // metrics.go
	return true, nil
}

func (m *Mgmt) notify(id string) {
	m.mu.Lock()
	if ch, ok := m.waiters[id]; ok {
		close(ch)
		delete(m.waiters, id)
	}
	m.mu.Unlock()
}

func (m *Mgmt) changed(id string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.waiters[id]
	if !ok {
		ch = make(chan struct{})
		m.waiters[id] = ch
	}
	return ch
}

// Credential is GET /v0/objects/{id}/credential. ExpiresAt/Tags/Hostname/
// ControlURL are additions.
type Credential struct {
	AuthKey    string   `json:"authKey"`
	ExpiresAt  int64    `json:"expiresAt"`
	Tags       []string `json:"tags"`
	Hostname   string   `json:"hostname"`
	Ephemeral  bool     `json:"ephemeral"`
	ControlURL string   `json:"controlURL,omitempty"`
}

func (c Credential) String() string { return "mgmt.Credential{[REDACTED]}" }

// Credential releases the object's auth key to the namespace lease holder
// at epoch (fenced in the store). hostID "" (unix-socket callers that don't
// name themselves) fences on the epoch alone. It waits up to wait for a
// pending mint.
func (m *Mgmt) Credential(ctx context.Context, id, hostID string, epoch int64, wait time.Duration) (Credential, error) {
	called := time.Now()
	deadline := m.now().Add(wait)
	for {
		ch := m.changed(id)
		o, err := m.store.GetObject(ctx, id)
		if err != nil {
			return Credential{}, err
		}
		switch o.Status {
		case StatusKilled:
			return Credential{}, errf(410, "killed", "object %s was killed (%s)", id, o.KillReason)
		case StatusFailed:
			return Credential{}, errf(502, "bootstrap_failed", "minting a key for %s failed", id)
		case StatusReady:
			return Credential{}, errf(409, "already_joined", "object %s already joined; rejoin from saved node state", id)
		case StatusAssigned:
			return Credential{}, errf(409, "already_joined", "object %s was assigned a pre-joined node (%s); it needs no key", id, o.StableNodeID)
		case StatusTransferred:
			return Credential{}, errf(410, "killed", "pool node %s was transferred (%s)", id, o.KillReason)
		case StatusMinted:
			if m.now().UnixMilli() >= o.KeyExpires {
				// Expired unused key: revoke it and mint again.
				m.store.UpdateObject(ctx, id, nil, func(o *Object) error {
					if o.Status == StatusMinted {
						o.Status, o.KeyID = StatusPending, ""
					}
					return nil
				})
				m.revokeKey(ctx, o)
				m.enqueue(o.TenantID, id)
				break
			}
			fence := &Fence{Lease: o.Lease, Holder: hostID, Epoch: epoch}
			// The release decision (the fenced commit) and its record commit
			// together (H22); the outcome is amended below.
			actx, ai := m.intend(ctx, AuditRecord{TenantID: o.TenantID, ObjectID: id, Kind: "credential"},
				map[string]any{"host": hostID, "epoch": epoch, "keyID": o.KeyID})
			if _, err := m.store.UpdateObject(actx, id, fence, func(*Object) error { return nil }); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					m.audit(ctx, AuditRecord{TenantID: o.TenantID, ObjectID: id, Kind: "deny", Code: "lease_lost"},
						map[string]any{"host": hostID, "epoch": epoch})
				}
				return Credential{}, err
			}
			secret, err := m.custody.Get(ctx, custodyName(o))
			if err != nil && o.KeyID != "" {
				secret, err = m.custody.Get(ctx, legacyCustodyName(o)) // minted before keyed custody names
			}
			if err != nil {
				m.recordIntent(ctx, ai, map[string]any{"released": false, "error": err.Error()})
				return Credential{}, fmt.Errorf("custody: %w", err)
			}
			t, _ := m.store.GetTenant(ctx, o.TenantID)
			m.recordIntent(ctx, ai, nil)
			m.log.Info("credential released", "object", id, "host", hostID, "keyID", o.KeyID,
				"waited_ms", time.Since(called).Milliseconds(), "since_create_ms", m.now().UnixMilli()-o.CreatedAt)
			return Credential{AuthKey: string(secret), ExpiresAt: o.KeyExpires, Tags: o.Tags, Hostname: o.Hostname,
				Ephemeral: o.Ephemeral, ControlURL: t.ControlURL}, nil
		}
		left := deadline.Sub(m.now())
		if left <= 0 {
			e := errf(503, "identity_pending", "object %s is waiting on the registration queue (depth %d)", id, m.QueueDepth(o.TenantID))
			return Credential{}, e
		}
		// m.changed only fires for mints on this replica; another replica's
		// mint is seen by re-reading the store (H05).
		select {
		case <-ch:
		case <-time.After(min(left, credentialPoll)):
		case <-ctx.Done():
			return Credential{}, ctx.Err()
		}
	}
}

// credentialPoll is how often a waiting credential request re-reads the
// object, to see a mint done by another replica.
const credentialPoll = 250 * time.Millisecond

// ReadyRequest is the host's PATCH {status: ready, …}. Epoch is required:
// the commit is always fenced on (lease, caller host, epoch) (H2).
type ReadyRequest struct {
	NodeID       string `json:"nodeID"`
	StableNodeID string `json:"stableNodeID"`
	MagicDNS     string `json:"magicDNS"`
	Epoch        int64  `json:"epoch,omitempty"`
}

func (m *Mgmt) Ready(ctx context.Context, id, hostID string, r ReadyRequest) (Object, error) {
	o, err := m.store.GetObject(ctx, id)
	if err != nil {
		return o, err
	}
	// Only the host holding the object's lease may report it ready (RT3-02).
	// hostd always sends the epoch; a host that omits it is still bound to
	// the holder. Epoch 0 with no host (in-process callers) stays unfenced.
	var fence *Fence
	if r.Epoch > 0 || hostID != "" {
		fence = &Fence{Lease: o.Lease, Holder: hostID, Epoch: r.Epoch}
	}
	wasKilled, wrongNode, assignedTo := false, false, ""
	// The ready record commits with the status change (H22); the node
	// certificate outcome is added below.
	actx, ai := m.intend(ctx, AuditRecord{TenantID: o.TenantID, ObjectID: id, Kind: "ready"},
		map[string]any{"nodeID": r.NodeID, "stableNodeID": r.StableNodeID, "magicDNS": r.MagicDNS, "host": hostID})
	o, err = m.store.UpdateObject(actx, id, fence, func(o *Object) error {
		if !o.Live() {
			wasKilled = true
			ai.Skip()
			if r.StableNodeID != "" || r.NodeID != "" {
				// Killed while joining: the device that just appeared must go
				// too. Record it durably in this write (F21).
				o.NodeID, o.StableNodeID = r.NodeID, r.StableNodeID
				o.DeviceDeleteAt = m.now().UnixMilli()
			}
			return nil
		}
		if o.Status == StatusAssigned && r.StableNodeID != o.StableNodeID {
			// An assigned object may only become ready on the node it was given.
			wrongNode, assignedTo = true, o.StableNodeID
			return errWrongNode
		}
		o.Status, o.NodeID, o.StableNodeID, o.MagicDNS, o.UpdatedAt = StatusReady, r.NodeID, r.StableNodeID, r.MagicDNS, m.now().UnixMilli()
		return nil
	})
	if wrongNode {
		return o, errf(409, "identity_conflict", "object %s was assigned node %s, not %s", id, assignedTo, r.StableNodeID)
	}
	if err != nil {
		if errors.Is(err, ErrLeaseLost) {
			m.audit(ctx, AuditRecord{TenantID: o.TenantID, ObjectID: id, Kind: "deny", Code: "lease_lost"},
				map[string]any{"host": hostID, "epoch": r.Epoch, "op": "ready"})
		}
		return o, err
	}
	m.deleteCustody(ctx, o)
	if wasKilled {
		// Killed while joining: the device that just appeared must go too.
		// The fence held above, so this is the lease holder's own report.
		o.NodeID, o.StableNodeID = r.NodeID, r.StableNodeID
		m.deleteDeviceClaimed(ctx, o, false) // claimed, recorded; the sweep retries if this fails
		return o, errf(410, "killed", "object %s was killed while joining; device deleted", id)
	}
	// Node certificate (ADR-010): binds this node to the object's tags, lim
	// and lineage under the tenant's OBO root key. Receivers fetch it by
	// stable node ID and compute effective caps = CapMap ∩ lim (RT-01).
	detail := map[string]any{}
	if cert, err := m.signNodeCert(ctx, o); err != nil {
		m.log.Error("node certificate not issued", "object", id, "err", err)
		detail["certError"] = err.Error()
	} else if o2, err := m.store.UpdateObject(ctx, id, nil, func(x *Object) error {
		if x.Live() {
			x.Cert = cert
		}
		return nil
	}); err == nil {
		o = o2
		if len(o.Caps) > 0 {
			detail["lim"] = o.Caps
		}
		detail["cert"] = true
	}
	m.recordIntent(ctx, ai, detail)
	m.notify(id)
	return o, nil
}

// RemintRequest is POST /v0/objects/{id}/remint: the host's rejoin from
// saved node state was rejected by control (the device expired or was
// removed; ephemeral nodes that are offline during a restart), so the
// object needs a fresh key under the same hostname. Live objects only: the
// object goes back to "pending" with its node IDs cleared and is queued
// for a mint; the host then takes the credential and reports ready with
// the new node ID as usual.
type RemintRequest struct {
	Epoch  int64  `json:"epoch,omitempty"`
	Reason string `json:"reason"`
}

func (m *Mgmt) Remint(ctx context.Context, id, hostID string, r RemintRequest) (Object, error) {
	old, err := m.store.GetObject(ctx, id)
	if err != nil {
		return old, err
	}
	switch old.Status {
	case StatusKilled:
		return old, errf(410, "killed", "object %s was killed (%s)", id, old.KillReason)
	case StatusFailed:
		return old, errf(502, "bootstrap_failed", "minting a key for %s failed", id)
	}
	if r.Epoch <= 0 {
		return old, errf(400, "bad_request", "epoch is required for a remint (the lease epoch the host holds)")
	}
	fence := &Fence{Lease: old.Lease, Holder: hostID, Epoch: r.Epoch}
	actx, ai := m.intend(ctx, AuditRecord{TenantID: old.TenantID, ObjectID: id, Kind: "remint"},
		map[string]any{"host": hostID, "reason": r.Reason, "previousStatus": old.Status, "previousStableNodeID": old.StableNodeID,
			"hostname": old.Hostname}) // committed with the reset (H22)
	o, err := m.store.UpdateObject(actx, id, fence, func(o *Object) error {
		if !o.Live() || o.Status == StatusPending {
			return nil
		}
		o.Status, o.NodeID, o.StableNodeID, o.MagicDNS, o.KeyID, o.KeyExpires = StatusPending, "", "", "", "", 0
		o.Cert = "" // the certificate named the dead node; ready re-issues one for the new node (RT-01)
		o.UpdatedAt = m.now().UnixMilli()
		return nil
	})
	if err != nil {
		return o, err
	}
	if old.Status == StatusMinted {
		m.revokeKey(ctx, old) // an unused key for a node that no longer exists
	}
	// The previous device is dead (control rejected its key) but may still
	// be listed: an ephemeral device that logged out is removed by control
	// only after a while, an admin-removed one is already gone (404).
	prevDeleted := false
	if old.StableNodeID != "" || old.NodeID != "" {
		if err := m.deleteDevice(ctx, old); err != nil {
			if !tsapi.IsNotFound(err) {
				m.log.Warn("remint: deleting the previous device failed", "object", id, "device", old.StableNodeID, "err", err)
			}
		} else {
			prevDeleted = true
		}
	}
	m.recordIntent(ctx, ai, map[string]any{"previousDeviceDeleted": prevDeleted})
	if old.Status != StatusPending {
		m.enqueue(o.TenantID, id)
	}
	m.notify(id)
	return o, nil
}

// ---- kill ----

// KillRequest is POST /v0/objects/{id}/kill.
type KillRequest struct {
	Cascade bool   `json:"cascade"`
	Reason  string `json:"reason"`
	By      string `json:"-"`
}

// KillResult reports what a kill did. Pushed lists the objects whose host
// acknowledged the tier-1 push; DevicesDeleting those whose device delete
// is scheduled (Config.DeviceDeleteDelay).
type KillResult struct {
	Killed          []string          `json:"killed"`
	DevicesDeleted  []string          `json:"devicesDeleted"`
	DevicesDeleting []string          `json:"devicesDeleting,omitempty"`
	Pushed          []string          `json:"pushed,omitempty"`
	PushMS          float64           `json:"pushMs,omitempty"`
	Errors          map[string]string `json:"errors,omitempty"`
	Warnings        []KillWarning     `json:"warnings,omitempty"`
}

// KillWarning is a non-fatal finding of a kill (the kill itself held).
type KillWarning struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Objects []string `json:"objects,omitempty"`
	Fix     string   `json:"fix,omitempty"`
}

// CodeDeviceDeleteUnavailable: the tenant's credential may not delete
// devices (an OAuth client without the devices:core scope).
const CodeDeviceDeleteUnavailable = "device_delete_unavailable"

// DeviceScopeFix tells the operator how to let kills delete devices.
const DeviceScopeFix = "the OAuth client lacks the devices:core scope: in the admin console (Settings › Trust credentials) generate a new OAuth client with Auth Keys (write) AND Devices › Core (write), both with tag tag:twd, write it to the same credential file (0600), then revoke the old client; until then remove killed devices in the admin console (Machines)"

// errDeviceDeleteUnavailable wraps a 403 on a device delete.
var errDeviceDeleteUnavailable = errors.New(CodeDeviceDeleteUnavailable)

func (r *KillResult) warnDeviceDelete(id string) {
	for i := range r.Warnings {
		if r.Warnings[i].Code == CodeDeviceDeleteUnavailable {
			r.Warnings[i].Objects = append(r.Warnings[i].Objects, id)
			return
		}
	}
	r.Warnings = append(r.Warnings, KillWarning{Code: CodeDeviceDeleteUnavailable, Objects: []string{id}, Fix: DeviceScopeFix,
		Message: "killed (unreachable, tombstoned), but the tenant's credential may not delete devices, so the device stays listed in the admin console"})
}

// Kill marks the object (and with cascade, every descendant) killed, pushes
// the kill to the lease-holding host (tier 1, immediate; the heartbeat's
// revokeObjects is the fallback), deletes devices via the tenant API (tier
// 2; deferred by DeviceDeleteDelay), revokes unused keys, wipes custody, and
// records audit.
// lookupErr turns a failed lookup into its API error: a real not-found is
// notFound (a 404), anything else (a store outage, a lock timeout) keeps its
// own retryable or internal mapping instead of masquerading as a 404 that
// clients would not retry (found by S9: a burst got 404 unknown_tenant for an
// existing tenant).
func lookupErr(err error, notFound *Error) error {
	if errors.Is(err, ErrNotFound) {
		return notFound
	}
	return asAPIError(err)
}

// errAlreadyDead: the object was killed (or failed) between Kill's read and
// its write, by another replica or the reaper.
var errAlreadyDead = errors.New("already dead")

func (m *Mgmt) Kill(ctx context.Context, id string, r KillRequest) (KillResult, error) {
	res := KillResult{Killed: []string{}, DevicesDeleted: []string{}, Errors: map[string]string{}}
	root, err := m.store.GetObject(ctx, id)
	if err != nil {
		return res, err
	}
	targets := []Object{root}
	if r.Cascade {
		seen := map[string]bool{root.ID: true}
		for i := 0; i < len(targets); i++ {
			kids, err := m.store.ListObjects(ctx, ObjectFilter{ParentID: targets[i].ID})
			if err != nil {
				return res, err
			}
			for _, k := range kids {
				if !seen[k.ID] {
					seen[k.ID] = true
					targets = append(targets, k)
				}
			}
		}
	}
	now := m.now()
	type killedObj struct {
		o    Object
		prev string
		ai   *auditIntent // committed with the kill (H22); outcomes amended below
	}
	var done []killedObj
	byHost := map[string][]string{}
	for _, o := range targets {
		if !o.Live() {
			continue
		}
		prev := o.Status
		actx, ai := m.intend(ctx, AuditRecord{TenantID: o.TenantID, ObjectID: o.ID, Kind: "kill", ActingFor: r.By},
			map[string]any{"reason": r.Reason, "cascade": r.Cascade, "requested": id, "root": o.RootID, "parent": o.ParentID, "prevStatus": prev})
		killed, err := m.store.UpdateObject(actx, o.ID, nil, func(x *Object) error {
			if !x.Live() {
				return errAlreadyDead // another replica's kill or reaper won (H09)
			}
			x.Status, x.KillReason, x.UpdatedAt = StatusKilled, r.Reason, now.UnixMilli()
			if x.StableNodeID != "" || x.NodeID != "" {
				// The device delete is recorded in the kill's own write: no
				// window where the kill commits but the cleanup is lost (F21).
				x.DeviceDeleteAt = now.Add(m.cfg.DeviceDeleteDelay).UnixMilli()
			}
			x.KilledAt, x.KilledBy, x.KillCascade, x.KillRoot = now.UnixMilli(), r.By, x.ID != id, id
			return nil
		})
		if errors.Is(err, errAlreadyDead) {
			continue // killed concurrently elsewhere: its tombstone and audit stand
		}
		if err != nil {
			res.Errors[o.ID] = err.Error()
			continue
		}
		res.Killed = append(res.Killed, o.ID)
		m.notify(o.ID)
		if l, err := m.store.GetLease(ctx, o.Lease); err == nil && l.Holder != "" {
			m.store.AddRevocation(ctx, l.Holder, o.ID, now)
			byHost[l.Holder] = append(byHost[l.Holder], o.ID)
		}
		done = append(done, killedObj{killed, prev, ai})
	}
	// Tier 1: push to the hosts, all objects in parallel, before any device
	// delete (the RST must leave while the node is still in peers' netmaps).
	ps := time.Now()
	var pmu sync.Mutex
	var wg sync.WaitGroup
	for host, ids := range byHost {
		for _, oid := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := m.push(host, "/v1/push/kill", map[string]any{"objectID": oid, "reason": r.Reason}); err == nil {
					pmu.Lock()
					res.Pushed = append(res.Pushed, oid)
					pmu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	if len(byHost) > 0 {
		res.PushMS = float64(time.Since(ps).Microseconds()) / 1000
	}
	for _, k := range done {
		o, killed, prev, ai := k.o, k.o, k.prev, k.ai
		m.deleteCustody(ctx, o)
		if prev != StatusReady {
			m.revokeKey(ctx, killed)
		}
		deleted, scheduled, unavailable := false, false, false
		if killed.StableNodeID != "" || killed.NodeID != "" {
			// Durable (F21): the kill's write recorded device_delete_at; any
			// replica's sweep finishes this delete if this one can't.
			if m.cfg.DeviceDeleteDelay > 0 {
				m.scheduleDelete(killed)
				scheduled = true
				res.DevicesDeleting = append(res.DevicesDeleting, o.ID)
				m.mu.Lock()
				unavailable = m.noDeviceScope[o.TenantID]
				m.mu.Unlock()
				if unavailable {
					res.warnDeviceDelete(o.ID) // learned from an earlier 403; the scheduled delete still tries
				}
			} else if _, err := m.deleteDeviceClaimedAudit(ctx, killed, false, false); errors.Is(err, errDeviceDeleteUnavailable) {
				unavailable = true
				res.warnDeviceDelete(o.ID)
			} else if err != nil {
				res.Errors[o.ID] = "device delete: " + err.Error()
			} else {
				deleted = true
				res.DevicesDeleted = append(res.DevicesDeleted, o.ID)
			}
		}
		detail := map[string]any{"deviceDeleted": deleted, "pushed": slices.Contains(res.Pushed, o.ID)}
		if scheduled {
			detail["deviceDeleteIn"] = m.cfg.DeviceDeleteDelay.String()
		}
		if unavailable {
			detail["deviceDelete"] = CodeDeviceDeleteUnavailable
			detail["fix"] = DeviceScopeFix
		}
		m.recordIntent(ctx, ai, detail)
	}
	if len(res.Errors) == 0 {
		res.Errors = nil
	}
	return res, nil
}

// push POSTs body to the host's push socket (best effort; bounded by
// PushTimeout). Hosts without a push URL get nothing (heartbeat fallback).
func (m *Mgmt) push(hostID, path string, body any) error {
	u := m.pushTarget(hostID)
	if u == "" {
		return errors.New("host has no push URL")
	}
	err := m.pushTo(u, hostID, path, body)
	if err == nil {
		return nil
	}
	// The cached target may be stale (the host re-registered through another
	// replica): drop it, re-read the hosts row, retry once if it changed.
	m.mu.Lock()
	delete(m.pushURLs, hostID)
	m.mu.Unlock()
	if u2 := m.pushTarget(hostID); u2 != "" && u2 != u {
		return m.pushTo(u2, hostID, path, body)
	}
	return err
}

func (m *Mgmt) pushTo(u, hostID, path string, body any) error {
	if u == EmbeddedPushURL {
		// A host embedded in this process (single-process twd up): served
		// through its handler, no dial. Another replica would see the same
		// persisted target and fail here, which is right: only the process
		// that embeds the host can reach it.
		m.mu.Lock()
		inproc := m.inprocPush[hostID]
		m.mu.Unlock()
		return m.pushInProcess(hostID, inproc, path, body)
	}
	base := "http://hostd"
	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	if sock, ok := strings.CutPrefix(u, "unix://"); ok {
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}
	} else {
		// A tsnet host: dial its node through mgmt's own node.
		base = u
		dial = m.cfg.TailnetDial
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
	}
	hc := &http.Client{Timeout: m.cfg.PushTimeout, Transport: &http.Transport{DialContext: dial}}
	defer hc.CloseIdleConnections()
	b, _ := json.Marshal(body)
	resp, err := hc.Post(base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		m.log.Debug("push failed", "host", hostID, "path", path, "err", err)
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("push %s: %s", path, resp.Status)
	}
	return nil
}

// pushInProcess serves one push through the embedded host's handler (no
// network; bounded by PushTimeout like a dialled push).
func (m *Mgmt) pushInProcess(hostID string, h http.Handler, path string, body any) error {
	if h == nil {
		return fmt.Errorf("embedded host %s has no push handler", hostID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.PushTimeout)
	defer cancel()
	b, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://hostd"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "inproc"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code/100 != 2 {
		m.log.Debug("push failed", "host", hostID, "path", path, "status", rec.Code)
		return fmt.Errorf("push %s: %d %s", path, rec.Code, http.StatusText(rec.Code))
	}
	return nil
}

type pendingDelete struct {
	o     Object
	timer *time.Timer
	at    int64 // unix ms when the delete runs
}

func (m *Mgmt) scheduleDelete(o Object) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pending[o.ID]; ok {
		return
	}
	pd := &pendingDelete{o: o, at: m.now().Add(m.cfg.DeviceDeleteDelay).UnixMilli()}
	pd.timer = time.AfterFunc(m.cfg.DeviceDeleteDelay, func() { m.runDelete(o.ID) })
	m.pending[o.ID] = pd
}

// runDelete performs a scheduled device delete (once).
func (m *Mgmt) runDelete(id string) { m.runDeleteForce(id, false) }

// runDeleteForce runs a scheduled delete now; force (FlushDeletes) runs it
// even before it is due.
func (m *Mgmt) runDeleteForce(id string, force bool) {
	m.mu.Lock()
	pd := m.pending[id]
	delete(m.pending, id)
	m.mu.Unlock()
	if pd == nil {
		return
	}
	pd.timer.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.deleteDeviceClaimed(ctx, pd.o, force)
}

// DeviceDeleteRetry is how long a failed device delete waits before any
// replica's sweep tries again; DeviceDeleteClaimTTL is how long a claimed
// delete stays claimed before it is due again (claimer died or couldn't
// record the outcome).
const (
	DeviceDeleteRetry    = 30 * time.Second
	DeviceDeleteClaimTTL = 2 * time.Minute
)

// deleteDeviceClaimed claims the object's durable device delete (so exactly
// one replica runs it), deletes the device, audits, and on a retryable
// failure puts it back as due in DeviceDeleteRetry. ran is false when the
// delete was not due or another replica claimed it.
func (m *Mgmt) deleteDeviceClaimed(ctx context.Context, o Object, force bool) (ran bool, err error) {
	return m.deleteDeviceClaimedAudit(ctx, o, force, true)
}

// deleteDeviceClaimedAudit is deleteDeviceClaimed; audit=false is the
// inline delete inside Kill, whose kill record already says deviceDeleted.
func (m *Mgmt) deleteDeviceClaimedAudit(ctx context.Context, o Object, force, audit bool) (ran bool, err error) {
	now := m.now().UnixMilli()
	if force {
		now = math.MaxInt64
	}
	if ok, cerr := m.store.ClaimDeviceDelete(ctx, o.ID, now); cerr != nil || !ok {
		return false, cerr
	}
	id := o.ID
	st := time.Now()
	err = m.deleteDevice(ctx, o)
	detail := map[string]any{"nodeID": o.StableNodeID, "ms": time.Since(st).Milliseconds()}
	if errors.Is(err, errDeviceDeleteUnavailable) {
		detail["error"], detail["code"], detail["fix"] = err.Error(), CodeDeviceDeleteUnavailable, DeviceScopeFix
		m.log.Warn("device delete unavailable: the tenant's OAuth client has no devices:core scope", "object", id, "tenant", o.TenantID)
	} else if err != nil {
		detail["error"] = err.Error()
		m.log.Warn("device delete failed", "object", id, "err", err)
	}
	// The outcome write and its audit record commit together (H22).
	octx := context.WithoutCancel(ctx)
	var ai *auditIntent
	if audit {
		octx, ai = m.intend(octx, AuditRecord{TenantID: o.TenantID, ObjectID: id, Kind: "device_delete"}, detail)
	}
	switch {
	case err == nil, errors.Is(err, errDeviceDeleteUnavailable):
		// Done (or not ours to do). If this write fails the claim lapses
		// and the delete re-runs: harmless, a gone device is a success.
		m.store.SetDeviceDelete(octx, id, -1)
	default:
		// Retry sooner than the claim would lapse; if this write fails too
		// (store blip), the claim lapsing still brings it back (H21).
		m.store.SetDeviceDelete(octx, id, m.now().Add(DeviceDeleteRetry).UnixMilli())
	}
	m.recordIntent(ctx, ai, nil)
	return true, err
}

// sweepDeviceDeletes runs every due durable device delete: ones whose
// replica died before its timer fired (H07) and failed ones being retried.
func (m *Mgmt) sweepDeviceDeletes(ctx context.Context) {
	ids, err := m.store.DueDeviceDeletes(ctx, m.now().UnixMilli())
	if err != nil {
		m.log.Warn("device delete sweep", "err", err)
		return
	}
	for _, id := range ids {
		m.mu.Lock()
		_, local := m.pending[id]
		m.mu.Unlock()
		if local {
			continue // this replica's own timer will run it
		}
		o, err := m.store.GetObject(ctx, id)
		if err != nil {
			continue
		}
		m.deleteDeviceClaimed(ctx, o, false)
	}
}

// PendingDeletes is how many device deletes are scheduled.
func (m *Mgmt) PendingDeletes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// PurgeResult is POST /v0/tenants/{id}/purge.
type PurgeResult struct {
	Purged []string `json:"purged"`
	// WarmKeysRevoked counts the tenant's unused pooled keys revoked by the purge.
	WarmKeysRevoked int `json:"warmKeysRevoked,omitempty"`
}

// Purge forgets a tenant's killed and failed objects (twd down --purge):
// pending device deletes run first, then the rows go, so the same
// (workload, namespace, name) can be created again on the next deploy.
// Before this, a redeploy on the same state dir after `twd down --purge`
// was refused: the killed workload-node object (same name, same ID) made
// CreateObject answer 410 and the deployment's endpoint read "killed".
// Live objects are never touched; every removal is audited as "purge".
func (m *Mgmt) Purge(ctx context.Context, tenantID string, statuses ...string) (PurgeResult, error) {
	res := PurgeResult{Purged: []string{}}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return res, err
	}
	m.FlushDeletes()
	l := m.tenantLock(tenantID)
	l.Lock()
	defer l.Unlock()
	dead, err := m.store.DeleteDeadObjects(ctx, tenantID, statuses)
	if err != nil {
		return res, err
	}
	// A purge leaves nothing behind: the tenant's unused pooled keys are
	// revoked too (the pool refills on the next create or deploy).
	if n, err := m.drainWarm(ctx, tenantID); err != nil {
		m.log.Warn("purge: warm keys", "tenant", tenantID, "err", err)
	} else if n > 0 {
		res.WarmKeysRevoked = n
	}
	for _, o := range dead {
		res.Purged = append(res.Purged, o.ID)
		m.mu.Lock()
		delete(m.waiters, o.ID)
		m.mu.Unlock()
		m.audit(ctx, AuditRecord{TenantID: tenantID, ObjectID: o.ID, Kind: "purge"},
			map[string]any{"workload": o.Workload, "namespace": o.Namespace, "name": o.Name, "status": o.Status, "reason": o.KillReason})
	}
	return res, nil
}

// FlushDeletes runs every scheduled device delete now (in parallel).
func (m *Mgmt) FlushDeletes() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.pending))
	for id := range m.pending {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); m.runDeleteForce(id, true) }()
	}
	wg.Wait()
}

func (m *Mgmt) revokeKey(ctx context.Context, o Object) {
	if o.KeyID == "" {
		return
	}
	t, err := m.store.GetTenant(ctx, o.TenantID)
	if err != nil {
		return
	}
	if api, err := m.broker.API(ctx, t, purposeMint); err == nil {
		if err := api.DeleteKey(ctx, o.KeyID); err != nil {
			m.log.Warn("key revoke failed", "object", o.ID, "key_id", o.KeyID, "err", err)
		}
	}
}

func (m *Mgmt) deleteDevice(ctx context.Context, o Object) error {
	dev := o.StableNodeID
	if dev == "" {
		dev = o.NodeID
	}
	if dev == "" {
		return nil
	}
	t, err := m.store.GetTenant(ctx, o.TenantID)
	if err != nil {
		return err
	}
	api, err := m.broker.API(ctx, t, purposeLifecycle)
	if err != nil {
		return err
	}
	err = api.DeleteDevice(ctx, dev)
	if t.MintMode == MintOAuth && tsapi.IsForbidden(err) {
		m.mu.Lock()
		m.noDeviceScope[t.ID] = true
		m.deviceKept[o.ID] = true
		m.mu.Unlock()
		return fmt.Errorf("%w: %v", errDeviceDeleteUnavailable, err)
	}
	if err == nil && t.MintMode == MintOAuth {
		m.mu.Lock()
		delete(m.noDeviceScope, t.ID) // the scope was added (new client in the file)
		m.mu.Unlock()
	}
	return err
}

// ---- reaper ----

// Reap kills (cascade) every live object past its TTL and every live
// sub-agent whose parent is dead. It returns how many it killed.
func (m *Mgmt) Reap(ctx context.Context) (int, error) {
	live, err := m.store.ListObjects(ctx, ObjectFilter{Status: LiveStatuses})
	if err != nil {
		return 0, err
	}
	now := m.now().UnixMilli()
	n := 0
	for _, o := range live {
		reason := ""
		if o.TTLSeconds > 0 && now >= o.CreatedAt+o.TTLSeconds*1000 {
			reason = "ttl_expired"
		} else if o.ParentID != "" {
			p, err := m.store.GetObject(ctx, o.ParentID)
			if errors.Is(err, ErrNotFound) || (err == nil && !p.Live()) {
				reason = "parent_dead"
			}
		}
		if reason == "" {
			continue
		}
		cur, err := m.store.GetObject(ctx, o.ID)
		if err != nil || !cur.Live() {
			continue // killed by an earlier cascade in this pass
		}
		res, err := m.Kill(ctx, o.ID, KillRequest{Cascade: true, Reason: reason, By: "twd-reaper"})
		if err != nil {
			return n, err
		}
		n += len(res.Killed)
	}
	return n, nil
}

// ---- audit ingest / query ----

// Audit record kinds. Privileged kinds describe platform decisions and may
// only be produced by netd/hostd/mgmt; tenant code (ctx.tailnet.audit) can
// only produce KindTenantNote, which carries source "tenant".
const (
	KindTenantNote = "tenant_note"
	SourceMgmt     = "mgmt"
	SourceHost     = "host"
	SourceTenant   = "tenant"
)

// PrivilegedKinds are the kinds a tenant may never submit (RT-02).
var PrivilegedKinds = map[string]bool{"allow": true, "deny": true, "egress": true, "spawn": true, "kill": true,
	"create": true, "mint": true, "ready": true, "credential": true, "principal": true, "device_delete": true,
	"tenant": true, "deploy": true, "obo_root": true, "container_start": true, "container_stop": true,
	"image_push": true, "image_pin": true, "image_public": true,
	// ADR-029: public ingress records come from netd/hostd only.
	"public_request": true, "public_rejected": true, "public_object_created": true, "expose_on": true, "expose_off": true,
	// Workflow approvals (hostd/approvals.go): decisions are hostd's.
	"approval_requested": true,
	"approval_decision":  true,
	"approval_denied":    true,
	"approval_timeout":   true,
}

// checkAuditSource enforces the provenance contract on a host-submitted
// record: a "tenant" source may only carry a tenant_note, and a tenant_note
// must be marked source "tenant". Anything else is a host record. The
// submitter itself is authenticated by the route (roleHost + host token).
func checkAuditSource(source, kind string) *Error {
	switch source {
	case SourceTenant:
		if kind != KindTenantNote {
			return errf(400, "bad_request", "tenant-sourced audit records may only be kind %q, not %q", KindTenantNote, kind)
		}
	case "", SourceHost:
		if kind == KindTenantNote {
			return errf(400, "bad_request", "a %s record must carry source %q", KindTenantNote, SourceTenant)
		}
	case SourceMgmt:
		return errf(400, "bad_request", "hosts may not submit records with source %q", SourceMgmt)
	default:
		return errf(400, "bad_request", "unknown audit source %q", source)
	}
	return nil
}

// hostAuditRecord is the part of a host-submitted record mgmt indexes.
type hostAuditRecord struct {
	TS        any    `json:"ts"`
	ObjectID  string `json:"objectID"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	TraceID   string `json:"traceID"`
	ActingFor string `json:"actingFor"`
	PrevHash  string `json:"prevHash"`
	Hash      string `json:"hash"`
	HostSig   string `json:"hostSig"`
}

// IngestAudit stores host-submitted records on the host's chain. Every
// record is verified before anything is written (M4): its hash must be
// the hash of its content, its hostSig must verify with the host's pinned
// audit key (when the host registered one), and the batch must link:
// record i's prevHash is record i-1's hash, and the first prevHash is the
// chain head. A first prevHash of "" on a non-empty chain is a chain
// restart (hostd keeps its link in memory); it is stored and reported by
// verification, never spliced into history. A batch that starts behind
// the head (a retry after a lost response) resumes after the record that
// is the head. Anything else is 400 and nothing of the batch is stored.
func (m *Mgmt) IngestAudit(ctx context.Context, hostID string, recs []json.RawMessage) (int, ChainStatus, error) {
	chain := "host:" + hostID
	// Every host record must be signed by the key the host registered
	// (RT3-05): an unsigned chain would let any host write "host" records,
	// and VerifyChain only checks signatures when a key exists.
	host, err := m.store.GetHost(ctx, hostID)
	if err != nil {
		return 0, ChainStatus{}, lookupErr(err, errf(404, "host_not_registered", "host %s is not registered", hostID))
	}
	if host.AuditPubKey == "" {
		return 0, ChainStatus{}, errf(403, "audit_key_required", "host %s registered without an auditPubKey; audit records are refused", hostID)
	}
	pub, err := decodeB64(host.AuditPubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return 0, ChainStatus{}, errf(403, "audit_key_required", "host %s has an invalid auditPubKey", hostID)
	}
	// A host may only write records about objects of workloads it has been
	// assigned (now or earlier): records naming another tenant's objects are
	// attribution forgery, whatever the chain says (RT3-05).
	served := map[string]bool{}
	servedBy := func(o Object) bool {
		k := o.TenantID + "/" + o.Workload
		if v, ok := served[k]; ok {
			return v
		}
		ds, err := m.store.ListDeployments(ctx, DeploymentFilter{TenantID: o.TenantID, Workload: o.Workload, HostID: hostID}, Page{Limit: 1})
		v := err == nil && len(ds) > 0
		served[k] = v
		return v
	}
	parsed := make([]hostAuditRecord, len(recs))
	canons := make([][]byte, len(recs))
	for i, raw := range recs {
		r := &parsed[i]
		if err := json.Unmarshal(raw, r); err != nil {
			return 0, ChainStatus{}, errf(400, "bad_request", "record %d: %v", i, err)
		}
		if r.Hash == "" || r.Kind == "" {
			return 0, ChainStatus{}, errf(400, "bad_request", "record %d: kind and hash are required", i)
		}
		if e := checkAuditSource(r.Source, r.Kind); e != nil {
			e.Message = fmt.Sprintf("record %d: %s", i, e.Message)
			return 0, ChainStatus{}, e
		}
		if sig, err := base64.StdEncoding.DecodeString(r.HostSig); err != nil || !ed25519.Verify(pub, []byte(r.Hash), sig) {
			return i, ChainStatus{}, errf(400, "bad_host_sig", "record %d: hostSig does not verify under host %s's auditPubKey", i, hostID)
		}
		canon, err := Canonicalize(raw)
		if err != nil {
			return 0, ChainStatus{}, errf(400, "bad_request", "record %d: %v", i, err)
		}
		want, err := HashRecord(raw)
		if err != nil {
			return 0, ChainStatus{}, errf(400, "bad_request", "record %d: %v", i, err)
		}
		if want != r.Hash {
			return 0, ChainStatus{}, errf(400, "bad_request", "record %d: hash does not match the record's content", i)
		}
		if i > 0 && r.PrevHash != parsed[i-1].Hash {
			return 0, ChainStatus{}, errf(400, "bad_request", "record %d: prevHash does not link to record %d", i, i-1)
		}
		// RT3-05: a record naming another workload's object is attribution
		// forgery, whatever the chain says.
		if o, err := m.store.GetObject(ctx, r.ObjectID); err == nil && !servedBy(o) {
			return i, ChainStatus{}, errf(403, "audit_object_foreign", "record %d: object %s belongs to workload %s/%s, which host %s has never served", i, o.ID, o.TenantID, o.Workload, hostID)
		}
		canons[i] = canon
	}
	start := 0
	if len(parsed) > 0 {
		_, head, err := m.store.AuditHead(ctx, chain)
		if err != nil {
			return 0, ChainStatus{}, err
		}
		if first := parsed[0].PrevHash; first != head && first != "" {
			for k := range parsed {
				if parsed[k].Hash == head {
					start = k + 1 // already stored up to here
				}
			}
			if start == 0 {
				return 0, ChainStatus{}, errf(400, "bad_request", "record 0: prevHash does not link to the head of chain %s", chain)
			}
		}
	}
	for i := start; i < len(parsed); i++ {
		r := parsed[i]
		tenant := ""
		var obj *Object
		if o, err := m.store.GetObject(ctx, r.ObjectID); err == nil {
			if !servedBy(o) {
				return i, ChainStatus{}, errf(403, "audit_object_foreign", "record %d: object %s belongs to workload %s/%s, which host %s has never served", i, o.ID, o.TenantID, o.Workload, hostID)
			}
			tenant, obj = o.TenantID, &o
		}
		if _, err := m.store.AppendAudit(ctx, AuditRecord{Chain: chain, TS: fmt.Sprint(r.TS), TenantID: tenant, ObjectID: r.ObjectID,
			Kind: r.Kind, TraceID: r.TraceID, ActingFor: r.ActingFor, PrevHash: r.PrevHash, Hash: r.Hash, HostSig: r.HostSig,
			Raw: canons[i]}); err != nil {
			return i, ChainStatus{}, err
		}
		m.notifyHostRecord(obj, r.Kind, recs[i]) // notify.go
	}
	return len(recs), m.VerifyChain(ctx, chain), nil
}

// VerifyChain verifies one chain (host chains with the host's key, if registered).
func (m *Mgmt) VerifyChain(ctx context.Context, chain string) ChainStatus {
	var pub []byte
	if h, ok := strings.CutPrefix(chain, "host:"); ok {
		if host, err := m.store.GetHost(ctx, h); err == nil && host.AuditPubKey != "" {
			pub, _ = decodeB64(host.AuditPubKey)
		}
	}
	return VerifyChain(ctx, m.store, chain, pub)
}

// VerifyAll verifies every chain.
func (m *Mgmt) VerifyAll(ctx context.Context) ([]ChainStatus, bool, error) {
	chains, err := m.store.AuditChains(ctx)
	if err != nil {
		return nil, false, err
	}
	out, ok := []ChainStatus{}, true
	for _, c := range chains {
		st := m.VerifyChain(ctx, c)
		ok = ok && st.OK
		out = append(out, st)
	}
	return out, ok, nil
}

// QueryAudit returns matching records plus the status of every chain they are on.
func (m *Mgmt) QueryAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, []ChainStatus, error) {
	recs, err := m.store.ListAudit(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	var chains []string
	for _, r := range recs {
		if !slices.Contains(chains, r.Chain) {
			chains = append(chains, r.Chain)
		}
	}
	sts := []ChainStatus{}
	for _, c := range chains {
		sts = append(sts, m.VerifyChain(ctx, c))
	}
	if recs == nil {
		recs = []AuditRecord{}
	}
	for i := range recs {
		recs[i].Source = auditSource(recs[i])
	}
	return recs, sts, nil
}

// auditSource derives a record's provenance: mgmt-chain records are
// "mgmt"; host-chain records carry "source" in their hashed body ("host"
// when absent, for records from before RT-02).
func auditSource(r AuditRecord) string {
	if r.Chain == "mgmt" {
		return SourceMgmt
	}
	var raw struct {
		Source string `json:"source"`
	}
	if len(r.Raw) > 0 && json.Unmarshal(r.Raw, &raw) == nil && raw.Source != "" {
		return raw.Source
	}
	if r.Kind == KindTenantNote {
		return SourceTenant
	}
	return SourceHost
}

// ---- deploy preflight ----

// PreflightRequest asks whether the tenant policy lets a workload's tags
// reach its declared dependencies (twd.jsonc tailnet.connects) and whether
// every tag it will mint is in tagOwners.
type PreflightRequest struct {
	SrcTags  []string `json:"srcTags"`  // workload + object tags
	Connects []string `json:"connects"` // tags ("tag:db") or "*"; names are reported unchecked
	Port     string   `json:"port,omitempty"`
	// Service, with WorkloadTags, checks autoApprovers.services.
	Service      string   `json:"service,omitempty"`
	WorkloadTags []string `json:"workloadTags,omitempty"`
	// Public: the workload declares public routes, so tag:twd-public needs
	// the funnel nodeAttr (ADR-029 D6).
	Public bool `json:"public,omitempty"`
}

// PreflightMissing is one missing grant.
type PreflightMissing struct {
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	Snippet string `json:"snippet"`
}

// PreflightResult is the preflight verdict.
type PreflightResult struct {
	OK               bool               `json:"ok"`
	SrcTags          []string           `json:"srcTags,omitempty"`
	Missing          []PreflightMissing `json:"missing"`
	MissingTagOwners []string           `json:"missingTagOwners"`
	TagOwnersSnippet string             `json:"tagOwnersSnippet,omitempty"`
	Unchecked        []string           `json:"unchecked,omitempty"`
	// Service is the Tailscale Service the workload node would host;
	// ServiceUnapproved: the policy's autoApprovers.services doesn't let the
	// workload tags host it (control would never route it), ServiceSnippet
	// is the entry to add.
	Service           string `json:"service,omitempty"`
	ServiceUnapproved bool   `json:"serviceUnapproved,omitempty"`
	ServiceSnippet    string `json:"serviceSnippet,omitempty"`
	// MissingNodeAttrs names missing nodeAttrs ("funnel on tag:twd-public");
	// NodeAttrsSnippet is the policy to add (ADR-029 D6).
	MissingNodeAttrs []string `json:"missingNodeAttrs,omitempty"`
	NodeAttrsSnippet string   `json:"nodeAttrsSnippet,omitempty"`
	// PublicURL is where the workload's public routes will be served.
	PublicURL string `json:"publicURL,omitempty"`
}

// Preflight checks the request against the tenant's current policy (read
// through the policy cache; never written).
func (m *Mgmt) Preflight(ctx context.Context, tenantID string, r PreflightRequest) (PreflightResult, error) {
	res := PreflightResult{Missing: []PreflightMissing{}, MissingTagOwners: []string{}}
	t, err := m.store.GetTenant(ctx, tenantID)
	if err != nil {
		return res, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if err := headscalePreflight(t, r); err != nil {
		return res, err
	}
	pol, err := m.policies.Get(ctx, t)
	if err != nil {
		return res, errf(503, "policy_unavailable", "can't read the tenant policy: %v", err)
	}
	port := r.Port
	if port == "" {
		port = "tcp:80"
	}
	var owners []string
	for _, tag := range r.SrcTags {
		if _, ok := pol.TagOwners[tag]; !ok && !slices.Contains(res.MissingTagOwners, tag) {
			res.MissingTagOwners = append(res.MissingTagOwners, tag)
			owners = append(owners, fmt.Sprintf("%q: %s", tag, tagOwnersFor(t)))
		}
	}
	if len(owners) > 0 {
		res.TagOwnersSnippet = `"tagOwners": { ` + strings.Join(owners, ", ") + ` }`
	}
	for _, dst := range r.Connects {
		if dst != "*" && !strings.HasPrefix(dst, "tag:") && !strings.HasPrefix(dst, "autogroup:") {
			res.Unchecked = append(res.Unchecked, dst)
			continue
		}
		for _, src := range r.SrcTags {
			if !pol.HasIPGrant(src, dst) {
				res.Missing = append(res.Missing, PreflightMissing{Src: src, Dst: dst,
					Snippet: fmt.Sprintf(`{ "src": [%q], "dst": [%q], "ip": [%q] }`, src, dst, port)})
			}
		}
	}
	if r.Public && !pol.HasNodeAttr(PublicTag, "funnel") {
		res.MissingNodeAttrs = append(res.MissingNodeAttrs, "funnel on "+PublicTag)
		res.NodeAttrsSnippet = PublicFunnelSnippet
	}
	if r.Service != "" {
		servicePreflight(pol, &res, r.Service, r.WorkloadTags)
	}
	res.OK = len(res.Missing) == 0 && len(res.MissingTagOwners) == 0 && len(res.MissingNodeAttrs) == 0 && !res.ServiceUnapproved
	return res, nil
}

// VolumeKeyResponse is GET /v0/volumes/key.
type VolumeKeyResponse struct {
	Key []byte `json:"key"`
}

// String never prints the key.
func (VolumeKeyResponse) String() string { return "mgmt.VolumeKeyResponse{[REDACTED]}" }

// volumeKeyName is the custody name of a workload's DO volume data key.
func volumeKeyName(tenantID, workload string) string {
	return "twd/" + tenantID + "/volumes/" + workload + "/dek"
}

// VolumeKey returns the 256-bit data key of the (tenant, workload) DO
// volume (microVM tier: dm-crypt on the host), creating it in custody on
// first use. Only the host holding the workload's namespace lease at epoch
// gets it (fenced like Credential, ADR-011); hostID "" (unix-socket callers
// that don't name themselves) fences on the epoch alone.
func (m *Mgmt) VolumeKey(ctx context.Context, tenantID, workload, hostID string, epoch int64) ([]byte, error) {
	if tenantID == "" || workload == "" || strings.ContainsAny(tenantID+workload, "/\x00") {
		return nil, errf(400, "bad_request", "tenant and workload are required")
	}
	l, err := m.store.GetLease(ctx, LeaseName(tenantID, workload))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, errf(409, "lease_lost", "no lease for %s/%s", tenantID, workload)
		}
		return nil, err
	}
	if l.Epoch != epoch || (hostID != "" && l.Holder != hostID) || l.ExpiresAt < m.now().UnixMilli() {
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "deny", Code: "lease_lost"},
			map[string]any{"host": hostID, "epoch": epoch, "volume": workload})
		return nil, errf(409, "lease_lost", "the lease on %s/%s is not held by %q at epoch %d", tenantID, workload, hostID, epoch)
	}
	m.volMu.Lock()
	defer m.volMu.Unlock()
	name := volumeKeyName(tenantID, workload)
	key, err := m.custody.Get(ctx, name)
	created := false
	if errors.Is(err, ErrNotFound) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := m.custody.Put(ctx, name, key); err != nil {
			return nil, fmt.Errorf("custody: %w", err)
		}
		created = true
	} else if err != nil {
		return nil, fmt.Errorf("custody: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("custody: volume key has the wrong length")
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "volume_key"},
		map[string]any{"host": hostID, "epoch": epoch, "workload": workload, "created": created})
	return key, nil
}
