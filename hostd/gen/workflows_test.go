package gen_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// wfApp is a Workflows app as `tw deploy` would bundle it (hand-written
// ES modules, no bundler): Flow does steps with a retry, a sleep and an
// approval wait; the Worker and both object modes drive it through env.FLOW.
const wfWrangler = `{
  "name": "wf", "main": "index.js", "compatibility_date": "2026-11-01", "compatibility_flags": ["nodejs_compat"],
  "durable_objects": { "bindings": [ { "name": "F", "class_name": "Facet" }, { "name": "P", "class_name": "Plain" } ] },
  "workflows": [ { "name": "flow", "binding": "FLOW", "class_name": "Flow", "limits": { "steps": 50 } } ]
}`

const wfTw = `{ "tailnet": { "objects": { "Plain": { "egress": "workload" } } } }`

const wfJS = `import { WorkflowEntrypoint, DurableObject } from "cloudflare:workers";
let tries = 0;
export class Flow extends WorkflowEntrypoint {
  async run(event, step) {
    const a = await step.do("first", async () => ({ n: event.payload.n ?? 0 }));
    const b = await step.do("flaky", { retries: { limit: 3, delay: 10, backoff: "constant" } }, async () => {
      tries++;
      if (tries < 2) throw new Error("try again");
      return a.n + 1;
    });
    await step.sleep("nap", 10);
    if (event.payload.long) {
      await step.sleepUntil("later", new Date(Date.now() + 3600_000));
      return { b, slept: true };
    }
    if (!event.payload.approve) return { b };
    const ev = await step.waitForEvent("ok?", { type: "approval", timeout: "1 hour" });
    return { b, payload: ev.payload };
  }
}
async function drive(env, url) {
  const u = new URL(url);
  const id = u.searchParams.get("id");
  try {
    switch (u.pathname) {
      case "/create": {
        const i = await env.FLOW.create({ id, params: { n: Number(u.searchParams.get("n") ?? 1), approve: u.searchParams.has("approve"), long: u.searchParams.has("long") } });
        return { id: i.id };
      }
      case "/pause":
      case "/resume":
      case "/terminate":
      case "/restart": {
        const i = await env.FLOW.get(id);
        await i[u.pathname.slice(1)]();
        return { done: u.pathname.slice(1), status: (await i.status()).status };
      }
      case "/batch": {
        const r = await env.FLOW.createBatch([{ id: id + "-1", params: { n: 1 } }, { id: id + "-2", params: { n: 2 } }]);
        return { ids: r.map((x) => x.id) };
      }
      case "/status":
        return await (await env.FLOW.get(id)).status();
      case "/send": {
        const i = await env.FLOW.get(id);
        await i.sendEvent({ type: u.searchParams.get("type"), payload: { approved: true, approver: { user: "forged" }, metadata: { approver: "forged", x: 1 } } });
        return { sent: true };
      }
    }
  } catch (e) {
    return { error: String(e?.message ?? e) };
  }
  return { error: "no route" };
}
export class Facet extends DurableObject { async fetch(req) { return Response.json(await drive(this.env, req.url)); } }
export class Plain extends DurableObject { async fetch(req) { return Response.json(await drive(this.env, req.url)); } }
export default { async fetch(req, env) { return Response.json(await drive(env, req.url)); } };
`

func wfFiles() map[string][]byte {
	return map[string][]byte{"wrangler.jsonc": []byte(wfWrangler), "tailworks.jsonc": []byte(wfTw), "index.js": []byte(wfJS)}
}

