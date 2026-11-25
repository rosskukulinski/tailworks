package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// A connects target that is another tailworks app's undefined tag: the
// snippet tw policy prints (and deploy's failure fix) defines it, so pasting
// it isn't rejected for a grant to an undefined tag.
func TestPolicySnippetDefinesConnectsTag(t *testing.T) {
	t.Parallel()
	pf := mgmt.PreflightResult{
		MissingTagOwners:  []string{},
		UndefinedConnects: []string{"tag:tw-wl-mcp"},
		TagOwnersSnippet:  `"tagOwners": { "tag:tw-wl-mcp": ["autogroup:admin", "tag:tw"] }`,
		Missing:           []mgmt.PreflightMissing{{Src: "tag:tw-wl-app", Dst: "tag:tw-wl-mcp", Snippet: `{ "src": ["tag:tw-wl-app"], "dst": ["tag:tw-wl-mcp"], "ip": ["tcp:80"] }`}},
	}
	if s := policySnippet(pf, nil, nil); !strings.Contains(s, `"tag:tw-wl-mcp": ["autogroup:admin", "tag:tw"]`) {
		t.Fatalf("snippet:\n%s", s)
	}
	if fix := strings.Join(preflightFailure(pf).Fix, "\n"); !strings.Contains(fix, `"tag:tw-wl-mcp": ["autogroup:admin", "tag:tw"]`) {
		t.Fatalf("fix:\n%s", fix)
	}
}

// Two of an app's tags missing the same grant: one grant with both srcs,
// in the snippet and in the checks.
func TestPolicySnippetMergesSrcs(t *testing.T) {
	t.Parallel()
	pf := mgmt.PreflightResult{Missing: []mgmt.PreflightMissing{
		{Src: "tag:tw-ai-agent-ai-agent", Dst: "tag:tw-wl-mcp", Snippet: `{ "src": ["tag:tw-ai-agent-ai-agent"], "dst": ["tag:tw-wl-mcp"], "ip": ["tcp:80"] }`},
		{Src: "tag:tw-wl-ai-agent", Dst: "tag:tw-wl-mcp", Snippet: `{ "src": ["tag:tw-wl-ai-agent"], "dst": ["tag:tw-wl-mcp"], "ip": ["tcp:80"] }`},
	}}
	s := policySnippet(pf, nil, nil)
	want := `{ "src": ["tag:tw-ai-agent-ai-agent", "tag:tw-wl-ai-agent"], "dst": ["tag:tw-wl-mcp"], "ip": ["tcp:80"] }`
	if !strings.Contains(s, want) || strings.Count(s, `"dst"`) != 1 {
		t.Fatalf("snippet:\n%s", s)
	}
}

// The ai-agent → mcp case: an app with a workload and an object class that
// connects to an app not deployed yet. --apply defines tag:tw-wl-mcp, writes
// one grant from both tags, and passes, with one report.
func TestCLIPolicyApplyUndeployedConnects(t *testing.T) {
	e := newPubEnv(t)
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "src"), 0o755)
	os.WriteFile(filepath.Join(d, "tailworks.jsonc"), []byte(`{ "name": "ai-agent",
  "tailnet": { "tags": ["tag:tw-wl-ai-agent"], "connects": ["tag:tw-wl-mcp"],
    "objects": { "AiAgent": { "tags": ["tag:tw-ai-agent-ai-agent"] } } } }`), 0o644)
	os.WriteFile(filepath.Join(d, "wrangler.jsonc"), []byte(`{"name": "ai-agent", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "durable_objects": {"bindings": [{"name": "AGENT", "class_name": "AiAgent"}]}, "migrations": [{"tag": "v1", "new_sqlite_classes": ["AiAgent"]}]}`), 0o644)
	os.WriteFile(filepath.Join(d, "src", "index.js"), []byte(`export class AiAgent { fetch() { return new Response("a"); } }
export default { fetch() { return new Response("ok"); } };`), 0o644)
	e.setPolicy(operatorPolicy)

	out, err := e.call(t, cmdPolicy, d, "--apply", "--yes")
	if err != nil {
		t.Fatalf("--apply --yes: %v\n%s", err, out)
	}
	t.Logf("tw policy --apply --yes:\n%s", out)
	for _, want := range []string{
		"✗ tagOwners  tag:tw-wl-mcp: missing (no app with this tag is deployed yet",
		"✗ connects   tag:tw-ai-agent-ai-agent, tag:tw-wl-ai-agent → tag:tw-wl-mcp tcp:80: no grant",
		"tag:tw-ai-agent-ai-agent (the AiAgent objects' nodes)",
		"The policy now has everything tailworks checks",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "won't write") || strings.Count(out, "tw policy: what") != 1 {
		t.Fatalf("output:\n%s", out)
	}
	after, _ := e.policyNow()
	if !strings.Contains(after, `"tag:tw-wl-mcp"`) || strings.Count(after, `"dst": ["tag:tw-wl-mcp"]`)+strings.Count(after, `"dst":["tag:tw-wl-mcp"]`) != 1 {
		t.Fatalf("policy after apply:\n%s", after)
	}
}
