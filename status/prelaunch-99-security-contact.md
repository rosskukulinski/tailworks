Last update: 2026-11-17 PT. Issue #99: SECURITY.md reporting channel.

## What changed

- Private vulnerability reporting is now **on** for the repository
  (maintainer action). Verified:
  `gh api repos/rosskukulinski/tailworks/private-vulnerability-reporting`
  → `{"enabled":true}`. This resolves the "Found, not fixed" item in
  `status/docs-product-issue-51.md` (left untouched, per status/README.md).
- `SECURITY.md`: says the form needs a GitHub account; adds a fallback for
  reporters who can't use it (a public issue asking for a private contact,
  with no details); states that no PGP key is published.
- `docs/RELEASING.md`, "Review the draft": check that private vulnerability
  reporting is still enabled before publishing.

## Tests

- `make lint test`: pass.
- `make changes-check`: pass, no fragment required (docs only).

## Not done (maintainer)

- Issue #99 also asks for a channel that works without GitHub (a
  security@ address or a named mailto). No contact address is published
  anywhere in the repository, and agents must not invent one. The
  maintainer should pick an address, add it to SECURITY.md next to the
  GitHub form, and close #99.
