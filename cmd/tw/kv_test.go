package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/schema"
)

// TestCLIKV: tw kv namespace create|ls|rm and key put|get|ls|rm (and
// wrangler's list and delete) against a real mgmt: --namespace or
// --binding as a name or a binding of the app's wrangler.jsonc, the app's
// only namespace by default, values from an argument, a file and stdin,
// metadata and TTL, ls with --prefix, and Cloudflare's rules.
func TestCLIKV(t *testing.T) {
	e := newPubEnv(t)
	e.setPolicy(`{}`)
	app := writePlainApp(t)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01",
		"kv_namespaces": [{ "binding": "OAUTH_KV", "id": "oauth" }, { "binding": "CACHE" }]}`), 0o644)
	if out, err := e.call(t, cmdDeploy, app, "--allow-missing-grants", "--no-wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	t.Chdir(app)
	oldIn, oldOut := kvInput, kvOutput
	t.Cleanup(func() { kvInput, kvOutput = oldIn, oldOut })
	var got bytes.Buffer
	kvOutput = func() io.Writer { return &got }
	run := func(args ...string) string {
		t.Helper()
		out, err := e.call(t, cmdKV, args...)
		if err != nil {
			t.Fatalf("tw kv %v: %v\n%s", args, err, out)
		}
		return out
	}
	failing := func(want string, args ...string) {
		t.Helper()
		out, err := e.call(t, cmdKV, args...)
		if err == nil || !strings.Contains(err.Error()+cliText(err)+out, want) {
			t.Fatalf("tw kv %v: %v %s, want %q", args, err, out, want)
		}
	}
	get := func(args ...string) string {
		t.Helper()
		got.Reset()
		run(append([]string{"key", "get"}, args...)...)
		return got.String()
	}

	if out := run("namespace", "create", "oauth"); !strings.Contains(out, "KV namespace oauth created for workload support") {
		t.Fatal(out)
	}
	if out := run("namespace", "create", "oauth"); !strings.Contains(out, "already exists") {
		t.Fatal(out)
	}
	failing("unknown_store", "key", "put", "k", "v", "--binding", "CACHE") // CACHE was never created

	// Values from an argument, a file and stdin.
	run("key", "put", "client:1", `{"name":"app"}`, "--binding", "OAUTH_KV", "--metadata", `{ "kind": "client" }`)
	file := filepath.Join(t.TempDir(), "v")
	os.WriteFile(file, []byte("from a file\n"), 0o644)
	run("key", "put", "grant:1", "--path", file, "--namespace", "oauth", "--ttl", "3600")
	kvInput = func() io.Reader { return strings.NewReader("from stdin") }
	run("key", "put", "token:1", "--binding", "OAUTH_KV")
	if v := get("client:1", "--binding", "OAUTH_KV"); v != `{"name":"app"}` {
		t.Fatalf("get client:1: %q", v)
	}
	if v := get("grant:1", "--namespace", "oauth"); v != "from a file\n" {
		t.Fatalf("get grant:1: %q", v)
	}
	if v := get("token:1", "--binding", "OAUTH_KV"); v != "from stdin" {
		t.Fatalf("get token:1: %q", v)
	}

	// list, --prefix, --json.
	out := run("key", "ls", "--binding", "OAUTH_KV", "--json")
	var res kvResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Keys == nil || len(*res.Keys) != 3 {
		t.Fatalf("list --json: %v %s", err, out)
	}
	var meta map[string]string
	keys := *res.Keys
	json.Unmarshal(keys[0].Metadata, &meta)
	if keys[0].Name != "client:1" || meta["kind"] != "client" {
		t.Fatalf("client:1: %+v %s", keys[0], keys[0].Metadata)
	}
	if x := keys[1].Expiration; x < time.Now().Unix()+3500 {
		t.Fatalf("grant:1 expiration %d", x)
	}
	if out := run("key", "ls", "--namespace", "OAUTH_KV", "--prefix", "tok"); !strings.Contains(out, `"token:1"`) || strings.Contains(out, "client") {
		t.Fatalf("list --prefix: %s", out)
	}
	if out := run("namespace", "ls"); !strings.Contains(out, "oauth") {
		t.Fatalf("namespace list: %s", out)
	}

	// Rules, before anything reaches mgmt.
	failing("at least 60", "key", "put", "k", "v", "--namespace", "oauth", "--ttl", "5")
	failing("--metadata must be JSON", "key", "put", "k", "v", "--namespace", "oauth", "--metadata", "{")
	failing("unknown_store", "key", "get", "k", "--namespace", "NOPE") // neither a binding nor a store: the name itself
	failing("the app has 2 kv_namespaces (OAUTH_KV -> oauth, CACHE -> CACHE): pass --namespace <NAME|BINDING>", "key", "get", "k")
	failing("different namespaces", "key", "get", "k", "--binding", "OAUTH_KV", "--namespace", "oauth")
	failing("flag provided but not defined: -ttl", "key", "get", "client:1", "--namespace", "oauth", "--ttl", "60") // key put's flag
	failing("flag provided but not defined: -namespace", "namespace", "ls", "--namespace", "oauth")
	failing("flag provided but not defined: -yes", "key", "rm", "client:1", "--namespace", "oauth", "--yes")
	// A binding as --namespace, and flags before the verb.
	if v := get("client:1", "--namespace", "OAUTH_KV"); v != `{"name":"app"}` {
		t.Fatalf("get --namespace <binding>: %q", v)
	}
	got.Reset()
	run("--workload", "support", "--namespace", "oauth", "key", "get", "client:1")
	if got.String() != `{"name":"app"}` {
		t.Fatalf("flags before the verb: %q", got.String())
	}

	// The app's only namespace is the default, but only for its own
	// workload: --workload <other> never picks it.
	wr := filepath.Join(app, "wrangler.jsonc")
	both, _ := os.ReadFile(wr)
	os.WriteFile(wr, []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01",
		"kv_namespaces": [{ "binding": "OAUTH_KV", "id": "oauth" }]}`), 0o644)
	if v := get("client:1"); v != `{"name":"app"}` {
		t.Fatalf("get with the app's only namespace: %q", v)
	}
	if v := get("client:1", "--workload", "support"); v != `{"name":"app"}` {
		t.Fatalf("get --workload <the app's>: %q", v)
	}
	failing("key commands for workload other need the namespace: --namespace <NAME>", "key", "rm", "client:1", "--workload", "other")
	os.WriteFile(wr, []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01"}`), 0o644)
	failing("the app has no kv_namespaces", "key", "get", "client:1")
	os.WriteFile(wr, both, 0o644)
	failing("illegal key", "key", "get", "..", "--namespace", "oauth")
	failing("not_found", "key", "get", "nope", "--namespace", "oauth")

	// Every subcommand's --json output matches schema/cli.schema.json.
	checkJSON := func(def string, args ...string) {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(run(append(args, "--json")...)), &v); err != nil {
			t.Fatalf("tw kv %v --json: %v", args, err)
		}
		if ps, err := schema.CheckCLI(def, v); err != nil || len(ps) > 0 {
			t.Fatalf("tw kv %v --json vs $defs/%q: %v %v", args, def, ps, err)
		}
	}
	checkJSON("kv namespace create", "namespace", "create", "scratch")
	checkJSON("kv namespace list", "namespace", "ls")
	checkJSON("kv key put", "key", "put", "j", "v", "--namespace", "scratch", "--metadata", `{"a":1}`, "--ttl", "120")
	checkJSON("kv key get", "key", "get", "j", "--namespace", "scratch")
	checkJSON("kv key list", "key", "ls", "--namespace", "scratch")
	checkJSON("kv key delete", "key", "delete", "j", "--namespace", "scratch")
	checkJSON("kv key list", "key", "list", "--namespace", "scratch") // empty: keys is []
	checkJSON("kv namespace delete", "namespace", "rm", "scratch", "--yes")

	// delete a key, then the namespace.
	run("key", "rm", "token:1", "--binding", "OAUTH_KV")
	failing("not_found", "key", "get", "token:1", "--binding", "OAUTH_KV")
	if out := run("namespace", "delete", "oauth", "--yes"); !strings.Contains(out, "deleted, with its data") {
		t.Fatal(out)
	}
	failing("unknown_store", "key", "ls", "--binding", "OAUTH_KV")
}
