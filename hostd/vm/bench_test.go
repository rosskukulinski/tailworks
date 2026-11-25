//go:build linux

package vm

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// heavyJS spends a few hundred ms of CPU in module top-level code: if
// workerd binds its ingress only after loading every worker,
// "workerd_listen" moves out by that much (checks what the "bundle load"
// stage really measures). (A Date.now() loop would never end: workerd
// freezes the clock during execution.)
const heavyJS = `let x = 0; for (let i = 0; i < 3e8; i++) { x = (x + i) % 1000003; }
export default { async fetch(req) { return new Response("ok " + x); } };
`

// TestBootBench boots the guest N times (TW_VM_BENCH=N) serially and
// records the per-stage breakdown (Phase 1 of the fast-VM work), as JSON
// into TW_VM_BENCH_OUT. Variants: the tiny smoke config without a volume,
// the same with a fresh encrypted DO volume per boot (what hostd always
// attaches), and one boot with 300 ms of top-level module work.
func TestBootBench(t *testing.T) {
	n := envInt("TW_VM_BENCH")
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if n <= 0 || kernel == "" || rootfs == "" {
		t.Skip("TW_VM_BENCH / TW_VM_KERNEL / TW_VM_ROOTFS not set")
	}
	b, err := New(Config{Kernel: kernel, Rootfs: rootfs, HugePages: os.Getenv("TW_VM_HUGEPAGES"), Helper: os.Getenv("TW_VM_HELPER")})
	if err != nil {
		t.Skip(err)
	}
	defer b.Close()
	out := map[string]any{"started": time.Now().UTC().Format(time.RFC3339), "n": n, "free_hugepages_2m": FreeHugePages2M(),
		"caveat": "Lima VM on Apple silicon: Firecracker under NESTED virtualization (stage-2 faults are much costlier than on bare metal)"}
	mk := func(js string) string {
		app := t.TempDir()
		os.WriteFile(filepath.Join(app, "w.js"), []byte(js), 0o600)
		os.WriteFile(filepath.Join(app, "config.capnp"), []byte(smokeCapnp), 0o600)
		return app
	}
	smoke, heavy := mk(smokeJS), mk(heavyJS)
	ctx := context.Background()
	t.Cleanup(func() {
		if p := os.Getenv("TW_VM_BENCH_OUT"); p != "" {
			bb, _ := json.MarshalIndent(out, "", "  ")
			os.WriteFile(p, bb, 0o644)
		}
	})
	only := os.Getenv("TW_VM_BENCH_ONLY")
	run := func(name, app string, count int, volume bool) {
		if only != "" && !strings.Contains(","+only+",", ","+name+",") {
			return
		}
		var infos []sandbox.Info
		var wb, warm []map[string]float64
		for i := 0; i < count; i++ {
			spec := sandbox.Spec{ID: fmt.Sprintf("b%d", i), AppDir: app, WorkerdArgs: []string{"serve", "config.capnp", "--experimental"},
				HealthCF: `{}`, Debug: true}
			if volume {
				key := make([]byte, 32)
				rand.Read(key)
				spec.Volume = &sandbox.Volume{Path: filepath.Join(t.TempDir(), "do.img"), Name: "bench/" + spec.ID, Epoch: 1, Key: key, SizeMiB: 128}
			}
			m, err := b.Boot(ctx, spec)
			if err != nil {
				t.Fatalf("%s boot %d: %v", name, i, err)
			}
			infos = append(infos, m.Info())
			if i < 3 && !volume {
				r, err := m.Op(ctx, proto.Msg{Op: "wbench", N: 3})
				if err == nil {
					wb = append(wb, r.Stages)
				}
				// A second workerd in the same, now warm, VM (page cache
				// and guest RAM already faulted in): the floor of a warm pool.
				m.Op(ctx, proto.Msg{Op: "wkill"})
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					gs := m.Info().GuestStages
					if h, ok := gs["healthy#1"]; ok {
						warm = append(warm, map[string]float64{"spawn_to_listen": round(gs["workerd_listen#1"] - gs["workerd_spawn#1"]),
							"listen_to_healthy": round(h - gs["workerd_listen#1"]), "spawn_to_healthy": round(h - gs["workerd_spawn#1"])})
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			m.Stop()
		}
		order, stats := Summarize(infos)
		var lines []string
		for _, k := range order {
			s := stats[k]
			lines = append(lines, fmt.Sprintf("%-75s p50 %7.1f  p95 %7.1f ms", k, s["p50"], s["p95"]))
		}
		t.Logf("%s (%d boots):\n%s", name, count, strings.Join(lines, "\n"))
		raw := make([]map[string]any, len(infos))
		for i, in := range infos {
			raw[i] = map[string]any{"stages": in.Stages, "guest": in.GuestStages, "breakdown": Breakdown(in), "hugepages": in.HugePages}
		}
		out[name] = map[string]any{"order": order, "stats": stats, "boots": raw, "workerd_version_ms": wb, "warm_workerd_restart_ms": warm}
		if len(warm) > 0 {
			t.Logf("%s: warm workerd restart in the same VM: %v", name, warm)
		}
	}
	run("smoke", smoke, n, false)
	run("smoke_volume", smoke, n, true)
	run("heavy_toplevel", heavy, 3, false)
}
