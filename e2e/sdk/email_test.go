//go:build sdk

package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/emailstub"
	"github.com/rosskukulinski/tailworks/internal/esbundle"
)

// emailCheck: cloudflare:email under the built-in bundler (docs/COMPAT.md
// "Email"). The `agents` SDK imports it at the top of its main module, so
// the cfagents app only builds (and this suite only runs) because a
// dependency's import gets the stub: the bundle carries it. The app's own
// import, here the dynamic import its probe used to make, is refused with
// not_supported naming the file.
func emailCheck(t *testing.T) {
	t.Helper()
	dir := app("cfagents")
	out := filepath.Join(t.TempDir(), "index.js")
	if _, err := esbundle.Build(esbundle.Options{Dir: dir, Main: "src/index.ts", Outfile: out, NodeCompat: true}); err != nil {
		t.Fatalf("cfagents: %v", err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), emailstub.Message) {
		t.Fatalf("the cfagents bundle has no cloudflare:email stub for the agents SDK's import (update docs/COMPAT.md)")
	}
	own := t.TempDir()
	os.WriteFile(filepath.Join(own, "probe.js"), []byte(`export default { async fetch() { const { EmailMessage } = await import("cloudflare:email"); return new Response(typeof EmailMessage); } };`), 0o644)
	_, err := esbundle.Build(esbundle.Options{Dir: own, Main: "probe.js", Outfile: filepath.Join(own, "o.js")})
	if err == nil || err.Error() != emailstub.Refusal("probe.js") {
		t.Fatalf("own import of cloudflare:email: %v; want %q", err, emailstub.Refusal("probe.js"))
	}
}
