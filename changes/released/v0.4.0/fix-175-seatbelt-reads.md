---
kind: security
upgrade: []
---
On macOS, the process-tier Seatbelt profile now allows reads only from the
OS dirs (`/System`, `/usr`, `/bin`, `/sbin`, `/etc`, `/opt`, `/dev`, the
time zone database), the runtime binary, the Node install prefix and the
workload's own dirs. Before, a workload could read anything your user can
read outside a deny list (`/tmp`, `/var/folders`, shared dirs, other
users' world-readable files). Its TCP rules are also IPv4-only now, so it
reaches its egress proxy and its own port on `127.0.0.1` but not on `[::1]`.

A Node app that reads files outside its own dirs (for example under `/tmp`
or `/Library`), or listens on `localhost` (which Node resolves to `::1`)
instead of `$HOST`, now gets `EPERM`. `tw-hostd -sandbox seatbelt-legacy`
still runs the old files-only profile for one release.
