// tailworks service bindings (TRUSTED; runs in the platform worker, never in
// a tenant isolate). ADR-service-bindings.
//
// Caller: a tenant's env.<Binding> is a service binding to ServiceBinding
// with props {binding} (a facet's: {binding, class, name}) that tenant code
// can't change. fetch() and RPC calls go out through the calling node's
// egress (the workload's EGRESS_WL, or the facet object's MAC'd egress)
// with x-tw-binding (and x-tw-rpc) set; hostd resolves the binding to the
// target workload's entry node and netd sends the request there as the
// calling node (in process when the target is on this host). The callee
// sees the caller through WhoIs, like any tailnet request.
//
// RPC: the method's arguments and its result cross the tailnet as JSON
// (encode/decode below): structured-clone values only. Anything passed by
// reference on Cloudflare (functions, RpcTarget stubs, streams, Request,
// Response) is refused with rpc_not_forwardable, as are cycles.
//
// Callee: hostd puts {entrypoint, rpc} in cf.tw.binding for a call to a
// named entrypoint or an RPC method (only from a tailworks node of this
// tenant). The tenant main module's __tw_binding entrypoint (TENANT_BINDING)
// runs it in the tenant isolate, wrapped like the default export.
import { WorkerEntrypoint, RpcStub, RpcTarget } from "cloudflare:workers";

// DOC prefixes every error's doc_url. platform.js imports it; it must equal
// netd.DocBase (TestDocBaseMatchesNetd).
export const DOC = "https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#";
// hostd rewrites this host to the target's MagicDNS name.
const SVC_HOST = "tw-binding.invalid";
// An RPC call's encoded arguments or result (docs/limits.md).
export const RPC_MAX_BYTES = 32 << 20;

let egress = null; // platform.js: objectEgress(env, cls, name, url, init, extraHeaders)
export function setObjectEgress(f) {
  egress = f;
}

const errJSON = (status, code, message) =>
  new Response(JSON.stringify({ code, message, doc_url: DOC + code }), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store", "tw-error": code },
  });

class NotForwardable extends Error {}
const notForwardable = (what) => new NotForwardable("[rpc_not_forwardable] " + what + " cannot cross a service binding on tailworks (only structured-clone values can: no functions, RpcTarget stubs, streams, Request or Response)");

// ---- the wire codec ----
//
// A value is JSON; objects are tagged so that every structured-clone type
// round-trips: {"$":"o",v:{…}} plain object, {"$":"a",v:[…]} array,
// {"$":"u"} undefined, {"$":"n",v:"NaN"|"Infinity"|"-Infinity"|"-0"},
// {"$":"bi",v:"123"}, {"$":"d",v:ms}, {"$":"re",s,f}, {"$":"m",v:[[k,v]…]},
// {"$":"s",v:[…]}, {"$":"ab",v:b64}, {"$":"ta",k:"Uint8Array",v:b64},
// {"$":"dv",v:b64}, {"$":"e",n:name,m:message}. Strings, booleans, null and
// finite numbers are themselves.
const TYPED = {
  Int8Array, Uint8Array, Uint8ClampedArray, Int16Array, Uint16Array, Int32Array, Uint32Array,
  Float32Array, Float64Array, BigInt64Array, BigUint64Array,
};
function b64(u8) {
  let s = "";
  for (let i = 0; i < u8.length; i += 0x8000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x8000));
  return btoa(s);
}
function unb64(s) {
  const bin = atob(String(s));
  const u8 = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  return u8;
}
const isStub = (v) => (typeof RpcStub === "function" && v instanceof RpcStub) || (typeof RpcTarget === "function" && v instanceof RpcTarget);

