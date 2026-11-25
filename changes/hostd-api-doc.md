---
kind: fix
upgrade: [protocol]
---
The host agent's API has a current contract,
`docs/contracts/hostd-api.md` (it replaces
the v0-era `hostd-additions.md`): one table of tw-hostd's routes with the
surface each is served on (local socket, the host's tailnet push port,
in-process), who calls it (mgmt or which `tw` command) and what it does.

Operator action: none. No route moved or changed; the `api/openapi.yaml`
change is description text only (it points at the new file).
