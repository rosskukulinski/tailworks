//go:build linux

// tw-guestd is PID 1 of a tailworks microVM guest. It:
//
//  1. mounts the pseudo filesystems and a tmpfs for runtime state;
//  2. sets the clock from the config drive (the platform's egress MACs are
//     timestamped);
//  3. extracts the config drive (/dev/vdb, a plain tar) into /run/tailworks/app;
//  4. brings up lo and eth0 with the static address from guest.json;
//  5. starts the pinned workerd on the delivered config and restarts it if
//     it exits;
//  6. reports boot, readiness and workerd exits to the host over vsock, and
//     answers the host's ops (stats, fsscan, and in debug mode probe).
//
// A warm-pool VM (guest.json "pool") has no tenant code on its config
// drive: guestd runs the host's no-tenant warm-up workerd once (so the
// binary and guest RAM are faulted in), kills it, empties the app dir and
// reports "pooled". It then accepts exactly one injection from the host
// (vsock "chunk" + "load": the bundle, its config, fresh entropy, a boot
// id, the clock, the DO volume device) and continues at step 5. A VM
// restored from the base snapshot resumes in that waiting state.
//
// The guest never holds tailnet keys: there is no tailscale here at all.
// All tailnet identity lives in host netd, on the other side of the TAP.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jsimonetti/rtnetlink"
	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/internal/version"
	"golang.org/x/sys/unix"
)

const workerdBin = "/usr/bin/workerd"

var start = time.Now()

// ---- stage timestamps (Phase 1 boot breakdown) ----

var (
	stMu   sync.Mutex
	stages = map[string]float64{}
)

// bootMS is ms since the guest kernel started (CLOCK_BOOTTIME; unaffected
// by settimeofday).
func bootMS() float64 {
	var ts unix.Timespec
	unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	return float64(ts.Nano()) / 1e6
}

// stage records the end of a boot stage (first time only).
func stage(name string) {
	t := bootMS()
	stMu.Lock()
	if _, ok := stages[name]; !ok {
		stages[name] = t
	}
	stMu.Unlock()
}

func stageCopy() map[string]float64 {
	stMu.Lock()
	defer stMu.Unlock()
	m := make(map[string]float64, len(stages))
	for k, v := range stages {
		m[k] = v
	}
	return m
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tw-guestd: "+format+"\n", args...)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ctr-exec" && os.Getpid() != 1 {
		ctrExec() // a container process's setup stage (container_linux.go)
		return
	}
	stage("init")
	if os.Getpid() != 1 {
		logf("not PID 1; refusing to run")
		os.Exit(2)
	}
	if err := run(); err != nil {
		logf("fatal: %v", err)
	}
	// PID 1 must never exit (the kernel panics; panic=1 reboots, which
	// Firecracker turns into a VM exit). Power off cleanly instead.
	unix.Sync()
	unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
	select {}
}

func run() error {
	if err := mounts(); err != nil {
		return err
	}
	stage("mounts")
	cfg, err := readConfigDrive("/dev/vdb")
	if err != nil {
		return fmt.Errorf("config drive: %w", err)
	}
	stage("config")
	if cfg.UnixNano > 0 {
		tv := unix.NsecToTimeval(cfg.UnixNano + time.Since(start).Nanoseconds())
		if err := unix.Settimeofday(&tv); err != nil {
			logf("settimeofday: %v", err)
		}
	}
	stage("clock")
	if err := netUp(cfg); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	stage("net")
	os.MkdirAll(proto.DODir, 0o700)
	g := &guest{cfg: cfg, out: make(chan proto.Msg, 256), waiters: map[int]chan int{}, loaded: make(chan struct{})}
	if cfg.Pool && cfg.Container == nil {
		// Warm-pool / snapshot-base VM: no tenant code yet. (Container
		// guests are never pool VMs: the host cold-boots them.)
		go g.vsockLoop()
		go g.reap()
		g.send(proto.Msg{Type: "boot", UptimeMS: time.Since(start).Milliseconds(), Stages: stageCopy()})
		g.warmup()
		stage("pooled")
		g.mu.Lock()
		g.pooled = true
		g.mu.Unlock()
		g.send(proto.Msg{Type: "pooled", Stages: stageCopy()})
		<-g.loaded // handle("load"): bundle extracted, volume mounted, token replaced
		g.superviseWorkerd()
		return nil
	}
	if cfg.Volume != "" {
		if err := mountVolume(cfg.Volume, cfg.VolumeFormat); err != nil {
			return fmt.Errorf("DO volume: %w", err)
		}
	}
	stage("volume")
	if cfg.Rosetta != "" {
		if err := rosetta(cfg.Rosetta); err != nil {
			logf("rosetta: %v", err) // x86_64 binaries won't run; the guest still serves
		}
	}

	go g.vsockLoop()
	go g.reap()
	g.send(proto.Msg{Type: "boot", UptimeMS: time.Since(start).Milliseconds(), Stages: stageCopy()})
	if cfg.Container != nil {
		g.runContainer()
		return nil
	}
	g.superviseWorkerd()
	return nil
}