export function encode(v, seen = new Set()) {
  switch (typeof v) {
    case "string":
    case "boolean":
      return v;
    case "undefined":
      return { $: "u" };
    case "bigint":
      return { $: "bi", v: v.toString() };
    case "number":
      if (Number.isNaN(v)) return { $: "n", v: "NaN" };
      if (v === Infinity) return { $: "n", v: "Infinity" };
      if (v === -Infinity) return { $: "n", v: "-Infinity" };
      if (Object.is(v, -0)) return { $: "n", v: "-0" };
      return v;
    case "function":
      throw notForwardable("a function (or an RPC stub)");
    case "symbol":
      throw notForwardable("a symbol");
  }
  if (v === null) return null;
  if (isStub(v)) throw notForwardable("an RpcTarget or RPC stub");
  if (typeof ReadableStream === "function" && v instanceof ReadableStream) throw notForwardable("a ReadableStream");
  if (typeof WritableStream === "function" && v instanceof WritableStream) throw notForwardable("a WritableStream");
  if (v instanceof Request || v instanceof Response || v instanceof Headers) throw notForwardable("a " + v.constructor.name);
  if (v instanceof Date) return { $: "d", v: v.getTime() };
  if (v instanceof RegExp) return { $: "re", s: v.source, f: v.flags };
  if (v instanceof ArrayBuffer) return { $: "ab", v: b64(new Uint8Array(v)) };
  if (v instanceof DataView) return { $: "dv", v: b64(new Uint8Array(v.buffer, v.byteOffset, v.byteLength)) };
  if (ArrayBuffer.isView(v)) {
    const k = Object.keys(TYPED).find((n) => v instanceof TYPED[n]);
    if (!k) throw notForwardable("a " + (v.constructor?.name ?? "typed array"));
    return { $: "ta", k, v: b64(new Uint8Array(v.buffer, v.byteOffset, v.byteLength)) };
  }
  if (v instanceof Error) return { $: "e", n: String(v.name ?? "Error"), m: String(v.message ?? "") };
  if (seen.has(v)) throw notForwardable("a cyclic value");
  seen.add(v);
  try {
    if (Array.isArray(v)) return { $: "a", v: v.map((x) => encode(x, seen)) };
    if (v instanceof Map) return { $: "m", v: [...v].map(([k, x]) => [encode(k, seen), encode(x, seen)]) };
    if (v instanceof Set) return { $: "s", v: [...v].map((x) => encode(x, seen)) };
    const o = {};
    for (const k of Object.keys(v)) o[k] = encode(v[k], seen);
    return { $: "o", v: o };
  } finally {
    seen.delete(v);
  }
}

const ERRORS = { Error, TypeError, RangeError, SyntaxError, ReferenceError, EvalError, URIError };

export function decode(x) {
  if (x === null || typeof x !== "object") {
    if (typeof x === "string" || typeof x === "boolean" || x === null || (typeof x === "number" && Number.isFinite(x))) return x;
    throw new TypeError("bad RPC value");
  }
  switch (x.$) {
    case "u":
      return undefined;
    case "n":
      return { NaN: NaN, Infinity: Infinity, "-Infinity": -Infinity, "-0": -0 }[x.v];
    case "bi":
      return BigInt(String(x.v));
    case "d":
      return new Date(Number(x.v));
    case "re":
      return new RegExp(String(x.s), String(x.f));
    case "ab":
      return unb64(x.v).buffer;
    case "dv":
      return new DataView(unb64(x.v).buffer);
    case "ta": {
      const T = TYPED[x.k];
      if (!T) throw new TypeError("bad RPC typed array");
      const u8 = unb64(x.v);
      return new T(u8.buffer, 0, u8.byteLength / T.BYTES_PER_ELEMENT);
    }
    case "e": {
      const E = ERRORS[x.n] ?? Error;
      const e = new E(String(x.m ?? ""));
      if (!ERRORS[x.n]) e.name = String(x.n ?? "Error");
      return e;
    }
    case "a":
      return (Array.isArray(x.v) ? x.v : []).map(decode);
    case "m":
      return new Map((Array.isArray(x.v) ? x.v : []).map((p) => [decode(p[0]), decode(p[1])]));
    case "s":
      return new Set((Array.isArray(x.v) ? x.v : []).map(decode));
    case "o": {
      const o = {};
      for (const [k, v] of Object.entries(x.v ?? {})) {
        if (k === "__proto__") continue;
        o[k] = decode(v);
      }
      return o;
    }
  }
  throw new TypeError("bad RPC value");
}

// ---- caller ----

// bindingEgress sends request to the binding's target as the calling node.
async function bindingEgress(env, props, request, rpc) {
  const u = new URL(request.url);
  const url = "http://" + SVC_HOST + u.pathname + u.search;
  const h = new Headers(request.headers);
  for (const k of [...h.keys()]) if (k.startsWith("x-tw-") || k.startsWith("tw-") || k === "proxy-authorization" || k === "tailworks-binding") h.delete(k);
  h.set("host", SVC_HOST);
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  const init = { method: request.method, headers: h, body: hasBody ? request.body : undefined, redirect: "manual" };
  const extra = { "x-tw-binding": props.binding };
  if (rpc) extra["x-tw-rpc"] = rpc;
  if (props.class) return egress(env, props.class, props.name, url, init, extra);
  for (const [k, v] of Object.entries(extra)) h.set(k, v);
  return env.EGRESS_WL.fetch(new Request(url, init));
}

