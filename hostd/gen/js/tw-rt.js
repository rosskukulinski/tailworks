// js/packages/tailnet/src/errors.ts
var DOC_BASE = "https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#";
var CATALOG = Object.freeze({
  peer_not_visible: { status: 403, title: "the destination is not in this node's netmap (a grant is missing, or the name does not exist)" },
  forbidden_destination: { status: 403, title: "the destination class is refused by netd (loopback, self, sibling, host, non-tailnet)" },
  internet_egress_unavailable: { status: 403, title: "internet egress is turned off for this workload or host (tailnet.egress.internet), or the host is not on its allowlist" },
  internet_port_refused: { status: 403, title: "internet egress to TCP port 25 is refused unless tailnet.egress.internet names the destination with that port" },
  acl_denied: { status: 403, title: "the peer is visible but the port or service is not granted" },
  cap_missing: { status: 403, title: "the caller lacks the app capability (or the tool) required" },
  cap_limit_exceeded: { status: 403, title: "within the granted tool, but above a numeric limit or budget" },
  delegation_invalid: { status: 403, title: "the on-behalf-of assertion failed verification" },
  delegation_exceeds_parent: { status: 403, title: "spawn asked for caps the parent does not have" },
  spawn_budget_exceeded: { status: 429, title: "too many live descendants or too deep a spawn chain" },
  bootstrap_failed: { status: 502, title: "the object's node could not be created (mint, token exchange or join failed)" },
  identity_pending: { status: 503, title: "the object's node is not ready yet" },
  derp_unreachable: { status: 502, title: "no direct path and no reachable DERP relay" },
  plan_feature_missing: { status: 501, title: "the tailnet's plan or control server lacks a feature tailworks can use" },
  not_supported: { status: 501, title: "tailworks doesn't support this call here (yet)" },
  socket_limit: { status: 429, title: "too many open sockets for this object" },
  killed: { status: 410, title: "the object was killed" },
  object_killed: { status: 410, title: "a new request reached a killed object (its node's tombstone answered)" },
  no_identity: { status: 401, title: "the request carries no verified tailnet caller" },
  lease_lost: { status: 409, title: "this host no longer holds the namespace lease" },
  namespace_conflict: { status: 409, title: "mgmt returned another identity's object for this workload's class + object name (an invariant violation; tailworks fails closed rather than share a node)" },
  bad_request: { status: 400, title: "the request is malformed" },
  unknown_object: { status: 404, title: "no such object" },
  service_not_found: { status: 404, title: "a service binding's target workload (or its entrypoint) does not exist in this tenant" },
  binding_forbidden: { status: 403, title: "a named entrypoint or RPC method was called by something that is not a tailworks node of the same tenant" },
  rpc_not_forwardable: { status: 400, title: "an RPC argument or result is passed by reference on Cloudflare (a function, RpcTarget, stream, Request or Response) and cannot cross a service binding" },
  dial_failed: { status: 502, title: "the tailnet dial failed" },
  upstream_unavailable: { status: 503, title: "the platform upstream is unavailable" },
  hostd_unavailable: { status: 503, title: "the host agent is unavailable" },
  public_request: { status: 403, title: "a public (Funnel) request reached code that needs a tailnet caller; use getRequester()" },
  public_create_denied: { status: 403, title: "a public request may not create a new object (or spawn); only existing objects, or classes with public.create" },
  create_not_admitted: { status: 403, title: "the object does not exist and nothing admitted its creation: reach new objects through the env tailworks hands your handler (not a raw or cached binding, not from a public request)" },
  public_ingress_not_allowed: { status: 403, title: "the tenant does not allow public ingress (operator setting publicIngress)" },
  public_name_taken: { status: 409, title: "the workload's public node name is taken on the tailnet (MagicDNS would rename it)" },
  rate_limited: { status: 429, title: "too many public requests (per workload or per client IP)" },
  payload_too_large: { status: 413, title: "the public request body is above the workload's limit" },
  header_too_large: { status: 431, title: "the public request headers are above the workload's limit" },
  not_public: { status: 404, title: "this path is not one of the workload's public routes" },
  upgrade_not_allowed: { status: 403, title: "connection upgrades (WebSocket) are not allowed on this public route" }
});
var BRAND = /* @__PURE__ */ Symbol.for("tw.TailnetError");
var TailnetError = class _TailnetError extends Error {
  code;
  missing;
  snippet;
  docUrl;
  traceId;
  status;
  /** Human-readable detail (message may be JSON; see @tailworks/mcp). */
  detail;
  constructor(code, message, init = {}) {
    super(message ?? CATALOG[code]?.title ?? code);
    this.name = "TailnetError";
    this.code = code;
    this.detail = message ?? this.message;
    const { missing, snippet, docUrl, traceId, status, ...rest } = init;
    Object.assign(this, rest);
    this.missing = missing;
    this.snippet = snippet;
    this.docUrl = docUrl ?? DOC_BASE + code;
    this.traceId = traceId ?? "";
    this.status = status ?? CATALOG[code]?.status ?? 500;
    Object.defineProperty(this, BRAND, { value: true });
  }
  /** The wire body (DX.md shape). */
  toJSON() {
    const o = { code: this.code, message: this.detail, doc_url: this.docUrl };
    if (this.missing) o.missing = this.missing;
    if (this.snippet) o.snippet = this.snippet;
    if (this.traceId) o.trace_id = this.traceId;
    for (const k of ["have", "chain", "limit", "used", "object", "destination"]) if (this[k] !== void 0) o[k] = this[k];
    return o;
  }
  /** Render as an HTTP response with the tw-error header. */
  toResponse() {
    return new Response(JSON.stringify(this.toJSON()), {
      status: this.status,
      headers: { "content-type": "application/json", "tw-error": String(this.code), "cache-control": "no-store" }
    });
  }
  /** Build from a wire body ({code, message|detail, ...}). */
  static from(body, status) {
    const b = body ?? {};
    const code = typeof b.code === "string" && b.code ? b.code : "upstream_unavailable";
    const { code: _c, message, detail, doc_url, trace_id, missing, snippet, ...rest } = b;
    return new _TailnetError(code, detail ?? message ?? code, { ...rest, missing, snippet, docUrl: doc_url, traceId: trace_id, status });
  }
};
function isTailnetError(e) {
  return !!e && typeof e === "object" && (e[BRAND] === true || e.name === "TailnetError");
}
async function fromResponse(r) {
  const code = r.headers.get("tw-error");
  if (!code) return null;
  let body = { code };
  try {
    body = { code, ...await r.clone().json() };
  } catch {
  }
  return TailnetError.from(body, r.status);
}

