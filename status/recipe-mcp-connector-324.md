# recipe-mcp-connector-324 (issue #324)

Recipe and SDK support for using one tailworks MCP server from hosted
assistants (claude.ai / ChatGPT connectors) over public ingress, with OAuth
tokens from tsidp, everything else tailnet-only.

## Done

- `docs/mcp-public.md`: the recipe (tsidp with Funnel, policy, client
  registration, the app's three files, connector setup, what a public caller
  lacks, turning it off, other providers, troubleshooting). Linked from
  recipes.md, docs/README.md, llms.txt, public-ingress.md, the mcp package
  README and the mcp template README.
- `@tailworks/mcp` (`js/packages/mcp/src/oauth.ts`, small edits to
  `index.ts`): `withTailnet` option `publicOAuth`, `requester()`,
  `introspectionVerifier()`. `caller()`/`authorizeTool()` throw
  `public_request` on a public request (fail closed). The answer to the
  issue's question 3: an extension was needed, because `withTailnet` refused
  every public request and tools could only see tailnet callers.

## Findings (tsidp, read from github.com/tailscale/tsidp master, 2026-11-25)

- `/authorize` refuses Funnel requests and tagged nodes; the browser must be
  on the tailnet. `/token` works over Funnel with the client secret.
- No Client ID Metadata Document support: `/authorize` accepts only
  registered client IDs. DCR (`/register`) is refused over Funnel, so a
  hosted client must be registered by hand in the admin UI.
- Access tokens are opaque (5 min), validated with `/introspect`; over the
  tailnet any node is an authenticated introspection client (WhoIs).
  `aud` = client ID + RFC 8707 resources, gated per user by the
  `tailscale.com/cap/tsidp` grant's `users`/`resources`.
- Refresh tokens: 30 days, single use, in memory, re-checked against the
  original untagged device.

## Tests

- `node --test js/packages/mcp/test/*.test.js`: 13 pass (8 new in oauth.test.js).
- `KEEP=1 sh scripts/templates-typecheck.sh mcp`: ok; then the recipe's
  three files over that app: `tw types`, `tsc --noEmit` ok; `tw dev`:
  metadata served at both RFC 9728 paths, `whoami` answers `via: tailnet`.

- After merging origin/master: `make lint` ok; `make test` (on mbp) ok;
  `make lint-docs test-docs` ok; `make templates-typecheck` ok (all
  templates); `make test-sdk` ok.

## Not verified

- The real flow (tsidp over Funnel, claude.ai / ChatGPT connector, workload
  introspecting tsidp over the tailnet) needs a real tailnet with Funnel and
  a hosted client account; not run.
