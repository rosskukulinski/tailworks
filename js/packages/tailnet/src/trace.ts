// W3C trace context inside tenant code (ADR-045).
//
// hostd's ingress gives every request a `traceparent` whose span is its
// SERVER span. The runtime runs each fetch handler inside that context
// (AsyncLocalStorage; tailworks sets the nodejs_als compatibility flag) and:
//   - adds it to egress fetch() (hostd's egress then opens a CLIENT span
//     under it and passes the trace on to the next hop);
//   - gives a Durable Object stub's fetch() a new child span, recorded here
//     as a CLIENT span and reported to hostd once, when the handler ends.
// Trace context is observability data only: it never feeds authorization
// or the audit trail (whose trace IDs the platform generates).

import { AsyncLocalStorage } from "node:async_hooks";

/** A finished span, as reported to hostd (TailnetApi op "spans"). */
export interface SpanRecord {
  traceId: string;
  spanId: string;
  parentSpanId: string;
  name: string;
  start: number; // ms since the epoch
  end: number;
  error: boolean;
  attrs: Record<string, string | number>;
  /** "server" for an entry span (DO RPC, alarm); absent for CLIENT. */
  kind?: "server";
}

interface TraceState {
  traceId: string;
  spanId: string; // the current span: children are parented to it
  flags: string;
  incoming: string; // the traceparent the handler received
  spans: SpanRecord[];
}

/** At most this many spans are kept per handler invocation. */
export const MAX_SPANS = 64;

const TP_RE = /^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$/;

/** parseTraceparent reads a W3C traceparent header value (null if invalid). */
export function parseTraceparent(v: unknown): { traceId: string; spanId: string; flags: string } | null {
  if (typeof v !== "string") return null;
  const m = TP_RE.exec(v.trim());
  if (!m || m[1] === "ff" || (m[1] === "00" && m[5])) return null;
  if (/^0+$/.test(m[2]) || /^0+$/.test(m[3])) return null;
  return { traceId: m[2], spanId: m[3], flags: m[4] };
}

function randHex(bytes: number): string {
  const b = new Uint8Array(bytes);
  for (;;) {
    crypto.getRandomValues(b);
    if (b.some((x) => x !== 0)) return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  }
}

/** newSpanId returns a random 8-byte span ID (16 hex). */
export function newSpanId(): string {
  return randHex(8);
}

let als: AsyncLocalStorage<TraceState> | null = null;
try {
  als = new AsyncLocalStorage<TraceState>();
} catch {
  als = null; // no async context: requests are still served, just not traced
}

/** currentTrace is the running handler's trace context, if any. */
export function currentTrace(): { traceId: string; spanId: string; traceparent: string } | undefined {
  const st = als?.getStore();
  if (!st) return undefined;
  return { traceId: st.traceId, spanId: st.spanId, traceparent: `00-${st.traceId}-${st.spanId}-${st.flags}` };
}

function headerOf(req: any): unknown {
  try {
    return req?.headers?.get?.("traceparent");
  } catch {
    return null;
  }
}

/**
 * runTraced runs fn (a fetch handler) in req's trace context. Spans
 * recorded during it are handed to flush when fn settles. Without an
 * incoming traceparent fn runs outside any context.
 */
export async function runTraced<T>(req: any, fn: () => T | Promise<T>, flush?: (spans: SpanRecord[]) => void): Promise<T> {
  if (!als) return fn();
  const header = headerOf(req);
  const tp = parseTraceparent(header);
  if (!tp) return als.run(undefined, fn);
  const st: TraceState = { ...tp, incoming: String(header), spans: [] };
  return als.run(st, async () => {
    try {
      return await fn();
    } finally {
      if (st.spans.length && flush) {
        const spans = st.spans.splice(0);
        try {
          flush(spans);
        } catch {
          /* reporting is best effort */
        }
      }
    }
  });
}

/**
 * withTraceparent returns fetch(input, init) arguments carrying value as
 * traceparent, or null to leave them as they are: when the request already
 * has a traceparent of its own (one the tenant set, not the one its handler
 * received) the tenant manages tracing itself.
 */
function withTraceparent(input: any, init: any, value: string, incoming: string): [any, any] | null {
  let base: any = init?.headers;
  if (base === undefined && typeof Request !== "undefined" && input instanceof Request) base = input.headers;
  let h: Headers;
  try {
    h = new Headers(base);
  } catch {
    return null;
  }
  const cur = h.get("traceparent");
  if (cur && cur !== incoming) return null;
  h.set("traceparent", value);
  return [input, { ...(init ?? {}), headers: h }];
}

