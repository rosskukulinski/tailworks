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
    callers: { alice: {}, ci: { tags: ["tag:ci"] } },
  });
});
after(() => app?.stop());

test("greets each verified caller", async () => {
  const alice = app.as("alice");
  const a = await (await alice.fetch("/")).json();
  assert.equal(a.caller.nodeId, alice.nodeId);
  assert.equal(a.hello, alice.login); // a user's node: getCaller(req).user.login

  const ci = app.as("ci");
  const c = await (await ci.fetch("/")).json();
  assert.equal(c.caller.nodeId, ci.nodeId);
  assert.deepEqual(c.caller.tags, ["tag:ci"]); // a tagged node has no user
  assert.equal(c.caller.user, null);
});
