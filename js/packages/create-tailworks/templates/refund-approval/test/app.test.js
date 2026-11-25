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

test("a refund waits for an approval from a granted identity", async () => {
  const alice = app.as("alice");
  const asked = await alice.fetch("/agents/refund-agent/alice/refund?order=o1&amount=42", { method: "POST" });
  assert.equal(asked.status, 200);
  const { id } = await asked.json();

  const list = await (await alice.fetch("/agents/refund-agent/alice/refunds")).json();
  const row = list.refunds.find((r) => r.id === id);
  assert.equal(row.state, "waiting for approval");

  // Deciding is tailworks' job, by identity and grant: tw dev grants no
  // github.com/rosskukulinski/tailworks/cap/approve, so bob's approval is refused.
  const denied = await app.as("bob").fetch(`/_tw/approvals/refund/${id}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ decision: "approve" }),
  });
  assert.equal(denied.status, 403);
  assert.equal(denied.headers.get("tw-error"), "cap_missing");
});
