Last update: 2026-11-25 PT. #539: tw up infers the Headscale profile from --api-key-file + a non-default --control-url; default key file ~/.config/tailworks/headscale.env.

## What changed

- `--api-key-file` with a `--control-url` other than Tailscale's, and no
  explicit `--control`, selects the Headscale profile in `tw up` and
  `tw tenant add` (cmd/tw/up_headscale.go). `--control=headscale` is still
  accepted; `--control=tailscale` always wins.
- With Tailscale's control URL, `--api-key-file` is still
  `mint_mode_removed` (ADR-042), with one added fix line pointing at
  `--control-url` for Headscale users.
- On the default state dir, with no credential flag and a Headscale
  `--control-url` (or `--control=headscale`), `tw up` reads
  `$XDG_CONFIG_HOME/tailworks/headscale.env` (else
  `~/.config/tailworks/headscale.env`) when it exists. Never prompted for
  or created. A bare `tw up` keeps the OAuth path even when the file exists.
  `tw tenant add` gets the inference but no default key file (a second
  tenant's server is a different server).
- `--control=headscale` with no key file now says the profile needs
  `--api-key-file` instead of printing the OAuth `mint_mode_required` text.
- Docs: headscale.md, INSTALL.md, errors.md, ADR-042 dated note, cli.md.

## Tests

- `go test ./cmd/tw -run 'TestUpHeadscale|TestUpDefaults|TestModeChoice|TestCLILink'`: pass.
- `make lint`: pass (after merging origin/master).
- `make test` (via mbp-test.sh): pass.
- `make e2e-headscale` (Docker, headscale v0.29.7): pass, including the new
  inferred-profile case in TestTwUp.
- `make changes-check`: ok; fragment `changes/up-headscale-infer.md`.

## Not changed

- scripts/demo-headscale.sh and scripts/demo.sh keep `--control headscale`
  (still accepted), so `make demo-test` was not needed.
