// The e2e/dataplane Workflows workload (#418, ledger/workflows.md). Every
// side effect and every observation goes to the recorder node, so the record
// survives whatever happens to the hosts.
//
//   GET /create?id=&steps=&fail=&sleep=  → WF.create; 200 {id, status}
//   GET /status?id=                      → 200 the instance's status; 404 not found
//   GET /event?id=&v=                    → sendEvent({type: "go"}); 404 not found
//
// Chaos.run(): `steps` step.do's, each one egress call to the recorder
// (POST /wf/do: instance, step, a fresh call ID; the recorder answers with
// how many calls it has seen for that step). Step `fail` throws on its first
// call, so it retries from itself. Then step.sleep, a wait for event "go",
// a wait for an approval, and a final step. Between steps, run() reports
// what it saw (POST /wf/note): those reports repeat on every replay of
// run(), which is how the checkers see stored results coming back.
import { WorkflowEntrypoint } from "cloudflare:workers";

async function rec(env, path, body) {
  const r = await fetch(env.REC + path, { method: "POST", body: JSON.stringify(body) });
  if (!r.ok) throw new Error("recorder " + r.status);
  return r.status === 204 ? {} : r.json();
}

// note is an observation outside any step: best effort.
async function note(env, inst, kind, extra) {
  try {
    await rec(env, "/wf/note", { inst, kind, version: env.VERSION, ...extra });
  } catch {}
}

const STEP = { retries: { limit: 20, delay: 500, backoff: "constant" }, timeout: "20 seconds" };

export class Chaos extends WorkflowEntrypoint {
  async run(event, step) {
    const p = event.payload ?? {};
    const inst = event.instanceId;
    const env = this.env;
    await note(env, inst, "run");
    const out = {};
    for (let i = 0; i < (p.steps ?? 3); i++) {
      const name = "s" + i;
      const r = await step.do(name, STEP, async () => {
        const call = crypto.randomUUID();
        const x = await rec(env, "/wf/do", { inst, step: name, call });
        if (p.fail === i && x.n === 1) throw new Error("planned failure of " + name);
        return { call, v: x.n * 1000 + i };
      });
      out[name] = r;
      await note(env, inst, "seen", { step: name, call: r.call, v: r.v });
    }
    // The sleep's deadline is at least the recorder's clock at pre-sleep + sleep.
    const pre = await step.do("pre-sleep", STEP, async () => {
      const call = crypto.randomUUID();
      const x = await rec(env, "/wf/do", { inst, step: "pre-sleep", call });
      return { call, t0: x.at };
    });
    await note(env, inst, "seen", { step: "pre-sleep", call: pre.call, v: 0 });
    const deadline = pre.t0 + (p.sleepMs ?? 2000);
    await note(env, inst, "sleep", { deadline });
    await step.sleep("nap", p.sleepMs ?? 2000);
    await note(env, inst, "woke", { deadline });

    await note(env, inst, "waiting");
    const ev = await step.waitForEvent("go", { type: "go", timeout: "15 minutes" });
    await note(env, inst, "event", { ev: ev?.payload?.v ?? "" });

    await note(env, inst, "approval-wait");
    // No try/catch: the engine ends a run at a long wait by throwing
    // WorkflowAbortedError, and runs it again when the event comes. A
    // timeout errors the instance (W5 notes it).
    const a = await step.waitForEvent("ok", { type: "approval", timeout: "15 minutes" });
    const pl = a?.payload ?? {};
    const decision = { approved: pl.approved === true, nodeId: pl.approver?.nodeId ?? "", via: pl.approver?.via ?? "", comment: pl.comment ?? "" };
    await note(env, inst, "decided", decision);

    const fin = await step.do("final", STEP, async () => {
      const call = crypto.randomUUID();
      const x = await rec(env, "/wf/do", { inst, step: "final", call });
      return { call, v: x.n * 1000 + 99 };
    });
    await note(env, inst, "seen", { step: "final", call: fin.call, v: fin.v });
    return { out, event: ev?.payload ?? null, decision, final: fin };
  }
}

const notFound = (e) => /not.?found/i.test(String(e?.message ?? e));

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const q = (k) => u.searchParams.get(k);
    try {
      switch (u.pathname) {
        case "/ready":
          return Response.json({ ok: true, version: env.VERSION });
        case "/create": {
          const params = { steps: Number(q("steps") ?? 3), fail: Number(q("fail") ?? -1), sleepMs: Number(q("sleep") ?? 2000) };
          const i = await env.WF.create({ id: q("id"), params });
          return Response.json({ id: i.id, status: await i.status() });
        }
        case "/status": {
          const i = await env.WF.get(q("id"));
          return Response.json(await i.status());
        }
        case "/event": {
          const i = await env.WF.get(q("id"));
          await i.sendEvent({ type: "go", payload: { v: q("v") } });
          return Response.json({ ok: true });
        }
      }
    } catch (e) {
      const m = String(e?.message ?? e).slice(0, 300);
      return Response.json({ error: m }, { status: notFound(e) ? 404 : 500 });
    }
    return new Response("not found", { status: 404 });
  },
};
