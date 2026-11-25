package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDevRefreshTypes: tw dev rewrites a stale worker-configuration.d.ts
// that plain `tw types` wrote, and leaves every other file alone.
func TestDevRefreshTypes(t *testing.T) {
	const cfg1 = `{ "name": "shop", "main": "src/index.ts", "compatibility_date": "2026-04-01", "vars": { "A": "1" } }`
	const cfg2 = `{ "name": "shop", "main": "src/index.ts", "compatibility_date": "2026-04-01", "vars": { "A": "1" }, "kv_namespaces": [{ "binding": "CACHE", "id": "c" }] }`
	setup := func(t *testing.T) string {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"wrangler.jsonc": cfg1,
			"src/index.ts":   "export default { fetch() { return new Response('hi') } }\n",
		})
		return dir
	}
	load := func(t *testing.T, dir string, o appOpts) *app {
		t.Helper()
		a, err := loadAppWith(dir, "", o)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	read := func(t *testing.T, dir string) string {
		t.Helper()
		b, _ := os.ReadFile(filepath.Join(dir, typesFile))
		return string(b)
	}
	setCfg := func(t *testing.T, dir, cfg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("refreshes a generated file", func(t *testing.T) {
		dir := setup(t)
		if err := cmdTypes([]string{dir}); err != nil {
			t.Fatal(err)
		}
		setCfg(t, dir, cfg2)
		var w bytes.Buffer
		if !devRefreshTypes(load(t, dir, appOpts{}), &w) {
			t.Fatalf("stale generated file not refreshed (%q)", w.String())
		}
		if got := read(t, dir); !strings.Contains(got, "CACHE: KVNamespace;") {
			t.Fatalf("refreshed file lacks the new binding:\n%s", got)
		}
		if !strings.Contains(w.String(), typesFile+" refreshed") {
			t.Fatalf("no notice: %q", w.String())
		}
		if devRefreshTypes(load(t, dir, appOpts{}), &w) {
			t.Fatal("rewrote an up-to-date file")
		}
		if err := cmdTypes([]string{dir, "--check"}); err != nil {
			t.Fatalf("tw types --check after the refresh: %v", err)
		}
	})

	t.Run("never creates the file", func(t *testing.T) {
		dir := setup(t)
		if devRefreshTypes(load(t, dir, appOpts{}), &bytes.Buffer{}) {
			t.Fatal("reported a write")
		}
		if _, err := os.Stat(filepath.Join(dir, typesFile)); !os.IsNotExist(err) {
			t.Fatalf("created %s: %v", typesFile, err)
		}
	})

	for name, content := range map[string]string{
		"hand-written": "interface Env { MINE: string }\n",
		"--env":        typesHeader + "wrangler.jsonc and tailworks.jsonc.\n// Environment: --env staging.\ninterface Env {\n  OLD: string;\n}\n",
	} {
		t.Run("leaves a "+name+" file alone", func(t *testing.T) {
			dir := setup(t)
			writeFiles(t, dir, map[string]string{typesFile: content})
			if devRefreshTypes(load(t, dir, appOpts{}), &bytes.Buffer{}) || read(t, dir) != content {
				t.Fatalf("touched the file:\n%s", read(t, dir))
			}
		})
	}

	t.Run("skips under --env", func(t *testing.T) {
		dir := setup(t)
		setCfg(t, dir, `{ "name": "shop", "main": "src/index.ts", "compatibility_date": "2026-04-01", "env": { "staging": {} } }`)
		writeFiles(t, dir, map[string]string{typesFile: typesHeader + "x.\ninterface Env {}\n"})
		before := read(t, dir)
		if devRefreshTypes(load(t, dir, appOpts{Env: "staging"}), &bytes.Buffer{}) || read(t, dir) != before {
			t.Fatal("refreshed under --env")
		}
	})
}
