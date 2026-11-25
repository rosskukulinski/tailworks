// workers-oauth-provider on tailworks: the library's own OAUTH_KV usage (clients,
// grants, tokens with expirationTtl, list by prefix) runs on tailworks' Workers
// KV binding. /authorize approves at once as "alice" (no consent page: the
// test is the user); /api/* is the protected API; /kv lists what the
// provider stored, so the test can see it went through KV.
import { OAuthProvider, type OAuthHelpers } from "@cloudflare/workers-oauth-provider";
import { WorkerEntrypoint } from "cloudflare:workers";

type Env = { OAUTH_KV: KVNamespace; OAUTH_PROVIDER: OAuthHelpers };

export class Api extends WorkerEntrypoint<Env> {
  async fetch(req: Request) {
    const u = new URL(req.url);
    if (u.pathname === "/api/whoami") return Response.json({ props: (this.ctx as any).props });
    return new Response("not found", { status: 404 });
  }
}

const app = {
  async fetch(req: Request, env: Env) {
    const u = new URL(req.url);
    if (u.pathname === "/authorize") {
      const ar = await env.OAUTH_PROVIDER.parseAuthRequest(req);
      const { redirectTo } = await env.OAUTH_PROVIDER.completeAuthorization({
        request: ar,
        userId: "alice",
        metadata: { label: "e2e" },
        scope: ar.scope,
        props: { user: "alice", via: "tw-kv" },
      });
      return Response.redirect(redirectTo, 302);
    }
    if (u.pathname === "/kv") {
      const out: Record<string, { expiration?: number }> = {};
      let cursor: string | undefined;
      for (;;) {
        const r: any = await env.OAUTH_KV.list({ cursor });
        for (const k of r.keys) out[k.name] = { expiration: k.expiration };
        if (r.list_complete) break;
        cursor = r.cursor;
      }
      return Response.json(out);
    }
    if (u.pathname === "/listed") {
      const c = await env.OAUTH_PROVIDER.listClients();
      const g = await env.OAUTH_PROVIDER.listUserGrants("alice");
      return Response.json({ clients: c.items.length, grants: g.items.map((x) => x.clientId) });
    }
    return new Response("not found", { status: 404 });
  },
};

const ORIGIN = "https://oauthkv.ttest.ts.net";

const provider = new OAuthProvider({
  apiRoute: "/api/",
  apiHandler: Api as any,
  defaultHandler: app as any,
  authorizeEndpoint: "/authorize",
  tokenEndpoint: "/token",
  clientRegistrationEndpoint: "/register",
  scopesSupported: ["read"],
  // The API's canonical identifier (the token audience): on tailworks, the
  // workload's MagicDNS name.
  resourceMetadata: { resource: ORIGIN },
});

// The provider compares request.url's origin with resourceMetadata.resource
// (token audience, RFC 8707). Tailworks hands a Worker http:// URLs (the tailnet
// already encrypts; public routes set X-Forwarded-Proto: https), so present
// the canonical https origin, as a TLS-terminating front door would. A
// class instance on purpose: `export default new X()` keeps fetch() on the
// prototype, which tailworks' runtime must find (TestOAuthProviderKV).
class CanonicalOrigin {
  constructor(private inner: { fetch: (r: Request, e: Env, c: ExecutionContext) => Promise<Response> }) {}
  fetch(req: Request, env: Env, ctx: ExecutionContext) {
    const u = new URL(req.url);
    return this.inner.fetch(new Request(ORIGIN + u.pathname + u.search, req), env, ctx);
  }
}

export default new CanonicalOrigin(provider as any);
