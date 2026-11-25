package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// viteProject is what `vite build` leaves with @cloudflare/vite-plugin
// (React Router shape, issue #301): the user's wrangler.jsonc, whose main
// only builds through Vite and whose assets directory is wrong, the
// redirect, and the generated config next to the pre-built Worker.
func viteProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"tailworks.jsonc": `{"name": "rr", "tailnet": {"tags": ["tag:tw-wl-rr"]}} // kept
`,
		"wrangler.jsonc":               `{"name": "rr", "main": "./workers/app.ts", "compatibility_date": "2025-04-01", "assets": {"directory": "./dist"}}`,
		"workers/app.ts":               `import build from "virtual:react-router/server-build"; export default { fetch: () => build }`,
		".wrangler/deploy/config.json": `{"configPath":"../../build/server/wrangler.json","auxiliaryWorkers":[]}`,
		"build/server/wrangler.json": `{"name": "rr", "main": "index.js", "compatibility_date": "2025-04-01",
			"no_bundle": true, "rules": [{"type": "ESModule", "globs": ["**/*.js", "**/*.mjs"]}],
			"assets": {"directory": "../client"},
			"d1_databases": [{"binding": "DB", "database_name": "rr", "migrations_dir": "../../migrations"}]}`,
		"build/server/index.js":                    `import { h } from "./assets/server-build-B3k9.js"; export default { fetch: () => new Response(h()) }`,
		"build/server/index.js.map":                `{}`,
		"build/server/assets/server-build-B3k9.js": `export const h = () => "hi"`,
		"build/server/assets/query.sql":            `select 1`,
		"build/server/.vite/manifest.json":         `{}`,
		"build/client/favicon.ico":                 "ico",
		"migrations/0001_init.sql":                 "create table t (x)",
	})
	return dir
}

func TestViteRedirectedConfig(t *testing.T) {
	useCreateTw(t)
	dir := viteProject(t)
	a, err := loadAppWith(dir, "", appOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if a.WranglerFile != "build/server/wrangler.json" || a.Name != "rr" {
		t.Fatalf("config %s, name %s", a.WranglerFile, a.Name)
	}
	want := []string{"using build/server/wrangler.json, the config `vite build` wrote (.wrangler/deploy/config.json), as wrangler deploy does"}
	if !reflect.DeepEqual(a.Warnings, want) {
		t.Fatalf("warnings (want one line, and no no_bundle warning):\n%s", strings.Join(a.Warnings, "\n"))
	}
	// Paths are the generated config's, rebased onto the project directory.
	if wranglerMain(a.Wrangler) != "build/server/index.js" || a.assetsDir() != dir+"/build/client" {
		t.Fatalf("main %s, assets %s", wranglerMain(a.Wrangler), a.assetsDir())
	}
	if m := a.Wrangler["d1_databases"].([]any)[0].(map[string]any)["migrations_dir"]; m != "migrations" {
		t.Fatalf("migrations_dir %v", m)
	}
	if a.builder() != builderPrebuilt {
		t.Fatalf("builder %s", a.builder())
	}
	ba, files := buildParse(t, a, nil)
	if string(files["tailworks.jsonc"]) != `{"name": "rr", "tailnet": {"tags": ["tag:tw-wl-rr"]}} // kept
` {
		t.Fatalf("tailworks.jsonc is not the project's: %s", files["tailworks.jsonc"])
	}
	if ba.Main != "dist/index.js" || !strings.Contains(string(ba.Modules[ba.Main]), "./assets/server-build-B3k9.js") {
		t.Fatalf("entry %s is not the pre-built module:\n%s", ba.Main, ba.Modules[ba.Main])
	}
	wantTypes := map[string]string{"dist/assets/server-build-B3k9.js": "js", "dist/assets/query.sql": "text"}
	if !reflect.DeepEqual(ba.ModuleTypes, wantTypes) {
		t.Fatalf("module types %v", ba.ModuleTypes)
	}
	for _, p := range []string{"dist/wrangler.json", "dist/index.js.map", "dist/.vite/manifest.json"} {
		if _, ok := files[p]; ok {
			t.Fatalf("%s was shipped", p)
		}
	}
	if _, ok := files[bundle.AssetsPrefix+"favicon.ico"]; !ok {
		t.Fatal("assets are not build/client's")
	}
}

func TestViteRedirectErrors(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		_, err := loadAppWith(viteProject(t), "", appOpts{Env: "staging"})
		if err == nil || !strings.Contains(errFix(err), "CLOUDFLARE_ENV=staging") {
			t.Fatalf("--env with a redirect: %v %s", err, errFix(err))
		}
	})
	t.Run("stale", func(t *testing.T) {
		dir := viteProject(t)
		writeFiles(t, dir, map[string]string{".wrangler/deploy/config.json": `{"configPath":"../../dist/gone/wrangler.json"}`})
		_, err := loadAppWith(dir, "", appOpts{})
		if err == nil || !strings.Contains(err.Error(), "dist/gone/wrangler.json, which does not exist") {
			t.Fatalf("missing target: %v", err)
		}
	})
	t.Run("aux", func(t *testing.T) {
		dir := viteProject(t)
		writeFiles(t, dir, map[string]string{".wrangler/deploy/config.json": `{"configPath":"../../build/server/wrangler.json","auxiliaryWorkers":[{"configPath":"../../dist/aux/wrangler.json"}]}`})
		a, err := loadAppWith(dir, "", appOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Warnings) != 2 || !strings.Contains(a.Warnings[1], "auxiliary Worker ../../dist/aux/wrangler.json: not deployed") {
			t.Fatalf("aux: %v %q", err, a.Warnings)
		}
	})
	t.Run("no_redirect", func(t *testing.T) {
		if f, _, err := redirectedWranglerConfig(t.TempDir()); f != "" || err != nil {
			t.Fatalf("%q %v", f, err)
		}
	})
}

