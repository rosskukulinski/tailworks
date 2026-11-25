// Package hostdrun runs the tailworks host agent (hostd) as a library: the
// standalone tw-hostd binary is a flag parser over New/Start, and the
// single-process `tw up` embeds it next to mgmt (internal/mgmtd) with an
// in-process transport (internal/inproc).
//
// Three transports:
//   - tsnet (normal standalone operation): the host's own tag:tw-host
//     node, joined once with the single-use key `tw host add` minted; mgmt
//     is reached over the tailnet and pushes to the node's PushPort;
//   - local-dev: mgmt's unix socket with the per-host token (tw dev, tests);
//   - embedded (Options.Client set): a caller-provided client, no node of
//     its own, pushes served in-process (hostd.Config.PushInProcess).
package hostdrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/image"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/internal/tsnetlc"
	"github.com/rosskukulinski/tailworks/netd"
	"tailscale.com/tsnet"
)

// HostTag is the tag hosts' own nodes carry (mgmt.HostTag).
const HostTag = "tag:tw-host"

// Flag defaults tw-hostd and the single-process tw up share (an Options
// zero value means "off" for these, so callers pass the default explicitly).
const (
	// DefaultImageCacheMiB caps the converted container image cache
	// (tw-hostd -container-image-cache-max-mib; 0 = no cap). Images in use
	// stay even past it.
	DefaultImageCacheMiB = 20480
	// DefaultPoolNodes is the pre-joined node pool per hot class
	// (tw-hostd -pool-nodes); hostd treats 0 as off.
	DefaultPoolNodes = 1
	// DefaultPushPort is where mgmt pushes to this host's node (-push-port).
	DefaultPushPort = 7474
)

// HostNodeName is the tsnet hostname of a host's own node (mgmt.HostNodeName).
func HostNodeName(hostID string) string { return "tw-host-" + hostID }

