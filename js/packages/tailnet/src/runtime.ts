// Platform-injected runtime. tw-hostd's generated shims import this module
// (bundled as __tw_rt.js) to install ctx.tailnet on Durable Object state
// and on the Worker ExecutionContext. Tenant code reaches it as
// `this.ctx.tailnet` / `ctx.tailnet` (RFC §6.1); it never imports it.
//
// Every call goes through `tw`, a platform TailnetApi entrypoint stub whose
// props (minted by the platform, read-only here) say which object or
// workload is calling, so tenant code cannot act as another object.

import { TailnetError, fromResponse, isTailnetError } from "./errors.js";
import { createContainer, defineContainer, dispatchIntercept, notSupportedPreview, type ContainerSpec } from "./container.js";
import { workflowClient } from "./workflows.js";

export { wrapWorkflowClass, isApprovalType } from "./workflows.js";
import { wrapStores } from "./d1.js";
import { sealedRequest } from "./seal.js";
import { installFetchPropagation, runTraced, tracedStubFetch, type SpanRecord } from "./trace.js";
import { rpcCall, tracedEntry, traceGate, type EntryInfo } from "./rpctrace.js";
import { facetNamespaces } from "./facetns.js";

// Egress fetch() carries the running handler's trace context
// (ADR-045).
installFetchPropagation();

/** spanFlusher reports a handler's DO-call spans to hostd (TailnetApi "spans"). */
function spanFlusher(tw: TailnetApiStub | undefined, ctx: any, as?: Self): (spans: SpanRecord[]) => void {
  return (spans) => {
    if (!tw || typeof tw.call !== "function") return;
    const p = Promise.resolve()
      .then(() => tw.call("spans", as ? { spans, as } : { spans }))
      .catch(() => {});
    try {
      ctx?.waitUntil?.(p);
    } catch {
      /* no waitUntil: the report may be cut short */
    }
  };
}

// The platform binding of each wrapped DO instance (for span reports).
const twOf = new WeakMap<object, TailnetApiStub | undefined>();

/**
 * entryInfo: how a DO's entry points are traced (rpctrace.ts). A facet's
 * span reports are attested by its platform binding; a plain DO's name its
 * object and carry its ID, which is how hostd matches them to the
 * invocation's tail record.
 */
function entryInfo(obj: any, cls: string, plain: boolean): EntryInfo {
  const ctx = () => obj.ctx;
  const oname = (): string | undefined => {
    const n = ctx()?.id?.name;
    return typeof n === "string" && n ? n : undefined;
  };
  return {
    cls,
    alarm: plain ? "alarm" : "__tw_alarm",
    attrs() {
      const a: Record<string, string> = {};
      const n = oname();
      if (n) a["tw.do.name"] = n;
      if (plain) {
        try {
          const id = ctx()?.id?.toString?.();
          if (typeof id === "string" && id) a["tw.do.id"] = id;
        } catch {
          /* no id */
        }
      }
      return a;
    },
    flush() {
      const n = oname();
      return spanFlusher(twOf.get(obj), ctx(), plain && n ? { class: cls, name: n } : undefined);
    },
  };
}

/** The platform's TailnetApi stub (RPC). */
export interface TailnetApiStub {
  call(op: string, args: Record<string, unknown>): Promise<{ ok: true; value: any } | { ok: false; error: Record<string, any> }>;
  /** ctx.tailnet.connect(): the socket's streams (platform.js). */
  tcpConnect?(args: Record<string, unknown>): Promise<{ ok: true; value: { readable: ReadableStream; writable: WritableStream } } | { ok: false; error: Record<string, any> }>;
}

/** A TCP destination: "host:port", or { hostname, port } as in cloudflare:sockets. */
export type SocketAddress = string | { hostname: string; port: number };

export interface TailnetSocketOptions {
  /**
   * "off" (the default) only: the flow is plaintext from the Worker. On the
   * tailnet WireGuard already encrypts it; for TLS to the destination use
   * `tls: "netd"`.
   */
  secureTransport?: "off";
  /** "netd": the host originates TLS to the destination (system roots, SNI = hostname). */
  tls?: "netd";
}

/** The socket ctx.tailnet.connect() returns (the cloudflare:sockets Socket shape). */
export interface TailnetSocket {
  readonly readable: ReadableStream<Uint8Array>;
  readonly writable: WritableStream<Uint8Array>;
  /** Resolves once the platform has dialled; rejects with a TailnetError (peer_not_visible, forbidden_destination, …). */
  readonly opened: Promise<{ remoteAddress: string }>;
  readonly closed: Promise<void>;
  close(): Promise<void>;
}

export interface SpawnOptions {
  name: string;
  /** Must be ⊆ the parent's effective caps (checked by tw-mgmt). */
  caps?: Record<string, unknown>;
  ttl?: string;
  principal?: "inherit" | "none";
  /** How long `ready` waits for the child's node (default 30 s). */
  readyTimeoutMs?: number;
}

export interface Spawned<T = unknown> {
  stub: T;
  /** Node hostname now; the MagicDNS name once ready. */
  name: string;
  objectId: string;
  state(): "pending" | "ready" | "failed";
  ready: Promise<void>;
}

export interface TailnetContext {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
  spawn<T = any>(ns: any, opts: SpawnOptions): Promise<Spawned<T>>;
  kill(opts?: { reason?: string; cascade?: boolean; target?: { class: string; name: string } }): Promise<{ killed: string[] }>;
  audit(records: Array<Record<string, unknown>>): Promise<void>;
  /**
   * This object's (or the workload's) node; `workload`, the name of the
   * workload it belongs to, and `deploymentId`, the deployment running now;
   * and `epoch`: the workload's lease epoch on this host. mgmt raises it on
   * every deploy and every move to another host, so it orders this
   * incarnation after every earlier one (fencing tokens, docs/locks.md).
   */
  self(): Promise<TailnetSelf>;
  /**
   * A raw TCP socket from this object's own node (facet mode) or, in the
   * Worker, the workload node, under the same policy as fetch(). Refusals
   * reject `opened` with a TailnetError. In a plain-DO object it throws
   * not_supported (no per-object identity; stock connect() leaves as the
   * workload node).
   */
  connect(address: SocketAddress, options?: TailnetSocketOptions): TailnetSocket;
}

