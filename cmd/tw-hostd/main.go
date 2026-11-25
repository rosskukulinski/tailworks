// tw-hostd is the tailworks host agent (process tier): it registers with
// tw-mgmt, heartbeats, pulls assignments, and runs each deployment as one
// supervised workerd process with per-object tailnet nodes (netd library).
//
//	tw-hostd -mgmt unix:///Users/me/.tailworks/run/mgmt.sock -host-id h1 -data-dir ~/.tailworks/hostd
//
// The push API (mgmt → hostd) and the operator's status live on
// <run-dir>/hostd.sock:
//
//	POST /v1/push/kill {objectID, reason}   POST /v1/push/assignments   GET /v1/status
//
// This host's own tailnet node serves the two pushes only, on -push-port,
// and only to mgmt's node (WhoIs: tag:tw-mgmt, pinned to the node -mgmt
// resolves to).
//
// The agent itself is internal/hostdrun; this binary parses the flags. The
// single-process `tw up` runs the same runtime embedded in mgmt's process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/rosskukulinski/tailworks/internal/version"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/image"
	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/internal/npminstall"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
	"tailscale.com/tsnet"
)

func main() {
	if version.Asked(os.Args) {
		fmt.Println(version.String("tw-hostd"))
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tw-hostd:", netd.Redact(err.Error()))
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "join" {
		return runJoin(os.Args[2:])
	}
	home, _ := os.UserHomeDir()
	var (
		mgmtAddr   = flag.String("mgmt", "", "tw-mgmt address: http(s)://tw-mgmt.<tailnet> over this host's node (default: the join file's, else the one saved at the join), or unix:///path with -local-dev")
		localDev   = flag.Bool("local-dev", false, "talk to mgmt over its unix socket with the per-host token instead of this host's own tailnet node (tw dev, hermetic tests)")
		tsDir      = flag.String("tsnet-dir", "", "state of this host's own tailnet node (default <data-dir>/tsnet)")
		tsControl  = flag.String("tsnet-control-url", "", "operator tailnet control URL for this host's node (default: the join file's, else the one saved at the join, else Tailscale's for a node that hasn't joined yet)")
		tsKeyTags  = flag.Bool("tsnet-key-tags-only", false, "the host's node advertises no tags; its join key carries tag:tw-host (required on Headscale, ADR-015)")
		joinFile   = flag.String("join-token-file", "", "the join file tw host add wrote: the single-use join key (TS_AUTHKEY=…), the registration secret, and the host ID, mgmt URL and control URL (flags win; kept beside the node state after the join); waited for when the node has no state yet. Deleted after use")
		pushPort   = flag.Int("push-port", hostdrun.DefaultPushPort, "port on this host's node where mgmt pushes kills and assignment changes")
		metricsPrt = flag.Int("metrics-port", obs.DefaultMetricsPort, "port on this host's node serving GET /metrics (Prometheus; who may scrape is a tailnet policy grant); 0 = off")
		hostID     = flag.String("host-id", "", "this host's ID (default: the join file's, else the one saved at the join, else hostname)")
		dataDir    = flag.String("data-dir", filepath.Join(home, ".tailworks", "hostd"), "node state, DO storage, objects.json")
		runDir     = flag.String("run-dir", "", "sockets and generated configs (short path; default a new /tmp dir)")
		workerd    = flag.String("workerd", "", "workerd binary (default: $TW_WORKERD, ~/.tailworks/bin/workerd, or PATH)")
		hb         = flag.Duration("heartbeat", 3*time.Second, "heartbeat / assignment poll period")
		fenceAfter = flag.Duration("fence-after", hostd.DefaultFenceAfter, "stop serving stateful workloads after this long without a successful heartbeat (≤ 40 s, below mgmt's 45 s lease; negative disables)")
		ephemeral  = flag.Bool("ephemeral", false, "join nodes as ephemeral (dev/test)")
		prefix     = flag.String("hostname-prefix", "", "prefix for every node hostname")
		controlURL = flag.String("control-url", "", "override every tenant's control URL (testing)")
		capacity   = flag.Int("capacity-nodes", 0, "advertised node capacity (0: derived from RAM, see docs/limits.md)")
		tombstone  = flag.Duration("kill-tombstone", 10*time.Second, "keep killed nodes RST-ing new connections this long")
		logLevel   = flag.String("log-level", "info", "debug|info|warn|error")
		tsnetLogs  = flag.Bool("tsnet-logs", false, "forward tsnet logs at debug level")
		vmKernel   = flag.String("vm-kernel", hostdrun.VMAsset(home, "vmlinux"), "microVM tier: guest kernel (Firecracker on Linux, Virtualization.framework on macOS)")
		vmRootfs   = flag.String("vm-rootfs", hostdrun.VMAsset(home, "rootfs.ext4"), "microVM tier: guest image from guest/build-image.sh")
		vmHelper   = flag.String("vm-helper", helperapi.DefaultSocket, "microVM tier: tw-vmhelper socket (sudo tw host setup --microvm)")
		vmFC       = flag.String("firecracker", "/usr/local/bin/firecracker", "microVM tier, as root without the helper service: firecracker binary")
		vmJailer   = flag.String("jailer", "/usr/local/bin/jailer", "microVM tier, as root without the helper service: jailer binary")
		vmVolMiB   = flag.Int("vm-volume-mib", 1024, "microVM tier: size of new DO volumes (sparse)")
		vmMem      = flag.Int("vm-mem-mib", 512, "microVM tier: guest memory")
		vmCPUs     = flag.Int("vm-vcpus", 2, "microVM tier: guest vCPUs")
		vmHuge     = flag.String("vm-hugepages", "auto", "microVM tier: back guest RAM with 2 MiB pages: auto | on | off")
		vzVMM      = flag.String("vz-vmm", "", "microVM tier on macOS: the tw-vz VMM, signed with the virtualization entitlement (default: next to tw-hostd)")
		vmRosetta  = flag.Bool("vm-rosetta", false, "microVM tier on macOS (Apple silicon): share Rosetta for Linux into guests so x86_64 binaries run")
		vmSnap     = flag.Bool("vm-snapshot", true, "microVM tier: when the warm pool is empty, restore a VM from a base snapshot of a warm VM without tenant code or keys (private, lazily loaded guest memory) instead of booting one (ADR-030; Durable Object VMs only, containers cold-boot)")
		vmSnapFill = flag.Bool("vm-snapshot-refill", false, "microVM tier: also refill the warm pool by restoring the base snapshot (faster refills, slower claims) instead of booting")
		vmRotate   = flag.Duration("vm-snapshot-rotate", time.Hour, "microVM tier: rebuild the base snapshot this often (and at every start); idle pool VMs restored from the old base are discarded; < 0 = never")
		vmPool     = flag.Int("vm-pool", 0, "microVM tier: warm pool size (pre-booted VMs without tenant code; single use; Durable Object VMs only, containers cold-boot): 0 = auto (≤ 2, within the huge-page reservation), -1 = off")
		sandboxF   = flag.String("sandbox", "auto", "process-tier workerd/Node sandbox: auto (seatbelt on macOS, landlock on Linux) | seatbelt | seatbelt-legacy (macOS: the previous files-only profile, kept for one release) | landlock | none")
		insecureSH = flag.Bool("insecure-shared-host", false, "without a process sandbox, still run process-tier workloads of more than one tenant (tenants are NOT isolated from each other)")
		pyodideDir = flag.String("pyodide-dir", "", "Python Workers: the pinned Pyodide bundles workerd reads, read-only (default <data-dir>/pyodide; fetched on the first Python deployment, or copied there on an offline host: docs/python.md#offline-hosts)")
		allowExp   = flag.Bool("allow-experimental-flags", false, "let deployments set compatibility flags workerd marks experimental (unsafe_module, rtti_api, precise_timers, …; refused by default, docs/COMPAT.md#experimental-flags); advertised to placement as the experimental-flags feature")
		sbDeny     = flag.String("sandbox-deny", "", "comma-separated extra paths no workerd may read, write or connect to (tw up: its state and run dirs)")
		nodeBin    = flag.String("node", "", "Node process tier (runtime \"node\"): node binary (default: $TW_NODE, or node on PATH)")
		tokenFile  = flag.String("mgmt-token-file", filepath.Join(home, ".tailworks", "hostd", "mgmt.token"), "per-host token file minted by tw-mgmt (sent as Tw-Host-Token on the unix socket; \"\" = none)")
		poolNodes  = flag.Int("pool-nodes", hostdrun.DefaultPoolNodes, "pre-joined nodes to keep per hot class (one with a create here in the last hour): a create takes one and renames it (~0.2 s) instead of joining a new node; each is a device; 0 = off")
		portMap    = flag.Bool("port-mapping", false, "use UPnP/NAT-PMP/PCP for this host's tailnet nodes (off by default: it adds ~250 ms to every new object node's join and has every node ask the router for a mapping; turn on for hosts behind hard NATs that need router-assisted direct paths)")
		pathEvery  = flag.Duration("path-every", hostd.DefaultPathEvery, "smart placement: measure and report tailnet paths to assignments' placement targets this often (negative disables)")
		reqMaxAge  = flag.Duration("request-log-max-age", 0, "also drop request records (tw logs --source requests) older than this, e.g. 168h; 0 = keep them until the size rotation does (the default)")
		queueVis   = flag.Duration("queue-visibility", hostd.DefaultQueueVisibility, "queues: how long a claimed batch stays hidden in mgmt (extended while queue() runs); bounds how soon a crashed consumer's batch is delivered again")
		pubTestDir = flag.String("test-public-listen-dir", "", "with -local-dev (hermetic tests): serve public nodes on loopback instead of Funnel, writing each listener's address to <dir>/<node>.addr")
		siblings   = flag.Bool("allow-sibling-egress", false, "dial other objects hosted here over WireGuard (a hairpin) instead of netd's in-process hop (tests)")
		ctrOff     = flag.Bool("no-containers", false, "don't offer containers (ctx.container) even with a microVM backend")
		ctrKernel  = flag.String("vm-container-kernel", "", "containers: guest kernel with erofs (guest/build-kernel.sh; default -vm-kernel)")
		ctrCache   = flag.String("container-image-cache", "", "containers: converted image cache (default <data-dir>/images)")
		ctrImages  = flag.String("container-image-dir", "", "containers: dir of image tarballs with an images.json index (tests and hosts without a registry; deployments with pinned images pull from mgmt's registry instead)")
		ctrPublic  = flag.String("container-public-images", "", "containers: comma-separated image refs cached once for all tenants (public bases; -container-image-dir only: pinned images take their scope from the pin)")
		ctrImgMB   = flag.Int64("container-image-max-mib", image.DefaultMaxImageBytes>>20, "containers: cap on one image's uncompressed layers while converting it (also 128 layers, 1 Mi entries); an image over it fails to convert")
		ctrCacheMB = flag.Int64("container-image-cache-max-mib", hostdrun.DefaultImageCacheMiB, "containers: cap on the converted image cache; past it the least recently used images not in use are removed (images in use stay, even past it; 0 = no cap)")
		ctrIdle    = flag.Duration("container-idle", hostd.DefaultContainerIdle, "containers: default inactivity timeout (setInactivityTimeout overrides, ≤ 6 h)")
		ctrMaxLife = flag.Duration("container-max-lifetime", hostd.DefaultContainerMaxLife, "containers: hard max lifetime of a container")
		ctrGrace   = flag.Duration("container-stop-grace", hostd.DefaultContainerGrace, "containers: cap on destroy()'s SIGTERM → SIGKILL grace")
		ctrMax     = flag.Int("container-max", 0, "containers: max running on this host (0 = unlimited)")
		ctrMemMiB  = flag.Int("container-mem-mib", 0, "containers: max total guest memory on this host (0 = unlimited)")
		ctrVCPU    = flag.Float64("container-vcpu", 0, "containers: max total vCPU on this host (0 = unlimited)")
		selfUpg    = flag.Bool("self-upgrade", false, "let tw-mgmt's upgrade runs install a new release here and restart this host (exits with status 75 after installing; run under a service manager that restarts it, e.g. systemd Restart=always)")
		instPrefix = flag.String("install-prefix", "", "with -self-upgrade: the install.sh prefix to upgrade (default: the prefix this tw-hostd runs from, if it has share/tailworks and VERSION)")
		ctrInet    = flag.String("container-internet", "direct", "containers: how start({enableInternet}) reaches a workload's egress.internet hosts: "+hostdrun.InternetModeHelp)
		wkInet     = flag.String("worker-internet", "direct", "Workers: how fetch() reaches internet hosts (subject to each workload's tailnet.egress.internet): "+hostdrun.InternetModeHelp)
	)
	// Process-tier heap caps (hostd/limits.go).
	wdHeap := flag.Int("workerd-heap-mib", hostd.DefaultWorkerdHeapMiB, "process tier: cap each V8 isolate's heap in a workerd (the Worker, each Durable Object class) at this many MiB; past it V8 aborts that workload's workerd and it restarts (-1 = no cap)")
	nodeHeap := flag.Int("node-heap-mib", hostd.DefaultNodeHeapMiB, "Node tier: the app's --max-old-space-size in MiB, unless node.args or NODE_OPTIONS set one (-1 = no cap)")
	var r2f r2.Flags // R2 bucket blob backend (ADR-036)
	r2f.Register(flag.CommandLine)
	defaultTier := hostdDeprecatedFlags(flag.CommandLine) // deprecated.go
	flag.Parse()
	r2b, err := r2f.Open(*dataDir, os.Getenv)
	if err != nil {
		return err
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	log := slog.New(netd.NewRedactingHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
	if err := hostdCheckDefaultTier(*defaultTier, log); err != nil {
		return err
	}
	o := hostdrun.Options{MgmtAddr: *mgmtAddr, LocalDev: *localDev, MgmtTokenFile: *tokenFile,
		TSNetDir: *tsDir, TSNetControlURL: *tsControl, TSNetKeyTagsOnly: *tsKeyTags, JoinTokenFile: *joinFile, PushPort: *pushPort, PortMapping: *portMap,
		MetricsPort: *metricsPrt,
		HostID:      *hostID, DataDir: *dataDir, RunDir: *runDir, Workerd: *workerd,
		Heartbeat: *hb, FenceAfter: *fenceAfter, Ephemeral: *ephemeral, HostnamePrefix: *prefix, ControlURL: *controlURL, CapacityNodes: *capacity,
		KillTombstone: *tombstone, TSNetLogs: *tsnetLogs, PoolNodes: *poolNodes, PathEvery: *pathEvery,
		QueueVisibility: *queueVis,
		VM: hostdrun.VMOptions{Kernel: *vmKernel, Rootfs: *vmRootfs, Firecracker: *vmFC, Jailer: *vmJailer, Helper: *vmHelper,
			MemMiB: *vmMem, VCPUs: *vmCPUs, VolumeMiB: *vmVolMiB, HugePages: *vmHuge, ContainerKernel: *ctrKernel, VzVMM: *vzVMM, Rosetta: *vmRosetta,
			Pool: *vmPool, NoSnapshot: !*vmSnap, SnapRefill: *vmSnapFill, Rotate: *vmRotate},
		Sandbox: *sandboxF, InsecureSharedHost: *insecureSH, SandboxDeny: hostdrun.SplitList(*sbDeny), NodeBin: *nodeBin,
		AllowSiblingEgress: *siblings,
		NoContainers:       *ctrOff, ContainerImageCache: *ctrCache, ContainerImageDir: *ctrImages, ContainerPublicImages: hostdrun.SplitList(*ctrPublic),
		ContainerImageMaxMiB: *ctrImgMB, ContainerImageCacheMaxMiB: *ctrCacheMB,
		ContainerIdle: *ctrIdle, ContainerMaxLife: *ctrMaxLife, ContainerGrace: *ctrGrace,
		ContainerMax: *ctrMax, ContainerMemMiB: *ctrMemMiB, ContainerVCPU: *ctrVCPU, ContainerInternet: *ctrInet, Log: log,
		WorkerInternet: *wkInet,
		R2:             r2b}
	o.RequestLogMaxAge = *reqMaxAge // hostd/telemetry_retention.go
	o.WorkerdHeapMiB, o.NodeHeapMiB = *wdHeap, *nodeHeap
	o.AllowExperimentalFlags = *allowExp // hostd/gen/compatflags.go (#337)
	o.PyodideDir = *pyodideDir           // hostd/pyodide.go
	o.TestPublicListenDir = *pubTestDir  // internal/hostdrun/testfunnel.go
	restart := make(chan struct{}, 1)
	if *selfUpg {
		o.SelfUpgrade, o.InstallPrefix = true, *instPrefix
		if o.InstallPrefix == "" {
			o.InstallPrefix = installPrefix()
		}
		if in, npm := npminstall.Self(); npm && o.InstallPrefix == "" {
			return fmt.Errorf("-self-upgrade: this tw-hostd was installed by npm (%s in %s), which owns that directory: upgrade it with %s instead", in.Package, in.Dir, npminstall.UpgradeCommand)
		}
		if o.InstallPrefix == "" {
			return fmt.Errorf("-self-upgrade: this tw-hostd doesn't run from an install.sh prefix (no share/tailworks and VERSION next to its bin/): pass -install-prefix")
		}
		o.Restart = func() {
			select {
			case restart <- struct{}{}:
			default:
			}
		}
	}
	rt, err := hostdrun.New(context.Background(), o)
	if err != nil {
		return err
	}
	if err := rt.Start(context.Background()); err != nil {
		rt.Close()
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
	case <-restart:
		log.Info("restarting into the upgraded release (node state kept for rejoin)")
		rt.Close()
		os.Exit(ExitRestart)
	}
	log.Info("shutting down (node state kept for rejoin)")
	return rt.Close()
}

// ExitRestart is the exit status after a self-upgrade installed a new
// release (EX_TEMPFAIL): the service manager starts the new binary.
const ExitRestart = 75

// installPrefix is the install.sh prefix this binary runs from
// (<prefix>/bin/tw-hostd with <prefix>/share/tailworks and <prefix>/VERSION),
// or "".
func installPrefix() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	p := filepath.Dir(filepath.Dir(exe))
	if _, npm := npminstall.At(p); npm { // an npm platform package: npm owns it
		return ""
	}
	for _, f := range []string{"VERSION", filepath.Join("share", "tailworks")} {
		if _, err := os.Stat(filepath.Join(p, f)); err != nil {
			return ""
		}
	}
	return p
}

// runJoin is `tw-hostd join --token-file <file> [--token <key>] --control-url … --tsnet-dir …`:
// join the operator tailnet once with the single-use key `tw host add`
// minted, save the node state and exit. The service then starts without a key.
func runJoin(args []string) error {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("tw-hostd join", flag.ContinueOnError)
	hostID := fs.String("host-id", "", "this host's ID (default: the token file's TW_HOST_ID, else hostname)")
	dataDir := fs.String("data-dir", filepath.Join(home, ".tailworks", "hostd"), "hostd state (the node state goes to <data-dir>/tsnet)")
	tsDir := fs.String("tsnet-dir", "", "node state dir (default <data-dir>/tsnet)")
	control := fs.String("control-url", "https://controlplane.tailscale.com", "operator tailnet control URL (default: the join file's TW_TSNET_CONTROL_URL, else Tailscale's)")
	tokenFile := fs.String("token-file", "", "TS_AUTHKEY=… file with the join key (tw host add --json > …)")
	token := fs.String("token", "", "the join key itself (prefer --token-file; argv is visible to other users)")
	keyTags := fs.Bool("key-tags-only", false, "advertise no tags; the join key carries tag:tw-host (required on Headscale, ADR-015)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// #488: the join file names the host ID, control URL and key-tags-only
	// (a flag still wins); they are kept beside the node state after the join.
	js := hostdrun.ReadJoinSettings(*tokenFile)
	controlGiven := false
	fs.Visit(func(f *flag.Flag) { controlGiven = controlGiven || f.Name == "control-url" })
	if !controlGiven && js.ControlURL != "" {
		*control = js.ControlURL
	}
	*keyTags = *keyTags || js.KeyTagsOnly
	if *hostID == "" {
		*hostID = js.HostID
	}
	if *hostID == "" {
		h, _ := os.Hostname()
		*hostID = h
	}
	if *tsDir == "" {
		*tsDir = filepath.Join(*dataDir, "tsnet")
	}
	key := *token
	if *tokenFile != "" {
		k, err := tsapi.LoadEnvFile(*tokenFile, "TS_AUTHKEY")
		if err != nil {
			return err
		}
		key = k
	}
	if key == "" {
		return errors.New("tw-hostd join needs --token-file or --token")
	}
	if err := os.MkdirAll(*tsDir, 0o700); err != nil {
		return err
	}
	ts := &tsnet.Server{Hostname: hostdrun.HostNodeName(*hostID), Dir: *tsDir, ControlURL: *control, AdvertiseTags: []string{hostdrun.HostTag}, AuthKey: key,
		Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	if *keyTags {
		ts.AdvertiseTags = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st, err := ts.Up(ctx)
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	ts.Close()
	if *tokenFile != "" {
		// M1: keep the registration secret for the first registration;
		// the join key itself is single use.
		hostdrun.PersistRegistrationSecret(*tokenFile, *tsDir)
		if err := hostdrun.SaveJoinSettings(*tsDir, hostdrun.JoinSettings{HostID: *hostID, MgmtURL: js.MgmtURL, ControlURL: *control, KeyTagsOnly: *keyTags}); err != nil {
			fmt.Fprintf(os.Stderr, "tw-hostd join: could not save the join settings beside the node state: %v (pass -host-id, -mgmt and -tsnet-control-url to tw-hostd)\n", err)
		}
		os.Remove(*tokenFile)
	}
	fmt.Printf("joined as %s (%s); node state in %s. Start tw-hostd without a key now (it reads the saved settings; -mgmt if the join file named none).\n", strings.TrimSuffix(st.Self.DNSName, "."), st.TailscaleIPs, *tsDir)
	return nil
}