// Options mirrors tw-hostd's flags, one field per flag (see cmd/tw-hostd).
type Options struct {
	// MgmtAddr is tw-mgmt's address: http(s)://… over the host's node, or
	// unix:///path with LocalDev. Ignored when Client is set.
	MgmtAddr string
	// LocalDev talks to mgmt over its unix socket with the per-host token
	// (MgmtTokenFile) instead of a node of its own.
	LocalDev      bool
	MgmtTokenFile string
	// Client, when set, is the transport (single-process tw up:
	// internal/inproc); no node is joined and no push listener opened,
	// and the host registers with hostd.EmbeddedPushURL.
	Client *mgmtapi.Client
	// tsnet transport: the node state dir (default <DataDir>/tsnet), the
	// control URL (required), the TS_AUTHKEY=… join file (waited for when
	// the node has no state yet; deleted after use), and the push port
	// (0 = DefaultPushPort).
	TSNetDir        string
	TSNetControlURL string
	JoinTokenFile   string
	PushPort        int

	// TSNetKeyTagsOnly: the host's node takes tag:tw-host from its join
	// key alone (Headscale refuses a pre-auth-key join that requests tags).
	TSNetKeyTagsOnly bool

	// PortMapping turns UPnP/NAT-PMP/PCP on for this host's tailnet nodes
	// (off by default, process-wide: netd.DisablePortMapping).
	PortMapping bool
	// MetricsPort serves GET /metrics on the host's own node
	// (ADR-045; 0 = off; flag default obs.DefaultMetricsPort).
	// Without a node of its own (Client, LocalDev) the host serves none:
	// mgmt's process serves the embedded host's metrics.
	MetricsPort int

	HostID  string // default: hostname
	DataDir string // node state, DO storage, objects.json
	RunDir  string // sockets and generated configs (short path)
	Workerd string // workerd binary (resolved by FindWorkerd when empty)
	// hostd.Config pass-through (0 / "" = hostd's or the flag's default).
	Heartbeat time.Duration
	// FenceAfter is hostd.Config.FenceAfter (0 = hostd.DefaultFenceAfter;
	// negative disables self-fencing).
	FenceAfter     time.Duration
	Ephemeral      bool
	HostnamePrefix string
	ControlURL     string
	CapacityNodes  int
	KillTombstone  time.Duration
	TSNetLogs      bool
	// PoolNodes is hostd.Config.PoolNodes (0 = off; the flag default is
	// DefaultPoolNodes).
	PoolNodes int
	// PathEvery is hostd.Config.PathEvery (0 = hostd.DefaultPathEvery;
	// negative disables).
	PathEvery          time.Duration
	VM                 VMOptions
	Sandbox            string
	InsecureSharedHost bool

	// AllowExperimentalFlags is hostd.Config.AllowExperimentalFlags
	// (tw-hostd -allow-experimental-flags, #337).
	AllowExperimentalFlags bool

	// PyodideDir is hostd.Config.PyodideDir (Python Workers; "" =
	// <data-dir>/pyodide).
	PyodideDir string

	// TestPublicListenDir (tests, LocalDev only) opens public nodes'
	// listeners on loopback instead of Funnel (testfunnel.go).
	TestPublicListenDir string

	SandboxDeny        []string
	NodeBin            string
	AllowSiblingEgress bool
	// Containers (ADR-032), offered only with a microVM backend that can
	// run them: NoContainers withholds them; ContainerImageCache is the
	// converted image cache (default <DataDir>/images); ContainerImageDir a
	// dir of image tarballs with an images.json index; ContainerPublicImages
	// refs cached once for all tenants; ContainerImageMaxMiB caps one
	// image's layers while converting (0 = image.DefaultMaxImageBytes);
	// ContainerImageCacheMaxMiB caps the cache (0 = none; callers pass
	// DefaultImageCacheMiB); ContainerIdle,
	// ContainerMaxLife, ContainerGrace are hostd.Config's (0 = default);
	// ContainerMax, ContainerMemMiB, ContainerVCPU the host's capacity (0 =
	// unlimited); ContainerInternet "direct" (default), "exit-node[=<node>]"
	// or "off" (internetpath.go).
	NoContainers              bool
	ContainerImageCache       string
	ContainerImageDir         string
	ContainerPublicImages     []string
	ContainerImageMaxMiB      int64
	ContainerImageCacheMaxMiB int64
	ContainerIdle             time.Duration
	ContainerMaxLife          time.Duration
	ContainerGrace            time.Duration
	ContainerMax              int
	ContainerMemMiB           int
	ContainerVCPU             float64
	ContainerInternet         string
	Log                       *slog.Logger

	// WorkerInternet is "direct" (default: Workers' fetch() reaches public
	// internet hosts from this host's network, subject to each workload's
	// tailnet.egress.internet), "exit-node=<node>" (through that tailnet
	// exit node via this host's own node; internetpath.go) or "off"
	// (ADR-049, ADR-054).
	WorkerInternet string

	// R2 holds R2 bucket bytes (hostd.Config.R2; nil: files under
	// <DataDir>/r2).
	R2 r2.Backend

	// QueueVisibility is hostd.Config.QueueVisibility (0 = default).
	QueueVisibility time.Duration

	// RequestLogMaxAge is hostd.TelemetryOptions.RecordMaxAge: drop request
	// records older than this (0 = only the size rotation, the default).
	RequestLogMaxAge time.Duration

	// WorkerdHeapMiB and NodeHeapMiB are hostd.Config's (process-tier
	// heap caps; 0 = default, negative = none).
	WorkerdHeapMiB int
	NodeHeapMiB    int

	// SelfUpgrade, InstallPrefix and Restart are hostd.Config's (upgrade
	// runs install a pushed release here and restart the host).
	SelfUpgrade   bool
	InstallPrefix string
	Restart       func()
}