/** What ctx.tailnet.self() answers. */
export interface TailnetSelf {
  objectId: string;
  hostname: string;
  magicDNS?: string;
  status: string;
  kind: string;
  /** The workload's name (what `tw deploy` named it). */
  workload: string;
  /** The deployment running now. */
  deploymentId: string;
  epoch: number;
}

export type BindingMap = Record<string, string>; // env binding name -> DO class

export interface Self {
  class: string;
  name: string;
}

export function createTailnetContext(tw: TailnetApiStub, env: Record<string, any>, bindings: BindingMap, self?: Self, ticket?: string): TailnetContext {
  if (!tw || typeof tw.call !== "function") throw new TailnetError("no_identity", "tw platform binding missing (not running on tailworks?)");
  const op = async (name: string, args: Record<string, unknown> = {}) => {
    // The request's create ticket admits a Worker-level spawn (hostd checks
    // it); an object's own spawns are attested by `as` instead.
    const a: Record<string, unknown> = self ? { ...args, as: self } : ticket ? { ...args, ticket } : args;
    const r = await tw.call(name, a);
    if (!r || !r.ok) throw TailnetError.from((r as any)?.error);
    return r.value;
  };
  const classOf = (ns: unknown): string => {
    for (const [k, cls] of Object.entries(bindings)) if (env[k] === ns) return cls;
    if (typeof ns === "string" && Object.values(bindings).includes(ns)) return ns;
    throw new TailnetError("bad_request", "spawn: the namespace is not a durable_objects binding of this app");
  };
  const tfetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    let r: Response;
    try {
      r = await fetch(input as any, init);
    } catch (e) {
      throw new TailnetError("dial_failed", String((e as Error)?.message ?? e));
    }
    const err = await fromResponse(r);
    if (err) throw err;
    return r;
  };
  const tn: TailnetContext & { forward: unknown } = {
    fetch: tfetch,
    // forward is no longer part of TailnetContext: signed on-behalf-of
    // assertions (RFC §4.6) are not built, so it was only fetch with the
    // request's headers copied. Kept, unlisted, for one minor release so
    // deployed apps that call it keep working; use
    // ctx.tailnet.fetch(new Request(url, request)) instead.
    forward(request: Request, to: string | URL, init?: RequestInit) {
      const hasBody = request.method !== "GET" && request.method !== "HEAD";
      return tfetch(new Request(String(to), { method: request.method, headers: request.headers, body: hasBody ? request.body : undefined, ...init }));
    },
    async spawn(ns: any, opts: SpawnOptions) {
      if (!opts || typeof opts.name !== "string" || !opts.name) throw new TailnetError("bad_request", "spawn: opts.name is required");
      const cls = classOf(ns);
      const v = await op("spawn", { class: cls, name: opts.name, caps: opts.caps, ttl: opts.ttl, principal: opts.principal });
      let state: "pending" | "ready" | "failed" = v.status === "ready" ? "ready" : "pending";
      // Await: inside a facet, env.X is an RPC stub and getByName returns an
      // RpcPromise that must not be stored un-awaited.
      const stub = await ns.getByName(opts.name);
      const sp: Spawned<any> = {
        stub,
        name: v.hostname ?? opts.name,
        objectId: v.objectID,
        state: () => state,
        ready: Promise.resolve(),
      };
      sp.ready = op("ready", { class: cls, name: opts.name, timeoutMs: opts.readyTimeoutMs ?? 30000 }).then(
        (r) => {
          state = "ready";
          if (r?.magicDNS) sp.name = r.magicDNS;
        },
        (e) => {
          state = "failed";
          throw e;
        },
      );
      sp.ready.catch(() => {}); // callers may ignore readiness
      return sp;
    },
    kill(opts: { reason?: string; cascade?: boolean; target?: { class: string; name: string } } = {}) {
      return op("kill", { reason: opts.reason ?? "", cascade: !!opts.cascade, target: opts.target });
    },
    async audit(records) {
      await op("audit", { records });
    },
    self() {
      return op("self", {});
    },
    connect(address: SocketAddress, options: TailnetSocketOptions = {}) {
      return tailnetSocket(tw, socketAddress(address), options, self);
    },
  };
  return Object.freeze(tn);
}

/** socketAddress is address as "host:port" (IPv6 hosts bracketed). */
export function socketAddress(address: SocketAddress): string {
  if (typeof address === "string") return address;
  if (!address || typeof address.hostname !== "string" || !Number.isInteger(address.port)) {
    throw new TailnetError("bad_request", "connect: address must be \"host:port\" or { hostname, port }");
  }
  const h = address.hostname.includes(":") && !address.hostname.startsWith("[") ? "[" + address.hostname + "]" : address.hostname;
  return h + ":" + address.port;
}

