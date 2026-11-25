package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/pyodide"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

func pythonBundle() map[string][]byte {
	return map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "py", "main": "src/entry.py", "compatibility_date": "2026-04-01",
  "compatibility_flags": ["python_workers"],
  "durable_objects": {"bindings": [{"name": "COUNTER", "class_name": "Counter"}]}}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"tags": ["tag:tw-wl-py"]}}`),
		"tw-bundle.json":  []byte(`{"main": "src/entry.py"}`),
		"src/entry.py": []byte(`from workers import WorkerEntrypoint, Response, DurableObject
import json


class Counter(DurableObject):
    async def fetch(self, request):
        n = (await self.ctx.storage.get("n")) or 0
        await self.ctx.storage.put("n", n + 1)
        return Response(str(n + 1))


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        ts = getattr(request.js_object.cf, "tailscale", None)
        c = getattr(ts, "caller", None) if ts else None
        r = await self.env.COUNTER.getByName("a").fetch("http://do/")
        return Response(json.dumps({"caller": c.to_py() if c else None, "do": await r.text()}))
`),
	}
}

// TestPythonWorkerSandboxed (issue #292): a Python Worker deployed to a
// host runs under the host's process sandbox (Seatbelt on macOS, Landlock
// on Linux), with Pyodide read-only from the host's bundle dir and no
// network: a tailnet caller reaches it, its identity is the verified one,
// and its Durable Object keeps state.
func TestPythonWorkerSandboxed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto)
	if sb == hostd.SandboxNone {
		t.Skip("no process sandbox on this host")
	}
	pd, err := pyodide.UserDir()
	if err != nil {
		t.Skip(err)
	}
	if err := pyodide.Ensure(context.Background(), pd, ""); err != nil {
		t.Skipf("no Pyodide bundles (offline?): %v", err)
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	e := hostdtest.Start(t, hostdtest.Options{Fake: fakemgmt.Config{StaticKey: nettest.DummyKey}, Ephemeral: true,
		Sandbox: hostd.SandboxAuto, InsecureSharedHost: true, PyodideDir: pd,
		Tenant: mgmtapi.Tenant{Tailnet: "a.ts.net", ControlURL: ctl.URL, MintMode: "static"}})
	wl := e.WaitActive("py", e.DeployFiles(pythonBundle(), "py"), 90*time.Second)
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == "py" && w.SandboxProfile == "" {
			t.Fatalf("no sandbox profile: %+v", w)
		}
	}
	client, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-client", filepath.Join(e.Dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	waitPeerOf(t, client, wl.IP)
	hc := client.HTTPClient(60 * time.Second)
	for i, want := range []string{"1", "2"} {
		resp, err := hc.Get("http://" + wl.IP + "/")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var r struct {
			Caller map[string]any `json:"caller"`
			DO     string         `json:"do"`
		}
		if err := json.Unmarshal(b, &r); err != nil || resp.StatusCode != 200 {
			t.Fatalf("GET %d: %d %s", i, resp.StatusCode, b)
		}
		if r.DO != want || r.Caller == nil || !strings.HasPrefix(r.Caller["hostname"].(string), "ttest-client") {
			t.Fatalf("GET %d: %s", i, b)
		}
	}
}

const pyQueueEntry = `from workers import WorkerEntrypoint, Response, DurableObject
import json


class Log(DurableObject):
    async def add(self, rec):
        recs = json.loads(await self.ctx.storage.get("recs") or "[]")
        recs.append(json.loads(rec))
        await self.ctx.storage.put("recs", json.dumps(recs))

    async def recs(self):
        return await self.ctx.storage.get("recs") or "[]"


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        if "/send?k=" in request.url:
            await self.env.JOBS.send(request.url.split("/send?k=", 1)[1])
            return Response("{}")
        return Response(await self.env.LOG.getByName("log").recs())

    async def queue(self, batch, env, ctx):
        log = self.env.LOG.getByName("log")
        for m in batch.messages:
            await log.add(json.dumps({"k": m.body, "attempts": m.attempts, "queue": batch.queue}))
            if m.body == "throw" and m.attempts == 1:
                raise ValueError("first try fails")
            if m.body == "retry" and m.attempts == 1:
                m.retry()
            else:
                m.ack()
`

// TestPythonQueues: a Python Worker produces to a queue and consumes it:
// hostd's consumer runs the Python default export's queue() through the
// front, and what it decides (ack, a per-message retry, a throw retrying
// the batch) is applied to the batch.
func TestPythonQueues(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	pd, err := pyodide.UserDir()
	if err != nil {
		t.Skip(err)
	}
	if err := pyodide.Ensure(context.Background(), pd, ""); err != nil {
		t.Skipf("no Pyodide bundles (offline?): %v", err)
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	e := hostdtest.Start(t, hostdtest.Options{Fake: fakemgmt.Config{StaticKey: nettest.DummyKey}, Ephemeral: true, PyodideDir: pd,
		QueueVisibility: 3 * time.Second,
		Tenant:          mgmtapi.Tenant{Tailnet: "a.ts.net", ControlURL: ctl.URL, MintMode: "static"}})
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "pyq", "main": "src/entry.py", "compatibility_date": "2026-04-01",
  "compatibility_flags": ["python_workers"],
  "durable_objects": {"bindings": [{"name": "LOG", "class_name": "Log"}]},
  "queues": {"producers": [{"binding": "JOBS", "queue": "jobs"}],
             "consumers": [{"queue": "jobs", "max_batch_size": 1, "max_batch_timeout": 1, "max_retries": 3}]}}`),
		"tw-bundle.json": []byte(`{"main": "src/entry.py"}`),
		"src/entry.py":   []byte(pyQueueEntry),
	}
	wl := e.WaitActive("pyq", e.DeployFiles(files, "pyq"), 90*time.Second)
	client, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-client", filepath.Join(e.Dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	waitPeerOf(t, client, wl.IP)
	hc := client.HTTPClient(60 * time.Second)
	get := func(path string) []byte {
		t.Helper()
		resp, err := hc.Get("http://" + wl.IP + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
		}
		return b
	}
	for _, k := range []string{"plain", "retry", "throw"} {
		get("/send?k=" + k)
	}
	type rec struct {
		K        string `json:"k"`
		Attempts int    `json:"attempts"`
		Queue    string `json:"queue"`
	}
	want := map[string]int{"plain": 1, "retry": 2, "throw": 2} // the highest attempt each must reach
	deadline := time.Now().Add(60 * time.Second)
	for {
		var recs []rec
		json.Unmarshal(get("/"), &recs)
		got := map[string]int{}
		for _, r := range recs {
			if r.Queue != "jobs" {
				t.Fatalf("queue %q in %+v", r.Queue, r)
			}
			got[r.K] = max(got[r.K], r.Attempts)
		}
		if fmt.Sprint(got) == fmt.Sprint(want) {
			time.Sleep(3 * time.Second) // nothing is redelivered after its ack
			json.Unmarshal(get("/"), &recs)
			if len(recs) != 5 {
				t.Fatalf("deliveries after the acks: %+v, want 5", recs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("deliveries %v, want highest attempts %v (%+v)", got, want, recs)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
