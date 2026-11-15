# Releasing twd

This is the maintainer process. The design behind it is
`docs/design/adr/041-upgrades-and-releases.md`.

## Versions

- **One version for everything.** Every binary, the microVM guest, the
  embedded platform runtime, the admin UI, the create-twd templates and the
  `@twd/*` npm packages share the version in `VERSION`.
- **Before 1.0** versions are `0.MINOR.PATCH`:
  - A minor may break things. A breaking change needs a migration path and
    a `breaking` fragment.
  - A patch fixes bugs only. It has no mgmt migration, no format change, no
    workerd bump (unless it is a security fix), no new feature string and
    no protocol change.
- **From 1.0** twd follows standard SemVer. Config and API stay stable within
  a major.
- **Skew.** A release line is a minor before 1.0 and a major after it. Hosts
  may run one line behind mgmt, never ahead (ADR §7).

## Branches

- master is always releasable.
- **A minor release** is a tag on master, after the `VERSION` bump.
- **A patch release** comes from `release-0.N`. Create that branch from the
  minor's tag when the first patch is needed, then cherry-pick fixes from
  master into it.
- **Support.** Only the latest minor gets patches. Security fixes also go to
  the minor before it.

## Release notes

Every branch adds `changes/<branch>.md` (format: `changes/README.md`).
`make lint` fails when a branch touches an upgrade-relevant path and no
fragment names the matching upgrade kind. Upgrade-relevant paths include
migrations, `api/openapi.yaml`, `schema/cli.schema.json`, the workerd pin,
guest inputs, format fixtures and protocol constants.

Preview the notes at any time:

```sh
scripts/release-notes.sh v0.1.0 v0.2.0
```

## Cutting a release

1. **Merge.** Merge everything intended for the release, then check out
   master, or `release-0.N` for a patch.
2. **Finalize the notes.**

   ```sh
   scripts/release-notes.sh --finalize v0.1.0 v0.2.0
   ```

   This moves the pending fragments to `changes/released/v0.2.0/`. It also
   writes `internal/catalog/releases/v0.2.0.json`, the catalog entry that
   twd-mgmt embeds for offline upgrade plans. The entry records the notes,
   the workerd pin, the newest migration, contract migrations and protocol
   versions. Last, it re-renders the user-facing
   [CHANGELOG.md](../CHANGELOG.md) from the catalog
   (`go run ./internal/catalog/changes changelog`; a test fails when it is
   stale).
3. **Bump `VERSION`**, and the `@twd/*` and `create-twd` package versions,
   to `0.2.0`. Commit all of it as `Release v0.2.0`.
4. **Tag and push.**

   ```sh
   git tag -a v0.2.0 -m "twd v0.2.0" && git push origin v0.2.0
   ```

5. **Watch `.github/workflows/release.yml`.** It runs:
   - `gates`: `make lint test`, the install.sh rollback test, and
     `make e2e-upgrade`, which upgrades from the previous release to this
     commit and checks data, rejoin and old bundles;
   - `vm-assets` and `vz-assets`: the microVM guests for each arch;
   - `release`: the three tarballs with `SHA256SUMS` and the catalog, and a
     **draft** GitHub release whose body comes from `release-notes.sh`.

   A failed gate means no draft.
6. **Review the draft.**
   - Check that the notes read well, and that the Breaking and Upgrade
     actions sections are complete.
   - Check the tarball list.
   - Install one tarball by hand with `scripts/get.sh v0.2.0` after you
     publish, or from the draft's assets before.
7. **Publish** the draft. `scripts/get.sh` and `twd upgrade` install the
   latest published release.
8. **Publish npm.** Once the GitHub release is out, publish the `@twd/*`
   packages and `create-twd` at the same version, from the same tag.

## If a release is bad

- **Before publishing:** delete the draft and the tag, fix the problem on
  master, then release again.
- **After publishing:** ship a patch. Never move a published tag.
- **For users:** `twd upgrade` rolls back on a failed health check. To roll
  back by hand, run `~/.twd/install.sh --rollback` and restore the mgmt
  backup that `twd upgrade` took. Contract migrations can't be undone by a
  rollback, which is why they ship a release after their expand step.
