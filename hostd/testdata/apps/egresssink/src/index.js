// Test fixture for hostd/gen/egress_sink_test.go (ledger claim C17): every
// way tenant code can try to reach the network, each aimed at a loopback
// sink the test listens on. None may reach the sink directly: what the
// platform carries goes through hostd's egress sockets (netd's ADR-019
// guard in production), and everything else fails.
import { DurableObject } from "cloudflare:workers";
import { connect } from "cloudflare:sockets";

const withTimeout = (p, ms) => Promise.race([p, new Promise((_, rej) => setTimeout(() => rej(new Error("timeout")), ms))]);

async function attempt(name, fn) {
  try {
    return { name, ok: true, result: await withTimeout(fn(), 3000) };
  } catch (e) {
    return { name, ok: false, error: String(e?.message ?? e) };
  }
}

async function summary(r) {
  return { status: r.status, twError: r.headers.get("tw-error"), text: (await r.text()).slice(0, 2000) };
}

async function probes(sinkURL) {
  const u = new URL(sinkURL);
  const out = [];
  out.push(await attempt("fetch", async () => summary(await fetch(sinkURL))));
  out.push(await attempt("fetch-post", async () => summary(await fetch(sinkURL, { method: "POST", body: "x" }))));
  out.push(await attempt("fetch-host-header", async () => summary(await fetch(new Request(sinkURL, { headers: { host: "evil.example" } })))));
  out.push(await attempt("redirect", async () =>
    summary(await fetch("http://peer.example.ts.net/__redirect?to=" + encodeURIComponent(sinkURL), { redirect: "follow" }))));
  out.push(await attempt("websocket-upgrade", async () => {
    const r = await fetch(sinkURL, { headers: { upgrade: "websocket" } });
    return { status: r.status, webSocket: !!r.webSocket };
  }));
  out.push(await attempt("websocket-ctor", () => new Promise((resolve, reject) => {
    const ws = new WebSocket(sinkURL.replace(/^http/, "ws"));
    ws.addEventListener("open", () => { ws.close(); resolve("open"); });
    ws.addEventListener("error", () => reject(new Error("ws error")));
    ws.addEventListener("close", () => resolve("closed"));
  })));
  out.push(await attempt("connect", async () => {
    const s = connect({ hostname: u.hostname, port: Number(u.port) });
    await s.opened;
    const w = s.writable.getWriter();
    await w.write(new TextEncoder().encode("GET / HTTP/1.0\r\n\r\n"));
    await s.close();
    return "opened";
  }));
  out.push(await attempt("connect-starttls", async () => {
    const s = connect({ hostname: u.hostname, port: Number(u.port) }, { secureTransport: "starttls" });
    await s.opened;
    await s.close();
    return "opened";
  }));
  out.push(await attempt("cache-put", async () => {
    await caches.default.put(sinkURL, new Response("x", { headers: { "cache-control": "max-age=60" } }));
    return "put";
  }));
  out.push(await attempt("cache-match", async () => {
    const r = await caches.default.match(sinkURL);
    return r ? r.status : null;
  }));
  out.push(await attempt("node-net", async () => {
    const net = await import("node:net");
    return await new Promise((resolve, reject) => {
      const s = net.connect({ host: u.hostname, port: Number(u.port) }, () => { s.end(); resolve("connected"); });
      s.on("error", reject);
    });
  }));
  out.push(await attempt("node-http", async () => {
    const http = await import("node:http");
    return await new Promise((resolve, reject) => {
      const req = http.get(sinkURL, (res) => { res.resume(); resolve(res.statusCode); });
      req.on("error", reject);
    });
  }));
  return out;
}

export class Sink extends DurableObject {
  async fetch(req) {
    return Response.json(await probes(new URL(req.url).searchParams.get("sink")));
  }
}

export default {
  async fetch(req) {
    return Response.json(await probes(new URL(req.url).searchParams.get("sink")));
  },
};
