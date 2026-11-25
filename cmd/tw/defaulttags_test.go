package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestDefaultWorkloadTags: a tailworks.jsonc without tailnet.tags gets
// tag:tw-wl-<workload> (issue #491), written into the bundled file with its
// comments kept; an explicit [] is still an error.
func TestDefaultWorkloadTags(t *testing.T) {
	t.Parallel()
	const wr = `{"name": "shop", "main": "src/index.js", "compatibility_date": "2026-04-01"}`
	for name, tc := range map[string]struct {
		tw, env string
		want    []string
	}{
		"empty file":       {tw: `{}`, want: []string{"tag:tw-wl-shop"}},
		"no tailnet":       {tw: "{\n  // keep me\n  \"tier\": \"process\"\n}", want: []string{"tag:tw-wl-shop"}},
		"tailnet, no tags": {tw: `{"tailnet": { /* keep me */ "connects": ["tag:db"] }}`, want: []string{"tag:tw-wl-shop"}},
		"env":              {tw: `{}`, env: "staging", want: []string{"tag:tw-wl-shop-staging"}},
		"explicit":         {tw: `{"tailnet": {"tags": ["tag:x"]}}`, want: []string{"tag:x"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			w := wr
			if tc.env != "" {
				w = `{"name": "shop", "main": "src/index.js", "compatibility_date": "2026-04-01", "env": {"` + tc.env + `": {}}}`
			}
			writeFiles(t, dir, map[string]string{"tailworks.jsonc": tc.tw, "wrangler.jsonc": w})
			a, err := loadAppWith(dir, "", appOpts{Env: tc.env})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a.Cfg.Tailnet.Tags, tc.want) {
				t.Fatalf("tags %v, want %v", a.Cfg.Tailnet.Tags, tc.want)
			}
			cfg, err := mgmt.ParseTwConfig(a.TwRaw)
			if err != nil || !reflect.DeepEqual(cfg.Tailnet.Tags, tc.want) {
				t.Fatalf("TwRaw tags %v (%v):\n%s", cfg, err, a.TwRaw)
			}
			if strings.Contains(tc.tw, "keep me") && !strings.Contains(string(a.TwRaw), "keep me") {
				t.Errorf("comment lost:\n%s", a.TwRaw)
			}
			if err := checkTwSchema(a.TwRaw); err != nil {
				t.Errorf("defaulted file fails the schema: %v\n%s", err, a.TwRaw)
			}
			if !slices.ContainsFunc(cfg.Classes(), func(c mgmt.ClassConfig) bool { return c.Class == mgmt.WorkloadNamespace }) {
				t.Errorf("no %s namespace: %+v", mgmt.WorkloadNamespace, cfg.Classes())
			}
			for _, n := range a.Warnings {
				if strings.Contains(n, "named after another app") {
					t.Errorf("note on the default tag: %s", n)
				}
			}
		})
	}

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"tailworks.jsonc": `{"tailnet": {"tags": []}}`, "wrangler.jsonc": wr})
	if _, err := loadApp(dir, ""); err == nil || !strings.Contains(err.Error(), "empty tailnet.tags") {
		t.Fatalf(`"tags": []: %v`, err)
	}
}

// TestDefaultWorkloadTagsBundled: both build paths ship the defaulted
// tailworks.jsonc, which is what mgmt reads the namespaces from.
func TestDefaultWorkloadTagsBundled(t *testing.T) {
	useCreateTw(t)
	check := func(t *testing.T, tb []byte) {
		t.Helper()
		cfg, err := mgmt.BundleConfig(tb)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(cfg.Classes(), func(c mgmt.ClassConfig) bool {
			return c.Class == mgmt.WorkloadNamespace && slices.Equal(c.Tags, []string{"tag:tw-wl-shop"})
		}) {
			t.Fatalf("bundle namespaces: %+v", cfg.Classes())
		}
	}
	t.Run("workerd", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"tailworks.jsonc": `{}`,
			"wrangler.jsonc":  `{"name": "shop", "main": "src/index.js", "compatibility_date": "2026-04-01"}`,
			"src/index.js":    `export default { fetch() { return new Response("hi") } }`,
		})
		a, err := loadApp(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		tb, _, err := a.build(nil)
		if err != nil {
			t.Fatal(err)
		}
		check(t, tb)
	})
	t.Run("node", func(t *testing.T) {
		// A stand-in for node + build-node.mjs: like the real one, it copies
		// the app's on-disk tailworks.jsonc into the output.
		bin := t.TempDir()
		os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\nmkdir -p \"$3/dist\" && echo 'export {}' > \"$3/dist/server.mjs\" && cp \"$2/tailworks.jsonc\" \"$3/tailworks.jsonc\"\n"), 0o755)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"tailworks.jsonc": `{"name": "shop", "runtime": "node", "main": "src/server.js"}`,
			"src/server.js":   `export {}`,
		})
		a, err := loadApp(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		tb, _, err := a.build(nil)
		if err != nil {
			t.Fatal(err)
		}
		check(t, tb)
		files, _ := bundle.Unpack(tb)
		if !strings.Contains(string(files["tailworks.jsonc"]), "tag:tw-wl-shop") {
			t.Fatalf("bundled tailworks.jsonc:\n%s", files["tailworks.jsonc"])
		}
	})
}

// TestInitDefaultTags: the hello template has no tailnet.tags, so a new app
// gets tag:tw-wl-<workload>, and --tags adds the line.
func TestInitDefaultTags(t *testing.T) {
	if _, err := createTailworksDir(); err != nil {
		t.Skip("create-tailworks not found:", err)
	}
	dir := filepath.Join(t.TempDir(), "billing")
	if err := cmdInit([]string{dir, "--template", "hello"}); err != nil {
		t.Fatal(err)
	}
	if got := initTags(dir); !slices.Equal(got, []string{"tag:tw-wl-billing"}) {
		t.Errorf("hello: tags %v", got)
	}
	dir = filepath.Join(t.TempDir(), "shared")
	if err := cmdInit([]string{dir, "--template", "hello", "--tags", "tag:tw-app"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "tailworks.jsonc"))
	if got := initTags(dir); !slices.Equal(got, []string{"tag:tw-app"}) || !strings.Contains(string(b), "// tags this app calls") {
		t.Errorf("hello --tags: tags %v\n%s", got, b)
	}
}

func TestInsertWorkloadTags(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{`[]`, `{"tailnet": 1}`, `{`} {
		if _, err := insertWorkloadTags([]byte(bad), []string{"tag:x"}); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	in := `{"tailnet": {"tags": ["tag:y"]}}`
	if out, err := insertWorkloadTags([]byte(in), []string{"tag:x"}); err != nil || string(out) != in {
		t.Errorf("existing tags changed: %s %v", out, err)
	}
}
