//go:build e2evm && (linux || darwin)

package vm

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const pyVMEntry = `from workers import WorkerEntrypoint, Response, DurableObject, fetch
import json
import sys


class Counter(DurableObject):
    async def increment(self):
        n = (await self.ctx.storage.get("n") or 0) + 1
        await self.ctx.storage.put("n", n)
        return n


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        ts = getattr(request.js_object.cf, "tailscale", None)
        c = getattr(ts, "caller", None) if ts else None
        n = await self.env.COUNTER.getByName("a").increment()
        return Response(json.dumps({"n": n, "caller": c.to_py() if c else None, "python": sys.version.split()[0], "greeting": self.env.GREETING}))
`

// TestE2EVMPython (ADR-055 decision 9), on the default warm pool: a Python Worker and a Python
// Durable Object on the microVM tier. The guest's workerd loads Pyodide from
// the bundles in the read-only guest image (the guest has no network), a
// tailnet caller reaches the Worker with its verified identity, and the
// object's storage is on the guest's encrypted DO volume, so it survives a
// hostd restart (a new VM).
func TestE2EVMPython(t *testing.T) { runE2EVMPython(t, 0, false) }

// TestE2EVMPythonSnapshot is TestE2EVMPython with the pool off and
// snapshots on: every VM is restored from the base snapshot, whose workerd
// started before any app (Firecracker only).
func TestE2EVMPythonSnapshot(t *testing.T) {
	if !fastVMSupported {
		t.Skip("this backend has no warm pool or snapshots (Firecracker only)")
	}
	runE2EVMPython(t, -1, true)
}

func runE2EVMPython(t *testing.T, pool int, snapshot bool) {
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL and TW_VM_ROOTFS must be set (make e2e-vm / e2e-vz)")
	}
	be, _, err := newBackend(kernel, rootfs, 2, pool, snapshot)
	if err != nil {
		t.Fatalf("microVM backend unavailable: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	e := hostdtest.Start(t, hostdtest.Options{ControlURL: ctl.URL, Fake: fakeStatic(), Ephemeral: true, KillTombstone: 5 * time.Second,
		VM: be, Tenant: mgmtapi.Tenant{Tailnet: "ttest.ts.net", ControlURL: ctl.URL, MintMode: "static", Tier: bundle.TierMicroVM}})
	ctx := context.Background()
	client, err := nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, "ttest-client", filepath.Join(e.Dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	hc := client.HTTPClient(60 * time.Second)

	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "py", "main": "src/entry.py", "compatibility_date": "2026-04-01",
  "compatibility_flags": ["python_workers"], "vars": {"GREETING": "hi"},
  "durable_objects": {"bindings": [{"name": "COUNTER", "class_name": "Counter"}]}}`),
		"tailworks.jsonc": []byte(`{"tier": "microvm", "tailnet": {"tags": ["tag:tw-wl-py"]}}`),
		"tw-bundle.json":  []byte(`{"main": "src/entry.py"}`),
		"src/entry.py":    []byte(pyVMEntry),
	}
	t0 := time.Now()
	dep := e.DeployFiles(files, "py")
	var ip string
	start := func() {
		t.Helper()
		wl := e.WaitActive("py", dep, 120*time.Second)
		for _, w := range e.Host.Status().Workloads {
			if w.Workload == "py" && w.Tier != "microvm" {
				t.Fatalf("py runs on tier %q, want microvm", w.Tier)
			}
		}
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := client.WaitPeers(wctx, wl.IP); err != nil {
			t.Fatal(err)
		}
		ip = wl.IP
	}
	call := func(want float64) {
		t.Helper()
		resp, err := hc.Get("http://" + ip + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var r struct {
			N      float64        `json:"n"`
			Caller map[string]any `json:"caller"`
			Python string         `json:"python"`
			Greet  string         `json:"greeting"`
		}
		if json.Unmarshal(b, &r) != nil || resp.StatusCode != 200 || r.N != want || r.Caller == nil || r.Greet != "hi" {
			t.Fatalf("GET /: %d %s (want n=%v and a caller)", resp.StatusCode, b, want)
		}
		t.Logf("n=%v, Python %s, caller %v", r.N, r.Python, r.Caller["hostname"])
	}
	start()
	t1 := time.Now()
	call(1)
	t.Logf("deploy to active %v, first request %v", t1.Sub(t0).Round(time.Millisecond), time.Since(t1).Round(time.Millisecond))
	call(2)

	// hostd restarts: a new VM, the same encrypted volume.
	e.Restart()
	start()
	call(3)

	// A late compatibility date runs Python 3.14: another bundle of the image.
	files["wrangler.jsonc"] = []byte(`{"name": "py", "main": "src/entry.py", "compatibility_date": "2026-11-01",
  "compatibility_flags": ["python_workers", "disable_python_external_sdk"], "vars": {"GREETING": "hi"},
  "durable_objects": {"bindings": [{"name": "COUNTER", "class_name": "Counter"}]}}`)
	dep = e.DeployFiles(files, "py")
	start()
	resp, err := hc.Get("http://" + ip + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var r struct {
		N      float64 `json:"n"`
		Python string  `json:"python"`
	}
	if json.Unmarshal(b, &r) != nil || r.N != 4 || !strings.HasPrefix(r.Python, "3.14") {
		t.Fatalf("compatibility_date 2026-11-01: %d %s (want Python 3.14, n=4)", resp.StatusCode, b)
	}
	t.Logf("redeployed: n=%v, Python %s", r.N, r.Python)
}
