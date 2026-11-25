// Trace context across Durable Object RPC and alarms (ADR-045, #295).
//
// An RPC call (`stub.method(...)`) and an alarm carry no request headers,
// so the runtime carries the trace itself:
//   - the caller's guarded stub opens a CLIENT span "<Class> <method>" and
//     appends one marker argument, {__tw_tp: "<traceparent>"};
//   - the callee's entry gate (the Proxy its wrapper hands workerd) strips
//     the marker before tenant code sees the arguments and runs the method
//     under a SERVER span, the CLIENT span's child;
//   - an alarm runs under a SERVER span that starts a new trace.
// Egress, stub fetch() and RPC made inside either join that trace. Every
// DO class bound in an app is wrapped (wrapFacetClass, wrapPlainClass), so
// a callee always strips the marker. Trace context is observability data
// only: it never feeds authorization or the audit trail.

import { runEntry, startChild, type SpanRecord } from "./trace.js";

const MARK = "__tw_tp";

/** rpcCall calls method on a DO stub under a CLIENT span, passing its context on. */
export async function rpcCall(cls: string, name: string, method: string, args: unknown[], call: (...a: unknown[]) => unknown): Promise<unknown> {
  const c = startChild(`${cls} ${method}`, { "tw.do.class": cls, "tw.do.name": name, "rpc.method": method });
  if (!c) return call(...args);
  try {
    const r = await call(...args, { [MARK]: c.traceparent });
    c.finish(false);
    return r;
  } catch (e) {
    c.finish(true);
    throw e;
  }
}

/** takeMark removes rpcCall's trailing marker from args and returns its traceparent. */
export function takeMark(args: unknown[]): string | undefined {
  if (!args.length) return undefined;
  const m = args[args.length - 1] as any;
  if (!m || typeof m !== "object") return undefined;
  const proto = Object.getPrototypeOf(m);
  if (proto !== Object.prototype && proto !== null) return undefined;
  const keys = Object.keys(m);
  if (keys.length !== 1 || keys[0] !== MARK || typeof m[MARK] !== "string") return undefined;
  args.pop();
  return m[MARK];
}

export interface EntryInfo {
  /** The object's class. */
  cls: string;
  /** The entry point workerd (or the facet's Host) calls for an alarm. */
  alarm: string;
  /** Identifying span attributes (tw.do.name, tw.do.id for a plain DO). */
  attrs: () => Record<string, string>;
  flush: () => ((spans: SpanRecord[]) => void) | undefined;
}

/**
 * tracedEntry runs one call into the object from outside: an alarm under
 * a new trace, an RPC call that carries the marker under a SERVER span,
 * anything else as it is.
 */
export function tracedEntry(info: EntryInfo, p: PropertyKey, args: unknown[], run: (a: unknown[]) => unknown): unknown {
  if (typeof p !== "string") return run(args);
  if (p === info.alarm) {
    return runEntry(null, `${info.cls} alarm`, { "tw.do.class": info.cls, ...info.attrs(), "tw.trigger": "alarm" }, () => run(args), info.flush());
  }
  const tp = takeMark(args);
  if (tp === undefined) return run(args);
  return runEntry(tp, `${info.cls} ${p}`, { "tw.do.class": info.cls, ...info.attrs(), "tw.trigger": "rpc", "rpc.method": p }, () => run(args), info.flush());
}

/**
 * traceGate returns the object a facet's constructor hands workerd: a
 * Proxy whose methods, looked up from outside (RPC from the Host, the
 * delegated alarm), go through tracedEntry. Methods run with `this` the
 * real object, as with the plain DO's kill gate.
 */
export function traceGate(obj: any, info: EntryInfo): any {
  const wrapped = new Map<PropertyKey, { v: Function; w: Function }>();
  return new Proxy(obj, {
    get(t, p) {
      const v = Reflect.get(t, p, t);
      if (typeof v !== "function" || p === "constructor") return v;
      const c = wrapped.get(p);
      if (c && c.v === v) return c.w;
      const w = function (...args: unknown[]) {
        return tracedEntry(info, p, args, (a) => v.apply(t, a));
      };
      wrapped.set(p, { v, w });
      return w;
    },
    set(t, p, v) {
      return Reflect.set(t, p, v, t);
    },
  });
}
