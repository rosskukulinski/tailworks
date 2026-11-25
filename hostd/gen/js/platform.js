// tailworks platform wrapper (TRUSTED). Generated per deployment by tw-hostd from
// this template plus tw-config.json / tw-modules.js. Tenant code never
// runs in this isolate and never sees these bindings:
//   LOADER       Dynamic Worker loader (facets)
//   NETD_EGRESS  hostd's attributed egress socket (trusts x-tw-class/name + MAC)
//   EGRESS_WL    hostd's workload egress socket (the tenant service's identity)
//   HOSTD        hostd's local host API socket (ensure/spawn/ready/kill/audit)
//   MAC_KEY      per-process root key; per-object egress MAC keys are derived from it (RT-04)
//   LOCAL_TOKEN  hostd's loopback token (Landlock sandbox only), sent in connect() tunnels
//   TENANT       the tenant service (the app's default export)
//   NS_<Class>   plain-DO namespaces (classes with "egress": "workload")
//
// Routing comes from request.cf.tw, which only hostd can set (ingress
// socket cfBlobHeader; netd strips client copies).
import { DurableObject, WorkerEntrypoint } from "cloudflare:workers";
import CFG from "tw-config.json";
import MODULES from "tw-modules.js";
import { deliverQueue } from "tw-queue.js";
import { DOC, bindingCallee, setObjectEgress } from "tw-svc.js";

const FACET_MODULES = MODULES;

const json = (status, body, extra = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", "cache-control": "no-store", ...extra } });

const STATUS = {
  killed: 410, no_identity: 401, bad_request: 400, unknown_object: 404, identity_pending: 503, hostd_unavailable: 503,
  public_create_denied: 403, public_request: 403, not_public: 404, create_not_admitted: 403,
};

// isPublic: netd delivered this request over Funnel (ADR-029). Only netd
// sets cf.tailscale.requester (the ingress socket's cfBlobHeader; client
// copies are stripped), so this cannot be forged from headers.
function isPublic(req) {
  return req?.cf?.tailscale?.requester?.kind === "public";
}
// httpsRequest is req with an https:// URL (tailnet.https). A port that is
// http's default drops out with the scheme change (443 is https's).
function httpsRequest(req) {
  const u = new URL(req.url);
  if (u.protocol !== "http:") return req;
  u.protocol = "https:";
  if (u.port === "80") u.port = "";
  return new Request(u, req);
}
function errBody(code, message, extra = {}) {
  return { code, message, doc_url: DOC + code, ...extra };
}
function errResponse(code, message, extra = {}) {
  return json(STATUS[code] ?? 403, errBody(code, message, extra), { "tw-error": code });
}

// principalOf names who a request acts for (audit "actingFor"): the human
// login for an untagged caller, else the calling node's name. Pass it a
// request sealedRequest() returned: cf.tailscale.caller is then the caller
// hostd sealed, or absent.
function principalOf(req) {
  if (isPublic(req)) return "public"; // never a caller name, whatever else cf holds
  const c = req?.cf?.tailscale?.caller;
  if (!c) return "";
  const user = typeof c.user === "string" ? c.user : c.user?.login;
  return user || String(c.node || c.hostname || "").replace(/\.$/, "");
}

// hostd local API. Errors come back as typed bodies.
async function hostd(env, op, body) {
  let r;
  try {
    r = await env.HOSTD.fetch("http://hostd/v1/" + op, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body ?? {}),
    });
  } catch (e) {
    throw Object.assign(new Error("hostd unavailable"), { body: errBody("hostd_unavailable", String(e)) });
  }
  let j;
  try {
    j = await r.json();
  } catch {
    j = errBody("hostd_unavailable", "bad hostd response (" + r.status + ")");
  }
  if (!r.ok) throw Object.assign(new Error(j.message ?? j.code), { body: j });
  return j;
}

const bindingMap = Object.fromEntries(CFG.bindings.map((b) => [b.name, b.class]));

function isFacet(cls) {
  return CFG.classes[cls]?.mode === "facet";
}

function objectStub(env, ctx, cls, name) {
  if (isFacet(cls)) return ctx.exports["Host_" + cls].getByName(name);
  return env["NS_" + cls].getByName(name);
}

async function routeObject(env, ctx, cls, name, req) {
  if (!CFG.classes[cls]) return errResponse("unknown_object", "no Durable Object class " + cls + " in this app");
  if (!name) return errResponse("bad_request", "object name required");
  return objectStub(env, ctx, cls, name).fetch(req);
}

// scheduled runs one cron trigger (cf.tw.kind = "scheduled", which only
// hostd can set): the tenant's default export scheduled() handler. Only the
// app's declared crons are accepted. The run goes to the tenant service as
// a fetch carrying cf.tw (kind scheduled, cron, scheduledTime and the run's
// create ticket): the runtime wrapper in the tenant isolate dispatches it to
// scheduled() with a controller and answers {outcome, noRetry, error}.
// (workerd's native Fetcher.scheduled() builds its own controller, so the
// ticket could not ride on that.)
function traceparentOnly(req) {
  const tp = req.headers.get("traceparent");
  return tp ? { traceparent: tp } : {};
}

async function scheduled(env, t, req) {
  const cron = t.cron;
  if (typeof cron !== "string" || !CFG.crons.includes(cron)) return errResponse("bad_request", "no such cron trigger in this deployment");
  const st = t.scheduledTime;
  if (typeof st !== "number" || !Number.isFinite(st)) return errResponse("bad_request", "bad scheduledTime");
  try {
    const r = await env.TENANT.fetch(
      new Request("http://tenant/_tw/scheduled", {
        method: "POST",
        signal: req.signal,
        // The run's root span (hostd crontrace.go): its record and egress join that trace.
        headers: traceparentOnly(req),
        cf: { tw: { kind: "scheduled", cron, scheduledTime: st, ticket: t.ticket }, traceId: req.cf?.traceId },
      }),
    );
    const body = await r.json().catch(() => null);
    if (!body || typeof body.outcome !== "string") return json(200, { outcome: "exception", noRetry: false, error: "scheduled() dispatch answered " + r.status });
    return json(200, { outcome: body.outcome, noRetry: !!body.noRetry, error: body.error });
  } catch (e) {
    return json(200, { outcome: "exception", noRetry: false, error: String(e?.message ?? e).slice(0, 512) });
  }
}

