package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// tomlApp is the wrangler.toml-only example project (no tailworks.jsonc).
const tomlApp = "../../e2e/wrangler/testdata/toml-app"

// copyApp copies the example into a temp dir (tests may add files).
func copyApp(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindWranglerConfigPrecedence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if f, _ := findWranglerConfig(dir); f != "" {
		t.Fatalf("empty dir: %q", f)
	}
	writeFiles(t, dir, map[string]string{"wrangler.toml": `name = "t"`})
	if f, sh := findWranglerConfig(dir); f != "wrangler.toml" || len(sh) != 0 {
		t.Fatalf("toml only: %q %v", f, sh)
	}
	writeFiles(t, dir, map[string]string{"wrangler.jsonc": `{"name": "c"}`})
	if f, sh := findWranglerConfig(dir); f != "wrangler.jsonc" || !reflect.DeepEqual(sh, []string{"wrangler.toml"}) {
		t.Fatalf("jsonc + toml: %q %v", f, sh)
	}
	writeFiles(t, dir, map[string]string{"wrangler.json": `{"name": "j"}`})
	if f, sh := findWranglerConfig(dir); f != "wrangler.json" || !reflect.DeepEqual(sh, []string{"wrangler.jsonc", "wrangler.toml"}) {
		t.Fatalf("all three (wrangler's order: json, jsonc, toml): %q %v", f, sh)
	}
}

