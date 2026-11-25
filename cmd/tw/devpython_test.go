package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevPython (issue #292): a Python Worker with a Durable Object, a KV
// namespace, a sibling module and a vendored pure-Python package runs under
// tw dev (real hostd, sandboxed workerd, Pyodide read-only from the
// per-user cache). Unsupported cases fail clearly at build.
func TestDevPython(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := nettest.WorkerdBinary(); err != nil {
		t.Skip("needs workerd (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	write := func(app string, files map[string]string) string {
		dir := filepath.Join(work, app)
		for name, s := range files {
			p := filepath.Join(dir, name)
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	wrangler := func(app, date, extra string) string {
		return `{"name": "` + app + `", "main": "src/entry.py", "compatibility_date": "` + date + `",
  "compatibility_flags": ["python_workers"],
  "vars": {"GREETING": "hi"}` + extra + `}`
	}
	ok := write("py", map[string]string{
		"tailworks.jsonc": `{ "name": "py", "tailnet": { "tags": ["tag:tw-wl-py"] } }`,
		"wrangler.jsonc": wrangler("py", "2026-04-01", `,
  "kv_namespaces": [{"binding": "KV", "id": "kv1"}],
  "durable_objects": {"bindings": [{"name": "COUNTER", "class_name": "Counter"}]}`),
		"src/entry.py": `from workers import WorkerEntrypoint, Response, DurableObject
import json
import tinypkg
from helper import double


class Counter(DurableObject):
    async def fetch(self, request):
        n = (await self.ctx.storage.get("n")) or 0
        await self.ctx.storage.put("n", n + 1)
        return Response(str(n + 1))


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        await self.env.KV.put("k", "v1")
        r = await self.env.COUNTER.getByName("a").fetch("http://do/")
        return Response(json.dumps({
            "greet": tinypkg.greet(), "double": double(21), "var": self.env.GREETING,
            "kv": await self.env.KV.get("k"), "do": await r.text(),
        }), headers={"content-type": "application/json"})
`,
		"src/helper.py":                                        "def double(x):\n    return 2 * x\n",
		"python_modules/tinypkg/__init__.py":                   "def greet():\n    return 'from tinypkg'\n",
		"python_modules/tinypkg/__pycache__/x.cpython-313.pyc": "junk",
	})
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	var r struct {
		Smoke map[string]any `json:"smoke"`
	}
	e.MustJSON(&r, "dev", ok, "--once", "--port", "0")
	if s := r.Smoke; s["greet"] != "from tinypkg" || s["double"] != float64(42) || s["var"] != "hi" || s["kv"] != "v1" || s["do"] != "1" {
		t.Fatalf("tw dev --once of a Python Worker: %+v", r.Smoke)
	}

	for _, c := range []struct {
		name, want string
		files      map[string]string
	}{
		{"noflag", "lacks python_workers", map[string]string{
			"wrangler.jsonc": `{"name": "noflag", "main": "src/entry.py", "compatibility_date": "2026-04-01"}`,
			"src/entry.py":   "x = 1\n",
		}},
		{"nosdk", "no longer has the `workers` SDK built in", map[string]string{
			"wrangler.jsonc": wrangler("nosdk", "2026-11-01", ""),
			"src/entry.py":   "from workers import WorkerEntrypoint\n",
		}},
		{"workflow", "workflows: not supported for Python Workers", map[string]string{
			"wrangler.jsonc": wrangler("workflow", "2026-04-01", `, "workflows": [{"name": "w", "binding": "W", "class_name": "Flow"}]`),
			"src/entry.py":   "from workers import WorkerEntrypoint\n",
		}},
	} {
		e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state-"+c.name), WorkDir: work}
		stdout, stderr, _, err := e.Tw("dev", write(c.name, c.files), "--once", "--port", "0")
		if out := stdout + stderr; err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: want an error with %q, got %v:\n%s", c.name, c.want, err, out)
		}
	}
}
