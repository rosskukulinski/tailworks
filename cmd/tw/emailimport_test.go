package main

import (
	"strings"
	"testing"
)

// TestEmailImportRefused: tw's built-in bundler refuses the app's own
// import of cloudflare:email with not_supported, naming the file; a
// pre-built (no_bundle) Worker that imports it still packs, since its
// importer can't be told apart (the workerd config stubs it, hostd/gen).
func TestEmailImportRefused(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc": `{"name":"mail","main":"src/index.js","compatibility_date":"2026-06-11"}`,
		"src/index.js":   `import { EmailMessage } from "cloudflare:email"; export default { fetch() { return new Response(typeof EmailMessage); } };`,
	})
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.build(nil)
	want := "cloudflare:email is not supported on tailworks yet (imported by src/index.js)"
	if cliCode(err) != "not_supported" || asCLIErr(err).Message != want {
		t.Fatalf("build: %v; want not_supported: %s", err, want)
	}
	if fix := strings.Join(asCLIErr(err).Fix, "\n"); !strings.Contains(fix, "remove the cloudflare:email import") {
		t.Fatalf("fix: %s", fix)
	}

	pre := t.TempDir()
	writeFiles(t, pre, map[string]string{
		"wrangler.jsonc": `{"name":"mail","main":"dist/index.js","compatibility_date":"2026-06-11","no_bundle":true,"rules":[{"type":"ESModule","globs":["**/*.js"]}]}`,
		"dist/index.js":  `import { EmailMessage } from "cloudflare:email"; export default { fetch() { return new Response(typeof EmailMessage); } };`,
	})
	if a, err = loadApp(pre, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.build(nil); err != nil {
		t.Fatalf("pre-built Worker: %v", err)
	}
}