// warmup runs workerd once on the pool config drive's no-tenant config
// until it answers healthy, then kills it and empties the app dir. That
// first run pays the cold-start cost (the workerd binary read into the page
// cache, guest RAM faulted in); the tenant's workerd, started later as a
// new process, finds both warm. Nothing of the warm-up process survives:
// its memory is freed (the kernel zeroes pages before reuse) and its files
// are removed.
func (g *guest) warmup() {
	cfg := g.config()
	if len(cfg.WarmupArgs) == 0 {
		return
	}
	g.mu.Lock()
	g.exits = make(chan int, 1)
	cmd := exec.Command(workerdBin, cfg.WarmupArgs...)
	cmd.Dir = proto.AppDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TZ=UTC"}
	err := cmd.Start()
	if err == nil {
		g.workerdPID = cmd.Process.Pid
	}
	g.mu.Unlock()
	if err != nil {
		logf("warm-up workerd: %v", err)
		return
	}
	stage("warmup_spawn")
	if !g.waitHealthy(20*time.Second, cfg.WarmupHealthCF, "warmup_listen") {
		logf("warm-up workerd never became healthy")
	}
	stage("warmup_healthy")
	unix.Kill(cmd.Process.Pid, unix.SIGKILL)
	select {
	case <-g.exits:
	case <-time.After(5 * time.Second):
		logf("warm-up workerd did not exit")
	}
	g.mu.Lock()
	g.workerdPID = 0
	g.mu.Unlock()
	emptyDir(proto.AppDir)
	emptyDir("/tmp")
	emptyDir("/dev/shm")
	stage("warmup_done")
}

func (g *guest) rewarm() error {
	g.mu.Lock()
	ok := g.pooled && !g.loading && !g.warming
	g.warming = ok
	g.mu.Unlock()
	if !ok {
		return errors.New("not a pooled VM waiting for its load")
	}
	defer func() {
		g.mu.Lock()
		g.warming = false
		g.mu.Unlock()
	}()
	st := bootMS()
	if _, err := readConfigDrive("/dev/vdb"); err != nil {
		return err
	}
	g.warmup()
	stMu.Lock()
	stages["rewarm_ms"] = bootMS() - st
	stMu.Unlock()
	return nil
}

// emptyDir removes everything inside dir (not dir itself).
func emptyDir(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

func (g *guest) isPooled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pooled
}

// ident reports this guest's clocks, boot id and fresh kernel randomness.
func (g *guest) ident() *proto.Ident {
	id := &proto.Ident{UnixNano: time.Now().UnixNano(), Pooled: g.isPooled()}
	var ts unix.Timespec
	unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	id.MonoNS = ts.Nano()
	unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	id.BootNS = ts.Nano()
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	id.BootID = strings.TrimSpace(string(b))
	u, _ := os.ReadFile("/proc/sys/kernel/random/uuid")
	id.UUID = strings.TrimSpace(string(u))
	id.Random = make([]byte, 32)
	unix.Getrandom(id.Random, 0)
	return id
}

func (g *guest) config() proto.GuestConfig {
	g.mu.Lock()
	defer g.mu.Unlock()
	return *g.cfg
}

// chunk stores bundle bytes sent ahead of "load" (pool VMs only).
func (g *guest) chunk(m proto.Msg) error {
	stage("inject_rx")
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.pooled || g.loading {
		return errors.New("not a pooled VM waiting for its load")
	}
	if m.Total <= 0 || m.Total > maxBundle || (g.bundle != nil && int64(len(g.bundle)) != m.Total) {
		return fmt.Errorf("bad bundle size %d", m.Total)
	}
	if g.bundle == nil {
		g.bundle = make([]byte, m.Total)
	}
	if m.Offset < 0 || m.Offset+int64(len(m.Data)) > m.Total {
		return errors.New("chunk out of range")
	}
	copy(g.bundle[m.Offset:], m.Data)
	return nil
}

const maxBundle = 256 << 20

