// Test fixture for RT3-08 / H08 (#148): tenant code forwards a request to a
// sibling object in-process with a forged request.cf.tailscale.caller. The
// receiving object reports what getCaller() saw and writes an audit note
// (hostd stamps it with the object's actingFor).
import { DurableObject } from "cloudflare:workers";
import { getCaller, isTailnetError } from "@tailworks/tailnet";

const FORGED = {
  nodeId: "nFORGED",
  node: "ceo-laptop.victim.example.",
  hostname: "ceo-laptop",
  user: "ceo@victim.example",
  tags: [],
  addrs: ["100.64.0.66"],
  capMap: { "example.com/cap/mcp": [{ tools: ["*"] }] },
};

function who(req) {
  try {
    const c = getCaller(req);
    return { nodeId: c.nodeId, user: c.user?.login ?? null, tools: c.caps("example.com/cap/mcp") };
  } catch (e) {
    return { callerError: isTailnetError(e) ? e.code : String(e) };
  }
}

class Base extends DurableObject {
  async fetch(req) {
    const w = who(req);
    await this.ctx.tailnet?.audit([{ kind: "probe", detail: "fetch" }]);
    return Response.json({ object: this.ctx.id.name, ...w });
  }
  async who(req) {
    return { object: this.ctx.id.name, ...who(req) };
  }
  async ping() {
    return this.ctx.id.name;
  }
}
export class Room extends Base {}
export class Desk extends Base {}

// forged builds the attack request: mode "forge" replaces netd's blob with a
// forged caller; "swap" keeps everything hostd sent (its seal included) and
// swaps only the caller.
function forged(req, mode) {
  const cf = req.cf ?? {};
  const tailscale = mode === "swap" ? { ...(cf.tailscale ?? {}), caller: FORGED } : { caller: FORGED };
  return new Request("http://obj/whoami", { cf: { ...cf, tailscale } });
}

export default {
  async fetch(req, env) {
    const url = new URL(req.url);
    const [mode, cls, name] = url.pathname.split("/").filter(Boolean);
    if (mode === "self") return Response.json(who(req));
    const ns = cls === "room" ? env.ROOM : cls === "desk" ? env.DESK : null;
    if (!ns || !name) return new Response("not found", { status: 404 });
    const stub = ns.getByName(decodeURIComponent(name));
    switch (mode) {
      case "real": // the documented pattern: pass the original request along
        return stub.fetch(req);
      case "forge":
      case "swap":
        return stub.fetch(forged(req, mode));
      case "rpc":
        return Response.json(await stub.who(forged(req, "forge")));
      case "ping":
        return Response.json({ pinged: await stub.ping() });
    }
    return new Response("not found", { status: 404 });
  },
};
