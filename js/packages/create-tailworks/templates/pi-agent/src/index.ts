// pi-agent: Cloudflare's Pi harness example (PiHarness from agents/harness/pi)
// on tailworks. Pi Durable runs inside a Durable Object, which is also its own
// tailnet node: the transcript, inbox, model calls, tool calls, retries and
// crash recovery live in the object's SQLite (pi_* tables), and a Lifecycle
// job (the object's alarm) wakes it, so a run survives eviction.
//
// The model (src/model.ts) is an OpenAI-compatible gateway on your tailnet,
// such as Aperture, when wrangler.jsonc vars.LLM_URL is set, and a scripted
// mock (no keys) when it is empty. Tailworks has no Workers AI binding.
//
//   GET /?prompt=…              ask your own assistant (keyed by your identity)
//   GET /agents/<name>?prompt=… a named assistant
//   GET /  (no prompt)          the assistant's transcript
import { DurableObject } from "cloudflare:workers";
import { createModels } from "@earendil-works/pi-ai/models";
import { createRegistry, Harness, type ToolRegistration } from "@earendil-works/pi-durable";
import { obj, str } from "@earendil-works/pi-schema";
import { PiHarness } from "agents/harness/pi";
import { Lifecycle } from "agents/lifecycle";
import { getCaller, principalKey } from "@tailworks/tailnet";
import { chooseModel } from "./model";

// Env (the Assistant namespace) is the global interface `tw types` writes to
// worker-configuration.d.ts. Run `tw types` again after changing
// wrangler.jsonc or tailworks.jsonc.

const WordCount = obj({ text: str() });

const wordCount: ToolRegistration<typeof WordCount> = {
  name: "word_count",
  description: "Count the words in a text.",
  parameters: WordCount,
  // Running it twice is harmless, so Pi may run it again after a crash.
  replay: "safe",
  async execute({ text }) {
    const words = text.split(/\s+/).filter(Boolean).length;
    return { output: [{ type: "text", text: String(words) }] };
  },
};

export class Assistant extends DurableObject<Env> {
  llm = chooseModel(this.env);
  registry = createRegistry();

  harness = new PiHarness({
    // Runs at startup and again if the harness reopens Pi: keep it safe to repeat.
    harness: ({ storage, context, onWake }) => {
      const models = createModels();
      models.setProvider(this.llm.provider);
      return Harness.open(storage, { models, registry: this.registry, onWake }, context);
    },
    defaults: { model: this.llm.model, maxTurns: 4 },
  });

  lifecycle = Lifecycle.install(this).use(this.harness);

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.registry.install({
      name: "editor",
      sections: [{ key: "preamble", render: () => "You are an editor.", tag: false }],
      tools: [wordCount],
    });
  }

  async fetch(req: Request): Promise<Response> {
    const prompt = new URL(req.url).searchParams.get("prompt");
    if (!prompt) return Response.json({ messages: await this.harness.messages() });
    const { status, text, reason } = await this.harness.prompt(prompt);
    return Response.json({ status, text, reason });
  }
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const m = url.pathname.match(/^\/agents\/([^/]+)/);
    const name = m ? decodeURIComponent(m[1]) : principalKey(getCaller(req));
    const res = await env.Assistant.getByName(name).fetch(req);
    return Response.json({ assistant: name, ...((await res.json()) as object) }, { status: res.status });
  },
};
