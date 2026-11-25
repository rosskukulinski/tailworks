//go:build linux

package helper

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Lazy guest memory for a restored VM (Firecracker `mem_backend: Uffd`,
// docs/snapshotting/handling-page-faults-on-snapshot-resume.md in v1.17).
// Firecracker connects to a unix socket in the jail and sends its
// userfaultfd plus a JSON array of guest memory regions; every first touch
// of a guest page then arrives here as a fault, and is served by copying
// that page from the base snapshot's memory file into the VM
// (UFFDIO_COPY). The memory file stays with the helper (root, 0600): the
// VMM never maps it, and each restored VM gets private copies of the pages
// it touches — no physical page is shared between VMs restored from the
// same base (see the ADR draft: cross-tenant memory sharing).
//
// With 2 MiB hugetlbfs guest memory (Firecracker requires Uffd for
// that), a fault is served a whole 2 MiB page.
//
// Prefetch: right after the handshake, a second goroutine copies every page
// that is non-zero in the base (its resident set: kernel, guestd, page
// cache; computed once per base, from the base alone — never from what a
// tenant touched), while faults keep being served on demand; whichever
// comes first wins (the other gets EEXIST).

// The region table (uffdRegion) and its validation are in uffdregions.go.

const (
	uffdEventPagefault = 0x12
	uffdEventRemove    = 0x15
	uffdMsgSize        = 32
	// _IOWR(0xAA, 0x03, struct uffdio_copy) — 40 bytes
	uffdioCopy = 0xC028AA03
)

type uffdioCopyArg struct {
	Dst, Src, Len, Mode uint64
	Copy                int64
}

// uffdServer serves one restored VM's page faults.
type uffdServer struct {
	ln      *net.UnixListener
	mem     []byte // the base memory file, mapped read-only
	done    chan struct{}
	stop    chan struct{}
	nonzero []uint64 // bitmap of the base's non-zero 4 KiB pages (prefetch)
	// stats
	faults, copied, eexist, prefetched atomic.Int64
	prefetchNS                         atomic.Int64
	firstFault                         atomic.Int64 // unix ns
	err                                atomic.Value
}

// listenUffd creates the socket Firecracker will connect to (owned by the
// jail uid) and maps the base memory file.
func listenUffd(path string, uid int, memFile string, nonzero []uint64) (*uffdServer, error) {
	f, err := os.Open(memFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, int(st.Size()), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("mmap base memory: %w", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		unix.Munmap(mem)
		return nil, err
	}
	os.Chown(path, uid, uid)
	os.Chmod(path, 0o600)
	u := &uffdServer{ln: ln, mem: mem, done: make(chan struct{}), stop: make(chan struct{}), nonzero: nonzero}
	go u.serve()
	return u, nil
}

func (u *uffdServer) fail(err error) {
	u.err.CompareAndSwap(nil, err)
}

// recoverSession turns a panic in one VM's fault loop or prefetch into
// that VM's error, so the root helper keeps serving every other VM
// (security/LOOP.md I8). It must be deferred directly.
func (u *uffdServer) recoverSession() {
	if r := recover(); r != nil {
		u.fail(fmt.Errorf("uffd: panic: %v", r))
	}
}

func (u *uffdServer) serve() {
	defer close(u.done)
	defer u.recoverSession()
	u.ln.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := u.ln.AcceptUnix()
	u.ln.Close()
	if err != nil {
		u.fail(fmt.Errorf("uffd accept: %w", err))
		return
	}
	defer c.Close()
	buf := make([]byte, 64<<10)
	oob := make([]byte, unix.CmsgSpace(4))
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		u.fail(fmt.Errorf("uffd handshake: %w", err))
		return
	}
	var uffd = -1
	if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil {
		for _, m := range msgs {
			if fds, err := syscall.ParseUnixRights(&m); err == nil && len(fds) > 0 {
				uffd = fds[0]
				for _, extra := range fds[1:] {
					unix.Close(extra)
				}
			}
		}
	}
	if uffd < 0 {
		u.fail(errors.New("uffd handshake: no file descriptor"))
		return
	}
	defer unix.Close(uffd)
	regions, err := parseRegions(buf[:n], uint64(len(u.mem)))
	if err != nil {
		u.fail(err)
		return
	}
	if len(u.nonzero) > 0 {
		go u.prefetch(uffd, regions)
	}
	msgs := make([]byte, uffdMsgSize*64)
	pfd := []unix.PollFd{{Fd: int32(uffd), Events: unix.POLLIN}}
	for {
		select {
		case <-u.stop:
			return
		default:
		}
		k, err := unix.Poll(pfd, 200)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			u.fail(err)
			return
		}
		if k == 0 {
			continue
		}
		if pfd[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			return // the VMM exited
		}
		n, err := unix.Read(uffd, msgs)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		if n == 0 {
			return
		}
		for off := 0; off+uffdMsgSize <= n; off += uffdMsgSize {
			m := msgs[off : off+uffdMsgSize]
			switch m[0] {
			case uffdEventPagefault:
				addr := binary.NativeEndian.Uint64(m[16:24])
				if err := u.copyPage(uffd, regions, addr); err != nil {
					u.fail(err)
					return
				}
			case uffdEventRemove:
				// Only a balloon removes pages; tailworks configures none.
			}
		}
	}
}