async function admin(req, env, ctx, t) {
  switch (t.op) {
    case "health":
      return json(200, { ok: true, deploymentID: CFG.deploymentID, workload: CFG.workload });
    case "kill": {
      try {
        await objectStub(env, ctx, t.class, t.name).__tw_kill(t.reason || "killed", await hostCap(env));
      } catch (e) {
        return json(500, errBody("bad_request", "kill: " + String(e)));
      }
      return json(200, { ok: true });
    }
    case "store": {
      // An operator operation on a platform store (tw d1 execute, …):
      // hostd sends it only for a store this deployment declares.
      if (!(CFG.stores ?? []).some((s) => s.kind === t.storeKind && s.name === t.name)) return errResponse("bad_request", "no such store in this deployment");
      try {
        const r = await ctx.exports.Store.getByName(t.storeKind + ":" + t.name).adminOp(t.action, [await req.json()]);
        return json(200, r);
      } catch (e) {
        const m = String(e?.message ?? e);
        const c = /^\[([a-z_]+)\]/.exec(m)?.[1] ?? "store_unavailable";
        return json(c === "bad_request" ? 400 : 503, errBody(c, m));
      }
    }
  }
  return errResponse("bad_request", "unknown admin op");
}

export default {
  async fetch(req, env, ctx) {
    const t = req.cf?.tw;
    if (!t || typeof t !== "object") return errResponse("no_identity", "request did not come through netd/hostd");
    if (isPublic(req)) {
      // A public node only fronts its workload's default export; the
      // platform's /_tw/... addressing is never public (netd refuses it
      // too: defense in depth).
      if (t.kind !== "workload") return errResponse("public_request", "a public request may only reach its workload's entry point");
      const p = new URL(req.url).pathname;
      if (p === "/_tw" || p.startsWith("/_tw/")) return errResponse("not_public", "/_tw is never public");
      // Funnel terminated TLS: the Worker sees the https:// URL (#360).
      return env.TENANT.fetch(t.https === true ? httpsRequest(req) : req);
    }
    switch (t.kind) {
      case "admin":
        return admin(req, env, ctx, t);
      case "scheduled":
        return scheduled(env, t, req);
      case "queue":
        return deliverQueue(env, t, req); // tw-queue.js (ADR-039)
      case "approval":
        return deliverApproval(env, t);
      case "object":
        return routeObject(env, ctx, t.class, t.name, req);
      case "intercept": {
        // A request an object's container sent to an intercepted
        // destination (hostd's sandbox egress proxy, ADR-032 D4): only the
        // object's own registered handler sees it, in the object.
        if (!CFG.classes[t.class]?.container) return errResponse("unknown_object", "no container class " + t.class + " in this app");
        if (!t.name || typeof t.key !== "string" || typeof t.url !== "string") return errResponse("bad_request", "bad intercept route");
        const hasBody = req.method !== "GET" && req.method !== "HEAD";
        const r = new Request(t.url, { method: req.method, headers: req.headers, body: hasBody ? req.body : undefined, redirect: "manual" });
        return objectStub(env, ctx, t.class, t.name).__tw_intercept(t.key, r, await hostCap(env));
      }
      case "workload": {
        // A service binding call to a named entrypoint or an RPC method
        // (hostd admitted it: cf.tw.binding; tw-svc.js).
        if (t.binding) return bindingCallee(req, env, t);
        // tailnet.https: the request reached the workload node on :443
        // (hostd sets cf.tw.https only then), so the Worker sees the
        // https:// URL it was called at, as on Cloudflare.
        if (t.https === true) req = httpsRequest(req);
        // /_tw/o/<Class>/<name>/... on the workload node reaches an object
        // before (or instead of) its own node.
        const u = new URL(req.url);
        const m = u.pathname.match(/^\/_tw\/o\/([A-Za-z_$][\w$]*)\/([^/]+)(\/.*)?$/);
        if (m) {
          const name = decodeURIComponent(m[2]);
          if (CFG.classes[m[1]] && name) {
            // A tailnet request may create the object it addresses: admit it
            // with the request's ticket (hostd verifies; public requests
            // never reach /_tw).
            try {
              await hostd(env, "admit", { class: m[1], name, ticket: t.ticket });
            } catch (e) {
              const b = e.body ?? errBody("create_not_admitted", String(e));
              return errResponse(b.code ?? "create_not_admitted", b.message ?? String(e));
            }
          }
          u.pathname = m[3] ?? "/";
          return routeObject(env, ctx, m[1], name, new Request(u, req));
        }
        return env.TENANT.fetch(req);
      }
    }
    return errResponse("bad_request", "unknown route kind");
  },
};

// Host_<Class>: one platform DO per tenant object (facet mode). The tenant
// class runs as a facet in a Dynamic Worker whose ONLY network path
// (globalOutbound) is Egress specialised with this object's class+name.
// Any RPC method is forwarded to the facet via a Proxy.
//
// Tenant code can hold a Host stub: the tenant service's raw binding
// (import { env } from "cloudflare:workers") is the Host namespace itself,
// and workerd exposes a DO's prototype methods over RPC. So the Host's
// helpers are module functions (hostAdmitted, hostEnsure, hostFacet), and
// the entry points only the platform calls (__tw_kill, __tw_intercept,
// __tw_alarm_op) refuse a call without hostCap.

