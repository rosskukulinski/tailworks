//go:build linux

package vm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
)

// Snapshot restore (vm.Config.Snapshot; off by default — see the ADR
// draft on cross-tenant memory sharing before turning it on).
//
// The base snapshot is a pooled VM: kernel booted, guestd up, the no-tenant
// warm-up workerd run and exited, AppDir empty, waiting for its load. It
// holds no tenant code, no keys and no instance identity (every VM has the
// same guest IP, MAC and vsock CID, each in its own netns and jail; the
// per-instance identity — bundle, MAC key, entropy, boot id, clock — comes
// with the load). The helper snapshots it into a root-only directory and
// kills it; the base lives as long as hostd's holder connection.
//
// A restore is a fresh jail + netns + VMM that loads the base with its
// guest memory served lazily by the helper (userfaultfd, UFFDIO_COPY into
// private pages). The restored guest's vsock connection was reset by the
// snapshot; guestd reconnects and says "pooled", and from there it is
// claimed exactly like a pooled VM (Backend.claim).
//
// Rotation (vm.Config.BaseRotate, default 1 h; ADR-030 decision 3): the
// base is rebuilt on a timer (and at every hostd start). Once the new one is
// ready, restores use it, idle pool VMs restored from the old one are
// discarded (refilled from the new one), and the old base's files are
// removed after BaseGrace (restores already reading it finish first;
// running VMs keep their own mapping of its memory).
type baseSnap struct {
	ready chan struct{}
	conn  *helperapi.Conn
	id    string
	files []string
	info  map[string]any
	err   error
	built time.Time
	// guest is the GuestIdentity the base was built from. A base is only
	// restored while the configured guest still has that identity
	// (ADR-041 §5): never under a different guest.
	guest string
	// ctlToken is the base VM's control-channel token: every copy restored
	// from this base holds it in guestd's memory, so it authenticates a
	// copy's pre-claim connection only (no tenant code has run in it yet);
	// each copy's claim replaces it with the copy's own (Load.CtlToken).
	// It is never sent to a guest after the base boot.
	ctlToken string
}

// BaseGrace is how long an old base outlives its replacement.
var BaseGrace = 30 * time.Second

func (b *Backend) curBase() *baseSnap {
	b.baseMu.Lock()
	defer b.baseMu.Unlock()
	return b.base
}

// startBase builds the first base snapshot in the background (b.base is set
// before the pool starts, so both see it), then rotates it.
func (b *Backend) startBase() {
	ctx, cancel := context.WithCancel(context.Background())
	b.baseStop = cancel
	b.baseWG.Add(1)
	go func() {
		defer b.baseWG.Done()
		bs := b.curBase()
		bs.err = b.buildBase(ctx, bs)
		close(bs.ready)
		every := b.cfg.BaseRotate
		if every <= 0 {
			return
		}
		next := every
		if bs.err != nil {
			next = min(every, time.Minute) // retry a failed first base soon
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(next):
			}
			nb := &baseSnap{ready: make(chan struct{})}
			nb.err = b.buildBase(ctx, nb)
			close(nb.ready)
			if nb.err != nil {
				if ctx.Err() == nil {
					b.cfg.Log.Warn("microVM base rotation failed; keeping the current base", "err", nb.err)
				}
				next = min(every, time.Minute)
				continue
			}
			next = every
			b.baseMu.Lock()
			old := b.base
			b.base = nb
			b.baseRotations++
			b.baseStale = false
			b.baseMu.Unlock()
			b.cfg.Log.Info("microVM base snapshot rotated", "old", old.id, "new", nb.id)
			if b.pool != nil {
				b.pool.dropBase(old.id)
			}
			b.retire(old)
		}
	}()
}

// retire removes an old base after BaseGrace.
func (b *Backend) retire(old *baseSnap) {
	if old == nil || old.conn == nil {
		return
	}
	b.baseWG.Add(1)
	go func() {
		defer b.baseWG.Done()
		select {
		case <-time.After(BaseGrace):
		case <-b.baseDone:
		}
		old.conn.Close() // the helper removes the base's files
		b.baseMu.Lock()
		delete(b.baseTokens, old.id)
		b.baseMu.Unlock()
	}()
}

// baseToken is base id's control-channel token ("" = no such base).
func (b *Backend) baseToken(id string) string {
	b.baseMu.Lock()
	defer b.baseMu.Unlock()
	return b.baseTokens[id]
}

