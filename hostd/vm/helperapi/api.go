// Package helperapi is the contract between tw-hostd (running as a normal
// user) and tw-vmhelper, the small privileged service that does the parts
// of the microVM tier that need root: network namespaces and TAP devices,
// the Firecracker jailer (chroot, per-VM uid/gid, cgroups v2), and the
// dm-crypt volumes that hold Durable Object storage.
//
// Transport: a SOCK_SEQPACKET unix socket (DefaultSocket, root-owned; the
// helper authorizes each connection by its SO_PEERCRED uid). One packet is
// one JSON message. File descriptors travel as SCM_RIGHTS on the packet
// that names them, so the helper never opens a path the caller chose and
// the caller never gets a path into the jail:
//
//	start  → kernel, rootfs, config drive, console log, [volume image]
//	start  ← gv0 TAP (hostd's side of the VM's network), vsock listener
//	attach → volume image (a warm-pool VM, started with VolumeSlot)
//	memdump ← the guest memory file of a full snapshot
//
// One connection is one VM: the helper kills the VM and releases
// everything (volume, netns, jail, cgroup) when the VM exits or the
// connection closes, then sends an "exit" event if it still can.
//
// Secrets: the volume key travels only over this socket, from hostd to the
// helper, which hands it to cryptsetup on a pipe. It is never logged, put
// on a command line, written to disk, or given to the guest.
package helperapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// DefaultSocket is where `tw host setup --microvm` puts the helper.
const DefaultSocket = "/run/tailworks/vmhelper.sock"

// Version is bumped when the protocol changes incompatibly.
// 2: VolumeSlot + the attach op (warm pool); snapshot-base + Restore.
const Version = 2

// MinVersion is the oldest protocol this side still serves. A helper and
// a hostd work together when their [MinVersion, Version] ranges overlap
// (Compatible), so the root-owned helper can be upgraded first and keep
// serving the previous hostd (ADR-041 §2, §5).
const MinVersion = 2

// Compatible reports whether a peer speaking [peerMin, peerMax] can talk
// to this side ([MinVersion, Version]). A peer that reports no minimum
// (peerMin 0, a helper from before ranges) speaks only peerMax.
func Compatible(peerMin, peerMax int) bool {
	return rangesOverlap(MinVersion, Version, peerMin, peerMax)
}

func rangesOverlap(min, max, peerMin, peerMax int) bool {
	if peerMin <= 0 || peerMin > peerMax {
		peerMin = peerMax
	}
	return peerMin <= max && min <= peerMax
}

// Check returns nil when the ping response r is from a compatible helper,
// else an error naming both ranges.
func Check(r Response) error {
	if Compatible(r.MinVersion, r.Version) {
		return nil
	}
	min := r.MinVersion
	if min <= 0 {
		min = r.Version
	}
	return fmt.Errorf("tw-vmhelper speaks protocol %d-%d, this tailworks %d-%d", min, r.Version, MinVersion, Version)
}

// Error codes.
const (
	CodeUnauthorized = "unauthorized"  // peer uid not allowed
	CodeVolumeBusy   = "volume_busy"   // attached to another running VM
	CodeVolumeFenced = "volume_fenced" // epoch older than the volume's
	CodeVolumeKey    = "volume_key"    // the key does not open the volume
	CodeVolumeBad    = "volume_invalid"
	CodeCapacity     = "capacity" // no free VM slot
	CodeBadRequest   = "bad_request"
	CodeInternal     = "internal"
)

// Request is one client → helper message.
type Request struct {
	Op    string        `json:"op"` // ping | start | attach | kill | memdump | snapshot-base | cpu
	Start *StartRequest `json:"start,omitempty"`
	// CPUQuotaUS (op "cpu", FeatureContainer): lower the running VM's
	// cpu.max quota per 100 ms period. A container guest boots at its
	// whole vCPUs and is then capped to its fraction (ADR-032 D5).
	CPUQuotaUS int `json:"cpuQuotaUs,omitempty"`
	// Attach opens a volume (fd: the image) for a VM started with
	// VolumeSlot and swaps it in behind the guest's /dev/vdc (Firecracker
	// PATCH /drives, post-boot). At most once per VM.
	Attach *Volume `json:"attach,omitempty"`
	// Snapshot (op snapshot-base) turns another of this uid's VMs — one
	// started with StartRequest.Base, warm and holding no tenant code — into a base snapshot:
	// the helper pauses it, writes a full snapshot (VM state + guest
	// memory) into its own root-only directory, and kills it (the original
	// never runs again). The base lives as long as the connection that
	// asked for it; restores name it by Response.BaseID.
	Snapshot *SnapshotRequest `json:"snapshot,omitempty"`
}

// SnapshotRequest names the VM to snapshot by its jail ID (Jail.ID).
type SnapshotRequest struct {
	Of string `json:"of"`
}

// FeatureContainer: the helper boots container guests (StartRequest.Image,
// ScratchMiB, CPUQuotaUS, Pids; ADR-032). Advertised in ping's Features.
const FeatureContainer = "container"