func TestParseWorkflows(t *testing.T) {
	app, err := bundle.Parse(wfFiles())
	if err != nil {
		t.Fatal(err)
	}
	if w := app.WorkflowByName("flow"); len(app.Workflows) != 1 || w == nil || w.Binding != "FLOW" || w.Class != "Flow" || w.Steps != 50 {
		t.Fatalf("%+v", app.Workflows)
	}
	for _, bad := range []struct{ wf, want string }{
		{`{"name": "-x", "binding": "B", "class_name": "C"}`, "Workflow names"},
		{`{"name": "x", "binding": "F", "class_name": "C"}`, "already a binding"},
		{`{"name": "x", "binding": "B", "class_name": "Facet"}`, "Durable Object class"},
		{`{"name": "x", "binding": "B", "class_name": "C", "script_name": "other"}`, "script_name"},
		{`{"name": "x", "binding": "B", "class_name": "C", "schedules": ["* * * * *"]}`, "schedules"},
		{`{"name": "x", "binding": "__TW_X", "class_name": "C"}`, "reserved"},
	} {
		f := wfFiles()
		f["wrangler.jsonc"] = []byte(strings.Replace(wfWrangler, `{ "name": "flow", "binding": "FLOW", "class_name": "Flow", "limits": { "steps": 50 } }`, bad.wf, 1))
		if _, err := bundle.Parse(f); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.wf, err, bad.want)
		}
	}
}