func (b *Backend) buildBase(ctx context.Context, bs *baseSnap) error {
	if p := b.pool; p != nil && !p.roomForOne() {
		return errors.New("no room in the huge-page budget for the base VM now")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	gid, err := GuestIdentity(b.cfg.Kernel, b.cfg.Rootfs)
	if err != nil {
		return fmt.Errorf("guest identity: %w", err)
	}
	bs.guest = gid
	err = b.makeBase(ctx, bs)
	if err != nil && ctx.Err() == nil {
		b.cfg.Log.Warn("microVM base snapshot failed; booting instead of restoring", "err", err)
	}
	bs.built = time.Now()
	return err
}

func (b *Backend) makeBase(ctx context.Context, bs *baseSnap) error {
	st := time.Now()
	m, err := b.boot(ctx, bootOpts{name: "base", pool: true, base: true})
	if err != nil {
		return err
	}
	defer m.Stop()
	booted := time.Since(st)
	// Belt and braces: nothing of a workload (or the warm-up) in the guest.
	r, err := m.Op(ctx, proto.Msg{Op: "fsscan"})
	if err != nil {
		return err
	}
	if len(r.Found) > 0 || len(r.State) > 0 {
		return fmt.Errorf("base VM is not clean: found %v, state %v", r.Found, r.State)
	}
	if m.info.Jail == nil {
		return errors.New("base VM has no jail id")
	}
	conn, err := helperapi.Dial(b.sock)
	if err != nil {
		return err
	}
	resp, _, err := conn.Do(helperapi.Request{Op: "snapshot-base", Snapshot: &helperapi.SnapshotRequest{Of: m.info.Jail.ID}}, nil, 2*time.Minute)
	if err != nil {
		conn.Close()
		return fmt.Errorf("snapshot-base: %w", err)
	}
	bs.conn, bs.id, bs.files, bs.ctlToken = conn, resp.BaseID, resp.BaseFiles, m.token()
	b.baseMu.Lock()
	if b.baseTokens == nil {
		b.baseTokens = map[string]string{}
	}
	b.baseTokens[bs.id] = bs.ctlToken
	b.baseMu.Unlock()
	bs.info = map[string]any{"base": resp.BaseID, "mem_bytes": resp.BaseBytes, "boot_ms": booted.Milliseconds(),
		"snapshot_ms": resp.Timings, "total_ms": time.Since(st).Milliseconds(), "hugepages": m.info.HugePages}
	b.cfg.Log.Info("microVM base snapshot ready", "info", bs.info)
	return nil
}

// baseID returns the current base snapshot's ID once it is built ("" if
// snapshots are off or the base failed). wait: block until it is decided.
func (b *Backend) baseID(ctx context.Context, wait bool) string {
	bs := b.curBase()
	if bs == nil {
		return ""
	}
	if wait {
		select {
		case <-bs.ready:
		case <-ctx.Done():
			return ""
		}
	} else {
		select {
		case <-bs.ready:
		default:
			return ""
		}
	}
	if bs.err != nil {
		return ""
	}
	if gid, err := GuestIdentity(b.cfg.Kernel, b.cfg.Rootfs); err != nil || gid != bs.guest {
		// The guest changed under the base (an install swapped
		// share/tailworks/vm): boot cold until a base of the new guest is built.
		b.baseMu.Lock()
		warn := !b.baseStale
		b.baseStale = true
		b.baseMu.Unlock()
		if warn {
			b.cfg.Log.Warn("microVM guest changed since the base snapshot; booting cold until the next base", "base", bs.id, "err", err)
		}
		return ""
	}
	return bs.id
}

// BaseSnapshot reports the current base snapshot (error if snapshots are
// off or it failed): its root-only files (for the key scan; readable when
// hostd is root) and how it was made.
func (b *Backend) BaseSnapshot(ctx context.Context) ([]string, map[string]any, error) {
	bs := b.curBase()
	if bs == nil {
		return nil, nil, errors.New("snapshots are off")
	}
	if b.baseID(ctx, true) == "" {
		return nil, nil, fmt.Errorf("no base snapshot: %v", bs.err)
	}
	info := map[string]any{}
	for k, v := range bs.info {
		info[k] = v
	}
	b.baseMu.Lock()
	info["rotations"] = b.baseRotations
	b.baseMu.Unlock()
	return bs.files, info, nil
}

// stopBase stops the rotator (before the pool closes).
func (b *Backend) stopBase() {
	if b.baseStop != nil {
		b.baseStop()
	}
}

// closeBase removes the bases (after the pool closed).
func (b *Backend) closeBase() {
	bs := b.curBase()
	if bs == nil {
		return
	}
	close(b.baseDone)
	b.baseWG.Wait()
	if bs.conn != nil {
		bs.conn.Close() // the helper removes the base's files
	}
}