// VMOptions configures the microVM tier (Firecracker on Linux + KVM,
// Virtualization.framework on macOS; see vmBackend). Zero values take
// tw-hostd's flag defaults (withDefaults).
type VMOptions struct {
	// Kernel and Rootfs are the guest kernel and image (VMAsset); Helper
	// the tw-vmhelper socket; Firecracker and Jailer the binaries used as
	// root without the helper; HugePages "auto" | "on" | "off";
	// ContainerKernel the erofs-capable kernel for containers (default
	// Kernel); VzVMM the tw-vz VMM on macOS.
	Kernel, Rootfs, Firecracker, Jailer, Helper, HugePages, ContainerKernel, VzVMM string
	// MemMiB and VCPUs size a guest; VolumeMiB new DO volumes.
	MemMiB, VCPUs, VolumeMiB int
	// Fast VMs (Firecracker only; vz ignores them): Pool is the warm pool
	// size (0 = auto, -1 = off); NoSnapshot disables restoring from the
	// base snapshot when the pool is empty (-vm-snapshot=false);
	// SnapRefill also refills the pool from the snapshot; Rotate rebuilds
	// the base snapshot this often (0 = hourly, < 0 = never).
	Pool       int
	NoSnapshot bool
	SnapRefill bool
	Rotate     time.Duration
	// Rosetta (macOS, Apple silicon) shares Rosetta for Linux into guests.
	Rosetta bool
}

// withDefaults fills VMOptions like tw-hostd's flag defaults: the release's
// guest assets (VMAsset), the tw-vmhelper socket, /usr/local/bin
// firecracker + jailer, 512 MiB, 2 vCPUs, huge pages auto, 1 GiB volumes,
// hourly snapshot rotation.
func (v VMOptions) withDefaults(home string) VMOptions {
	if v.Kernel == "" {
		v.Kernel = VMAsset(home, "vmlinux")
	}
	if v.Rootfs == "" {
		v.Rootfs = VMAsset(home, "rootfs.ext4")
	}
	if v.Helper == "" {
		v.Helper = helperapi.DefaultSocket
	}
	if v.Firecracker == "" {
		v.Firecracker = "/usr/local/bin/firecracker"
	}
	if v.Jailer == "" {
		v.Jailer = "/usr/local/bin/jailer"
	}
	if v.MemMiB == 0 {
		v.MemMiB = 512
	}
	if v.VCPUs == 0 {
		v.VCPUs = 2
	}
	if v.HugePages == "" {
		v.HugePages = "auto"
	}
	if v.VolumeMiB == 0 {
		v.VolumeMiB = 1024
	}
	if v.Rotate == 0 {
		v.Rotate = time.Hour
	}
	return v
}

// Runtime is a prepared (New) or running (Start) host agent.
type Runtime struct {
	Host   *hostd.Host
	Client *mgmtapi.Client
	// TS is the host's own node (tsnet transport only).
	TS *tsnet.Server
	// Transport is "tsnet", "local-dev" or "embedded".
	Transport string
	Workerd   string
	mgmtAddr  string
	log       *slog.Logger
	// exit is the host's exit-node internet path, when it has one.
	exit *hostd.ExitNodeInternet

	// joinSrc says where the tsnet settings came from (joinenv.go).
	joinSrc string
}

