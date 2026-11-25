//go:build linux

package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// markerJS is a worker whose code and responses carry a unique marker, and
// that writes it to its DO volume: the state a later VM must never see.
func markerJS(marker string) string {
	return `const MARK = "` + marker + `";
export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    if (u.pathname === "/_tw/admin") return new Response("ok");
    if (u.pathname === "/egress") { const r = await env.GW.fetch("http://anything/x"); return new Response("via host: " + await r.text()); }
    if (u.pathname === "/put") { const r = await env.DISK.fetch("http://disk/" + MARK, { method: "PUT", body: MARK + MARK }); return new Response("put " + r.status); }
    return new Response("hello " + MARK);
  }
};
`
}

const markerCapnp = `using Workerd = import "/workerd/workerd.capnp";
const config :Workerd.Config = (
  services = [ (name = "main", worker = .w), (name = "gw", external = (address = "192.168.72.1:3128", http = ())),
               (name = "disk", disk = (path = "/run/tailworks/do", writable = true)) ],
  sockets = [ (name = "in", address = "*:8080", service = "main", http = (cfBlobHeader = "x-tw-cf")) ],
);
const w :Workerd.Worker = (
  compatibilityDate = "2026-04-01",
  modules = [ (name = "w.js", esModule = embed "w.js") ],
  bindings = [ (name = "GW", service = "gw"), (name = "DISK", service = "disk") ],
);
`

func poolBackend(t *testing.T, cfg Config) *Backend {
	t.Helper()
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL / TW_VM_ROOTFS not set")
	}
	cfg.Kernel, cfg.Rootfs, cfg.Helper = kernel, rootfs, os.Getenv("TW_VM_HELPER")
	if cfg.HugePages == "" {
		cfg.HugePages = os.Getenv("TW_VM_HUGEPAGES")
	}
	b, err := New(cfg)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { b.Close() })
	if b.pool == nil {
		t.Skipf("no warm pool (budget %d MiB)", cfg.PoolBudgetMiB)
	}
	return b
}

type markerApp struct {
	dir, marker string
	spec        sandbox.Spec
}

func newMarkerApp(t *testing.T, name string, volume bool) *markerApp {
	var mb [12]byte
	rand.Read(mb[:])
	a := &markerApp{dir: t.TempDir(), marker: "TWMARK" + hex.EncodeToString(mb[:])}
	os.WriteFile(filepath.Join(a.dir, "w.js"), []byte(markerJS(a.marker)), 0o600)
	os.WriteFile(filepath.Join(a.dir, "config.capnp"), []byte(markerCapnp), 0o600)
	a.spec = sandbox.Spec{ID: name, AppDir: a.dir, WorkerdArgs: []string{"serve", "config.capnp", "--experimental"}, HealthCF: `{}`, Debug: true,
		Handlers: map[uint16]http.Handler{proto.EgressObj: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "host handler for "+name)
		})}}
	if volume {
		key := make([]byte, 32)
		rand.Read(key)
		a.spec.Volume = &sandbox.Volume{Path: filepath.Join(t.TempDir(), "do.img"), Name: "pool/" + name, Epoch: 1, Key: key, SizeMiB: 128}
	}
	return a
}

