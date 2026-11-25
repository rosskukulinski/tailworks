package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/internal/emailstub"
)

// TestEmailImportUnbundled (issue #354): a pre-built Worker (no_bundle, as a
// Vite build leaves it) that imports cloudflare:email starts, in the Worker,
// a facet object and a plain-DO object. No bundler stubbed the import:
// workerd's own cloudflare:email finds its internal module in the config's
// extension, and the facets' Dynamic Worker has a cloudflare:email module.
// Constructing an EmailMessage throws the bundler stub's error.
func TestEmailImportUnbundled(t *testing.T) {
	const js = `import { DurableObject } from "cloudflare:workers";
import { EmailMessage } from "cloudflare:email";
const probe = () => {
  let error = "";
  try { new EmailMessage("a@example.com", "b@example.com", "raw"); } catch (e) { error = String(e.message); }
  return Response.json({ type: typeof EmailMessage, error });
};
export class Facet extends DurableObject { fetch() { return probe(); } }
export class Plain extends DurableObject { fetch() { return probe(); } }
export default { fetch() { return probe(); } };
`
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "repro-email", "main": "index.js", "compatibility_date": "2026-06-11", "no_bundle": true,
  "rules": [{"type": "ESModule", "globs": ["**/*.js"]}],
  "durable_objects": {"bindings": [{"name": "FACET", "class_name": "Facet"}, {"name": "PLAIN", "class_name": "Plain"}]}}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"objects": {"Plain": {"egress": "workload"}}}}`),
		"index.js":        []byte(js),
	}
	in := startFiles(t, files)
	for w, tw := range map[string]map[string]any{"Worker": wl, "Facet": obj("Facet", "e1"), "Plain": obj("Plain", "e2")} {
		code, _, r := in.get("/", tw)
		if code != 200 || r["type"] != "function" || r["error"] != emailstub.Message {
			t.Fatalf("%s: %d %v", w, code, r)
		}
	}
}

// TestGenerateEmailExtension: the tenant service's stub is an internal
// module (tenant code can't import cloudflare-internal:email by name); the
// facets' Dynamic Worker carries cloudflare:email itself.
func TestGenerateEmailExtension(t *testing.T) {
	app := &bundle.App{
		Name: "w", Main: "index.js", CompatDate: "2026-04-01",
		Modules: map[string][]byte{"index.js": []byte(`export default { fetch() { return new Response("ok"); } };`)},
		Classes: map[string]*bundle.Class{},
	}
	dir := filepath.Join(t.TempDir(), "i")
	if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
	want := `extensions = [ (modules = [ (name = "cloudflare-internal:email", esModule = embed "tw-email.js", internal = true) ]) ]`
	if !strings.Contains(string(cfg), want) {
		t.Errorf("config.capnp lacks %s", want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tw-email.js")); string(b) != emailstub.InternalModuleJS {
		t.Errorf("tw-email.js = %q", b)
	}
	if tm, _ := os.ReadFile(filepath.Join(dir, "tw-modules.js")); !strings.Contains(string(tm), `"cloudflare:email": { js: `) {
		t.Errorf("tw-modules.js has no cloudflare:email stub for the facets:\n%s", tm)
	}
}
