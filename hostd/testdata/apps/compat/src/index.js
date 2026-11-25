// Test fixture for docs/COMPAT.md runtime rows (hostd/gen/compat_test.go):
// each route exercises one Workers surface and reports what happened, so the
// Go test can assert the measured behaviour in both DO modes.
import { DurableObject } from "cloudflare:workers";
import { connect } from "cloudflare:sockets";
import { Buffer } from "node:buffer";
import { createHash, randomUUID } from "node:crypto";
import { AsyncLocalStorage } from "node:async_hooks";
import { EventEmitter } from "node:events";

async function nodeCompat() {
  const als = new AsyncLocalStorage();
  const seen = await als.run("ctx-1", async () => {
    await new Promise((r) => setTimeout(r, 1));
    return als.getStore();
  });
  let emitted = null;
  const ee = new EventEmitter();
  ee.on("x", (v) => (emitted = v));
  ee.emit("x", 42);
  return {
    base64: Buffer.from("tw").toString("base64"),
    sha256: createHash("sha256").update("tw").digest("hex"),
    uuid: /^[0-9a-f-]{36}$/.test(randomUUID()),
    als: seen,
    emitted,
  };
}

// connect() (cloudflare:sockets), or ctx.tailnet.connect() with
// via=tailnet: open a TCP socket, send "ping\n", and read until the reply
// holds "ping" (the fake host's server greets first, then echoes) or the
// socket ends.
async function tcp(ctx, host, port, via, tls) {
  try {
    const opts = tls ? { tls: "netd" } : undefined;
    const s = via === "tailnet" ? ctx.tailnet.connect({ hostname: host, port: Number(port) }, opts) : connect({ hostname: host, port: Number(port) });
    await s.opened;
    const w = s.writable.getWriter();
    await w.write(new TextEncoder().encode("ping\n"));
    const r = s.readable.getReader();
    let read = "";
    while (!read.includes("ping")) {
      const { value, done } = await r.read();
      if (done) break;
      read += new TextDecoder().decode(value);
    }
    await s.close();
    return { ok: read.includes("ping"), read: read.slice(0, 200) };
  } catch (e) {
    return { ok: false, error: String(e?.message ?? e), code: e?.code };
  }
}

// hold opens a socket like tcp(), then reads until the socket ends, and
// reports how (a kill resets it: "error"), for at most 30 s.
async function hold(ctx, host, port, via) {
  const st = Date.now();
  const s = via === "tailnet" ? ctx.tailnet.connect({ hostname: host, port: Number(port) }) : connect({ hostname: host, port: Number(port) });
  const r = s.readable.getReader();
  try {
    await s.opened;
    const w = s.writable.getWriter();
    await w.write(new TextEncoder().encode("ping\n"));
    const t = setTimeout(() => r.cancel("timeout"), 30000);
    for (;;) {
      const { done } = await r.read();
      if (done) break;
    }
    clearTimeout(t);
    return { ended: "done", ms: Date.now() - st };
  } catch (e) {
    return { ended: "error", error: String(e?.message ?? e), ms: Date.now() - st };
  }
}

// DO storage: the KV API (put/get/list/delete, transaction) and SQLite.
async function storage(ctx) {
  const st = ctx.storage;
  await st.put("a", 1);
  await st.put({ b: 2, c: { n: 3 } });
  await st.transaction(async (tx) => {
    await tx.put("d", 4);
  });
  const got = await st.get("c");
  const listed = [...(await st.list({ prefix: "" })).keys()];
  await st.delete("a");
  const afterDelete = (await st.get("a")) ?? null;
  st.sql.exec("CREATE TABLE IF NOT EXISTS t (k TEXT PRIMARY KEY, v INTEGER)");
  st.sql.exec("INSERT OR REPLACE INTO t VALUES (?, ?), (?, ?)", "x", 1, "y", 2);
  const sum = st.sql.exec("SELECT sum(v) AS s FROM t").one().s;
  return { got, listed, afterDelete, sqlSum: sum, sqlBytes: st.sql.databaseSize > 0 };
}

async function route(url, ctx) {
  switch (url.pathname) {
    case "/node":
      return Response.json(await nodeCompat());
    case "/connect":
      return Response.json(await tcp(ctx, url.searchParams.get("host"), url.searchParams.get("port"), url.searchParams.get("via"), url.searchParams.has("tls")));
    case "/hold":
      return Response.json(await hold(ctx, url.searchParams.get("host"), url.searchParams.get("port"), url.searchParams.get("via")));
    case "/storage":
      return Response.json(await storage(ctx));
  }
  return null;
}

export class Facet extends DurableObject {
  async fetch(req) {
    return (await route(new URL(req.url), this.ctx)) ?? Response.json({ kind: "facet" });
  }
}

export class Plain extends DurableObject {
  async fetch(req) {
    return (await route(new URL(req.url), this.ctx)) ?? Response.json({ kind: "plain" });
  }
}

export default {
  async fetch(req, env, ctx) {
    return (await route(new URL(req.url), ctx)) ?? Response.json({ kind: "worker" });
  },
};