// load applies the host's one-time injection: fresh entropy, boot id and
// clock, the tenant bundle into AppDir, the DO volume; then the supervisor
// starts the tenant's workerd. A VM is loaded at most once.
func (g *guest) load(l *proto.Load) error {
	stage("inject_rx")
	g.mu.Lock()
	if !g.pooled || g.loading || g.warming {
		g.mu.Unlock()
		return errors.New("not a pooled VM waiting for its load")
	}
	g.loading = true // never again, even if this load fails
	b := g.bundle
	g.bundle = nil
	// This VM's own control-channel token from now on (the one it booted
	// with is a pool VM's pre-claim token, shared by every copy restored
	// from one base snapshot): every later hello carries it, and the host
	// refuses the old one already.
	if l != nil && l.CtlToken != "" {
		g.cfg.CtlToken = l.CtlToken
	}
	g.mu.Unlock()
	if l == nil || int64(len(b)) != l.Size {
		return fmt.Errorf("bundle: have %d bytes, load says %d", len(b), l.Size)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != l.SHA256 {
		return errors.New("bundle sha256 mismatch")
	}
	// Entropy before anything else here can use randomness: copies of one
	// snapshot share the kernel CRNG state until reseeded.
	if err := reseed(l.Entropy); err != nil {
		return fmt.Errorf("reseed: %w", err)
	}
	if l.BootID != "" {
		setBootID(l.BootID)
	}
	if l.UnixNano > 0 {
		tv := unix.NsecToTimeval(l.UnixNano)
		if err := unix.Settimeofday(&tv); err != nil {
			logf("settimeofday: %v", err)
		}
	}
	emptyDir(proto.AppDir)
	if _, err := extractTar(bytes.NewReader(b), proto.AppDir, false); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	g.mu.Lock()
	g.cfg.WorkerdArgs, g.cfg.HealthCF, g.cfg.Debug, g.cfg.Volume = l.WorkerdArgs, l.HealthCF, l.Debug, l.Volume
	g.mu.Unlock()
	stage("inject_done")
	if l.Volume != "" {
		if err := waitVolume(l.Volume, l.VolumeSectors); err != nil {
			return err
		}
		if err := mountVolume(l.Volume, false); err != nil { // the host formats a Firecracker DO volume
			return fmt.Errorf("DO volume: %w", err)
		}
	}
	stage("volume")
	g.mu.Lock()
	g.pooled = false
	g.mu.Unlock()
	close(g.loaded)
	return nil
}

// reseed mixes the host's bytes into the kernel's input pool, credited as
// entropy, and forces the CRNG to reseed from it (RNDADDENTROPY +
// RNDRESEEDCRNG; Firecracker's random-for-clones.md, "direct approach").
func reseed(ent []byte) error {
	if len(ent) < 32 {
		return errors.New("need at least 32 bytes of entropy")
	}
	f, err := os.OpenFile("/dev/random", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	// struct rand_pool_info { int entropy_count; int buf_size; __u32 buf[]; }
	buf := make([]byte, 8+len(ent))
	binary.NativeEndian.PutUint32(buf[0:], uint32(len(ent)*8))
	binary.NativeEndian.PutUint32(buf[4:], uint32(len(ent)))
	copy(buf[8:], ent)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDADDENTROPY, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return fmt.Errorf("RNDADDENTROPY: %w", e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), unix.RNDRESEEDCRNG, 0); e != 0 {
		return fmt.Errorf("RNDRESEEDCRNG: %w", e)
	}
	return nil
}

// setBootID bind-mounts a file holding id over the kernel's boot_id.
func setBootID(id string) {
	p := "/run/tailworks/boot_id"
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(id+"\n"), 0o444); err != nil {
		return
	}
	const target = "/proc/sys/kernel/random/boot_id"
	unix.Unmount(target, unix.MNT_DETACH) // a previous override, if any
	if err := unix.Mount(p, target, "", unix.MS_BIND, ""); err != nil {
		logf("boot_id: %v", err)
	}
}

// waitVolume waits until dev has the size the host attached (the VM
// booted with a small placeholder behind it), then drops any cached
// blocks of the placeholder.
func waitVolume(dev string, sectors int64) error {
	sys := "/sys/block/" + filepath.Base(dev) + "/size"
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(sys)
		n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if sectors <= 0 || n == sectors {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: size %d sectors, want %d (the host's volume did not arrive)", dev, n, sectors)
		}
		time.Sleep(time.Millisecond)
	}
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	defer f.Close()
	unix.IoctlSetInt(int(f.Fd()), unix.BLKFLSBUF, 0)
	return nil
}