// tailnetSocket returns a Socket at once (as cloudflare:sockets does) and
// wires its streams to the platform's once the dial succeeds.
function tailnetSocket(tw: TailnetApiStub, address: string, options: TailnetSocketOptions, self?: Self): TailnetSocket {
  if (options.secureTransport && options.secureTransport !== "off") {
    throw new TailnetError("not_supported", 'ctx.tailnet.connect: secureTransport "' + String(options.secureTransport) + '" is not supported; use { tls: "netd" }');
  }
  if (options.tls !== undefined && options.tls !== "netd") throw new TailnetError("bad_request", 'ctx.tailnet.connect: tls must be "netd"');
  if (typeof tw.tcpConnect !== "function") throw new TailnetError("not_supported", "ctx.tailnet.connect() needs a newer tailworks host");
  const toUp = new TransformStream<Uint8Array, Uint8Array>();
  const fromUp = new TransformStream<Uint8Array, Uint8Array>();
  let closeUp: () => Promise<void> = async () => {};
  const opened = (async () => {
    const args: Record<string, unknown> = { address };
    if (options.tls) args.tls = options.tls;
    if (self) args.as = self;
    const r = await tw.tcpConnect!(args);
    if (!r || !r.ok) throw TailnetError.from((r as any)?.error);
    return r.value;
  })();
  const closed = opened.then(
    async (up) => {
      const a = new AbortController();
      closeUp = async () => a.abort();
      await Promise.allSettled([
        toUp.readable.pipeTo(up.writable, { signal: a.signal }),
        up.readable.pipeTo(fromUp.writable, { signal: a.signal }),
      ]);
    },
    async (e) => {
      await Promise.allSettled([toUp.readable.cancel(e), fromUp.writable.abort(e)]);
      throw e;
    },
  );
  closed.catch(() => {});
  return {
    readable: fromUp.readable,
    writable: toUp.writable,
    opened: opened.then(() => ({ remoteAddress: address })),
    closed,
    async close() {
      await closeUp();
      await Promise.allSettled([toUp.writable.abort(), fromUp.readable.cancel()]);
    },
  };
}

function define(ctx: any, tn: TailnetContext) {
  try {
    Object.defineProperty(ctx, "tailnet", { value: tn, enumerable: false, configurable: true });
  } catch {
    // a frozen ctx: leave ctx.tailnet absent rather than fail the request
  }
}

/**
 * installAlarmShim: workerd facets cannot set alarms ("Facets currently
 * cannot set alarms"), so a facet's ctx.storage alarm methods are delegated
 * to its parent Host DO (a real DO, same object identity), which owns the
 * physical alarm and calls the facet's alarm() when it fires. This is what
 * makes the Agents SDK's schedule()/scheduleEvery() work in facet mode.
 */
function installAlarmShim(ctx: any, tw: TailnetApiStub) {
  const st = ctx?.storage;
  if (!st || !tw) return;
  const call = async (action: string, time?: number) => {
    const r = await tw.call("alarm", { action, time });
    if (!r || !r.ok) throw TailnetError.from((r as any)?.error);
    return r.value;
  };
  const shim: Record<string, (...a: any[]) => Promise<any>> = {
    async setAlarm(t: number | Date) {
      const ms = typeof t === "number" ? t : t instanceof Date ? t.getTime() : Number(t);
      if (!Number.isFinite(ms)) throw new TypeError("setAlarm: invalid time");
      await call("set", ms);
    },
    async getAlarm() {
      return (await call("get")) ?? null;
    },
    async deleteAlarm() {
      await call("delete");
    },
  };
  for (const [k, fn] of Object.entries(shim)) {
    try {
      Object.defineProperty(st, k, { value: fn, configurable: true, writable: true });
    } catch {
      /* non-extensible storage: alarms stay unsupported in this facet */
    }
  }
}

/** wrapFacetClass wraps a tenant DO class run as a facet (per-object egress). */
export function wrapFacetClass(C: any, name: string, bindings: BindingMap, opts: { container?: ContainerSpec; workflows?: Record<string, string> } = {}) {
  if (typeof C !== "function") throw new Error(`tw: durable_objects class ${name} is not exported by the main module`);
  const W = {
    [name]: class extends C {
      constructor(ctx: any, env: any) {
        const tw = env?.__TW;
        // Bindings admit before they reach an object (chained creation by
        // an attested object; hostd records it), and __TW stays platform-only.
        // Its namespace bindings are NsProxy stubs: facetns.ts gives them
        // idFromName and get() (#391).
        const e = guardEnv(facetNamespaces(env, bindings), bindings, "tailnet", admitter(tw, {}));
        // Workflow bindings: the platform's WorkflowGate, as the Workflow API.
        for (const k of Object.keys(opts.workflows ?? {})) if (e?.[k]) e[k] = workflowClient(e[k]);
        define(ctx, createTailnetContext(tw, e, bindings));
        installAlarmShim(ctx, tw);
        // ctx.container: the object is attested by the platform (props).
        if (tw && opts.container) defineContainer(ctx, createContainer(tw, ctx, name, opts.container));
        super(ctx, e);
        twOf.set(this, tw);
        // RPC from the Host and the delegated alarm are traced (rpctrace.ts).
        return traceGate(this, entryInfo(this, name, false));
      }
      // Called by the Host DO when the delegated alarm fires.
      async __tw_alarm(info: unknown) {
        const self = this as any;
        if (typeof self.alarm === "function") return self.alarm(info);
      }
      // Called by the Host DO with a request the container sent to an
      // intercepted destination (hostd's sandbox egress proxy).
      async __tw_intercept(key: string, req: Request) {
        return dispatchIntercept((this as any).ctx, key, req);
      }
    },
  }[name];
  const ff = C.prototype?.fetch;
  if (typeof ff === "function") {
    W.prototype.fetch = function (this: any, req: Request) {
      return runTraced(req, () => ff.call(this, req), spanFlusher(twOf.get(this), this.ctx));
    };
  }
  if (opts.container) noPreviewURLs(W);
  return W;
}

