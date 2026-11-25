/// <reference types="node" />
// @tailworks/tailnet/testing/dev: black-box tests against a real local tailworks.
// startDev() runs `tw dev <dir> --port 0 --json` as a child process (the
// same tw-hostd, netd and workerd as production, on an embedded control
// plane), waits for its `ready` event, and gives you one local URL per
// identity. Every request through a caller's URL arrives over that caller's
// own dev tailnet node, so getCaller() in the app sees a real, netd-verified
// caller: nothing is forged. stop() sends SIGTERM and waits for tw dev to
// exit (it reaps its own workerd); docs/testing.md has the details.
//
// Node only (it spawns a process). For unit tests inside workerd, use
// @tailworks/tailnet/testing.

import { spawn, type ChildProcess } from "node:child_process";
import { createInterface } from "node:readline";

/** An extra identity for startDev: a dev node, untagged (a user) or with tags. */
export interface DevCallerSpec {
  /** Tags for the caller's node (tag:<name>). Without tags it is a user's node. */
  tags?: string[];
}

export interface StartDevOptions {
  /** The app directory (default: the current directory). */
  dir?: string;
  /** Extra callers by name (a-z, 0-9, -). Each gets its own dev node and local URL. */
  callers?: Record<string, DevCallerSpec> | string[];
  /** The tw binary (default: $TW_BIN, else `tw` on PATH). */
  tw?: string;
  /** More `tw dev` flags, e.g. ["--env", "staging"]. */
  args?: string[];
  /** Environment for tw dev (default: this process's). */
  env?: NodeJS.ProcessEnv;
  /** How long to wait for `ready` (default 120 s: the first run builds and starts workerd). */
  readyTimeoutMs?: number;
  /** How long stop() waits after SIGTERM before SIGKILL (default 15 s). */
  stopTimeoutMs?: number;
  /** Receives tw dev's stderr lines (default: kept, and shown if start fails). */
  onLog?: (line: string) => void;
}

/** One identity the app can be called as. */
export interface DevCaller {
  name: string;
  /** Local URL; requests to it reach the app from this caller's node. */
  url: string;
  /** The user's login (untagged callers). */
  login?: string;
  tags: string[];
  /** The caller's node ID, as getCaller(req).nodeId will report it. */
  nodeId?: string;
  /** The caller's MagicDNS name. */
  node?: string;
  /** fetch() a path (or URL) of the app as this caller. */
  fetch(path: string | URL, init?: RequestInit): Promise<Response>;
}

export interface DevApp {
  /** The default caller's URL (`tw dev`'s own client node, "you"). */
  url: string;
  /** url plus the MCP route, for an app that mounts an MCP server. */
  serves: string;
  workload: string;
  /** The workload's node hostname. */
  node: string;
  /** Every caller, by name; "you" is the default one. */
  callers: Record<string, DevCaller>;
  /** as returns a caller by name (throws if there is none). */
  as(name: string): DevCaller;
  /** fetch as the default caller. */
  fetch(path: string | URL, init?: RequestInit): Promise<Response>;
  /** The raw `ready` event. */
  ready: Record<string, any>;
  /** stop sends SIGTERM to tw dev and waits for it to exit. Safe to call twice. */
  stop(): Promise<void>;
  [Symbol.asyncDispose](): Promise<void>;
}

const NAME = /^[a-z0-9][a-z0-9-]{0,39}$/;

function callerArgs(callers: StartDevOptions["callers"]): string[] {
  const list: Array<[string, DevCallerSpec]> = Array.isArray(callers) ? callers.map((n) => [n, {}]) : Object.entries(callers ?? {});
  const out: string[] = [];
  for (const [name, spec] of list) {
    if (!NAME.test(name) || name === "you") throw new Error(`startDev: caller name ${JSON.stringify(name)}: want a-z, 0-9 and -, not "you"`);
    const tags = spec?.tags ?? [];
    out.push("--caller", tags.length ? `${name}=${tags.join(",")}` : name);
  }
  return out;
}

function makeCaller(c: { name: string; url: string; as?: string; login?: string; tags?: string[]; nodeId?: string; node?: string }): DevCaller {
  return {
    name: c.name,
    url: c.url,
    login: c.login ?? c.as ?? undefined,
    tags: c.tags ?? [],
    nodeId: c.nodeId,
    node: c.node,
    fetch: (path, init) => fetch(new URL(String(path), c.url), init),
  };
}

