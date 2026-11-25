import { test } from "node:test";
import assert from "node:assert/strict";
import { withTailnet, caller, requester, authorizeTool, introspectionVerifier } from "../dist/index.js";

const RES = "https://mcp.example.ts.net/mcp";
const IDP = "https://idp.example.ts.net";

// A public (Funnel) request as netd delivers it: request.cf.tailscale.requester.
const pubReq = (path, headers = {}, method = "POST") => {
  const r = new Request("https://mcp.example.ts.net" + path, { method, headers });
  Object.defineProperty(r, "cf", { value: { objectId: "o", traceId: "0123456789abcdef0123456789abcdef", tailscale: { requester: { kind: "public", ip: "203.0.113.7", proto: "https", host: "mcp.example.ts.net" } } } });
  return r;
};
const tailnetReq = (path) => {
  const r = new Request("http://mcp" + path, { method: "POST" });
  Object.defineProperty(r, "cf", { value: { objectId: "o", tailscale: { caller: { nodeId: "nA", hostname: "laptop", tags: [], user: { login: "alice@example.com" }, capMap: {} } } } });
  return r;
};

// verify accepts "good" (audience = RES), "other-aud" (another resource), "no-aud".
const verify = async (token) => {
  if (token === "good") return { subject: "42", login: "alice@example.com", clientId: "c1", scopes: ["openid"], audience: ["c1", RES] };
  if (token === "other-aud") return { subject: "42", audience: ["c1", "https://other.example.ts.net/mcp"] };
  if (token === "no-aud") return { subject: "42" };
  if (token === "boom") throw new Error("idp down");
  return null;
};
const opts = { path: "/mcp", publicOAuth: { resource: RES, authorizationServers: [IDP], verify } };
const echo = withTailnet(async () => Response.json(requester()), opts);

test("public request without publicOAuth is still 403 public_request", async () => {
  const h = withTailnet(async () => new Response("reached"), { path: "/mcp" });
  const r = await h.fetch(pubReq("/mcp", { authorization: "Bearer good" }), {}, {});
  assert.equal(r.status, 403);
  assert.equal(r.headers.get("tw-error"), "public_request");
});

test("no token: 401 with the protected resource metadata URL", async () => {
  const r = await echo.fetch(pubReq("/mcp"), {}, {});
  assert.equal(r.status, 401);
  assert.equal(r.headers.get("www-authenticate"), `Bearer resource_metadata="https://mcp.example.ts.net/.well-known/oauth-protected-resource/mcp"`);
});

test("rejected, wrong-audience and audience-less tokens are 401 invalid_token", async () => {
  for (const t of ["bad", "other-aud", "no-aud"]) {
    const r = await echo.fetch(pubReq("/mcp", { authorization: `Bearer ${t}` }), {}, {});
    assert.equal(r.status, 401, t);
    assert.match(r.headers.get("www-authenticate"), /error="invalid_token"/, t);
  }
  // A non-Bearer scheme is "no token".
  const basic = await echo.fetch(pubReq("/mcp", { authorization: "Basic Zm9vOmJhcg==" }), {}, {});
  assert.equal(basic.status, 401);
  assert.doesNotMatch(basic.headers.get("www-authenticate"), /invalid_token/);
  // verify throwing is an error, never an accepted token.
  await assert.rejects(echo.fetch(pubReq("/mcp", { authorization: "Bearer boom" }), {}, {}), /idp down/);
});

test("an accepted token gives requester() a public caller; caller() and authorizeTool refuse it", async () => {
  const r = await echo.fetch(pubReq("/mcp", { authorization: "Bearer good" }), {}, {});
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { kind: "public", subject: "42", scopes: ["openid"], ip: "203.0.113.7", traceId: "0123456789abcdef0123456789abcdef", login: "alice@example.com", clientId: "c1" });
  const h = withTailnet(async () => {
    const out = {};
    try { caller(); } catch (e) { out.caller = e.code; }
    try { authorizeTool(requester(), "example.com/cap/mcp", { tool: "refund" }); } catch (e) { out.authorize = e.code; }
    return Response.json(out);
  }, opts);
  assert.deepEqual(await (await h.fetch(pubReq("/mcp", { authorization: "Bearer good" }), {}, {})).json(), { caller: "public_request", authorize: "public_request" });
});

test("tailnet requests ignore bearer tokens and keep caller()", async () => {
  const h = withTailnet(async () => Response.json({ kind: requester().kind, node: caller().nodeId }), opts);
  const req = tailnetReq("/mcp");
  const r = await h.fetch(req, {}, {});
  assert.deepEqual(await r.json(), { kind: "tailnet", node: "nA" });
});

test("protected resource metadata is served at the RFC 9728 paths", async () => {
  for (const p of ["/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"]) {
    const r = await echo.fetch(pubReq(p, {}, "GET"), {}, {});
    assert.equal(r.status, 200, p);
    assert.deepEqual(await r.json(), { resource: RES, authorization_servers: [IDP], bearer_methods_supported: ["header"] });
  }
  assert.equal((await echo.fetch(pubReq("/.well-known/oauth-protected-resource/other", {}, "GET"), {}, {})).status, 404);
});

test("publicOAuth options are checked up front", () => {
  const h = async () => new Response();
  assert.throws(() => withTailnet(h, { publicOAuth: { resource: "http://mcp/mcp", authorizationServers: [IDP], verify } }), /https URL/);
  assert.throws(() => withTailnet(h, { publicOAuth: { resource: RES + '"', authorizationServers: [IDP], verify } }), /https URL/);
  assert.throws(() => withTailnet(h, { publicOAuth: { resource: RES, authorizationServers: [], verify } }), /authorizationServers/);
  assert.throws(() => withTailnet(h, { publicOAuth: { resource: RES, authorizationServers: [IDP] } }), /verify/);
});

test("introspectionVerifier maps an RFC 7662 answer and refuses inactive or expired tokens", async () => {
  const now = Math.floor(Date.now() / 1000);
  const answers = {
    good: { active: true, sub: "42", username: "alice@example.com", client_id: "c1", scope: "openid email", aud: ["c1", RES], exp: now + 60 },
    single: { active: true, sub: "42", aud: RES },
    inactive: { active: false },
    expired: { active: true, sub: "42", aud: [RES], exp: now - 1 },
    nosub: { active: true, aud: [RES] },
  };
  const seen = [];
  const v = introspectionVerifier({
    endpoint: IDP + "/introspect",
    async fetch(url, init) {
      seen.push({ url, init });
      const token = new URLSearchParams(init.body).get("token");
      if (token === "down") return new Response("no", { status: 502 });
      return Response.json(answers[token] ?? { active: false });
    },
  });
  assert.deepEqual(await v("good"), { subject: "42", audience: ["c1", RES], login: "alice@example.com", clientId: "c1", scopes: ["openid", "email"] });
  assert.deepEqual(await v("single"), { subject: "42", audience: [RES] });
  for (const t of ["inactive", "expired", "nosub", "unknown"]) assert.equal(await v(t), null, t);
  await assert.rejects(v("down"), /502/);
  assert.equal(seen[0].url, IDP + "/introspect");
  assert.equal(seen[0].init.method, "POST");
  assert.equal(seen[0].init.headers["content-type"], "application/x-www-form-urlencoded");
});
