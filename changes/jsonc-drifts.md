---
kind: fix
upgrade: [config-default]
---
`tailworks.jsonc` fixes. `tailnet.egress.via: "exit-node"` is accepted by
`tw dev` and `tw deploy`: the schema refused it as an unknown key, though
hostd and mgmt already read it. A deploy with it still fails with
`feature_unavailable` until a host offers exit-node egress.

`public.limits`: an unset `burst` (`ipBurst`) now follows `rps` (`ipRps`):
two seconds' worth of the rate, never below the default (100, or 20 per
client IP) and never above the ceiling. A deployed workload with `rps`
above 50 (or `ipRps` above 10) and no burst gets a larger burst when its
mgmt is upgraded; nothing gets a smaller one.

The wrangler templates no longer repeat `name` in `tailworks.jsonc`: the
wrangler config's `name` names the workload. When both files set a name and
they differ, `tw deploy` and `tw dev` print a note saying which wins
(tailworks.jsonc, as before). They also note a workload tag
`tag:tw-wl-<other>` named after a different app, as a copied and renamed
app has, since it shares that app's grants.

Schema and docs: `ttl` is a lifetime from creation; image pins are refs you
write in `tailworks.jsonc`, not something `tw deploy` writes; the
Cloudflare placement hints `host`, `hostname` and `region` are marked
deprecated in the schema (they still work); an internal class needs only
`"node": false`.