// Children still running when this process exits are killed, so a crashed
// test run doesn't leave tw dev (and its workerd) behind.
const live = new Set<ChildProcess>();
let exitHook = false;
function track(p: ChildProcess) {
  live.add(p);
  p.once("exit", () => live.delete(p));
  if (!exitHook) {
    exitHook = true;
    process.once("exit", () => {
      for (const c of live) c.kill("SIGTERM");
    });
  }
}

function waitExit(p: ChildProcess, ms: number): Promise<boolean> {
  if (p.exitCode !== null || p.signalCode !== null) return Promise.resolve(true);
  return new Promise((resolve) => {
    const t = setTimeout(() => resolve(false), ms);
    p.once("exit", () => {
      clearTimeout(t);
      resolve(true);
    });
  });
}

/** startDev starts the app under `tw dev` and resolves once it is ready. */
export async function startDev(opts: StartDevOptions = {}): Promise<DevApp> {
  const bin = opts.tw ?? process.env.TW_BIN ?? "tw";
  const args = ["dev", opts.dir ?? ".", "--port", "0", "--json", ...callerArgs(opts.callers), ...(opts.args ?? [])];
  const child = spawn(bin, args, { env: opts.env ?? process.env, stdio: ["ignore", "pipe", "pipe"] });
  track(child);
  const logs: string[] = [];
  createInterface({ input: child.stderr! }).on("line", (l) => {
    logs.push(l);
    if (logs.length > 200) logs.shift();
    opts.onLog?.(l);
  });

  let stopped: Promise<void> | undefined;
  const stop = () =>
    (stopped ??= (async () => {
      if (child.exitCode !== null || child.signalCode !== null) return;
      child.kill("SIGTERM");
      if (!(await waitExit(child, opts.stopTimeoutMs ?? 15000))) {
        child.kill("SIGKILL");
        await waitExit(child, 5000);
      }
    })());

  const ready = await new Promise<Record<string, any>>((resolve, reject) => {
    const fail = (why: string) => {
      stop();
      reject(new Error(`startDev: ${why}\n  ${bin} ${args.join(" ")}\n${logs.slice(-20).join("\n")}`));
    };
    const t = setTimeout(() => fail(`no ready event in ${opts.readyTimeoutMs ?? 120000} ms`), opts.readyTimeoutMs ?? 120000);
    child.once("error", (e) => {
      clearTimeout(t);
      fail(`can't run tw (${e.message}); install it, or set TW_BIN`);
    });
    child.once("exit", (code, sig) => {
      clearTimeout(t);
      fail(`tw dev exited (${sig ?? code}) before it was ready`);
    });
    createInterface({ input: child.stdout! }).on("line", (l) => {
      let ev: any;
      try {
        ev = JSON.parse(l);
      } catch {
        return;
      }
      if (ev?.event === "ready") {
        clearTimeout(t);
        child.removeAllListeners("exit");
        resolve(ev);
      } else if (ev?.error?.code || ev?.code) {
        // an error document (tw dev failed before ready), or an error event
        const e = ev.error ?? ev;
        logs.push(`${e.code}: ${e.message ?? ""}${(e.detail ?? []).length ? "\n  " + [].concat(e.detail).join("\n  ") : ""}`);
      }
    });
  });

  const callers: Record<string, DevCaller> = {
    you: makeCaller({ name: "you", url: ready.url, login: ready.as, tags: [] }),
  };
  for (const c of ready.callers ?? []) callers[c.name] = makeCaller(c);
  const app: DevApp = {
    url: ready.url,
    serves: ready.serves ?? ready.url,
    workload: ready.workload,
    node: ready.node,
    callers,
    ready,
    as(name: string) {
      const c = callers[name];
      if (!c) throw new Error(`startDev: no caller ${JSON.stringify(name)} (have: ${Object.keys(callers).join(", ")})`);
      return c;
    },
    fetch: (path, init) => callers.you.fetch(path, init),
    stop,
    [Symbol.asyncDispose]: stop,
  };
  return app;
}