// New resolves the transport (joins the host's node when needed, waits for
// mgmt to be reachable over it) and prepares the Host without registering
// it. The caller may then hand the Host's PushHandler to an in-process
// mgmt before Start.
func New(ctx context.Context, o Options) (rt *Runtime, err error) {
	if !o.PortMapping {
		// Process-wide, so before any tsnet node (this host's own included).
		netd.DisablePortMapping()
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	home, _ := os.UserHomeDir()
	joinSrc := "" // tsnet: where the host ID, mgmt and control URLs came from
	if o.Client == nil && !o.LocalDev {
		if joinSrc, err = resolveJoinSettings(&o, home); err != nil { // joinenv.go
			return nil, err
		}
	}
	if o.HostID == "" {
		o.HostID, _ = os.Hostname()
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(home, ".tailworks", "hostd")
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = 3 * time.Second
	}
	// CapacityNodes 0 stays 0: hostd derives it from RAM
	// (hostd.DefaultCapacityNodes).
	if o.KillTombstone == 0 {
		o.KillTombstone = 10 * time.Second
	}
	if o.Sandbox == "" {
		o.Sandbox = "auto"
	}
	if o.PushPort == 0 {
		o.PushPort = DefaultPushPort
	}
	if o.ContainerInternet == "" {
		o.ContainerInternet = "direct"
	}
	if o.WorkerInternet == "" {
		o.WorkerInternet = "direct"
	}
	o.VM = o.VM.withDefaults(home)
	wd, err := FindWorkerd(o.Workerd, home)
	if err != nil {
		return nil, err
	}
	if o.NodeBin == "" {
		o.NodeBin = os.Getenv("TW_NODE")
	}
	rt = &Runtime{log: log, Workerd: wd, joinSrc: joinSrc}
	defer func() {
		if err != nil && rt.TS != nil {
			rt.TS.Close()
		}
	}()
	var mc *mgmtapi.Client
	var pushLn net.Listener
	regSecret := "" // M1: the one-time registration secret (tsnet mode only)
	switch {
	case o.Client != nil:
		mc, rt.Transport = o.Client, "embedded"
	case o.LocalDev:
		rt.Transport = "local-dev"
		if o.MgmtAddr == "" {
			o.MgmtAddr = "unix://" + filepath.Join(home, ".tailworks", "run", "mgmt.sock")
		}
		if mc, err = mgmtapi.NewClient(o.MgmtAddr); err != nil {
			return nil, err
		}
		mc.HostID = o.HostID // before first use: the client is shared (rt.Client, the image puller)
		if strings.HasPrefix(o.MgmtAddr, "unix://") || strings.HasPrefix(o.MgmtAddr, "/") {
			mc.TokenFile = o.MgmtTokenFile
		}
	default:
		rt.Transport = "tsnet"
		if o.TSNetDir == "" {
			o.TSNetDir = filepath.Join(o.DataDir, "tsnet")
		}
		if rt.TS, err = JoinHostNode(ctx, o.TSNetDir, HostNodeName(o.HostID), o.TSNetControlURL, o.JoinTokenFile, o.TSNetKeyTagsOnly, log); err != nil {
			return nil, err
		}
		regSecret = RegistrationSecret(o.JoinTokenFile, o.TSNetDir)
		if mc, err = mgmtapi.NewClient(o.MgmtAddr); err != nil {
			return nil, err
		}
		mc.HostID = o.HostID // before first use: the client is shared (rt.Client, the image puller)
		mc.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DialContext: rt.TS.Dial, MaxIdleConnsPerHost: 16}}
		if pushLn, err = rt.TS.Listen("tcp", fmt.Sprintf(":%d", o.PushPort)); err != nil {
			return nil, fmt.Errorf("push listener on the node: %w", err)
		}
		// mgmt's node must be in our netmap before the first request.
		wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := waitMgmt(wctx, func(ctx context.Context) error { return mc.Do(ctx, "GET", "/v1/healthz", nil, nil) }, log) // mgmtwait.go
		cancel()
		if err != nil {
			return nil, fmt.Errorf("mgmt %s unreachable over the tailnet: %w (does the policy grant tag:tw-host → tag:tw-mgmt?)", o.MgmtAddr, err)
		}
		if err := SaveJoinSettings(o.TSNetDir, JoinSettings{HostID: o.HostID, MgmtURL: o.MgmtAddr, ControlURL: o.TSNetControlURL, KeyTagsOnly: o.TSNetKeyTagsOnly}); err != nil {
			log.Warn("could not save the join settings beside the node state (pass the flags on the next start)", "err", err)
		}
	}
	rt.Client, rt.mgmtAddr = mc, o.MgmtAddr
	hcfg := hostd.Config{HostID: o.HostID, Mgmt: mc, DataDir: o.DataDir, RunDir: o.RunDir, WorkerdBin: wd,
		HeartbeatEvery: o.Heartbeat, FenceAfter: o.FenceAfter, Ephemeral: o.Ephemeral, HostnamePrefix: o.HostnamePrefix, ControlURL: o.ControlURL,
		PathEvery: o.PathEvery, CapacityNodes: o.CapacityNodes, KillTombstone: o.KillTombstone, Logger: log, TSNetLogs: o.TSNetLogs,
		Sandbox: o.Sandbox, SandboxDeny: o.SandboxDeny, InsecureSharedHost: o.InsecureSharedHost, AllowSiblingEgress: o.AllowSiblingEgress, PoolNodes: o.PoolNodes,
		NodeBin: o.NodeBin, VMVolumeMiB: o.VM.VolumeMiB, VMMemMiB: o.VM.MemMiB, VMVCPUs: o.VM.VCPUs,
		VM:            vmBackend(o.VM, log),
		ContainerIdle: o.ContainerIdle, ContainerMaxLife: o.ContainerMaxLife, ContainerGrace: o.ContainerGrace,
		ContainerCapacity: hostd.ContainerCapacity{Max: o.ContainerMax, MemMiB: o.ContainerMemMiB, VCPU: o.ContainerVCPU},
		PushInProcess:     o.Client != nil, RegistrationSecret: regSecret,
		R2:            o.R2,
		SelfUpgrade:   o.SelfUpgrade,
		InstallPrefix: o.InstallPrefix,
		Restart:       o.Restart,
	}
	hcfg.QueueVisibility = o.QueueVisibility
	hcfg.Telemetry.RecordMaxAge = o.RequestLogMaxAge
	hcfg.WorkerdHeapMiB = o.WorkerdHeapMiB
	hcfg.NodeHeapMiB = o.NodeHeapMiB
	hcfg.AllowExperimentalFlags = o.AllowExperimentalFlags // hostd/gen/compatflags.go (#337)
	hcfg.PyodideDir = o.PyodideDir                         // hostd/pyodide.go
	if o.TestPublicListenDir != "" {
		if !o.LocalDev {
			return nil, errors.New("-test-public-listen-dir needs -local-dev (hermetic tests only)")
		}
		hcfg.PublicListen = testPublicListen(o.TestPublicListenDir) // testfunnel.go
	}
	// Containers (ADR-032) run on the microVM backend; without it this host
	// offers none (start() fails with tier_unavailable, no fallback).
	if o.NoContainers {
		hcfg.ContainersOff = "turned off (tw-hostd -no-containers)"
	}
	if cb, ok := hcfg.VM.(sandbox.ContainerBackend); ok && hcfg.VM != nil && !o.NoContainers {
		// Images pinned at deploy are pulled from mgmt's registry over the
		// same connection as the API (ADR-032 D3: never from Docker Hub).
		st := &image.Store{Dir: o.ContainerImageCache, Aliases: image.DefaultAliases, Public: map[string]bool{},
			Puller: image.RegistryPuller{Client: mc.OCI()}, MaxBytes: o.ContainerImageCacheMaxMiB << 20, Limits: image.Limits{MaxBytes: o.ContainerImageMaxMiB << 20}}
		if o.ContainerImageCache == "" {
			st.Dir = filepath.Join(o.DataDir, "images")
		}
		if o.ContainerImageDir != "" {
			st.Sources = append(st.Sources, image.DirSource{Dir: o.ContainerImageDir})
		}
		for _, r := range o.ContainerPublicImages {
			st.Public[image.NormalizeRef(r)] = true
		}
		// vz with Rosetta for Linux runs linux/amd64 images as they are
		// (ADR-032 D7): the stock cloudflare/sandbox images, so no arm64
		// alias substitution.
		if em, ok := cb.(interface{ EmulatedArches() []string }); ok && len(em.EmulatedArches()) > 0 {
			st.Emulated, st.Aliases = em.EmulatedArches(), nil
			log.Info("containers: emulated image arches (no alias map)", "arches", st.Emulated)
		}
		// Offer containers only when they can actually run here (the host
		// advertises the "containers" tier from this; ADR-032 D5).
		if err := containersUsable(cb, st); err != nil {
			log.Info("containers unavailable on this host; not offered", "reason", err)
			hcfg.ContainersOff = err.Error() // tw doctor, tw up (hostd/containersoff.go)
		} else {
			hcfg.Containers, hcfg.Images = cb, st
			log.Info("containers available", "image_cache", st.Dir, "image_dir", o.ContainerImageDir, "idle", o.ContainerIdle, "max_life", o.ContainerMaxLife, "internet", o.ContainerInternet)
		}
	}
	if hcfg.WorkerInternet, hcfg.ContainerInternet, rt.exit, err = internetPaths(ctx, o.WorkerInternet, o.ContainerInternet, rt.TS, log); err != nil {
		return nil, err
	}
	if pushLn != nil {
		lc, err := rt.TS.LocalClient()
		if err != nil {
			return nil, fmt.Errorf("node local client: %w", err)
		}
		hcfg.PushListener, hcfg.PushPort = pushLn, o.PushPort
		if o.MetricsPort > 0 {
			if hcfg.MetricsListener, err = rt.TS.Listen("tcp", fmt.Sprintf(":%d", o.MetricsPort)); err != nil {
				return nil, fmt.Errorf("metrics listener: %w", err)
			}
		}
		hcfg.WhoIs, hcfg.TailnetStatus = lc.WhoIs, lc.Status
	}
	if rt.Host, err = hostd.New(hcfg); err != nil {
		return nil, err
	}
	return rt, nil
}

