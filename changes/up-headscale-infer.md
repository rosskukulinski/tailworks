---
kind: feature
upgrade: []
---
`tw up` on Headscale no longer needs `--control=headscale`: `--api-key-file`
with a `--control-url` other than Tailscale's selects the Headscale profile,
and on the default state dir `tw up --control-url <server_url> --tailnet
<base_domain>` reads the API key from `~/.config/tailworks/headscale.env`
(or `$XDG_CONFIG_HOME/tailworks/headscale.env`) when it exists. `tw tenant
add` infers the profile the same way (no default key file there).
`--control=headscale` is still accepted, `--control=tailscale` still wins,
and `--api-key-file` with Tailscale's control URL is still
`mint_mode_removed`, now with a hint to add `--control-url` on Headscale.
A bare `tw up` keeps the OAuth path even when `headscale.env` exists.