// js/packages/tailnet/src/container.ts
var NOT_LISTENING = "the container is not listening";
var NOT_RUNNING = "the container is not running, consider calling start()";
var UNEXPECTED_EXIT = "Container exited with unexpected exit code: ";
var RUNTIME_SIGNALLED = "Runtime signalled the container to exit: ";
var STATE_KEY = "__tw_container";
var handlers = /* @__PURE__ */ new WeakMap();
async function dispatchIntercept(ctx, key, req) {
  const h = handlers.get(ctx)?.get(key);
  const fetcher = typeof h === "function" ? { fetch: h } : h;
  if (!fetcher || typeof fetcher.fetch !== "function") {
    return new Response(
      JSON.stringify({ code: "intercept_unavailable", message: "no handler for " + key + " in this object (it was registered before the object restarted; register it again)" }),
      { status: 502, headers: { "content-type": "application/json", "tw-error": "intercept_unavailable" } }
    );
  }
  return await fetcher.fetch(req);
}
function notSupportedPreview() {
  return Object.assign(
    new Error("exposePort()/preview URLs are not supported on tailworks yet: they need public ingress (ADR-029); reach a container port from its object with ctx.container.getTcpPort(port).fetch()"),
    { code: "not_supported" }
  );
}
function err(body, fallback) {
  const code = body?.code ?? "container_error";
  let msg = String(body?.message ?? body?.detail ?? fallback);
  if (code === "not_listening") msg = NOT_LISTENING + " (" + msg + ")";
  else if (code === "container_not_running") msg = NOT_RUNNING;
  const e = new Error(msg);
  e.code = code;
  return e;
}
async function unwrap(p) {
  const r = await p;
  if (!r || !r.ok) throw err(r?.error, "container op failed");
  return r.value;
}
async function errorOf(r) {
  let body = { code: r.headers.get("tw-error") ?? "container_error", message: "HTTP " + r.status };
  try {
    body = await r.json();
  } catch {
  }
  return err(body, "HTTP " + r.status);
}
function toBytes(chunks) {
  let n = 0;
  for (const c of chunks) n += c.byteLength;
  const out = new Uint8Array(n);
  let o = 0;
  for (const c of chunks) {
    out.set(c, o);
    o += c.byteLength;
  }
  return out.buffer;
}
async function readAll(s) {
  if (!s) return new ArrayBuffer(0);
  const chunks = [];
  const rd = s.getReader();
  for (; ; ) {
    const { done, value } = await rd.read();
    if (done) break;
    chunks.push(value);
  }
  return toBytes(chunks);
}
function demux(body, wantOut, wantErr) {
  const ctl = { out: null, err: null };
  const stdout = wantOut ? new ReadableStream({ start: (c) => void (ctl.out = c) }) : null;
  const stderr = wantErr ? new ReadableStream({ start: (c) => void (ctl.err = c) }) : null;
  const outC = () => ctl.out;
  const errC = () => ctl.err;
  const exitCode = (async () => {
    const rd = body.getReader();
    let buf = new Uint8Array(0);
    const fail = (e) => {
      for (const c of [outC(), errC()]) {
        try {
          c?.error(e);
        } catch {
        }
      }
    };
    try {
      for (; ; ) {
        while (buf.byteLength >= 5) {
          const len = new DataView(buf.buffer, buf.byteOffset + 1, 4).getUint32(0);
          if (buf.byteLength < 5 + len) break;
          const kind = buf[0];
          const p = buf.slice(5, 5 + len);
          buf = buf.subarray(5 + len);
          if (kind === 1) outC()?.enqueue(p);
          else if (kind === 2) errC()?.enqueue(p);
          else if (kind === 3) {
            const code = p.byteLength === 4 ? new DataView(p.buffer, p.byteOffset, 4).getInt32(0) : -1;
            for (const c of [outC(), errC()]) {
              try {
                c?.close();
              } catch {
              }
            }
            rd.cancel().catch(() => {
            });
            return code;
          }
        }
        const { done, value } = await rd.read();
        if (done) throw new Error("exec stream ended without an exit status");
        const nb = new Uint8Array(buf.byteLength + value.byteLength);
        nb.set(buf);
        nb.set(value, buf.byteLength);
        buf = nb;
      }
    } catch (e) {
      fail(e);
      throw e;
    }
  })();
  exitCode.catch(() => {
  });
  return { stdout, stderr, exitCode };
}
function createContainer(tw, ctx, cls, spec, self) {
  const as = self ? { as: self } : {};
  const op = (name, args = {}) => unwrap(tw.ctr(name, { ...as, ...args })).catch((e) => {
    if (e?.code === "container_not_running" && !starting) {
      running = false;
      save();
    }
    throw e;
  });
  const kv = ctx?.storage?.kv;
  const load = () => {
    try {
      const v = kv?.get(STATE_KEY);
      if (v && typeof v === "object") return { running: !!v.running, gen: Number(v.gen) || 0 };
    } catch {
    }
    return { running: false, gen: 0 };
  };
  const st = load();
  let running = st.running;
  let gen = st.gen;
  let starting = null;
  let startErr = null;
  const intercepts = /* @__PURE__ */ new Map();
  if (ctx && typeof ctx === "object") handlers.set(ctx, intercepts);
  const save = () => {
    try {
      kv?.put(STATE_KEY, { running, gen });
    } catch {
    }
  };
  const keep = (p) => {
    try {
      ctx.waitUntil?.(p);
    } catch {
    }
  };
  let watching = 0;
  const watch = () => {
    const g = gen;
    if (!running || !g || watching === g) return;
    watching = g;
    keep((async () => {
      try {
        for (; ; ) {
          const v = await op("monitor", { gen: g });
          if (v.done) {
            if (gen === g) {
              running = false;
              save();
            }
            return;
          }
        }
      } catch {
      } finally {
        if (watching === g) watching = 0;
      }
    })());
  };
  const intercept = async (kind, addr, binding) => {
    const b = binding;
    if (!b || typeof b !== "function" && typeof b.fetch !== "function") throw new TypeError("intercept: the handler must be a Fetcher (an object with fetch())");
    await settle();
    const key = kind + " " + addr;
    const v = await op("intercept", { kind, addr });
    intercepts.set(String(v?.key ?? key), binding);
  };
  const settle = async () => {
    if (starting) await starting.catch(() => {
    });
    watch();
  };
  const reconcile = async () => {
    try {
      const v = await op("status");
      running = !!v.running;
      gen = Number(v.gen) || 0;
      save();
      watch();
    } catch {
    }
  };
  try {
    ctx.blockConcurrencyWhile?.(reconcile);
  } catch {
  }
  const c = {
    get running() {
      return running;
    },
    get images() {
      return { ...spec.images ?? {} };
    },
    start(options = {}) {
      if (running) throw new Error("start() cannot be called on a container that is already running.");
      if (options?.containerSnapshot) throw Object.assign(new Error("containerSnapshot is not supported on tailworks yet (ADR-032 D6: v1.1)"), { code: "not_supported" });
      if (options?.directorySnapshots?.length) throw Object.assign(new Error("directorySnapshots are not supported on tailworks"), { code: "not_supported" });
      running = true;
      startErr = null;
      save();
      const req = {
        image: options.image,
        entrypoint: options.entrypoint,
        env: options.env,
        enableInternet: !!options.enableInternet,
        instance: options.instance,
        labels: options.labels
      };
      const p = op("start", { start: req }).then(
        (v) => {
          gen = Number(v.gen) || gen + 1;
          save();
          watch();
        },
        (e) => {
          running = false;
          startErr = e;
          save();
          throw e;
        }
      );
      starting = p;
      p.catch(() => {
      }).finally(() => {
        if (starting === p) starting = null;
      });
      keep(p.catch(() => {
      }));
    },
    async monitor() {
      if (starting) await starting;
      else if (!running) {
        if (startErr) throw startErr;
        throw new Error(NOT_RUNNING);
      }
      const g = gen;
      for (; ; ) {
        const v = await op("monitor", { gen: g });
        if (!v.done) continue;
        if (gen === g) {
          running = false;
          save();
        }
        const code = Number(v.exitCode);
        const reason = String(v.reason ?? "");
        if (code === 0 || reason.startsWith("destroy")) return;
        if (reason === "exited") throw Object.assign(new Error(UNEXPECTED_EXIT + code), { exitCode: code });
        throw Object.assign(new Error(RUNTIME_SIGNALLED + (code >= 0 ? code : 137) + " (" + reason + ")"), { exitCode: code });
      }
    },
    async destroy(reason) {
      await settle();
      await op("destroy", { reason: reason === void 0 ? "" : String(reason?.message ?? reason) });
      running = false;
      save();
    },
    signal(signo) {
      if (!Number.isInteger(signo) || signo < 1 || signo > 64) throw new RangeError("signal: bad signal number");
      const p = settle().then(() => op("signal", { signal: signo }));
      p.catch(() => {
      });
      keep(p.catch(() => {
      }));
    },
    async setInactivityTimeout(ms) {
      const n = Number(ms);
      if (!Number.isFinite(n) || n < 0) throw new RangeError("setInactivityTimeout: bad duration");
      await settle();
      if (!running) return;
      await op("setInactivityTimeout", { ms: Math.floor(n) });
    },
    async inspect() {
      await settle();
      const v = await op("inspect");
      return v.info ?? null;
    },
    getTcpPort(port) {
      if (!Number.isInteger(port) || port < 1 || port > 65535) throw new RangeError("getTcpPort: bad port");
      return {
        async fetch(input, init) {
          await settle();
          const src = new Request(input, init);
          const sig = init?.signal ?? (input instanceof Request ? input.signal : void 0);
          if (sig?.aborted) throw sig.reason ?? new Error("aborted");
          const hasBody = src.method !== "GET" && src.method !== "HEAD";
          const req = new Request(src.url, { method: src.method, headers: src.headers, body: hasBody ? src.body : void 0, redirect: "manual" });
          const pending = tw.ctrFetch({ ...as, port }, req);
          const r = sig ? await new Promise((res, rej) => {
            const onAbort = () => rej(sig.reason ?? new Error("aborted"));
            sig.addEventListener("abort", onAbort, { once: true });
            pending.then(
              (v) => {
                sig.removeEventListener("abort", onAbort);
                res(v);
              },
              (e) => {
                sig.removeEventListener("abort", onAbort);
                rej(e);
              }
            );
          }) : await pending;
          const code = r.headers.get("tw-error");
          if (code && (code === "not_listening" || code === "container_not_running" || code.startsWith("container_"))) {
            throw await errorOf(r);
          }
          return r;
        },
        connect() {
          throw Object.assign(new Error("getTcpPort().connect() is not supported on tailworks yet (ADR-032: deferred)"), { code: "not_supported" });
        }
      };
    },
    async exec(argv, options = {}) {
      if (!Array.isArray(argv) || argv.length === 0 || argv.some((a) => typeof a !== "string")) throw new TypeError("exec: argv must be a non-empty string array");
      await settle();
      if (!running) throw startErr ?? new Error(NOT_RUNNING);
      const sig = options.signal;
      if (sig?.aborted) throw sig.reason ?? new Error("aborted");
      const ptyOpt = options.pty;
      const pty = !!ptyOpt;
      const stdoutMode = options.stdout ?? "pipe";
      const stderrMode = pty ? "ignore" : options.stderr ?? "pipe";
      const stdinOpt = options.stdin;
      const execSpec = {
        argv,
        env: options.env,
        cwd: options.cwd,
        user: options.user,
        stdin: stdinOpt !== void 0 && stdinOpt !== null,
        stdout: stdoutMode,
        stderr: stderrMode,
        pty,
        cols: typeof ptyOpt === "object" ? ptyOpt.cols : void 0,
        rows: typeof ptyOpt === "object" ? ptyOpt.rows : void 0
      };
      const r = await tw.ctrExec({ ...as, spec: execSpec });
      if (!r.ok || !r.body) throw await errorOf(r);
      const id = r.headers.get("x-tw-exec") ?? "";
      const pid = Number(r.headers.get("x-tw-exec-pid") ?? 0);
      const { stdout, stderr, exitCode } = demux(r.body, stdoutMode !== "ignore", stderrMode === "pipe");
      let done = false;
      exitCode.then(
        () => done = true,
        () => done = true
      );
      let stdin = null;
      if (execSpec.stdin) {
        let src;
        if (stdinOpt === "pipe") {
          const ts = new TransformStream();
          stdin = ts.writable;
          src = ts.readable;
        } else {
          src = stdinOpt;
        }
        const p = tw.ctrStdin({ ...as, exec: id }, src).catch(() => {
        });
        keep(p);
      }
      const kill = (signo = 15) => {
        if (done) return;
        const p = op("exec-signal", { exec: id, signal: signo }).catch(() => {
        });
        keep(p);
      };
      if (sig) {
        const onAbort = () => kill(9);
        sig.addEventListener("abort", onAbort, { once: true });
        exitCode.finally(() => sig.removeEventListener("abort", onAbort)).catch(() => {
        });
      }
      return {
        stdin,
        stdout,
        stderr,
        pid,
        isPty: pty,
        exitCode,
        async output() {
          const [o, e, code] = await Promise.all([readAll(stdout), readAll(stderr), exitCode]);
          return { stdout: o, stderr: e, exitCode: code };
        },
        kill,
        resize(cols, rows) {
          if (!pty) throw new Error("resize: not a PTY exec");
          const p = op("exec-resize", { exec: id, cols, rows }).catch(() => {
          });
          keep(p);
        }
      };
    },
    // Egress interception: hostd's sandbox egress proxy delivers the
    // container's matching requests back to this object, which runs them
    // through the handler kept here (dispatchIntercept). HTTPS is
    // terminated with the container's own ephemeral CA (its certificate is
    // at /etc/cloudflare/certs/cloudflare-containers-ca.crt in the
    // container). At most 128 per container.
    async interceptOutboundHttp(addr, binding) {
      await intercept("http", String(addr), binding);
    },
    async interceptOutboundHttps(addr, binding) {
      await intercept("https", String(addr), binding);
    },
    async interceptAllOutboundHttp(binding) {
      await intercept("all", "*", binding);
    },
    async snapshotContainer() {
      throw Object.assign(new Error("snapshotContainer() is not supported on tailworks yet (ADR-032 D6: v1.1)"), { code: "not_supported" });
    }
  };
  void cls;
  return Object.freeze(c);
}
function defineContainer(ctx, c) {
  try {
    Object.defineProperty(ctx, "container", { value: c, enumerable: false, configurable: true });
  } catch {
  }
}

