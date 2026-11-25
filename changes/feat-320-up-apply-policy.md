---
kind: breaking
upgrade: [protocol]
---
`--apply-policy` is now one flag with one rule everywhere it exists (`tw
up`, `tw tenant add`, `tw deploy`; `tw policy --apply`): tailworks shows
the lines it will add to the tailnet policy, asks, and writes them. `--yes`
skips the question and is required without a terminal or with `--json`.

**Breaking:** `--policy-apply` is gone from `tw up` and `tw tenant add`;
the flag is `--apply-policy`. And both now ask before writing, on Headscale
too, where they used to write without asking.

`tw up --apply-policy` now works on Tailscale: when the OAuth client has
the **Policy File › Write** scope, `tw up` adds the `tagOwners` and grants
tailworks needs instead of printing them for you to paste. It only ever
adds, and writes with `If-Match`, retrying once if the policy changed in
the meantime. A tag that is already in `tagOwners` keeps its owners.
Without the write scope nothing is written: `tw up` says why and goes on as
before. One line still has to be pasted first: `"tag:tw":
["autogroup:admin"]`, because the admin console only lets you tag an OAuth
client with a tag that exists.

`tw deploy --apply-policy` and `tw policy --apply` now also write the grant
that lets people call a new app, so the first `curl` works without a paste:
`autogroup:member` (every user of the tailnet) to the app's own tag on port
80. It is in the lines shown before the question, with a sentence saying
what it does. It is written only together with a new app's tag: never for a
tag your policy already defines, never when a grant of yours already
reaches the app or every node, and not on Headscale. To choose the callers
yourself, add your own grant to the app's tag first, or deploy without
`--apply-policy`. The mgmt API's policy-apply answer has a new
`callerGrant` field.

Operator action: in scripts, replace `--policy-apply` with `--apply-policy
--yes` on `tw up` and `tw tenant add`. If you don't want every user of the
tailnet to reach new apps, don't pass `--apply-policy` to `tw deploy`
unattended, or add your own caller grant before the first deploy.
