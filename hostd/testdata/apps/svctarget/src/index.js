// The target of svccaller's service bindings (ADR-service-bindings).
import { WorkerEntrypoint, RpcTarget } from "cloudflare:workers";
import { getCaller, isTailnetError } from "@tailworks/tailnet";

function who(x) {
  try {
    const c = getCaller(x);
    return { hostname: c.hostname, nodeId: c.nodeId, tw: c.tw?.objectId ?? null };
  } catch (e) {
    return { callerError: isTailnetError(e) ? e.code : String(e) };
  }
}

export default class Target extends WorkerEntrypoint {
  async fetch(req) {
    const u = new URL(req.url);
    if (req.headers.get("upgrade") === "websocket") {
      const [client, server] = Object.values(new WebSocketPair());
      server.accept();
      server.addEventListener("message", (e) => server.send("echo:" + e.data));
      return new Response(null, { status: 101, webSocket: client });
    }
    if (u.pathname === "/stream") {
      // Read the streamed request body chunk by chunk, then answer with a
      // streamed body (chunks with gaps). Not full duplex: tailnet ingress
      // is HTTP/1.1 through Go's server, which reads the request first.
      const chunks = [];
      const rd = req.body.getReader();
      const dec = new TextDecoder();
      for (;;) {
        const { done, value } = await rd.read();
        if (done) break;
        chunks.push(dec.decode(value, { stream: true }));
      }
      const { readable, writable } = new TransformStream();
      const w = writable.getWriter();
      const enc = new TextEncoder();
      (async () => {
        for (const c of ["got:", chunks.join(""), ":end"]) {
          await w.write(enc.encode(c));
          await new Promise((r) => setTimeout(r, 30));
        }
        await w.close();
      })();
      return new Response(readable, { headers: { "content-type": "text/plain" } });
    }
    return Response.json({
      entry: "default",
      path: u.pathname + u.search,
      method: req.method,
      caller: who(req),
      sawBindingHeader: req.headers.get("tailworks-binding"),
    });
  }
  add(a, b) {
    return a + b;
  }
  echo(v) {
    return v;
  }
  whoami() {
    return who(this.ctx);
  }
  boom() {
    throw new RangeError("boom!");
  }
  giveFn() {
    return () => 1;
  }
  giveTarget() {
    return new (class extends RpcTarget {})();
  }
}

export class Admin extends WorkerEntrypoint {
  fetch(req) {
    return Response.json({ entry: "Admin", caller: who(req) });
  }
  stats() {
    return { entry: "Admin", caller: who(this.ctx) };
  }
}
