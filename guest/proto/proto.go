// Package proto is the tw-guestd ⇄ tw-hostd contract for the microVM
// tier: the guest config delivered on the config drive, and the
// newline-delimited JSON control channel over vsock.
//
// Nothing in here is secret, and nothing tailnet-related ever crosses it:
// the guest gets workerd's config and bundle, a per-instance egress MAC key
// (platform attribution, not a tailnet credential), and its network
// addresses. Node keys and auth keys stay in host netd.
//
// The one credential is guest-internal: GuestConfig.CtlToken, a random
// per-boot token that authenticates guestd's control connections to the
// host (so a process in the guest other than guestd can't open, take over
// or forge the control channel), and the per-exec Msg.Token that does the
// same for an exec's stdio stream. Neither is worth anything outside that
// one VM's boot.
package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Config drive layout (/dev/vdb): a plain ustar stream (no filesystem), padded
// to a 4 KiB multiple. guestd extracts it into AppDir.
const (
	// GuestConfigFile is the JSON GuestConfig inside the config tar.
	GuestConfigFile = "guest.json"
	// AppDir is where the generated workerd instance dir is extracted.
	AppDir = "/run/tailworks/app"
	// DODir is the guest's DO storage directory: the persistent volume
	// (GuestConfig.Volume) mounted there, else guest tmpfs.
	DODir = "/run/tailworks/do"
	// VolumeDev is the persistent volume's device (the third drive).
	VolumeDev = "/dev/vdc"

	// Container guests (ADR-032) have no DO volume: the third drive is the
	// image (erofs, read-only) and the fourth the instance's scratch disk
	// (ext4, made fresh on the host for every boot; the overlay upper).
	ImageDev   = "/dev/vdc"
	ScratchDev = "/dev/vdd"
)

// Defaults for the guest network (every VM lives in its own network
// namespace on the host, so all of them can use the same addresses).
const (
	GatewayIP   = "192.168.72.1"
	GuestIP     = "192.168.72.2"
	PrefixLen   = 24
	IngressPort = 8080 // workerd ingress (cfBlobHeader x-tw-cf); only host netd dials it
	EgressObj   = 3128 // facet egress (platform Egress, MAC'd object attribution)
	EgressWL    = 3129 // tenant service egress (workload identity)
	HostAPI     = 3130 // local host API (ensure/spawn/ready/kill/audit)
	// VsockPort is the host port guestd connects to (CID 2). Firecracker
	// forwards it to the unix socket <vsock uds>_<VsockPort> on the host.
	VsockPort = 1024
)

