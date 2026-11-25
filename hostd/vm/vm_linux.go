//go:build linux

package vm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/vm/helper"
	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
	"github.com/rosskukulinski/tailworks/netd"
	"golang.org/x/sys/unix"
)

// Config configures the Firecracker backend.
type Config struct {
	// Helper is tw-vmhelper's socket (installed by `sudo tw host setup
	// --microvm`): the privileged netns/TAP/jailer/dm-crypt half. If it is
	// "" and this process is root, an in-process helper is used instead
	// (tests; Firecracker and Jailer name its binaries then).
	Helper      string
	Firecracker string // in-process helper only (default /usr/local/bin/firecracker)
	Jailer      string // in-process helper only (default /usr/local/bin/jailer)
	Kernel      string // uncompressed guest kernel (vmlinux)
	// ContainerKernel boots container guests (needs erofs + overlayfs:
	// guest/build-kernel.sh); "" = Kernel.
	ContainerKernel string
	Rootfs          string // guest image from guest/build-image.sh (mounted read-only by every VM)
	RunDir          string // per-VM dirs: config drive, console log
	MemMiB          int    // guest memory (default 512)
	VCPUs           int    // default 2 (1 vCPU: workerd's threads contend; p50 latency ~30 ms nested)
	MTU             int    // default 1500
	BootArgs        string // kernel cmdline (default: DefaultBootArgs for this arch)
	// HugePages backs guest memory with 2 MiB pages (needs free hugetlbfs
	// pages on the host: vm.nr_hugepages). Far fewer stage-2 faults, which
	// dominate workerd start-up under nested virtualization (measured in
	// Lima: InstanceStart → workerd ready 3.7 s without, 0.43 s with).
	// "auto" (the default, "") uses them when enough are free at boot.
	HugePages string // "" | "auto" | "on" | "off"
	// ReadyTimeout bounds boot → workerd healthy in the guest (default 30 s).
	ReadyTimeout time.Duration
	// Pool is the warm pool's size: pre-booted, warm VMs without tenant
	// code that Boot() claims instead of booting (see pool_linux.go). 0 =
	// auto (half the huge-page reservation's VMs, at most 2; off without
	// huge pages), < 0 = off. Durable Object (workerd) guests only:
	// container guests (BootContainer) always cold-boot (ADR-032 + fast-vm:
	// docs/containers.md "Warm pool and snapshots").
	Pool int
	// PoolBudgetMiB caps the guest RAM of all of this backend's VMs for
	// refills (default: the huge-page reservation, vm.nr_hugepages × 2 MiB).
	PoolBudgetMiB int
	// Snapshot (off by default; tw-hostd -vm-snapshot): restore VMs from a
	// base snapshot (a pooled VM: warm, no tenant code, no keys) instead of
	// booting them — for pool refills, and on demand when the pool is
	// empty. Guest memory is served lazily (userfaultfd). See
	// snapshot_linux.go and the ADR draft on cross-tenant memory sharing.
	// Durable Object guests only, like Pool.
	Snapshot bool
	// SnapshotRefill: refill the pool by restoring the base (+ re-warm)
	// instead of booting. Refills are 6x faster (~0.1 vs ~0.6 s, in the
	// background), but claims of restored VMs were measured 20-50 ms slower
	// (STATUS FV), so the default is off: boot-filled pool, and restores
	// for pool misses only.
	SnapshotRefill bool
	// BaseRotate rebuilds the base snapshot this often (default 1 h; < 0:
	// never). Idle pool VMs restored from the old base are discarded.
	BaseRotate time.Duration
	Log        *slog.Logger
}

// Backend is the Firecracker microVM backend.
type Backend struct {
	cfg    Config
	seq    atomic.Int64
	ownDir bool
	sock   string
	// in-process helper (root only)
	srv     *helper.Server
	srvLn   *net.UnixListener
	srvBase string
	via     string
	pool    *pool

	baseMu        sync.Mutex
	base          *baseSnap         // current base snapshot (Snapshot on)
	baseTokens    map[string]string // base ID → its control-channel token (baseSnap.ctlToken), while the base exists
	baseRotations int
	// baseStale: the guest changed since the current base was built
	// (baseID refuses it; logged once until the next base).
	baseStale bool
	baseStop  context.CancelFunc
	baseDone  chan struct{}
	baseWG    sync.WaitGroup
}

