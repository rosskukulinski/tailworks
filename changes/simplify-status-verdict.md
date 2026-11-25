---
kind: feature
upgrade: [cli-json, config-default]
---
`tw status` now leads with the verdict: `tw status · healthy · <state dir> ·
tailnet <tailnet>`. The mint mode and transport moved out of the header
(they are still in `--json` and `tw doctor`). The mgmt line shows the mint
queue, pending device deletes and warm keys in custody only when they are
not zero. The host line counts objects (`3 objects`) and breaks them down
only when some aren't ready (`3 objects (2 ready, 1 pending)`). The Worker
internet line is printed only when it warns or the path isn't `direct`. On
a linked machine, `tw status` counts each tenant's deployments and lists
only the ones with an error or not active; `tw ls` lists them all, and now
shows each deployment's pinned container images.

`tw up` and `tw status` now say, every time, which `tw up` flags tw.json
keeps for every later `tw up` and `tw upgrade` restart. The unsafe ones
(`--insecure-shared-host`, `--allow-experimental-flags`, `--local-dev`) go
on a `! unsafe` line, `--ephemeral` and `--allow-sibling-egress` on a
`› saved` line. `tw status` also flags a running host whose
`--insecure-shared-host` differs from tw.json.

`--json`: `tw up` and `tw status` (on the host) have a new `unsafeFlags`
list (`[]` when none). On a linked machine, `tw status --json` now has
`stateDir` and matches `$defs/status`, which documents both field sets
(`linked` tells them apart).
