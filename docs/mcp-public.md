# Use your MCP server from claude.ai or ChatGPT

An MCP server on tailworks is tailnet-only: a client on your laptop reaches
it over WireGuard, and its tools are authorized by tailnet grants. Hosted
assistants (claude.ai, ChatGPT and other connectors) call MCP servers from
their own cloud, so they need a **public** URL that asks for **OAuth**. This
recipe serves one MCP endpoint to the internet over
[public ingress](public-ingress.md), with
[tsidp](https://github.com/tailscale/tsidp) as the OAuth authorization
server, so the token is issued to **your tailnet identity**. Every other
path of the app, and every other app, stays tailnet-only.

It's for a person's own hosted assistant. It isn't for untrusted agents:
see [what a public caller doesn't have](#what-a-public-caller-doesnt-have).

- [How it fits together](#how-it-fits-together)
- [1. Run tsidp with Funnel](#1-run-tsidp-with-funnel)
- [2. Tailnet policy](#2-tailnet-policy)
- [3. Register the hosted client in tsidp](#3-register-the-hosted-client-in-tsidp)
- [4. The MCP server](#4-the-mcp-server)
- [5. Add the connector](#5-add-the-connector)
- [What a public caller doesn't have](#what-a-public-caller-doesnt-have)
- [Turning it off](#turning-it-off)
- [Another OAuth provider](#another-oauth-provider)
- [Troubleshooting](#troubleshooting)

## How it fits together

The names below assume the workload is called `mcp` and tsidp runs as
`idp`; `<tailnet>` is your tailnet's MagicDNS suffix.

1. The hosted client calls `https://mcp.<tailnet>.ts.net/mcp` without a
   token and gets `401` with
   `WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource/mcp"`.
2. It reads that protected resource metadata
   ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)), which names
   `https://idp.<tailnet>.ts.net` as the authorization server, and reads
   tsidp's `/.well-known/oauth-authorization-server`.
3. It sends **your browser** to tsidp's `/authorize`. tsidp answers
   `/authorize` only over the tailnet, never over Funnel, and only for a
   device that belongs to a user (not a tagged node). So **the browser you
   connect from must be on your tailnet**: Tailscale running, signed in as
   you. tsidp identifies you with WhoIs; there's no password.
4. tsidp redirects back to the client, which swaps the code for a token at
   tsidp's `/token` over Funnel, with its client secret, PKCE and the MCP
   server's URL as the `resource` ([RFC 8707](https://www.rfc-editor.org/rfc/rfc8707)).
5. The client calls `/mcp` with `Authorization: Bearer <token>`. The Worker
   asks tsidp's `/introspect` about the token **over the tailnet**, checks
   that the token names this server's URL in its audience, and runs the
   tool with a public caller that carries your subject and login.

| Endpoint | Over Funnel (internet) | Over the tailnet |
|---|---|---|
| `mcp`: `/mcp`, `/.well-known/oauth-protected-resource/*` | yes, with a valid token for `/mcp` | yes, as before (tailnet identity, grants) |
| `mcp`: every other path | `404 not_public` | yes |
| tsidp `/authorize` | refused by tsidp | yes, for user-owned devices |
| tsidp `/token`, metadata, JWKS | yes | yes |
| tsidp `/introspect` | only with a client's credentials | yes (the caller is identified by WhoIs) |
| tsidp admin UI, client registration | refused by tsidp | with a grant (below) |

tsidp's access tokens are opaque and last 5 minutes. Its refresh tokens last
30 days and are single-use, and tsidp refuses a refresh unless the device
you signed in from is still the same untagged node owned by you with an
unexpired key.

## 1. Run tsidp with Funnel

Run tsidp on any machine that can keep its state directory (Docker shown,
as in [tsidp's README](https://github.com/tailscale/tsidp#readme)):

```yaml
# compose.yaml
services:
  tsidp:
    image: ghcr.io/tailscale/tsidp:latest
    volumes:
      - tsidp-data:/data
    environment:
      - TAILSCALE_USE_WIP_CODE=1   # tsidp is pre-1.0
      - TS_STATE_DIR=/data         # keeps its node key and registered clients
      - TS_HOSTNAME=idp            # https://idp.<tailnet>.ts.net
      - TSIDP_USE_FUNNEL=1         # serve /token and the metadata to the internet
      - TS_AUTHKEY=tskey-client-…  # an OAuth client secret with auth_keys write…
      - TS_ADVERTISE_TAGS=tag:tsidp # …needs a tag
volumes:
  tsidp-data:
```

`docker compose up -d`, then open `https://idp.<tailnet>.ts.net/` from a
tailnet device once the policy below is in place. The first certificate
takes a minute or two.

## 2. Tailnet policy

```jsonc
"tagOwners": {
  "tag:tsidp": ["autogroup:admin"]
},
"nodeAttrs": [
  // tsidp serves over Funnel. (tw deploy prints the tag:tw-public entries it needs.)
  { "target": ["tag:tsidp"], "attr": ["funnel"] }
],
"grants": [
  // You: reach tsidp, use its admin UI, and get tokens for the MCP server.
  {
    "src": ["alice@example.com"],
    "dst": ["tag:tsidp"],
    "ip": ["tcp:443"],
    "app": {
      "tailscale.com/cap/tsidp": [{
        "allow_admin_ui": true,
        "users": ["alice@example.com"],
        "resources": ["https://mcp.<tailnet>.ts.net/mcp"]
      }]
    }
  },
  // The MCP workload: introspect tokens at tsidp.
  { "src": ["tag:tw-wl-mcp"], "dst": ["tag:tsidp"], "ip": ["tcp:443"] }
]
```

- **`resources`** is what makes this per-user. At `/token`, tsidp checks the
  client's `resource` against the `resources` of the grants that apply to
  the device you signed in from, for a login in `users`. A user without a
  matching entry gets no token for the MCP server, whatever client they use.
- **`allow_admin_ui`** is only needed by whoever registers clients
  (step 3). Drop it afterwards if you like.
- tsidp's capability schema is still pre-1.0 and may change; its README has
  the current fields.

## 3. Register the hosted client in tsidp

tsidp's `/authorize` accepts only clients it knows, and a hosted client
can't register itself here:

- **Dynamic client registration** is served only over the tailnet (and
  needs `allow_dcr`), so a client calling from the internet can't use it.
- **Client ID Metadata Documents**, the MCP authorization spec's way for a
  client to use an HTTPS URL as its `client_id` with no registration, aren't
  supported by tsidp: an unknown `client_id` is refused.

So register the client by hand:

1. Open `https://idp.<tailnet>.ts.net/` from your tailnet device and add a
   client (**`/new`**).
2. Redirect URI: the callback URL the hosted client documents. For claude.ai
   it is `https://claude.ai/api/mcp/auth_callback`. tsidp compares it
   exactly.
3. Keep the **client ID** and **client secret** it shows for step 5.

## 4. The MCP server

Start from the `mcp` template (`tw init mcp --template mcp`) and change three
files. Public OAuth needs `@tailworks/mcp` with `publicOAuth` and
`requester()` (the first release after 0.5.0).

```jsonc
// tailworks.jsonc
{
  "$schema": "https://raw.githubusercontent.com/rosskukulinski/tailworks/master/schema/tailworks.schema.json",
  "public": {
    // only these reach your code from the internet; everything else stays tailnet-only
    "routes": ["/mcp", "/.well-known/oauth-protected-resource/*"]
  },
  "tailnet": {
    "tags": ["tag:tw-wl-mcp"],
    "connects": ["tag:tsidp:443"], // token introspection; tw deploy preflights the grant on tcp:443
    "objects": { "TwBudget": { "node": false } }
  }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "mcp",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01",
  "compatibility_flags": ["nodejs_compat"],
  "durable_objects": { "bindings": [{ "name": "BUDGET", "class_name": "TwBudget" }] },
  "migrations": [{ "tag": "v1", "new_sqlite_classes": ["TwBudget"] }],
  "vars": {
    "MCP_CAP": "example.com/cap/mcp",
    // the URL hosted clients call; tokens must be issued for exactly this
    "MCP_RESOURCE": "https://mcp.<tailnet>.ts.net/mcp",
    "TSIDP_URL": "https://idp.<tailnet>.ts.net",
    // who may use the public endpoint (Tailscale login names), comma-separated
    "PUBLIC_USERS": "alice@example.com"
  }
}
```

```ts
// src/index.ts
import { createMcpHandler } from "agents/mcp/server";
import { McpServer } from "@modelcontextprotocol/server";
import { z } from "zod";
import { withTailnet, requester, authorizeTool, introspectionVerifier, mcpError } from "@tailworks/mcp";
export { TwBudget } from "@tailworks/mcp/budget-do";

let ENV: Env;

// allowed checks a tool call: tailnet callers by their grant, as in the
// template; public (OAuth) callers by an allowlist of logins.
function allowed(tool: string) {
  const who = requester();
  if (who.kind === "tailnet") return authorizeTool(who, ENV.MCP_CAP, { tool });
  const users = ENV.PUBLIC_USERS.split(",").map((s) => s.trim());
  if (!who.login || !users.includes(who.login)) throw mcpError("cap_missing", `tool "${tool}" is not allowed for ${who.login ?? who.subject}`);
}

const server = () => {
  const s = new McpServer({ name: "orders", version: "1.0.0" });
  s.registerTool("whoami", { description: "Who this server thinks you are", inputSchema: {} }, async () => {
    const who = requester();
    const me = who.kind === "public" ? { via: "public", login: who.login, subject: who.subject, client: who.clientId } : { via: "tailnet", node: who.node, tags: who.tags };
    return { content: [{ type: "text" as const, text: JSON.stringify(me) }] };
  });
  s.registerTool("lookup_order", { description: "Look up an order", inputSchema: { order: z.string() } }, async ({ order }) => {
    allowed("lookup_order");
    return { content: [{ type: "text" as const, text: JSON.stringify({ order, status: "shipped" }) }] };
  });
  return s;
};

let mcp: ReturnType<typeof withTailnet> | undefined;

export default {
  fetch(req: Request, env: Env, ctx: any) {
    ENV = env;
    mcp ??= withTailnet(createMcpHandler(server, { route: "/mcp" }), {
      path: "/mcp",
      publicOAuth: {
        resource: env.MCP_RESOURCE,
        authorizationServers: [env.TSIDP_URL],
        // over the tailnet: tsidp identifies this workload's node with WhoIs, no client secret
        verify: introspectionVerifier({ endpoint: `${env.TSIDP_URL}/introspect` }),
      },
    });
    return mcp.fetch(req, env, ctx);
  },
};
```

Run `tw types` after editing `wrangler.jsonc`, then `tw deploy`. It prints
the `tag:tw-public` policy entries to add if they're missing, and the public
URL (`tw deploy --wait-public 10m` waits until it answers from the
internet).

What `withTailnet`'s `publicOAuth` does:

- **Serves the metadata.** `GET /.well-known/oauth-protected-resource/mcp`
  (and the root form) returns `resource` and `authorization_servers`, to
  the internet and the tailnet alike.
- **Challenges.** A public request to `/mcp` without a bearer token gets
  `401` with `WWW-Authenticate: Bearer resource_metadata="…"`; a token that
  `verify` refuses, or whose audience doesn't include `resource`, gets
  `401` with `error="invalid_token"`. If `verify` throws (tsidp
  unreachable), the request fails; it is never let through.
- **Checks the audience.** The token must name `resource` (scheme, host
  and path; a trailing slash is ignored). `resource` comes from your
  config, never from the request's `Host`. A token tsidp issued for
  another server is refused, so a client can't replay one here.
- **Tells tools who called.** `requester()` returns
  `{kind: "public", subject, login, clientId, scopes, ip, traceId}` for
  such a request, and `{kind: "tailnet", ...Caller}` for a tailnet one.
  For tsidp, `subject` is your Tailscale user ID and `login` your login
  name.
- **Leaves tailnet code fail-closed.** `caller()` and `authorizeTool()`
  throw `public_request` on a public request, so a tool written for
  tailnet grants refuses public callers until you handle `kind: "public"`.
  A tailnet request is never asked for a token.

**Try it under `tw dev`.** `tw dev` has no Funnel, so every request is a
tailnet request: the tools work through your tailnet identity, and you can
read the metadata:

```sh
curl http://127.0.0.1:8787/.well-known/oauth-protected-resource/mcp
# {"resource":"https://mcp.<tailnet>.ts.net/mcp","authorization_servers":["https://idp.<tailnet>.ts.net"],"bearer_methods_supported":["header"]}
```

In tests, `withPublic(req)` from `@tailworks/tailnet/testing` makes a
request public, so you can check the `401` and a stub `verify`
([testing.md](testing.md)).

## 5. Add the connector

From a device on your tailnet:

- **claude.ai:** Settings › Connectors › add a custom connector. URL
  `https://mcp.<tailnet>.ts.net/mcp`; under the advanced settings, the
  client ID and secret from step 3. Connect: your browser goes to
  `idp.<tailnet>.ts.net`, comes straight back (tsidp knows you from the
  tailnet), and the connector lists the tools.
- **ChatGPT and other hosted clients:** the same URL. The client must let
  you enter a pre-registered client ID and secret, and you register its
  callback URL in step 3; a client that only does dynamic registration or
  Client ID Metadata Documents can't use tsidp today (use
  [another provider](#another-oauth-provider)).

Ask the assistant to call `whoami`: it answers with `"via": "public"` and
your login. After sign-in the client refreshes the token from its own
cloud, so a mobile app that uses the connector you set up on the web
doesn't need Tailscale. Signing in again needs a browser on the tailnet:
after 30 days, when the device you signed in from leaves the tailnet, or
after tsidp restarts (it keeps tokens in memory).

## What a public caller doesn't have

- **No tailnet identity.** `caller()` throws; there's no node, tags or
  `user` from WhoIs. `subject` and `login` come from the token, and they
  are only as good as the authorization server and your `verify`.
- **No grants.** Tailnet app capabilities (`authorizeTool`, limits such as
  `maxUsd`) don't apply, and `budget()` needs an explicit `key` (for
  example `budget(env.BUDGET, who, tool, { key: "oauth:" + who.subject })`).
  Gate each tool on `subject` or `login` yourself; keep the dangerous ones
  tailnet-only.
- **No per-node kill.** `tw kill` acts on tailnet nodes; the hosted client
  isn't one. Use [Turning it off](#turning-it-off).
- **No new objects.** Public requests can't create Durable Objects unless
  a class opts in ([public-ingress.md](public-ingress.md#objects-and-spawn-amplification)).
- **The limits of public ingress apply:** per-IP and per-workload rates,
  1 MiB bodies, 30 s per request (raise `public.limits.timeoutSeconds` for
  slow tools), and every request is audited (`public_request`)
  ([public-ingress.md](public-ingress.md#limits)).
- **The hosted client holds the token.** Anything it does with your tools
  is done as you. Give the public endpoint the tools you'd let that
  assistant run unattended.

## Turning it off

- **Now, for everyone:** `tw expose off mcp` closes the public listener and
  resets in-flight connections; the tailnet endpoint keeps working.
  `tw expose on mcp` reopens it.
- **For one person:** remove their login from `PUBLIC_USERS` (next deploy),
  or from the tsidp grant's `users`/`resources` so tsidp stops issuing
  them tokens for this server. Removing or expiring the device they signed
  in from stops their refreshes, so access ends within one access-token
  lifetime (5 minutes).
- **For good:** remove `"public"` from `tailworks.jsonc` and redeploy; the
  public node is deleted. The name stays yours until
  `tw expose release mcp` ([public-ingress.md](public-ingress.md#name-ownership)).

## Another OAuth provider

Any OAuth 2.1 / OIDC provider that issues access tokens with an audience
(RFC 8707 `resource`) works the same way; only `authorizationServers` and
`verify` change. With a provider that supports Client ID Metadata
Documents, step 3 goes away.

- **Introspection** (RFC 7662): `introspectionVerifier({ endpoint, headers })`,
  with the client credentials the provider wants in `headers`
  (`authorization: "Basic …"`), kept in a [secret](secrets.md). The
  provider is on the internet, so the Worker's egress must allow it
  (`tailnet.egress.internet`).
- **JWT access tokens:** write `verify` with a JWT library: check the
  signature against the provider's JWKS, `iss`, `exp`, and return
  `{subject: sub, audience: aud, login}`. withTailnet checks `audience`
  against `resource`.

What you lose against tsidp: the sign-in is the provider's, not your
tailnet identity, so `login` is whatever the provider says, and a device
off the tailnet can sign in.

## Troubleshooting

- **The connector says the server needs no auth, or fails before
  sign-in:** `curl -i https://mcp.<tailnet>.ts.net/mcp -X POST` from off
  the tailnet should be `401` with `WWW-Authenticate`. `404 not_public`
  means `/mcp` isn't in `public.routes`; `403 public_request` means
  `publicOAuth` isn't set.
- **`not allowed over funnel` from tsidp:** the browser that opened the
  sign-in isn't on the tailnet. Turn Tailscale on and connect again.
- **`tagged node doesn't have a user identity`:** you signed in from a
  tagged device (a server, a shared kiosk). Use a device you own.
- **`invalid client ID` or `redirect_uri mismatch`:** the client ID or the
  callback URL doesn't match what you registered in step 3.
- **`invalid resource` at `/token`:** the `resource` the client sent isn't
  in the tsidp grant's `resources` for your login. It must equal
  `MCP_RESOURCE`.
- **Every call is `401 invalid_token` after sign-in:** the token's audience
  doesn't include `MCP_RESOURCE`: the client sent no `resource`, or a
  different URL (check the path).
- **Every call fails with `500`:** the Worker can't introspect. `tw logs mcp`
  shows `token introspection: … answered 403 (tw-error: peer_not_visible)`
  when the `tag:tw-wl-mcp` → `tag:tsidp` grant is missing.
