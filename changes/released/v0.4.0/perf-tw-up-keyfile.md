---
kind: fix
upgrade: []
---
`tw up` is faster: it no longer joins mgmt's tailnet node itself and then
closes it for tw-mgmt to open again. Each node start costs about a second
of round trips to Tailscale's control plane, and `tw up` now does one
(two with `--separate-host`) instead of two (three). On a Linux VM against
production control, `tw up --service` to ready went from 3.8 s to
about 2.5 s (single process) and from 5.4 s to 4.2–4.5 s
(`--separate-host`). On macOS, `tw up` went from about 3.1–3.5 s to about
1.8–2.0 s.

On the first start, `tw up` now hands mgmt its single-use join key in
`<state>/mgmt/join.env` (0600, in mgmt's 0700 state dir) instead of using
it in-process. mgmt deletes the file once it has read the key; a failed
start deletes it too, and so does `tw up` when it returns. The key is
pre-authorized, tagged `tag:tw-mgmt` and expires within 5 minutes. A
host's own join file already worked this way. If mgmt's join fails, `tw
up` reports control's reason and stops the supervisor (on a first
`tw up --service`, it also removes the service).