// Available reports whether this host can run microVMs: KVM, the guest
// kernel and image, and either the helper (as a normal user) or root.
func Available(cfg Config) error {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("no /dev/kvm: %w", err)
	}
	for _, p := range []string{cfg.Kernel, cfg.Rootfs} {
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}
	if err := CheckArch(cfg.Kernel, cfg.Rootfs, runtime.GOARCH); err != nil {
		return err
	}
	if cfg.Helper != "" {
		r, err := helperapi.Ping(cfg.Helper)
		if err != nil {
			return fmt.Errorf("microVM helper at %s: %w (run `sudo tw host setup --microvm`)", cfg.Helper, err)
		}
		if err := helperapi.Check(r); err != nil {
			return fmt.Errorf("microVM helper at %s: %w (re-run `sudo tw host setup --microvm`)", cfg.Helper, err)
		}
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("the microVM tier needs the tw-vmhelper service (run `sudo tw host setup --microvm`)")
	}
	return nil
}

// New returns a Backend.
func New(cfg Config) (*Backend, error) {
	if cfg.Firecracker == "" {
		cfg.Firecracker = "/usr/local/bin/firecracker"
	}
	if cfg.Jailer == "" {
		cfg.Jailer = "/usr/local/bin/jailer"
	}
	if cfg.MemMiB == 0 {
		cfg.MemMiB = 512
	}
	if cfg.VCPUs == 0 {
		cfg.VCPUs = 2
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1500
	}
	if cfg.BootArgs == "" {
		cfg.BootArgs = DefaultBootArgs(runtime.GOARCH)
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 30 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if err := Available(cfg); err != nil {
		return nil, err
	}
	b := &Backend{cfg: cfg, sock: cfg.Helper, via: "tw-vmhelper " + cfg.Helper}
	if cfg.RunDir == "" {
		d, err := os.MkdirTemp("/tmp", "tw-vm-")
		if err != nil {
			return nil, err
		}
		b.cfg.RunDir, b.ownDir = d, true
	}
	if err := os.MkdirAll(b.cfg.RunDir, 0o700); err != nil {
		return nil, err
	}
	if b.sock == "" {
		if err := b.startInProcessHelper(); err != nil {
			b.Close()
			return nil, err
		}
	}
	if cfg.Snapshot {
		if b.cfg.BaseRotate == 0 {
			b.cfg.BaseRotate = time.Hour
		}
		b.base = &baseSnap{ready: make(chan struct{})}
		b.baseDone = make(chan struct{})
	}
	b.pool = newPool(b)
	if b.base != nil {
		b.startBase()
	}
	return b, nil
}

// startInProcessHelper runs the privileged helper inside this (root)
// process, on a private socket, with its own jail dir and resource names.
func (b *Backend) startInProcessHelper() error {
	inst := fmt.Sprintf("p%d", os.Getpid())
	b.srvBase = filepath.Join("/var/lib", "tw-vm-"+inst)
	helper.SweepDeadInProcess(os.Getpid(), b.cfg.Log.With("component", "vmhelper")) // a crashed predecessor's VMs (#587)
	srv, err := helper.New(helper.Config{Firecracker: b.cfg.Firecracker, Jailer: b.cfg.Jailer,
		JailBase: filepath.Join(b.srvBase, "jail"), StateDir: b.srvBase, UIDBase: 3910000000 + (os.Getpid()%1000)*1000,
		MaxVMs: 1000, Instance: inst, Log: b.cfg.Log.With("component", "vmhelper")})
	if err != nil {
		return fmt.Errorf("in-process microVM helper: %w", err)
	}
	b.sock = filepath.Join(b.cfg.RunDir, "helper.sock")
	os.Remove(b.sock)
	ln, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: b.sock, Net: "unixpacket"})
	if err != nil {
		return err
	}
	os.Chmod(b.sock, 0o600)
	b.srv, b.srvLn, b.via = srv, ln, "in-process helper (root)"
	go srv.Serve(ln)
	return nil
}