func TestRebaseWranglerPaths(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{
		"main": "index.js", "tsconfig": "/abs/tsconfig.json",
		"containers": []any{
			map[string]any{"class_name": "A", "image": "../../Dockerfile"},
			map[string]any{"class_name": "B", "image": "./sandbox"},
			map[string]any{"class_name": "C", "image": "docker.io/x/y:1"},
		},
	}
	rebaseWranglerPaths(cfg, "dist/w")
	cs := cfg["containers"].([]any)
	got := []any{cfg["main"], cfg["tsconfig"], cs[0].(map[string]any)["image"], cs[1].(map[string]any)["image"], cs[2].(map[string]any)["image"]}
	want := []any{"dist/w/index.js", "/abs/tsconfig.json", "./Dockerfile", "./dist/w/sandbox", "docker.io/x/y:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPrebuiltNoBundle(t *testing.T) {
	t.Parallel()
	esm := func(globs ...any) map[string]any { return map[string]any{"type": "ESModule", "globs": globs} }
	for _, c := range []struct {
		name string
		w    map[string]any
		want bool
	}{
		{"vite", map[string]any{"main": "index.js", "no_bundle": true, "rules": []any{esm("**/*.js", "**/*.mjs")}}, true},
		{"plus_text", map[string]any{"main": "index.js", "no_bundle": true, "rules": []any{esm("**/*.js"), map[string]any{"type": "Text", "globs": []any{"**/*.md"}}}}, true},
		{"bundled", map[string]any{"main": "index.js", "rules": []any{esm("**/*.js")}}, false},
		{"no_esm_rule", map[string]any{"main": "index.js", "no_bundle": true}, false},
		{"ts_glob", map[string]any{"main": "index.js", "no_bundle": true, "rules": []any{esm("**/*.ts")}}, false},
		{"commonjs", map[string]any{"main": "index.js", "no_bundle": true, "rules": []any{esm("**/*.js"), map[string]any{"type": "CommonJS", "globs": []any{"**/*.cjs"}}}}, false},
	} {
		if got := prebuiltNoBundle(c.w); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// TestPrebuiltNoBundleDirect: the generated config deployed directly (tw
// deploy build/server) packs the same way, and find_additional_modules
// false ships main alone.
func TestPrebuiltNoBundleDirect(t *testing.T) {
	useCreateTw(t)
	dir := viteProject(t) + "/build/server"
	// The generated config as issue #301 found it (assets, no D1).
	writeFiles(t, dir, map[string]string{"wrangler.json": `{"name": "rr", "main": "index.js", "no_bundle": true,
		"rules": [{"type": "ESModule", "globs": ["**/*.js", "**/*.mjs"]}], "assets": {"directory": "../client"}}`})
	a, err := loadAppWith(dir, "", appOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ba, _ := buildParse(t, a, nil)
	if len(ba.ModuleTypes) != 2 {
		t.Fatalf("module types %v", ba.ModuleTypes)
	}
	writeFiles(t, dir, map[string]string{"wrangler.json": `{"name": "rr", "main": "index.js", "no_bundle": true, "find_additional_modules": false,
		"rules": [{"type": "ESModule", "globs": ["**/*.js"]}]}`})
	a, _ = loadAppWith(dir, "", appOpts{})
	files, main, types, _, err := a.buildPrebuilt()
	if err != nil || main != "dist/index.js" || len(files) != 1 || len(types) != 0 {
		t.Fatalf("find_additional_modules false: %v %s %v", err, main, types)
	}
}