// hostAdmitted: hostd must know this object (it exists, or a tailnet
// request / an attested object / public.create admitted its creation)
// before any tenant code runs. A refused create is final; other hostd
// errors (unavailable) are not fatal here, as before.
async function hostAdmitted(h, cls) {
  try {
    await hostEnsure(h, cls);
  } catch (e) {
    const c = e.body?.code;
    if (c === "create_not_admitted" || c === "public_create_denied" || c === "killed") return { code: c, message: e.body?.message ?? String(e) };
  }
  return null;
}
function hostEnsure(h, cls) {
  if (!h.__ensured) {
    h.__ensured = hostd(h.env, "ensure", { as: { class: cls, name: h.ctx.id.name }, wake: true, actingFor: h.__actingFor || undefined }).catch((e) => {
      h.__ensured = null;
      throw e;
    });
  }
  return h.__ensured;
}
// hostFacet: the tenant class as a facet (callers await hostAdmitted first).
function hostFacet(h, cls) {
  // Every path to tenant code ends here: a killed object runs none (#211).
  if (h.__killed) throw new Error("[killed] object " + cls + "/" + h.ctx.id.name + " was killed: " + h.__killed);
  if (!h.__w) {
    const name = h.ctx.id.name;
    if (!name) throw new Error("[bad_request] tw objects must be addressed by name (getByName/idFromName); newUniqueId() is not supported yet");
    const exp = h.ctx.exports;
    h.__w = h.env.LOADER.get(CFG.deploymentID + ":" + cls + ":" + name, () => {
      const env = { ...CFG.vars };
      for (const b of CFG.bindings) env[b.name] = exp.NsProxy({ props: { class: b.class } });
      for (const w of CFG.workflows ?? []) env[w.binding] = exp.WorkflowGate({ props: { workflow: w.name } });
      env.__TW = exp.TailnetApi({ props: { kind: "object", class: cls, name } });
      for (const s of CFG.stores ?? []) env[s.binding] = exp[s.facet]({ props: s.props });
      for (const q of CFG.queues?.producers ?? []) env[q.binding] = exp.QueueFacetProxy({ props: { binding: q.binding } });
      for (const sb of CFG.services ?? []) env[sb.binding] = exp.ServiceBinding({ props: { binding: sb.binding, class: cls, name } });
      const wrap = Object.fromEntries((CFG.stores ?? []).filter((s) => s.wrap).map((s) => [s.binding, s.wrap]));
      if (Object.keys(wrap).length) env.__TW_WRAP = wrap; // tw-rt.js puts the API around these stubs (D1)
      return {
        compatibilityDate: CFG.compatDate,
        compatibilityFlags: CFG.compatFlags,
        mainModule: "__tw_facet.js",
        modules: FACET_MODULES,
        globalOutbound: exp.Egress({ props: { class: cls, name } }),
        // The facet's console.*, exceptions and outcomes, attested as
        // this object (ADR-045).
        tails: [exp.Tail({ props: { kind: "object", class: cls, name } })],
        env,
      };
    });
  }
  // The facet stub is not kept between calls: once the facet calls
  // ctx.abort() its stub fails with the abort reason for good, and only a
  // fresh get() starts a new instance, as Cloudflare resets an aborted
  // object for its next request (#395). get() of a running facet returns it.
  const w = h.__w;
  return h.ctx.facets.get("app", () => ({ class: w.getDurableObjectClass(cls) }));
}

// hostCap is the capability the platform passes to a Host's internal entry
// points: derived from MAC_KEY, which only the platform worker holds.
let hostCapP = null;
function hostCap(env) {
  hostCapP ??= (async () => hex(new Uint8Array(await crypto.subtle.sign("HMAC", await hmacKey(env.MAC_KEY), enc.encode("tw-host-internal-v1")))))().catch((e) => {
    hostCapP = null;
    throw e;
  });
  return hostCapP;
}
async function checkHostCap(env, cap) {
  if (typeof cap !== "string" || cap !== (await hostCap(env))) throw new Error("[forbidden] platform-internal method");
}

function makeHost(cls) {
  const H = class extends DurableObject {
    constructor(ctx, env) {
      super(ctx, env);
      this.__w = null; // the facet's Dynamic Worker (hostFacet)
      this.__ensured = null;
      this.__actingFor = ""; // own property: the Proxy would answer unknown names with an RPC forwarder
      this.__killed = ctx.storage.kv.get("__tw_killed") ?? null;
      const self = this;
      return new Proxy(this, {
        get(t, p, r) {
          if (typeof p === "symbol" || p in t) return Reflect.get(t, p, r);
          if (p === "then") return undefined;
          return async (...args) => {
            if (self.__killed) throw new Error("[killed] object " + cls + "/" + self.ctx.id.name + " was killed: " + self.__killed);
            // No tenant code runs for an object nobody admitted (RT3-06).
            const d = await hostAdmitted(self, cls);
            if (d) throw new Error("[" + d.code + "] " + d.message);
            // A Request argument carries only a sealed caller, as a fetch does.
            const a = await Promise.all(args.map((x) => sealedRequest(self.env, x)));
            return hostFacet(self, cls)[p](...a);
          };
        },
      });
    }
    async fetch(req) {
      if (this.__killed) return errResponse("killed", "object " + cls + "/" + this.ctx.id.name + " was killed: " + this.__killed);
      if (!this.ctx.id.name) return errResponse("bad_request", "tw objects must be addressed by name (getByName/idFromName); newUniqueId() is not supported yet");
      // Only a caller hostd sealed reaches the facet or names a principal
      // (RT3-08): the sender may be tenant code forwarding in-process.
      req = await sealedRequest(this.env, req);
      // Internal fetches (e.g. partyserver's set-name) carry no caller.
      const who = principalOf(req);
      if (who && !this.__actingFor) {
        this.__actingFor = who;
        if (this.__ensured) {
          // Created before its first caller-bearing request (RPC first):
          // tell hostd whom it acts for (audit "principal").
          const p = hostd(this.env, "ensure", { as: { class: cls, name: this.ctx.id.name }, actingFor: who }).catch(() => {});
          try {
            this.ctx.waitUntil(p);
          } catch {
            /* ignore */
          }
        }
      }
      // Whether the request is public or not is not read from req.cf here:
      // hostd decides from what it admitted (a public request's guarded env
      // admits only under public.create; a raw binding admits nothing).
      const d = await hostAdmitted(this, cls);
      if (d) return errResponse(d.code, d.message);
      return hostFacet(this, cls).fetch(req);
    }
    // Facet alarm delegation (facets cannot set alarms in workerd): the
    // facet's ctx.storage.{set,get,delete}Alarm land here via TailnetApi
    // "alarm", and alarm() runs the facet's alarm handler.
    async __tw_alarm_op(action, time, cap) {
      await checkHostCap(this.env, cap);
      if (this.__killed) throw new Error("[killed] object " + cls + "/" + this.ctx.id.name + " was killed: " + this.__killed);
      if (action === "set") {
        await this.ctx.storage.setAlarm(time);
        return null;
      }
      if (action === "delete") {
        await this.ctx.storage.deleteAlarm();
        return null;
      }
      return await this.ctx.storage.getAlarm();
    }
    // A request the object's container sent to an intercepted destination.
    async __tw_intercept(key, req, cap) {
      await checkHostCap(this.env, cap);
      if (this.__killed) return errResponse("killed", "object " + cls + "/" + this.ctx.id.name + " was killed: " + this.__killed);
      return hostFacet(this, cls).__tw_intercept(key, req);
    }
    async alarm(info) {
      if (this.__killed) return;
      // AlarmInvocationInfo is not serializable; pass its fields.
      await hostFacet(this, cls).__tw_alarm({ retryCount: info?.retryCount ?? 0, isRetry: !!info?.isRetry });
    }
    async __tw_kill(reason, cap) {
      await checkHostCap(this.env, cap);
      this.__killed = reason || "killed";
      this.ctx.storage.kv.put("__tw_killed", this.__killed);
      try {
        await this.ctx.storage.deleteAlarm();
      } catch {
        /* ignore */
      }
      try {
        this.ctx.facets.abort("app", new Error("killed"));
      } catch {
        /* not running */
      }
      return true;
    }
  };
  Object.defineProperty(H, "name", { value: "Host_" + cls });
  return H;
}