// Close stops the in-process helper (if any) and removes the backend's run
// directory if it created it. Running VMs must be stopped first.
func (b *Backend) Close() error {
	b.stopBase()
	if b.pool != nil {
		b.pool.close()
	}
	b.closeBase()
	if b.srv != nil {
		b.srvLn.Close()
		b.srv.Close()
		b.srv.Sweep() // what the stopped VMs left (netns, jails)
		os.RemoveAll(b.srvBase)
	}
	if b.ownDir {
		return os.RemoveAll(b.cfg.RunDir)
	}
	return nil
}

// Name implements sandbox.Backend.
func (b *Backend) Name() string { return "firecracker" }

// Via says how VMs are launched (helper service or in-process).
func (b *Backend) Via() string { return b.via }

// Machine is one running Firecracker microVM.
type Machine struct {
	agent
	b      *Backend
	id     string
	dir    string
	conn   *helperapi.Conn
	pid    int
	tapFD  int
	vsock  net.Listener
	volume bool

	stopped atomic.Bool
	exitErr error
	disks   []string
	cleanup sync.Once
	// the helper's time from VMM exit to volume released (exit event), and
	// its other exit stats (restored VMs: uffd faults served)
	releaseMS float64
	exitStats map[string]float64

	ctr *ctrState // container guests only
}

// Boot implements sandbox.Backend. With a warm pool, it claims a pooled VM
// (booted earlier, warm, holding no tenant code) and injects the workload
// into it; otherwise (pool off or empty) it restores one from the base
// snapshot (snapshots on) or cold-boots a VM with the workload on its
// config drive. Container guests never come from the pool or a snapshot
// (BootContainer cold-boots).
func (b *Backend) Boot(ctx context.Context, s sandbox.Spec) (sandbox.VM, error) {
	if b.pool != nil {
		m := b.pool.take()
		b.pool.kick()
		if m != nil {
			vm, err := b.claim(ctx, m, s)
			if err == nil || helperapi.IsCode(err, helperapi.CodeVolumeBusy) || helperapi.IsCode(err, helperapi.CodeVolumeFenced) ||
				helperapi.IsCode(err, helperapi.CodeVolumeKey) || helperapi.IsCode(err, helperapi.CodeVolumeBad) || ctx.Err() != nil {
				return vm, err
			}
			// The pooled VM failed (it is discarded): boot one cold.
			b.cfg.Log.Warn("pooled microVM failed its claim; booting cold", "spec", s.ID, "err", err)
		}
	}
	if b.curBase() != nil {
		// Snapshots on and no pooled VM: restore one now and claim it.
		t0 := time.Now()
		if id := b.baseID(ctx, true); id != "" {
			m, err := b.boot(ctx, bootOpts{name: s.ID, restore: id, log: s.Log})
			if err == nil {
				return b.claimAt(ctx, m, s, t0, true)
			}
			b.cfg.Log.Warn("microVM restore failed; booting cold", "spec", s.ID, "err", err)
		}
	}
	m, err := b.boot(ctx, bootOpts{name: s.ID, spec: &s, log: s.Log})
	if err != nil {
		return nil, err // not a typed nil *Machine
	}
	return m, nil
}

// bootOpts: one workerd-guest VM boot. A cold boot carries the workload
// (spec, on the config drive); a pool boot (pool) carries none: its config
// drive is the warm-up app, its DO drive a placeholder (volume slot), its
// stack has no listeners, and it is ready when the guest says "pooled".
type bootOpts struct {
	name    string
	spec    *sandbox.Spec
	log     *slog.Logger
	pool    bool
	restore string // restore this base snapshot (a pooled VM) instead of booting

	// base: a pool boot that becomes the base snapshot (StartRequest.Base:
	// the helper refuses snapshot-base on any other VM).
	base bool
}