// mountVolume mounts the persistent DO volume (ext4 made on the host, or
// here if format is set and the device is blank) at DODir. The host
// decrypts it (dm-crypt, or an encrypted disk image on macOS); there is no
// key in here. data=ordered plus the host's flush handling make workerd's
// fsyncs reach the host disk.
func mountVolume(dev string, format bool) error {
	var err error
	for i := 0; i < 100; i++ { // devtmpfs creates the node asynchronously
		if _, err = os.Stat(dev); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	if format {
		if err := formatVolume(dev); err != nil {
			return err
		}
	}
	if err := unix.Mount(dev, proto.DODir, "ext4", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC|unix.MS_NOATIME, "errors=remount-ro,data=ordered"); err != nil {
		return fmt.Errorf("mount %s: %w", dev, err)
	}
	os.Chmod(proto.DODir, 0o700)
	return nil
}

// formatVolume makes the DO filesystem on a new volume. It refuses a
// device that is not blank: the host asks only for new volumes, and a
// wrong answer must not destroy data.
func formatVolume(dev string) error {
	f, err := os.Open(dev)
	if err != nil {
		return err
	}
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	f.Close()
	if !bytes.Equal(head[:n], make([]byte, n)) {
		return fmt.Errorf("refusing to format %s: not blank", dev)
	}
	st := time.Now()
	out, err := foreground(exec.Command("/usr/sbin/mke2fs", "-q", "-F", "-t", "ext4", "-L", "tw-do", "-m", "0",
		"-E", "lazy_itable_init=0,lazy_journal_init=0,nodiscard", dev))
	if err != nil {
		return fmt.Errorf("mke2fs: %v: %s", err, bytes.TrimSpace(out))
	}
	logf("formatted %s in %v", dev, time.Since(st).Round(time.Millisecond))
	return nil
}

// rosettaBinfmt is Apple's documented binfmt_misc entry for Rosetta for
// Linux (x86_64 ELF executables), with the interpreter path left out. The
// escapes are parsed by the kernel, not by Go.
const rosettaBinfmt = `:rosetta:M::\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00:` +
	`\xff\xff\xff\xff\xff\xfe\xfe\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff:`

// rosetta mounts the host's Rosetta for Linux share (virtiofs tag) and
// registers it for x86_64 executables. Flags: F opens the interpreter now
// (it keeps working whatever mount namespace a later process runs in), C
// computes credentials from the binary, not the interpreter.
func rosetta(tag string) error {
	if err := os.MkdirAll(proto.RosettaDir, 0o755); err != nil {
		return err
	}
	if err := unix.Mount(tag, proto.RosettaDir, "virtiofs", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return fmt.Errorf("mount virtiofs %s: %w", tag, err)
	}
	const bf = "/proc/sys/fs/binfmt_misc"
	if _, err := os.Stat(bf + "/register"); err != nil {
		if err := unix.Mount("binfmt_misc", bf, "binfmt_misc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return fmt.Errorf("mount binfmt_misc: %w", err)
		}
	}
	if err := os.WriteFile(bf+"/register", []byte(rosettaBinfmt+proto.RosettaDir+"/rosetta:CF"), 0); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("register binfmt: %w", err)
	}
	logf("rosetta registered for x86_64 binaries")
	return nil
}

func mounts() error {
	for _, m := range []struct {
		src, dst, typ string
		flags         uintptr
		data          string
	}{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755,size=50%"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777,size=64m"},
	} {
		os.MkdirAll(m.dst, 0o755)
		if err := unix.Mount(m.src, m.dst, m.typ, m.flags, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("mount %s: %w", m.dst, err)
		}
	}
	os.MkdirAll("/dev/shm", 0o1777)
	unix.Mount("tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")
	return nil
}

// readConfigDrive extracts the tar on dev into proto.AppDir and parses
// guest.json.
func readConfigDrive(dev string) (*proto.GuestConfig, error) {
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfgb, err := extractTar(bufio.NewReaderSize(f, 1<<20), proto.AppDir, true)
	if err != nil {
		return nil, err
	}
	if cfgb == nil {
		return nil, errors.New(proto.GuestConfigFile + " missing")
	}
	var cfg proto.GuestConfig
	if err := json.Unmarshal(cfgb, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// extractTar extracts a tar stream into dir. With wantConfig, guest.json
// is returned instead of written; otherwise it is refused (a bundle can't
// carry one).
func extractTar(r io.Reader, dir string, wantConfig bool) ([]byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tr := tar.NewReader(r)
	var cfgb []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Clean("/" + h.Name)
		if name == "/" || strings.Contains(h.Name, "..") {
			continue
		}
		p := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o700)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o700)
			b, err := io.ReadAll(io.LimitReader(tr, 256<<20))
			if err != nil {
				return nil, err
			}
			if name == "/"+proto.GuestConfigFile {
				if !wantConfig {
					return nil, errors.New("a bundle may not carry " + proto.GuestConfigFile)
				}
				cfgb = b
				continue
			}
			mode := os.FileMode(0o600)
			if h.Mode&0o100 != 0 {
				mode = 0o700
			}
			if err := os.WriteFile(p, b, mode); err != nil {
				return nil, err
			}
		}
	}
	return cfgb, nil
}

