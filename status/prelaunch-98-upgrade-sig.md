Last update: 2026-11-17 PT. `tw upgrade` (tw up machine) verifies the release signature before anything in it runs (issue #98).

# tw upgrade release signature (issue #98)

Follows `status/release-signing-issue-71.md`, which left the single-machine
`tw upgrade` path checking only a same-origin `SHA256SUMS`.

## What's built

- **One verifier, one extractor.** hostd's `os.Root`-hardened tarball
  extractor moved to `internal/releasesig/extract.go`
  (`releasesig.ExtractTarGz`); hostd keeps a one-line `extractTarGz`
  wrapper (hostd/upgradesig.go). The CLI's own extractor in
  `cmd/tw/upgrade.go` (lexical checks only) is gone; `extractRelease` now
  calls the shared one.
- **`cmd/tw/upgradesig.go`:** `verifyUpgradeRelease(dir, allowUnsigned)`
  runs `releasesig.VerifyTree` with the keys pinned in the binary, or the
  allowed_signers file named by `TW_RELEASE_SIGNERS` (as get.sh and
  install.sh honour). `homelabUpgrade` calls it right after
  `fetchRelease`, so it covers the download, `--from <tgz>` and
  `--from <dir>`, and runs before the plan, the backup and `install.sh`.
  The staging dir is removed by the existing `defer`.
- **Typed errors:** `release_unsigned` (no `SHA256SUMS` or no
  `SHA256SUMS.sig`; `releasesig.ErrUnsigned`, new sentinel wrapped by
  `VerifyTree`, same message text as before) and `release_bad_signature`
  (unpinned key, tampered or extra file, or no key pinned in this build).
  Both are in docs/errors.md.
- **`--allow-unsigned` / `TW_ALLOW_UNSIGNED=1`:** lets an unverified
  release through with a plan warning, and passes `--allow-unsigned` on
  to the release's `install.sh` (which would otherwise refuse it too).
- **Version binding:** `tw upgrade <version>` refuses a release whose
  `release.json` version isn't the one asked for (a mirror serving an
  older signed release under the new name).
- **Docs:** RELEASING.md "Who checks it" gains the `tw upgrade` row;
  ADR-041 §8 step 1 and the attestation sentence (nothing verifies
  attestations yet) corrected; upgrading.md step 1; docs/cli.md
  regenerated; `changes/prelaunch-98-upgrade-sig.md` (security).

## Tests

New `cmd/tw/upgradesig_test.go`:

- `TestUpgradeRefusesUnverifiedReleases`: unsigned, other key, tampered,
  extra file; each through `--plan --from <tgz>`, `--plan --from <dir>`,
  and a real run (`--yes --prefix …`) from tgz and dir. Each fails with the
  right code, the release's install.sh never runs, no backup is taken,
  and the staging dir is gone.
- `TestUpgradeAcceptsSignedReleases`: a release signed by a key trusted via
  `TW_RELEASE_SIGNERS` plans; `--allow-unsigned` and `TW_ALLOW_UNSIGNED=1`
  plan an unsigned one with a warning.
- `TestUpgradeUsesPinnedKeys`: with no override, a test-key signature is
  refused (`release_bad_signature`).
- `TestExtractReleaseRefusesEscapes`: `..`, absolute and escaping symlink
  entries are refused by the shared extractor.

Commands run (see the PR for results): `make lint`, `make test`,
`go test ./cmd/tw ./hostd ./internal/releasesig -run 'Upgrade|Extract|Release|Verify'`.

## Gaps

- Until a release key is pinned in `internal/releasesig/allowed_signers`,
  `tw upgrade` refuses every release without `--allow-unsigned`
  (`release_bad_signature`, "this tw build pins no release signing key").
  That matches tw-hostd's self-upgrade and is intended: pinning the key is
  the maintainer's step before the first release.
- install.sh installs an unsigned local build (`x.y.z-<commit>`) with a
  warning; `tw upgrade` asks for `--allow-unsigned` for it instead.
- GitHub artifact attestations are still not verified by anything.