// js/packages/tailnet/src/workflows.ts
function isApprovalType(t) {
  return t === "approval" || typeof t === "string" && t.startsWith("approval-");
}
var REPORT_STEP = { retries: { limit: 10, delay: "1 second", backoff: "exponential" }, timeout: "30 seconds" };
function numericFacts(payload) {
  const out = {};
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return out;
  for (const [k, v] of Object.entries(payload)) {
    if (typeof v === "number" && Number.isFinite(v) && !k.startsWith("__")) out[k] = v;
  }
  return out;
}
function isAborted(e) {
  return e?.name === "WorkflowAbortedError" || String(e?.message ?? "").includes("WorkflowAbortedError");
}
function isTimeout(e) {
  const m = String(e?.message ?? "");
  return e?.name === "WorkflowTimeoutError" || m.includes("Execution timed out");
}
async function report(tw, args) {
  if (!tw) return false;
  const r = await tw.call("approval", args);
  if (!r.ok) throw new Error(`[${r.error?.code ?? "hostd_unavailable"}] approval ${args.op}: ${r.error?.message ?? "refused"}`);
  return true;
}
var wrapped = /* @__PURE__ */ new WeakSet();
function installApprovalWaits(step, tw, workflow, event) {
  if (!step || typeof step !== "object" && typeof step !== "function" || wrapped.has(step)) return;
  const origWait = step.waitForEvent;
  const origDo = step.do;
  if (typeof origWait !== "function" || typeof origDo !== "function") return;
  const wait = (...a) => Reflect.apply(origWait, step, a);
  const doStep = (...a) => Reflect.apply(origDo, step, a);
  const instance = String(event?.instanceId ?? "");
  const facts = numericFacts(event?.payload);
  step.waitForEvent = async (name, opts) => {
    const type = opts?.type;
    if (!isApprovalType(type) || !instance) return wait(name, opts);
    const base = { workflow, instance, step: name, type, timeout: opts?.timeout ?? "24 hours", facts };
    await doStep(`__tw_approval_requested:${name}`, REPORT_STEP, () => report(tw, { ...base, op: "requested" }));
    try {
      return await wait(name, opts);
    } catch (e) {
      if (isTimeout(e)) await doStep(`__tw_approval_timeout:${name}`, REPORT_STEP, () => report(tw, { ...base, op: "timeout" }));
      throw e;
    }
  };
  wrapped.add(step);
}
function wrapWorkflowClass(C, name, workflow, _binding, bindings) {
  if (typeof C !== "function") throw new Error(`tw: workflows class ${name} (Workflow ${workflow}) is not exported by the main module`);
  const W = {
    [name]: class extends C {
      constructor(ctx, env) {
        const tw = env?.__TW;
        super(ctx, guardTailnetEnv(env, bindings));
        Object.defineProperty(this, "__tw_tw", { value: tw });
      }
      async run(event, step) {
        installApprovalWaits(step, this.__tw_tw, workflow, event);
        try {
          return await super.run(event, step);
        } catch (e) {
          if (isAborted(e) && "_errorReported" in this) this._errorReported = true;
          throw e;
        }
      }
    }
  }[name];
  return W;
}
var InstanceClient = class {
  id;
  #f;
  constructor(id, f) {
    this.id = id;
    this.#f = f;
  }
  async pause() {
    await this.#f.pause(this.id);
  }
  async resume() {
    await this.#f.resume(this.id);
  }
  async terminate(options) {
    await this.#f.terminate(this.id, options);
  }
  async restart(options) {
    await this.#f.restart(this.id, options);
  }
  async delete() {
    await this.#f.deleteInstance(this.id);
  }
  async status() {
    return await this.#f.status(this.id);
  }
  async sendEvent({ type, payload }) {
    await this.#f.sendEvent(this.id, { type, payload });
  }
  async subscribe(options) {
    return await this.#f.subscribe(this.id, options);
  }
};
function workflowClient(f) {
  return {
    async get(id) {
      const r = await f.getInstance(id);
      return new InstanceClient(r.id, f);
    },
    async create(options) {
      const r = await f.create(options);
      return new InstanceClient(r.id, f);
    },
    async createBatch(options) {
      const r = await f.createBatch(options);
      if (Array.isArray(options)) return r.map((x) => new InstanceClient(x.id, f));
      return { created: r.created.map((x) => new InstanceClient(x.id, f)), errors: r.errors };
    },
    async deleteBatch(instanceIds) {
      return await f.deleteBatch({ instances: instanceIds });
    }
  };
}