// Start registers the host with mgmt and starts its loops (30 s bound).
func (rt *Runtime) Start(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := rt.Host.Start(ctx); err != nil {
		return err
	}
	rt.log.Info("hostd running", "host", rt.Host.Status().HostID, "mgmt", rt.mgmtAddr, "transport", rt.Transport, "push", rt.Host.PushSocket(), "workerd", rt.Workerd, "sandbox", rt.Host.SandboxInfo(), "settings", rt.joinSrc)
	return nil
}

// Close stops the host (node state is kept for rejoin) and its own node.
func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	var err error
	if rt.Host != nil {
		err = rt.Host.Close()
	}
	if rt.exit != nil {
		rt.exit.Close()
	}
	if rt.TS != nil {
		tsnetlc.Close(rt.TS)
	}
	return err
}

// JoinHostNode opens (or joins) this host's own tailnet node. A node with
// saved state re-opens without a key. Otherwise it needs the single-use
// join key: from joinFile, which is waited for (tw up writes it after
// mgmt minted it) and deleted once the node is up. The key never reaches
// the log. keyTagsOnly: advertise no tags (Headscale; the key carries them).
func JoinHostNode(ctx context.Context, dir, hostname, control, joinFile string, keyTagsOnly bool, log *slog.Logger) (*tsnet.Server, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ts := &tsnet.Server{Hostname: hostname, Dir: dir, ControlURL: control, AdvertiseTags: []string{HostTag},
		Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	if keyTagsOnly {
		ts.AdvertiseTags = nil
	}
	hasState := false
	if _, err := os.Stat(filepath.Join(dir, "tailscaled.state")); err == nil {
		hasState = true
	}
	if !hasState {
		if joinFile == "" {
			return nil, errors.New("this host's node has no state yet: run `tw-hostd join --token-file <file>` (tw host add mints the key) or pass -join-token-file")
		}
		log.Info("waiting for the join key", "file", joinFile, "node", hostname)
		for {
			if k, err := tsapi.LoadEnvFile(joinFile, "TS_AUTHKEY"); err == nil && k != "" {
				ts.AuthKey = k
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	// Before anything uses the node's LocalAPI client: Close drains it
	// (internal/tsnetlc).
	if _, err := tsnetlc.Client(ts); err != nil {
		return nil, fmt.Errorf("start the node %s: %w", hostname, err)
	}
	uctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	st, err := ts.Up(uctx)
	if err != nil {
		ts.Close()
		return nil, fmt.Errorf("join the operator tailnet as %s: %w", hostname, err)
	}
	if !hasState && joinFile != "" {
		PersistRegistrationSecret(joinFile, dir)
		os.Remove(joinFile) // single use
	}
	tags := []string{}
	if st.Self != nil && st.Self.Tags != nil {
		tags = st.Self.Tags.AsSlice()
	}
	log.Info("tailnet node", "name", strings.TrimSuffix(st.Self.DNSName, "."), "tags", tags, "ips", st.TailscaleIPs)
	return ts, nil
}

// registrationSecretFile is where the one-time registration secret from the
// join file is kept (0600, beside the node state) until the host has
// registered: the join file itself is deleted after the join, but mgmt
// needs the secret on the host ID's first PUT /v1/hosts/{id} (M1).
func registrationSecretFile(tsDir string) string { return filepath.Join(tsDir, "registration.secret") }

// PersistRegistrationSecret copies TW_REGISTRATION_SECRET from a join
// file to the node state dir. A join file without one (an older tw host
// add) leaves things as they are. The secret is never logged.
func PersistRegistrationSecret(joinFile, tsDir string) {
	s, err := tsapi.LoadEnvFile(joinFile, "TW_REGISTRATION_SECRET")
	if err != nil || s == "" {
		return
	}
	os.WriteFile(registrationSecretFile(tsDir), []byte(s+"\n"), 0o600)
}

// RegistrationSecret is the secret hostd presents when it registers: from
// the join file if it is still there (a fresh `tw host add` for a pending
// host), else the copy PersistRegistrationSecret kept. "" when there is
// none (a host bound before secrets existed; mgmt then ignores it).
func RegistrationSecret(joinFile, tsDir string) string {
	if joinFile != "" {
		if s, err := tsapi.LoadEnvFile(joinFile, "TW_REGISTRATION_SECRET"); err == nil && s != "" {
			return s
		}
	}
	b, err := os.ReadFile(registrationSecretFile(tsDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// VMAsset finds a microVM asset: next to the installed release
// (<prefix>/share/tailworks/vm, e.g. ~/.tailworks), else ~/.tailworks/vm.
func VMAsset(home, name string) string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "tailworks", "vm", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(home, ".tailworks", "vm", name)
}

// FindWorkerd resolves the workerd binary: flagVal, $TW_WORKERD,
// ~/.tailworks/bin/workerd, then PATH.
func FindWorkerd(flagVal, home string) (string, error) {
	for _, c := range []string{flagVal, os.Getenv("TW_WORKERD"), filepath.Join(home, ".tailworks", "bin", "workerd")} {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	if p, err := exec.LookPath("workerd"); err == nil {
		return p, nil
	}
	return "", errors.New("workerd not found (use -workerd or $TW_WORKERD)")
}

// SplitList splits a comma-separated flag value, dropping blanks.
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// containersUsable: the container backend can boot guests here (helper,
// kernel with erofs) and the image store can convert images (mkfs.erofs).
func containersUsable(cb sandbox.ContainerBackend, st *image.Store) error {
	if u, ok := cb.(interface{ ContainersUsable() error }); ok {
		if err := u.ContainersUsable(); err != nil {
			return err
		}
	}
	return st.Check()
}
