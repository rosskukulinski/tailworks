// A black-box test: `tw dev` runs the app (the same netd and workerd as a
// deployment, on a local control plane) and the test calls it as two tailnet
// identities, each from its own dev node. Needs tailworks on PATH (or TW_BIN).
// docs/testing.md has unit tests too (@tailworks/tailnet/testing, vitest).
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { startDev } from "@tailworks/tailnet/testing/dev";

let app;
before(async () => {
  app = await startDev({
    dir: fileURLToPath(new URL("..", import.meta.url)),
    callers: ["alice", "bob"],
  });
});
after(() => app?.stop());

const post = (who, path, body = {}) => who.fetch(path, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });

test("the lock is held by a tailnet identity", async () => {
  const alice = app.as("alice");
  const bob = app.as("bob");

  const got = await post(alice, "/locks/demo/acquire", { ttlMs: 5000 });
  assert.equal(got.status, 200);
  const { token } = await got.json();

  const busy = await post(bob, "/locks/demo/acquire");
  assert.equal(busy.status, 409);
  const held = await busy.json();
  assert.equal(held.error, "lock_held");
  assert.equal(held.holder.nodeId, alice.nodeId);

  // only the holder's node can release it, even with the right token
  assert.equal((await post(bob, "/locks/demo/release", { token })).status, 403);
  assert.equal((await post(alice, "/locks/demo/release", { token })).status, 200);
  const next = await post(bob, "/locks/demo/acquire");
  assert.equal(next.status, 200);
  assert.ok((await next.json()).token > token, "fencing tokens only increase");
});