func netUp(cfg *proto.GuestConfig) error {
	c, err := rtnetlink.Dial(nil)
	if err != nil {
		return err
	}
	defer c.Close()
	links, err := c.Link.List()
	if err != nil {
		return err
	}
	var eth, lo uint32
	for _, l := range links {
		if l.Attributes == nil {
			continue
		}
		switch l.Attributes.Name {
		case "eth0":
			eth = l.Index
		case "lo":
			lo = l.Index
		}
	}
	if eth == 0 {
		return errors.New("no eth0")
	}
	ip := net.ParseIP(cfg.IP).To4()
	gw := net.ParseIP(cfg.Gateway).To4()
	if ip == nil || gw == nil || cfg.Prefix <= 0 || cfg.Prefix > 30 {
		return fmt.Errorf("bad address config %q/%d via %q", cfg.IP, cfg.Prefix, cfg.Gateway)
	}
	if lo != 0 {
		if err := c.Address.New(&rtnetlink.AddressMessage{Family: unix.AF_INET, PrefixLength: 8, Index: lo,
			Attributes: &rtnetlink.AddressAttributes{Address: net.IPv4(127, 0, 0, 1).To4(), Local: net.IPv4(127, 0, 0, 1).To4()}}); err != nil && !errors.Is(err, unix.EEXIST) {
			logf("lo address: %v", err)
		}
		c.Link.Set(&rtnetlink.LinkMessage{Family: unix.AF_UNSPEC, Index: lo, Flags: unix.IFF_UP, Change: unix.IFF_UP})
	}
	if cfg.MTU > 0 {
		mtu := uint32(cfg.MTU)
		c.Link.Set(&rtnetlink.LinkMessage{Family: unix.AF_UNSPEC, Index: eth, Attributes: &rtnetlink.LinkAttributes{MTU: mtu}})
	}
	if err := c.Address.New(&rtnetlink.AddressMessage{Family: unix.AF_INET, PrefixLength: uint8(cfg.Prefix), Index: eth,
		Attributes: &rtnetlink.AddressAttributes{Address: ip, Local: ip}}); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("eth0 address: %w", err)
	}
	if err := c.Link.Set(&rtnetlink.LinkMessage{Family: unix.AF_UNSPEC, Index: eth, Flags: unix.IFF_UP, Change: unix.IFF_UP}); err != nil {
		return fmt.Errorf("eth0 up: %w", err)
	}
	// A default route, so that the ADR-019 probes below test the host side
	// (every non-allowed flow must die at host netd), not a missing route.
	if err := c.Route.Add(&rtnetlink.RouteMessage{Family: unix.AF_INET, Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_BOOT,
		Scope: unix.RT_SCOPE_UNIVERSE, Type: unix.RTN_UNICAST,
		Attributes: rtnetlink.RouteAttributes{Gateway: gw, OutIface: eth}}); err != nil && !errors.Is(err, unix.EEXIST) {
		logf("default route: %v", err)
	}
	return nil
}

// ---- guest state + vsock control channel ----

type guest struct {
	cfg *proto.GuestConfig
	out chan proto.Msg

	mu         sync.Mutex
	workerdPID int
	stopping   bool     // shutdown: don't restart workerd
	sfx        string   // stage-name suffix of the current workerd attempt
	exits      chan int // workerd exit statuses, fed by reap()
	ctr        *container
	waiters    map[int]chan int // container processes: pid -> exit status (reap)

	// pool VMs: waiting for the one load (pooled), it has begun (loading:
	// never again), the bundle bytes so far, and closed when it's applied.
	pooled  bool
	loading bool
	warming bool // rewarm running (a load waits for it)
	bundle  []byte
	loaded  chan struct{}
}

func (g *guest) send(m proto.Msg) {
	select {
	case g.out <- m:
	default: // the host is not reading; drop rather than block PID 1
	}
}

// vsockLoop keeps a connection to the host (CID 2) open, writing queued
// messages and serving the host's ops. It reconnects if the host restarts.
// The first retries are fast: after a snapshot restore the connection was
// reset and the host is waiting for this VM to say "pooled".
func (g *guest) vsockLoop() {
	delay := time.Millisecond
	for {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			err = unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: proto.VsockPort})
			if err == nil {
				f := os.NewFile(uintptr(fd), "vsock")
				g.serveHost(f)
				f.Close()
				delay = time.Millisecond
			} else {
				unix.Close(fd)
			}
		}
		time.Sleep(delay)
		delay = min(2*delay, 100*time.Millisecond)
	}
}

