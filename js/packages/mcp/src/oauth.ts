// OAuth 2.1 resource-server support for public (Funnel) MCP requests
// (docs/mcp-public.md). Tailnet requests never use any of this: WireGuard and
// WhoIs authenticate them. A public request has no tailnet identity, so it is
// served only when the app opts in with withTailnet's `publicOAuth` and the
// request carries a bearer token that the app's verify() accepts for this
// server's resource URL.

/**
 * PublicCaller is a public (Funnel) request whose bearer token was verified.
 * It has no tailnet identity: no node, no tags, no grants (caps), and no
 * per-node kill. Gate tools on `subject` (the token's `sub`).
 */
export interface PublicCaller {
  kind: "public";
  /** The token's subject (`sub`) as the authorization server issued it. */
  subject: string;
  /** A display login (for tsidp, the Tailscale login name), if the server returned one. */
  login?: string;
  /** The OAuth client the token was issued to (e.g. the hosted assistant). */
  clientId?: string;
  scopes: readonly string[];
  /** The client address Funnel reported. */
  ip: string;
  traceId: string;
}

/** What verify() returns for a token it accepts. */
export interface VerifiedToken {
  subject: string;
  /** The token's audiences; withTailnet refuses the token unless one is `resource`. */
  audience: readonly string[];
  login?: string;
  clientId?: string;
  scopes?: readonly string[];
}

export interface PublicOAuthOptions {
  /**
   * This MCP server's canonical URL as hosted clients call it, e.g.
   * `https://mcp.<tailnet>.ts.net/mcp`. Tokens must name it in their
   * audience. It is configured, never taken from the request's Host.
   */
  resource: string;
  /** Authorization server issuer URLs for the protected resource metadata (RFC 9728). */
  authorizationServers: string[];
  /** Optional `scopes_supported` for the metadata. */
  scopes?: string[];
  /**
   * verify checks a bearer token and returns who it is for, or null to
   * refuse it (401). Throwing fails the request (500); it is not treated
   * as a valid token.
   */
  verify(token: string, req: Request, env: any, ctx: any): Promise<VerifiedToken | null> | VerifiedToken | null;
}

const PRM = "/.well-known/oauth-protected-resource";

/** checkPublicOAuth validates the options once, when withTailnet is called. */
export function checkPublicOAuth(o: PublicOAuthOptions): { resource: URL; metadataPaths: string[]; metadataURL: string } {
  if (!o || typeof o.verify !== "function") throw new TypeError("withTailnet: publicOAuth.verify is required");
  const resource = httpsURL(o.resource, "publicOAuth.resource");
  if (!Array.isArray(o.authorizationServers) || o.authorizationServers.length === 0) {
    throw new TypeError("withTailnet: publicOAuth.authorizationServers needs at least one issuer URL");
  }
  for (const s of o.authorizationServers) httpsURL(s, "publicOAuth.authorizationServers");
  // RFC 9728 §3.1: the metadata for https://h/p lives at
  // https://h/.well-known/oauth-protected-resource/p. MCP clients also try
  // the root form.
  const p = resource.pathname === "/" ? "" : resource.pathname.replace(/\/$/, "");
  const metadataPaths = p ? [PRM + p, PRM] : [PRM];
  return { resource, metadataPaths, metadataURL: resource.origin + metadataPaths[0] };
}

function httpsURL(s: string, what: string): URL {
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    throw new TypeError(`withTailnet: ${what} must be an absolute https URL, got ${JSON.stringify(s)}`);
  }
  if (u.protocol !== "https:" || u.search || u.hash || u.username || u.password || /["\\\s]/.test(s)) {
    throw new TypeError(`withTailnet: ${what} must be an https URL without query, fragment or credentials, got ${JSON.stringify(s)}`);
  }
  return u;
}

/** protectedResourceMetadata is the RFC 9728 document. */
export function protectedResourceMetadata(o: PublicOAuthOptions, resource: URL): Response {
  const body: Record<string, unknown> = {
    resource: resource.href,
    authorization_servers: o.authorizationServers,
    bearer_methods_supported: ["header"],
  };
  if (o.scopes?.length) body.scopes_supported = o.scopes;
  return Response.json(body, { headers: { "cache-control": "max-age=300" } });
}

