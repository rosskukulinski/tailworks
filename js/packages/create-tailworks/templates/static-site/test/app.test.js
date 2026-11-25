// A black-box test: `tw dev` runs the app (the same netd and workerd as a
// deployment, on a local control plane) and the test calls it as a person
// and as a tagged node, each from its own dev node. Needs tailworks on PATH
// (or TW_BIN).
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

test("serves the files in public/", async () => {
  const alice = app.as("alice");
  const home = await alice.fetch("/");
  assert.equal(home.status, 200);
  assert.equal(home.headers.get("content-type"), "text/html; charset=utf-8");
  assert.match(await home.text(), /Hello from a static site/);

  const css = await alice.fetch("/style.css");
  assert.equal(css.headers.get("content-type"), "text/css; charset=utf-8");

  // ETag and If-None-Match: an unchanged file is a 304.
  const etag = css.headers.get("etag");
  assert.ok(etag);
  const again = await alice.fetch("/style.css", { headers: { "if-none-match": etag } });
  assert.equal(again.status, 304);
});

test("_redirects and _headers apply", async () => {
  const alice = app.as("alice");
  const r = await alice.fetch("/home", { redirect: "manual" });
  assert.equal(r.status, 301);
  assert.equal(r.headers.get("location"), "/");

  const css = await alice.fetch("/style.css");
  assert.equal(css.headers.get("x-content-type-options"), "nosniff");
  // Worker responses don't get them.
  const api = await alice.fetch("/api/whoami");
  assert.equal(api.headers.get("x-content-type-options"), null);
});

test("the Worker answers /api/* with the verified caller", async () => {
  const alice = app.as("alice");
  const me = await (await alice.fetch("/api/whoami")).json();
  assert.equal(me.login, alice.login);
});

test("/team/ is served only to people", async () => {
  const team = await app.as("alice").fetch("/team/");
  assert.equal(team.status, 200);
  assert.match(await team.text(), /Team pages/);

  const ci = await app.as("ci").fetch("/team/");
  assert.equal(ci.status, 403); // a tagged node: the Worker refused before any file
});

test("a path with no file gets 404.html", async () => {
  const r = await app.as("alice").fetch("/missing");
  assert.equal(r.status, 404);
  assert.match(await r.text(), /Not found/);
});
