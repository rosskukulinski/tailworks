---
kind: feature
upgrade: [config-default]
---
`tw dev` honours the wrangler config's `dev.port`, as `wrangler dev`
does (#473). The port is `--port`, else `dev.port`, else 8787. A port you
chose fails when it is busy; when you chose none and 8787 is busy,
`tw dev` takes a free port and prints one line naming both. `dev.ip`,
`dev.local_protocol` and `dev.host` stay ignored: `tw dev` serves plain
HTTP on 127.0.0.1 only. The busy-port error now also suggests
`tw dev --port 0`.
