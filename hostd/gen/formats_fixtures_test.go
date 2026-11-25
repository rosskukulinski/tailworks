package gen_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The JS half of the persisted-format fixtures (ADR-041
// §4): the platform store kinds in js/ (kv.js, store-r2.js, store-d1.js)
// decode or replay every committed fixture, in node with the workerd-only
// imports stubbed. The R2 and D1 fixtures are entries exactly as those
// modules wrote them; KV shares internal/kvwire's (Go and JS must agree).

const jsHooks = `
const stub = (src) => ({ url: "data:text/javascript," + encodeURIComponent(src), shortCircuit: true });
export async function resolve(spec, ctx, next) {
  if (spec === "cloudflare:workers") return stub("export class WorkerEntrypoint {}; export class DurableObject {};");
  if (spec === "tw-store.js")
    return stub(` + "`" + `export function registerStoreKind(k, d) { (globalThis.__kinds ??= {})[k] = d; }
export const utf8 = (u) => new TextDecoder().decode(u);
export const toBytes = (x) => (x instanceof Uint8Array ? x : new Uint8Array(x));
export function storeStub() {}
export class StoreError extends Error {}` + "`" + `);
  return next(spec, ctx);
}
`

const jsRun = `
import { register } from "node:module";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
const [hooks, js, kvDir, fx] = process.argv.slice(2);
register(pathToFileURL(hooks));
const kv = await import(pathToFileURL(js + "/kv.js"));
await import(pathToFileURL(js + "/store-r2.js"));
const d1 = await import(pathToFileURL(js + "/store-d1.js"));
const te = new TextEncoder();
const err = (f) => { try { f(); return ""; } catch (e) { return String(e.message); } };
const out = { kv: {}, r2: {}, d1: {} };
for (const n of ["kv-entry-v1-put.bin", "kv-entry-v1-del.bin"]) {
  const e = kv.decodeEntry(new Uint8Array(readFileSync(kvDir + "/" + n)));
  out.kv[n] = { h: e.h, v: new TextDecoder().decode(e.v) };
}
const put = new Uint8Array(readFileSync(kvDir + "/kv-entry-v1-put.bin"));
const v2 = put.slice(); v2[0] = 2;
out.kv.v2 = err(() => kv.decodeEntry(v2));
const r2 = globalThis.__kinds.r2;
for (const n of ["r2-index-v0.jsonl", "r2-index-v1.jsonl"]) {
  const calls = [];
  const s = { sql: { exec: (q, ...a) => { calls.push([q.split(" ").slice(0, 3).join(" "), ...a]); return []; } } };
  for (const line of readFileSync(fx + "/" + n, "utf8").split("\n").filter(Boolean)) r2.apply(s, te.encode(line));
  out.r2[n] = calls;
}
const s0 = { sql: { exec: () => [] } };
out.r2.v2 = err(() => r2.apply(s0, te.encode('{"v":2,"o":"put","k":"x","row":{}}')));
out.r2.unknownOp = err(() => r2.apply(s0, te.encode('{"v":1,"o":"compact"}')));
const e = d1.decodeEntry(te.encode(readFileSync(fx + "/d1-entry-v1.json", "utf8")));
out.d1.v1 = { id: e.id, stmts: e.stmts.map((x) => ({ sql: x.sql, params: x.params.map((p) => (p instanceof Uint8Array ? "bytes:" + new TextDecoder().decode(p) : p)), empty: x.empty })) };
out.d1.v2 = err(() => d1.decodeEntry(te.encode('{"v":2,"id":"x","q":[]}')));
const d1k = globalThis.__kinds.d1;
const ds = { name: "db", d1Want: new Map(), d1Out: new Map() };
out.d1.v2apply = err(() => d1k.apply(ds, te.encode('{"v":2,"id":"x","q":[]}'), 9));
console.log(JSON.stringify(out));
`

func TestJSFormatFixtures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("needs node")
	}
	dir := t.TempDir()
	hooks, run := filepath.Join(dir, "hooks.mjs"), filepath.Join(dir, "run.mjs")
	os.WriteFile(hooks, []byte(jsHooks), 0o644)
	os.WriteFile(run, []byte(jsRun), 0o644)
	js, _ := filepath.Abs("js")
	kvDir, _ := filepath.Abs("../../internal/kvwire/testdata/formats")
	fx, _ := filepath.Abs("testdata/formats")
	cmd := exec.Command(node, "--no-warnings", run, hooks, js, kvDir, fx)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s%s", err, b, stderr.String())
	}
	var out struct {
		KV map[string]json.RawMessage `json:"kv"`
		R2 map[string]json.RawMessage `json:"r2"`
		D1 map[string]json.RawMessage `json:"d1"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	eq := func(what string, got json.RawMessage, want string) {
		t.Helper()
		if string(got) != want {
			t.Errorf("%s:\n got %s\nwant %s", what, got, want)
		}
	}
	eq("kv put", out.KV["kv-entry-v1-put.bin"], `{"h":{"k":"greeting","x":1893456000,"m":"{\"lang\":\"en\"}"},"v":"hello"}`)
	eq("kv del", out.KV["kv-entry-v1-del.bin"], `{"h":{"k":"greeting","d":true},"v":""}`)
	if !strings.Contains(string(out.KV["v2"]), "newer tailworks") {
		t.Errorf("kv v2: %s", out.KV["v2"])
	}
	// v0 (no v) and v1 replay to the same index operations.
	if string(out.R2["r2-index-v0.jsonl"]) != string(out.R2["r2-index-v1.jsonl"]) || len(out.R2["r2-index-v1.jsonl"]) < 100 {
		t.Errorf("r2 v0 and v1 replay differently:\nv0 %s\nv1 %s", out.R2["r2-index-v0.jsonl"], out.R2["r2-index-v1.jsonl"])
	}
	for _, want := range []string{`"docs/hello.txt","7f3a",5`, `"INSERT OR REPLACE","u1","big.bin"`, `"u1",1,"b2",5242880`, `"DELETE FROM r2_uploads","u1"`, `"DELETE FROM r2_objects","docs/hello.txt"`} {
		if !strings.Contains(string(out.R2["r2-index-v1.jsonl"]), want) {
			t.Errorf("r2 replay lacks %s: %s", want, out.R2["r2-index-v1.jsonl"])
		}
	}
	for _, k := range []string{"v2", "unknownOp"} {
		if !strings.Contains(string(out.R2[k]), "newer tailworks") {
			t.Errorf("r2 %s: %s", k, out.R2[k])
		}
	}
	eq("d1 v1", out.D1["v1"], `{"id":"7c9e6679-7425-40de-944b-e07fc1f90ae7","stmts":[{"sql":"INSERT INTO t (a, b) VALUES (?, ?)","params":[1,"bytes:hi"],"empty":false},{"sql":"SELECT 1","params":[],"empty":false}]}`)
	for _, k := range []string{"v2", "v2apply"} {
		if !strings.Contains(string(out.D1[k]), "newer tailworks") {
			t.Errorf("d1 %s (a newer entry must not be skipped): %s", k, out.D1[k])
		}
	}
}
