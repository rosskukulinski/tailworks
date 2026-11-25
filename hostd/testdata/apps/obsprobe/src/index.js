// Test fixture for observability (ADR-045): console output,
// exceptions, a DO call from the Worker and egress from the DO, so the Go
// tests can check request records, metrics, OTLP export and traceparent
// propagation; RPC to both DOs, their alarms and a cron run (#295). Deliberately without nodejs_compat (tailworks adds nodejs_als).
import { DurableObject } from "cloudflare:workers";

async function echo(env, from) {
  // The echo node answers with the headers it received.
  const r = await fetch(env.UPSTREAM + "?from=" + from);
  return r.json();
}

export class Obj extends DurableObject {
  async fetch(req) {
    const name = this.ctx.id.name;
    console.log("in Obj", name);
    if (new URL(req.url).pathname.endsWith("/boom")) throw new Error("obj boom " + name);
    const e = await echo(this.env, "obj");
    return Response.json({ name, traceparent: req.headers.get("traceparent"), echoTraceparent: e.headers?.Traceparent?.[0] ?? null });
  }

  // RPC: the runtime strips its trace marker, so args are the caller's.
  async ping(...args) {
    console.log("rpc Obj", this.ctx.id.name);
    const e = await echo(this.env, "obj-rpc");
    return { args, echoTraceparent: e.headers?.Traceparent?.[0] ?? null };
  }
  async arm() {
    await this.ctx.storage.put("alarm", null);
    await this.ctx.storage.setAlarm(Date.now() + 50);
  }
  async alarm() {
    console.log("alarm Obj");
    const e = await echo(this.env, "obj-alarm");
    await this.ctx.storage.put("alarm", e.headers?.Traceparent?.[0] ?? "none");
  }
  async alarmSeen() {
    return (await this.ctx.storage.get("alarm")) ?? null;
  }
}

export class Plain extends DurableObject {
  async fetch(req) {
    console.warn("in Plain", this.ctx.id.name);
    const e = await echo(this.env, "plain");
    return Response.json({ name: this.ctx.id.name, traceparent: req.headers.get("traceparent"), echoTraceparent: e.headers?.Traceparent?.[0] ?? null });
  }

  // RPC: the runtime strips its trace marker, so args are the caller's.
  async ping(...args) {
    console.log("rpc Plain", this.ctx.id.name);
    const e = await echo(this.env, "plain-rpc");
    return { args, echoTraceparent: e.headers?.Traceparent?.[0] ?? null };
  }
  async arm() {
    await this.ctx.storage.put("alarm", null);
    await this.ctx.storage.setAlarm(Date.now() + 50);
  }
  async alarm() {
    console.log("alarm Plain");
    const e = await echo(this.env, "plain-alarm");
    await this.ctx.storage.put("alarm", e.headers?.Traceparent?.[0] ?? "none");
  }
  async alarmSeen() {
    return (await this.ctx.storage.get("alarm")) ?? null;
  }
}

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const seen = req.headers.get("traceparent");
    if (u.pathname === "/throw") {
      console.error("about to throw");
      throw new Error("worker boom");
    }
    const m = u.pathname.match(/^\/(obj|plain)\/([^/]+)$/);
    if (m) {
      console.log("calling", m[1], m[2]);
      // A new Request: the runtime gives the DO call its own child span.
      const ns = m[1] === "obj" ? env.OBJ : env.PLAIN;
      const r = await ns.getByName(m[2]).fetch(new Request("http://do/" + m[2]));
      return Response.json({ worker: seen, do: await r.json() });
    }
    const rpc = u.pathname.match(/^\/(rpc|arm|alarm)\/(obj|plain)\/([^/]+)$/);
    if (rpc) {
      const stub = (rpc[2] === "obj" ? env.OBJ : env.PLAIN).getByName(rpc[3]);
      if (rpc[1] === "rpc") return Response.json({ worker: seen, rpc: await stub.ping("a", 2) });
      if (rpc[1] === "arm") return Response.json({ armed: (await stub.arm()) ?? true });
      return Response.json({ alarm: await stub.alarmSeen() });
    }
    if (u.pathname === "/egress") {
      const e = await echo(env, "worker");
      return Response.json({ worker: seen, echoTraceparent: e.headers?.Traceparent?.[0] ?? null });
    }
    console.log("hello", u.pathname, "secret=" + (u.searchParams.get("token") ? "[given]" : "none"));
    return Response.json({ worker: seen });
  },
  async scheduled(controller, env) {
    console.log("cron", controller.cron);
    await echo(env, "cron");
  },
};
