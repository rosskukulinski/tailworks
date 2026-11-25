// @tailworks/tailnet/testing: unit-test helpers for the tw-specific APIs
// (getCaller, getRequester, ctx.tailnet). Works in Node (node:test, vitest)
// and inside workerd (@cloudflare/vitest-plugin). docs/testing.md has the
// whole story.
//
// Why this can't weaken a deployed app: on tailworks, request.cf is not something
// a client can send. netd (the only process that can connect to workerd's
// ingress socket) drops any client-sent x-tw-cf header and writes its own,
// built from WireGuard + WhoIs; workerd turns that header into request.cf
// (cfBlobHeader) and removes it. withCaller() adds no header and talks to no
// socket: it builds an in-memory Request object whose `cf` property is set,
// in the test's own process. A request that arrives over the network never
// passes through it. On top of that, withCaller() refuses to run unless the
// test opted in (createTestTailnet() or enableTestCallers()), so a stray
// import in app code throws instead of quietly minting callers.

import { TailnetError, fromResponse } from "./errors.js";
import type { SocketAddress, SpawnOptions, Spawned, TailnetContext, TailnetSelf, TailnetSocket, TailnetSocketOptions } from "./runtime.js";

/** The MagicDNS suffix fake callers and fake objects get. */
export const TEST_DOMAIN = "test.ts.net";

/** Who a fake caller is. A tailnet node is either a user's (login) or tagged (tags), never both. */
export interface TestCaller {
  /** The user's login, for an untagged (human) node. */
  login?: string;
  /** The node's tags, for a tagged node (a server, an agent, CI). */
  tags?: string[];
  /** Stable node ID (default: derived from the login or the first tag). */
  nodeId?: string;
  /** Short hostname (default: the login's local part, or the first tag's name). */
  hostname?: string;
  /** MagicDNS FQDN (default: hostname + "." + TEST_DOMAIN). */
  node?: string;
  addrs?: string[];
  /** App capabilities granted to this caller: name → values (what caller.caps(name) returns). */
  caps?: Record<string, unknown[]>;
  /** Set for a caller that is itself a tailworks object (ADR-010). */
  tw?: { objectId: string; lim?: Record<string, unknown>; parent?: string; root?: string };
  /** netd could not verify the caller's node certificate (caps() is empty). */
  capsWithheld?: boolean;
  /** request.cf.objectId: the object the request was delivered to. */
  objectId?: string;
  /** request.cf.traceId (default: a random one). */
  traceId?: string;
}

/** A fake public (Funnel) requester: no identity, just the client address. */
export interface TestPublicRequester {
  ip?: string;
  port?: number;
  host?: string;
  traceId?: string;
}

// Test mode: set by createTestTailnet() or enableTestCallers(). Module
// state, so it holds for this copy of the module only (a bundled app has
// its own copy, which a test never enabled).
let testMode = false;

/**
 * enableTestCallers is the explicit test-only switch for the top-level
 * withCaller()/withPublic(). Call it in a test setup file. createTestTailnet()
 * turns it on too.
 */
export function enableTestCallers(): void {
  testMode = true;
}

/** disableTestCallers turns test mode off again (for tests of this module). */
export function disableTestCallers(): void {
  testMode = false;
}

function requireTestMode(fn: string): void {
  if (!testMode) {
    throw new TailnetError(
      "no_identity",
      `${fn}() is a test helper: call enableTestCallers() or createTestTailnet() in your test setup first (it never runs in a deployed app)`,
    );
  }
}

function slug(s: string): string {
  return s.toLowerCase().replace(/^tag:/, "").replace(/@.*$/, "").replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "") || "test";
}