func (g *guest) serveHost(f *os.File) {
	done := make(chan struct{})
	var wmu sync.Mutex
	enc := json.NewEncoder(f)
	write := func(m proto.Msg) error {
		wmu.Lock()
		defer wmu.Unlock()
		return enc.Encode(m)
	}
	// The first line names the connection: control (this, authenticated
	// by the boot's token, or once a pool VM is loaded by the load's) or
	// an exec's stdio stream (proto.ExecStreamHello).
	if write(proto.Msg{Type: "hello", Token: g.config().CtlToken, UptimeMS: time.Since(start).Milliseconds(),
		Proto: version.GuestProtocol, GuestVersion: version.Version}) != nil {
		return
	}
	g.mu.Lock()
	pooled := g.pooled
	g.mu.Unlock()
	if pooled {
		// A new connection (first, or after a restore): say we wait.
		if write(proto.Msg{Type: "pooled", Stages: stageCopy()}) != nil {
			return
		}
	}
	go func() {
		defer close(done)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20) // bundle chunks are ≤ 1 MiB (base64)
		for sc.Scan() {
			var m proto.Msg
			if json.Unmarshal(sc.Bytes(), &m) != nil {
				continue
			}
			go func() { write(g.handle(m)) }()
		}
	}()
	for {
		select {
		case m := <-g.out:
			if err := write(m); err != nil {
				g.send(m) // retry on the next connection
				return
			}
		case <-done:
			return
		}
	}
}

func (g *guest) handle(m proto.Msg) proto.Msg {
	r := proto.Msg{Type: "resp", ID: m.ID}
	switch m.Op {
	case "ping":
		r.UptimeMS = time.Since(start).Milliseconds()
	case "stats":
		r.Stats = g.stats()
	case "fsscan":
		r.Found, r.Scanned = fsscan("/")
		r.State = stateFiles()
	case "rewarm":
		// A pooled VM restored from the base snapshot: run the warm-up
		// workerd again (from the config drive, still attached), so the
		// restored copy's guest memory is faulted in before its claim.
		if err := g.rewarm(); err != nil {
			r.Error = err.Error()
		}
		r.Stages = stageCopy()
	case "chunk":
		if err := g.chunk(m); err != nil {
			r.Error = err.Error()
		}
	case "load":
		if err := g.load(m.Load); err != nil {
			r.Error = err.Error()
		}
	case "shutdown":
		if err := g.shutdown(); err != nil {
			r.Error = err.Error()
		}
	case "fsbench":
		b, err := fsbench(m.N)
		if err != nil {
			r.Error = err.Error()
		}
		r.Bench = b
	case "probe":
		if !g.config().Debug {
			r.Error = "probe needs debug mode"
			break
		}
		r.Results = probe(m.Targets)
	case "exec", "signal", "resize", "status":
		g.handleContainer(m, &r)
	case "run":
		if !g.config().Debug {
			r.Error = "run needs debug mode"
			break
		}
		r.Text, r.ExitCode, r.Error = runDebug(m.Argv)
	case "wbench":
		// Bare workerd process start (exec, dynamic loading, binary page
		// faults, no config): `workerd --version`, N times.
		if !g.config().Debug {
			r.Error = "wbench needs debug mode"
			break
		}
		r.Stages = wbench(m.N)
	case "ident":
		if !g.config().Debug && !g.isPooled() {
			r.Error = "ident needs debug mode"
			break
		}
		r.Ident = g.ident()
	case "wkill":
		// SIGKILL workerd (the supervisor restarts it): warm-restart timing.
		if !g.config().Debug {
			r.Error = "wkill needs debug mode"
			break
		}
		g.mu.Lock()
		pid := g.workerdPID
		g.mu.Unlock()
		if pid > 0 {
			unix.Kill(pid, unix.SIGKILL)
		}
	default:
		r.Error = "unknown op " + m.Op
	}
	return r
}

// shutdown stops workerd for good, then syncs and unmounts the DO volume,
// so the host can hand it to the next VM with nothing in flight.
func (g *guest) shutdown() error {
	g.mu.Lock()
	g.stopping = true
	pid := g.workerdPID
	g.mu.Unlock()
	if pid > 0 {
		unix.Kill(pid, unix.SIGTERM)
		for i := 0; i < 2000; i++ { // ≤ 2 s
			if unix.Kill(pid, 0) != nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		unix.Kill(pid, unix.SIGKILL)
		for i := 0; i < 1000 && unix.Kill(pid, 0) == nil; i++ {
			time.Sleep(time.Millisecond)
		}
	}
	unix.Sync()
	if g.config().Volume != "" {
		if err := unix.Unmount(proto.DODir, 0); err != nil {
			// Something still holds it: force it read-only (flushes).
			unix.Mount("", proto.DODir, "", unix.MS_REMOUNT|unix.MS_RDONLY, "")
			return fmt.Errorf("unmount %s: %w (remounted read-only)", proto.DODir, err)
		}
	}
	return nil
}

func doFS() string {
	b, _ := os.ReadFile("/proc/mounts")
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) > 2 && f[1] == proto.DODir {
			return f[0] + " " + f[2]
		}
	}
	return "tmpfs (/run)"
}

