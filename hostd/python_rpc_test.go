package hostd_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/pyodide"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

const pyRPCTarget = `from workers import WorkerEntrypoint, Response
from tailworks import get_caller


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        return Response("python fetch")

    async def add(self, a, b):
        return a + b

    def echo(self, v):
        return {"got": v, "type": type(v).__name__}

    async def whoami(self):
        return get_caller(self.ctx)

    async def boom(self, what):
        raise ValueError("boom: " + what)

    def _private(self):
        return "private"


class Admin(WorkerEntrypoint):
    async def stats(self):
        c = get_caller(self.ctx)
        return {"entry": "Admin", "caller": c["hostname"] if c else None}
`

// pyRPCCaller calls pyrpc's default entrypoint (PY), its Admin entrypoint
// (ADMIN), and tenant-py's internal TailworksRpc entrypoint by name (GATE).
const pyRPCCaller = `async function attempt(f) {
  try {
    return { ok: true, value: plain(await f()) };
  } catch (e) {
    return { ok: false, name: e?.name, message: String(e?.message ?? e) };
  }
}
// A Python dict arrives as a Map (the workers SDK's python_to_rpc).
function plain(v) {
  if (v instanceof Map) return Object.fromEntries([...v].map(([k, x]) => [k, plain(x)]));
  if (Array.isArray(v)) return v.map(plain);
  if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, plain(x)]));
  return v;
}
export default {
  async fetch(req, env) {
    const fetched = await env.PY.fetch("http://pyrpc/");
    const adminFetch = await env.ADMIN.fetch("http://pyrpc/");
    return Response.json({
      fetch: { status: fetched.status, body: await fetched.text() },
      adminFetch: { status: adminFetch.status, error: adminFetch.headers.get("tw-error") },
      add: await attempt(() => env.PY.add(2, 3)),
      echo: await attempt(() => env.PY.echo({ s: "x", n: [1, 2.5], nested: { t: true, z: null } })),
      whoami: await attempt(() => env.PY.whoami()),
      admin: await attempt(() => env.ADMIN.stats()),
      boom: await attempt(() => env.PY.boom("from js")),
      noMethod: await attempt(() => env.PY.nope()),
      private: await attempt(() => env.PY._private()),
      handler: await attempt(() => env.PY.queue()),
      gate: await attempt(() => env.GATE.call("", "add", [1, 2], "{}")),
    });
  },
};
`

// TestPythonRPC (issue #430): a JavaScript Worker calls a Python Worker's
// RPC methods over service bindings, as it can a JavaScript
// WorkerEntrypoint's: arguments and return values cross, a Python
// exception reaches the caller, the Python method names its caller with
// get_caller(self.ctx), a named entrypoint answers, and what isn't an RPC
// method (handlers, private names, the platform's own entrypoint) is
// refused.
func TestPythonRPC(t *testing.T) {
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
	helper, err := os.ReadFile(hostdtest.Template("python") + "/src/tailworks.py")
	if err != nil {
		t.Fatal(err)
	}
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}},
		func(o *hostdtest.Options) {
			o.PyodideDir = pd
			// Under the host's process sandbox where there is one (Seatbelt,
			// Landlock), as a deployment runs.
			if sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto); sb != hostd.SandboxNone {
				o.Sandbox, o.InsecureSharedHost = hostd.SandboxAuto, true
			}
		})
	e := s.e
	e.WaitActive("pyrpc", e.DeployFiles(map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "pyrpc", "main": "src/entry.py", "compatibility_date": "2026-04-01",
  "compatibility_flags": ["python_workers"]}`),
		"tailworks.jsonc":  []byte(`{"tailnet": {"tags": ["tag:ttest"]}}`),
		"tw-bundle.json":   []byte(`{"main": "src/entry.py"}`),
		"src/entry.py":     []byte(pyRPCTarget),
		"src/tailworks.py": helper,
	}, "pyrpc"), 90*time.Second)
	cl := e.WaitActive("pyrpccaller", e.DeployFiles(map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "pyrpccaller", "main": "dist/index.js", "compatibility_date": "2026-04-01",
  "services": [
    {"binding": "PY", "service": "pyrpc"},
    {"binding": "ADMIN", "service": "pyrpc", "entrypoint": "Admin"},
    {"binding": "GATE", "service": "pyrpc", "entrypoint": "TailworksRpc"}
  ]}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"tags": ["tag:ttest"]}}`),
		"tw-bundle.json":  []byte(`{"main": "dist/index.js"}`),
		"dist/index.js":   []byte(pyRPCCaller),
	}, "pyrpccaller"), 60*time.Second)
	s.waitPeer(cl.IP)

	var m map[string]any
	deadline := time.Now().Add(90 * time.Second)
	for {
		var code int
		code, m = s.getJSON("http://" + cl.IP + "/")
		if code == 200 && get(m, "add", "ok") == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d %v", code, m)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if get(m, "fetch", "status") != float64(200) || get(m, "fetch", "body") != "python fetch" {
		t.Errorf("fetch() to the Python Worker: %v", m["fetch"])
	}
	if get(m, "add", "value") != float64(5) {
		t.Errorf("add(2, 3): %v", m["add"])
	}
	if get(m, "echo", "value", "type") != "dict" || js(get(m, "echo", "value", "got")) != `{"n":[1,2.5],"nested":{"t":true,"z":null},"s":"x"}` {
		t.Errorf("echo: an object argument should arrive as a dict and come back: %v", m["echo"])
	}
	if get(m, "whoami", "value", "hostname") != cl.Hostname || get(m, "whoami", "value", "nodeId") == "" {
		t.Errorf("get_caller(self.ctx) in a Python RPC method: %v, want the caller's workload node %s", m["whoami"], cl.Hostname)
	}
	if get(m, "admin", "value", "entry") != "Admin" || get(m, "admin", "value", "caller") != cl.Hostname {
		t.Errorf("named entrypoint: %v", m["admin"])
	}
	if msg, _ := get(m, "boom", "message").(string); get(m, "boom", "ok") != false || !strings.Contains(msg, "ValueError: boom: from js") {
		t.Errorf("a Python exception should reach the caller: %v", m["boom"])
	}
	for _, k := range []string{"noMethod", "private", "handler"} {
		// A handler name is refused on the caller's side already.
		if msg, _ := get(m, k, "message").(string); get(m, k, "ok") != false || !strings.Contains(msg, "does not implement") && !strings.Contains(msg, "not a method an RPC call can name") {
			t.Errorf("%s: want a refusal, got %v", k, m[k])
		}
	}
	if msg, _ := get(m, "gate", "message").(string); get(m, "gate", "ok") != false || !strings.Contains(msg, "no entrypoint TailworksRpc") {
		t.Errorf("tenant-py's own entrypoint must not be reachable by name: %v", m["gate"])
	}
	if get(m, "adminFetch", "status") != float64(404) || get(m, "adminFetch", "error") != "service_not_found" {
		t.Errorf("fetch() to a named Python entrypoint: %v", m["adminFetch"])
	}
}