// boot turns bootOpts into a launch (the boot path shared with container
// guests).
func (b *Backend) boot(ctx context.Context, o bootOpts) (*Machine, error) {
	name := o.name
	req := &helperapi.StartRequest{Name: name, MemMiB: b.cfg.MemMiB, VCPUs: b.cfg.VCPUs, HugePages: b.cfg.HugePages,
		BootArgs: b.cfg.BootArgs, MTU: b.cfg.MTU, GuestMAC: guestMAC, GuestCID: 3, VsockPort: proto.VsockPort}
	l := launch{name: name, log: o.log, kernel: b.cfg.Kernel, req: req}
	switch {
	case o.restore != "":
		// A restored copy's guestd holds the base's control-channel token
		// (it is in the snapshot's memory) until its claim's load replaces
		// it with the copy's own.
		tok := b.baseToken(o.restore)
		if tok == "" {
			return nil, fmt.Errorf("base snapshot %s is gone", o.restore)
		}
		req.Restore = o.restore
		l.pool, l.restore, l.ctlToken = true, o.restore, tok
		// (the jail needs a config drive file; the restored guest never
		// reads it, and it carries no token: the base's lives in memory)
		l.drive = func(p, _ string) error { return writePoolDrive(p, b.cfg.MTU, "") }
	case o.pool:
		req.VolumeSlot = true
		req.Base = o.base
		l.pool = true
		l.drive = func(p, token string) error { return writePoolDrive(p, b.cfg.MTU, token) }
	default:
		s := o.spec
		l.handlers = s.Handlers
		l.drive = func(p, token string) error { return writeConfigDrive(p, *s, b.cfg.MTU, token, nil) }
		if v := s.Volume; v != nil {
			l.extra = func(m *Machine) (*os.File, error) {
				f, format, err := openVolume(v)
				if err != nil {
					return nil, fmt.Errorf("volume %s: %w", v.Name, err)
				}
				req.Volume = &helperapi.Volume{Name: v.Name, Epoch: v.Epoch, Key: v.Key, Format: format}
				m.disks = append(m.disks, v.Path)
				m.volume = true
				m.info.Volume = v.Name
				return f, nil
			}
		}
	}
	if l.pool && b.pool != nil && b.pool.hugeOnly {
		req.HugePages = "on" // a pool VM never falls back to 4 KiB pages
	}
	return b.launch(ctx, l)
}

// launch is one VM boot's inputs (Boot: a workerd guest, cold or for the
// pool; BootContainer: a container guest).
type launch struct {
	name     string
	log      *slog.Logger
	kernel   string
	drive    func(path, token string) error // writes the config drive (token: GuestConfig.CtlToken)
	req      *helperapi.StartRequest
	extra    func(m *Machine) (*os.File, error) // the fifth drive (DO volume or container image)
	handlers map[uint16]http.Handler
	gnet     sandbox.ContainerNet // a container guest's egress proxy (nil: no egress)
	ctr      *ctrState
	// pool: a warm-pool or base VM (ready when the guest says "pooled");
	// restore: the base snapshot it is restored from; ctlToken: the token
	// its guestd already holds (a restore: the base's; "" = a fresh one).
	pool     bool
	restore  string
	ctlToken string
}

