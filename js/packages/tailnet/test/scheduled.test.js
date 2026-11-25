import { test } from "node:test";
import assert from "node:assert/strict";
import { wrapHandler } from "../dist/runtime.js";

// A cron run dispatched by the platform (cf.tw.kind "scheduled").
function schedReq() {
  return {
    cf: { tw: { kind: "scheduled", cron: "* * * * *", scheduledTime: 1 } },
  };
}

// realCtx records whether a waitUntil() promise escaped to the request's own
// waitUntil, which would outlive the run (H17, #152).
function realCtx() {
  const escaped = [];
  return {
    escaped,
    ctx: {
      waitUntil: (p) => escaped.push(p),
      passThroughOnException() {
        return this;
      },
    },
  };
}

const tw = {
  async call() {
    return { ok: true, value: {} };
  },
};
const env = () => ({ __TW: tw });

function sleepThen(log, ms, v) {
  return new Promise((r) => setTimeout(r, ms)).then(() => log.push(v));
}

test("cron run: ctx.waitUntil() work belongs to the run (object export)", async () => {
  const log = [];
  const H = wrapHandler(
    {
      async fetch() {
        return new Response("fetch");
      },
      async scheduled(c, env, ctx) {
        ctx.waitUntil(sleepThen(log, 30, "waited"));
        assert.equal(
          ctx.passThroughOnException(),
          ctx.passThroughOnException(),
        ); // other methods still work
        log.push("returned");
      },
    },
    {},
  );
  const { escaped, ctx } = realCtx();
  const r = await H.fetch(schedReq(), env(), ctx);
  assert.deepEqual(await r.json(), { outcome: "ok", noRetry: false });
  assert.deepEqual(log, ["returned", "waited"]); // answered only after the waitUntil work settled
  assert.equal(escaped.length, 0);
});

test("cron run: waitUntil work settles even when scheduled() throws", async () => {
  const log = [];
  const H = wrapHandler(
    {
      async fetch() {},
      async scheduled(c, env, ctx) {
        ctx.waitUntil(sleepThen(log, 20, "waited"));
        ctx.waitUntil(Promise.reject(new Error("ignored")));
        throw new Error("boom");
      },
    },
    {},
  );
  const r = await H.fetch(schedReq(), env(), realCtx().ctx);
  const b = await r.json();
  assert.equal(b.outcome, "exception");
  assert.equal(b.error, "boom");
  assert.deepEqual(log, ["waited"]);
});

test("cron run: ctx.waitUntil() work belongs to the run (WorkerEntrypoint class)", async () => {
  const log = [];
  class Base {
    constructor(ctx, env) {
      this.ctx = ctx;
      this.env = env;
    }
  }
  class App extends Base {
    async fetch() {
      return new Response("fetch");
    }
    async scheduled() {
      this.ctx.waitUntil(
        sleepThen(log, 30, "waited").then(() =>
          this.ctx.waitUntil(sleepThen(log, 10, "chained")),
        ),
      );
      log.push("returned");
    }
  }
  const W = wrapHandler(App, {});
  const { escaped, ctx } = realCtx();
  const r = await new W(ctx, env()).fetch(schedReq());
  assert.deepEqual(await r.json(), { outcome: "ok", noRetry: false });
  assert.deepEqual(log, ["returned", "waited", "chained"]);
  assert.equal(escaped.length, 0);
});