// StartRequest boots one VM. File descriptors, in order: kernel (r),
// rootfs (r), config drive (r), console log (w, append), then the volume
// image (rw) if Volume is set, or the container image (r, erofs) if Image
// is set (never both).
type StartRequest struct {
	Name      string  `json:"name"` // caller's short VM name (logs only)
	MemMiB    int     `json:"memMiB"`
	VCPUs     int     `json:"vcpus"`
	HugePages string  `json:"hugePages,omitempty"` // "" | auto | on | off
	BootArgs  string  `json:"bootArgs"`
	MTU       int     `json:"mtu"`
	GuestMAC  string  `json:"guestMAC"`
	GuestCID  int     `json:"guestCID"`
	VsockPort int     `json:"vsockPort"` // host port the guest connects to
	Volume    *Volume `json:"volume,omitempty"`

	// Container guests (FeatureContainer). Image: a read-only erofs fd
	// follows the console fd (the guest's /dev/vdc). ScratchMiB: the helper
	// makes a fresh sparse ext4 of this size inside the jail (/dev/vdd),
	// gone with the jail. CPUQuotaUS: cpu.max quota per 100 ms period for
	// a fractional vCPU (0 = VCPUs × 100 ms). Pids: the VMM cgroup's
	// pids.max (0 = unset).
	Image      bool `json:"image,omitempty"`
	ScratchMiB int  `json:"scratchMiB,omitempty"`
	CPUQuotaUS int  `json:"cpuQuotaUs,omitempty"`
	Pids       int  `json:"pids,omitempty"`
	// VolumeSlot (warm pool; not with Volume): boot with a small blank
	// placeholder as the DO drive, so that the workload's volume can be
	// attached later (the attach op) without a reboot.
	VolumeSlot bool `json:"volumeSlot,omitempty"`
	// Base (with VolumeSlot; not with Image or Restore): this VM is booted
	// to become a base snapshot. The helper remembers it: snapshot-base is
	// refused on any VM started without Base, and attach on one started
	// with it, so a VM that has had a workload's volume (or was claimed)
	// is never snapshotted. A helper from before this field ignores it and
	// keeps its older check; an older hostd never sets it, so this helper
	// refuses its snapshot-base and that hostd boots cold instead of
	// restoring until it is upgraded too.
	Base bool `json:"base,omitempty"`
	// Restore (a base ID from snapshot-base, same uid): restore that base
	// instead of booting — a fresh jail, netns and VMM, snapshot load with
	// the guest memory served lazily by the helper (userfaultfd). The VM
	// resumes as the base was: a pooled VM waiting for its load, with a
	// volume slot. Machine and boot fields are ignored (the snapshot has
	// them); the kernel fd is still passed and ignored.
	Restore string `json:"restore,omitempty"`
}

// Volume is a persistent, encrypted block device for the guest (/dev/vdc).
type Volume struct {
	// Name identifies the volume for fencing and logs ("tenant/workload").
	Name string `json:"name"`
	// Epoch is the namespace lease epoch (ADR-011) the caller holds. The
	// helper refuses an epoch older than the newest it has attached.
	Epoch int64 `json:"epoch"`
	// Key opens (or, with Format, creates) the LUKS2 volume. 32 bytes.
	Key []byte `json:"key"`
	// Format: the image is new (all zeros); create LUKS2 + ext4 on it.
	Format bool `json:"format,omitempty"`
}

// String never prints the key.
func (v *Volume) String() string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("volume{%s epoch %d format %v}", v.Name, v.Epoch, v.Format)
}

// Response is one helper → client message: the answer to an op (Re) or an
// event (Event "exit").
type Response struct {
	Re    string `json:"re,omitempty"`
	Event string `json:"event,omitempty"`
	OK    bool   `json:"ok"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`

	// ping
	Version    int      `json:"version,omitempty"`
	MinVersion int      `json:"minVersion,omitempty"` // oldest protocol served (0: only Version)
	UID        int      `json:"uid,omitempty"`        // the caller's uid, as the helper sees it
	Jailer     string   `json:"jailer,omitempty"`
	Crypt      string   `json:"crypt,omitempty"`
	Features   []string `json:"features,omitempty"`

	// start
	PID       int                `json:"pid,omitempty"`       // firecracker (in the jail)
	StartedAt int64              `json:"startedAt,omitempty"` // unix ns of InstanceStart
	Jail      *Jail              `json:"jail,omitempty"`
	Timings   map[string]float64 `json:"timingsMs,omitempty"`
	HugePages bool               `json:"hugePages,omitempty"`
	VolumeNew bool               `json:"volumeNew,omitempty"`
	// attach: the plaintext volume's size in 512-byte sectors (the guest
	// waits for its /dev/vdc to show it).
	VolumeSectors int64 `json:"volumeSectors,omitempty"`

	// snapshot-base
	BaseID    string   `json:"baseId,omitempty"`
	BaseBytes int64    `json:"baseBytes,omitempty"` // memory file
	BaseFiles []string `json:"baseFiles,omitempty"` // root-only paths (state, memory, config drive)
}

