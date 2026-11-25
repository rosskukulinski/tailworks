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

test("each caller gets their own agent", async () => {
  const alice = app.as("alice");
  const bob = app.as("bob");
  const call = async (who) => (await who.fetch("/")).json();

  const a1 = await call(alice);
  const a2 = await call(alice);
  assert.equal(a2.caller.nodeId, alice.nodeId);
  assert.equal(a2.agent, a1.agent); // GET / is keyed by the caller (principalKey)
  assert.equal(a2.state.visits, a1.state.visits + 1);

  const b = await call(bob);
  assert.equal(b.caller.nodeId, bob.nodeId);
  assert.notEqual(b.agent, a1.agent);
  assert.equal(b.state.visits, 1);
});
