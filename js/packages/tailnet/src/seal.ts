// Caller seals (RT3-08 / H08, #148).
//
// request.cf is whatever the sender says once tenant code forwards a
// request in-process (a Durable Object stub fetch), so the tenant service's
// entry points (the Worker, plain DOs) only see a caller hostd sealed. The
// seal (cf.tailscale.seal) is an HMAC under the instance key, which lives
// only in the platform worker: the runtime asks it ("unseal") and replaces
// cf.tailscale.caller with the sealed copy, or drops the caller (and seal)
// when the seal is missing or does not verify. Facet objects get the same
// treatment in the platform's Host_<Class>, before the facet sees a request.
import type { TailnetApiStub } from "./runtime.js";

const CACHE_MAX = 1024;
// seal MAC -> the caller it verified (and its expiry). Keyed by MAC: a hit
// returns the caller hostd sealed, whatever else the presented seal says.
const verified = new Map<string, { caller: unknown; exp: number }>();

function hasCaller(req: any): boolean {
  const ts = req?.cf?.tailscale;
  return !!ts && typeof ts === "object" && ("caller" in ts || "seal" in ts);
}

async function unseal(tw: TailnetApiStub | undefined, seal: any): Promise<unknown> {
  if (!seal || typeof seal !== "object" || typeof seal.mac !== "string" || typeof seal.exp !== "number") return null;
  const hit = verified.get(seal.mac);
  if (hit) {
    if (Date.now() <= hit.exp) return hit.caller;
    verified.delete(seal.mac);
  }
  if (!tw || typeof tw.call !== "function") return null;
  let r: any;
  try {
    r = await tw.call("unseal", { seal });
  } catch {
    return null;
  }
  const caller = r?.ok ? (r.value?.caller ?? null) : null;
  if (caller) {
    if (verified.size >= CACHE_MAX) verified.delete(verified.keys().next().value as string);
    verified.set(seal.mac, { caller, exp: seal.exp });
  }
  return caller;
}

/**
 * sealedRequest returns req with only a verified caller in cf.tailscale (see
 * above). A request with neither a caller nor a seal is returned as is.
 */
export async function sealedRequest<R>(tw: TailnetApiStub | undefined, req: R): Promise<R> {
  if (!hasCaller(req)) return req;
  const cf = (req as any).cf;
  const caller = await unseal(tw, cf.tailscale.seal);
  const { caller: _c, seal, ...rest } = cf.tailscale;
  const out = { ...cf, tailscale: caller ? { ...rest, caller, seal } : rest };
  if (typeof Request === "function" && req instanceof Request) return new Request(req, { cf: out } as RequestInit) as R;
  return { ...(req as any), cf: out }; // not a workerd Request (unit tests)
}
