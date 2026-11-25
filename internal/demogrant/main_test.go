package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

var testSpec = spec{
	CallerSrc:   "autogroup:member",
	AgentWLTag:  "tag:tw-wl-demo-agent",
	AgentTag:    "tag:tw-demo-agent-support-agent",
	MCPTag:      "tag:tw-wl-demo",
	Cap:         "example.com/cap/mcp",
	FullTools:   []string{"lookup_order", "refund"},
	NarrowTools: []string{"lookup_order"},
}

const basePolicy = `{
	// the operator's own rules stay as they are
	"groups": {"group:ops": ["alice@example.com"]},
	"grants": [
		{"src": ["group:ops"], "dst": ["tag:db"], "ip": ["tcp:5432"]}, // ops reach the database
	],
}`

func grantsOf(t *testing.T, pol []byte) []any {
	t.Helper()
	std, err := hujson.Standardize(append([]byte(nil), pol...)) // in place otherwise
	if err != nil {
		t.Fatalf("not HuJSON: %v\n%s", err, pol)
	}
	var doc struct {
		Grants []any `json:"grants"`
	}
	if err := json.Unmarshal(std, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Grants
}

func mustEdit(t *testing.T, pol []byte, op string) ([]byte, []string) {
	t.Helper()
	out, diff, err := edit(pol, testSpec, op)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out, diff
}

// The whole life of the demo's grants: added once, narrowed, restored,
// removed, with the operator's rules and comments untouched throughout.
func TestEditLifecycle(t *testing.T) {
	pol, diff := mustEdit(t, []byte(basePolicy), "ensure")
	if len(diff) != 2 || len(grantsOf(t, pol)) != 3 {
		t.Fatalf("ensure: diff %v\n%s", diff, pol)
	}
	if again, diff := mustEdit(t, pol, "ensure"); len(diff) != 0 || string(again) != string(pol) {
		t.Fatalf("ensure twice changed the policy: %v", diff)
	}

	narrow, diff := mustEdit(t, pol, "narrow")
	if len(diff) != 2 || !strings.HasPrefix(diff[0], "- ") || !strings.HasPrefix(diff[1], "+ ") {
		t.Fatalf("narrow: diff %v", diff)
	}
	if !sameJSON(grantsOf(t, narrow)[2], testSpec.toolGrant(testSpec.NarrowTools)) {
		t.Fatalf("narrow: tool grant is %s", pretty(grantsOf(t, narrow)[2]))
	}
	if _, diff := mustEdit(t, narrow, "narrow"); len(diff) != 0 {
		t.Fatalf("narrow twice: %v", diff)
	}

	// ensure also restores a grant left narrowed by a run that died.
	for _, op := range []string{"restore", "ensure"} {
		back, _ := mustEdit(t, narrow, op)
		if !sameJSON(grantsOf(t, back), grantsOf(t, pol)) {
			t.Fatalf("%s did not put the grants back:\n%s", op, back)
		}
	}

	for _, from := range [][]byte{pol, narrow} {
		gone, diff := mustEdit(t, from, "remove")
		if len(diff) != 2 || !sameJSON(grantsOf(t, gone), grantsOf(t, []byte(basePolicy))) {
			t.Fatalf("remove: diff %v\n%s", diff, gone)
		}
		for _, keep := range []string{"the operator's own rules stay as they are", "ops reach the database", "group:ops"} {
			if !strings.Contains(string(gone), keep) {
				t.Fatalf("remove lost %q:\n%s", keep, gone)
			}
		}
	}
	if _, diff := mustEdit(t, []byte(basePolicy), "remove"); len(diff) != 0 {
		t.Fatalf("remove with nothing to remove: %v", diff)
	}
}

func TestEditNoGrantsKey(t *testing.T) {
	pol, _ := mustEdit(t, []byte(`{"tagOwners": {"tag:tw": []}}`), "ensure")
	if len(grantsOf(t, pol)) != 2 {
		t.Fatalf("ensure on a policy without grants:\n%s", pol)
	}
}

// A grant between the demo's tags that someone changed is not the demo's
// to rewrite or delete.
func TestEditRefusesForeignGrant(t *testing.T) {
	pol := []byte(`{"grants": [
		{"src": ["tag:tw-demo-agent-support-agent"], "dst": ["tag:tw-wl-demo"], "ip": ["tcp:80", "tcp:443"],
		 "app": {"example.com/cap/mcp": [{"tools": ["lookup_order", "refund"]}]}},
	]}`)
	if _, _, err := edit(pol, testSpec, "remove"); err == nil {
		t.Fatal("remove deleted a grant the demo did not write")
	}
	two := []byte(`{"grants": [
		{"src": ["tag:tw-demo-agent-support-agent"], "dst": ["tag:tw-wl-demo"], "ip": ["tcp:80"],
		 "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}, {"tools": ["refund"]}]}},
	]}`)
	if _, _, err := edit(two, testSpec, "narrow"); err == nil {
		t.Fatal("narrow rewrote a grant with two capability entries")
	}
	if _, _, err := edit([]byte(basePolicy), testSpec, "narrow"); err == nil {
		t.Fatal("narrow without the grant should say to run ensure")
	}
}

func TestSpecCheck(t *testing.T) {
	if err := testSpec.check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []spec{
		{CallerSrc: "autogroup:member", AgentWLTag: "tag:prod", AgentTag: testSpec.AgentTag, MCPTag: testSpec.MCPTag},
		{CallerSrc: "autogroup:member", AgentWLTag: testSpec.AgentWLTag, AgentTag: testSpec.AgentTag, MCPTag: "tag:tw"},
		{CallerSrc: "*", AgentWLTag: testSpec.AgentWLTag, AgentTag: testSpec.AgentTag, MCPTag: testSpec.MCPTag},
	} {
		if bad.check() == nil {
			t.Errorf("check accepted %+v", bad)
		}
	}
}