// fsbench measures 4 KiB write + fdatasync latency on the DO volume.
func fsbench(n int) (*proto.Bench, error) {
	if n <= 0 || n > 2000 {
		n = 200
	}
	p := filepath.Join(proto.DODir, ".tailworks-fsbench")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer os.Remove(p)
	defer f.Close()
	buf := bytes.Repeat([]byte{0x5a}, 4096)
	var w, s []int64
	for i := 0; i < n; i++ {
		t := time.Now()
		if _, err := f.WriteAt(buf, int64(i%256)*4096); err != nil {
			return nil, err
		}
		t2 := time.Now()
		if err := unix.Fdatasync(int(f.Fd())); err != nil {
			return nil, err
		}
		w = append(w, t2.Sub(t).Microseconds())
		s = append(s, time.Since(t2).Microseconds())
	}
	pct := func(xs []int64, q float64) int64 {
		c := append([]int64(nil), xs...)
		sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
		return c[int(float64(len(c)-1)*q)]
	}
	return &proto.Bench{N: n, FS: doFS(), WriteP50US: pct(w, 0.5), WriteP95US: pct(w, 0.95), SyncP50US: pct(s, 0.5), SyncP95US: pct(s, 0.95)}, nil
}

func (g *guest) stats() *proto.Stats {
	s := &proto.Stats{UptimeMS: time.Since(start).Milliseconds(), DOFS: doFS()}
	var sf unix.Statfs_t
	if unix.Statfs(proto.DODir, &sf) == nil {
		s.DOUsedKB = int64(sf.Blocks-sf.Bfree) * sf.Bsize / 1024
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.MemTotalKB = kv(b, "MemTotal:")
		s.MemAvailableKB = kv(b, "MemAvailable:")
	}
	g.mu.Lock()
	pid := g.workerdPID
	g.mu.Unlock()
	if pid > 0 {
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
			s.WorkerdRSSKB = kv(b, "VmRSS:")
		}
	}
	return s
}

func kv(b []byte, key string) int64 {
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, key) {
			f := strings.Fields(strings.TrimPrefix(ln, key))
			if len(f) > 0 {
				n, _ := strconv.ParseInt(f[0], 10, 64)
				return n
			}
		}
	}
	return 0
}

// fsscan lists files whose names look like tailscale/tsnet state. It does
// not grep contents for key material: the host scans the guest's disks and
// a full memory snapshot for that, so the needles never enter the guest.
func fsscan(root string) (found []string, n int) {
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (p == "/proc" || p == "/sys" || p == "/dev") {
			return filepath.SkipDir
		}
		n++
		b := strings.ToLower(d.Name())
		if strings.Contains(b, "tailscale") || strings.HasPrefix(b, "tsnet") || strings.HasSuffix(b, ".state") ||
			b == "derpmap.cached.json" || strings.Contains(b, "machinekey") || strings.Contains(b, "nodekey") {
			found = append(found, p)
		}
		return nil
	})
	return found, n
}

// runDebug runs argv (relative to AppDir) for a platform test and returns its
// combined output (≤ 64 KiB) and exit code.
func runDebug(argv []string) (string, int, string) {
	if len(argv) == 0 {
		return "", -1, "no argv"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = proto.AppDir
	cmd.Env = []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TZ=UTC"}
	out, err := foreground(cmd)
	if len(out) > 64<<10 {
		out = out[:64<<10]
	}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return string(out), ee.ExitCode(), ""
	case err != nil:
		return string(out), -1, err.Error()
	}
	return string(out), 0, ""
}

// stateFiles lists every file under the guest's writable state dirs with
// its size: a pooled VM must have none of a workload's (or the warm-up's).
func stateFiles() map[string]int64 {
	out := map[string]int64{}
	for _, root := range []string{"/run", "/tmp", "/dev/shm"} {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			var sz int64
			if fi, err := d.Info(); err == nil {
				sz = fi.Size()
			}
			out[p] = sz
			return nil
		})
	}
	return out
}

func wbench(n int) map[string]float64 {
	if n <= 0 || n > 50 {
		n = 5
	}
	out := map[string]float64{}
	for i := 0; i < n; i++ {
		st := time.Now()
		if err := exec.Command(workerdBin, "--version").Run(); err != nil {
			out["error"] = 1
		}
		out[fmt.Sprintf("run%02d", i)] = float64(time.Since(st).Microseconds()) / 1000
	}
	return out
}

func probe(targets []string) []proto.ProbeResult {
	out := make([]proto.ProbeResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := time.Now()
			c, err := net.DialTimeout("tcp", t, 1500*time.Millisecond)
			r := proto.ProbeResult{Target: t, MS: time.Since(st).Milliseconds()}
			switch {
			case err == nil:
				// A connect can succeed against a TCP stack that accepts
				// then resets; count it only if the peer actually talks
				// or holds the connection open.
				c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				_, rerr := c.Read(make([]byte, 1))
				c.Close()
				var ne net.Error
				if rerr == nil || errors.As(rerr, &ne) && ne.Timeout() {
					r.Result = "connected"
				} else {
					r.Result, r.Detail = "reset", rerr.Error()
				}
			case errors.Is(err, unix.ECONNREFUSED) || errors.Is(err, unix.ECONNRESET):
				r.Result = "refused"
			case errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH):
				r.Result = "unreachable"
			default:
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					r.Result = "timeout"
				} else {
					r.Result = "error"
				}
				r.Detail = err.Error()
			}
			out[i] = r
		}()
	}
	wg.Wait()
	return out
}