func (b *Backend) launch(ctx context.Context, l launch) (_ *Machine, err error) {
	t0 := time.Now()
	n := b.seq.Add(1)
	id := fmt.Sprintf("%d-%s-%d", os.Getpid(), l.name, n)
	m := &Machine{agent: newAgent(), b: b, id: id, dir: filepath.Join(b.cfg.RunDir, id), tapFD: -1, ctr: l.ctr}
	if l.ctlToken != "" {
		m.ctlToken = l.ctlToken
	}
	m.info.Stages = map[string]float64{}
	m.info.Source = "cold"
	if l.pool {
		m.info.Source = "pool"
	}
	if l.restore != "" {
		m.info.Source, m.info.Base = "restore", l.restore
	}
	stage := func(name string) { m.info.Stages[name] = float64(time.Since(t0).Microseconds()) / 1000 }
	if l.ctr != nil {
		l.ctr.m = m
		m.onEvent, m.onStream = l.ctr.event, m.attachStream
	}
	m.log = b.cfg.Log.With("vm", id)
	if l.log != nil {
		m.log = l.log.With("vm", id)
	}
	defer func() {
		if err != nil {
			m.teardown()
		}
	}()
	os.RemoveAll(m.dir)
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	// 1. Config drive: the instance dir + guest.json, as a raw tar (a pool
	// VM's: the warm-up app, no tenant code).
	drive := filepath.Join(m.dir, "config.tar")
	if err := l.drive(drive, m.ctlToken); err != nil {
		return nil, fmt.Errorf("config drive: %w", err)
	}
	m.disks = []string{b.cfg.Rootfs, drive}
	stage("config_drive")

	// 2. The files the helper puts in the jail, as open fds.
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for _, p := range []string{l.kernel, b.cfg.Rootfs, drive} {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	console, err := os.OpenFile(filepath.Join(m.dir, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	files = append(files, console)
	m.info.Console = console.Name()
	req := l.req
	if l.extra != nil {
		f, err := l.extra(m)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	// 3. The helper: netns + TAPs, jail, volume, jailer → Firecracker,
	// API config, InstanceStart.
	m.conn, err = helperapi.Dial(b.sock)
	if err != nil {
		return nil, fmt.Errorf("microVM helper: %w", err)
	}
	stage("helper_dial")
	hs := time.Since(t0)
	resp, fds, err := m.conn.Do(helperapi.Request{Op: "start", Start: req}, files, 90*time.Second)
	req.Volume = nil // drop our reference to the key copy
	if err != nil {
		return nil, fmt.Errorf("microVM start: %w; console: %s", err, m.consoleTail(1500))
	}
	if len(fds) != 2 {
		for _, fd := range fds {
			unix.Close(fd)
		}
		return nil, fmt.Errorf("microVM helper returned %d fds", len(fds))
	}
	m.tapFD = fds[0]
	vf := os.NewFile(uintptr(fds[1]), "vsock")
	m.vsock, err = net.FileListener(vf)
	vf.Close()
	if err != nil {
		return nil, err
	}
	stage("helper_start")
	for k, v := range resp.Timings { // the helper's, relative to its own start
		m.info.Stages["helper."+k] = float64(hs.Microseconds())/1000 + v
	}
	m.pid = resp.PID
	m.start = time.Unix(0, resp.StartedAt)
	m.info.Stages["instance_start"] = float64(m.start.Sub(t0).Microseconds()) / 1000
	m.info.PID = resp.PID
	m.info.HugePages = resp.HugePages
	m.info.VolumeNew = resp.VolumeNew
	m.info.VolumeAttachMS = resp.Timings["volume"]
	if j := resp.Jail; j != nil {
		m.info.Jail = &sandbox.Jail{ID: j.ID, Chroot: j.Chroot, Cgroup: j.Cgroup, CPUMax: j.CPUMax, Seccomp: j.Seccomp,
			Netns: j.Netns, UID: j.UID, GID: j.GID, MemMax: j.MemMax}
	}
	m.vn, err = newVnet(m.tapFD, uint32(b.cfg.MTU), gwMAC, gatewayIP, guestIP, proto.PrefixLen, l.handlers, l.gnet)
	if err != nil {
		return nil, err
	}
	m.ingress = m.ingressProxy()
	stage("vnet")
	go m.acceptCtl(m.vsock)
	go m.watch()
	if b.pool != nil {
		b.pool.track(m) // every VM of this backend counts against the pool's budget
	}

	// 4. Wait for guestd to report workerd healthy (or, in a container
	// guest, the entrypoint started; a pool VM: warm and waiting for its
	// load).
	want, what := m.ready, "ready"
	if l.pool {
		want, what = m.pooled, "pooled"
	}
	rctx, cancel := context.WithTimeout(ctx, b.cfg.ReadyTimeout)
	defer cancel()
	select {
	case <-want:
	case <-m.done:
		m.mu.Lock()
		le := m.lastErr
		m.mu.Unlock()
		if le != "" {
			return nil, fmt.Errorf("microVM exited during boot: %s", le)
		}
		return nil, fmt.Errorf("microVM exited during boot: %v", m.exitErr)
	case <-rctx.Done():
		m.mu.Lock()
		le := m.lastErr
		m.mu.Unlock()
		return nil, fmt.Errorf("microVM not %s after %v; events %v; last %s; console: %s", what, b.cfg.ReadyTimeout, m.eventTypes(), le, m.consoleTail(3000))
	}
	m.info.Total = time.Since(t0)
	stage(what)
	m.log.Info("microVM "+what, "boot_ms", m.info.Boot.Milliseconds(), "ready_ms", m.info.Ready.Milliseconds(),
		"total_ms", m.info.Total.Milliseconds(), "jail", resp.Jail != nil, "volume", m.info.Volume, "volume_ms", m.info.VolumeAttachMS,
		"container", l.ctr != nil)
	return m, nil
}

// claim turns a pooled VM into the workload's: attach its volume (the
// helper swaps it in behind the DO drive), serve its gateway handlers,
// inject the bundle and config over vsock (guestd reseeds, sets the clock,
// takes its own control-channel token, extracts, mounts), and wait for the
// tenant's workerd to be healthy. The VM is single-use: on any error it is
// destroyed, never returned to the pool.
func (b *Backend) claim(ctx context.Context, m *Machine, s sandbox.Spec) (vmi sandbox.VM, err error) {
	return b.claimAt(ctx, m, s, time.Now(), false)
}

// claimAt is claim with stage times relative to t0 (Boot()'s start: an
// on-demand restore happened in between, recorded as "restore").
func (b *Backend) claimAt(ctx context.Context, m *Machine, s sandbox.Spec, t0 time.Time, restored bool) (vmi sandbox.VM, err error) {
	restoreMS := float64(time.Since(t0).Microseconds()) / 1000
	m.mu.Lock()
	boot := m.info.Stages
	m.info.Stages = map[string]float64{}
	for k, v := range boot {
		m.info.Stages["pool."+k] = v
	}
	m.info.PoolIdle = time.Since(m.pooledAt)
	if restored {
		m.info.Stages["restore"] = restoreMS
	}
	m.mu.Unlock()
	stage := func(name string) {
		m.mu.Lock()
		m.info.Stages[name] = float64(time.Since(t0).Microseconds()) / 1000
		m.mu.Unlock()
	}
	if s.Log != nil {
		m.log = s.Log.With("vm", m.id)
	}
	defer func() {
		if err != nil {
			m.Stop()
		}
	}()
	stage("claim")
	ld := &proto.Load{UnixNano: time.Now().UnixNano(), WorkerdArgs: s.WorkerdArgs, HealthCF: s.HealthCF, Debug: s.Debug,
		Entropy: make([]byte, 64), BootID: newUUID()}
	rand.Read(ld.Entropy)
	if v := s.Volume; v != nil {
		f, format, err := openVolume(v)
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", v.Name, err)
		}
		av := &helperapi.Volume{Name: v.Name, Epoch: v.Epoch, Key: v.Key, Format: format}
		resp, _, err := m.conn.Do(helperapi.Request{Op: "attach", Attach: av}, []*os.File{f}, 90*time.Second)
		f.Close()
		av.Key = nil
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.volume = true
		m.disks = append(m.disks, v.Path)
		m.info.Volume, m.info.VolumeNew, m.info.VolumeAttachMS = v.Name, resp.VolumeNew, resp.Timings["volume"]+resp.Timings["patch"]
		m.mu.Unlock()
		ld.Volume, ld.VolumeSectors = proto.VolumeDev, resp.VolumeSectors
		stage("volume_attach")
	}
	if err := m.vn.serve(s.Handlers); err != nil {
		return nil, err
	}
	bundle, err := bundleTar(s.AppDir)
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	sum := sha256.Sum256(bundle)
	ld.Size, ld.SHA256 = int64(len(bundle)), hex.EncodeToString(sum[:])
	ictx, cancel := context.WithTimeout(ctx, b.cfg.ReadyTimeout)
	defer cancel()
	// Chunks are sent back to back and acknowledged; load goes after all.
	const chunk = 1 << 20
	errs := make(chan error, len(bundle)/chunk+1)
	nch := 0
	for off := 0; off < len(bundle); off += chunk {
		end := min(off+chunk, len(bundle))
		nch++
		go func(off, end int) {
			_, err := m.Op(ictx, proto.Msg{Op: "chunk", Offset: int64(off), Total: int64(len(bundle)), Data: bundle[off:end]})
			errs <- err
		}(off, end)
	}
	for i := 0; i < nch; i++ {
		if err := <-errs; err != nil {
			return nil, fmt.Errorf("inject: %w", err)
		}
	}
	ld.UnixNano = time.Now().UnixNano() // now, not at claim start: the guest's clock
	// The claimed VM's own control-channel token (ADR-032 + fast-vm): a
	// pooled VM's guestd holds the token it booted with, and every copy
	// restored from one base holds the base's. From the load on, the host
	// accepts only this fresh one; guestd adopts it with the load and
	// uses it for every reconnect.
	ld.CtlToken = m.rekey()
	if _, err := m.Op(ictx, proto.Msg{Op: "load", Load: ld}); err != nil {
		return nil, fmt.Errorf("inject: %w; console: %s", err, m.consoleTail(1500))
	}
	stage("inject")
	select {
	case <-m.ready:
	case <-m.done:
		return nil, fmt.Errorf("microVM exited during claim: %v", m.exitErr)
	case <-ictx.Done():
		m.mu.Lock()
		le := m.lastErr
		m.mu.Unlock()
		return nil, fmt.Errorf("claimed microVM not ready after %v; last %s; console: %s", b.cfg.ReadyTimeout, le, m.consoleTail(3000))
	}
	stage("ready")
	m.mu.Lock()
	m.info.Total = time.Since(t0)
	m.info.Ready = m.info.Total
	m.mu.Unlock()
	m.log.Info("pooled microVM claimed", "spec", s.ID, "total_ms", m.info.Total.Milliseconds(), "idle_s", int(m.info.PoolIdle.Seconds()),
		"volume", m.info.Volume, "volume_ms", m.info.VolumeAttachMS)
	return m, nil
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// openVolume opens the volume image, creating it (sparse) if missing.
// format reports that it is new (or was created but never formatted).
func openVolume(v *sandbox.Volume) (*os.File, bool, error) {
	if len(v.Key) != 32 {
		return nil, false, errors.New("no volume key")
	}
	size := int64(v.SizeMiB) << 20
	if size == 0 {
		size = 1 << 30
	}
	if err := os.MkdirAll(filepath.Dir(v.Path), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(v.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, false, err
	}
	if st.Size() == 0 {
		if err := f.Truncate(size); err != nil {
			f.Close()
			return nil, false, err
		}
	}
	head := make([]byte, 4096)
	n, _ := f.ReadAt(head, 0)
	blank := true
	for _, c := range head[:n] {
		if c != 0 {
			blank = false
			break
		}
	}
	return f, blank, nil
}

// watch turns the helper's exit event (or a lost helper connection) into
// the VM's exit.
func (m *Machine) watch() {
	var why string
	select {
	case ev := <-m.conn.Events():
		why = ev.Error
		m.mu.Lock()
		m.releaseMS = ev.Timings["volume_release"]
		m.exitStats = ev.Timings
		m.mu.Unlock()
	case <-m.conn.Closed():
		why = "helper connection closed"
	}
	if !m.stopped.Load() {
		if why == "" {
			why = "killed"
		}
		m.exitErr = fmt.Errorf("firecracker exited: %s; console: %s", why, m.consoleTail(1500))
		m.log.Warn("microVM exited", "why", why)
	}
	m.teardown()
	close(m.done)
}

func (m *Machine) consoleTail(n int) string {
	b, _ := os.ReadFile(filepath.Join(m.dir, "console.log"))
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return netd.Redact(string(bytes.TrimSpace(b)))
}

// ---- lifecycle ----

// ExitStats are the helper's stats from the VM's exit (volume release ms;
// for a restored VM, uffd faults and pages copied). nil before the exit.
func (m *Machine) ExitStats() map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.exitStats)
}

// Err implements sandbox.VM.
func (m *Machine) Err() error { return m.exitErr }

// Disks implements sandbox.VM.
func (m *Machine) Disks() []string { return m.disks }

// RSSKB implements sandbox.VM: the firecracker process's RSS.
func (m *Machine) RSSKB() int64 {
	if m.pid > 0 {
		return procRSSKB(m.pid)
	}
	return 0
}

// Kill implements sandbox.VM: the helper SIGKILLs the VMM (no clean
// shutdown; the guest's volume is left as a power cut would leave it).
func (m *Machine) Kill() {
	if m.conn != nil {
		m.conn.Do(helperapi.Request{Op: "kill"}, nil, 5*time.Second)
	}
}

// Stop implements sandbox.VM. With a volume, the guest first stops workerd
// and unmounts it (so every write is on the host), then the VMM is killed
// and the helper releases the volume, which can then be attached to the
// next VM. Stop returns after that release. The same on both arches: arm64
// Firecracker has no ACPI shutdown, and x86_64's SendCtrlAltDel (i8042)
// would add nothing after the vsock shutdown has done the clean part.
func (m *Machine) Stop() {
	if m.volume && m.readyOK.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st := time.Now()
		_, err := m.Op(ctx, proto.Msg{Op: "shutdown"})
		cancel()
		m.log.Info("guest released its volume", "ms", time.Since(st).Milliseconds(), "err", err)
	}
	m.stopped.Store(true)
	kt := time.Now()
	m.Kill()
	select {
	case <-m.done:
		m.mu.Lock()
		rel := m.releaseMS
		m.mu.Unlock()
		m.log.Info("microVM stopped", "kill_to_exit_ms", time.Since(kt).Milliseconds(), "helper_volume_release_ms", rel, "exit", m.ExitStats())
	case <-time.After(15 * time.Second):
		m.log.Warn("microVM did not exit after kill")
	}
	m.teardown()
	if os.Getenv("TW_KEEP_INSTANCES") == "" {
		os.RemoveAll(m.dir)
	}
}

// teardown aborts every flow and drops the helper session (idempotent).
func (m *Machine) teardown() {
	m.cleanup.Do(func() {
		if m.vn != nil {
			m.vn.close()
		}
		if m.tapFD >= 0 {
			unix.Close(m.tapFD)
		}
		if m.vsock != nil {
			m.vsock.Close()
		}
		m.mu.Lock()
		if m.ctl != nil {
			m.ctl.Close()
		}
		m.mu.Unlock()
		if m.conn != nil {
			m.conn.Close()
		}
	})
}

// MemoryDump implements sandbox.VM via a Firecracker full snapshot (taken
// by the helper inside the jail; we get the memory file as an fd).
func (m *Machine) MemoryDump(ctx context.Context, dir string) (string, error) {
	_, fds, err := m.conn.Do(helperapi.Request{Op: "memdump"}, nil, 60*time.Second)
	if err != nil {
		return "", err
	}
	if len(fds) != 1 {
		for _, fd := range fds {
			unix.Close(fd)
		}
		return "", errors.New("memdump: no fd")
	}
	src := os.NewFile(uintptr(fds[0]), "mem")
	defer src.Close()
	mem := filepath.Join(dir, m.id+".mem")
	out, err := os.OpenFile(mem, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return mem, err
}

// PoolStats reports the warm pool (nil: no pool).
func (b *Backend) PoolStats() *PoolStats {
	if b.pool == nil {
		return nil
	}
	s := b.pool.stats()
	return &s
}
