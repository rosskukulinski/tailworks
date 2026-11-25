// A black-box test: `tw dev` runs the app (the Node tier's supervisor and
// the same netd as a deployment, on a local control plane) and the test calls
// it as a tailnet identity from its own dev node. Needs tailworks on PATH (or
// TW_BIN) and `npm install`. docs/testing.md has the details.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { startDev } from "@tailworks/tailnet/testing/dev";

let app;
before(async () => {
  app = await startDev({
    dir: fileURLToPath(new URL("..", import.meta.url)),
    callers: ["alice"],
  });
});
after(() => app?.stop());

test("the server sees the verified caller", async () => {
  const alice = app.as("alice");
  const r = await alice.fetch("/whoami");
  assert.equal(r.status, 200);
  const claims = await r.json();
  assert.equal(claims.caller.nodeId, alice.nodeId);
});