let installed = false;

/** installFetchPropagation patches globalThis.fetch once (egress). */
export function installFetchPropagation(): void {
  if (installed || !als || typeof globalThis.fetch !== "function") return;
  installed = true;
  const orig = globalThis.fetch;
  const traced = function fetch(this: unknown, input: any, init?: any): Promise<Response> {
    const st = als?.getStore();
    if (st) {
      const a = withTraceparent(input, init, `00-${st.traceId}-${st.spanId}-${st.flags}`, st.incoming);
      if (a) return orig.call(globalThis, a[0], a[1]);
    }
    return orig.call(globalThis, input, init);
  };
  try {
    Object.defineProperty(globalThis, "fetch", { value: traced, writable: true, configurable: true });
  } catch {
    /* a frozen global: egress is not traced */
  }
}

/**
 * tracedStubFetch calls a Durable Object stub's fetch under a new CLIENT
 * span ("<Class> fetch") whose ID is the callee's parent.
 */
export async function tracedStubFetch(cls: string, name: string, args: unknown[], call: (...a: unknown[]) => Promise<Response>): Promise<Response> {
  const st = als?.getStore();
  if (!st) return call(...args);
  const spanId = newSpanId();
  const a = withTraceparent(args[0], args[1], `00-${st.traceId}-${spanId}-${st.flags}`, st.incoming);
  if (!a) return call(...args);
  let method = "GET";
  try {
    method = String(a[1]?.method ?? (args[0] as any)?.method ?? "GET").toUpperCase();
  } catch {
    /* default */
  }
  const rec: SpanRecord = {
    traceId: st.traceId,
    spanId,
    parentSpanId: st.spanId,
    name: `${cls} fetch`,
    start: Date.now(),
    end: 0,
    error: false,
    attrs: { "tw.do.class": cls, "tw.do.name": name, "http.request.method": method },
  };
  const done = () => {
    rec.end = Date.now();
    if (st.spans.length < MAX_SPANS) st.spans.push(rec);
  };
  try {
    const r = await call(...a, ...args.slice(2));
    rec.attrs["http.response.status_code"] = r?.status ?? 0;
    rec.error = (r?.status ?? 0) >= 500;
    done();
    return r;
  } catch (e) {
    rec.error = true;
    done();
    throw e;
  }
}

/**
 * startChild opens a CLIENT span under the running handler's context
 * (undefined outside one). finish records it for the handler's report.
 */
export function startChild(name: string, attrs: Record<string, string | number>): { traceparent: string; finish(error: boolean): void } | undefined {
  const st = als?.getStore();
  if (!st) return undefined;
  const rec: SpanRecord = { traceId: st.traceId, spanId: newSpanId(), parentSpanId: st.spanId, name, start: Date.now(), end: 0, error: false, attrs };
  return {
    traceparent: `00-${st.traceId}-${rec.spanId}-${st.flags}`,
    finish(error: boolean) {
      rec.end = Date.now();
      rec.error = error;
      if (st.spans.length < MAX_SPANS) st.spans.push(rec);
    },
  };
}

/**
 * runEntry runs fn, an entry point that gets no traceparent header (a DO
 * RPC method, an alarm), under its own SERVER span: a child of parent (a
 * traceparent value) or, without a valid one, the root of a new trace.
 * The span and every span recorded during fn are handed to flush when fn
 * settles.
 */
export async function runEntry<T>(
  parent: string | null,
  name: string,
  attrs: Record<string, string | number>,
  fn: () => T | Promise<T>,
  flush?: (spans: SpanRecord[]) => void,
): Promise<T> {
  if (!als) return fn();
  const tp = parseTraceparent(parent);
  const st: TraceState = { traceId: tp?.traceId ?? randHex(16), spanId: newSpanId(), flags: tp?.flags ?? "01", incoming: "", spans: [] };
  const rec: SpanRecord = { traceId: st.traceId, spanId: st.spanId, parentSpanId: tp?.spanId ?? "", name, start: Date.now(), end: 0, error: false, attrs, kind: "server" };
  return als.run(st, async () => {
    try {
      return await fn();
    } catch (e) {
      rec.error = true;
      throw e;
    } finally {
      rec.end = Date.now();
      // The entry span is always reported, even past MAX_SPANS children.
      const spans = [rec, ...st.spans.splice(0, MAX_SPANS - 1)];
      if (flush) {
        try {
          flush(spans);
        } catch {
          /* reporting is best effort */
        }
      }
    }
  });
}
