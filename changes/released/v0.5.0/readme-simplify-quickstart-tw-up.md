---
kind: feature
upgrade: [config-default]
---
`tw up` needs no flags on a new machine. With the default state dir it reads
the OAuth client from `~/.config/tailworks/oauth.env`, and on the first run
asks for the Client Secret and saves it there (0600). It installs the login
service by default (`--service=false` to skip it); an existing state dir keeps
how it runs, so `tw upgrade` doesn't change it. It no longer asks for
confirmation (`--yes` is accepted and does nothing), and it tightens an OAuth
client file other users can read to 0600 instead of refusing it.

The `hello` template no longer declares `tailnet.connects: ["tag:db"]`, a tag
most tailnets don't have (pasting its grant failed with `tag not found`).
`tw deploy` now ends with a `curl` line for the app and `tw logs <app> -f`.
The README is rewritten as a short quick start.
