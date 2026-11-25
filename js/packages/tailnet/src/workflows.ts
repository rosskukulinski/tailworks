// Workflows on tailworks (ADR-040): the runtime side.
//
// - wrapWorkflowClass wraps each WorkflowEntrypoint class the engine runs
//   (the tenant service's exports). Its run() reports every approval wait
//   (step.waitForEvent with type "approval" or "approval-*", which is what
//   the Agents SDK's waitForApproval() does) to tailworks, so the wait is listed
//   (`tw approvals`) and audited, and tailworks can deliver the decision. The
//   reports are durable steps: a run() that runs again (after a restart, a
//   long wait or an event) does not report twice.
// - workflowClient is env.<binding> inside a facet-mode object: the Workflow
//   API (as cloudflare-internal:workflows-api builds it) over the platform's
//   WorkflowGate, which the platform put in the facet's env.

import { guardTailnetEnv, type BindingMap, type TailnetApiStub } from "./runtime.js";

/** isApprovalType: the event types tailworks treats as approvals. */
export function isApprovalType(t: unknown): boolean {
  return t === "approval" || (typeof t === "string" && t.startsWith("approval-"));
}

// The reports retry while hostd is briefly away (a restart).
const REPORT_STEP = { retries: { limit: 10, delay: "1 second", backoff: "exponential" }, timeout: "30 seconds" } as const;

function numericFacts(payload: unknown): Record<string, number> {
  const out: Record<string, number> = {};
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return out;
  for (const [k, v] of Object.entries(payload as Record<string, unknown>)) {
    if (typeof v === "number" && Number.isFinite(v) && !k.startsWith("__")) out[k] = v;
  }
  return out;
}

function isAborted(e: unknown): boolean {
  return (e as any)?.name === "WorkflowAbortedError" || String((e as any)?.message ?? "").includes("WorkflowAbortedError");
}

function isTimeout(e: unknown): boolean {
  const m = String((e as any)?.message ?? "");
  return (e as any)?.name === "WorkflowTimeoutError" || m.includes("Execution timed out");
}

async function report(tw: TailnetApiStub | undefined, args: Record<string, unknown>): Promise<boolean> {
  if (!tw) return false; // not on tailworks (tests): nothing to report to
  const r = await tw.call("approval", args);
  if (!r.ok) throw new Error(`[${r.error?.code ?? "hostd_unavailable"}] approval ${args.op}: ${r.error?.message ?? "refused"}`);
  return true;
}

// The steps already wrapped. (Not a property: the step is an RPC stub, on
// which any property name reads as a callable.)
const wrapped = new WeakSet<object>();

/** installApprovalWaits makes step.waitForEvent report approval waits (in place: the SDKs rely on the step's identity). */
export function installApprovalWaits(step: any, tw: TailnetApiStub | undefined, workflow: string, event: any) {
  if (!step || (typeof step !== "object" && typeof step !== "function") || wrapped.has(step)) return;
  const origWait = step.waitForEvent;
  const origDo = step.do;
  if (typeof origWait !== "function" || typeof origDo !== "function") return;
  // Reflect.apply, not .call(): on an RPC stub, .call is a remote method name.
  const wait = (...a: unknown[]) => Reflect.apply(origWait, step, a);
  const doStep = (...a: unknown[]) => Reflect.apply(origDo, step, a);
  const instance = String(event?.instanceId ?? "");
  const facts = numericFacts(event?.payload);
  step.waitForEvent = async (name: string, opts?: { type?: string; timeout?: unknown }) => {
    const type = opts?.type;
    if (!isApprovalType(type) || !instance) return wait(name, opts);
    // limits.md#approval-timeout
    const base = { workflow, instance, step: name, type, timeout: opts?.timeout ?? "24 hours", facts };
    await doStep(`__tw_approval_requested:${name}`, REPORT_STEP, () => report(tw, { ...base, op: "requested" }));
    try {
      return await wait(name, opts);
    } catch (e) {
      // waitForEvent's timeout: the default is "rejected" (nothing was
      // approved). Record it, then let the Workflow see the error.
      if (isTimeout(e)) await doStep(`__tw_approval_timeout:${name}`, REPORT_STEP, () => report(tw, { ...base, op: "timeout" }));
      throw e;
    }
  };
  wrapped.add(step);
}

/** wrapWorkflowClass wraps a Workflow class (wrangler "workflows" class_name) for the engine. */
export function wrapWorkflowClass(C: any, name: string, workflow: string, _binding: string, bindings: BindingMap) {
  if (typeof C !== "function") throw new Error(`tw: workflows class ${name} (Workflow ${workflow}) is not exported by the main module`);
  const W: any = {
    [name]: class extends C {
      constructor(ctx: any, env: any) {
        const tw = env?.__TW;
        // A Workflow reaches objects as the Worker does without a request:
        // existing objects only (no create ticket), and never __TW.
        super(ctx, guardTailnetEnv(env, bindings));
        Object.defineProperty(this, "__tw_tw", { value: tw });
      }
      async run(event: any, step: any) {
        installApprovalWaits(step, (this as any).__tw_tw, workflow, event);
        try {
          return await super.run(event, step);
        } catch (e) {
          // workerd's engine ends a run() that waits long (a sleep or an
          // event wait) by throwing WorkflowAbortedError through it; the run
          // goes on later. It is not a failure: keep the Agents SDK's
          // AgentWorkflow from reporting it to its Agent (onWorkflowError).
          if (isAborted(e) && "_errorReported" in (this as any)) (this as any)._errorReported = true;
          throw e;
        }
      }
    },
  }[name];
  return W;
}

// ---- the Workflow API in a facet (over WorkflowGate) ----

class InstanceClient {
  readonly id: string;
  readonly #f: any;
  constructor(id: string, f: any) {
    this.id = id;
    this.#f = f;
  }
  async pause() {
    await this.#f.pause(this.id);
  }
  async resume() {
    await this.#f.resume(this.id);
  }
  async terminate(options?: unknown) {
    await this.#f.terminate(this.id, options);
  }
  async restart(options?: unknown) {
    await this.#f.restart(this.id, options);
  }
  async delete() {
    await this.#f.deleteInstance(this.id);
  }
  async status() {
    return await this.#f.status(this.id);
  }
  async sendEvent({ type, payload }: { type: string; payload: unknown }) {
    await this.#f.sendEvent(this.id, { type, payload });
  }
  async subscribe(options?: unknown) {
    return await this.#f.subscribe(this.id, options);
  }
}

/** workflowClient is the Workflow binding API over the platform's WorkflowGate stub. */
export function workflowClient(f: any) {
  return {
    async get(id: string) {
      const r = await f.getInstance(id);
      return new InstanceClient(r.id, f);
    },
    async create(options?: unknown) {
      const r = await f.create(options);
      return new InstanceClient(r.id, f);
    },
    async createBatch(options: any) {
      const r = await f.createBatch(options);
      if (Array.isArray(options)) return (r as Array<{ id: string }>).map((x) => new InstanceClient(x.id, f));
      return { created: r.created.map((x: { id: string }) => new InstanceClient(x.id, f)), errors: r.errors };
    },
    async deleteBatch(instanceIds: string[]) {
      return await f.deleteBatch({ instances: instanceIds });
    },
  };
}