//@@HOST_CLASSES@@

// Platform stores (tw-store.js; ADR-044), when the app has
// storage bindings.
//@@STORES@@

// Queue producer bindings and their facet proxies (tw-queue.js; ADR-039).
export { QueueProducer, QueueFacetProxy } from "tw-queue.js";

// NsProxy stands in for a DO namespace binding inside a facet's env:
// env.X.getByName(name) returns the real stub (RPC promise pipelining makes
// env.X.getByName(n).method() work).
export class NsProxy extends WorkerEntrypoint {
  getByName(name) {
    return objectStub(this.env, this.ctx, this.ctx.props.class, String(name));
  }
}

const enc = new TextEncoder();
const hex = (u8) => [...u8].map((b) => b.toString(16).padStart(2, "0")).join("");
async function hmacKey(secret) {
  return crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
}
let rootKey = null;
const objectKeys = new Map(); // "class\nname" -> CryptoKey
// objectKey derives this object's egress key from the instance root key
// (hostd gen.ObjectMACKey): hex(HMAC(root, "tw-egress-object-key-v1\n" +
// class + "\n" + name)). hostd verifies a (class, name) MAC only under that
// object's key.
async function objectKey(env, cls, name) {
  const k = cls + "\n" + name;
  let ck = objectKeys.get(k);
  if (!ck) {
    rootKey ??= await hmacKey(env.MAC_KEY);
    const derived = hex(new Uint8Array(await crypto.subtle.sign("HMAC", rootKey, enc.encode("tw-egress-object-key-v1\n" + k))));
    ck = await hmacKey(derived);
    objectKeys.set(k, ck);
  }
  return ck;
}
async function mac(env, cls, name, s) {
  const sig = new Uint8Array(await crypto.subtle.sign("HMAC", await objectKey(env, cls, name), enc.encode(s)));
  return hex(sig);
}

// ---- caller seals (RT3-08 / H08, #148) ----
//
// request.cf is whatever the sender says once tenant code forwards a
// request in-process, so cf.tailscale.caller alone proves nothing. hostd
// seals the caller netd got from WhoIs (cf.tailscale.seal, gen.SealCaller):
// hex(HMAC-SHA256(MAC_KEY, "tw-caller-seal-v1\n" + exp + "\n" + caller)),
// where caller is the caller's exact JSON. A caller counts only if its seal
// verifies, and then the sealed copy is used, never cf.tailscale.caller:
// tenant code can drop a seal or replay one it was given (the same real
// caller), but cannot make one for anybody else. MAC_KEY never leaves this
// isolate; the runtime asks here ("unseal").
const SEAL_DOMAIN = "tw-caller-seal-v1\n";
const SEAL_MAX_AHEAD_MS = 6 * 60_000; // gen.CallerSealTTL plus a minute of skew
let sealKey = null;

