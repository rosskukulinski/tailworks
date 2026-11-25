---
name: tw-scaffold
description: Use when creating a new tailworks app - a Worker, a Durable Object, a Cloudflare Agents SDK Agent, an MCP server, an AI SDK agent or a Node.js workflow - with `tw init`, when bringing an existing wrangler project to tailworks, or when editing an app's tailworks.jsonc or wrangler config.
---

# Scaffold a tailworks app

`tw init <dir> --template <template>` creates an app from a template. Pick
the template closest to what the user asked for:

| Template | What it is | Serves |
|---|---|---|
| `hello` | a plain Worker that returns its verified caller | `/` |
| `agent` | a Cloudflare Agents SDK `Agent`: each instance is a Durable Object with its own SQLite storage and its own tailnet node | `/agents/support-agent/<name>` |
| `mcp` | a stateless MCP server; each tool is allowed or denied by the caller's tailnet grant | `/mcp` |
| `static-site` | wrangler `assets`: the files in `public/` served next to a Worker that answers `/api/*` and checks the caller before serving `/team/` | `/` |
| `ai-agent` | a Vercel AI SDK agent (one per caller) that calls MCP tools over the tailnet; ships a scripted stand-in model, no LLM key; needs the `mcp` template deployed as workload `mcp` plus a cap grant (its README) | `/` |
| `node-workflow` | a Vercel Workflow on tailworks' Node.js process tier (needs Node.js 24+ and `npm install`) | `/` |

<!-- tw-eval: scaffold -->
```sh
tw init hello-mcp --template mcp --json      # {"dir", "template", "workload"}
cd hello-mcp
```

- The directory must not exist or must be empty.
- The workload is named after the directory (`--name <workload>` to pick
  another). Names are lowercase letters, digits and `-`.
- `tw init` renames the template's tags after the app:
  `tag:tw-wl-<workload>` for the workload node and
  `tag:tw-<workload>-<class>` for each Durable Object class. Each new tag
  needs a `tagOwners` line in the user's tailnet policy before the first
  deploy (`tw deploy` prints it; see `tw-policy`). For tags the user
  already owns, `--tags` sets the workload's and `--object-tags
  [<Class>=]<tag>` the classes'.

## The files

- `src/index.ts`: the Worker. `getCaller(req)` (from `@tailworks/tailnet`)
  returns the verified caller: `nodeId`, `node`, `tags`, `user`. Use it
  instead of any token or API-key check.
- `wrangler.jsonc`: the Cloudflare config. Tailworks reads `wrangler.json`,
  `wrangler.jsonc` or `wrangler.toml`, the first it finds, like wrangler,
  and prints a warning for each setting it ignores (bindings it has no
  equivalent for, `routes`, `site`, ...). `--env <name>` on `tw dev` and
  `tw deploy` selects `[env.<name>]` as wrangler does.
- `tailworks.jsonc`: tailworks' own settings. It's optional: without it the
  workload's tag is `tag:tw-wl-<workload>` and Durable Object classes get
  no nodes of their own. Tailworks refuses keys it doesn't know, so check
  `tw docs show tailworks-jsonc.md` before you add one. The ones you'll use
  most:

```jsonc
{
  "name": "hello-mcp",
  "tailnet": {
    "tags": ["tag:tw-wl-hello-mcp"],        // the workload node's tags (this is the default)
    "connects": ["tag:db"],                   // what it calls; deploy preflights these grants
    "objects": {
      // a Durable Object class whose instances each get their own node
      "Room": { "tags": ["tag:tw-hello-mcp-room"] }
    }
  }
}
```

## An existing wrangler project

No `tw init` needed: `cd` into it and run `tw dev`, then `tw deploy`.
Read the warnings they print; each one names a setting or binding that
won't work on tailworks. More: `tw docs show wrangler.md`.

## Things that don't work on tailworks

Check before you reach for a Cloudflare binding: KV, R2, D1, Queues,
Workflows, Workers AI, Vectorize, Hyperdrive, Browser Rendering, service
bindings have no binding on tailworks. Static assets (`assets`) work, with
`_redirects`, `_headers` and assets-only projects. Durable Object storage
(KV API and SQLite) works. Call other services over the tailnet with
`fetch("http://<magicdns-name>/...")`; the call leaves from the workload's
own node. The full table: `tw docs show COMPAT.md`.

Outbound `fetch()` reaches tailnet peers, and public internet hosts from
the host's network (on by default; `tailnet.egress.internet: false` turns
it off, a list limits it).

## Next

Run it locally with `tw dev` (`tw-dev`), then `tw deploy`
(`tw-deploy`).
