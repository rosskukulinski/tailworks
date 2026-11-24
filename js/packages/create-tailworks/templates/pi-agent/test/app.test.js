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

const ask = async (who, prompt) => (await who.fetch(`/?prompt=${encodeURIComponent(prompt)}`)).json();

test("Pi answers, runs a tool, and keeps the transcript in the caller's own assistant", async () => {
  const alice = app.as("alice");
  const a = await ask(alice, "What is 47 × 19?");
  assert.equal(a.status, "done");
  assert.equal(a.text, "893");

  const w = await ask(alice, 'Count the words in "the quick brown fox"');
  assert.equal(w.status, "done");
  assert.equal(w.text, "word_count: 4");
  assert.equal(w.assistant, a.assistant);

  const mine = await (await alice.fetch("/")).json();
  assert.ok(mine.messages.length >= 4, `transcript: ${JSON.stringify(mine.messages).slice(0, 400)}`);
  const theirs = await (await app.as("bob").fetch("/")).json();
  assert.notEqual(theirs.assistant, a.assistant);
  assert.equal(theirs.messages.length, 0);
});
