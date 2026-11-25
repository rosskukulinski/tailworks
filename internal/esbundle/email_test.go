package esbundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/emailstub"
)

// emailApp writes an app whose entry imports ./lib.js and the dependency
// "dep" (node_modules/dep, which imports cloudflare:email itself, as the
// Agents SDK does). lib is lib.js's source; main is the entry.
func emailApp(t *testing.T, lib, main string) string {
	t.Helper()
	dir := t.TempDir()
	for p, s := range map[string]string{
		"src/index.ts":                  `import { lib } from "./lib.js"; import { dep } from "dep"; export default { fetch() { return new Response(String(lib) + dep()); } };`,
		"src/lib.js":                    lib,
		"wrangler.jsonc":                `{"name": "email", "main": "` + main + `", "compatibility_date": "2026-06-11"}`,
		"node_modules/dep/package.json": `{"name": "dep", "main": "index.js"}`,
		"node_modules/dep/index.js":     `import { EmailMessage } from "cloudflare:email"; export const dep = () => typeof EmailMessage;`,
	} {
		f := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// buildMJS runs create-tailworks' build.mjs over dir (skipped without node).
func buildMJS(t *testing.T, dir string) (string, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	root, _ := filepath.Abs("../..")
	if _, err := os.Stat(filepath.Join(root, "node_modules", "esbuild")); err != nil {
		t.Skip("needs node_modules (make deps)")
	}
	b, err := exec.Command(node, filepath.Join(root, "js", "packages", "create-tailworks", "lib", "build.mjs"), dir, filepath.Join(dir, "js.js")).CombinedOutput()
	return string(b), err
}

// TestEmailImportRefused: the app's own import of cloudflare:email, static
// or dynamic, fails the build with the refusal naming the file, in the Go
// bundler and in build.mjs.
func TestEmailImportRefused(t *testing.T) {
	for name, c := range map[string]struct{ lib, main string }{
		"static":  {`import { EmailMessage } from "cloudflare:email"; export const lib = typeof EmailMessage;`, "src/index.ts"},
		"dynamic": {`export const lib = async () => (await import("cloudflare:email")).EmailMessage;`, "src/index.ts"},
		"entry":   {`import { EmailMessage } from "cloudflare:email"; export default { fetch() { return new Response(typeof EmailMessage); } };`, "src/lib.js"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := emailApp(t, c.lib, c.main)
			want := emailstub.Refusal("src/lib.js")
			_, err := Build(Options{Dir: dir, Main: c.main, Outfile: filepath.Join(dir, "go.js")})
			if ie, ok := emailstub.AsImportError(err); !ok || err.Error() != want || len(ie.Importers) != 1 {
				t.Fatalf("Go bundler: %v; want %q", err, want)
			}
			if out, err := buildMJS(t, dir); err == nil || !strings.Contains(out, want) {
				t.Fatalf("build.mjs: %v %s; want %q", err, out, want)
			}
		})
	}
}

// TestEmailImportDependencyStubbed: a dependency's import of
// cloudflare:email (under node_modules) still bundles, with the stub.
func TestEmailImportDependencyStubbed(t *testing.T) {
	dir := emailApp(t, `export const lib = 1;`, "src/index.ts")
	out := filepath.Join(dir, "go.js")
	if _, err := Build(Options{Dir: dir, Main: "src/index.ts", Outfile: out}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), emailstub.Message) || strings.Contains(string(b), `"cloudflare:email"`) {
		t.Fatalf("Go bundle lacks the stub:\n%s", b)
	}
	if o, err := buildMJS(t, dir); err != nil {
		t.Fatalf("build.mjs: %v %s", err, o)
	}
	jb, _ := os.ReadFile(filepath.Join(dir, "js.js"))
	if string(jb) != string(b) {
		t.Fatalf("build.mjs bundle differs from the Go bundle:\n%s", jb)
	}
}
