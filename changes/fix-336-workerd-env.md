---
kind: security
upgrade: []
---
A process-tier workerd no longer inherits tw-hostd's environment. Under a
foreground `tw up` that environment is the operator's shell (cloud
credentials, API tokens, `SSH_AUTH_SOCK`); under `--service` it is the
service manager's. Tenant JavaScript could never read it, but code that
escapes V8 could, and egress is on by default. workerd now gets a list hostd
builds from nothing, as a Node app already did: only `TZ`, and only when
hostd has one, on every sandbox (Seatbelt, Landlock, none). `tw doctor`'s
workerd probe runs with the same list. The microVM guest already gave its
workerd a fixed environment and is unchanged.

Workers see one difference: with no `LANG`/`LC_*`, the default `Intl`
locale is `en-US` on every host (as on Cloudflare), where before it
followed the operator's shell. The time zone is unchanged: `TZ` is passed
through, and without it workerd reads the host's `/etc/localtime` as before.