func TestReadWranglerTOML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"wrangler.toml": `
name = "t"
compatibility_date = 2026-04-01  # unquoted: a TOML local date
[vars]
N = 3
[[durable_objects.bindings]]
name = "A"
class_name = "A"
[triggers]
crons = ["*/5 * * * *"]
`})
	cfg, err := readWranglerConfig(filepath.Join(dir, "wrangler.toml"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg)
	var w struct {
		CompatDate string         `json:"compatibility_date"`
		Vars       map[string]any `json:"vars"`
		DOs        struct {
			Bindings []map[string]string `json:"bindings"`
		} `json:"durable_objects"`
		Triggers struct{ Crons []string } `json:"triggers"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	if w.CompatDate != "2026-04-01" || w.Vars["N"] != float64(3) || len(w.DOs.Bindings) != 1 || w.DOs.Bindings[0]["class_name"] != "A" || w.Triggers.Crons[0] != "*/5 * * * *" {
		t.Fatalf("%s", b)
	}
	writeFiles(t, dir, map[string]string{"wrangler.toml": "name = "})
	if _, err := readWranglerConfig(filepath.Join(dir, "wrangler.toml")); err == nil {
		t.Fatal("bad TOML accepted")
	}
}

func TestApplyWranglerEnv(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{
		"name": "app", "main": "src/i.ts", "compatibility_date": "2026-04-01",
		"migrations": []any{"m"}, "triggers": map[string]any{"crons": []any{"* * * * *"}},
		"vars": map[string]any{"A": "top"}, "define": map[string]any{"X": "1"},
		"durable_objects": map[string]any{"bindings": []any{}},
		"keep_vars":       true,
		"env": map[string]any{
			"staging": map[string]any{"vars": map[string]any{"A": "stg"}},
			"prod":    map[string]any{"name": "app-production", "main": "src/p.ts", "define": map[string]any{"X": "2"}},
		},
	}
	top, explicit, notes, err := applyWranglerEnv(cfg, "", "wrangler.toml")
	if err != nil || explicit || len(notes) != 0 || top["env"] != nil || top["name"] != "app" {
		t.Fatalf("top level: %v %v %v %v", top, explicit, notes, err)
	}
	stg, explicit, notes, err := applyWranglerEnv(cfg, "staging", "wrangler.toml")
	if err != nil || explicit {
		t.Fatal(err, explicit)
	}
	// Inherited: main, compatibility_date, migrations, triggers. Not: define,
	// durable_objects (and vars, which staging sets itself). Top-only: keep_vars.
	if stg["name"] != "app-staging" || stg["main"] != "src/i.ts" || stg["compatibility_date"] != "2026-04-01" || stg["migrations"] == nil || stg["triggers"] == nil {
		t.Fatalf("staging: %v", stg)
	}
	if stg["define"] != nil || stg["durable_objects"] != nil || stg["keep_vars"] != nil || stg["vars"].(map[string]any)["A"] != "stg" {
		t.Fatalf("staging non-inheritable: %v", stg)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "define is set at the top level but not in env.staging") || !strings.Contains(notes[1], "durable_objects") {
		t.Fatalf("notes: %q", notes)
	}
	prod, explicit, _, _ := applyWranglerEnv(cfg, "prod", "wrangler.toml")
	if !explicit || prod["name"] != "app-production" || prod["main"] != "src/p.ts" || prod["vars"] != nil || prod["define"].(map[string]any)["X"] != "2" {
		t.Fatalf("prod: %v", prod)
	}
	_, _, _, err = applyWranglerEnv(cfg, "qa", "wrangler.toml")
	if err == nil || !strings.Contains(err.Error(), `no environment "qa"`) || !strings.Contains(errFix(err), "prod, staging") {
		t.Fatalf("unknown env: %v (%s)", err, errFix(err))
	}
}

// errFix is the fix line(s) of a CLI error.
func errFix(err error) string {
	b, _ := json.Marshal(err)
	return string(b)
}

func TestWranglerWarnings(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{
		"name": "a", "main": "m.ts", "compatibility_date": "x", "compatibility_flags": []any{}, "vars": map[string]any{},
		"migrations": []any{}, "triggers": map[string]any{}, "placement": map[string]any{}, "containers": []any{},
		"define": map[string]any{}, "alias": map[string]any{}, "rules": []any{}, "$schema": "x",
		"routes":      []any{"example.com/*"},
		"workers_dev": true,
		"vectorize":   []any{map[string]any{"binding": "CACHE", "index_name": "x"}, map[string]any{"binding": "SESSIONS", "index_name": "y"}},
		// Platform store bindings (Workers KV, ADR-037; R2, ADR-036; D1, ADR-038): no line.
		"d1_databases":  []any{map[string]any{"binding": "DB", "database_name": "d"}},
		"kv_namespaces": []any{map[string]any{"binding": "KV", "id": "k"}},
		"r2_buckets":    []any{map[string]any{"binding": "BUCKET", "bucket_name": "b"}},
		"ai":            map[string]any{"binding": "AI"},
		// Static assets (ADR-050): no line; Workers Sites still has one.
		"assets":        map[string]any{"directory": "public"},
		"site":          map[string]any{"bucket": "./public"},
		"build":         map[string]any{"command": "npm run build"},
		"observability": map[string]any{"enabled": true},
		"frobnicate":    1,
		"durable_objects": map[string]any{"bindings": []any{
			map[string]any{"name": "REMOTE", "class_name": "R", "script_name": "other"},
		}},
	}
	got := wranglerWarnings("wrangler.toml", cfg, "esbuild")
	want := []string{
		"wrangler.toml: ai: no such binding on tailworks: env.AI will be undefined",
		"wrangler.toml: frobnicate: unknown key, ignored",
		"wrangler.toml: observability: ignored by tailworks",
		"wrangler.toml: routes: ignored",
		"wrangler.toml: site: not supported",
		"wrangler.toml: vectorize: no such binding on tailworks: env.CACHE, env.SESSIONS will be undefined",
		"wrangler.toml: workers_dev: ignored",
		"wrangler.toml: durable_objects binding REMOTE: script_name",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("line %d: %q, want prefix %q", i, got[i], want[i])
		}
	}
	// With wrangler as the bundler, build settings are honoured: no line.
	if w := wranglerWarnings("wrangler.toml", map[string]any{"build": map[string]any{}, "no_bundle": true}, "wrangler"); len(w) != 0 {
		t.Fatalf("wrangler bundler: %q", w)
	}
}

// TestLoadAppWranglerTOMLOnly: the example deploys unchanged: no tailworks.jsonc
// (defaults: tag:tw-wl-<name>, DO classes internal), name from
// wrangler.toml, -<env> for --env, one warning per ignored key.
func TestLoadAppWranglerTOMLOnly(t *testing.T) {
	a, err := loadAppWith(tomlApp, "", appOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "wtoml" || a.WranglerFile != "wrangler.toml" || !reflect.DeepEqual(a.Cfg.Tailnet.Tags, []string{"tag:tw-wl-wtoml"}) {
		t.Fatalf("app: %s %s %v", a.Name, a.WranglerFile, a.Cfg.Tailnet.Tags)
	}
	if o := a.Cfg.Tailnet.Objects["Counter"]; o.Node == nil || *o.Node {
		t.Fatalf("Counter should be internal (node: false): %+v", o)
	}
	for _, w := range []string{"vectorize: no such binding on tailworks: env.CACHE", "routes: ignored", "workers_dev: ignored", "no tailworks.jsonc: using defaults"} {
		if !slices.ContainsFunc(a.Warnings, func(s string) bool { return strings.Contains(s, w) }) {
			t.Errorf("no warning %q in %q", w, a.Warnings)
		}
	}
	if len(a.Warnings) != 4 {
		t.Errorf("warnings: %q", a.Warnings)
	}

	s, err := loadAppWith(tomlApp, "", appOpts{Env: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "wtoml-staging" || !reflect.DeepEqual(s.Cfg.Tailnet.Tags, []string{"tag:tw-wl-wtoml-staging"}) {
		t.Fatalf("staging: %s %v", s.Name, s.Cfg.Tailnet.Tags)
	}
	if v := s.Wrangler["vars"].(map[string]any)["GREETING"]; v != "hello from staging" {
		t.Fatalf("staging vars: %v", v)
	}
	if n, err := loadAppWith(tomlApp, "other", appOpts{Env: "staging"}); err != nil || n.Name != "other" {
		t.Fatalf("--name wins: %v %v", n, err)
	}

	// With a tailworks.jsonc, its name and tags are used (the env suffix too).
	dir := copyApp(t, tomlApp)
	writeFiles(t, dir, map[string]string{"tailworks.jsonc": `{"name": "shop", "tailnet": {"tags": ["tag:shop"]}}`})
	b, err := loadAppWith(dir, "", appOpts{Env: "staging"})
	if err != nil || b.Name != "shop-staging" || !reflect.DeepEqual(b.Cfg.Tailnet.Tags, []string{"tag:shop"}) || strings.Contains(strings.Join(b.Warnings, "\n"), "no tailworks.jsonc") {
		t.Fatalf("tailworks.jsonc + env: %+v %v", b, err)
	}

	// Neither file: the old error.
	if _, err := loadAppWith(t.TempDir(), "", appOpts{}); err == nil || !strings.Contains(err.Error(), "no tailworks.jsonc or wrangler config") {
		t.Fatalf("empty dir: %v", err)
	}
	// tailworks.jsonc but no wrangler config.
	only := t.TempDir()
	writeFiles(t, only, map[string]string{"tailworks.jsonc": `{"tailnet": {"tags": ["tag:x"]}}`})
	if _, err := loadAppWith(only, "", appOpts{}); err == nil || !strings.Contains(err.Error(), "wrangler.toml") {
		t.Fatalf("no wrangler config: %v", err)
	}
}

func useCreateTw(t *testing.T) {
	ct, _ := filepath.Abs("../../js/packages/create-tailworks")
	t.Setenv("TW_CREATE_TAILWORKS", ct)
}

// unpack builds a and parses the bundle as tw-hostd does.
func buildParse(t *testing.T, a *app, vars map[string]string) (*bundle.App, map[string][]byte) {
	t.Helper()
	tb, js, err := a.build(vars)
	if err != nil {
		t.Fatal(err)
	}
	files, err := bundle.Unpack(tb)
	if err != nil {
		t.Fatal(err)
	}
	ba, err := bundle.Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	if js == 0 || len(ba.Modules[ba.Main]) != js {
		t.Fatalf("entry %s: %d bytes, reported %d", ba.Main, len(ba.Modules[ba.Main]), js)
	}
	return ba, files
}

// checkTOMLBundle: what tw-hostd gets for the example, whichever bundler.
func checkTOMLBundle(t *testing.T, ba *bundle.App, files map[string][]byte, greeting string) {
	t.Helper()
	types := map[string]int{}
	for p := range ba.Modules {
		if p != ba.Main {
			types[ba.ModuleKind(p)+":"+strings.TrimLeft(filepath.Ext(p), ".")]++
		}
	}
	if !reflect.DeepEqual(types, map[string]int{"text:md": 1, "text:txt": 1, "wasm:wasm": 1}) {
		t.Fatalf("modules: %v (%v)", types, ba.ModuleTypes)
	}
	if string(ba.Vars["GREETING"]) != `"`+greeting+`"` {
		t.Fatalf("vars: %s", ba.Vars["GREETING"])
	}
	if len(ba.Bindings) != 1 || ba.Classes["Counter"] == nil || ba.Classes["Counter"].Node || ba.Classes["Counter"].Facet() {
		t.Fatalf("Counter: %+v %+v", ba.Bindings, ba.Classes["Counter"])
	}
	if ba.CompatDate != "2026-04-01" || !slices.Contains(ba.CompatFlags, "nodejs_compat") {
		t.Fatalf("compat: %s %v", ba.CompatDate, ba.CompatFlags)
	}
	// The bundle's wrangler config is JSON, whatever the source format.
	var w map[string]any
	if err := json.Unmarshal(files["wrangler.jsonc"], &w); err != nil || w["env"] != nil {
		t.Fatalf("wrangler.jsonc in bundle: %v %v", err, w["env"])
	}
}

func TestBuildWranglerTOMLApp(t *testing.T) {
	useCreateTw(t)
	t.Run("esbuild", func(t *testing.T) {
		a, err := loadAppWith(tomlApp, "", appOpts{})
		if err != nil {
			t.Fatal(err)
		}
		ba, files := buildParse(t, a, map[string]string{"GREETING": "from --var"})
		checkTOMLBundle(t, ba, files, "from --var")
		js := string(ba.Modules[ba.Main])
		if !strings.Contains(js, `"top"`) || !strings.Contains(js, "toUpperCase") {
			t.Fatal("define / alias not applied")
		}
	})
	t.Run("esbuild_env", func(t *testing.T) {
		a, _ := loadAppWith(tomlApp, "", appOpts{Env: "staging"})
		ba, files := buildParse(t, a, nil)
		checkTOMLBundle(t, ba, files, "hello from staging")
		if !strings.Contains(string(ba.Modules[ba.Main]), `"staging"`) {
			t.Fatal("env.staging define not applied")
		}
	})
	t.Run("from_wrangler_out", func(t *testing.T) {
		a, err := loadAppWith(tomlApp, "", appOpts{WranglerOut: tomlApp + ".wrangler-out"})
		if err != nil {
			t.Fatal(err)
		}
		ba, files := buildParse(t, a, nil)
		checkTOMLBundle(t, ba, files, "hello from wrangler.toml")
		if ba.Main != "dist/index.js" || !strings.Contains(string(ba.Modules[ba.Main]), "unenv") {
			t.Fatalf("entry %s is not wrangler's output", ba.Main)
		}
		if _, ok := files["dist/README.md"]; ok {
			t.Fatal("wrangler's README.md was shipped")
		}
	})
	t.Run("from_wrangler_out_missing", func(t *testing.T) {
		a, _ := loadAppWith(tomlApp, "", appOpts{WranglerOut: filepath.Join(t.TempDir(), "nope")})
		if _, _, err := a.build(nil); err == nil || !strings.Contains(err.Error(), "wrangler output") {
			t.Fatalf("missing outdir: %v", err)
		}
	})
	t.Run("wrangler_cli_fake", func(t *testing.T) {
		// --wrangler runs `wrangler deploy --dry-run --outdir <dir> --config
		// <file> --env=<env>` in the app dir; a fake CLI copies the fixture.
		fake := filepath.Join(t.TempDir(), "wrangler")
		args := filepath.Join(t.TempDir(), "args")
		out, _ := filepath.Abs(tomlApp + ".wrangler-out")
		writeFiles(t, filepath.Dir(fake), map[string]string{"wrangler": "#!/bin/sh\necho \"$@\" > " + args + "\nmkdir -p \"$4\" && cp -R " + out + "/. \"$4\"\n"})
		os.Chmod(fake, 0o755)
		t.Setenv("TW_WRANGLER", fake)
		a, _ := loadAppWith(tomlApp, "", appOpts{Wrangler: true, Env: "staging"})
		ba, _ := buildParse(t, a, nil)
		got, _ := os.ReadFile(args)
		if f := strings.Fields(string(got)); len(f) != 7 || f[0] != "deploy" || f[1] != "--dry-run" || f[2] != "--outdir" || f[5] != "wrangler.toml" || f[6] != "--env=staging" {
			t.Fatalf("wrangler args: %q", got)
		}
		if len(ba.ModuleTypes) != 3 {
			t.Fatalf("module types: %v", ba.ModuleTypes)
		}
	})
	t.Run("wrangler_cli_fails", func(t *testing.T) {
		fake := filepath.Join(t.TempDir(), "wrangler")
		writeFiles(t, filepath.Dir(fake), map[string]string{"wrangler": "#!/bin/sh\necho 'X [ERROR] Build failed with 1 error' >&2\nexit 1\n"})
		os.Chmod(fake, 0o755)
		t.Setenv("TW_WRANGLER", fake)
		a, _ := loadAppWith(tomlApp, "", appOpts{Wrangler: true})
		if _, _, err := a.build(nil); err == nil || !strings.Contains(errFix(err), "Build failed") {
			t.Fatalf("wrangler failure: %v %s", err, errFix(err))
		}
	})
}

