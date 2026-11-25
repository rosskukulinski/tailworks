// Unit tests for the lock template's client (templates/lock/src/client.ts)
// against a fake lock service. The end-to-end tests are
// hostd:TestLockTemplate and e2e/locks.
import { test } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import * as esbuild from "esbuild";

const src = fileURLToPath(new URL("../templates/lock/src/client.ts", import.meta.url));
const built = await esbuild.build({ entryPoints: [src], bundle: true, format: "esm", platform: "neutral", write: false });
const { lockClient } = await import("data:text/javascript;base64," + Buffer.from(built.outputFiles[0].text).toString("base64"));

// fakeService answers acquire, renew and release, and counts them.
function fakeService() {
  const calls = { acquire: 0, renew: 0, release: 0 };
  const fetch = async (url, init) => {
    const op = new URL(url).pathname.split("/").pop();
    calls[op]++;
    const body = JSON.parse(init.body ?? "{}");
    const ttlMs = body.ttlMs ?? 1000;
    return Response.json({ token: 7, ttlMs, expiresAt: Date.now() + ttlMs, holder: { nodeId: "n1", node: "a.ts.net", tags: [] } });
  };
  return { calls, fetch };
}

const busy = (ms) => {
  const end = Date.now() + ms;
  while (Date.now() < end);
};

test("withLock: fn returns within the lease", async () => {
  const svc = fakeService();
  const locks = lockClient({ url: "http://locks", fetch: svc.fetch });
  const out = await locks.withLock("job", async (l) => l.token, { ttlMs: 1000 });
  assert.equal(out, 7);
  assert.equal(svc.calls.release, 1);
});

test("withLock: renews a lease held longer than its ttl", async () => {
  const svc = fakeService();
  const locks = lockClient({ url: "http://locks", fetch: svc.fetch });
  const out = await locks.withLock("job", async (l) => {
    await new Promise((r) => setTimeout(r, 700));
    return l.signal.aborted;
  }, { ttlMs: 300 });
  assert.equal(out, false);
  assert.ok(svc.calls.renew >= 2, `renewals: ${svc.calls.renew}`);
  assert.equal(svc.calls.release, 1);
});

// #196: the deadline passed while fn ran, but the event loop was too busy
// for the renewal or the expiry timer to run. withLock must not report that
// fn held the lock throughout.
test("withLock: lease_expired when the deadline passed before fn returned, even if the expiry timer has not fired", async () => {
  const svc = fakeService();
  const locks = lockClient({ url: "http://locks", fetch: svc.fetch });
  await assert.rejects(
    locks.withLock("job", async () => {
      busy(600); // blocks the event loop past the 300 ms deadline: no renewal, no expiry timer
      return "done";
    }, { ttlMs: 300 }),
    (e) => e.name === "LockError" && e.code === "lease_expired",
  );
  assert.equal(svc.calls.renew, 0);
  assert.equal(svc.calls.release, 1, "still releases (a lease that is already gone is fine)");
});
