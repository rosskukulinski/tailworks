package gen_test

import (
	"strings"
	"testing"
	"time"
)

// TestCtxAbortRecreates (#395): after a Durable Object calls
// ctx.abort(reason), the next request (fetch or RPC) and the next alarm
// run on a fresh instance with its storage intact, as on Cloudflare, in
// facet and plain-DO mode. Only the request that was running when the
// object aborted fails. A killed object still stays dead.
func TestCtxAbortRecreates(t *testing.T) {
	const js = `import { DurableObject } from "cloudflare:workers";
class Base extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.inst = crypto.randomUUID();
  }
  hello() { return { inst: this.inst, n: this.ctx.storage.kv.get("n") ?? 0, alarmed: this.ctx.storage.kv.get("alarmed") ?? null }; }
  async alarm() { this.ctx.storage.kv.put("alarmed", this.inst); }
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname === "/abort") {
      this.ctx.storage.kv.put("n", (this.ctx.storage.kv.get("n") ?? 0) + 1);
      // Well after the abort below (50 ms), even on a loaded runner: an alarm
      // that beat the abort would run on the old instance.
      if (u.searchParams.get("alarm")) await this.ctx.storage.setAlarm(Date.now() + 1500);
      await this.ctx.storage.sync(); // abort drops writes not yet committed
      if (u.searchParams.get("later")) {
        // As the Agents SDK lifecycle example does: abort after answering.
        setTimeout(() => this.ctx.abort("demo restart"), 50);
        return Response.json(this.hello());
      }
      this.ctx.abort("demo restart");
      return Response.json({ unreachable: true });
    }
    return Response.json(this.hello());
  }
}
export class Facet extends Base {}
export class Plain extends Base {}
export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const ns = env[u.searchParams.get("to")];
    try {
      return Response.json(await ns.getByName(u.searchParams.get("n")).hello());
    } catch (e) {
      return Response.json({ err: String(e?.message ?? e) }, { status: 500 });
    }
  },
};
`
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "ctxabort", "main": "index.js", "compatibility_date": "2026-04-01",
  "durable_objects": {"bindings": [{"name": "FACET", "class_name": "Facet"}, {"name": "PLAIN", "class_name": "Plain"}]}}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"objects": {"Plain": {"egress": "workload"}}}}`),
		"index.js":        []byte(js),
	}
	in := startFiles(t, files)

	for _, c := range []struct{ cls, ns string }{{"Facet", "FACET"}, {"Plain", "PLAIN"}} {
		fetchHello := func(t *testing.T, name string) map[string]any {
			t.Helper()
			code, _, r := in.get("/", obj(c.cls, name))
			if code != 200 || r["inst"] == nil {
				t.Fatalf("fetch %s/%s: %d %v", c.cls, name, code, r)
			}
			return r
		}
		rpcHello := func(t *testing.T, name string) map[string]any {
			t.Helper()
			code, _, r := in.get("/?to="+c.ns+"&n="+name, wl)
			if code != 200 || r["inst"] == nil {
				t.Fatalf("rpc %s/%s: %d %v", c.cls, name, code, r)
			}
			return r
		}
		t.Run(c.cls+"/Now", func(t *testing.T) {
			name := "now-" + strings.ToLower(c.cls)
			prev := fetchHello(t, name)
			for i := 1; i <= 2; i++ {
				// The request that aborts fails with the abort reason ...
				if code, _, r := in.get("/abort", obj(c.cls, name)); code == 200 {
					t.Fatalf("abort %d answered: %v", i, r)
				}
				// ... and the next one, fetch or RPC, gets a new instance
				// with the storage the aborted one committed.
				var r map[string]any
				if i == 1 {
					r = fetchHello(t, name)
					if rr := rpcHello(t, name); rr["inst"] != r["inst"] {
						t.Fatalf("rpc after abort reached another instance: %v vs %v", rr, r)
					}
				} else {
					r = rpcHello(t, name)
				}
				if r["inst"] == prev["inst"] || r["n"] != float64(i) {
					t.Fatalf("after abort %d: %v (before: %v)", i, r, prev)
				}
				prev = r
			}
		})
		t.Run(c.cls+"/AfterResponse", func(t *testing.T) {
			name := "later-" + strings.ToLower(c.cls)
			prev := fetchHello(t, name)
			for i := 1; i <= 2; i++ {
				code, _, r := in.get("/abort?later=1", obj(c.cls, name))
				if code != 200 || r["inst"] != prev["inst"] {
					t.Fatalf("abort %d: %d %v", i, code, r)
				}
				time.Sleep(200 * time.Millisecond) // the object has aborted
				if i == 1 {
					r = fetchHello(t, name)
				} else {
					r = rpcHello(t, name)
				}
				if r["inst"] == prev["inst"] || r["n"] != float64(i) {
					t.Fatalf("after abort %d: %v (before: %v)", i, r, prev)
				}
				prev = r
			}
		})
		t.Run(c.cls+"/Alarm", func(t *testing.T) {
			name := "alarm-" + strings.ToLower(c.cls)
			prev := fetchHello(t, name)
			if code, _, r := in.get("/abort?later=1&alarm=1", obj(c.cls, name)); code != 200 {
				t.Fatalf("abort: %d %v", code, r)
			}
			// The alarm set before the abort fires on a new instance.
			deadline := time.Now().Add(5 * time.Second)
			for {
				r := fetchHello(t, name)
				if a, _ := r["alarmed"].(string); a != "" {
					if a == prev["inst"] || a != r["inst"] {
						t.Fatalf("alarm ran on %v, object is %v (before abort %v)", a, r["inst"], prev["inst"])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("alarm never ran after the abort: %v", r)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}

	// Kill is unchanged: a killed object stays dead, including one that
	// aborted itself before.
	t.Run("KillStaysDead", func(t *testing.T) {
		for _, c := range []string{"Facet", "Plain"} {
			name := "now-" + strings.ToLower(c) // aborted twice above
			if code, _, r := in.get("/", map[string]any{"kind": "admin", "op": "kill", "class": c, "name": name, "reason": "test"}); code != 200 {
				t.Fatalf("%s kill: %d %v", c, code, r)
			}
			for i := 0; i < 3; i++ {
				code, h, r := in.get("/", obj(c, name))
				if code != 410 || h.Get("tw-error") != "killed" {
					t.Fatalf("%s after kill: %d %v", c, code, r)
				}
			}
			if code, _, r := in.get("/?to="+strings.ToUpper(c)+"&n="+name, wl); code == 200 {
				t.Fatalf("%s: RPC to a killed object succeeded: %v", c, r)
			}
		}
	})
}
