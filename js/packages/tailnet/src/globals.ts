// Global types: tailworks installs ctx.tailnet on a Durable Object's state and on
// the Worker's ExecutionContext (RFC §6.1). Importing @tailworks/tailnet, or
// listing it in tsconfig.json "types", adds the property to
// @cloudflare/workers-types' (or workerd's generated) declarations, so
// `this.ctx.tailnet` and `ctx.tailnet` type-check. It is optional: it is
// undefined when the code doesn't run on tailworks (use tailnet(ctx) to throw).
import type { TailnetContext } from "./runtime.js";

declare global {
  // The type parameters must match workers-types' declarations exactly.
  interface DurableObjectState<Props = unknown> {
    readonly tailnet?: TailnetContext;
  }
  interface ExecutionContext<Props = unknown> {
    readonly tailnet?: TailnetContext;
  }
}

export {};