func guestGet(t *testing.T, m sandbox.VM, path string) string {
	t.Helper()
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return m.DialGuest(ctx) }}}
	resp, err := cl.Get("http://guest" + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestPoolClaim: Boot() claims a warm pooled VM, injects the workload
// (bundle, config, volume, gateway handlers), and it serves; the claimed VM
// never goes back to the pool; a second load is refused by the guest.
func TestPoolClaim(t *testing.T) {
	b := poolBackend(t, Config{Pool: 2})
	ctx := context.Background()
	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := b.WaitPool(wctx, 2); err != nil {
		t.Fatal(err)
	}
	a := newMarkerApp(t, "claim", true)
	m, err := b.Boot(ctx, a.spec)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	in := m.Info()
	t.Logf("claimed: source %s, total %v, idle %v, stages %v, guest %v", in.Source, in.Total, in.PoolIdle, in.Stages, in.GuestStages)
	if in.Source != "pool" || !in.HugePages {
		t.Fatalf("source %q hugepages %v: want a pooled VM on huge pages", in.Source, in.HugePages)
	}
	if got := guestGet(t, m, "/"); got != "hello "+a.marker {
		t.Fatalf("GET /: %q", got)
	}
	if got := guestGet(t, m, "/egress"); got != "via host: host handler for claim" {
		t.Fatalf("egress through the claimed VM's gateway: %q", got)
	}
	if got := guestGet(t, m, "/put"); got != "put 204" && got != "put 200" {
		t.Fatalf("volume write: %q", got)
	}
	st, err := m.Op(ctx, proto.Msg{Op: "stats"})
	if err != nil || !strings.HasPrefix(st.Stats.DOFS, proto.VolumeDev+" ext4") {
		t.Fatalf("DO storage is not the attached volume: %+v %v", st.Stats, err)
	}
	// One load per VM, ever.
	if _, err := m.Op(ctx, proto.Msg{Op: "load", Load: &proto.Load{}}); err == nil || !strings.Contains(err.Error(), "not a pooled VM") {
		t.Fatalf("second load: %v", err)
	}
	// It is not in the pool any more (and the pool refills).
	if err := b.WaitPool(wctx, 2); err != nil {
		t.Fatal(err)
	}
	b.pool.mu.Lock()
	for _, x := range b.pool.idle {
		if x == m.(*Machine) {
			t.Fatal("the claimed VM is back in the pool")
		}
	}
	b.pool.mu.Unlock()
	if ps := b.PoolStats(); ps.Claims < 1 || ps.Booted < 3 {
		t.Fatalf("pool stats %+v", ps)
	}
}

// TestPoolNoCarryOver: a pooled VM never carries another workload's state.
// Workload A runs (its code, responses and a volume write carry a unique
// marker) and is stopped; then a pooled VM is inspected before its
// injection: its guest state dirs hold nothing (the warm-up app is gone
// too), and a full RAM dump contains neither A's marker nor the warm-up
// marker file's text. Then the VM is loaded with workload B, which serves B.
func TestPoolNoCarryOver(t *testing.T) {
	b := poolBackend(t, Config{Pool: 1})
	ctx := context.Background()
	wctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := b.WaitPool(wctx, 1); err != nil {
		t.Fatal(err)
	}
	a := newMarkerApp(t, "wa", true)
	ma, err := b.Boot(ctx, a.spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := guestGet(t, ma, "/put"); !strings.HasPrefix(got, "put 20") {
		t.Fatalf("A put: %q", got)
	}
	guestGet(t, ma, "/")
	pidA := ma.Info().PID
	ma.Stop()

	// The pool refilled after A's claim: take that VM by hand.
	if err := b.WaitPool(wctx, 1); err != nil {
		t.Fatal(err)
	}
	m := b.pool.take()
	if m == nil {
		t.Fatal("no pooled VM")
	}
	if m.Info().PID == pidA {
		t.Fatal("the pooled VM is A's VMM")
	}
	r, err := m.Op(ctx, proto.Msg{Op: "fsscan"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pooled VM state before injection (%d files scanned): %v", r.Scanned, r.State)
	for p := range r.State {
		if strings.HasPrefix(p, proto.AppDir+"/") || strings.HasPrefix(p, proto.DODir+"/") || strings.HasPrefix(p, "/tmp/") || strings.HasPrefix(p, "/dev/shm/") {
			t.Fatalf("pooled VM holds state before its load: %s", p)
		}
		if strings.Contains(p, proto.WarmupMarker) || strings.Contains(p, a.marker) {
			t.Fatalf("pooled VM holds %s", p)
		}
	}
	dir := t.TempDir()
	mem, err := m.MemoryDump(ctx, dir)
	if err != nil {
		t.Fatalf("memory dump: %v (VM exit: %v)", err, m.Err())
	}
	if hit := scanBytes(t, mem, map[string][]byte{"workload A's marker": []byte(a.marker)}); hit != "" {
		t.Fatalf("%s in the pooled VM's RAM", hit)
	}
	fi, _ := os.Stat(mem)
	os.Remove(mem)
	// Load workload B into it (claim does what Boot does after take).
	bApp := newMarkerApp(t, "wb", true)
	preClaim := m.token()
	vm, err := b.claim(ctx, m, bApp.spec)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Stop()
	// The claimed VM has its own control token; the one it booted with
	// (on its pool config drive) is refused from now on (ADR-032 × fast-vm).
	if _, err := checkClaimToken(ctx, vm.(*Machine), preClaim); err != nil {
		t.Fatal(err)
	}
	if got := guestGet(t, vm, "/"); got != "hello "+bApp.marker {
		t.Fatalf("B: %q", got)
	}
	st, _ := vm.Op(ctx, proto.Msg{Op: "fsscan"})
	for p := range st.State {
		if strings.Contains(p, a.marker) {
			t.Fatalf("B's VM has A's file %s", p)
		}
	}
	t.Logf("pooled VM: no state files, A's marker absent from %d MiB of RAM; B loaded and serving", fi.Size()>>20)
}

// TestPoolBudget: the pool refills in the background but never past the
// budget (all of this backend's VMs, pooled + claimed), and pool VMs are
// always on huge pages. Budget: 3 VMs; pool target 2.
func TestPoolBudget(t *testing.T) {
	const memMiB = 512
	free0 := FreeHugePages2M() // before the pool's first boot
	b := poolBackend(t, Config{Pool: 2, MemMiB: memMiB, PoolBudgetMiB: 3 * memMiB})
	ctx := context.Background()
	wctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := b.WaitPool(wctx, 2); err != nil {
		t.Fatal(err)
	}
	var vms []sandbox.VM
	for i := 0; i < 2; i++ {
		m, err := b.Boot(ctx, newMarkerApp(t, fmt.Sprintf("bud%d", i), false).spec)
		if err != nil {
			t.Fatal(err)
		}
		vms = append(vms, m)
		if !m.Info().HugePages {
			t.Fatal("a pooled VM without huge pages")
		}
	}
	// 2 claimed: one more fits (3), so exactly 1 idle, and it stays so.
	if err := b.WaitPool(wctx, 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	ps := b.PoolStats()
	t.Logf("after 2 claims: %+v; free 2 MiB pages %d → %d", *ps, free0, FreeHugePages2M())
	if ps.Idle != 1 || ps.Live != 3 || ps.MaxLive > 3 || ps.SkipQuota == 0 {
		t.Fatalf("budget not respected: %+v", *ps)
	}
	// (Free 2 MiB pages can't show the budget: Firecracker maps guest RAM
	// without a hugetlb reservation (resv_hugepages stays 0), so a VM takes
	// pages only as its guest touches them. That is why the pool counts
	// every live VM at its full size against the reservation.)
	t.Logf("available 2 MiB pages: %d before, %d with 3 live VMs (pages are taken on touch)", free0, FreeHugePages2M())
	// A third claim takes the last pooled VM; nothing refills (3 live).
	m3, err := b.Boot(ctx, newMarkerApp(t, "bud2", false).spec)
	if err != nil {
		t.Fatal(err)
	}
	vms = append(vms, m3)
	time.Sleep(2 * time.Second)
	if ps := b.PoolStats(); ps.Idle != 0 || ps.MaxLive > 3 {
		t.Fatalf("refilled past the budget: %+v", *ps)
	}
	// A 4th Boot() is a miss: a cold boot, outside the pool's budget (the
	// pool only governs what it pre-boots).
	booted := b.PoolStats().Booted
	for _, m := range vms {
		m.Stop()
	}
	if err := b.WaitPool(wctx, 2); err != nil {
		t.Fatal(err)
	}
	ps = b.PoolStats()
	t.Logf("after stopping the claimed VMs: %+v", *ps)
	if ps.Booted < booted+2 || ps.MaxLive > 3 {
		t.Fatalf("did not refill to target within budget: %+v", *ps)
	}
}

func scanBytes(t *testing.T, path string, needles map[string][]byte) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, n := range needles {
		if strings.Contains(string(b), string(n)) {
			return name
		}
	}
	return ""
}
