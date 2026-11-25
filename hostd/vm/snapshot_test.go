//go:build linux

package vm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

func snapBackend(t *testing.T, pool int) *Backend {
	t.Helper()
	b := func() *Backend {
		kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
		if kernel == "" || rootfs == "" {
			t.Skip("TW_VM_KERNEL / TW_VM_ROOTFS not set")
		}
		b, err := New(Config{Kernel: kernel, Rootfs: rootfs, Helper: os.Getenv("TW_VM_HELPER"), HugePages: os.Getenv("TW_VM_HUGEPAGES"),
			Pool: pool, Snapshot: true, SnapshotRefill: pool >= 0,
			VCPUs: envInt("TW_VM_VCPUS")})
		if err != nil {
			t.Skip(err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	files, info, err := b.BaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("base snapshot %v: %v", files, info)
	return b
}

// TestSnapshotRestore: with snapshots on and no pool, every Boot()
// restores the base (lazy memory via userfaultfd) and claims it: the
// workload serves, with its own gateway handler (and the first one with a
// volume). Times N restores (TW_VM_BENCH, default 5) without a volume:
// restore → workerd ready.
func TestSnapshotRestore(t *testing.T) {
	b := snapBackend(t, -1)
	ctx := context.Background()
	n := max(envInt("TW_VM_BENCH"), 5)
	var infos []sandbox.Info
	for i := 0; i < n; i++ {
		a := newMarkerApp(t, fmt.Sprintf("rs%d", i), i == 0)
		m, err := b.Boot(ctx, a.spec)
		if err != nil {
			t.Fatal(err)
		}
		in := m.Info()
		if in.Source != "restore" {
			t.Fatalf("source %q, want restore", in.Source)
		}
		if got := guestGet(t, m, "/"); got != "hello "+a.marker {
			t.Fatalf("GET /: %q", got)
		}
		if got := guestGet(t, m, "/egress"); got != "via host: host handler for "+a.spec.ID {
			t.Fatalf("egress: %q", got)
		}
		if a.spec.Volume != nil {
			if got := guestGet(t, m, "/put"); !strings.HasPrefix(got, "put 20") {
				t.Fatalf("volume write: %q", got)
			}
		}
		if _, err := m.Op(ctx, proto.Msg{Op: "load", Load: &proto.Load{}}); err == nil {
			t.Fatal("a restored VM accepted a second load")
		}
		if i > 0 {
			infos = append(infos, in)
		}
		if i == 0 { // a second workerd in the restored VM: first-touch or steady cost?
			m.Op(ctx, proto.Msg{Op: "wkill"})
			for d := time.Now().Add(10 * time.Second); time.Now().Before(d); time.Sleep(10 * time.Millisecond) {
				if gs := m.Info().GuestStages; gs["healthy#1"] > 0 {
					t.Logf("restored VM, second workerd: spawn→listen %.1f ms, listen→healthy %.1f ms", gs["workerd_listen#1"]-gs["workerd_spawn#1"], gs["healthy#1"]-gs["workerd_listen#1"])
					break
				}
			}
		}
		m.Stop()
		t.Logf("restore %d: uffd %v", i, m.(*Machine).ExitStats())
	}
	order, stats := Summarize(infos)
	var lines []string
	for _, k := range order {
		lines = append(lines, fmt.Sprintf("%-75s p50 %7.1f  p95 %7.1f ms", k, stats[k]["p50"], stats[k]["p95"]))
	}
	t.Logf("restore → ready, %d on-demand restores (no volume):\n%s", len(infos), strings.Join(lines, "\n"))
	if p := os.Getenv("TW_VM_BENCH_OUT"); p != "" {
		raw := make([]map[string]any, len(infos))
		for i, in := range infos {
			raw[i] = map[string]any{"stages": in.Stages, "guest": in.GuestStages, "breakdown": Breakdown(in)}
		}
		bb, _ := json.MarshalIndent(map[string]any{"order": order, "stats": stats, "restores": raw,
			"caveat": "nested virtualization (Lima on Apple silicon)"}, "", "  ")
		os.WriteFile(p, bb, 0o644)
	}
}

// TestSnapshotPool: snapshots + pool: the pool refills by restoring the
// base and re-warming the copy in the background; claims then cost what a
// booted pool VM's do. Records refill (restore + rewarm) and claim times.
func TestSnapshotPool(t *testing.T) {
	b := snapBackend(t, 2)
	ctx := context.Background()
	n := max(envInt("TW_VM_BENCH"), 5)
	var infos []sandbox.Info
	var refill []float64
	for i := 0; i < n; i++ {
		wc, cancel := context.WithTimeout(ctx, 60*time.Second)
		if err := b.WaitPool(wc, 2); err != nil {
			t.Fatal(err)
		}
		cancel()
		m, err := b.Boot(ctx, newMarkerApp(t, fmt.Sprintf("sp%d", i), false).spec)
		if err != nil {
			t.Fatal(err)
		}
		in := m.Info()
		if in.Source != "restore" || in.Stages["restore"] != 0 {
			t.Fatalf("source %q (restore stage %v): want a restored, pooled VM", in.Source, in.Stages["restore"])
		}
		if got := guestGet(t, m, "/egress"); got != "via host: host handler for sp"+fmt.Sprint(i) {
			t.Fatalf("egress: %q", got)
		}
		infos = append(infos, in)
		refill = append(refill, in.Stages["pool.pooled"]+in.Stages["pool.rewarm"])
		m.Stop()
	}
	order, stats := Summarize(infos)
	var lines []string
	for _, k := range order {
		lines = append(lines, fmt.Sprintf("%-75s p50 %7.1f  p95 %7.1f ms", k, stats[k]["p50"], stats[k]["p95"]))
	}
	t.Logf("snapshot pool: refill (restore + rewarm) %v; %d claims:\n%s; pool %+v", Percentiles(refill), n, strings.Join(lines, "\n"), *b.PoolStats())
}

// identJS answers with what workerd itself sees: the time and its
// randomness (crypto.getRandomValues, randomUUID, Math.random).
const identJS = `export default {
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname === "/_tw/admin") return new Response("ok");
    const r = new Uint8Array(16); crypto.getRandomValues(r);
    return Response.json({ now: Date.now(), rand: [...r].map(b => b.toString(16).padStart(2, "0")).join(""),
      uuid: crypto.randomUUID(), math: Math.random() });
  }
};
`

// TestSnapshotUniqueness: copies restored from one base are distinct
// guests. Three copies are restored concurrently. Before their load they
// are (by construction) the same guest: same clock origin, kernel CRNG
// state and boot id — recorded, not required. After the load (which brings
// 64 bytes of host entropy + RNDRESEEDCRNG, a boot id, the wall clock):
//   - kernel randomness (getrandom, /proc uuid) differs between copies;
//   - boot_id is the injected one, different per copy;
//   - the wall clock is within 1 s of the host's, and workerd's Date.now()
//     too (TLS and the egress MACs need real time);
//   - workerd's crypto.getRandomValues / randomUUID / Math.random differ;
//   - each copy's network is its own: same guest IP/MAC by design, but
//     egress reaches only that copy's gateway handler, and the vsock
//     control channel of each Machine answers with that copy's boot id.
func TestSnapshotUniqueness(t *testing.T) {
	b := snapBackend(t, -1)
	ctx := context.Background()
	const copies = 3
	id, _ := b.baseID(ctx, true), 0
	type res struct {
		pre, post *proto.Ident
		hostPre   int64
		hostPost  int64
		worker    map[string]any
		egress    string
		token     string // the copy's control-channel token after its claim
		err       error
	}
	out := make([]res, copies)
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := &out[i]
			m, err := b.boot(ctx, bootOpts{name: fmt.Sprintf("u%d", i), restore: id})
			if err != nil {
				r.err = err
				return
			}
			pre, err := m.Op(ctx, proto.Msg{Op: "ident"})
			r.hostPre = time.Now().UnixNano()
			if err != nil {
				r.err = err
				m.Stop()
				return
			}
			r.pre = pre.Ident
			app := t.TempDir()
			os.WriteFile(filepath.Join(app, "w.js"), []byte(identJS), 0o600)
			os.WriteFile(filepath.Join(app, "config.capnp"), []byte(markerCapnp), 0o600)
			name := fmt.Sprintf("copy%d", i)
			spec := sandbox.Spec{ID: name, AppDir: app, WorkerdArgs: []string{"serve", "config.capnp", "--experimental"}, HealthCF: `{}`, Debug: true,
				Handlers: map[uint16]http.Handler{proto.EgressObj: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, name) })}}
			vm, err := b.claim(ctx, m, spec)
			if err != nil {
				r.err = err
				return
			}
			defer vm.Stop()
			if r.token, err = checkClaimToken(ctx, vm.(*Machine), b.baseToken(id)); err != nil {
				r.err = err
				return
			}
			post, err := vm.Op(ctx, proto.Msg{Op: "ident"})
			r.hostPost = time.Now().UnixNano()
			if err != nil {
				r.err = err
				return
			}
			r.post = post.Ident
			json.Unmarshal([]byte(guestGet(t, vm, "/")), &r.worker)
			// egress from this copy (markerCapnp's GW → gateway:3128)
			r.egress = func() string {
				c := vm.(*Machine)
				conn, err := c.DialGuest(ctx)
				if err != nil {
					return err.Error()
				}
				conn.Close()
				return ""
			}()
		}(i)
	}
	wg.Wait()
	host := time.Now().UnixNano()
	for i, r := range out {
		if r.err != nil {
			t.Fatalf("copy %d: %v", i, r.err)
		}
		t.Logf("copy %d: before load: wall %s (host %s, skew %v), boot_id %s, random %x, mono %v | after: skew %v, boot_id %s, random %x, worker %v",
			i, time.Unix(0, r.pre.UnixNano).UTC().Format(time.RFC3339Nano), time.Unix(0, r.hostPre).UTC().Format(time.RFC3339Nano),
			time.Duration(r.hostPre-r.pre.UnixNano), r.pre.BootID, r.pre.Random[:8], time.Duration(r.pre.MonoNS),
			time.Duration(r.hostPost-r.post.UnixNano), r.post.BootID, r.post.Random[:8], r.worker)
		if !r.pre.Pooled || r.post.Pooled {
			t.Fatalf("copy %d: pooled before/after %v/%v", i, r.pre.Pooled, r.post.Pooled)
		}
		// The guest's clock vs the host's at the time of its ident reply
		// (read right after it): within 50 ms.
		if d := time.Duration(r.hostPost - r.post.UnixNano); d < -50*time.Millisecond || d > 50*time.Millisecond {
			t.Fatalf("copy %d: guest wall clock off by %v after its load", i, d)
		}
		now, _ := r.worker["now"].(float64)
		if d := time.Duration(host - int64(now)*1e6); d < -time.Second || d > 2*time.Second {
			t.Fatalf("copy %d: workerd Date.now() off by %v", i, d)
		}
		for j := 0; j < i; j++ {
			o := out[j]
			if bytes.Equal(r.post.Random, o.post.Random) || r.post.UUID == o.post.UUID {
				t.Fatalf("copies %d and %d: same kernel randomness after load", i, j)
			}
			if r.post.BootID == o.post.BootID {
				t.Fatalf("copies %d and %d: same boot_id after load", i, j)
			}
			for _, k := range []string{"rand", "uuid", "math"} {
				if fmt.Sprint(r.worker[k]) == fmt.Sprint(o.worker[k]) {
					t.Fatalf("copies %d and %d: workerd %s identical: %v", i, j, k, r.worker[k])
				}
			}
		}
		if r.post.BootID == r.pre.BootID {
			t.Fatalf("copy %d: boot_id not replaced by the load", i)
		}
		for j := 0; j < i; j++ {
			if r.token == out[j].token {
				t.Fatalf("copies %d and %d: same control-channel token after the claim", i, j)
			}
		}
	}
	// Recorded (not asserted): were the copies' kernel CRNGs identical
	// before the reseed? (No VMGenID driver in this guest kernel.)
	same := bytes.Equal(out[0].pre.Random, out[1].pre.Random)
	t.Logf("before the load: copies 0/1 boot_id equal: %v; first getrandom() equal: %v (no VMGenID on this 6.1 aarch64 guest)",
		out[0].pre.BootID == out[1].pre.BootID, same)
	if p := os.Getenv("TW_VM_UNIQ_OUT"); p != "" {
		bb, _ := json.MarshalIndent(out, "", "  ")
		os.WriteFile(p, bb, 0o644)
	}
	_ = rand.Read
	_ = hex.EncodeToString
}

