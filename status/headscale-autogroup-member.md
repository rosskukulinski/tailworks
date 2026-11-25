Last update: 2026-11-25 11:40 PT. Headscale profile keeps autogroup:member grant sources (#552).

## What changed

- `internal/policy/headscale.go`: `Headscale()` keeps grants from
  `autogroup:member` and `checkHeadscale` accepts it as a grant source.
  `autogroup:admin` (owner or source) and every other autogroup are still
  dropped/refused. RT-06 is unchanged: without `AllowSrcs` the source is
  still refused, so only mgmt's own caller grant uses it.
- So on a Headscale tailnet `tw deploy --apply-policy` / `tw policy --apply`
  now write the caller grant (`autogroup:member` → the app's tag, tcp:80)
  instead of returning a manual `callers:` line.
- `e2e/headscale`: the profile snippet applied to the real server carries an
  `autogroup:member` grant; `autogroup:admin` as a source is now in the
  rejected list (Headscale 0.29.7: "autogroup not supported for ACL sources:
  autogroup:admin, can be [autogroup:member autogroup:tagged
  autogroup:danger-all]"); `TestTwUp` deploys a new app with
  `--apply-policy --yes` and checks the caller grant was written.
- docs/headscale.md: the caller grant row and the e2e description.

## Tests

See the PR's Tests section.
