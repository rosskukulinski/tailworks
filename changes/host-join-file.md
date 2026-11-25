---
kind: feature
upgrade: [protocol]
---
A new host needs no flags: the join file `tw host add` writes now also
carries the host ID, mgmt's URL, the control URL and (Headscale)
key-tags-only, so on the new machine `tw-hostd -join-token-file
<id>.join.env` is the whole command. tw-hostd keeps those settings beside
the node state (`<data-dir>/tsnet/join.env`, 0600, no secrets) after the
join, so later starts and restarts need no flags either. Per setting, a
flag wins, then the join file, then the saved settings; the control URL
defaults to Tailscale's only for a node that hasn't joined yet (a node with
state and no known control URL still refuses to start). `tw-hostd join
--token-file` reads the same file. `tw host add` prints the one-command
runbook and a systemd hint.

The mgmt API's `POST /v1/hosts` answer has two new optional fields,
`mgmtURL` and `keyTagsOnly`, so the join file is right when `tw host add`
runs on a linked machine. `tw host add --json` keeps every field. A join
file from an older `tw host add` still works with the flags as before.
