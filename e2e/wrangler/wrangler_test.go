//go:build e2e

// Package wrangler checks tailworks with the wrangler CLI (issue #19,
// docs/wrangler.md) across processes: the tailworks binary runs `tw dev --once`
// (tw-hostd, netd and stock workerd behind it) on testdata/toml-app, a
// wrangler.toml-only Workers project, bundled three ways:
//
//   - tailworks' built-in esbuild (define, alias, rules from wrangler.toml);
//   - --from-wrangler-out testdata/toml-app.wrangler-out, the checked-in
//     output of `wrangler deploy --dry-run --outdir` (wrangler 4.60.0);
//   - --wrangler, which runs that command itself (skipped without a
//     wrangler CLI: $TW_WRANGLER, node_modules/.bin or PATH).
//
// All three must answer the same. Regenerate the fixture after changing
// the app (then strip the machine-specific path comments):
//
//	cd e2e/wrangler/testdata/toml-app && wrangler deploy --dry-run --outdir ../toml-app.wrangler-out --env= && rm ../toml-app.wrangler-out/index.js.map
package wrangler

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

type devOnce struct {
	Workload string         `json:"workload"`
	Smoke    map[string]any `json:"smoke"`
}

func TestDevWrangler(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	t.Setenv("TW_WORKERD", wd)
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	app := filepath.Join(work, "toml-app")
	if err := os.CopyFS(app, os.DirFS("testdata/toml-app")); err != nil {
		t.Fatal(err)
	}
	out, _ := filepath.Abs("testdata/toml-app.wrangler-out")
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}

	want := map[string]any{
		"status":   float64(200),
		"greeting": "hello from wrangler.toml",
		"build":    "top",
		"shout":    "ALIAS",
		"notes":    "# notes\n\nShipped as a Text module.\n",
		"hello":    "plain text module\n",
		"wasm":     true,
		"buffer":   "dHc=",
		"count":    float64(1),
		"cache":    "undefined",
	}
	run := func(t *testing.T, args ...string) devOnce {
		var r devOnce
		e.MustJSON(&r, append([]string{"dev", app, "--once", "--port", "0"}, args...)...)
		if r.Workload != "wtoml" || !reflect.DeepEqual(r.Smoke, want) {
			b, _ := json.MarshalIndent(r, "", "  ")
			t.Fatalf("tw dev %v:\n%s", args, b)
		}
		return r
	}
	t.Run("BuiltinEsbuild", func(t *testing.T) { run(t) })
	t.Run("FromWranglerOut", func(t *testing.T) { run(t, "--from-wrangler-out", out) })
	t.Run("WranglerCLI", func(t *testing.T) {
		if os.Getenv("TW_WRANGLER") == "" {
			if _, err := exec.LookPath("wrangler"); err != nil {
				t.Skip("no wrangler CLI (set $TW_WRANGLER or put wrangler on PATH)")
			}
		}
		run(t, "--wrangler")
	})
}

// TestDevViteRedirect: testdata/vite-app is a @cloudflare/vite-plugin
// project after `vite build` (issue #301). tw follows
// .wrangler/deploy/config.json to build/server/wrangler.json and packs its
// pre-built no_bundle modules as they are; the Worker imports a chunk and
// reads build/client through its assets binding.
func TestDevViteRedirect(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	t.Setenv("TW_WORKERD", wd)
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	app := filepath.Join(work, "vite-app")
	if err := os.CopyFS(app, os.DirFS("testdata/vite-app")); err != nil {
		t.Fatal(err)
	}
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	var r devOnce
	e.MustJSON(&r, "dev", app, "--once", "--port", "0")
	want := map[string]any{"status": float64(200), "greeting": "hello from a pre-built chunk", "asset": "served from build/client"}
	if r.Workload != "vite-app" || !reflect.DeepEqual(r.Smoke, want) {
		b, _ := json.MarshalIndent(r, "", "  ")
		t.Fatalf("tw dev vite-app:\n%s", b)
	}
}