// ---- workerd supervision ----

// fgMu keeps reap() from stealing the exit status of a command guestd
// itself waits for (exec.Cmd.Wait): such commands run under its read lock
// (foreground), reap's Wait4(-1) under its write lock.
var fgMu sync.RWMutex

// foreground runs cmd to completion and returns its combined output, with
// reap() held off until it has been waited for.
func foreground(cmd *exec.Cmd) ([]byte, error) {
	fgMu.RLock()
	defer fgMu.RUnlock()
	return cmd.CombinedOutput()
}

// reap collects every child (PID 1 inherits orphans) and reports workerd's
// exit status.
func (g *guest) reap() {
	sig := make(chan os.Signal, 8)
	signal.Notify(sig, syscall.SIGCHLD)
	for range sig {
		fgMu.Lock()
		for {
			var ws unix.WaitStatus
			pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
			g.mu.Lock()
			if ch := g.waiters[pid]; ch != nil {
				delete(g.waiters, pid)
				code := ws.ExitStatus()
				if ws.Signaled() {
					code = 128 + int(ws.Signal())
				}
				ch <- code // buffered (1)
			}
			if pid == g.workerdPID && g.exits != nil {
				g.workerdPID = 0
				select {
				case g.exits <- ws.ExitStatus():
				default:
				}
			}
			g.mu.Unlock()
		}
		fgMu.Unlock()
	}
}

func (g *guest) superviseWorkerd() {
	g.mu.Lock()
	g.exits = make(chan int, 1)
	g.mu.Unlock()
	for attempt := 0; ; attempt++ {
		st := time.Now()
		sfx := ""
		if attempt > 0 {
			sfx = fmt.Sprintf("#%d", attempt) // a restart's own stage times
		}
		g.mu.Lock()
		g.sfx = sfx
		g.mu.Unlock()
		cmd := exec.Command(workerdBin, g.config().WorkerdArgs...)
		cmd.Dir = proto.AppDir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TZ=UTC"}
		g.mu.Lock()
		err := cmd.Start()
		if err == nil {
			g.workerdPID = cmd.Process.Pid
		}
		g.mu.Unlock()
		stage("workerd_spawn" + sfx)
		if err != nil {
			logf("start workerd: %v", err)
			g.send(proto.Msg{Type: "workerd-exit", ExitCode: -1, Error: err.Error(), Restarts: attempt})
			if attempt >= 5 {
				select {}
			}
			time.Sleep(time.Second)
			continue
		}
		healthy := make(chan struct{})
		go func() {
			if g.waitHealthy(20*time.Second, g.config().HealthCF, "workerd_listen"+sfx) {
				close(healthy)
			}
		}()
		select {
		case <-healthy:
			stage("healthy" + sfx)
			g.send(proto.Msg{Type: "ready", UptimeMS: time.Since(start).Milliseconds(), WorkerdMS: time.Since(st).Milliseconds(), Restarts: attempt, Stages: stageCopy()})
			code := <-g.exits
			g.send(proto.Msg{Type: "workerd-exit", ExitCode: code, Restarts: attempt})
		case code := <-g.exits:
			g.send(proto.Msg{Type: "workerd-exit", ExitCode: code, Restarts: attempt, Error: "exited before healthy"})
		}
		g.mu.Lock()
		stopping := g.stopping
		g.mu.Unlock()
		if stopping {
			logf("workerd stopped for shutdown")
			select {}
		}
		if attempt >= 5 {
			logf("workerd keeps exiting; giving up")
			select {}
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
}

func (g *guest) waitHealthy(d time.Duration, healthCF, listenStage string) bool {
	cl := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(d)
	addr := fmt.Sprintf("http://127.0.0.1:%d/_tw/admin", proto.IngressPort)
	tcp := fmt.Sprintf("127.0.0.1:%d", proto.IngressPort)
	listening := false
	for time.Now().Before(deadline) {
		if !listening {
			// workerd binds its sockets once the config (and every
			// worker in it) is loaded: the end of "bundle load".
			c, err := net.DialTimeout("tcp", tcp, 100*time.Millisecond)
			if err != nil {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			c.Close()
			listening = true
			stage(listenStage)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, "POST", addr, bytes.NewReader(nil))
		req.Header.Set("x-tw-cf", healthCF)
		resp, err := cl.Do(req)
		cancel()
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