// GuestConfig is guest.json.
type GuestConfig struct {
	// UnixNano is host wall-clock time when the drive was written. The
	// guest has no trustworthy clock at boot; the platform's egress MAC
	// timestamps need one within ±2 min.
	UnixNano int64  `json:"unixNano"`
	IP       string `json:"ip"`
	Gateway  string `json:"gateway"`
	Prefix   int    `json:"prefix"`
	MTU      int    `json:"mtu,omitempty"`
	// Workerd is the argv after the binary (config path relative to AppDir).
	WorkerdArgs []string `json:"workerdArgs"`
	// HealthPath is requested on 127.0.0.1:IngressPort with HealthCF as
	// the x-tw-cf header; 200 means ready.
	HealthCF string `json:"healthCF"`
	// Debug enables the probe op (connectivity tests).
	Debug bool `json:"debug,omitempty"`
	// Volume is the block device holding DO storage (ext4, created and
	// decrypted on the host), mounted at DODir. "" = tmpfs (not persistent).
	Volume string `json:"volume,omitempty"`
	// VolumeFormat: Volume is blank and the guest formats it (ext4) before
	// mounting it. The vz backend (macOS) has no mkfs.ext4 on the host and
	// decrypts on the host (an encrypted disk image), so the guest formats
	// plaintext it already sees; under Firecracker the host formats it.
	VolumeFormat bool `json:"volumeFormat,omitempty"`
	// Rosetta is the virtiofs tag of a Rosetta for Linux share (vz on Apple
	// silicon): guestd mounts it read-only at RosettaDir and registers it
	// with binfmt_misc, so x86_64 ELF binaries run in the arm64 guest.
	Rosetta string `json:"rosetta,omitempty"`
	// CtlToken authenticates guestd to the host: the first line of every
	// control connection ("hello") carries it, and the host refuses a
	// connection without it (random per boot; guestd keeps it in memory,
	// and container processes can't read the config drive).
	CtlToken string `json:"ctlToken,omitempty"`
	// Container, if set, makes this a container guest: no workerd; guestd
	// mounts the image + scratch as an overlay root and runs the image's
	// entrypoint in it as a supervised child.
	Container *ContainerConfig `json:"container,omitempty"`

	// Pool: this VM is a warm-pool (or snapshot-base) VM. Its config drive
	// carries no tenant code: only the network config above and the
	// warm-up workerd config (WarmupArgs, run in AppDir). guestd runs that
	// workerd once until healthy (faulting in the workerd binary and guest
	// memory, the cost of a cold start), kills it, empties AppDir and
	// reports "pooled". It then waits for exactly one "load" from the host
	// (the tenant bundle, see Load), and from there on behaves like a
	// config-drive VM. A pooled VM is never loaded twice.
	Pool           bool     `json:"pool,omitempty"`
	WarmupArgs     []string `json:"warmupArgs,omitempty"`
	WarmupHealthCF string   `json:"warmupHealthCF,omitempty"`
}

// WarmupMarker is a file in the warm-up app (pool config drive): fsscan
// reports AppDir's contents, and a pooled VM must show none of it (nor
// anything else) before its load.
const WarmupMarker = "tw-warmup.txt"

// Load is the host's one-time injection into a pooled (or restored) VM:
// the tenant bundle (sent before it in "chunk" messages: Data at Offset,
// Total bytes in all; a tar like the config drive's, without guest.json)
// plus what the config drive would have said.
type Load struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // hex, of the whole bundle tar
	// UnixNano: host wall-clock time now (a pooled VM may have idled for
	// hours; a restored one resumes with the base snapshot's clock).
	UnixNano    int64    `json:"unixNano"`
	WorkerdArgs []string `json:"workerdArgs"`
	HealthCF    string   `json:"healthCF"`
	Debug       bool     `json:"debug,omitempty"`
	// Volume is the DO volume's device ("" = tmpfs). The host has swapped
	// the encrypted volume in behind it already; VolumeSectors is its size
	// in 512-byte sectors, which guestd waits to see before mounting (the
	// placeholder it booted with is smaller).
	Volume        string `json:"volume,omitempty"`
	VolumeSectors int64  `json:"volumeSectors,omitempty"`
	// Entropy (host crypto/rand) is mixed into the guest kernel's pool and
	// the CRNG is reseeded from it before workerd starts: copies restored
	// from one snapshot share the kernel RNG state, and this guest kernel
	// (6.1, aarch64 DeviceTree) has no VMGenID driver to reseed it.
	Entropy []byte `json:"entropy"`
	// BootID replaces /proc/sys/kernel/random/boot_id (identical in every
	// copy of a snapshot otherwise).
	BootID string `json:"bootId"`
	// CtlToken is this VM's own control-channel token from the load on: a
	// pooled VM booted with a pre-claim token, and every copy restored from
	// one base snapshot holds the base's. guestd replaces its token with
	// this one before it answers the load and uses it for every later
	// hello; the host accepts nothing else after the claim.
	CtlToken string `json:"ctlToken,omitempty"`
}