// js/packages/tailnet/src/d1.ts
var SESSION_HEADER = "x-cf-d1-session-commit-token";
var FIRST_UNCONSTRAINED = "first-unconstrained";
var FIRST_PRIMARY = "first-primary";
function d1Error(prefix, message) {
  return new Error(`${prefix}: ${message}`, { cause: new Error(message) });
}
function first(r) {
  return Array.isArray(r) ? r[0] : r;
}
function toObjects(r) {
  const res = r.results;
  if (res === void 0) return { ...r, results: [] };
  if (Array.isArray(res)) return { ...r, results: res };
  return { ...r, results: res.rows.map((row) => Object.fromEntries(row.map((c, i) => [res.columns[i], c]))) };
}
var D1Session = class {
  #f;
  #token;
  constructor(f, token) {
    this.#f = f;
    this.#token = token;
  }
  get fetcher() {
    return this.#f;
  }
  prepare(sql) {
    return new D1Statement(this, sql, []);
  }
  async batch(stmts) {
    const r = await this.sendOrThrow("/query", stmts.map((s) => ({ sql: s.statement, params: s.params })), "ROWS_AND_COLUMNS");
    return (Array.isArray(r) ? r : [r]).map(toObjects);
  }
  getBookmark() {
    return this.#token === FIRST_UNCONSTRAINED || this.#token === FIRST_PRIMARY ? null : this.#token;
  }
  update(b) {
    b = b.trim();
    const cur = this.getBookmark();
    if (b && (cur === null || cur.localeCompare(b) < 0)) this.#token = b;
  }
  async send(path, body, format) {
    const headers = { "content-type": "application/json" };
    if (this.#token) headers[SESSION_HEADER] = this.#token;
    const resp = await this.#f.fetch("http://d1" + path + "?resultsFormat=" + format, { method: "POST", headers, body: JSON.stringify(body) });
    const bm = resp.headers.get(SESSION_HEADER);
    if (bm) this.update(bm);
    const text = await resp.text();
    let j;
    try {
      j = JSON.parse(text);
    } catch {
      throw d1Error("D1_ERROR", `Failed to parse body as JSON, got: ${text}`);
    }
    const norm = (r) => r.error ? { success: false, error: r.error } : { success: true, meta: r.meta, ..."results" in r ? { results: r.results } : {} };
    return Array.isArray(j) ? j.map(norm) : norm(j);
  }
  async sendOrThrow(path, body, format) {
    const r = await this.send(path, body, format);
    const f = first(r);
    if (!f.success) throw d1Error("D1_ERROR", f.error ?? "Something went wrong");
    return r;
  }
};
var D1AlwaysPrimary = class extends D1Session {
  constructor(f) {
    super(f, null);
  }
  getBookmark() {
    return null;
  }
  update() {
  }
};
var D1Database = class extends D1AlwaysPrimary {
  async exec(query) {
    const lines = query.trim().split("\n");
    const r = await this.send("/execute", lines.map((sql) => ({ sql })), "NONE");
    const rs = Array.isArray(r) ? r : [r];
    let duration = 0;
    for (const [i, x] of rs.entries()) {
      if (!x.success) throw d1Error("D1_EXEC_ERROR", `Error in line ${i + 1}: ${lines[i]}: ${x.error || "Something went wrong"}`);
      duration += Number(x.meta?.duration ?? 0);
    }
    return { count: rs.length, duration };
  }
  withSession(constraintOrBookmark) {
    const t = constraintOrBookmark?.trim();
    return new D1Session(this.fetcher, t ? t : FIRST_UNCONSTRAINED);
  }
  async dump() {
    throw d1Error("D1_DUMP_ERROR", "dump() is not supported on tailworks; use `tw d1 export`");
  }
};
var D1Statement = class _D1Statement {
  statement;
  params;
  #s;
  constructor(s, statement, params) {
    this.#s = s;
    this.statement = statement;
    this.params = params;
  }
  bind(...values) {
    const out = values.map((v) => {
      const t = typeof v;
      if (t === "number" || t === "string") return v;
      if (t === "boolean") return v ? 1 : 0;
      if (t === "object") {
        if (v == null) return v;
        if (Array.isArray(v) && v.every((b) => typeof b === "number" && b >= 0 && b < 256)) return v;
        if (v instanceof ArrayBuffer) return Array.from(new Uint8Array(v));
        if (ArrayBuffer.isView(v)) return Array.from(v);
      }
      throw d1Error("D1_TYPE_ERROR", `Type '${t}' not supported for value '${String(v)}'`);
    });
    return new _D1Statement(this.#s, this.statement, out);
  }
  async #one(path, format) {
    return first(await this.#s.sendOrThrow(path, { sql: this.statement, params: this.params }, format));
  }
  async first(col) {
    const rows = toObjects(await this.#one("/query", "ROWS_AND_COLUMNS")).results;
    if (!rows.length) return null;
    if (col === void 0) return rows[0];
    if (rows[0][col] === void 0) throw d1Error("D1_COLUMN_NOTFOUND", `Column not found (${col})`);
    return rows[0][col];
  }
  async run() {
    return this.#one("/execute", "NONE");
  }
  async all() {
    return toObjects(await this.#one("/query", "ROWS_AND_COLUMNS"));
  }
  async raw(opts) {
    const r = await this.#one("/query", "ROWS_AND_COLUMNS");
    const res = r.results;
    if (!res) return [];
    if (Array.isArray(res)) {
      const out = [];
      for (const row of res) {
        if (opts?.columnNames && out.length === 0) out.push(Object.keys(row));
        out.push(Object.values(row));
      }
      return out;
    }
    return [...opts?.columnNames ? [res.columns] : [], ...res.rows];
  }
};
function makeD1(fetcher) {
  return new D1Database(fetcher);
}
var WRAPS = { d1: makeD1 };
function wrapStores(env) {
  const w = env.__TW_WRAP;
  if (!w || typeof w !== "object") return;
  delete env.__TW_WRAP;
  for (const [k, kind] of Object.entries(w)) {
    const f = WRAPS[kind];
    if (f && env[k]) env[k] = f(env[k]);
  }
}

// js/packages/tailnet/src/seal.ts
var CACHE_MAX = 1024;
var verified = /* @__PURE__ */ new Map();
function hasCaller(req) {
  const ts = req?.cf?.tailscale;
  return !!ts && typeof ts === "object" && ("caller" in ts || "seal" in ts);
}
async function unseal(tw, seal) {
  if (!seal || typeof seal !== "object" || typeof seal.mac !== "string" || typeof seal.exp !== "number") return null;
  const hit = verified.get(seal.mac);
  if (hit) {
    if (Date.now() <= hit.exp) return hit.caller;
    verified.delete(seal.mac);
  }
  if (!tw || typeof tw.call !== "function") return null;
  let r;
  try {
    r = await tw.call("unseal", { seal });
  } catch {
    return null;
  }
  const caller = r?.ok ? r.value?.caller ?? null : null;
  if (caller) {
    if (verified.size >= CACHE_MAX) verified.delete(verified.keys().next().value);
    verified.set(seal.mac, { caller, exp: seal.exp });
  }
  return caller;
}
async function sealedRequest(tw, req) {
  if (!hasCaller(req)) return req;
  const cf = req.cf;
  const caller = await unseal(tw, cf.tailscale.seal);
  const { caller: _c, seal, ...rest } = cf.tailscale;
  const out = { ...cf, tailscale: caller ? { ...rest, caller, seal } : rest };
  if (typeof Request === "function" && req instanceof Request) return new Request(req, { cf: out });
  return { ...req, cf: out };
}

// js/packages/tailnet/src/trace.ts
import { AsyncLocalStorage } from "node:async_hooks";
var MAX_SPANS = 64;
var TP_RE = /^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$/;
function parseTraceparent(v) {
  if (typeof v !== "string") return null;
  const m = TP_RE.exec(v.trim());
  if (!m || m[1] === "ff" || m[1] === "00" && m[5]) return null;
  if (/^0+$/.test(m[2]) || /^0+$/.test(m[3])) return null;
  return { traceId: m[2], spanId: m[3], flags: m[4] };
}
function randHex(bytes) {
  const b = new Uint8Array(bytes);
  for (; ; ) {
    crypto.getRandomValues(b);
    if (b.some((x) => x !== 0)) return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  }
}
function newSpanId() {
  return randHex(8);
}
var als = null;
try {
  als = new AsyncLocalStorage();
} catch {
  als = null;
}
function headerOf(req) {
  try {
    return req?.headers?.get?.("traceparent");
  } catch {
    return null;
  }
}
async function runTraced(req, fn, flush) {
  if (!als) return fn();
  const header = headerOf(req);
  const tp = parseTraceparent(header);
  if (!tp) return als.run(void 0, fn);
  const st = { ...tp, incoming: String(header), spans: [] };
  return als.run(st, async () => {
    try {
      return await fn();
    } finally {
      if (st.spans.length && flush) {
        const spans = st.spans.splice(0);
        try {
          flush(spans);
        } catch {
        }
      }
    }
  });
}
function withTraceparent(input, init, value, incoming) {
  let base = init?.headers;
  if (base === void 0 && typeof Request !== "undefined" && input instanceof Request) base = input.headers;
  let h;
  try {
    h = new Headers(base);
  } catch {
    return null;
  }
  const cur = h.get("traceparent");
  if (cur && cur !== incoming) return null;
  h.set("traceparent", value);
  return [input, { ...init ?? {}, headers: h }];
}
var installed = false;
function installFetchPropagation() {
  if (installed || !als || typeof globalThis.fetch !== "function") return;
  installed = true;
  const orig = globalThis.fetch;
  const traced = function fetch2(input, init) {
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
  }
}
async function tracedStubFetch(cls, name, args, call) {
  const st = als?.getStore();
  if (!st) return call(...args);
  const spanId = newSpanId();
  const a = withTraceparent(args[0], args[1], `00-${st.traceId}-${spanId}-${st.flags}`, st.incoming);
  if (!a) return call(...args);
  let method = "GET";
  try {
    method = String(a[1]?.method ?? args[0]?.method ?? "GET").toUpperCase();
  } catch {
  }
  const rec = {
    traceId: st.traceId,
    spanId,
    parentSpanId: st.spanId,
    name: `${cls} fetch`,
    start: Date.now(),
    end: 0,
    error: false,
    attrs: { "tw.do.class": cls, "tw.do.name": name, "http.request.method": method }
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
function startChild(name, attrs) {
  const st = als?.getStore();
  if (!st) return void 0;
  const rec = { traceId: st.traceId, spanId: newSpanId(), parentSpanId: st.spanId, name, start: Date.now(), end: 0, error: false, attrs };
  return {
    traceparent: `00-${st.traceId}-${rec.spanId}-${st.flags}`,
    finish(error) {
      rec.end = Date.now();
      rec.error = error;
      if (st.spans.length < MAX_SPANS) st.spans.push(rec);
    }
  };
}
async function runEntry(parent, name, attrs, fn, flush) {
  if (!als) return fn();
  const tp = parseTraceparent(parent);
  const st = { traceId: tp?.traceId ?? randHex(16), spanId: newSpanId(), flags: tp?.flags ?? "01", incoming: "", spans: [] };
  const rec = { traceId: st.traceId, spanId: st.spanId, parentSpanId: tp?.spanId ?? "", name, start: Date.now(), end: 0, error: false, attrs, kind: "server" };
  return als.run(st, async () => {
    try {
      return await fn();
    } catch (e) {
      rec.error = true;
      throw e;
    } finally {
      rec.end = Date.now();
      const spans = [rec, ...st.spans.splice(0, MAX_SPANS - 1)];
      if (flush) {
        try {
          flush(spans);
        } catch {
        }
      }
    }
  });
}

// js/packages/tailnet/src/rpctrace.ts
var MARK = "__tw_tp";
async function rpcCall(cls, name, method, args, call) {
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
function takeMark(args) {
  if (!args.length) return void 0;
  const m = args[args.length - 1];
  if (!m || typeof m !== "object") return void 0;
  const proto = Object.getPrototypeOf(m);
  if (proto !== Object.prototype && proto !== null) return void 0;
  const keys = Object.keys(m);
  if (keys.length !== 1 || keys[0] !== MARK || typeof m[MARK] !== "string") return void 0;
  args.pop();
  return m[MARK];
}
function tracedEntry(info, p, args, run) {
  if (typeof p !== "string") return run(args);
  if (p === info.alarm) {
    return runEntry(null, `${info.cls} alarm`, { "tw.do.class": info.cls, ...info.attrs(), "tw.trigger": "alarm" }, () => run(args), info.flush());
  }
  const tp = takeMark(args);
  if (tp === void 0) return run(args);
  return runEntry(tp, `${info.cls} ${p}`, { "tw.do.class": info.cls, ...info.attrs(), "tw.trigger": "rpc", "rpc.method": p }, () => run(args), info.flush());
}
function traceGate(obj, info) {
  const wrapped2 = /* @__PURE__ */ new Map();
  return new Proxy(obj, {
    get(t, p) {
      const v = Reflect.get(t, p, t);
      if (typeof v !== "function" || p === "constructor") return v;
      const c = wrapped2.get(p);
      if (c && c.v === v) return c.w;
      const w = function(...args) {
        return tracedEntry(info, p, args, (a) => v.apply(t, a));
      };
      wrapped2.set(p, { v, w });
      return w;
    },
    set(t, p, v) {
      return Reflect.set(t, p, v, t);
    }
  });
}

// js/packages/tailnet/src/facetns.ts
var BY_NAME = "tw objects must be addressed by name (getByName/idFromName)";
var FacetId = class _FacetId {
  name;
  constructor(name) {
    this.name = name;
  }
  // Not workerd's hex id (only the platform can derive that); unique per
  // class, as the name is.
  toString() {
    return this.name;
  }
  equals(other) {
    return other instanceof _FacetId && other.name === this.name;
  }
};
function facetNamespace(ns) {
  return Object.freeze({
    getByName(name) {
      return ns.getByName(String(name));
    },
    idFromName(name) {
      return new FacetId(String(name));
    },
    idFromString(_s) {
      throw new TailnetError("bad_request", `idFromString: ${BY_NAME}`);
    },
    newUniqueId(_opts) {
      throw new TailnetError("bad_request", `newUniqueId: ${BY_NAME}; newUniqueId() is not supported yet`);
    },
    get(id, _opts) {
      const n = id?.name;
      if (!(id instanceof FacetId) || typeof n !== "string" || !n) throw new TailnetError("bad_request", `get: ${BY_NAME}`);
      return ns.getByName(n);
    },
    jurisdiction(_j) {
      throw new Error("Jurisdiction restrictions are not implemented in workerd.");
    }
  });
}
function facetNamespaces(env, bindings) {
  if (!env || typeof env !== "object") return env;
  const out = { ...env };
  for (const k of Object.keys(bindings)) if (env[k]) out[k] = facetNamespace(env[k]);
  return out;
}

// js/packages/tailnet/src/runtime.ts
installFetchPropagation();
function spanFlusher(tw, ctx, as) {
  return (spans) => {
    if (!tw || typeof tw.call !== "function") return;
    const p = Promise.resolve().then(() => tw.call("spans", as ? { spans, as } : { spans })).catch(() => {
    });
    try {
      ctx?.waitUntil?.(p);
    } catch {
    }
  };
}
var twOf = /* @__PURE__ */ new WeakMap();
function entryInfo(obj, cls, plain) {
  const ctx = () => obj.ctx;
  const oname = () => {
    const n = ctx()?.id?.name;
    return typeof n === "string" && n ? n : void 0;
  };
  return {
    cls,
    alarm: plain ? "alarm" : "__tw_alarm",
    attrs() {
      const a = {};
      const n = oname();
      if (n) a["tw.do.name"] = n;
      if (plain) {
        try {
          const id = ctx()?.id?.toString?.();
          if (typeof id === "string" && id) a["tw.do.id"] = id;
        } catch {
        }
      }
      return a;
    },
    flush() {
      const n = oname();
      return spanFlusher(twOf.get(obj), ctx(), plain && n ? { class: cls, name: n } : void 0);
    }
  };
}
function createTailnetContext(tw, env, bindings, self, ticket) {
  if (!tw || typeof tw.call !== "function") throw new TailnetError("no_identity", "tw platform binding missing (not running on tailworks?)");
  const op = async (name, args = {}) => {
    const a = self ? { ...args, as: self } : ticket ? { ...args, ticket } : args;
    const r = await tw.call(name, a);
    if (!r || !r.ok) throw TailnetError.from(r?.error);
    return r.value;
  };
  const classOf = (ns) => {
    for (const [k, cls] of Object.entries(bindings)) if (env[k] === ns) return cls;
    if (typeof ns === "string" && Object.values(bindings).includes(ns)) return ns;
    throw new TailnetError("bad_request", "spawn: the namespace is not a durable_objects binding of this app");
  };
  const tfetch = async (input, init) => {
    let r;
    try {
      r = await fetch(input, init);
    } catch (e) {
      throw new TailnetError("dial_failed", String(e?.message ?? e));
    }
    const err2 = await fromResponse(r);
    if (err2) throw err2;
    return r;
  };
  const tn = {
    fetch: tfetch,
    // forward is no longer part of TailnetContext: signed on-behalf-of
    // assertions (RFC §4.6) are not built, so it was only fetch with the
    // request's headers copied. Kept, unlisted, for one minor release so
    // deployed apps that call it keep working; use
    // ctx.tailnet.fetch(new Request(url, request)) instead.
    forward(request, to, init) {
      const hasBody = request.method !== "GET" && request.method !== "HEAD";
      return tfetch(new Request(String(to), { method: request.method, headers: request.headers, body: hasBody ? request.body : void 0, ...init }));
    },
    async spawn(ns, opts) {
      if (!opts || typeof opts.name !== "string" || !opts.name) throw new TailnetError("bad_request", "spawn: opts.name is required");
      const cls = classOf(ns);
      const v = await op("spawn", { class: cls, name: opts.name, caps: opts.caps, ttl: opts.ttl, principal: opts.principal });
      let state = v.status === "ready" ? "ready" : "pending";
      const stub = await ns.getByName(opts.name);
      const sp = {
        stub,
        name: v.hostname ?? opts.name,
        objectId: v.objectID,
        state: () => state,
        ready: Promise.resolve()
      };
      sp.ready = op("ready", { class: cls, name: opts.name, timeoutMs: opts.readyTimeoutMs ?? 3e4 }).then(
        (r) => {
          state = "ready";
          if (r?.magicDNS) sp.name = r.magicDNS;
        },
        (e) => {
          state = "failed";
          throw e;
        }
      );
      sp.ready.catch(() => {
      });
      return sp;
    },
    kill(opts = {}) {
      return op("kill", { reason: opts.reason ?? "", cascade: !!opts.cascade, target: opts.target });
    },
    async audit(records) {
      await op("audit", { records });
    },
    self() {
      return op("self", {});
    },
    connect(address, options = {}) {
      return tailnetSocket(tw, socketAddress(address), options, self);
    }
  };
  return Object.freeze(tn);
}
function socketAddress(address) {
  if (typeof address === "string") return address;
  if (!address || typeof address.hostname !== "string" || !Number.isInteger(address.port)) {
    throw new TailnetError("bad_request", 'connect: address must be "host:port" or { hostname, port }');
  }
  const h = address.hostname.includes(":") && !address.hostname.startsWith("[") ? "[" + address.hostname + "]" : address.hostname;
  return h + ":" + address.port;
}
function tailnetSocket(tw, address, options, self) {
  if (options.secureTransport && options.secureTransport !== "off") {
    throw new TailnetError("not_supported", 'ctx.tailnet.connect: secureTransport "' + String(options.secureTransport) + '" is not supported; use { tls: "netd" }');
  }
  if (options.tls !== void 0 && options.tls !== "netd") throw new TailnetError("bad_request", 'ctx.tailnet.connect: tls must be "netd"');
  if (typeof tw.tcpConnect !== "function") throw new TailnetError("not_supported", "ctx.tailnet.connect() needs a newer tailworks host");
  const toUp = new TransformStream();
  const fromUp = new TransformStream();
  let closeUp = async () => {
  };
  const opened = (async () => {
    const args = { address };
    if (options.tls) args.tls = options.tls;
    if (self) args.as = self;
    const r = await tw.tcpConnect(args);
    if (!r || !r.ok) throw TailnetError.from(r?.error);
    return r.value;
  })();
  const closed = opened.then(
    async (up) => {
      const a = new AbortController();
      closeUp = async () => a.abort();
      await Promise.allSettled([
        toUp.readable.pipeTo(up.writable, { signal: a.signal }),
        up.readable.pipeTo(fromUp.writable, { signal: a.signal })
      ]);
    },
    async (e) => {
      await Promise.allSettled([toUp.readable.cancel(e), fromUp.writable.abort(e)]);
      throw e;
    }
  );
  closed.catch(() => {
  });
  return {
    readable: fromUp.readable,
    writable: toUp.writable,
    opened: opened.then(() => ({ remoteAddress: address })),
    closed,
    async close() {
      await closeUp();
      await Promise.allSettled([toUp.writable.abort(), fromUp.readable.cancel()]);
    }
  };
}
function define(ctx, tn) {
  try {
    Object.defineProperty(ctx, "tailnet", { value: tn, enumerable: false, configurable: true });
  } catch {
  }
}
function installAlarmShim(ctx, tw) {
  const st = ctx?.storage;
  if (!st || !tw) return;
  const call = async (action, time) => {
    const r = await tw.call("alarm", { action, time });
    if (!r || !r.ok) throw TailnetError.from(r?.error);
    return r.value;
  };
  const shim = {
    async setAlarm(t) {
      const ms = typeof t === "number" ? t : t instanceof Date ? t.getTime() : Number(t);
      if (!Number.isFinite(ms)) throw new TypeError("setAlarm: invalid time");
      await call("set", ms);
    },
    async getAlarm() {
      return await call("get") ?? null;
    },
    async deleteAlarm() {
      await call("delete");
    }
  };
  for (const [k, fn] of Object.entries(shim)) {
    try {
      Object.defineProperty(st, k, { value: fn, configurable: true, writable: true });
    } catch {
    }
  }
}
function wrapFacetClass(C, name, bindings, opts = {}) {
  if (typeof C !== "function") throw new Error(`tw: durable_objects class ${name} is not exported by the main module`);
  const W = {
    [name]: class extends C {
      constructor(ctx, env) {
        const tw = env?.__TW;
        const e = guardEnv(facetNamespaces(env, bindings), bindings, "tailnet", admitter(tw, {}));
        for (const k of Object.keys(opts.workflows ?? {})) if (e?.[k]) e[k] = workflowClient(e[k]);
        define(ctx, createTailnetContext(tw, e, bindings));
        installAlarmShim(ctx, tw);
        if (tw && opts.container) defineContainer(ctx, createContainer(tw, ctx, name, opts.container));
        super(ctx, e);
        twOf.set(this, tw);
        return traceGate(this, entryInfo(this, name, false));
      }
      // Called by the Host DO when the delegated alarm fires.
      async __tw_alarm(info) {
        const self = this;
        if (typeof self.alarm === "function") return self.alarm(info);
      }
      // Called by the Host DO with a request the container sent to an
      // intercepted destination (hostd's sandbox egress proxy).
      async __tw_intercept(key, req) {
        return dispatchIntercept(this.ctx, key, req);
      }
    }
  }[name];
  const ff = C.prototype?.fetch;
  if (typeof ff === "function") {
    W.prototype.fetch = function(req) {
      return runTraced(req, () => ff.call(this, req), spanFlusher(twOf.get(this), this.ctx));
    };
  }
  if (opts.container) noPreviewURLs(W);
  return W;
}
var PREVIEW_HEADER = "x-sandbox-preview-proxy";
function noPreviewURLs(W) {
  const proto = Object.getPrototypeOf(W.prototype);
  if (typeof proto?.exposePort === "function") {
    W.prototype.exposePort = async function() {
      throw notSupportedPreview();
    };
  }
  const f = W.prototype.fetch;
  if (typeof f === "function") {
    W.prototype.fetch = async function(req) {
      if (req?.headers?.get?.(PREVIEW_HEADER)) {
        const e = notSupportedPreview();
        return new Response(JSON.stringify({ code: "not_supported", message: e.message }), {
          status: 501,
          headers: { "content-type": "application/json", "tw-error": "not_supported" }
        });
      }
      return f.call(this, req);
    };
  }
}
function killedMark(ctx) {
  try {
    return ctx?.storage?.kv?.get?.("__tw_killed") ?? void 0;
  } catch {
    return void 0;
  }
}
var KILL_SELF_GATED = /* @__PURE__ */ new Set(["constructor", "fetch", "__tw_kill", "__tw_intercept"]);
var KILL_NOOP = /* @__PURE__ */ new Set(["alarm", "webSocketMessage", "webSocketClose", "webSocketError"]);
function findDescriptor(o, p) {
  for (let x = o; x && x !== Object.prototype; x = Object.getPrototypeOf(x)) {
    const d = Object.getOwnPropertyDescriptor(x, p);
    if (d) return d;
  }
  return void 0;
}
function killGate(obj, cls, info) {
  const killedErr = (k) => new Error("[killed] object " + cls + "/" + (obj.ctx?.id?.name ?? "") + " was killed: " + k);
  const wrapped2 = /* @__PURE__ */ new Map();
  return new Proxy(obj, {
    get(t, p) {
      if (typeof p === "string" && !KILL_SELF_GATED.has(p)) {
        const k = killedMark(t.ctx);
        if (k && findDescriptor(t, p)?.get) throw killedErr(k);
      }
      const v = Reflect.get(t, p, t);
      if (typeof v !== "function" || p === "constructor") return v;
      const c = wrapped2.get(p);
      if (c && c.v === v) return c.w;
      const gate = typeof p === "string" && !KILL_SELF_GATED.has(p);
      const w = function(...args) {
        if (gate) {
          const k = killedMark(t.ctx);
          if (k) {
            if (KILL_NOOP.has(p)) return Promise.resolve(void 0);
            throw killedErr(k);
          }
        }
        return tracedEntry(info, p, args, (a) => v.apply(t, a));
      };
      wrapped2.set(p, { v, w });
      return w;
    },
    set(t, p, v) {
      return Reflect.set(t, p, v, t);
    }
  });
}
function wrapPlainClass(C, name, bindings, opts) {
  if (typeof C !== "function") throw new Error(`tw: durable_objects class ${name} is not exported by the main module`);
  const proto = C.prototype;
  const killedResponse = (reason) => new TailnetError("killed", `object ${name} was killed: ${reason}`).toResponse();
  const W = {
    [name]: class extends C {
      constructor(ctx, env) {
        const tw = env?.__TW;
        const oname = ctx?.id?.name;
        const self = oname ? { class: name, name: oname } : void 0;
        const e = guardEnv(env, bindings, "tailnet", admitter(tw, self ? { as: self } : {}));
        if (tw && oname) define(ctx, createTailnetContext(tw, e, bindings, self));
        if (tw && oname && opts.container) defineContainer(ctx, createContainer(tw, ctx, name, opts.container, { class: name, name: oname }));
        super(ctx, e);
        twOf.set(this, tw);
        const gated = killGate(this, name, entryInfo(this, name, true));
        if (tw && oname && opts.node && !killedMark(ctx)) {
          const p = tw.call("ensure", { as: { class: name, name: oname } }).then((r) => {
            if (r && !r.ok) {
              const err2 = TailnetError.from(r.error);
              if (err2.code === "create_not_admitted") this.__tw_denied = err2;
            }
          }, () => {
          });
          this.__tw_ensure = p;
          try {
            ctx.waitUntil?.(p);
          } catch {
          }
        }
        return gated;
      }
      async __tw_kill(reason) {
        this.ctx.storage.kv.put("__tw_killed", reason || "killed");
        const oname = this.ctx?.id?.name;
        if (oname) forgetAdmitted(name, oname);
        return true;
      }
      async __tw_intercept(key, req) {
        const self = this;
        const k = self.ctx.storage.kv.get("__tw_killed");
        if (k) return killedResponse(k);
        return dispatchIntercept(self.ctx, key, req);
      }
    }
  }[name];
  if (typeof proto.fetch === "function") {
    W.prototype.fetch = async function(req) {
      const k = this.ctx.storage.kv.get("__tw_killed");
      if (k) return killedResponse(k);
      req = await sealedRequest(twOf.get(this), req);
      if (this.__tw_ensure) await this.__tw_ensure;
      if (this.__tw_denied) return this.__tw_denied.toResponse();
      return runTraced(req, () => proto.fetch.call(this, req), spanFlusher(twOf.get(this), this.ctx));
    };
  }
  if (opts.container) noPreviewURLs(W);
  return W;
}
function isPublicReq(req) {
  return req?.cf?.tailscale?.requester?.kind === "public";
}
function admitter(tw, extra) {
  return async (cls, name) => {
    if (!tw || typeof tw.call !== "function") return { ok: false, error: { code: "create_not_admitted", message: "tw platform binding missing; refusing to reach a Durable Object" } };
    return tw.call("admit", { class: cls, name, ...extra });
  };
}
var ADMIT_CACHE_MAX = 1e4;
var ADMIT_CACHE_TTL_MS = 6e4;
var admitted = /* @__PURE__ */ new Map();
function remember(key) {
  admitted.delete(key);
  if (admitted.size >= ADMIT_CACHE_MAX) {
    const first2 = admitted.keys().next().value;
    if (first2 !== void 0) admitted.delete(first2);
  }
  admitted.set(key, Date.now() + ADMIT_CACHE_TTL_MS);
}
function forgetAdmitted(cls, name) {
  admitted.delete(cls + "\n" + name);
}
async function admit(adm, cls, name) {
  const key = cls + "\n" + name;
  const exp = admitted.get(key);
  if (exp !== void 0) {
    if (Date.now() < exp) return;
    admitted.delete(key);
  }
  const r = await adm(cls, name);
  if (!r || !r.ok) throw TailnetError.from(r?.error ?? { code: "create_not_admitted", message: "admit refused" });
  if (r.value?.existing === true) remember(key);
}
function denied(msg) {
  throw new TailnetError("public_create_denied", msg);
}
function guardStub(adm, cls, name, real) {
  return new Proxy(
    {},
    {
      get(_t, p) {
        if (p === "then") return void 0;
        if (p === "id" || p === "name") return real[p];
        if (typeof p === "symbol") return real[p];
        return async (...args) => {
          await admit(adm, cls, name);
          const target = real && typeof real.then === "function" ? await real : real;
          if (p === "fetch") return tracedStubFetch(cls, name, args, (...a) => target.fetch(...a));
          return rpcCall(cls, name, String(p), args, (...a) => target[p](...a));
        };
      }
    }
  );
}
function guardNamespace(adm, cls, ns, mode) {
  return Object.freeze({
    getByName(name) {
      const n = String(name);
      return guardStub(adm, cls, n, ns.getByName(n));
    },
    idFromName(name) {
      return ns.idFromName(name);
    },
    idFromString(s) {
      return ns.idFromString(s);
    },
    get(id, opts) {
      const n = id?.name;
      if (typeof n !== "string" || !n) {
        if (mode === "public") denied(`a public request may only address ${cls} objects by name (idFromName/getByName)`);
        return ns.get(id, opts);
      }
      return guardStub(adm, cls, n, ns.get(id, opts));
    },
    newUniqueId(opts) {
      if (mode === "public") denied(`a public request may not create a new ${cls} object (newUniqueId)`);
      return ns.newUniqueId(opts);
    },
    jurisdiction(j) {
      return guardNamespace(adm, cls, ns.jurisdiction(j), mode);
    }
  });
}
function guardEnv(env, bindings, mode, adm) {
  if (!env || typeof env !== "object") return env;
  const out = { ...env };
  delete out.__TW;
  wrapStores(out);
  for (const [k, cls] of Object.entries(bindings)) {
    if (env[k]) out[k] = guardNamespace(adm, cls, env[k], mode);
  }
  return out;
}
function guardTailnetEnv(env, bindings) {
  return guardEnv(env, bindings, "tailnet", admitter(env?.__TW, { workflow: true }));
}
function publicEnv(env, bindings) {
  return guardEnv(env, bindings, "public", admitter(env?.__TW, {}));
}
function publicTailnet(tn) {
  return Object.freeze({
    ...tn,
    spawn() {
      return Promise.reject(new TailnetError("public_create_denied", "a public request may not spawn objects"));
    },
    kill() {
      return Promise.reject(new TailnetError("public_request", "a public request may not kill objects"));
    }
  });
}
function asResponse(e) {
  const m = String(e?.message ?? e);
  const i = m.indexOf("[killed]");
  if (i >= 0) return new TailnetError("killed", m.slice(i + 8).trim()).toResponse();
  const j = m.indexOf("[create_not_admitted]");
  if (j >= 0) return new TailnetError("create_not_admitted", m.slice(j + 21).trim()).toResponse();
  if (isTailnetError(e) && (e.code === "public_create_denied" || e.code === "public_request" || e.code === "create_not_admitted" || e.code === "killed")) return e.toResponse();
  return null;
}
function requestTicket(req) {
  const t = req?.cf?.tw?.ticket;
  return typeof t === "string" && t ? t : void 0;
}
function isScheduledReq(req) {
  return req?.cf?.tw?.kind === "scheduled";
}
function scheduledCtx(ctx) {
  const pending = [];
  const drain = async () => {
    while (pending.length) await Promise.allSettled(pending.splice(0));
  };
  if (!ctx || typeof ctx !== "object") return { ctx, drain };
  const waitUntil = (p) => {
    pending.push(Promise.resolve(p));
  };
  const proxy = new Proxy(ctx, {
    get(t, k) {
      if (k === "waitUntil") return waitUntil;
      const v = Reflect.get(t, k, t);
      return typeof v === "function" ? v.bind(t) : v;
    }
  });
  return { ctx: proxy, drain };
}
async function runScheduled(scheduledFn, req, extra, drain) {
  const t = req.cf.tw;
  let noRetry = false;
  const controller = {
    cron: String(t.cron ?? ""),
    scheduledTime: Number(t.scheduledTime),
    type: "scheduled",
    noRetry() {
      noRetry = true;
    }
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
    return Response.json({ outcome: "exception", noRetry, error: String(e?.message ?? e).slice(0, 512) });
  }
}
var QUEUE_TICKET_ID = "__tw_ticket";
function queueBatch(batch) {
  const msgs = Array.isArray(batch?.messages) ? batch.messages : [];
  const t = msgs.find((m) => m?.id === QUEUE_TICKET_ID);
  if (!t) return { batch };
  const rest = Object.freeze(msgs.filter((m) => m?.id !== QUEUE_TICKET_ID));
  const b = Object.freeze({
    queue: batch.queue,
    messages: rest,
    ackAll: () => batch.ackAll(),
    retryAll: (o) => batch.retryAll(o)
  });
  return { batch: b, ticket: typeof t.body === "string" && t.body ? t.body : void 0 };
}
var HANDLER_METHODS = ["fetch", "scheduled", "queue", "tail", "tailStream", "trace", "email", "test", "connect"];
function handlerKeys(H) {
  const keys = new Set(Object.keys(H));
  for (const k of HANDLER_METHODS) if (typeof H[k] === "function") keys.add(k);
  return [...keys];
}
function wrapHandler(H, bindings) {
  if (!H) {
    return {
      fetch: () => new TailnetError("unknown_object", "this app has no default export (Worker); address objects via their own nodes or /_tw/o/<Class>/<name>/").toResponse()
    };
  }
  if (typeof H === "function") {
    const C = class extends H {
      constructor(ctx, env) {
        const tw = env?.__TW;
        const e = guardEnv(env, bindings, "tailnet", admitter(tw, {}));
        define(ctx, createTailnetContext(tw, e, bindings));
        super(ctx, e);
        this.__tw = tw;
      }
    };
    if (typeof H.prototype?.fetch === "function") {
      const f = H.prototype.fetch;
      C.prototype.fetch = async function(req) {
        const tw = this.__tw;
        req = await sealedRequest(tw, req);
        if (isPublicReq(req)) {
          this.env = guardEnv(this.env, bindings, "public", admitter(tw, {}));
          if (tw) define(this.ctx, publicTailnet(createTailnetContext(tw, this.env, bindings)));
        } else {
          const ticket = requestTicket(req);
          this.env = guardEnv(this.env, bindings, "tailnet", admitter(tw, { ticket }));
          if (tw) define(this.ctx, createTailnetContext(tw, this.env, bindings, void 0, ticket));
          if (isScheduledReq(req)) {
            const sc = scheduledCtx(this.ctx);
            const flush = spanFlusher(tw, this.ctx);
            this.ctx = sc.ctx;
            const sf = typeof this.scheduled === "function" ? this.scheduled.bind(this) : void 0;
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
      C.prototype.queue = async function(batch) {
        const tw = this.__tw;
        const q = queueBatch(batch);
        this.env = guardEnv(this.env, bindings, "tailnet", admitter(tw, { ticket: q.ticket }));
        if (tw) define(this.ctx, createTailnetContext(tw, this.env, bindings, void 0, q.ticket));
        return qf.call(this, q.batch);
      };
    }
    return { [H.name || "Default"]: C }[H.name || "Default"];
  }
  const out = { ...H };
  for (const k of handlerKeys(H)) {
    if (typeof H[k] !== "function") continue;
    const inner = function(a, env, ctx) {
      const tw = env?.__TW;
      const pub = k === "fetch" && isPublicReq(a);
      if (pub) {
        env = guardEnv(env, bindings, "public", admitter(tw, {}));
        if (ctx && tw) define(ctx, publicTailnet(createTailnetContext(tw, env, bindings)));
      } else {
        let ticket;
        if (k === "fetch") ticket = requestTicket(a);
        else if (k === "queue") ({ batch: a, ticket } = queueBatch(a));
        env = guardEnv(env, bindings, "tailnet", admitter(tw, { ticket }));
        if (ctx && tw) define(ctx, createTailnetContext(tw, env, bindings, void 0, ticket));
        if (k === "fetch" && isScheduledReq(a)) {
          const sc = scheduledCtx(ctx);
          return runTraced(a, () => runScheduled(H.scheduled, a, [env, sc.ctx], sc.drain), spanFlusher(tw, ctx));
        }
      }
      let r;
      try {
        r = k === "fetch" ? runTraced(a, () => H[k].call(H, a, env, ctx), spanFlusher(tw, ctx)) : H[k].call(H, a, env, ctx);
      } catch (e) {
        const resp = k === "fetch" ? asResponse(e) : null;
        if (resp) return resp;
        throw e;
      }
      if (k !== "fetch") return r;
      return Promise.resolve(r).catch((e) => {
        const resp = asResponse(e);
        if (resp) return resp;
        throw e;
      });
    };
    out[k] = k !== "fetch" ? inner : function(a, env, ctx) {
      return sealedRequest(env?.__TW, a).then((r) => inner.call(this, r, env, ctx));
    };
  }
  return out;
}
var wrappedEntrypoints = /* @__PURE__ */ new WeakMap();
function callCtx(ctx, cf) {
  if (!ctx || typeof ctx !== "object") return ctx;
  return new Proxy(ctx, {
    get(t, k) {
      if (k === "cf") return cf;
      const v = Reflect.get(t, k, t);
      return typeof v === "function" ? v.bind(t) : v;
    }
  });
}
var PY_RPC = /* @__PURE__ */ Symbol.for("tailworks.pyRpc");
function pyCallCf(scf) {
  const { tw: _tw, ...rest } = scf ?? {};
  return rest;
}
function bindingDispatcher(T, bindings) {
  const target = (ep) => {
    const H = ep ? T[ep] : T.default;
    if (!H || typeof H !== "function" && typeof H !== "object") {
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
      const ep = String(req?.cf?.tw?.binding?.entrypoint ?? "");
      if (ep && typeof T.default?.[PY_RPC] === "function" && !(ep in T)) {
        return new TailnetError("service_not_found", `entrypoint ${ep} of a Python Worker answers RPC calls, not fetch(); bind the default entrypoint for fetch()`).toResponse();
      }
      let t;
      try {
        t = target(ep);
      } catch (e) {
        return e.toResponse();
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
      const tw = env?.__TW;
      const sealed = await sealedRequest(tw, { cf });
      const scf = sealed?.cf ?? {};
      const py = T.default?.[PY_RPC];
      if (typeof py === "function") {
        if (typeof method !== "string" || method in Object.prototype || HANDLER_METHODS.includes(method) || method === "constructor") {
          throw new TypeError(`The RPC receiver does not implement the method "${method}".`);
        }
        const ticket2 = typeof scf?.tw?.ticket === "string" && scf.tw.ticket ? scf.tw.ticket : void 0;
        const genv = guardEnv(env, bindings, "tailnet", admitter(tw, { ticket: ticket2 }));
        return await py(genv, ep || "", method, args, pyCallCf(scf));
      }
      const t = target(ep);
      if (typeof t.H !== "function") throw new TypeError(`entrypoint ${t.name} is not a WorkerEntrypoint class; RPC needs one`);
      const cctx = callCtx(ctx, scf);
      const inst = new t.W(cctx, env);
      const ticket = typeof scf?.tw?.ticket === "string" && scf.tw.ticket ? scf.tw.ticket : void 0;
      if (ticket) {
        inst.env = guardEnv(env, bindings, "tailnet", admitter(tw, { ticket }));
        if (tw) define(cctx, createTailnetContext(tw, inst.env, bindings, void 0, ticket));
      }
      const f = typeof method === "string" && !(method in Object.prototype) ? inst[method] : void 0;
      if (typeof f !== "function" || HANDLER_METHODS.includes(method) || method === "constructor") {
        throw new TypeError(`The RPC receiver does not implement the method "${method}".`);
      }
      return await f.apply(inst, args);
    }
  };
}
export {
  bindingDispatcher,
  createTailnetContext,
  guardTailnetEnv,
  isApprovalType,
  publicEnv,
  socketAddress,
  wrapFacetClass,
  wrapHandler,
  wrapPlainClass,
  wrapWorkflowClass
};