// verifiedCaller returns the caller a seal vouches for, or null.
async function verifiedCaller(env, s) {
  if (!s || typeof s !== "object" || s.v !== 1 || typeof s.caller !== "string" || typeof s.mac !== "string") return null;
  if (!Number.isSafeInteger(s.exp) || !/^[0-9a-f]{64}$/.test(s.mac)) return null;
  const now = Date.now();
  if (now > s.exp || s.exp - now > SEAL_MAX_AHEAD_MS) return null;
  sealKey ??= await crypto.subtle.importKey("raw", enc.encode(env.MAC_KEY), { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  const sig = new Uint8Array(32);
  for (let i = 0; i < 32; i++) sig[i] = parseInt(s.mac.slice(2 * i, 2 * i + 2), 16);
  if (!(await crypto.subtle.verify("HMAC", sealKey, sig, enc.encode(SEAL_DOMAIN + s.exp + "\n" + s.caller)))) return null;
  try {
    const c = JSON.parse(s.caller);
    return c && typeof c === "object" && !Array.isArray(c) ? c : null;
  } catch {
    return null;
  }
}

// sealedCf returns cf with only a verified caller (the sealed copy, seal
// kept so a further forward still verifies), or with no caller and no
// seal; null when cf has neither (nothing to change).
async function sealedCf(env, cf) {
  const ts = cf?.tailscale;
  if (!ts || typeof ts !== "object" || (!("caller" in ts) && !("seal" in ts))) return null;
  const caller = await verifiedCaller(env, ts.seal);
  const { caller: _c, seal, ...rest } = ts;
  return { ...cf, tailscale: caller ? { ...rest, caller, seal } : rest };
}

// sealedRequest is req as tenant code may see it (see sealedCf).
async function sealedRequest(env, req) {
  if (!(req instanceof Request)) return req;
  const cf = await sealedCf(env, req.cf);
  return cf ? new Request(req, { cf }) : req;
}

// objectEgress sends a request as facet object cls/name: hostd verifies the
// MAC, maps class+name to the object and its node, and netd dials from that
// node. extra are platform headers set after tenant x-tw-* are stripped
// (a service binding's, tw-svc.js). tailworks-binding is the header a
// binding call carries between hosts: hostd's to set, never the tenant's
// (#347; hostd strips it again).
async function objectEgress(env, cls, name, url, init, extra = {}) {
  const h = new Headers(init.headers);
  for (const k of [...h.keys()]) if (k.startsWith("x-tw-") || k.startsWith("tw-") || k === "proxy-authorization" || k === "tailworks-binding") h.delete(k);
  for (const [k, v] of Object.entries(extra)) h.set(k, v);
  const ts = String(Date.now());
  const nonce = crypto.randomUUID();
  h.set("x-tw-class", cls);
  h.set("x-tw-name", encodeURIComponent(name));
  h.set("x-tw-ts", ts);
  h.set("x-tw-nonce", nonce);
  h.set("x-tw-mac", await mac(env, cls, name, [cls, name, init.method, url, ts, nonce].join("\n")));
  // workerd's proxy-style external sends absolute-form without Host; Go's net/http rejects that.
  h.set("host", new URL(url).host);
  return env.NETD_EGRESS.fetch(new Request(url, { ...init, headers: h, redirect: "manual" }));
}
setObjectEgress(objectEgress);

// Egress for a facet's fetch(). props were minted by Host_<Class>; tenant code
// cannot change them.
export class Egress extends WorkerEntrypoint {
  async fetch(request) {
    const { class: cls, name } = this.ctx.props;
    return objectEgress(this.env, cls, name, request.url, { method: request.method, headers: request.headers, body: request.body });
  }
  // Stock connect() from a facet: as the object's own node.
  async connect(socket) {
    const { class: cls, name } = this.ctx.props;
    await tcpServe(socket, (dst) => tcpDial(this.env, this.env.NETD_EGRESS, dst, { class: cls, name }, false));
  }
}

// Service bindings (tw-svc.js, ADR-053).
export { ServiceBinding } from "tw-svc.js";

// WorkloadEgress is the tenant service's globalOutbound: the Worker and
// plain-DO objects egress as the workload node.
export class WorkloadEgress extends WorkerEntrypoint {
  async fetch(request) {
    const h = new Headers(request.headers);
    for (const k of [...h.keys()]) if (k.startsWith("x-tw-") || k.startsWith("tw-") || k === "proxy-authorization" || k === "tailworks-binding") h.delete(k);
    h.set("host", new URL(request.url).host);
    return this.env.EGRESS_WL.fetch(new Request(request.url, { method: request.method, headers: h, body: request.body, redirect: "manual" }));
  }
  // Stock connect() from the Worker or a plain-DO object (RFC §4.8: as
  // the workload node).
  async connect(socket) {
    await tcpServe(socket, (dst) => tcpDial(this.env, this.env.EGRESS_WL, dst, null, false));
  }
}

// ---- raw TCP egress (ADR-052) ----
//
// A tenant's connect() reaches its globalOutbound's connect() handler.
// The handler opens a CONNECT tunnel to hostd (workerd sends it with no
// headers), writes one preamble line (gen.ConnectPreamble: the loopback
// token, a facet's MAC'd identity), and reads hostd's status line
// (gen.ConnectStatus) before any of the flow's bytes. Refusals reach stock
// connect() only as a lost connection (workerd has no way to fail a
// handler's socket with a reason); ctx.tailnet.connect() gets them typed
// (TailnetApi.tcpConnect).

const MAX_DST = 512;
const NL = 10;

class TcpRefused extends Error {
  constructor(code, message) {
    super("[" + code + "] " + message);
    this.code = code;
    this.detail = message;
  }
}

function checkDst(dst) {
  if (typeof dst !== "string" || !dst || dst.length > MAX_DST || /[\s\/@]/.test(dst) || !/:\d{1,5}$/.test(dst)) {
    throw new TcpRefused("bad_request", "connect() needs host:port");
  }
  return dst;
}

// tcpDial opens an authenticated tunnel for dst through binding and returns
// { readable, writable, close } once hostd has dialled, or throws TcpRefused.
// ident is a facet's { class, name } (MAC'd), null for the workload.
async function tcpDial(env, binding, dst, ident, tls) {
  checkDst(dst);
  const up = binding.connect(dst);
  const pre = { v: 1 };
  if (env.LOCAL_TOKEN) pre.token = env.LOCAL_TOKEN;
  if (ident) {
    const ts = String(Date.now());
    const nonce = crypto.randomUUID();
    Object.assign(pre, {
      class: ident.class,
      name: encodeURIComponent(ident.name),
      ts,
      nonce,
      mac: await mac(env, ident.class, ident.name, [ident.class, ident.name, "CONNECT", dst, ts, nonce].join("\n")),
    });
  }
  if (tls) pre.tls = true;
  const r = up.readable.getReader();
  let buf = new Uint8Array(0);
  let st;
  try {
    const w = up.writable.getWriter();
    await w.write(enc.encode(JSON.stringify(pre) + "\n"));
    w.releaseLock();
    for (;;) {
      const i = buf.indexOf(NL);
      if (i >= 0) {
        st = JSON.parse(new TextDecoder().decode(buf.subarray(0, i)));
        buf = buf.subarray(i + 1);
        break;
      }
      if (buf.length > 4096) throw new Error("status line too long");
      const { value, done } = await r.read();
      if (done) throw new Error("hostd closed the tunnel");
      const b = new Uint8Array(buf.length + value.length);
      b.set(buf);
      b.set(value, buf.length);
      buf = b;
    }
  } catch (e) {
    up.close().catch(() => {});
    throw new TcpRefused("hostd_unavailable", "egress tunnel: " + String(e?.message ?? e));
  }
  if (!st || st.ok !== true) {
    up.close().catch(() => {});
    throw new TcpRefused(String(st?.code || "dial_failed"), String(st?.message || "refused"));
  }
  // The destination may speak first (MySQL, Redis greetings): bytes read
  // past the status line come first.
  let rest = buf.length ? buf : null;
  const readable = new ReadableStream({
    async pull(c) {
      if (rest) {
        c.enqueue(rest);
        rest = null;
        return;
      }
      const { value, done } = await r.read();
      if (done) c.close();
      else c.enqueue(value);
    },
    cancel(reason) {
      return r.cancel(reason);
    },
  });
  return { readable, writable: up.writable, close: () => up.close() };
}

// tcpServe answers a connect() handler's socket with dial(dst)'s tunnel.
async function tcpServe(socket, dial) {
  let dst, up;
  try {
    const info = await socket.opened;
    dst = info?.localAddress ?? info?.remoteAddress;
    up = await dial(dst);
  } catch (e) {
    console.warn("tw: connect(" + String(dst) + ") refused: " + String(e?.message ?? e));
    await socket.close().catch(() => {});
    return;
  }
  await Promise.allSettled([
    socket.readable.pipeTo(up.writable),
    up.readable.pipeTo(socket.writable),
  ]);
  await Promise.allSettled([socket.close(), up.close()]);
}

// attestedAs is who a TailnetApi call acts for: the facet object named by
// props (unforgeable), or a plain-DO object the tenant service claims
// (args.as; never a facet object).
function attestedAs(props, args) {
  if (props.kind === "object") return { class: props.class, name: props.name };
  if (args?.as && CFG.classes[args.as.class] && !isFacet(args.as.class)) return { class: String(args.as.class), name: String(args.as.name) };
  return null;
}

const ctrErr = (message) => ({ ok: false, error: errBody("bad_request", message) });

// containerAs: the calling object, which must have a container (wrangler
// "containers"); container ops only ever act on that object's own one.
function containerAs(props, args) {
  const as = attestedAs(props, args);
  if (!as || !as.name) return null;
  if (!CFG.classes[as.class]?.container) return null;
  return as;
}

// TailnetApi backs ctx.tailnet and ctx.container. props: {kind:"object",
// class, name} for a facet (unforgeable), or {kind:"workload"} for the
// tenant service, which may act as a plain-DO object it hosts (args.as) but
// never as a facet object.
export class TailnetApi extends WorkerEntrypoint {
  // ctx.tailnet.connect(): the socket's streams, or a typed refusal. A
  // facet connects as its own node; the Worker as the workload node. A
  // plain-DO object has no node of its own to connect from (WD-3), so it
  // is refused rather than silently given the workload's identity.
  async tcpConnect(args = {}) {
    const p = this.ctx.props ?? {};
    const tls = args.tls === "netd";
    try {
      let s;
      if (p.kind === "object") s = await tcpDial(this.env, this.env.NETD_EGRESS, args.address, { class: p.class, name: p.name }, tls);
      else if (args.as) throw new TcpRefused("not_supported", "ctx.tailnet.connect() in a plain-DO object (\"egress\": \"workload\") has no per-object identity; use connect() from cloudflare:sockets (egresses as the workload node) or facet mode");
      else s = await tcpDial(this.env, this.env.EGRESS_WL, args.address, null, tls);
      return { ok: true, value: { readable: s.readable, writable: s.writable } };
    } catch (e) {
      if (e instanceof TcpRefused) return { ok: false, error: errBody(e.code, e.detail) };
      return { ok: false, error: errBody("dial_failed", String(e?.message ?? e)) };
    }
  }
  // ctx.container ops (JSON): status, start, destroy, signal, monitor,
  // setInactivityTimeout, inspect, intercept, exec-signal, exec-resize.
  async ctr(op, args = {}) {
    const as = containerAs(this.ctx.props ?? {}, args);
    if (!as) return ctrErr("ctx.container is only for the calling object's own container");
    const { as: _ignored, ...rest } = args;
    try {
      return { ok: true, value: await hostd(this.env, "container", { ...rest, op: String(op), as }) };
    } catch (e) {
      return { ok: false, error: e.body ?? errBody("hostd_unavailable", String(e)) };
    }
  }
  // exec: a streamed Response (stdio frames), headers x-tw-exec(-pid).
  async ctrExec(args = {}) {
    const as = containerAs(this.ctx.props ?? {}, args);
    if (!as) return errResponse("bad_request", "ctx.container is only for the calling object's own container");
    return this.env.HOSTD.fetch("http://hostd/v1/container/exec", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ as, spec: args.spec ?? {} }),
    });
  }
  // stdin: body streams into the exec's stdin, then it is half-closed.
  async ctrStdin(args = {}, body) {
    const as = containerAs(this.ctx.props ?? {}, args);
    if (!as) return ctrErr("ctx.container is only for the calling object's own container");
    const r = await this.env.HOSTD.fetch("http://hostd/v1/container/stdin?exec=" + encodeURIComponent(String(args.exec ?? "")), {
      method: "POST",
      headers: { "x-tw-as": JSON.stringify(as) },
      body,
    });
    return { ok: r.ok, status: r.status };
  }
  // getTcpPort(port).fetch(request)
  async ctrFetch(args = {}, request) {
    const as = containerAs(this.ctx.props ?? {}, args);
    if (!as) return errResponse("bad_request", "ctx.container is only for the calling object's own container");
    const port = Number(args.port);
    if (!Number.isInteger(port) || port < 1 || port > 65535) return errResponse("bad_request", "bad port");
    const u = new URL(request.url);
    const h = new Headers(request.headers);
    for (const k of [...h.keys()]) if (k.startsWith("x-tw-") || k.startsWith("tw-")) h.delete(k);
    h.set("x-tw-as", JSON.stringify(as));
    h.set("x-tw-host", u.host);
    const hasBody = request.method !== "GET" && request.method !== "HEAD";
    return this.env.HOSTD.fetch("http://hostd/v1/container/port/" + port + u.pathname + u.search, {
      method: request.method,
      headers: h,
      body: hasBody ? request.body : undefined,
      redirect: "manual",
    });
  }
  async call(op, args = {}) {
    const p = this.ctx.props ?? {};
    const as = attestedAs(p, args);
    if (p.kind !== "object" && args.as && !as) {
      return { ok: false, error: errBody("bad_request", "the workload may only act as its plain-DO objects") };
    }
    if (op === "alarm") {
      // Only a facet object may manage its own (delegated) alarm.
      if (p.kind !== "object" || !isFacet(p.class)) return { ok: false, error: errBody("bad_request", "alarm delegation is for facet objects only") };
      const act = args.action;
      if (!["set", "get", "delete"].includes(act)) return { ok: false, error: errBody("bad_request", "bad alarm action") };
      if (act === "set" && !Number.isFinite(args.time)) return { ok: false, error: errBody("bad_request", "bad alarm time") };
      try {
        return { ok: true, value: await this.ctx.exports["Host_" + p.class].getByName(p.name).__tw_alarm_op(act, args.time, await hostCap(this.env)) };
      } catch (e) {
        const m = String(e?.message ?? e);
        return { ok: false, error: errBody(m.includes("[killed]") ? "killed" : "bad_request", m) };
      }
    }
    if (op === "approval") return approvalReport(this.env, p, args);
    if (op === "unseal") {
      // The runtime's check of a request's caller seal (RT3-08). A verify
      // oracle only: nothing here makes a seal.
      return { ok: true, value: { caller: await verifiedCaller(this.env, args.seal) } };
    }
    if (op === "admit") {
      // Create admission: may the caller reach, and so create, class/name?
      // hostd decides from who asks: an attested object (as), a tailnet
      // request (its ticket), a Workflow (workflow: classes with no node,
      // and existing objects), or a public request (public.create, ADR-029
      // D5). Nothing is created here.
      if (typeof args.class !== "string" || !CFG.classes[args.class] || typeof args.name !== "string" || !args.name) {
        return { ok: false, error: errBody("create_not_admitted", "admit: unknown class or empty name") };
      }
      try {
        return {
          ok: true,
          value: await hostd(this.env, "admit", {
            class: args.class,
            name: args.name,
            ticket: typeof args.ticket === "string" ? args.ticket : undefined,
            workflow: args.workflow === true,
            as,
          }),
        };
      } catch (e) {
        return { ok: false, error: e.body ?? errBody("hostd_unavailable", String(e)) };
      }
    }
    if (op === "spans") {
      // The runtime's DO-call spans (ADR-045): observability
      // data only, attributed to the attested caller.
      if (!Array.isArray(args.spans)) return { ok: false, error: errBody("bad_request", "spans must be an array") };
      try {
        return { ok: true, value: await hostd(this.env, "spans", { spans: args.spans.slice(0, 64), as }) };
      } catch (e) {
        return { ok: false, error: e.body ?? errBody("hostd_unavailable", String(e)) };
      }
    }
    if (!["ensure", "spawn", "ready", "kill", "audit", "self"].includes(op)) return { ok: false, error: errBody("bad_request", "unknown op " + op) };
    if (op === "ensure" && !as) return { ok: false, error: errBody("bad_request", "ensure needs an object") };
    // Tenant code never names a principal: actingFor comes from the
    // platform's own calls (Host_<Class>), never from these args (RT3-08).
    const { actingFor: _af, public: _pub, ...rest } = args;
    try {
      return { ok: true, value: await hostd(this.env, op, { ...rest, as }) };
    } catch (e) {
      return { ok: false, error: e.body ?? errBody("hostd_unavailable", String(e)) };
    }
  }
}