function remoteError(e) {
  const E = ERRORS[e?.name] ?? Error;
  const err = new E(String(e?.message ?? "service binding call failed"));
  if (e?.name && !ERRORS[e.name]) err.name = String(e.name);
  return err;
}

async function bindingRPC(env, props, method, args) {
  let body;
  try {
    body = JSON.stringify(encode(args));
  } catch (e) {
    if (e instanceof NotForwardable) throw new TypeError(e.message);
    throw e;
  }
  if (body.length > RPC_MAX_BYTES) throw new RangeError("[rpc_too_large] the RPC call's arguments exceed " + RPC_MAX_BYTES + " bytes");
  const r = await bindingEgress(env, props, new Request("http://" + SVC_HOST + "/", { method: "POST", headers: { "content-type": "application/json" }, body }), method);
  const code = r.headers.get("tw-error");
  let j = null;
  try {
    j = await r.json();
  } catch {
    /* not an RPC reply */
  }
  if (j && typeof j === "object" && "ok" in j && !code) {
    if (j.ok) return decode(j.value);
    throw remoteError(j.error);
  }
  // A typed refusal on the way (netd, hostd, the callee's platform).
  const c = code || j?.code || "dial_failed";
  throw new Error("[" + c + "] env." + props.binding + "." + method + "(): " + (j?.message ?? "the call failed (" + r.status + ")"));
}

export class ServiceBinding extends WorkerEntrypoint {
  constructor(ctx, env) {
    super(ctx, env);
    // Any other property is an RPC method of the target entrypoint.
    return new Proxy(this, {
      get(t, p, r) {
        if (typeof p === "symbol" || p in t) return Reflect.get(t, p, r);
        if (p === "then") return undefined;
        return (...args) => bindingRPC(t.env, t.ctx.props, p, args);
      },
    });
  }
  async fetch(request) {
    return bindingEgress(this.env, this.ctx.props, request, "");
  }
  connect() {
    throw new Error("[not_supported] env." + this.ctx.props.binding + ".connect(): raw TCP over a service binding is not supported on tailworks");
  }
}

// ---- callee ----

const rpcReply = (body) => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json", "cache-control": "no-store" } });

// bindingCallee runs a binding call hostd admitted (cf.tw.binding) in the
// tenant isolate (TENANT_BINDING: the main module's __tw_binding).
export async function bindingCallee(req, env, t) {
  const b = t.binding ?? {};
  const cf = { ...req.cf, tw: { kind: "binding", binding: { entrypoint: String(b.entrypoint ?? "") }, ticket: t.ticket } };
  if (!b.rpc) return env.TENANT_BINDING.fetch(new Request(req, { cf }));
  if (req.method !== "POST") return errJSON(400, "bad_request", "an RPC call is a POST");
  let args;
  try {
    const text = await req.text();
    if (text.length > RPC_MAX_BYTES) return errJSON(400, "bad_request", "the RPC call's arguments exceed " + RPC_MAX_BYTES + " bytes");
    args = decode(JSON.parse(text));
    if (!Array.isArray(args)) throw new TypeError("arguments must be an array");
  } catch (e) {
    return errJSON(400, "bad_request", "bad RPC arguments: " + String(e?.message ?? e));
  }
  let v;
  try {
    v = await env.TENANT_BINDING.__tw_call(String(b.entrypoint ?? ""), String(b.rpc), args, cf);
  } catch (e) {
    return rpcReply({ ok: false, error: { name: String(e?.name ?? "Error"), message: String(e?.message ?? e) } });
  }
  try {
    return rpcReply({ ok: true, value: encode(v) });
  } catch (e) {
    const m = e instanceof NotForwardable ? e.message.replace("[rpc_not_forwardable] ", "the result: ") : String(e?.message ?? e);
    return rpcReply({ ok: false, error: { name: "TypeError", message: "[rpc_not_forwardable] " + m } });
  }
}