// Sandbox SDK preview URLs (exposePort, proxyToSandbox) need public
// ingress (ADR-029): on tailworks they fail with a typed not_supported error
// instead of minting URLs nothing can reach.
const PREVIEW_HEADER = "x-sandbox-preview-proxy";
function noPreviewURLs(W: any) {
  const proto = Object.getPrototypeOf(W.prototype);
  if (typeof proto?.exposePort === "function") {
    W.prototype.exposePort = async function () {
      throw notSupportedPreview();
    };
  }
  const f = W.prototype.fetch;
  if (typeof f === "function") {
    W.prototype.fetch = async function (this: any, req: Request) {
      if (req?.headers?.get?.(PREVIEW_HEADER)) {
        const e = notSupportedPreview();
        return new Response(JSON.stringify({ code: "not_supported", message: e.message }), {
          status: 501,
          headers: { "content-type": "application/json", "tw-error": "not_supported" },
        });
      }
      return f.call(this, req);
    };
  }
}

/** killedMark reads a plain DO's kill marker (set by __tw_kill), if its storage has one. */
function killedMark(ctx: any): string | undefined {
  try {
    return ctx?.storage?.kv?.get?.("__tw_killed") ?? undefined;
  } catch {
    return undefined;
  }
}

// Entry points that check the kill marker themselves (fetch answers 410)
// or must work on a killed object (__tw_kill).
const KILL_SELF_GATED = new Set(["constructor", "fetch", "__tw_kill", "__tw_intercept"]);
// Runtime-invoked handlers: on a killed object they do nothing (throwing
// would only make workerd retry an alarm).
const KILL_NOOP = new Set(["alarm", "webSocketMessage", "webSocketClose", "webSocketError"]);

function findDescriptor(o: any, p: string): PropertyDescriptor | undefined {
  for (let x = o; x && x !== Object.prototype; x = Object.getPrototypeOf(x)) {
    const d = Object.getOwnPropertyDescriptor(x, p);
    if (d) return d;
  }
  return undefined;
}

/**
 * killGate returns the object a plain DO's constructor hands workerd: a
 * Proxy whose every method (its RPC entry points and runtime handlers)
 * checks the kill marker before tenant code runs (#211), as the facet
 * Host's Proxy does in the platform worker. Only lookups from outside go
 * through it: methods run with `this` the real object, so private fields
 * and the object's own `this.method` lookups (decorator metadata, e.g.
 * the Agents SDK's @callable) are unchanged.
 */
function killGate(obj: any, cls: string, info: EntryInfo): any {
  const killedErr = (k: string) => new Error("[killed] object " + cls + "/" + (obj.ctx?.id?.name ?? "") + " was killed: " + k);
  const wrapped = new Map<PropertyKey, { v: Function; w: Function }>();
  return new Proxy(obj, {
    get(t, p) {
      if (typeof p === "string" && !KILL_SELF_GATED.has(p)) {
        const k = killedMark(t.ctx);
        if (k && findDescriptor(t, p)?.get) throw killedErr(k); // a getter is tenant code
      }
      const v = Reflect.get(t, p, t);
      if (typeof v !== "function" || p === "constructor") return v;
      const c = wrapped.get(p);
      if (c && c.v === v) return c.w;
      const gate = typeof p === "string" && !KILL_SELF_GATED.has(p);
      const w = function (...args: unknown[]) {
        if (gate) {
          const k = killedMark(t.ctx);
          if (k) {
            if (KILL_NOOP.has(p as string)) return Promise.resolve(undefined);
            throw killedErr(k); // over RPC the caller's promise rejects with it
          }
        }
        return tracedEntry(info, p, args, (a) => v.apply(t, a)); // rpctrace.ts
      };
      wrapped.set(p, { v, w });
      return w;
    },
    set(t, p, v) {
      return Reflect.set(t, p, v, t);
    },
  });
}

/**
 * wrapPlainClass wraps a tenant DO class run as a plain DO (egress:
 * "workload"): alarms work, egress leaves as the workload node. The wrapper
 * tells hostd the object exists (so its node comes up) and enforces kill.
 */
export function wrapPlainClass(C: any, name: string, bindings: BindingMap, opts: { node: boolean; container?: ContainerSpec }) {
  if (typeof C !== "function") throw new Error(`tw: durable_objects class ${name} is not exported by the main module`);
  const proto = C.prototype;
  const killedResponse = (reason: string) =>
    new TailnetError("killed", `object ${name} was killed: ${reason}`).toResponse();
  const W: any = {
    [name]: class extends C {
      constructor(ctx: any, env: any) {
        const tw = env?.__TW;
        const oname = ctx?.id?.name;
        const self = oname ? { class: name, name: oname } : undefined;
        const e = guardEnv(env, bindings, "tailnet", admitter(tw, self ? { as: self } : {}));
        if (tw && oname) define(ctx, createTailnetContext(tw, e, bindings, self));
        if (tw && oname && opts.container) defineContainer(ctx, createContainer(tw, ctx, name, opts.container, { class: name, name: oname }));
        super(ctx, e);
        twOf.set(this, tw);
        const gated = killGate(this, name, entryInfo(this, name, true));
        if (tw && oname && opts.node && !killedMark(ctx)) {
          // The object's node comes up only if its creation was admitted
          // (RT3-06/07); fetch answers the refusal below. A killed object
          // asks for none (#153): fetch answers 410.
          const p = tw.call("ensure", { as: { class: name, name: oname } }).then((r) => {
            if (r && !r.ok) {
              const err = TailnetError.from((r as any).error);
              if (err.code === "create_not_admitted") (this as any).__tw_denied = err;
            }
          }, () => {});
          (this as any).__tw_ensure = p;
          try {
            ctx.waitUntil?.(p);
          } catch {
            /* ignore */
          }
        }
        return gated;
      }
      async __tw_kill(reason: string) {
        (this as any).ctx.storage.kv.put("__tw_killed", reason || "killed");
        const oname = (this as any).ctx?.id?.name;
        if (oname) forgetAdmitted(name, oname); // #153: the next call admits again
        return true;
      }
      async __tw_intercept(key: string, req: Request) {
        const self = this as any;
        const k = self.ctx.storage.kv.get("__tw_killed");
        if (k) return killedResponse(k);
        return dispatchIntercept(self.ctx, key, req);
      }
    },
  }[name];
  if (typeof proto.fetch === "function") {
    W.prototype.fetch = async function (this: any, req: Request) {
      const k = this.ctx.storage.kv.get("__tw_killed");
      if (k) return killedResponse(k);
      req = await sealedRequest(twOf.get(this), req); // seal.ts (#148)
      if (this.__tw_ensure) await this.__tw_ensure;
      if (this.__tw_denied) return (this.__tw_denied as TailnetError).toResponse();
      return runTraced(req, () => proto.fetch.call(this, req), spanFlusher(twOf.get(this), this.ctx));
    };
  }
  if (opts.container) noPreviewURLs(W);
  return W;
}