// ContainerConfig is a container guest's process and filesystem config:
// the OCI image config with the start() overrides already applied by the
// host.
type ContainerConfig struct {
	Image   string   `json:"image"`   // block device with the erofs image
	Scratch string   `json:"scratch"` // block device with the fresh ext4 scratch ("" = tmpfs upper)
	Argv    []string `json:"argv"`    // ENTRYPOINT + CMD (or the start() entrypoint)
	Env     []string `json:"env"`     // KEY=VALUE, image env then start() env
	Cwd     string   `json:"cwd,omitempty"`
	User    string   `json:"user,omitempty"` // "", "name", "uid", "uid:gid", "name:group"
	// Pids is pids.max of the container's cgroup in the guest (0 = 4096).
	Pids     int    `json:"pids,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// DNS is the container's nameserver (the gateway, answered by the
	// host's sandbox egress proxy); "" = no DNS (an empty resolv.conf).
	DNS string `json:"dns,omitempty"`
	// CACert is the PEM certificate of the container's HTTPS-intercept CA
	// (ADR-032 D4), installed at CACertPath and appended to the image's
	// trust bundles. Its key never enters the guest.
	CACert string `json:"caCert,omitempty"`
	// Canary is held in guestd's memory as the no-keys scan's positive
	// control (it is not a secret).
	Canary string `json:"canary,omitempty"`
	// ScratchFormat: Scratch is a blank device the guest formats itself
	// (vz: macOS has no mkfs.ext4; Firecracker's helper makes it instead).
	ScratchFormat bool `json:"scratchFormat,omitempty"`
	// CPUQuotaUS, if > 0, is cpu.max (µs per 100 ms) of the guest's
	// container cgroup: the fractional-vCPU cap where the host can't cap
	// the VMM itself (vz). Firecracker caps the VMM's cgroup instead.
	CPUQuotaUS int `json:"cpuQuotaUs,omitempty"`
}

// CACertPath is where a container finds its HTTPS-intercept CA certificate
// (Cloudflare's path).
const CACertPath = "/etc/cloudflare/certs/cloudflare-containers-ca.crt"

// CATrustBundles are the image trust stores the CA is appended to, where
// they exist (Debian/Ubuntu/Alpine, RHEL/Fedora, and the OpenSSL default).
var CATrustBundles = []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"}

// Container-mode stream framing.
const (
	// ExecStreamHello is the Type of the first line a guest sends on a new
	// vsock connection that carries one exec's stdio (Msg.Exec names the
	// exec, Msg.Token is the stream token the host issued in the exec op)
	// instead of the control channel.
	ExecStreamHello = "stream"

	// Exec stream frames: a 1-byte kind, a 4-byte big-endian length, then
	// the payload. guest → host: FrameStdout, FrameStderr, FrameExit (int32
	// big-endian exit code; the last frame). host → guest: FrameStdin,
	// FrameStdinEOF (half-close; empty).
	FrameStdin    = 0
	FrameStdout   = 1
	FrameStderr   = 2
	FrameExit     = 3
	FrameStdinEOF = 4
	// MaxFrame bounds one frame's payload.
	MaxFrame = 1 << 20
)

// ExecSpec is an "exec" op's process (container guests).
type ExecSpec struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env,omitempty"` // KEY=VALUE, over the container's env
	Cwd  string   `json:"cwd,omitempty"`
	User string   `json:"user,omitempty"`
	// Stdin: the host streams stdin (else /dev/null).
	Stdin bool `json:"stdin,omitempty"`
	// Stdout "pipe" (default) | "ignore"; Stderr "pipe" (default) |
	// "ignore" | "combined" (into stdout).
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	// PTY runs it on a pseudo-terminal (stdout carries the terminal's
	// output; there is no separate stderr).
	PTY  bool `json:"pty,omitempty"`
	Cols int  `json:"cols,omitempty"`
	Rows int  `json:"rows,omitempty"`
}

// RosettaDir is where the Rosetta share is mounted in the guest.
const RosettaDir = "/run/rosetta"

