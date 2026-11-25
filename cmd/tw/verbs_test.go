package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

func TestNormalizeVerb(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"ls": "ls", "list": "ls", "rm": "rm", "remove": "rm", "delete": "rm", "del": "rm", "put": "put", "peek": "peek"} {
		if got := normalizeVerb(in); got != want {
			t.Errorf("normalizeVerb(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestScanVerb: the verb is found among flags given before, between and
// after its words, a flag's value is never taken for a word, and the
// last word is normalized.
func TestScanVerb(t *testing.T) {
	t.Parallel()
	reg := func(f *flags) { kvFlags(f, anyVerb) }
	two := func(string) int { return 2 }
	for _, c := range []struct {
		args     []string
		verb     string
		rest     []string
		complete bool
	}{
		{[]string{"key", "get", "k"}, "key get", []string{"k"}, true},
		{[]string{"--workload", "app", "key", "get", "k"}, "key get", []string{"--workload", "app", "k"}, true},
		{[]string{"key", "--namespace", "key", "list", "--json"}, "key ls", []string{"--namespace", "key", "--json"}, true},
		{[]string{"--json", "namespace", "delete", "x", "--yes"}, "namespace rm", []string{"--json", "x", "--yes"}, true},
		{[]string{"--tenant=t", "key", "put", "k", "--", "-v"}, "key put", []string{"--tenant=t", "k", "--", "-v"}, true},
		{[]string{"--state-dir", "/x", "key"}, "key", []string{"--state-dir", "/x"}, false},
		{[]string{"--help"}, "", []string{"--help"}, false},
	} {
		verb, rest, complete := scanVerb(c.args, reg, two)
		if verb != c.verb || !slices.Equal(rest, c.rest) || complete != c.complete {
			t.Errorf("scanVerb(%q) = %q %q %v, want %q %q %v", c.args, verb, rest, complete, c.verb, c.rest, c.complete)
		}
	}
	// d1: two words only after "migrations".
	d1Words := func(first string) int {
		if first == "migrations" {
			return 2
		}
		return 1
	}
	d1Reg := func(f *flags) { d1Flags(f, anyVerb) }
	if verb, rest, ok := scanVerb([]string{"--workload", "w", "migrations", "list", "DB"}, d1Reg, d1Words); verb != "migrations ls" || !ok || !slices.Equal(rest, []string{"--workload", "w", "DB"}) {
		t.Errorf("d1 migrations list: %q %q %v", verb, rest, ok)
	}
	if verb, rest, ok := scanVerb([]string{"execute", "--command", "select 1", "DB"}, d1Reg, d1Words); verb != "execute" || !ok || !slices.Equal(rest, []string{"--command", "select 1", "DB"}) {
		t.Errorf("d1 execute: %q %q %v", verb, rest, ok)
	}
}

// TestDataVerbFlags: each data command's verb lists only its own flags in
// --help (and so in docs/cli.md and completion), and --yes only on the
// destructive ones.
func TestDataVerbFlags(t *testing.T) {
	tr := buildCompletionTree()
	for path, c := range map[string]struct{ has, not []string }{
		"kv":               {[]string{"--tenant", "--workload"}, []string{"--ttl", "--namespace", "--yes", "--prefix"}},
		"kv key get":       {[]string{"--namespace", "--binding", "--workload"}, []string{"--ttl", "--path", "--prefix", "--yes"}},
		"kv key put":       {[]string{"--path", "--ttl", "--expiration", "--metadata", "--namespace"}, []string{"--prefix", "--yes"}},
		"kv key ls":        {[]string{"--prefix", "--namespace"}, []string{"--ttl", "--yes"}},
		"kv namespace rm":  {[]string{"--yes", "--workload"}, []string{"--namespace", "--prefix"}},
		"kv namespace ls":  {[]string{"--workload"}, []string{"--yes", "--namespace"}},
		"d1 execute":       {[]string{"--command", "--file", "--workload"}, []string{"--output", "--skip", "--yes"}},
		"d1 export":        {[]string{"--output"}, []string{"--command", "--yes"}},
		"d1 repair":        {[]string{"--skip", "--yes"}, []string{"--command"}},
		"d1 rm":            {[]string{"--yes"}, []string{"--skip"}},
		"d1 migrations ls": {[]string{"--workload", "--tenant"}, []string{"--yes", "--command"}},
		"queues send":      {[]string{"--text", "--delay", "--tenant"}, []string{"--limit", "--yes"}},
		"queues messages":  {[]string{"--limit"}, []string{"--text", "--yes"}},
		"queues purge":     {[]string{"--yes"}, []string{"--limit"}},
		"queues ls":        {[]string{"--tenant"}, []string{"--yes", "--limit"}},
		"r2 object ls":     {[]string{"--prefix", "--workload"}, []string{"--yes"}},
		"r2 bucket rm":     {[]string{"--yes"}, []string{"--prefix"}},
		"r2 bucket create": {[]string{"--workload"}, []string{"--yes", "--prefix"}},
		"secret put":       {[]string{"--workload", "--tenant"}, []string{"--yes"}},
	} {
		n := tr.nodes[path]
		if n == nil {
			t.Errorf("completion has no path %q", path)
			continue
		}
		for _, fl := range c.has {
			if !n.flags[fl] {
				t.Errorf("tw %s lacks %s (has %v)", path, fl, tr.flagList(path))
			}
		}
		for _, fl := range c.not {
			if n.flags[fl] {
				t.Errorf("tw %s has another verb's flag %s", path, fl)
			}
		}
	}
}

// TestDataFlagsBeforeVerb: --tenant and --workload (flags with a value)
// before the verb reach the verb (and stop at the missing tailworks), for
// each data command.
func TestDataFlagsBeforeVerb(t *testing.T) {
	dir := t.TempDir() + "/never-created"
	t.Chdir(t.TempDir())
	withSecretInput(t, "hunter2-value", false)
	for _, c := range []struct {
		run  func([]string) error
		args []string
	}{
		{cmdD1, []string{"--tenant", "t.example.ts.net", "--workload", "app", "ls"}},
		{cmdD1, []string{"--workload", "app", "migrations", "create", "DB", "init"}}, // no app: no d1_databases entry
		{cmdKV, []string{"--tenant", "t.example.ts.net", "--workload", "app", "namespace", "ls"}},
		{cmdKV, []string{"--workload", "app", "--namespace", "sessions", "key", "get", "k"}},
		{cmdQueues, []string{"--tenant", "t.example.ts.net", "ls"}},
		{cmdQueues, []string{"--limit", "5", "messages", "jobs"}},
		{cmdR2, []string{"--tenant", "t.example.ts.net", "--workload", "app", "bucket", "ls"}},
		{cmdR2, []string{"--workload", "app", "--prefix", "a/", "object", "ls", "photos"}},
		{cmdSecret, []string{"--tenant", "t.example.ts.net", "--workload", "app", "ls"}},
		{cmdSecret, []string{"--tenant", "t.example.ts.net", "--workload", "app", "put", "API_TOKEN"}},
	} {
		err := c.run(append(c.args, "--state-dir", dir))
		text := cliText(err)
		if err == nil || strings.Contains(text, "subcommand") || strings.Contains(text, "invalid value") || strings.Contains(text, "unknown command") {
			t.Errorf("%v: %v", c.args, err)
		}
		if strings.Contains(text, "hunter2") {
			t.Errorf("%v: the error echoes the secret value: %s", c.args, text)
		}
	}
	// A secret value typed where the name goes is never echoed, flags
	// before the verb or not.
	for _, args := range [][]string{
		{"--tenant", "t.example.ts.net", "put", "API_TOKEN", "hunter2-value"},
		{"--workload", "app", "put", "hunter2-value"},
	} {
		if err := cmdSecret(append(args, "--state-dir", dir)); cliCode(err) != "bad_request" || strings.Contains(cliText(err), "hunter2") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// TestStoreName: a declared store's name wins over a binding of that
// name, then a binding, then the name itself.
func TestStoreName(t *testing.T) {
	t.Parallel()
	bs := []bundle.StoreBinding{
		{Kind: "kv", Binding: "OAUTH_KV", Name: "oauth"},
		{Kind: "kv", Binding: "oauth2", Name: "CACHE"},
		{Kind: "kv", Binding: "CACHE", Name: "oauth2"}, // a binding named like another store
	}
	for in, want := range map[string]string{"oauth": "oauth", "OAUTH_KV": "oauth", "CACHE": "CACHE", "oauth2": "oauth2", "other": "other"} {
		if got := storeName(bs, in); got != want {
			t.Errorf("storeName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := storeName(nil, "x"); got != "x" {
		t.Errorf("storeName(nil, x) = %q", got)
	}
	if got := bindingList(bs[:2]); got != "OAUTH_KV -> oauth, oauth2 -> CACHE" {
		t.Errorf("bindingList: %q", got)
	}
}

// TestStoreByBinding: tw r2 and tw d1 take a binding of the app in the
// current directory for the store, resolved before the delete
// confirmation, so it names the real store.
func TestStoreByBinding(t *testing.T) {
	app := writePlainApp(t)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01",
		"r2_buckets": [{ "binding": "PHOTOS", "bucket_name": "photos" }],
		"d1_databases": [{ "binding": "DB", "database_name": "notes" }]}`), 0o644)
	t.Chdir(app)
	oldT := confirmIsTerminal
	t.Cleanup(func() { confirmIsTerminal = oldT })
	confirmIsTerminal = func() bool { return false }
	dir := t.TempDir() + "/never-created"
	for _, c := range []struct {
		run  func([]string) error
		args []string
		want string
	}{
		{cmdR2, []string{"bucket", "rm", "PHOTOS"}, "tw r2 bucket rm photos --workload support --yes"},
		{cmdR2, []string{"bucket", "delete", "photos"}, "tw r2 bucket rm photos --workload support --yes"},
		{cmdR2, []string{"bucket", "rm", "other"}, "tw r2 bucket rm other --workload support --yes"},
		{cmdD1, []string{"rm", "DB"}, "tw d1 rm notes --yes"},
		{cmdD1, []string{"delete", "notes"}, "tw d1 rm notes --yes"},
	} {
		err := c.run(append(c.args, "--state-dir", dir))
		if cliCode(err) != "confirmation_required" || !strings.Contains(cliText(err), c.want) {
			t.Errorf("%v: %v %s, want %q", c.args, err, cliText(err), c.want)
		}
	}
}
