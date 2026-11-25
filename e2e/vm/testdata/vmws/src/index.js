// e2e/vm WebSockets fixture: GET /<ROOM|PLAIN>/<name>/ws upgrades to a
// WebSocket the object accepts with the hibernation API
// (ctx.acceptWebSocket). Each message gets a reply with the socket's count
// (kept in its attachment, so it survives hibernation), the object's boot
// ID (new after an eviction) and the number of open sockets; "all" also
// broadcasts to every socket.
import { DurableObject } from "cloudflare:workers";

class Base extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.boot = crypto.randomUUID();
  }
  async fetch(req) {
    if (req.headers.get("upgrade") !== "websocket") return Response.json({ boot: this.boot, conns: this.ctx.getWebSockets().length });
    const [client, server] = Object.values(new WebSocketPair());
    this.ctx.acceptWebSocket(server);
    server.serializeAttachment({ n: 0, tag: new URL(req.url).searchParams.get("tag") });
    server.send(JSON.stringify({ type: "hello", boot: this.boot }));
    return new Response(null, { status: 101, webSocket: client });
  }
  async webSocketMessage(ws, msg) {
    const att = ws.deserializeAttachment();
    att.n++;
    ws.serializeAttachment(att);
    const out = JSON.stringify({ type: "reply", msg, n: att.n, tag: att.tag, boot: this.boot, conns: this.ctx.getWebSockets().length });
    if (msg === "all") {
      for (const s of this.ctx.getWebSockets()) s.send(out);
    } else {
      ws.send(out);
    }
  }
  async webSocketClose(ws, code) {
    ws.close(code, "bye");
  }
}

export class Room extends Base {}
export class PlainRoom extends Base {}

export default {
  async fetch(req, env) {
    const [, ns, name] = new URL(req.url).pathname.split("/");
    if (!env[ns] || !name) return Response.json({ error: "want /ROOM|PLAIN/<name>/..." }, { status: 404 });
    return env[ns].getByName(name).fetch(req);
  },
};
