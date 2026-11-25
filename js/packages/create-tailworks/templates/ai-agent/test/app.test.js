// A black-box test: `tw dev` runs the app (the same netd and workerd as a
// deployment, on a local control plane) and the test calls it as a tailnet
// identity from its own dev node. Needs tailworks on PATH (or TW_BIN).
// docs/testing.md has unit tests too (@tailworks/tailnet/testing, vitest).
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

test("the agent's MCP calls leave from its own node, and a missing server is a typed error", async () => {
  // tw dev runs one app, so no MCP server runs next to this one: MCP_URL
  // (http://tw-mcp/mcp) is not in the agent node's netmap: tailworks refuses the call with a typed
  // error, which the template returns as its catalog response.
  const r = await app.as("alice").fetch("/?prompt=where%20is%20o1");
  assert.equal(r.status, 403);
  assert.equal(r.headers.get("tw-error"), "peer_not_visible");
  const body = await r.json();
  assert.equal(body.code, "peer_not_visible");
});