// ---- observability (ADR-045) ----

const TAIL_MAX_LOGS = 100;
const TAIL_MAX_LINE = 4096;
const TAIL_MAX_EXC = 8;

// logText renders one console call's arguments as a line.
function logText(args) {
  const parts = Array.isArray(args) ? args : [args];
  return parts
    .map((a) => {
      if (typeof a === "string") return a;
      try {
        return JSON.stringify(a) ?? String(a);
      } catch {
        return String(a);
      }
    })
    .join(" ")
    .slice(0, TAIL_MAX_LINE);
}

function pathOf(u) {
  try {
    return new URL(u).pathname; // never the query string
  } catch {
    return "";
  }
}

// slimEvent keeps what a request record needs from a workerd TraceItem:
// no request header but traceparent, no query string, no cf beyond the
// route, never the create ticket.
function slimEvent(e) {
  const ev = e?.event ?? {};
  const out = {
    time: e?.eventTimestamp ?? 0,
    outcome: String(e?.outcome ?? ""),
    entrypoint: String(e?.entrypoint ?? ""),
    model: String(e?.executionModel ?? ""),
    doId: String(e?.durableObjectId ?? ""),
    truncated: !!e?.truncated,
    event: "",
  };
  if (ev.request) {
    out.event = "fetch";
    out.method = String(ev.request.method ?? "");
    out.path = pathOf(ev.request.url);
    const tp = ev.request.headers?.traceparent;
    if (typeof tp === "string") out.traceparent = tp;
    const t = ev.request.cf?.tw;
    if (t && typeof t === "object") {
      out.route = { kind: String(t.kind ?? ""), class: String(t.class ?? ""), name: String(t.name ?? "") };
      if (t.kind === "scheduled") out.event = "scheduled";
    }
    out.status = ev.response?.status ?? 0;
  } else if (typeof ev.rpcMethod === "string") {
    out.event = ev.rpcMethod === "__tw_alarm" ? "alarm" : "rpc";
    out.method = ev.rpcMethod;
  } else if (typeof ev.cron === "string") {
    out.event = "scheduled";
  } else if (ev.scheduledTime !== undefined) {
    out.event = "alarm";
  } else if (ev.getWebSocketEvent || ev.webSocketEventType || ev.type === "message" || ev.type === "close" || ev.type === "error") {
    out.event = "websocket";
  }
  out.logs = (Array.isArray(e?.logs) ? e.logs : []).slice(0, TAIL_MAX_LOGS).map((l) => ({ t: Number(l?.timestamp ?? 0), level: String(l?.level ?? "log"), msg: logText(l?.message) }));
  out.exceptions = (Array.isArray(e?.exceptions) ? e.exceptions : []).slice(0, TAIL_MAX_EXC).map((x) => ({
    t: Number(x?.timestamp ?? 0),
    name: String(x?.name ?? "").slice(0, 256),
    message: String(x?.message ?? "").slice(0, TAIL_MAX_LINE),
    stack: String(x?.stack ?? "").slice(0, TAIL_MAX_LINE),
  }));
  if ((e?.logs?.length ?? 0) > TAIL_MAX_LOGS || (e?.exceptions?.length ?? 0) > TAIL_MAX_EXC) out.truncated = true;
  return out;
}

