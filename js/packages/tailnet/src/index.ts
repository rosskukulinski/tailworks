// @tailworks/tailnet: verified tailnet caller identity and per-object egress for
// tailworks Workers and Durable Objects (RFC §6.1).
export { getCaller, getRequester, isPublicRequest, principalKey } from "./caller.js";
export type { Caller, Requester, PublicRequester, TailnetRequester } from "./caller.js";
export { TailnetError, isTailnetError, fromResponse, CATALOG, DOC_BASE } from "./errors.js";
export type { TailnetErrorCode, TailnetErrorInit, Missing, CatalogEntry } from "./errors.js";
export type { TailnetContext, SpawnOptions, Spawned } from "./runtime.js";
export type { TailnetSelf } from "./runtime.js";
import "./globals.js"; // ctx.tailnet on DurableObjectState and ExecutionContext
import { TailnetError } from "./errors.js";
import type { TailnetContext } from "./runtime.js";

/**
 * tailnet returns ctx.tailnet (DurableObjectState or ExecutionContext), or
 * throws no_identity when not running on tailworks.
 */
export function tailnet(ctx: { tailnet?: TailnetContext } | null | undefined): TailnetContext {
  const t = ctx?.tailnet;
  if (!t) throw new TailnetError("no_identity", "ctx.tailnet is not available (not running on tailworks?)");
  return t;
}