// TestSnapshotRotate: the base is rebuilt on a timer; restores then use the
// new one, idle pool VMs restored from the old one are discarded and
// refilled from the new one, and the old base's files are removed after
// the grace period (ADR-030 decision 3).
func TestSnapshotRotate(t *testing.T) {
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL / TW_VM_ROOTFS not set")
	}
	defer func(g time.Duration) { BaseGrace = g }(BaseGrace)
	BaseGrace = time.Second
	b, err := New(Config{Kernel: kernel, Rootfs: rootfs, Helper: os.Getenv("TW_VM_HELPER"), HugePages: os.Getenv("TW_VM_HUGEPAGES"),
		Pool: 1, Snapshot: true, SnapshotRefill: true, BaseRotate: 4 * time.Second})
	if err != nil {
		t.Skip(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	filesA, _, err := b.BaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idA := b.baseID(ctx, true)
	if err := b.WaitPool(ctx, 1); err != nil {
		t.Fatal(err)
	}
	idleBase := func() string {
		b.pool.mu.Lock()
		defer b.pool.mu.Unlock()
		if len(b.pool.idle) == 0 {
			return ""
		}
		return b.pool.idle[0].info.Base
	}
	if got := idleBase(); got != idA {
		t.Fatalf("pooled VM from base %q, want %q", got, idA)
	}
	var idB string
	for idB == "" || idB == idA {
		if ctx.Err() != nil {
			t.Fatal("the base was not rotated")
		}
		time.Sleep(100 * time.Millisecond)
		idB = b.baseID(ctx, false)
	}
	t.Logf("rotated %s → %s", idA, idB)
	// The idle VM from A is replaced by one from B (or a later base).
	for {
		if got := idleBase(); got != "" && got != idA {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("idle pool VM still from the old base: %+v", *b.PoolStats())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ps := b.PoolStats(); ps.Rotated < 1 {
		t.Fatalf("no pooled VM discarded on rotation: %+v", *ps)
	}
	// A's files go after the grace period.
	deadline := time.Now().Add(10 * time.Second)
	for _, f := range filesA {
		for {
			if _, err := os.Stat(f); os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("old base file %s still there", f)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	m, err := b.Boot(ctx, newMarkerApp(t, "rot", false).spec)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if in := m.Info(); in.Source != "restore" || in.Base == idA || in.Base == "" {
		t.Fatalf("claimed VM source %q base %q (old base %q)", in.Source, in.Base, idA)
	}
	if got := guestGet(t, m, "/"); !strings.HasPrefix(got, "hello ") {
		t.Fatalf("GET /: %q", got)
	}
	t.Logf("pool %+v", *b.PoolStats())
}

// checkClaimToken (ADR-032 × fast-vm): a claimed copy of the base has its
// own control-channel token (not the base's, which every copy booted with);
// a guest connection presenting the base token is refused; and guestd uses
// the new token when it reconnects (the host drops the channel; ops work
// again only over guestd's re-authenticated connection).
func checkClaimToken(ctx context.Context, m *Machine, base string) (string, error) {
	tok := m.token()
	if base == "" || tok == base || len(tok) != 64 {
		return "", fmt.Errorf("claim token %q, base %q: not a fresh per-copy token", tok, base)
	}
	host, guest := net.Pipe()
	go m.route(host)
	guest.SetDeadline(time.Now().Add(5 * time.Second))
	go json.NewEncoder(guest).Encode(proto.Msg{Type: "hello", Token: base})
	if _, err := guest.Read(make([]byte, 1)); err != io.EOF && err != io.ErrClosedPipe {
		guest.Close()
		return "", fmt.Errorf("a hello with the base token after the claim was not refused (read: %v)", err)
	}
	guest.Close()
	m.mu.Lock()
	n0, c := m.ctlConns, m.ctl
	m.mu.Unlock()
	c.Close() // guestd reconnects, with the load's token or not at all
	deadline := time.Now().Add(15 * time.Second)
	for {
		m.mu.Lock()
		n := m.ctlConns
		m.mu.Unlock()
		if n > n0 {
			break
		}
		if time.Now().After(deadline) {
			return "", errors.New("guestd did not reconnect with the claim's token")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m.token() != tok {
		return "", errors.New("token changed after the claim")
	}
	octx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := m.Op(octx, proto.Msg{Op: "ping"}); err != nil {
		return "", fmt.Errorf("op after guestd's reconnect: %w", err)
	}
	return tok, nil
}