// Msg is one control-channel message (either direction).
//
// guest → host: Type "hello" (the first line of a control connection;
// Token = GuestConfig.CtlToken, or after a pool VM's load Load.CtlToken),
// "boot" (once), "ready" (workerd healthy), "workerd-exit", "log",
// "pooled" (a pool VM is warm and waits for its load; sent again on every
// new connection while it waits, e.g. after a snapshot restore), or "resp"
// (answer to an op, same ID).
// host → guest: Op "probe", "stats", "fsscan", "ping", "shutdown" (stop
// workerd, sync and unmount the volume: the host may then release it),
// "fsbench" (write+fsync latency on the DO volume), for pool VMs "chunk"
// (bundle bytes) then "load" (Load: once), and in debug mode "run" (run
// Argv from AppDir, return its output and exit code: platform tests such
// as the Rosetta check, not a tenant exec API) and "ident".
//
// Container guests: guest → host Type "ctr-ready" (the entrypoint runs:
// PID), "ctr-exit" (it exited: ExitCode, 128+n for signal n), "exec-exit"
// (Exec, ExitCode). host → guest Op "exec" (Exec id + ExecSpec + Token: the
// guest first opens the exec's stdio stream, naming Exec and Token, then
// answers with the PID),
// "signal" (Signal to the entrypoint, or to Exec's process if set),
// "resize" (Exec, Cols, Rows) and "status" (Running, PID, Exited, ExitCode).
// Container guests are never pool VMs (they always cold-boot).
type Msg struct {
	Type string `json:"type,omitempty"`
	ID   int64  `json:"id,omitempty"`
	Op   string `json:"op,omitempty"`

	// boot / ready
	UptimeMS  int64  `json:"uptimeMs,omitempty"`
	WorkerdMS int64  `json:"workerdMs,omitempty"`
	ExitCode  int    `json:"exitCode,omitempty"`
	Restarts  int    `json:"restarts,omitempty"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
	// Stages (boot / ready) are guest-side stage timestamps in ms since the
	// guest kernel started (CLOCK_BOOTTIME): "init" (guestd is PID 1),
	// "mounts", "config", "clock", "net", "volume", "workerd_spawn"
	// (exec returned), "workerd_listen" (ingress accepts TCP), "healthy".
	Stages map[string]float64 `json:"stages,omitempty"`

	// probe
	Targets []string      `json:"targets,omitempty"`
	Results []ProbeResult `json:"results,omitempty"`

	// stats
	Stats *Stats `json:"stats,omitempty"`

	// fsbench: N writes of 4 KiB + fdatasync on DODir
	N     int    `json:"n,omitempty"`
	Bench *Bench `json:"bench,omitempty"`

	// run (debug): argv; the answer's Text is stdout+stderr (capped)
	Argv []string `json:"argv,omitempty"`

	// fsscan: files whose name looks like tailscale state; and every file
	// under the guest's state dirs (AppDir, DODir, /tmp, /run/tailworks) with
	// its size, so a test can check a pooled VM holds no workload's state.
	Found   []string         `json:"found,omitempty"`
	Scanned int              `json:"scanned,omitempty"`
	State   map[string]int64 `json:"state,omitempty"`

	// chunk / load (pool VMs)
	Data   []byte `json:"data,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Total  int64  `json:"total,omitempty"`
	Load   *Load  `json:"load,omitempty"`

	// ident (debug): what makes this guest unique, for the snapshot
	// uniqueness tests.
	Ident *Ident `json:"ident,omitempty"`

	// hello: GuestConfig.CtlToken (Load.CtlToken once a pool VM is
	// loaded); exec / ExecStreamHello: the exec's
	// stream token (random per exec, issued by the host)
	Token string `json:"token,omitempty"`
	// hello: the guest's protocol (internal/version.GuestProtocol; 0 = a
	// guest from before protocol reporting, which speaks 1) and its tailworks
	// release version (ADR-041 §2). hostd and its guest
	// image ship in one tarball, so a mismatch means a mixed install:
	// hostd logs it, and treats an "unknown op" reply to an op newer than
	// the guest's protocol as "feature absent" (UnknownOp), not a failure.
	Proto        int    `json:"proto,omitempty"`
	GuestVersion string `json:"guestVersion,omitempty"`

	// container guests
	Exec     string    `json:"exec,omitempty"`
	ExecSpec *ExecSpec `json:"execSpec,omitempty"`
	PID      int       `json:"pid,omitempty"`
	Signal   int       `json:"signal,omitempty"`
	Cols     int       `json:"cols,omitempty"`
	Rows     int       `json:"rows,omitempty"`
	Running  bool      `json:"running,omitempty"`
	Exited   bool      `json:"exited,omitempty"`
}