func (u *uffdServer) copyPage(uffd int, regions []uffdRegion, addr uint64) error {
	u.faults.Add(1)
	u.firstFault.CompareAndSwap(0, time.Now().UnixNano())
	page, src, size, ok := pageSource(regions, addr)
	if !ok {
		return fmt.Errorf("uffd: fault at %#x outside guest memory", addr)
	}
	if src > uint64(len(u.mem)) || size > uint64(len(u.mem))-src { // validateRegions rules this out
		return fmt.Errorf("uffd: fault at %#x maps past the memory file", addr)
	}
	for {
		arg := uffdioCopyArg{Dst: page, Src: uint64(uintptr(unsafe.Pointer(&u.mem[src]))), Len: size}
		_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(uffd), uffdioCopy, uintptr(unsafe.Pointer(&arg)))
		switch {
		case e == 0:
			u.copied.Add(1)
			return nil
		case e == unix.EEXIST: // another vCPU faulted the same page first
			u.eexist.Add(1)
			return nil
		case e == unix.EAGAIN:
			continue
		default:
			return fmt.Errorf("UFFDIO_COPY %#x: %w", page, e)
		}
	}
}

// prefetch copies the base's non-zero pages into the VM, in address order.
// regions passed validateRegions, so r.Offset+page is inside the file.
func (u *uffdServer) prefetch(uffd int, regions []uffdRegion) {
	defer u.recoverSession()
	st := time.Now()
	for _, r := range regions {
		for page := uint64(0); page < r.Size; page += r.PageSize {
			select {
			case <-u.stop:
				return
			default:
			}
			if !anyBit(u.nonzero, (r.Offset+page)/4096, r.PageSize/4096) {
				continue
			}
			arg := uffdioCopyArg{Dst: r.Base + page, Src: uint64(uintptr(unsafe.Pointer(&u.mem[r.Offset+page]))), Len: r.PageSize}
			_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(uffd), uffdioCopy, uintptr(unsafe.Pointer(&arg)))
			switch e {
			case 0:
				u.prefetched.Add(1)
			case unix.EEXIST:
			default:
				return // the VMM is gone (ESRCH/ENOENT) or the range changed
			}
		}
	}
	u.prefetchNS.Store(int64(time.Since(st)))
}

func anyBit(bm []uint64, from, n uint64) bool {
	for i := from; i < from+n; i++ {
		if w := i / 64; w < uint64(len(bm)) && bm[w]&(1<<(i%64)) != 0 {
			return true
		}
	}
	return false
}

// nonzeroPages maps which 4 KiB pages of the memory file are not all zero.
func nonzeroPages(memFile string) ([]uint64, error) {
	f, err := os.Open(memFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	n := uint64(st.Size()) / 4096
	bm := make([]uint64, (n+63)/64)
	buf := make([]byte, 2<<20)
	zero := make([]byte, 4096)
	var page uint64
	for {
		k, err := io.ReadFull(f, buf)
		for off := 0; off+4096 <= k; off += 4096 {
			if !bytes.Equal(buf[off:off+4096], zero) {
				bm[page/64] |= 1 << (page % 64)
			}
			page++
		}
		if err != nil {
			break
		}
	}
	return bm, nil
}

func (u *uffdServer) stats() map[string]float64 {
	return map[string]float64{"uffd_faults": float64(u.faults.Load()), "uffd_pages_copied": float64(u.copied.Load()), "uffd_eexist": float64(u.eexist.Load()),
		"uffd_prefetched": float64(u.prefetched.Load()), "uffd_prefetch_ms": float64(u.prefetchNS.Load()) / 1e6}
}

// close stops accepting (if Firecracker never connected) and unmaps the
// memory once the fault loop has ended (the VMM is gone by then).
func (u *uffdServer) close() {
	close(u.stop)
	u.ln.Close()
	select {
	case <-u.done:
	case <-time.After(5 * time.Second):
		return // still serving a live VMM: leak the mapping rather than crash it
	}
	unix.Munmap(u.mem)
}
