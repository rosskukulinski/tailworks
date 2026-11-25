// Refund approvals on tailworks: an Agent starts a durable Workflow per refund;
// the Workflow waits for a person to approve it. On tailworks an approval is a
// decision by a tailnet identity:
//
//   - only tailworks delivers it (app code cannot approve: approveWorkflow() is
//     refused), from `tw approve`, the admin UI, or POST
//     /_tw/approvals/refund/<id> on this workload's node (the page at /);
//   - tailworks checks the approver's github.com/rosskukulinski/tailworks/cap/approve grant first (for
//     example: anyone in group:support up to 100, group:finance any amount);
//   - the Workflow sees who approved (approval.approver: user, node, tags),
//     verified over WireGuard, and can add its own rule (here: not the
//     person who asked for the refund).
//
// See docs/approvals.md for the policy grants.
import { Agent, routeAgentRequest } from "agents";
import { AgentWorkflow, type AgentWorkflowEvent, type AgentWorkflowStep } from "agents/workflows";
import { NonRetryableError } from "cloudflare:workflows";
import { getCaller } from "@tailworks/tailnet";

// Env (the RefundAgent namespace and REFUND_WORKFLOW) is the global
// interface `tw types` writes to worker-configuration.d.ts. Run `tw types`
// again after changing wrangler.jsonc or tailworks.jsonc.

type RefundParams = { order: string; amount: number; requester: string };

export class RefundAgent extends Agent<Env> {
  private app?: string;

  // appName is this workload's name (ctx.tailnet.self().workload), for the
  // `tw approve <app> <id>` hint. Off tailworks (no ctx.tailnet, as in vitest)
  // a placeholder stands in.
  private async appName(): Promise<string> {
    this.app ??= this.ctx.tailnet ? ((await this.ctx.tailnet.self()).workload ?? "<app>") : "<app>";
    return this.app;
  }

  onStart() {
    this.sql`create table if not exists refunds (id text primary key, order_id text, amount real, state text, detail text)`;
  }

  async onWorkflowComplete(_name: string, id: string, result: any) {
    this.sql`update refunds set state = 'refunded', detail = ${JSON.stringify(result)} where id = ${id}`;
  }

  async onWorkflowError(_name: string, id: string, error: string) {
    this.sql`update refunds set state = 'not refunded', detail = ${JSON.stringify({ error })} where id = ${id}`;
  }

  async onRequest(req: Request): Promise<Response> {
    const url = new URL(req.url);
    // POST /agents/refund-agent/<customer>/refund?order=o1&amount=42
    if (req.method === "POST" && url.pathname.endsWith("/refund")) {
      const who = getCaller(req); // the verified tailnet caller asking for the refund
      const params: RefundParams = {
        order: url.searchParams.get("order") ?? "unknown",
        amount: Number(url.searchParams.get("amount") ?? 0),
        requester: who.nodeId,
      };
      const id = await this.runWorkflow("REFUND_WORKFLOW", params, { agentBinding: "RefundAgent" });
      this.sql`insert into refunds (id, order_id, amount, state) values (${id}, ${params.order}, ${params.amount}, 'waiting for approval')`;
      return Response.json({ id, approve: `tw approve ${await this.appName()} ${id}` });
    }
    // GET /agents/refund-agent/<customer>/refunds
    const rows = this.sql`select * from refunds order by rowid`;
    return Response.json({ customer: this.name, refunds: rows });
  }
}

export class RefundWorkflow extends AgentWorkflow<RefundAgent, RefundParams> {
  async run(event: AgentWorkflowEvent<RefundParams>, step: AgentWorkflowStep) {
    const { order, amount, requester } = event.payload;
    const checked = await step.do("check order", async () => ({ order, amount, refundable: amount > 0 }));
    if (!checked.refundable) throw new NonRetryableError("nothing to refund");

    // Waits (durably: a host restart does not lose it) until tailworks delivers a
    // decision from an identity whose grant covers this refund. `amount` is
    // a param, so a grant's {"limits": {"amount": 100}} applies to it.
    // Times out after 7 days: nothing is refunded (reject by default).
    const approval = (await this.waitForApproval(step, { timeout: "7 days" })) as any;

    // The Workflow's own rule, on tailworks' verified identity.
    if (approval.approver.nodeId === requester) {
      throw new NonRetryableError("the person who asked for a refund cannot approve it");
    }

    const receipt = await step.do("refund", async () => ({ order, amount, refundedAt: new Date().toISOString() }));
    const result = { ...receipt, approvedBy: approval.approver.user ?? approval.approver.node, comment: approval.comment ?? null };
    await step.reportComplete(result);
    return result;
  }
}

// The approvals page: same-origin fetches to tailworks' endpoint on this node
// (tailworks answers them; app code never sees the decision request).
const PAGE = `<!doctype html><meta charset="utf-8"><title>refund approvals</title>
<style>body{font:15px system-ui;margin:2em}td,th{padding:.3em .8em;text-align:left}</style>
<h1>refunds waiting for approval</h1><p id="who"></p><table id="t"></table>
<script>
async function load() {
  const r = await fetch("/_tw/approvals");
  const j = await r.json();
  if (!r.ok) { document.getElementById("who").textContent = j.code + ": " + j.message; return; }
  document.getElementById("who").textContent = "you are " + (j.approver.user || j.approver.node);
  const t = document.getElementById("t");
  t.innerHTML = "<tr><th>refund</th><th>params</th><th>waiting since</th><th></th></tr>";
  for (const a of j.approvals) {
    const tr = t.insertRow();
    tr.insertCell().textContent = a.instance;
    tr.insertCell().textContent = JSON.stringify(a.facts || {});
    tr.insertCell().textContent = a.requestedAt;
    const c = tr.insertCell();
    for (const decision of ["approve", "reject"]) {
      const b = document.createElement("button");
      b.textContent = decision;
      b.onclick = async () => {
        const comment = prompt(decision + " " + a.instance + ": comment?") ?? "";
        const res = await fetch("/_tw/approvals/" + a.workflow + "/" + a.instance, {
          method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ decision, comment }) });
        alert(res.ok ? decision + "d" : (await res.json()).message);
        load();
      };
      c.append(b, " ");
    }
  }
}
load();
</script>`;

export default {
  async fetch(req: Request, env: Env) {
    const agent = await routeAgentRequest(req, env);
    if (agent) return agent;
    if (new URL(req.url).pathname === "/") return new Response(PAGE, { headers: { "content-type": "text/html; charset=utf-8" } });
    return new Response("not found", { status: 404 });
  },
};
