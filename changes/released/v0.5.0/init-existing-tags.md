---
kind: feature
upgrade: []
---
`tw init` can now use tags your tailnet policy already has, instead of
naming new ones after the app. `--tags tag:tw-app` sets the workload
node's tags, and `--object-tags tag:tw-agent` sets the tags of every
Durable Object class with its own node (`--object-tags <Class>=tag:a` sets
one class). A new app then needs no policy change, which helps when someone
else manages the policy file. `tw init` checks the tags before it writes
anything, and refuses tailworks' own tags (`reserved_tag`).