func TestReadWranglerOutEntry(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"worker.js": "export default {}", "worker.js.map": "{}", "README.md": "x",
		"0123456789012345678901234567890123456789-q.sql": "select 1",
		"0123456789012345678901234567890123456789-blob":  "?",
		"chunks/dep.mjs": "export {}",
	})
	files, entry, types, js, err := readWranglerOut(dir, "src/worker.ts", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"dist/0123456789012345678901234567890123456789-q.sql": "text", // wrangler's default Text rule
		"dist/0123456789012345678901234567890123456789-blob":  "data",
		"dist/chunks/dep.mjs": "js",
	}
	if entry != "dist/worker.js" || js != len("export default {}") || !reflect.DeepEqual(types, want) || len(files) != 4 {
		t.Fatalf("entry %s js %d types %v files %d", entry, js, types, len(files))
	}
	writeFiles(t, dir, map[string]string{"x.cjs": "module.exports = 1"})
	if _, _, _, _, err := readWranglerOut(dir, "src/worker.ts", nil); err == nil || !strings.Contains(err.Error(), "CommonJS") {
		t.Fatalf("cjs: %v", err)
	}
}

// TestWranglerWarningsStoreKinds: a key registered as a platform store
// binding (ADR-044) is supported, so it isn't warned about.
func TestWranglerWarningsStoreKinds(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{hostdtest.ProbeStoreKey: []any{map[string]any{"binding": "P", "name": "main"}}}
	if w := wranglerWarnings("wrangler.toml", cfg, "esbuild"); len(w) != 1 || !strings.Contains(w[0], "unknown key") {
		t.Fatalf("unregistered: %q", w)
	}
	hostdtest.RegisterProbeStore()
	if w := wranglerWarnings("wrangler.toml", cfg, "esbuild"); len(w) != 0 {
		t.Fatalf("registered store kind warned: %q", w)
	}
}
