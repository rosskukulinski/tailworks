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

const say = async (who, message) => {
  const r = await who.fetch("/", { method: "POST", body: message });
  assert.equal(r.status, 200);
  return r.json();
};

test("a server tool runs, and the chat is the caller's own", async () => {
  const alice = app.as("alice");
  const r = await say(alice, "What is the weather in Tokyo?");
  assert.equal(r.status, "completed");
  const weather = r.parts.find((p) => p.type === "tool-getWeather");
  assert.equal(weather?.state, "output-available");
  assert.equal(weather.output.city, "Tokyo");
  assert.match(r.reply, /getWeather/);

  const mine = await (await alice.fetch("/messages")).json();
  assert.equal(mine.chat, r.chat);
  assert.equal(mine.messages.length, 2);
  const theirs = await (await app.as("bob").fetch("/messages")).json();
  assert.notEqual(theirs.chat, r.chat);
  assert.equal(theirs.messages.length, 0);
});

test("a large calculation waits for approval; a small one runs", async () => {
  const alice = app.as("alice");
  const small = await say(alice, "47 * 19");
  assert.equal(small.parts.find((p) => p.type === "tool-calculate")?.output?.result, 893);

  const large = await say(alice, "5000 * 2");
  assert.equal(large.parts.find((p) => p.type === "tool-calculate")?.state, "approval-requested");
});

test("GET / is the chat page for the caller's own chat", async () => {
  const alice = app.as("alice");
  const { chat } = await (await alice.fetch("/messages")).json();
  const r = await alice.fetch("/");
  assert.equal(r.status, 200);
  assert.match(r.headers.get("content-type"), /text\/html/);
  const html = await r.text();
  assert.ok(html.includes(JSON.stringify(chat)), "the page names the caller's chat");
  assert.match(html, /cf_agent_use_chat_request/);
});

// The page's WebSocket protocol (the same one useAgentChat speaks): a chat
// request, an approval, and a client tool answered by the "browser".
test("over the WebSocket: approve a calculation, answer a client tool", async () => {
  const bob = app.as("bob");
  const { chat } = await (await bob.fetch("/messages")).json();
  const ws = new WebSocket(bob.url.replace(/^http/, "ws") + "/agents/chat-agent/" + chat);
  await new Promise((ok, fail) => { ws.onopen = ok; ws.onerror = fail; });
  const frames = [];
  let waiter = null;
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    frames.push(m);
    if (m.type === "cf_agent_use_chat_response" && m.done && waiter) { const w = waiter; waiter = null; w(m); }
  };
  const done = () => new Promise((ok, fail) => { waiter = ok; setTimeout(() => fail(new Error("no done frame: " + JSON.stringify(frames.slice(-5)))), 15000); });
  const send = (m) => ws.send(JSON.stringify(m));
  const transcript = async () => (await (await bob.fetch("/messages")).json()).messages;
  const part = (msgs, type) => msgs.flatMap((m) => m.parts).filter((p) => p.type === type).at(-1);

  try {
    const user = (text) => ({ id: crypto.randomUUID(), role: "user", parts: [{ type: "text", text }] });
    let msgs = [user("5000 * 2")];
    let d = done();
    send({ type: "cf_agent_use_chat_request", id: crypto.randomUUID(), init: { method: "POST", body: JSON.stringify({ messages: msgs, trigger: "submit-message" }) } });
    assert.ok(!(await d).error);
    msgs = await transcript();
    const calc = part(msgs, "tool-calculate");
    assert.equal(calc.state, "approval-requested");

    d = done();
    send({ type: "cf_agent_tool_approval", toolCallId: calc.toolCallId, approved: true, autoContinue: true });
    await d;
    msgs = await transcript();
    assert.equal(part(msgs, "tool-calculate").output.result, 10000);

    msgs = [...msgs, user("What timezone am I in?")];
    d = done();
    send({ type: "cf_agent_use_chat_request", id: crypto.randomUUID(), init: { method: "POST", body: JSON.stringify({ messages: msgs, trigger: "submit-message" }) } });
    await d;
    const tz = part(await transcript(), "tool-getUserTimezone");
    assert.equal(tz.state, "input-available");

    d = done();
    send({ type: "cf_agent_tool_result", toolCallId: tz.toolCallId, toolName: "getUserTimezone", output: { timezone: "Europe/Oslo" }, autoContinue: true });
    await d;
    const after = await transcript();
    assert.equal(part(after, "tool-getUserTimezone").output.timezone, "Europe/Oslo");
    assert.match(after.at(-1).parts.filter((p) => p.type === "text").map((p) => p.text).join(""), /Europe\/Oslo/);
  } finally {
    ws.close();
  }
});
