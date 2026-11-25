---
kind: security
upgrade: []
---
On Linux, the process-tier sandbox (Landlock) could not keep tenant code
out of tailworks' state or a tenant's credential file when an operator had
placed them under a directory the sandbox lets every workload read: `/usr`,
`/lib`, `/bin`, `/sbin`, `/etc`, `/opt`, `/nix/store` or
`/run/current-system` (for example `--state-dir /opt/tailworks`, or an
OAuth client file in `/etc/tailworks/`). Landlock has no deny rules, so
hostd's deny list was documentation there. Default installs (`~/.tailworks`,
`~/.config/tailworks/oauth.env`) were not affected; macOS was not affected.

tailworks now refuses such a layout with `sandbox_layout_unsafe`: `tw up`,
`tw tenant add|set`, `tw-hostd` at start and when it first serves a
tenant, and the sandbox launcher itself before it applies a profile whose
deny list it could not honour. `tw doctor`'s `sandbox` check probes the
real paths (state dir, run dir, every tenant's credential file, the home
dir) under the sandbox and names the one a workload could read, instead of
only a temp canary and the home dir.

Operator action: on a Linux host, if `tw up` or `tw doctor` now reports
`sandbox_layout_unsafe`, move the named path outside those directories
(`tw up --state-dir /var/lib/tailworks`; `tw tenant set <tailnet>
--oauth-client-file ~/.config/tailworks/oauth.env`) and restart. See
docs/errors.md#sandbox_layout_unsafe.
