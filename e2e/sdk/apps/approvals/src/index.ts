// e2e/sdk approvals app: a real Agents SDK AgentWorkflow that waits for a
// human approval (waitForApproval) on tailworks. Tailworks delivers the approval only
// from a tailnet identity holding github.com/rosskukulinski/tailworks/cap/approve, and puts that
// identity in the approval's metadata (approval.approver).
//
// The same Agent body runs in both object modes:
//   RefundAgent       facet mode (default)
//   PlainRefundAgent  plain-DO mode ("egress": "workload")
import { Agent, routeAgentRequest } from "agents";
import { AgentWorkflow, type AgentWorkflowEvent, type AgentWorkflowStep } from "agents/workflows";

interface Env {
  RefundAgent: any;
  PlainRefundAgent: any;
  REFUND_WORKFLOW: any;
}

type Params = { order: string; amount: number; failQuote?: number };

const errOf = (e: any) => ({ error: String(e?.message ?? e) });

class RefundBase extends Agent<Env> {
  onStart() {
    this.sql`create table if not exists steplog (wf text, what text, at integer)`;
    this.sql`create table if not exists results (wf text primary key, result text)`;
  }

  // Called by the Workflow (RPC) inside its steps: how often each ran.
  async note(wf: string, what: string): Promise<number> {
    this.sql`insert into steplog (wf, what, at) values (${wf}, ${what}, ${Date.now()})`;
    const [r] = this.sql<{ n: number }>`select count(*) as n from steplog where wf = ${wf} and what = ${what}`;
    return r.n;
  }

  async onWorkflowComplete(_name: string, id: string, result: unknown) {
    this.sql`insert or replace into results (wf, result) values (${id}, ${JSON.stringify({ result })})`;
  }

  async onWorkflowError(_name: string, id: string, error: string) {
    this.sql`insert or replace into results (wf, result) values (${id}, ${JSON.stringify({ error })})`;
  }

  async onRequest(req: Request): Promise<Response> {
    const url = new URL(req.url);
    const op = url.pathname.split("/").filter(Boolean).pop() ?? "";
    const q = (k: string) => url.searchParams.get(k);
    const binding = this.constructor.name; // the DO binding is named after the class
    try {
      switch (op) {
        case "start": {
          const id = await this.runWorkflow(
            "REFUND_WORKFLOW",
            { order: q("order") ?? "o1", amount: Number(q("amount") ?? 50), failQuote: Number(q("failQuote") ?? 0) },
            { id: q("id") ?? undefined, agentBinding: binding },
          );
          return Response.json({ id });
        }
        case "status": {
          const id = q("id")!;
          const status = await this.getWorkflowStatus("REFUND_WORKFLOW", id);
          const steps = this.sql<{ what: string; n: number }>`select what, count(*) as n from steplog where wf = ${id} group by what order by what`;
          const [row] = this.sql<{ result: string }>`select result from results where wf = ${id}`;
          return Response.json({ status, steps: Object.fromEntries(steps.map((s) => [s.what, s.n])), outcome: row ? JSON.parse(row.result) : null });
        }
        case "self-approve": {
          // App code approving its own Workflow (the Agents SDK's
          // approveWorkflow), with a forged approver: tailworks refuses it.
          try {
            await this.approveWorkflow(q("id")!, { reason: "self", metadata: { approver: { user: "forged@example.com" } } });
            return Response.json({ approved: true });
          } catch (e) {
            return Response.json(errOf(e));
          }
        }
      }
    } catch (e) {
      return Response.json(errOf(e), { status: 500 });
    }
    return Response.json({ error: "unknown op " + op }, { status: 404 });
  }
}

export class RefundAgent extends RefundBase {}
export class PlainRefundAgent extends RefundBase {}

export class RefundWorkflow extends AgentWorkflow<RefundBase, Params> {
  async run(event: AgentWorkflowEvent<Params>, step: AgentWorkflowStep) {
    const { order, amount } = event.payload;
    const wf = event.instanceId;
    await step.do("lookup", async () => {
      await this.agent.note(wf, "lookup");
      return { order, amount };
    });
    // Fails its first failQuote attempts: the engine retries this step, and
    // "lookup" (completed) does not run again.
    const quote = await step.do("quote", { retries: { limit: 3, delay: 50, backoff: "constant" } }, async () => {
      const n = await this.agent.note(wf, "quote-attempt");
      if (n <= (event.payload.failQuote ?? 0)) throw new Error("quote service down");
      return { amount };
    });
    // tailworks lists this wait, and delivers the decision only from a tailnet
    // identity holding github.com/rosskukulinski/tailworks/cap/approve. The metadata carries tailworks'
    // verified approver, whatever the request said.
    const approval = (await this.waitForApproval(step, { timeout: "1 hour" })) as any;
    await step.do("refund", async () => {
      await this.agent.note(wf, "refund");
    });
    const result = { order, amount: quote.amount, approver: approval?.approver ?? null, comment: approval?.comment ?? null, note: approval?.note ?? null };
    await step.reportComplete(result); // the Agent's onWorkflowComplete
    return result;
  }
}

export default {
  async fetch(req: Request, env: Env) {
    return (await routeAgentRequest(req, env)) ?? new Response("not found", { status: 404 });
  },
};