// ---- create admission (RT3-06/07; public requests: ADR-029 D5) ----
//
// Every new object name mints a tailnet node, so creating one is a platform
// decision: hostd "admit". Tenant code reaches Durable Objects only through
// a guarded env whose namespaces admit a name before any call reaches it:
//   - a tailnet request admits with its create ticket (cf.tw.ticket, which
//     only hostd sets and verifies);
//   - an object admits as itself (attested by the platform; chained
//     creation is the app's logic);
//   - a public request (request.cf.tailscale.requester.kind === "public",
//     set only by netd) admits only names its class opens with
//     public.create, under its cap; its ctx.tailnet cannot spawn or kill.
// A raw binding (import { env } from "cloudflare:workers", an env cached in
// a global, __TW itself) admits nothing: hostd refuses to create for it
// (create_not_admitted), so bypassing the guard loses the ability to mint
// nodes rather than gaining it. Existing objects are reachable either way.

function isPublicReq(req: any): boolean {
  return req?.cf?.tailscale?.requester?.kind === "public";
}

type Admitter = (cls: string, name: string) => Promise<{ ok: boolean; error?: Record<string, any> } | undefined>;

/** admitter builds the admit call for one env: extra is {ticket} for a request, {as} for a plain object, {} for a facet. */
function admitter(tw: TailnetApiStub | undefined, extra: Record<string, unknown>): Admitter {
  return async (cls, name) => {
    if (!tw || typeof tw.call !== "function") return { ok: false, error: { code: "create_not_admitted", message: "tw platform binding missing; refusing to reach a Durable Object" } };
    return tw.call("admit", { class: cls, name, ...extra });
  };
}

const ADMIT_CACHE_MAX = 10_000;
const ADMIT_CACHE_TTL_MS = 60_000;
// "class\nname" -> expiry, for objects hostd said exist (bounded, short-
// lived). Only "existing" answers are kept: an admission to create is
// one-shot in hostd, and a cached one let a later call skip admit after
// the object was killed and purged (#153, ledger H19). A plain DO's kill
// evicts its entry (forgetAdmitted); the TTL bounds the rest (a facet's
// kill runs in the platform worker, not in this isolate).
const admitted = new Map<string, number>();

function remember(key: string) {
  admitted.delete(key);
  if (admitted.size >= ADMIT_CACHE_MAX) {
    const first = admitted.keys().next().value;
    if (first !== undefined) admitted.delete(first);
  }
  admitted.set(key, Date.now() + ADMIT_CACHE_TTL_MS);
}

/** forgetAdmitted drops class/name from this isolate's admit cache (its object was killed). */
function forgetAdmitted(cls: string, name: string) {
  admitted.delete(cls + "\n" + name);
}

async function admit(adm: Admitter, cls: string, name: string): Promise<void> {
  const key = cls + "\n" + name;
  const exp = admitted.get(key);
  if (exp !== undefined) {
    if (Date.now() < exp) return;
    admitted.delete(key);
  }
  const r = await adm(cls, name);
  if (!r || !r.ok) throw TailnetError.from((r as any)?.error ?? { code: "create_not_admitted", message: "admit refused" });
  if ((r as any).value?.existing === true) remember(key);
}

function denied(msg: string): never {
  throw new TailnetError("public_create_denied", msg);
}

function guardStub(adm: Admitter, cls: string, name: string, real: any): any {
  return new Proxy(
    {},
    {
      get(_t, p) {
        if (p === "then") return undefined;
        if (p === "id" || p === "name") return real[p];
        if (typeof p === "symbol") return real[p];
        return async (...args: unknown[]) => {
          await admit(adm, cls, name);
          // Inside a facet, env.X is an NsProxy stub and getByName returns an
          // RpcPromise: RPC methods pipeline on it, fetch() does not, so
          // settle it first.
          const target = real && typeof real.then === "function" ? await real : real;
          // A stub fetch() is a traced hop (its callee's parent span).
          if (p === "fetch") return tracedStubFetch(cls, name, args, (...a) => target.fetch(...a));
          // RPC: a traced hop too, its context passed as a marker argument.
          return rpcCall(cls, name, String(p), args, (...a) => target[p](...a));
        };
      },
    },
  );
}

type GuardMode = "tailnet" | "public";

function guardNamespace(adm: Admitter, cls: string, ns: any, mode: GuardMode): any {
  return Object.freeze({
    getByName(name: unknown) {
      const n = String(name);
      return guardStub(adm, cls, n, ns.getByName(n));
    },
    idFromName(name: unknown) {
      return ns.idFromName(name);
    },
    idFromString(s: unknown) {
      return ns.idFromString(s);
    },
    get(id: any, opts?: unknown) {
      const n = id?.name;
      if (typeof n !== "string" || !n) {
        if (mode === "public") denied(`a public request may only address ${cls} objects by name (idFromName/getByName)`);
        return ns.get(id, opts); // a nameless object gets no node: nothing to admit
      }
      return guardStub(adm, cls, n, ns.get(id, opts));
    },
    newUniqueId(opts?: unknown) {
      if (mode === "public") denied(`a public request may not create a new ${cls} object (newUniqueId)`);
      return ns.newUniqueId(opts); // no name, no node
    },
    jurisdiction(j: unknown) {
      return guardNamespace(adm, cls, ns.jurisdiction(j), mode);
    },
  });
}

