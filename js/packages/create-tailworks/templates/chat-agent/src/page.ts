// The chat page, served by the Worker at `/` (tailworks doesn't serve
// wrangler `assets`). It is dependency-free: it speaks the same WebSocket
// protocol as `useAgentChat` from @cloudflare/ai-chat/react, so you can
// replace it with Cloudflare's React client (see README.md) and nothing on
// the server changes.
//
//   → cf_agent_use_chat_request  {id, init: {method: "POST", body: {messages, trigger}}}
//   → cf_agent_tool_result       {toolCallId, toolName, output, autoContinue}   (client tools)
//   → cf_agent_tool_approval     {toolCallId, approved, autoContinue}           (needsApproval)
//   → cf_agent_chat_clear
//   ← cf_agent_use_chat_response {id, body: one UI message stream chunk, done, error?}
//   ← cf_agent_chat_messages / cf_agent_message_updated  (the stored transcript)

/** page returns the chat page for the chat named `chat` (the caller's own; URL-safe). */
export function page(chat: string): string {
  // JSON in a <script>: escape "<" so a name can't close the tag.
  const name = JSON.stringify(chat).replace(/</g, "\\u003c");
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>chat-agent</title>
<style>
  :root { color-scheme: light dark; font: 15px/1.45 system-ui, sans-serif; }
  body { margin: 0; display: flex; flex-direction: column; height: 100vh; }
  header { padding: .6rem 1rem; border-bottom: 1px solid #8884; display: flex; gap: 1rem; align-items: center; }
  header h1 { font-size: 1rem; margin: 0; flex: 1; }
  header .status { font-size: .8rem; opacity: .7; }
  #log { flex: 1; overflow-y: auto; padding: 1rem; display: flex; flex-direction: column; gap: .6rem; }
  .msg { max-width: 48rem; padding: .5rem .75rem; border-radius: .6rem; white-space: pre-wrap; }
  .user { align-self: flex-end; background: #2563eb; color: #fff; }
  .assistant { align-self: flex-start; background: #8882; }
  .tool { font: 12px/1.4 ui-monospace, monospace; border: 1px solid #8886; border-radius: .4rem; padding: .35rem .5rem; margin: .3rem 0; }
  .tool b { font-weight: 600; }
  .tool button { margin: .3rem .4rem 0 0; }
  .error { color: #dc2626; }
  form { display: flex; gap: .5rem; padding: .75rem 1rem; border-top: 1px solid #8884; }
  form input { flex: 1; padding: .5rem; font: inherit; }
  .hint { font-size: .8rem; opacity: .7; padding: 0 1rem .6rem; }
</style></head>
<body>
<header><h1>chat-agent</h1><span class="status" id="status">connecting…</span><button id="clear" type="button">Clear history</button></header>
<div id="log"></div>
<form id="form"><input id="input" autocomplete="off" placeholder="Ask about the weather in Tokyo, your timezone, or 47 * 19" autofocus><button>Send</button></form>
<div class="hint">One chat per tailnet identity. Calculations with a number over 1000 ask for approval first.</div>
<script>
const CHAT = ${name};
const AGENT = "/agents/chat-agent/" + CHAT; // CHAT is URL-safe already
// crypto.randomUUID needs a secure context; a tailnet http:// page isn't one.
const uid = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");
const $ = (id) => document.getElementById(id);

let messages = [];      // the stored transcript
let live = null;        // the assistant message being streamed
const answered = new Set(); // client tool calls this page already answered

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}

function toolPart(p) {
  const name = p.type.slice("tool-".length);
  const box = el("div", "tool");
  box.append(el("b", null, name), " " + JSON.stringify(p.input ?? {}));
  if (p.state === "output-available") box.append(el("div", null, "→ " + JSON.stringify(p.output)));
  else if (p.state === "output-error") box.append(el("div", "error", "→ " + p.errorText));
  else if (p.state === "output-denied") box.append(el("div", null, "→ denied"));
  else if (p.state === "approval-requested") {
    box.append(el("div", null, "needs your approval"));
    for (const approved of [true, false]) {
      const b = el("button", null, approved ? "Approve" : "Reject");
      b.type = "button";
      b.onclick = () => { send({ type: "cf_agent_tool_approval", toolCallId: p.toolCallId, approved, autoContinue: true }); box.querySelectorAll("button").forEach((x) => (x.disabled = true)); };
      box.append(b);
    }
  } else box.append(el("div", null, "… " + (p.state ?? "")));
  return box;
}

function render() {
  const log = $("log");
  log.replaceChildren();
  const all = live && !messages.some((m) => m.id === live.id) ? [...messages, live] : messages.map((m) => (live && m.id === live.id ? live : m));
  for (const m of all) {
    const div = el("div", "msg " + m.role);
    for (const p of m.parts ?? []) {
      if (p.type === "text") div.append(p.text);
      else if (p.type.startsWith("tool-")) div.append(toolPart(p));
    }
    log.append(div);
  }
  log.scrollTop = log.scrollHeight;
}

// Apply one UI message stream chunk to the live assistant message.
function apply(c) {
  if (c.type === "start") {
    const existing = messages.find((m) => m.id === c.messageId);
    live = existing ? structuredClone(existing) : { id: c.messageId ?? uid(), role: "assistant", parts: [] };
    return;
  }
  if (!live) live = { id: uid(), role: "assistant", parts: [] };
  const tool = (id) => live.parts.find((p) => p.toolCallId === id);
  switch (c.type) {
    case "text-start": live.parts.push({ type: "text", id: c.id, text: "" }); break;
    case "text-delta": { const p = live.parts.find((p) => p.type === "text" && p.id === c.id); if (p) p.text += c.delta; break; }
    case "tool-input-available": {
      const p = tool(c.toolCallId);
      const next = { type: "tool-" + c.toolName, toolCallId: c.toolCallId, state: "input-available", input: c.input };
      p ? Object.assign(p, next) : live.parts.push(next);
      break;
    }
    case "tool-approval-request": { const p = tool(c.toolCallId); if (p) p.state = "approval-requested"; break; }
    case "tool-output-available": { const p = tool(c.toolCallId); if (p) Object.assign(p, { state: "output-available", output: c.output }); break; }
    case "tool-output-error": { const p = tool(c.toolCallId); if (p) Object.assign(p, { state: "output-error", errorText: c.errorText }); break; }
    case "tool-output-denied": { const p = tool(c.toolCallId); if (p) p.state = "output-denied"; break; }
    case "error": live.parts.push({ type: "text", text: "\\n[error] " + c.errorText }); break;
  }
}

// Client-side tools: the model calls them, this page answers.
const clientTools = {
  getUserTimezone: () => ({ timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, localTime: new Date().toLocaleTimeString() }),
};
function answerClientTools() {
  for (const m of messages) for (const p of m.parts ?? []) {
    const name = p.type?.startsWith("tool-") && p.type.slice(5);
    if (name && clientTools[name] && p.state === "input-available" && !answered.has(p.toolCallId)) {
      answered.add(p.toolCallId);
      send({ type: "cf_agent_tool_result", toolCallId: p.toolCallId, toolName: name, output: clientTools[name](p.input), autoContinue: true });
    }
  }
}

async function refresh() {
  const r = await fetch(AGENT + "/get-messages");
  if (r.ok) messages = await r.json();
  render();
  answerClientTools();
}

let ws;
function send(m) { ws.send(JSON.stringify(m)); }
function connect() {
  ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + AGENT);
  ws.onopen = () => { $("status").textContent = "connected as " + decodeURIComponent(CHAT); refresh(); };
  ws.onclose = () => { $("status").textContent = "disconnected; reconnecting…"; setTimeout(connect, 1000); };
  ws.onmessage = (e) => {
    let m;
    try { m = JSON.parse(e.data); } catch { return; }
    switch (m.type) {
      case "cf_agent_chat_messages": messages = m.messages; render(); answerClientTools(); break;
      case "cf_agent_message_updated": {
        const i = messages.findIndex((x) => x.id === m.message.id);
        i >= 0 ? (messages[i] = m.message) : messages.push(m.message);
        render(); answerClientTools(); break;
      }
      case "cf_agent_chat_clear": messages = []; live = null; render(); break;
      case "cf_agent_use_chat_response":
        if (m.error) { live = null; messages.push({ id: uid(), role: "assistant", parts: [{ type: "text", text: "[error] " + m.body }] }); render(); break; }
        if (m.body) { try { apply(JSON.parse(m.body)); render(); } catch {} }
        if (m.done) { live = null; refresh(); }
        break;
    }
  };
}

$("form").onsubmit = (e) => {
  e.preventDefault();
  const text = $("input").value.trim();
  if (!text) return;
  $("input").value = "";
  messages.push({ id: uid(), role: "user", parts: [{ type: "text", text }] });
  render();
  send({ type: "cf_agent_use_chat_request", id: uid(), init: { method: "POST", body: JSON.stringify({ messages, trigger: "submit-message" }) } });
};
$("clear").onclick = () => { send({ type: "cf_agent_chat_clear" }); messages = []; live = null; render(); };
connect();
</script>
</body></html>`;
}
