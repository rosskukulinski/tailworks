import { DurableObject } from "cloudflare:workers";
import { Buffer } from "node:buffer";
import { shout } from "greeting-lib";
import notes from "./notes.md";
import hello from "./hello.txt";
import wasm from "./empty.wasm";

declare const BUILD_ID: string;

interface Env {
  GREETING: string;
  COUNTER: DurableObjectNamespace<Counter>;
  CACHE?: unknown;
}

export class Counter extends DurableObject {
  async bump(): Promise<number> {
    const n = ((await this.ctx.storage.get<number>("n")) ?? 0) + 1;
    await this.ctx.storage.put("n", n);
    return n;
  }
}

export default {
  async fetch(_req: Request, env: Env): Promise<Response> {
    const inst = await WebAssembly.instantiate(wasm);
    return Response.json({
      greeting: env.GREETING,
      build: BUILD_ID,
      shout: shout("alias"),
      notes,
      hello,
      wasm: inst instanceof WebAssembly.Instance,
      buffer: Buffer.from("tw").toString("base64"),
      count: await env.COUNTER.getByName("c").bump(),
      cache: typeof env.CACHE,
    });
  },
};
