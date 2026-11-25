import { test } from "node:test";
import assert from "node:assert/strict";
import { parseTraceparent, runTraced, tracedStubFetch, currentTrace, installFetchPropagation, MAX_SPANS } from "../dist/trace.js";

const T = "4bf92f3577b34da6a3ce929d0e0e4736";
const P = "00f067aa0ba902b7";
const IN = `00-${T}-${P}-01`;

test("parseTraceparent follows W3C trace context", () => {
  assert.deepEqual(parseTraceparent(IN), { traceId: T, spanId: P, flags: "01" });
  assert.ok(parseTraceparent(`cc-${T}-${P}-01-future`)); // a future version, by its 00 prefix
  for (const bad of [null, "", "x", `ff-${T}-${P}-01`, `00-${"0".repeat(32)}-${P}-01`, `00-${T}-${"0".repeat(16)}-01`, `00-${T.toUpperCase()}-${P}-01`, `00-${T}-${P}-01-x`]) {
    assert.equal(parseTraceparent(bad), null, String(bad));
  }
});

test("egress fetch() carries the handler's context; the tenant's own traceparent wins", async () => {
  const seen = [];
  const orig = globalThis.fetch;
  globalThis.fetch = async (input, init) => {
    seen.push(new Headers(init?.headers ?? input?.headers).get("traceparent"));
    return new Response("ok");
  };
  try {
    installFetchPropagation();
    await fetch("http://outside/"); // no context: untouched
    const req = new Request("http://x/", { headers: { traceparent: IN } });
    await runTraced(req, async () => {
      assert.equal(currentTrace().traceId, T);
      await fetch("http://peer/a");
      await fetch(new Request("http://peer/b", { headers: { traceparent: IN } })); // the incoming one, forwarded
      await fetch("http://peer/c", { headers: { traceparent: `00-${"a".repeat(32)}-${"b".repeat(16)}-01` } }); // the tenant's own
      await Promise.all([1, 2].map(() => fetch("http://peer/d"))); // concurrent continuations keep it
    });
    assert.equal(seen[0], null);
    assert.equal(seen[1], IN);
    assert.equal(seen[2], IN);
    assert.equal(seen[3], `00-${"a".repeat(32)}-${"b".repeat(16)}-01`);
    assert.equal(seen[4], IN);
    assert.equal(seen[5], IN);
    // Outside any handler again.
    await fetch("http://outside/");
    assert.equal(seen[6], null);
  } finally {
    globalThis.fetch = orig;
  }
});

test("a DO stub fetch gets a child span, reported when the handler ends", async () => {
  const flushed = [];
  const req = new Request("http://x/", { headers: { traceparent: IN } });
  let calleeSaw;
  const r = await runTraced(
    req,
    async () => {
      const resp = await tracedStubFetch("Counter", "alice", [new Request("http://do/hit", { method: "POST", body: "x" })], async (input, init) => {
        calleeSaw = new Request(input, init).headers.get("traceparent");
        return new Response("boom", { status: 503 });
      });
      return resp.status;
    },
    (spans) => flushed.push(...spans),
  );
  assert.equal(r, 503);
  const tp = parseTraceparent(calleeSaw);
  assert.equal(tp.traceId, T);
  assert.notEqual(tp.spanId, P);
  assert.equal(flushed.length, 1);
  const s = flushed[0];
  assert.equal(s.spanId, tp.spanId);
  assert.equal(s.parentSpanId, P);
  assert.equal(s.name, "Counter fetch");
  assert.equal(s.error, true);
  assert.equal(s.attrs["tw.do.name"], "alice");
  assert.equal(s.attrs["http.request.method"], "POST");
  assert.equal(s.attrs["http.response.status_code"], 503);
  assert.ok(s.end >= s.start);
});

test("spans are bounded; a handler with no context reports nothing", async () => {
  const flushed = [];
  await runTraced(
    new Request("http://x/", { headers: { traceparent: IN } }),
    async () => {
      for (let i = 0; i < MAX_SPANS + 10; i++) await tracedStubFetch("C", "n", ["http://do/"], async () => new Response("ok"));
    },
    (spans) => flushed.push(...spans),
  );
  assert.equal(flushed.length, MAX_SPANS);
  let called = false;
  await runTraced(new Request("http://x/"), async () => tracedStubFetch("C", "n", ["http://do/"], async (i, init) => {
    called = true;
    assert.equal(init, undefined); // untouched
    return new Response("ok");
  }), () => assert.fail("no spans without a context"));
  assert.ok(called);
});