/** guardEnv returns env with every DO binding guarded and the platform binding removed. */
function guardEnv(env: any, bindings: BindingMap, mode: GuardMode, adm: Admitter): any {
  if (!env || typeof env !== "object") return env;
  const out: Record<string, unknown> = { ...env };
  delete out.__TW;
  wrapStores(out); // a facet's storage bindings (D1)
  for (const [k, cls] of Object.entries(bindings)) {
    if (env[k]) out[k] = guardNamespace(adm, cls, env[k], mode);
  }
  return out;
}

/**
 * guardTailnetEnv guards env as for a tailnet caller without a create
 * ticket (Workflows). Its admits say they are a Workflow's, so hostd lets
 * them reach objects of classes with no node (no admission applies to
 * those), which a public request may not (#359).
 */
export function guardTailnetEnv(env: any, bindings: BindingMap): any {
  return guardEnv(env, bindings, "tailnet", admitter(env?.__TW, { workflow: true }));
}

/** publicEnv returns env guarded for a public request (kept for callers of the old name). */
export function publicEnv(env: any, bindings: BindingMap): any {
  return guardEnv(env, bindings, "public", admitter(env?.__TW, {}));
}

/** publicTailnet is ctx.tailnet for a public request: no spawn, no kill. */
function publicTailnet(tn: TailnetContext): TailnetContext {
  return Object.freeze({
    ...tn,
    spawn(): Promise<never> {
      return Promise.reject(new TailnetError("public_create_denied", "a public request may not spawn objects"));
    },
    kill(): Promise<never> {
      return Promise.reject(new TailnetError("public_request", "a public request may not kill objects"));
    },
  }) as TailnetContext;
}

// asResponse renders errors the platform throws through tenant code.
function asResponse(e: unknown): Response | null {
  const m = String((e as Error)?.message ?? e);
  const i = m.indexOf("[killed]");
  if (i >= 0) return new TailnetError("killed", m.slice(i + 8).trim()).toResponse();
  const j = m.indexOf("[create_not_admitted]");
  if (j >= 0) return new TailnetError("create_not_admitted", m.slice(j + 21).trim()).toResponse();
  if (isTailnetError(e) && (e.code === "public_create_denied" || e.code === "public_request" || e.code === "create_not_admitted" || e.code === "killed")) return e.toResponse();
  return null;
}

/** requestTicket is the create ticket hostd stamped on a tailnet request or cron run. */
function requestTicket(req: any): string | undefined {
  const t = req?.cf?.tw?.ticket;
  return typeof t === "string" && t ? t : undefined;
}

/** isScheduledReq: the platform dispatching a cron run (cf.tw.kind "scheduled", set by hostd). */
function isScheduledReq(req: any): boolean {
  return req?.cf?.tw?.kind === "scheduled";
}

/**
 * scheduledCtx wraps a cron run's ExecutionContext so that ctx.waitUntil()
 * work belongs to the run, as on Cloudflare (a cron event lasts until its
 * waitUntil promises settle). Promises are collected, not handed to the
 * request's own waitUntil, so the run's answer waits for them and hostd's
 * cron timeout, which drops the connection, cancels them with the run
 * instead of leaving them running after the next tick starts (H17, #152).
 */
function scheduledCtx(ctx: any): { ctx: any; drain: () => Promise<void> } {
  const pending: Promise<unknown>[] = [];
  const drain = async () => {
    while (pending.length) await Promise.allSettled(pending.splice(0));
  };
  if (!ctx || typeof ctx !== "object") return { ctx, drain };
  const waitUntil = (p: unknown) => {
    pending.push(Promise.resolve(p));
  };
  const proxy = new Proxy(ctx, {
    get(t, k) {
      if (k === "waitUntil") return waitUntil;
      const v = Reflect.get(t, k, t);
      return typeof v === "function" ? v.bind(t) : v;
    },
  });
  return { ctx: proxy, drain };
}

/**
 * runScheduled dispatches a cron run to the handler's scheduled() with a
 * controller (cron, scheduledTime, type, noRetry()) and answers the
 * platform's {outcome, noRetry, error} record once scheduled() and its
 * waitUntil work (drain) have finished.
 */
async function runScheduled(
  scheduledFn: ((c: any, ...rest: any[]) => unknown) | undefined,
  req: any,
  extra: unknown[],
  drain: () => Promise<void>,
): Promise<Response> {
  const t = req.cf.tw;
  let noRetry = false;
  const controller = {
    cron: String(t.cron ?? ""),
    scheduledTime: Number(t.scheduledTime),
    type: "scheduled",
    noRetry() {
      noRetry = true;
    },
  };
  if (typeof scheduledFn !== "function") {
    return Response.json({ outcome: "exception", noRetry: false, error: "the default export has no scheduled() handler" });
  }
  try {
    await scheduledFn(controller, ...extra);
    await drain();
    return Response.json({ outcome: "ok", noRetry });
  } catch (e) {
    await drain();
    return Response.json({ outcome: "exception", noRetry, error: String((e as any)?.message ?? e).slice(0, 512) });
  }
}

/**
 * QUEUE_TICKET_ID: the platform's first message of a queue batch carries the
 * delivery's create ticket in its body (message IDs come from mgmt, so no
 * producer can send one). queueBatch strips it before the handler sees the
 * batch; hostd verifies the ticket like a request's.
 */
const QUEUE_TICKET_ID = "__tw_ticket";

