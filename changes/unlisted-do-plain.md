---
kind: breaking
upgrade: [config-default]
---
A Durable Object class that `tailworks.jsonc` leaves out of `tailnet.objects` is now internal (`"node": false`): a plain Durable Object, as on Cloudflare, with `idFromName`, `get(id)` and `newUniqueId`, and its storage under `<Class>`. This is what a project with only a wrangler config already got; before, any `tailworks.jsonc` (even one with only `tailnet.tags`) turned every unlisted class into a facet with no node, its storage under `Host_<Class>`. `tw` applies the default when it packs the app, so an app deployed by an older `tw` keeps running as it was until you redeploy it. `tw deploy` now says that such classes share the workload node. The `mcp` template no longer lists `TwBudget`. An unlisted class's `ctx.tailnet.connect()` now needs the class listed. With no node, an unlisted class needs no create admission, so Agents SDK apps that reach their agents through the imported `env` (Cloudflare's `cross-domain` and `mcp-elicitation` examples) now run unchanged.

Operator action: before redeploying an app with an unlisted class whose objects store data, list the class in `tailnet.objects` with `tags` to keep it in facet mode and keep its `Host_<Class>` storage; otherwise its objects start empty.
