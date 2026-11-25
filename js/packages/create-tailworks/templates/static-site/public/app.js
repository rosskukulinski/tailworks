// Runs in the browser: ask the Worker who is visiting.
const el = document.getElementById("greeting");
try {
  const me = await (await fetch("/api/whoami")).json();
  el.textContent = me.login ? `Hello, ${me.login} (from ${me.node})` : `Hello, ${me.node} (${me.tags.join(", ")})`;
} catch (e) {
  el.textContent = "Could not reach the Worker: " + e;
}