/** bearerToken returns the token of an `Authorization: Bearer` header, or null. */
export function bearerToken(req: Request): string | null {
  const m = /^Bearer +([A-Za-z0-9\-._~+/]+=*) *$/i.exec(req.headers.get("authorization") ?? "");
  return m ? m[1] : null;
}

/** unauthorized is the RFC 6750 401 that starts a client's OAuth flow. */
export function unauthorized(metadataURL: string, invalid: boolean): Response {
  const challenge = `Bearer resource_metadata="${metadataURL}"` + (invalid ? `, error="invalid_token"` : "");
  return Response.json(
    invalid ? { error: "invalid_token", error_description: "the bearer token is not valid for this server" } : { error: "unauthorized", error_description: "a bearer token is required" },
    { status: 401, headers: { "www-authenticate": challenge } },
  );
}

/** acceptToken checks verify()'s result against the resource; null refuses it. */
export function acceptToken(t: VerifiedToken | null | undefined, resource: URL): VerifiedToken | null {
  if (!t || typeof t.subject !== "string" || t.subject === "") return null;
  if (!Array.isArray(t.audience) || !t.audience.some((a) => sameResource(a, resource))) return null;
  return t;
}

function sameResource(a: unknown, resource: URL): boolean {
  if (typeof a !== "string") return false;
  try {
    const u = new URL(a);
    const strip = (s: string) => s.replace(/\/$/, "");
    // URL parsing lowercases the scheme and host (RFC 8707 compares them case-insensitively).
    return u.origin === resource.origin && strip(u.pathname) === strip(resource.pathname) && !u.search && !u.hash;
  } catch {
    return false;
  }
}

export interface IntrospectionOptions {
  /** The authorization server's RFC 7662 endpoint, e.g. `https://idp.<tailnet>.ts.net/introspect`. */
  endpoint: string;
  /**
   * Extra headers, e.g. `authorization: Basic …` for a server that wants
   * client credentials. tsidp needs none when called over the tailnet: it
   * identifies the calling node with WhoIs.
   */
  headers?: Record<string, string>;
  /** fetch to use (default: the global fetch). */
  fetch?: typeof fetch;
}

/**
 * introspectionVerifier returns a verify() that asks an RFC 7662 token
 * introspection endpoint (tsidp's `/introspect`, or any provider's) about
 * each token. An inactive, expired or not-yet-valid token is refused; an
 * unreachable endpoint or a non-200 answer throws.
 */
export function introspectionVerifier(o: IntrospectionOptions): PublicOAuthOptions["verify"] {
  const f = o.fetch ?? ((input: RequestInfo | URL, init?: RequestInit) => fetch(input, init));
  return async (token: string) => {
    const r = await f(o.endpoint, {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json", ...(o.headers ?? {}) },
      body: new URLSearchParams({ token, token_type_hint: "access_token" }).toString(),
    });
    if (r.status !== 200) {
      const tw = r.headers.get("tw-error");
      throw new Error(`token introspection: ${o.endpoint} answered ${r.status}${tw ? ` (tw-error: ${tw})` : ""}`);
    }
    const j: any = await r.json();
    if (j?.active !== true) return null;
    const now = Math.floor(Date.now() / 1000);
    if (typeof j.exp === "number" && j.exp <= now) return null;
    if (typeof j.nbf === "number" && j.nbf > now + 60) return null;
    if (typeof j.sub !== "string" || j.sub === "") return null;
    const aud = typeof j.aud === "string" ? [j.aud] : Array.isArray(j.aud) ? j.aud.filter((a: unknown) => typeof a === "string") : [];
    const out: VerifiedToken = { subject: j.sub, audience: aud };
    if (typeof j.username === "string") out.login = j.username;
    if (typeof j.client_id === "string") out.clientId = j.client_id;
    if (typeof j.scope === "string") out.scopes = j.scope.split(" ").filter(Boolean);
    return out;
  };
}