// Tail is the tail worker of the tenant service (props {kind: "workload"},
// config.capnp) and of every facet object (props {kind: "object", class,
// name}, minted by Host_<Class>, so a facet's records are attested). It
// hands the slimmed events to hostd, which joins them with its ingress
// records. The platform worker itself has no tails: it would trace this.
export class Tail extends WorkerEntrypoint {
  async tail(events) {
    const p = this.ctx.props ?? {};
    const evs = (Array.isArray(events) ? events : []).filter((e) => !e?.event?.consumedEvents).map(slimEvent);
    if (!evs.length) return;
    const as = p.kind === "object" && typeof p.class === "string" && typeof p.name === "string" ? { class: p.class, name: p.name } : null;
    try {
      const r = await this.env.HOSTD.fetch("http://hostd/v1/tail", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ as, events: evs }),
      });
      await r.body?.cancel();
    } catch {
      /* hostd restarting: these records are lost */
    }
  }
}

// ---- Workflows and approvals (ADR-040) ----
//
// WF_<name> binds workerd's built-in engine (its WorkflowBinding entrypoint).
// Tenant code never holds it: its env.<binding> is the Workflow API over
// WorkflowGate (props minted here or in the generated config).

const isApprovalType = (t) => t === "approval" || (typeof t === "string" && t.startsWith("approval-"));

