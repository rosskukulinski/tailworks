---
kind: breaking
upgrade: [protocol, cli-json, format, schema-contract, workerd, config-default]
---
twd is now **tailworks** (issue #86). The project, the repo
(`github.com/rosskukulinski/tailworks`), the Go module, the npm packages
(`@tailworks/tailnet`, `@tailworks/node`, `@tailworks/mcp`,
`npm create tailworks`), the install root (`~/.tailworks`), the per-app
config (`tailworks.jsonc`, top-level key `"tw"`) and the release tarballs
(`tailworks-<version>-<os>-<arch>.tar.gz`) use the long name. The command is
`tw` (`install.sh` also links `tailworks` → `tw`), and everything else uses
the short name: the daemons (`tw-mgmt`, `tw-hostd`, `tw-netd`,
`tw-vmhelper`, `tw-vz`, `tw-guestd`), env vars (`TW_*`), tags (`tag:tw`,
`tag:tw-mgmt`, `tag:tw-host`, `tag:tw-public`, `tag:tw-wl-*`), the Service
`svc:tw-mgmt`, node hostnames (`tw-`), headers (`x-tw-*`, `Tw-Error`,
`Tw-Actor`, `Tw-MCP-Tools`), metrics (`tw_*`), the launchd label
`dev.tailworks.up` and the systemd units `tw.service` and
`tw-vmhelper.service`. Peer capabilities move from `twd.dev/cap/*` to
`github.com/rosskukulinski/tailworks/cap/*`.

There is no upgrade path from twd 0.x: nothing reads `~/.twd`,
`twd.jsonc`, the old env vars, tags or headers.

Operator action: tear down and reinstall. On each machine, run
`twd down --purge --service`, remove any hand-made agents
(`dev.twd.hostd`, `twd-vmhelper.service`) and delete `~/.twd`. In the
admin console, remove the `tag:twd*` devices, then the `tag:twd*` tag
owners, `svc:twd-mgmt` and the `twd.dev/cap/*` grants; revoke the OAuth
client and create one scoped to `tag:tw`. Rename each app's `twd.jsonc` to
`tailworks.jsonc` (key `"tw"`) and its `@twd/*` imports to
`@tailworks/*`. Then install with `get.sh` and run `tw doctor`.
