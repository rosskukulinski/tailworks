// e2e/sdk agentwf app (#359): an Agents SDK Agent starts an AgentWorkflow
// with runWorkflow; the Workflow calls back into its Agent (an RPC from a
// step, and reportProgress → onWorkflowProgress), as Cloudflare's agents
// "workflows" example does. Neither Agent class has a node of its own.
import { Agent, routeAgentRequest } from "agents";
import { AgentWorkflow, type AgentWorkflowEvent, type AgentWorkflowStep } from "agents/workflows";

interface Env {
  TaskAgent: any;
  InternalTaskAgent: any;
  TASK_WORKFLOW: any;
}

class TaskBase extends Agent<Env> {
  onStart() {
    this.sql`create table if not exists log (wf text, what text)`;
  }

  async note(wf: string, what: string): Promise<void> {
    this.sql`insert into log (wf, what) values (${wf}, ${what})`;
  }

  async onWorkflowProgress(_name: string, id: string, progress: unknown) {
    this.sql`insert into log (wf, what) values (${id}, ${"progress " + JSON.stringify(progress)})`;
  }

  async onWorkflowComplete(_name: string, id: string) {
    this.sql`insert into log (wf, what) values (${id}, ${"complete"})`;
  }

  async onWorkflowError(_name: string, id: string, error: string) {
    this.sql`insert into log (wf, what) values (${id}, ${"error " + error})`;
  }

  async onRequest(req: Request): Promise<Response> {
    const url = new URL(req.url);
    const op = url.pathname.split("/").filter(Boolean).pop() ?? "";
    try {
      if (op === "start") {
        const id = await this.runWorkflow("TASK_WORKFLOW", { task: "t" }, { agentBinding: this.constructor.name });
        return Response.json({ id });
      }
      if (op === "log") {
        const id = url.searchParams.get("id") ?? "";
        const rows = this.sql<{ what: string }>`select what from log where wf = ${id}`;
        return Response.json({ log: rows.map((r) => r.what) });
      }
    } catch (e: any) {
      return Response.json({ error: String(e?.message ?? e) }, { status: 500 });
    }
    return Response.json({ error: "unknown op " + op }, { status: 404 });
  }
}

export class TaskAgent extends TaskBase {}
export class InternalTaskAgent extends TaskBase {}

export class TaskWorkflow extends AgentWorkflow<TaskBase, { task: string }> {
  async run(event: AgentWorkflowEvent<{ task: string }>, step: AgentWorkflowStep) {
    const wf = event.instanceId;
    await this.reportProgress({ step: "validate", percent: 0 });
    await step.do("validate", async () => {
      await this.agent.note(wf, "validate");
    });
    await step.reportComplete({ ok: true });
    return { ok: true };
  }
}

export default {
  async fetch(req: Request, env: Env) {
    return (await routeAgentRequest(req, env)) ?? new Response("not found", { status: 404 });
  },
};