// Jail describes how the VMM is confined.
type Jail struct {
	ID      string `json:"id"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
	Chroot  string `json:"chroot"`
	Cgroup  string `json:"cgroup"`
	CPUMax  string `json:"cpuMax"`
	MemMax  int64  `json:"memMax"`
	Seccomp string `json:"seccomp"`
	Netns   string `json:"netns"`
}

// Err is a helper error with a code.
type Err struct {
	Code, Msg string
}

func (e *Err) Error() string { return e.Code + ": " + e.Msg }

// IsCode reports whether err is a helper error with code.
func IsCode(err error, code string) bool {
	var e *Err
	return errors.As(err, &e) && e.Code == code
}

const maxPacket = 1 << 20

// Conn is a client connection (one VM).
type Conn struct {
	c      *net.UnixConn
	opMu   sync.Mutex
	resp   chan packet
	events chan Response
	closed chan struct{}
	once   sync.Once
	err    error
}

type packet struct {
	r   Response
	fds []int
}

// Dial connects to the helper.
func Dial(sock string) (*Conn, error) {
	c, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: sock, Net: "unixpacket"})
	if err != nil {
		return nil, err
	}
	cn := &Conn{c: c, resp: make(chan packet, 1), events: make(chan Response, 4), closed: make(chan struct{})}
	go cn.read()
	return cn, nil
}

func (cn *Conn) read() {
	defer cn.once.Do(func() { close(cn.closed) })
	buf := make([]byte, maxPacket)
	oob := make([]byte, syscall.CmsgSpace(16*4))
	for {
		n, oobn, _, _, err := cn.c.ReadMsgUnix(buf, oob)
		if err != nil || n == 0 {
			cn.err = err
			return
		}
		fds := ParseRights(oob[:oobn])
		var r Response
		if json.Unmarshal(buf[:n], &r) != nil {
			closeFDs(fds)
			continue
		}
		if r.Event != "" {
			closeFDs(fds)
			select {
			case cn.events <- r:
			default:
			}
			continue
		}
		cn.resp <- packet{r, fds}
	}
}

// Events delivers helper events ("exit").
func (cn *Conn) Events() <-chan Response { return cn.events }

// Closed is closed when the connection is gone (helper exit, or Close).
func (cn *Conn) Closed() <-chan struct{} { return cn.closed }

// Do sends a request (with fds) and waits for its answer.
func (cn *Conn) Do(req Request, files []*os.File, timeout time.Duration) (Response, []int, error) {
	cn.opMu.Lock()
	defer cn.opMu.Unlock()
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, nil, err
	}
	var oob []byte
	if len(files) > 0 {
		fds := make([]int, len(files))
		for i, f := range files {
			fds[i] = int(f.Fd())
		}
		oob = syscall.UnixRights(fds...)
	}
	if _, _, err := cn.c.WriteMsgUnix(b, oob, nil); err != nil {
		return Response{}, nil, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case p := <-cn.resp:
		if !p.r.OK {
			closeFDs(p.fds)
			return p.r, nil, &Err{Code: p.r.Code, Msg: p.r.Error}
		}
		return p.r, p.fds, nil
	case <-cn.closed:
		return Response{}, nil, fmt.Errorf("helper connection closed: %v", cn.err)
	case <-t.C:
		return Response{}, nil, fmt.Errorf("helper %s: timeout after %v", req.Op, timeout)
	}
}

// Close closes the connection (the helper then stops the VM).
func (cn *Conn) Close() error { return cn.c.Close() }

// Ping checks the helper is up and this uid may use it.
func Ping(sock string) (Response, error) {
	c, err := Dial(sock)
	if err != nil {
		return Response{}, err
	}
	defer c.Close()
	r, _, err := c.Do(Request{Op: "ping"}, nil, 5*time.Second)
	return r, err
}

// ParseRights extracts SCM_RIGHTS file descriptors from oob.
func ParseRights(oob []byte) []int {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var fds []int
	for _, m := range msgs {
		if f, err := syscall.ParseUnixRights(&m); err == nil {
			for _, fd := range f {
				// Never leak a passed fd (a volume's lock, a vsock
				// listener) into a child process (another VM's jailer).
				syscall.CloseOnExec(fd)
			}
			fds = append(fds, f...)
		}
	}
	return fds
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		syscall.Close(fd)
	}
}

// WriteResponse sends r (with fds) on c (helper side).
func WriteResponse(c *net.UnixConn, r Response, fds ...int) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	_, _, err = c.WriteMsgUnix(b, oob, nil)
	return err
}

// ReadRequest reads one request and its fds (helper side).
func ReadRequest(c *net.UnixConn) (Request, []int, error) {
	buf := make([]byte, maxPacket)
	oob := make([]byte, syscall.CmsgSpace(16*4))
	n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		return Request{}, nil, err
	}
	if n == 0 {
		return Request{}, nil, errors.New("EOF")
	}
	fds := ParseRights(oob[:oobn])
	var r Request
	if err := json.Unmarshal(buf[:n], &r); err != nil {
		closeFDs(fds)
		return Request{}, nil, fmt.Errorf("bad request: %w", err)
	}
	return r, fds, nil
}