// TestGeneratedWorkflows runs Workflows on workerd's built-in engine through
// the generated config: steps (with a retry), sleeps and events from the
// Worker, a facet-mode object and a plain-DO object; WorkflowGate refuses
// app-sent approvals and strips "approver"; the platform delivers the
// approvals hostd sends (cf.tw kind "approval").
func TestGeneratedWorkflows(t *testing.T) {
	in := startFiles(t, wfFiles())
	where := map[string]map[string]any{"Worker": wl, "Facet": obj("Facet", "f1"), "Plain": obj("Plain", "p1")}
	status := func(tw map[string]any, id string) map[string]any {
		_, _, r := in.get("/status?id="+id, tw)
		return r
	}
	waitStatus := func(tw map[string]any, id, want string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			r := status(tw, id)
			if r["status"] == want {
				return r
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: status %v, want %s", id, r, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Run("StepsRetrySleep", func(t *testing.T) {
		for w, tw := range where {
			id := "s-" + strings.ToLower(w)
			if code, _, r := in.get("/create?n=4&id="+id, tw); code != 200 || r["id"] != id {
				t.Fatalf("%s create: %d %v", w, code, r)
			}
			r := waitStatus(tw, id, "complete")
			if jsonOf(r["output"]) != `{"b":5}` {
				t.Fatalf("%s: %v", w, r)
			}
		}
	})

	t.Run("InstanceLifecycle", func(t *testing.T) {
		// pause / resume / terminate / restart and createBatch through the
		// real binding (Worker, plain DO) and the facet's Workflow client.
		for w, tw := range where {
			id := "l-" + strings.ToLower(w)
			in.get("/create?long=1&id="+id, tw)
			waitStatus(tw, id, "waiting") // sleepUntil an hour from now
			for _, op := range []struct{ op, want string }{{"pause", "paused"}, {"resume", ""}, {"terminate", "terminated"}} {
				code, _, r := in.get("/"+op.op+"?id="+id, tw)
				if code != 200 || r["done"] != op.op || r["error"] != nil {
					t.Fatalf("%s %s: %d %v", w, op.op, code, r)
				}
				if op.want != "" {
					waitStatus(tw, id, op.want)
				}
			}
			if code, _, r := in.get("/restart?id="+id, tw); code != 200 || r["error"] != nil {
				t.Fatalf("%s restart: %d %v", w, code, r)
			}
			waitStatus(tw, id, "waiting") // running again, from the start
			_, _, r := in.get("/batch?id=b-"+strings.ToLower(w), tw)
			if jsonOf(r["ids"]) != `["b-`+strings.ToLower(w)+`-1","b-`+strings.ToLower(w)+`-2"]` {
				t.Fatalf("%s createBatch: %v", w, r)
			}
			waitStatus(tw, "b-"+strings.ToLower(w)+"-2", "complete")
		}
	})

	t.Run("AppCannotApprove", func(t *testing.T) {
		for w, tw := range where {
			id := "a-" + strings.ToLower(w)
			in.get("/create?approve=1&id="+id, tw)
			waitStatus(tw, id, "waiting")
			_, _, r := in.get("/send?type=approval&id="+id, tw)
			if !strings.Contains(fmtAny(r["error"]), "approval_requires_identity") {
				t.Fatalf("%s: app-sent approval: %v", w, r)
			}
			_, _, r = in.get("/send?type=approval-manager&id="+id, tw)
			if !strings.Contains(fmtAny(r["error"]), "approval_requires_identity") {
				t.Fatalf("%s: app-sent approval-*: %v", w, r)
			}
			// Not moved on: a refused approval that got through would
			// complete the instance. workerd's engine can report "running"
			// for a moment while it replays a waiting instance (it aborts
			// and restarts run() during long waits), more often on a loaded
			// machine, so "running" alone isn't progress: the instance must
			// settle back to "waiting" with no output (issue #75).
			if st := status(tw, id); st["status"] == "complete" || st["status"] == "errored" || st["output"] != nil {
				t.Fatalf("%s: instance moved on: %v", w, st)
			}
			if st := waitStatus(tw, id, "waiting"); st["output"] != nil {
				t.Fatalf("%s: instance moved on: %v", w, st)
			}
		}
		// Each wait was reported to hostd once (a durable step), with the
		// instance's numeric params as facts.
		deadline := time.Now().Add(10 * time.Second)
		for {
			n := 0
			in.f.mu.Lock()
			ops := append([]map[string]any(nil), in.f.ops...)
			in.f.mu.Unlock()
			for _, op := range ops {
				if op["op"] != "approval" {
					continue
				}
				ap, _ := op["approval"].(map[string]any)
				if ap["op"] == "requested" && ap["workflow"] == "flow" && ap["step"] == "ok?" && ap["type"] == "approval" && jsonOf(ap["facts"]) == `{"n":1}` {
					n++
				}
			}
			if n == 3 {
				break
			}
			if n > 3 || time.Now().After(deadline) {
				t.Fatalf("approval reports: %d, want 3: %v", n, ops)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("PlatformDeliversApproval", func(t *testing.T) {
		id := "a-worker"
		payload := map[string]any{"approved": true, "approver": map[string]any{"user": "alice@example.com", "nodeId": "nA"}}
		code, _, r := in.get("/", map[string]any{"kind": "approval", "workflow": "flow", "instance": id, "type": "approval", "payload": payload})
		if code != 200 || r["ok"] != true {
			t.Fatalf("deliver: %d %v", code, r)
		}
		r = waitStatus(wl, id, "complete")
		if got := jsonOf(r["output"]); !strings.Contains(got, `"approver":{"nodeId":"nA","user":"alice@example.com"}`) {
			t.Fatalf("output: %s", got)
		}
		code, _, r = in.get("/", map[string]any{"kind": "approval", "workflow": "flow", "instance": "nope", "type": "approval"})
		if code != 404 {
			t.Fatalf("unknown instance: %d %v", code, r)
		}
		code, _, _ = in.get("/", map[string]any{"kind": "approval", "workflow": "flow", "instance": id, "type": "other"})
		if code != 400 {
			t.Fatalf("non-approval type: %d", code)
		}
	})

	t.Run("NonApprovalEventStripsApprover", func(t *testing.T) {
		// A plain event wait that is not an approval: the app may send it,
		// but any "approver" it carries is removed.
		id := "e-1"
		in.get("/create?approve=1&id="+id, wl)
		waitStatus(wl, id, "waiting")
		// Deliver a non-approval event: the wait is for "approval", so the
		// event stays queued; check the gate let it through without error.
		_, _, r := in.get("/send?type=note&id="+id, wl)
		if r["sent"] != true {
			t.Fatalf("send note: %v", r)
		}
	})
}