function engineOf(env, workflow) {
  const w = (CFG.workflows ?? []).find((x) => x.name === workflow);
  const e = w && env["WF_" + w.name];
  if (!e) throw new Error("no Workflow " + workflow + " in this app");
  return e;
}

// dropApprover removes any "approver" an app put in an event payload (and
// in payload.metadata, which waitForApproval returns): only tailworks sets it.
function dropApprover(payload) {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return payload;
  const { approver: _a, ...rest } = payload;
  if (rest.metadata && typeof rest.metadata === "object" && !Array.isArray(rest.metadata)) {
    const { approver: _m, ...meta } = rest.metadata;
    rest.metadata = meta;
  }
  return rest;
}

// WorkflowGate is the Workflow API tenant code reaches (props.workflow). It
// forwards to the engine, except that an approval event (type "approval" or
// "approval-*") can only come from tailworks, which checked the approver's grant
// (hostd), and no other event may carry an approver.
export class WorkflowGate extends WorkerEntrypoint {
  get #e() {
    return engineOf(this.env, this.ctx.props.workflow);
  }
  getInstance(id) {
    return this.#e.getInstance(id);
  }
  deleteInstance(id) {
    return this.#e.deleteInstance(id);
  }
  create(options) {
    return this.#e.create(options);
  }
  createBatch(options) {
    return this.#e.createBatch(options);
  }
  deleteBatch(options) {
    return this.#e.deleteBatch(options);
  }
  pause(id) {
    return this.#e.pause(id);
  }
  resume(id) {
    return this.#e.resume(id);
  }
  terminate(id, options) {
    return this.#e.terminate(id, options);
  }
  restart(id, options) {
    return this.#e.restart(id, options);
  }
  status(id) {
    return this.#e.status(id);
  }
  subscribe(id, options) {
    return this.#e.subscribe(id, options);
  }
  async sendEvent(id, event) {
    const type = event?.type;
    if (isApprovalType(type)) {
      throw new Error(
        "[approval_requires_identity] an approval is a decision by a tailnet identity: send it through tw (POST /_tw/approvals/" +
          this.ctx.props.workflow + "/<instance> on this workload's node, tw approve, or the admin UI), not from app code",
      );
    }
    return this.#e.sendEvent(id, { type, payload: dropApprover(event?.payload) });
  }
}

// deliverApproval sends an approval hostd authorised and audited (cf.tw
// kind "approval": only hostd sets it) to the waiting instance.
async function deliverApproval(env, t) {
  if (typeof t.workflow !== "string" || typeof t.instance !== "string" || !isApprovalType(t.type)) {
    return errResponse("bad_request", "bad approval route");
  }
  try {
    await engineOf(env, t.workflow).sendEvent(t.instance, { type: t.type, payload: t.payload ?? {} });
  } catch (e) {
    const m = String(e?.message ?? e);
    if (m.includes("instance.not_found")) return errResponse("unknown_object", "no instance " + t.instance + " of Workflow " + t.workflow);
    return json(502, errBody("bad_request", "approval delivery: " + m.slice(0, 512)), { "tw-error": "bad_request" });
  }
  return json(200, { ok: true });
}

// approvalReport: the runtime in the tenant service reports an approval
// wait (op "requested" | "timeout") of an instance of one of this app's
// Workflows. The instance must exist; hostd records it (pending list,
// audit). Only the tenant service runs Workflows, so facets may not report.
async function approvalReport(env, props, args) {
  if (props.kind !== "workload") return { ok: false, error: errBody("bad_request", "only a Workflow may report an approval wait") };
  const a = args ?? {};
  if (!["requested", "timeout"].includes(a.op) || typeof a.instance !== "string" || typeof a.step !== "string" || !isApprovalType(a.type)) {
    return { ok: false, error: errBody("bad_request", "bad approval report") };
  }
  try {
    await engineOf(env, a.workflow).status(a.instance);
  } catch (e) {
    return { ok: false, error: errBody("unknown_object", "approval report: " + String(e?.message ?? e).slice(0, 256)) };
  }
  const facts = {};
  for (const [k, v] of Object.entries(a.facts ?? {})) if (typeof v === "number" && Number.isFinite(v) && Object.keys(facts).length < 32) facts[k.slice(0, 64)] = v;
  const approval = {
    op: a.op,
    workflow: a.workflow,
    instance: a.instance,
    step: a.step.slice(0, 256),
    type: a.type,
    timeout: typeof a.timeout === "string" || typeof a.timeout === "number" ? String(a.timeout).slice(0, 64) : "",
    facts,
  };
  try {
    return { ok: true, value: await hostd(env, "approval", { approval }) };
  } catch (e) {
    return { ok: false, error: e.body ?? errBody("hostd_unavailable", String(e)) };
  }
}