// Ident is a guest's clocks, boot id and fresh kernel randomness.
type Ident struct {
	UnixNano int64  `json:"unixNano"` // wall clock
	MonoNS   int64  `json:"monoNs"`   // CLOCK_MONOTONIC
	BootNS   int64  `json:"bootNs"`   // CLOCK_BOOTTIME
	BootID   string `json:"bootId"`   // /proc/sys/kernel/random/boot_id
	Random   []byte `json:"random"`   // 32 bytes from getrandom(2)
	UUID     string `json:"uuid"`     // /proc/sys/kernel/random/uuid
	Pooled   bool   `json:"pooled"`   // still waiting for its load
}

// WriteFrame writes one exec stream frame.
func WriteFrame(w io.Writer, kind byte, p []byte) error {
	var h [5]byte
	h[0] = kind
	binary.BigEndian.PutUint32(h[1:], uint32(len(p)))
	if _, err := w.Write(append(h[:], p...)); err != nil {
		return err
	}
	return nil
}

// ReadFrame reads one exec stream frame (payloads over MaxFrame are an
// error).
func ReadFrame(r io.Reader) (kind byte, p []byte, err error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("exec stream frame of %d bytes", n)
	}
	p = make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return h[0], p, nil
}

// ProbeResult is the outcome of one TCP connect from inside the guest.
type ProbeResult struct {
	Target string `json:"target"`
	Result string `json:"result"` // connected | refused | timeout | unreachable | error
	Detail string `json:"detail,omitempty"`
	MS     int64  `json:"ms"`
}

// Bench is fsbench's result (microseconds).
type Bench struct {
	N          int    `json:"n"`
	FS         string `json:"fs"` // the mount's source and type
	WriteP50US int64  `json:"writeP50us"`
	WriteP95US int64  `json:"writeP95us"`
	SyncP50US  int64  `json:"fsyncP50us"`
	SyncP95US  int64  `json:"fsyncP95us"`
}

// Stats is guest memory accounting.
type Stats struct {
	MemTotalKB     int64 `json:"memTotalKB"`
	MemAvailableKB int64 `json:"memAvailableKB"`
	WorkerdRSSKB   int64 `json:"workerdRssKB"`
	UptimeMS       int64 `json:"uptimeMs"`
	// DO storage: the mount's source ("/dev/vdc ext4" or "tmpfs") and usage.
	DOFS     string `json:"doFS,omitempty"`
	DOUsedKB int64  `json:"doUsedKB,omitempty"`
}

// HelloProto is the protocol a hello speaks (0, from older guests, is 1).
func (m Msg) HelloProto() int {
	if m.Proto <= 0 {
		return 1
	}
	return m.Proto
}

// UnknownOp reports whether a guest reply refused its op as unknown (an
// older guest). A caller of an op newer than GuestProtocol 1 treats that
// as the feature being absent; every op hostd sends today is protocol 1.
func UnknownOp(m Msg) bool {
	return len(m.Error) > len(unknownOpPrefix) && m.Error[:len(unknownOpPrefix)] == unknownOpPrefix
}

const unknownOpPrefix = "unknown op "