function queueBatch(batch: any): { batch: any; ticket?: string } {
  const msgs: any[] = Array.isArray(batch?.messages) ? batch.messages : [];
  const t = msgs.find((m) => m?.id === QUEUE_TICKET_ID);
  if (!t) return { batch };
  const rest = Object.freeze(msgs.filter((m) => m?.id !== QUEUE_TICKET_ID));
  const b = Object.freeze({
    queue: batch.queue,
    messages: rest,
    ackAll: () => batch.ackAll(),
    retryAll: (o?: unknown) => batch.retryAll(o),
  });
  return { batch: b, ticket: typeof t.body === "string" && t.body ? t.body : undefined };
}

/** wrapHandler installs ctx.tailnet (workload identity) on the default export. */
// HANDLER_METHODS are the Workers handler entry points. A default export
// that is a class instance (`export default new OAuthProvider(...)`, as
// @cloudflare/workers-oauth-provider documents) has them on its prototype,
// not as own properties, so they are looked up along the prototype chain.
const HANDLER_METHODS = ["fetch", "scheduled", "queue", "tail", "tailStream", "trace", "email", "test", "connect"];

function handlerKeys(H: any): string[] {
  const keys = new Set(Object.keys(H));
  for (const k of HANDLER_METHODS) if (typeof H[k] === "function") keys.add(k);
  return [...keys];
}

export function wrapHandler(H: any, bindings: BindingMap) {
  if (!H) {
    return {
      fetch: () => new TailnetError("unknown_object", "this app has no default export (Worker); address objects via their own nodes or /_tw/o/<Class>/<name>/").toResponse(),
    };
  }
  if (typeof H === "function") {
    // A WorkerEntrypoint class (one instance per request).
    const C = class extends H {
      constructor(ctx: any, env: any) {
        const tw = env?.__TW;
        const e = guardEnv(env, bindings, "tailnet", admitter(tw, {}));
        define(ctx, createTailnetContext(tw, e, bindings));
        super(ctx, e);
        (this as any).__tw = tw;
      }
    };
    if (typeof H.prototype?.fetch === "function") {
      const f = H.prototype.fetch;
      (C.prototype as any).fetch = async function (this: any, req: Request) {
        const tw: TailnetApiStub | undefined = this.__tw;
        req = await sealedRequest(tw, req); // seal.ts (#148)
        if (isPublicReq(req)) {
          this.env = guardEnv(this.env, bindings, "public", admitter(tw, {}));
          if (tw) define(this.ctx, publicTailnet(createTailnetContext(tw, this.env, bindings)));
        } else {
          const ticket = requestTicket(req);
          this.env = guardEnv(this.env, bindings, "tailnet", admitter(tw, { ticket }));
          if (tw) define(this.ctx, createTailnetContext(tw, this.env, bindings, undefined, ticket));
          if (isScheduledReq(req)) {
            const sc = scheduledCtx(this.ctx);
            const flush = spanFlusher(tw, this.ctx);
            this.ctx = sc.ctx;
            const sf = typeof this.scheduled === "function" ? this.scheduled.bind(this) : undefined;
            // hostd opens the run's root span and sends it as traceparent.
            return runTraced(req, () => runScheduled(sf, req, [], sc.drain), flush);
          }
        }
        try {
          return await runTraced(req, () => f.call(this, req), spanFlusher(tw, this.ctx));
        } catch (e) {
          const r = asResponse(e);
          if (r) return r;
          throw e;
        }
      };
    }
    if (typeof H.prototype?.queue === "function") {
      const qf = H.prototype.queue;
      (C.prototype as any).queue = async function (this: any, batch: any) {
        const tw: TailnetApiStub | undefined = this.__tw;
        const q = queueBatch(batch);
        this.env = guardEnv(this.env, bindings, "tailnet", admitter(tw, { ticket: q.ticket }));
        if (tw) define(this.ctx, createTailnetContext(tw, this.env, bindings, undefined, q.ticket));
        return qf.call(this, q.batch);
      };
    }
    return { [H.name || "Default"]: C }[H.name || "Default"];
  }
  const out: Record<string, unknown> = { ...H };
  for (const k of handlerKeys(H)) {
    if (typeof H[k] !== "function") continue;
    const inner = function (this: unknown, a: unknown, env: any, ctx: any) {
      const tw: TailnetApiStub | undefined = env?.__TW;
      const pub = k === "fetch" && isPublicReq(a);
      if (pub) {
        env = guardEnv(env, bindings, "public", admitter(tw, {}));
        if (ctx && tw) define(ctx, publicTailnet(createTailnetContext(tw, env, bindings)));
      } else {
        // The create ticket rides on the request (a cron run is dispatched
        // as a fetch with cf.tw.kind "scheduled" and its own ticket), or on
        // a queue batch's first message.
        let ticket: string | undefined;
        if (k === "fetch") ticket = requestTicket(a);
        else if (k === "queue") ({ batch: a, ticket } = queueBatch(a));
        env = guardEnv(env, bindings, "tailnet", admitter(tw, { ticket }));
        if (ctx && tw) define(ctx, createTailnetContext(tw, env, bindings, undefined, ticket));
        if (k === "fetch" && isScheduledReq(a)) {
          const sc = scheduledCtx(ctx);
          // hostd opens the run's root span and sends it as traceparent.
          return runTraced(a, () => runScheduled(H.scheduled, a, [env, sc.ctx], sc.drain), spanFlusher(tw, ctx));
        }
      }
      let r: unknown;
      try {
        r = k === "fetch" ? runTraced(a, () => H[k].call(H, a, env, ctx), spanFlusher(tw, ctx)) : H[k].call(H, a, env, ctx);
      } catch (e) {
        const resp = k === "fetch" ? asResponse(e) : null;
        if (resp) return resp;
        throw e;
      }
      if (k !== "fetch") return r;
      // A call into a killed object throws "[killed] …" from the platform;
      // answer the caller with a typed 410 instead of an opaque 500. A
      // public create refusal that escapes the handler becomes its 403.
      return Promise.resolve(r).catch((e: unknown) => {
        const resp = asResponse(e);
        if (resp) return resp;
        throw e;
      });
    };
    out[k] = k !== "fetch" ? inner : function (this: unknown, a: unknown, env: any, ctx: any) {
      return sealedRequest(env?.__TW, a).then((r) => inner.call(this, r, env, ctx)); // seal.ts (#148)
    };
  }
  return out;
}

