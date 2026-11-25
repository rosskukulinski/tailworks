// Test fixture (#572): GET /ws upgrades to a WebSocket the Room object
// accepts with the hibernation API; every message is echoed back.
import { DurableObject } from "cloudflare:workers";

export class Room extends DurableObject {
  async fetch(req) {
    if (req.headers.get("upgrade") !== "websocket") return new Response("want a WebSocket", { status: 400 });
    const [client, server] = Object.values(new WebSocketPair());
    this.ctx.acceptWebSocket(server);
    return new Response(null, { status: 101, webSocket: client });
  }
  async webSocketMessage(ws, msg) {
    ws.send(msg);
  }
}

export default {
  async fetch(req, env) {
    return env.ROOM.getByName("r").fetch(req);
  },
};
