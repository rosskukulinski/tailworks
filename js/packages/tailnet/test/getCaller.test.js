import { test } from "node:test";
import assert from "node:assert/strict";
import { getCaller, TailnetError } from "../dist/index.js";

const req = (cf) => ({ cf });

test("getCaller reads netd's cf blob", () => {
  const c = getCaller(req({
    objectId: "room-1",
    tailscale: { caller: { nodeId: "nX", node: "ttest-client.ts.net", hostname: "ttest-client", tags: ["tag:ttest"],
      addrs: ["100.64.0.1"], capMap: { "example.com/cap/ttest": [{ role: "tester" }] } } },
  }));
  assert.equal(c.nodeId, "nX");
  assert.deepEqual(c.tags, ["tag:ttest"]);
  assert.equal(c.user, undefined);
  assert.equal(c.objectId, "room-1");
  assert.equal("chain" in c, false); // on-behalf-of is not built (RFC §4.6)
  assert.equal("actingFor" in c, false);
  assert.deepEqual(c.caps("example.com/cap/ttest"), [{ role: "tester" }]);
  assert.deepEqual(c.caps("missing"), []);
});

test("getCaller throws no_identity without a caller", () => {
  assert.throws(() => getCaller(req({})), (e) => e instanceof TailnetError && e.code === "no_identity");
  assert.throws(() => getCaller({}), (e) => e.code === "no_identity");
});