// ---- Service bindings, the callee side (ADR-053) ----
//
// The platform reaches the tenant main module's __tw_binding entrypoint
// (generated by hostd) for a service binding call to a named entrypoint or
// an RPC method, which hostd admitted only from a tailworks node of this
// tenant. The entrypoint is wrapped like the default export (guarded env,
// ctx.tailnet, sealed caller). An RPC method sees its caller as
// getCaller(this.ctx): ctx.cf is the call's cf, with only a caller hostd
// sealed.

const wrappedEntrypoints = new WeakMap<object, any>();

/** callCtx is ctx with cf (the call's) for getCaller(this.ctx). */
function callCtx(ctx: any, cf: unknown): any {
  if (!ctx || typeof ctx !== "object") return ctx;
  return new Proxy(ctx, {
    get(t, k) {
      if (k === "cf") return cf;
      const v = Reflect.get(t, k, t);
      return typeof v === "function" ? v.bind(t) : v;
    },
  });
}

export interface BindingDispatcher {
  fetch(ctx: any, env: any, req: Request): Promise<Response>;
  call(ctx: any, env: any, entrypoint: string, method: string, args: unknown[], cf: any): Promise<unknown>;
}

// PY_RPC marks a Python Worker's generated front (hostd/gen/python.go,
// ADR-055): its default export forwards an RPC call, to any entrypoint, to
// the Python worker, which has the entrypoint classes the front can't see.
const PY_RPC = Symbol.for("tailworks.pyRpc");

/** pyCallCf is the part of a call's sealed cf a Python method sees as self.ctx.cf (no ticket). */
function pyCallCf(scf: any): Record<string, unknown> {
  const { tw: _tw, ...rest } = scf ?? {};
  return rest;
}

export function bindingDispatcher(T: Record<string, any>, bindings: BindingMap): BindingDispatcher {
  const target = (ep: string) => {
    const H = ep ? T[ep] : T.default;
    if (!H || (typeof H !== "function" && typeof H !== "object")) {
      throw new TailnetError("service_not_found", ep ? `this workload's main module exports no entrypoint ${ep}` : "this workload has no default export");
    }
    let W = wrappedEntrypoints.get(H);
    if (!W) {
      W = wrapHandler(H, bindings);
      wrappedEntrypoints.set(H, W);
    }
    return { H, W, name: ep || "default" };
  };
  return {
    async fetch(ctx, env, req) {
      const ep = String((req as any)?.cf?.tw?.binding?.entrypoint ?? "");
      if (ep && typeof T.default?.[PY_RPC] === "function" && !(ep in T)) {
        return new TailnetError("service_not_found", `entrypoint ${ep} of a Python Worker answers RPC calls, not fetch(); bind the default entrypoint for fetch()`).toResponse();
      }
      let t;
      try {
        t = target(ep);
      } catch (e) {
        return (e as TailnetError).toResponse();
      }
      if (typeof t.H === "function") {
        if (typeof t.W.prototype?.fetch !== "function") return new TailnetError("service_not_found", `entrypoint ${t.name} has no fetch() handler`).toResponse();
        const inst = new t.W(ctx, env);
        return inst.fetch(req);
      }
      if (typeof t.W.fetch !== "function") return new TailnetError("service_not_found", `entrypoint ${t.name} has no fetch() handler`).toResponse();
      return t.W.fetch(req, env, ctx);
    },
    async call(ctx, env, ep, method, args, cf) {
      const tw: TailnetApiStub | undefined = env?.__TW;
      const sealed: any = await sealedRequest(tw, { cf });
      const scf = sealed?.cf ?? {};
      const py = T.default?.[PY_RPC];
      if (typeof py === "function") {
        // A Python Worker: the same refusals, then the call goes to the
        // Python worker, which checks the entrypoint and method itself.
        if (typeof method !== "string" || method in Object.prototype || HANDLER_METHODS.includes(method) || method === "constructor") {
          throw new TypeError(`The RPC receiver does not implement the method "${method}".`);
        }
        const ticket = typeof scf?.tw?.ticket === "string" && scf.tw.ticket ? scf.tw.ticket : undefined;
        const genv = guardEnv(env, bindings, "tailnet", admitter(tw, { ticket }));
        return await py(genv, ep || "", method, args, pyCallCf(scf));
      }
      const t = target(ep);
      if (typeof t.H !== "function") throw new TypeError(`entrypoint ${t.name} is not a WorkerEntrypoint class; RPC needs one`);
      const cctx = callCtx(ctx, scf);
      const inst = new t.W(cctx, env);
      const ticket = typeof scf?.tw?.ticket === "string" && scf.tw.ticket ? scf.tw.ticket : undefined;
      if (ticket) {
        inst.env = guardEnv(env, bindings, "tailnet", admitter(tw, { ticket }));
        if (tw) define(cctx, createTailnetContext(tw, inst.env, bindings, undefined, ticket));
      }
      const f = typeof method === "string" && !(method in Object.prototype) ? inst[method] : undefined;
      if (typeof f !== "function" || HANDLER_METHODS.includes(method) || method === "constructor") {
        throw new TypeError(`The RPC receiver does not implement the method "${method}".`);
      }
      return await f.apply(inst, args);
    },
  };
}