function randomTrace(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

/** wireCaller is what netd puts in request.cf.tailscale.caller. */
function wireCaller(who: TestCaller): Record<string, unknown> {
  if (!who || typeof who !== "object") throw new TailnetError("bad_request", "withCaller: want {login} or {tags}");
  const tags = [...(who.tags ?? [])];
  if (who.login && tags.length) {
    throw new TailnetError("bad_request", "withCaller: a tailnet node has a user (login) or tags, not both");
  }
  if (!who.login && !tags.length && !who.nodeId) {
    throw new TailnetError("bad_request", "withCaller: want {login} or {tags} (or at least a nodeId)");
  }
  for (const t of tags) if (!/^tag:[A-Za-z0-9-]+$/.test(t)) throw new TailnetError("bad_request", `withCaller: ${JSON.stringify(t)} is not a tag (tag:<name>)`);
  const hostname = who.hostname ?? slug(who.login ?? tags[0] ?? who.nodeId ?? "test");
  const c: Record<string, unknown> = {
    nodeId: who.nodeId ?? "test-" + slug(who.login ?? tags.join("-") ?? hostname),
    hostname,
    node: who.node ?? `${hostname}.${TEST_DOMAIN}`,
    addrs: [...(who.addrs ?? [])],
    tags,
    capMap: structuredClone(who.caps ?? {}),
  };
  if (who.login) c.user = who.login;
  if (who.tw) c.tw = structuredClone(who.tw);
  if (who.capsWithheld) c.capsWithheld = true;
  return c;
}

/**
 * attachCf returns a copy of input whose request.cf is cf merged over the
 * input's own. In workerd, `new Request(x, {cf})` sets request.cf and it
 * survives a service binding and a Durable Object stub's fetch(); in Node
 * (undici ignores `cf`) the property is defined on the copy instead.
 */
function attachCf(input: Request | string | URL, cf: Record<string, any>): Request {
  const base = input instanceof Request ? input : new Request(String(input));
  const r = new Request(base, { cf } as RequestInit);
  if ((r as any).cf?.tailscale === undefined) {
    Object.defineProperty(r, "cf", { value: cf, enumerable: true, configurable: true });
  }
  return r;
}

function callerCf(input: Request | string | URL, who: TestCaller): Request {
  const prev = input instanceof Request ? ((input as any).cf ?? {}) : {};
  const { requester: _r, ...ts } = prev.tailscale ?? {};
  const cf: Record<string, any> = { ...prev, tailscale: { ...ts, caller: wireCaller(who) }, traceId: who.traceId ?? prev.traceId ?? randomTrace() };
  if (who.objectId !== undefined) cf.objectId = who.objectId;
  return attachCf(input, cf);
}

function publicCf(input: Request | string | URL, p: TestPublicRequester = {}): Request {
  const prev = input instanceof Request ? ((input as any).cf ?? {}) : {};
  const { caller: _c, ...ts } = prev.tailscale ?? {};
  const url = new URL(input instanceof Request ? input.url : String(input));
  const requester: Record<string, unknown> = { kind: "public", ip: p.ip ?? "203.0.113.7", proto: "https", host: p.host ?? url.host };
  if (typeof p.port === "number") requester.port = p.port;
  return attachCf(input, { ...prev, tailscale: { ...ts, requester }, traceId: p.traceId ?? prev.traceId ?? randomTrace() });
}

/**
 * withCaller returns a copy of req that getCaller() sees as coming from
 * `who`, as netd would deliver it. Test mode only (see enableTestCallers).
 *
 *   const res = await worker.fetch(withCaller("http://app/", { login: "alice@example.com" }), env, ctx);
 */
export function withCaller(req: Request | string | URL, who: TestCaller): Request {
  requireTestMode("withCaller");
  return callerCf(req, who);
}

/** withPublic returns a copy of req as a public (Funnel) request: getCaller() throws public_request. Test mode only. */
export function withPublic(req: Request | string | URL, requester: TestPublicRequester = {}): Request {
  requireTestMode("withPublic");
  return publicCf(req, requester);
}

/** A ctx.tailnet.spawn() the fake runtime saw. */
export interface SpawnRecord {
  class: string;
  name: string;
  caps?: Record<string, unknown>;
  ttl?: string;
  principal?: "inherit" | "none";
  objectId: string;
}

/** A ctx.tailnet.kill() the fake runtime saw. */
export interface KillRecord {
  reason: string;
  cascade: boolean;
  target?: { class: string; name: string };
  killed: string[];
}

export interface TestTailnetOptions {
  /** The app's env, to name a spawned namespace by its binding (env.LOCK → "LOCK"). */
  env?: Record<string, any>;
  /** env binding name → Durable Object class (wrangler durable_objects.bindings), for class names in spawned. */
  bindings?: Record<string, string>;
  /** What ctx.tailnet.self() answers (defaults: a ready workload "test-workload", epoch 1). */
  self?: Partial<TailnetSelf>;
  /**
   * The egress of ctx.tailnet.fetch(). Default: every destination
   * is refused (forbidden_destination), as on tailworks with no grant. A response
   * with a tw-error header is thrown as a TailnetError, as on tailworks.
   */
  fetch?: (req: Request) => Response | Promise<Response>;
  /** Called on each spawn before it is recorded; throw (e.g. a TailnetError) to make it fail. */
  onSpawn?: (rec: SpawnRecord) => void | Promise<void>;
  /**
   * ctx.tailnet.connect(): return a socket (e.g. cloudflare:sockets
   * connect() to a test server). Default: refused with not_supported.
   */
  connect?: (address: SocketAddress, options: TailnetSocketOptions) => TailnetSocket;
}

/** A fake tw runtime: a ctx.tailnet that records what the app asked of it. */
export interface TestTailnet {
  /** The fake ctx.tailnet. */
  readonly tailnet: TailnetContext;
  readonly spawned: SpawnRecord[];
  readonly killed: KillRecord[];
  readonly audits: Array<Record<string, unknown>>;
  /** Requests the app sent through ctx.tailnet.fetch(). */
  readonly fetches: Request[];
  /** withCaller, bound to this fake runtime (no enableTestCallers() needed). */
  withCaller(req: Request | string | URL, who: TestCaller): Request;
  withPublic(req: Request | string | URL, requester?: TestPublicRequester): Request;
  /**
   * ctx returns an ExecutionContext for a Worker handler with ctx.tailnet set.
   * Pass vitest-plugin's createExecutionContext() to keep its waitUntil.
   */
  ctx<T extends object = ExecutionContextLike>(base?: T): T & { tailnet: TailnetContext };
  /** attach sets ctx.tailnet on an object (a Durable Object's state, e.g. inside runInDurableObject). */
  attach<T extends object>(state: T): T & { tailnet: TailnetContext };
  /** settle waits for everything passed to waitUntil() of a ctx() made here. */
  settle(): Promise<void>;
}

export interface ExecutionContextLike {
  waitUntil(p: Promise<unknown>): void;
  passThroughOnException(): void;
  props: Record<string, unknown>;
}

/** createTestTailnet makes a fake tw runtime, and turns test mode on. */
export function createTestTailnet(opts: TestTailnetOptions = {}): TestTailnet {
  testMode = true;
  const spawned: SpawnRecord[] = [];
  const killed: KillRecord[] = [];
  const audits: Array<Record<string, unknown>> = [];
  const fetches: Request[] = [];
  const pending: Promise<unknown>[] = [];
  const self: TailnetSelf = {
    objectId: "test-workload",
    hostname: "test-workload",
    magicDNS: `test-workload.${TEST_DOMAIN}`,
    status: "ready",
    kind: "workload",
    workload: "test-workload",
    deploymentId: "test-deployment",
    epoch: 1,
    ...opts.self,
  };
  const egress =
    opts.fetch ??
    (() => new TailnetError("forbidden_destination", "no fake egress: pass createTestTailnet({ fetch }) to answer ctx.tailnet.fetch()").toResponse());

  const classOf = (ns: unknown): string => {
    if (typeof ns === "string") return ns;
    for (const [k, v] of Object.entries(opts.env ?? {})) if (v === ns) return opts.bindings?.[k] ?? k;
    if (opts.env) throw new TailnetError("bad_request", "spawn: the namespace is not a durable_objects binding of this app");
    return "DurableObject";
  };
  const tfetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const req = new Request(input as any, init);
    fetches.push(req.clone());
    let r: Response;
    try {
      r = await egress(req);
    } catch (e) {
      throw new TailnetError("dial_failed", String((e as Error)?.message ?? e));
    }
    const err = await fromResponse(r);
    if (err) throw err;
    return r;
  };

  const tailnet: TailnetContext = Object.freeze({
    fetch: tfetch,
    async spawn<T = any>(ns: any, o: SpawnOptions): Promise<Spawned<T>> {
      if (!o || typeof o.name !== "string" || !o.name) throw new TailnetError("bad_request", "spawn: opts.name is required");
      const cls = classOf(ns);
      const rec: SpawnRecord = { class: cls, name: o.name, objectId: `test-${slug(cls)}-${slug(o.name)}` };
      if (o.caps !== undefined) rec.caps = structuredClone(o.caps);
      if (o.ttl !== undefined) rec.ttl = o.ttl;
      if (o.principal !== undefined) rec.principal = o.principal;
      await opts.onSpawn?.(rec);
      spawned.push(rec);
      const stub = typeof ns?.getByName === "function" ? await ns.getByName(o.name) : typeof ns?.idFromName === "function" ? ns.get(ns.idFromName(o.name)) : undefined;
      const sp: Spawned<T> = {
        stub,
        name: `${slug(o.name)}.${TEST_DOMAIN}`,
        objectId: rec.objectId,
        state: () => "ready",
        ready: Promise.resolve(),
      };
      return sp;
    },
    async kill(o: { reason?: string; cascade?: boolean; target?: { class: string; name: string } } = {}) {
      const ids = o.target ? [`test-${slug(o.target.class)}-${slug(o.target.name)}`] : [self.objectId];
      const rec: KillRecord = { reason: o.reason ?? "", cascade: !!o.cascade, killed: ids };
      if (o.target) rec.target = { ...o.target };
      killed.push(rec);
      return { killed: ids };
    },
    async audit(records: Array<Record<string, unknown>>) {
      for (const r of records) audits.push(structuredClone(r));
    },
    async self() {
      return { ...self };
    },
    connect(address: SocketAddress, options: TailnetSocketOptions = {}): TailnetSocket {
      if (opts.connect) return opts.connect(address, options);
      throw new TailnetError("not_supported", "ctx.tailnet.connect() has no test socket: pass createTestTailnet({ connect })");
    },
  });

  const define = <T extends object>(o: T): T & { tailnet: TailnetContext } => {
    Object.defineProperty(o, "tailnet", { value: tailnet, enumerable: false, configurable: true });
    return o as T & { tailnet: TailnetContext };
  };

  return {
    tailnet,
    spawned,
    killed,
    audits,
    fetches,
    withCaller: (req, who) => callerCf(req, who),
    withPublic: (req, p) => publicCf(req, p),
    ctx<T extends object>(base?: T) {
      const c: any =
        base ??
        ({
          waitUntil(p: Promise<unknown>) {
            pending.push(Promise.resolve(p).catch(() => {}));
          },
          passThroughOnException() {},
          props: {},
        } satisfies ExecutionContextLike);
      return define(c);
    },
    attach: define,
    async settle() {
      while (pending.length) await pending.shift();
    },
  };
}
