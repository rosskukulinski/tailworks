# Releasing tailworks

This is the maintainer process. The design behind it is
`docs/design/adr/041-upgrades-and-releases.md`.

## Versions

- **One version for everything.** Every binary, the microVM guest, the
  embedded platform runtime, the admin UI, the create-tailworks templates and the
  `@tailworks/*` npm packages share the version in `VERSION`.
- **Before 1.0** versions are `0.MINOR.PATCH`:
  - A minor may break things. A breaking change needs a migration path and
    a `breaking` fragment.
  - A patch fixes bugs only. It has no mgmt migration, no format change, no
    workerd bump (unless it is a security fix), no new feature string and
    no protocol change.
- **From 1.0** tailworks follows standard SemVer. Config and API stay stable within
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

## Upstream advisories

The maintainer owns this. Every release bundles code we don't write, so
watch each upstream that ships in it:

| Upstream | Pin | Where advisories come from |
| --- | --- | --- |
| Go modules and the Go toolchain | `go.mod`, `tools/go.mod` | `make vulncheck` (govulncheck, run by `make lint`), Dependabot alerts, golang-announce |
| npm packages | `package-lock.json`, `e2e/*/package-lock.json` | Dependabot alerts, `npm audit` |
| workerd | `package.json`, `internal/version/workerd.go` | the workerd GitHub releases and security advisories; for the V8 inside it, Chrome's stable-channel release notes, watched by the nightly `v8-check` job (see **V8** below) |
| Tailscale (`tailscale.com`) | `go.mod` | tailscale.com/security-bulletins, govulncheck |
| Firecracker | `guest/inputs.sh` | the Firecracker GitHub releases and security advisories |
| Guest kernel (6.1 LTS) | `guest/inputs.sh` | the kernel.org stable releases and linux-cve-announce |
| Ubuntu base (guest rootfs) | `guest/inputs.sh` | Ubuntu Security Notices for 24.04 |
| GitHub Actions | `.github/workflows` (SHA-pinned) | Dependabot |

**Routine updates.** Dependabot (`.github/dependabot.yml`) opens grouped
minor and patch PRs every Monday, one per ecosystem. They go through
`make lint test` like any PR. tailscale.com and workerd get their own PRs:
a tailscale.com bump also needs `make e2e-placement` and `make e2e-real`,
and a workerd bump needs a `workerd` release-note fragment. The pins that
Dependabot can't see (Go toolchain, Firecracker, kernel, Ubuntu base) are
checked by hand once a month.

**When an advisory affects a release.** It affects us when govulncheck
reports it as reachable, or when the vulnerable code ships in a tarball or
the guest and a tenant or a network peer can reach it.

- Triage within 2 business days: affected or not, and the severity.
  Record it in a private security advisory, or in an issue labelled
  `security` if the issue is already public.
- Critical or high: a patch release (on `release-0.N`, and the minor
  before it, per **Support** above) within 7 days of a fix being
  available upstream.
- Medium or low: the next release. Ship it sooner if it is cheap.
- Not affected: say so in the issue or advisory, so the next person
  doesn't triage it again.

**V8.** workerd carries V8, and V8 security fixes don't arrive as workerd
advisories: V8 rolls into ordinary workerd releases, and the public signal
is Chrome's stable-channel security release, whose notes list the V8 CVEs
([Chrome Releases, stable updates](https://chromereleases.googleblog.com/search/label/Stable%20updates);
the current stable version is also at
`https://chromiumdash.appspot.com/fetch_releases?channel=Stable&platform=Linux&num=1`).
V8 `MAJOR.MINOR` ships in Chrome milestone `MAJOR*10+MINOR`: V8 15.8 is
Chrome 158.

- *Which V8 a release carries* is the V8 in its pinned workerd. `make
  v8-check` prints it: it reads V8's version string out of the workerd
  binary, which is the only place it shows (`process.versions.v8` is empty
  under workerd's `nodejs_compat`, `workerd --version` prints only a date,
  and the inspector names no V8). Put it in the `workerd` fragment when the
  pin moves ("workerd 1.20261121.1, V8 15.8.62.5").
- *What watches the feed* is the nightly `v8-check` job
  (`.github/workflows/nightly.yml`, `scripts/v8-check.sh`,
  `internal/v8check`). It compares the pin's V8 milestone with Chrome
  stable's and, when the pin is behind, opens or updates one `security`
  issue, "workerd pin is behind Chrome stable's V8", with both versions and
  the link to the release notes; it closes the issue when the pin catches
  up. The job stays green either way; the issue is the signal.
- *That issue starts the clock.* Triage it within 2 business days: read the
  Chrome notes for the V8 CVEs and decide whether tenant code can reach
  them. A High or Critical one means a patch release whose workerd carries
  the fixed V8 **within 7 days of the Chrome stable release that names it**
  (the 7-day rule above, counted from Chrome's release because that is when
  the fix is public). Its fragment is `kind: security`: that makes the
  Security section of the release notes, which `tw` reads to call the
  release a security release (below). If workerd hasn't rolled the fixed V8
  yet, say so in the issue and ship when it has.
- *What the operator sees.* `tw doctor`'s `workerd_age` check shows how old
  the pinned workerd is and warns past 30 days. The once-a-day update
  notice (after `tw doctor`, `tw status` and other commands) and `tw
  version --check` say "security release" when the latest release's notes
  have a Security section (`securityRelease` in `tw version --check
  --json`). Whether the shipped workerd has the V8 sandbox is recorded in
  `docs/security-model.md`.

**What a patch release may bump for a security fix.** The patch rules under
[Versions](#versions) hold, except that a patch may move the Go toolchain,
a Go or npm module, tailscale.com, workerd, Firecracker, the guest kernel or
the Ubuntu base to the smallest version that has the fix. That bump must
not change a format, a protocol or an API. A guest bump rebuilds the guest
assets and needs a `workerd` fragment (upgrade kind `workerd` covers
`guest/inputs.sh`). If the only fix is in an upstream release that would
break one of those rules, it ships in a minor and the advisory says so.

**govulncheck in CI.** `make lint` runs `make vulncheck` and fails on any
finding the code can reach. A new advisory published upstream can then
fail CI on any branch: fix it in its own small PR rather than in the branch
that hit it.

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
   tw-mgmt embeds for offline upgrade plans. The entry records the notes,
   the workerd pin, the newest migration, contract migrations and protocol
   versions. Last, it re-renders the user-facing
   [CHANGELOG.md](../CHANGELOG.md) from the catalog
   (`go run ./internal/catalog/changes changelog`; a test fails when it is
   stale).
3. **Bump `VERSION`**, and the `@tailworks/*` and `create-tailworks` package versions,
   to `0.2.0`. Commit all of it as `Release v0.2.0`.

   ```sh
   echo 0.2.0 >VERSION && node scripts/npm-versions.mjs --set 0.2.0
   ```

   `npm-versions.mjs --set` moves every workspace package, their
   dependencies on each other and the templates' `^0.2.0` ranges together.
   `make test` (`TestNPMVersions`) fails if any of them differs from
   `VERSION`.
4. **Check, tag and push.** After the release commit is on master (or
   `release-0.N`), from a checkout of it:

   ```sh
   sh scripts/release-check.sh v0.2.0
   git tag -a v0.2.0 -m "tw v0.2.0" && git push origin v0.2.0
   ```

   `release-check.sh` fails if the tag doesn't match `VERSION` or the npm
   versions, if the notes aren't finalized for the tag or a fragment is
   still pending in `changes/`, or if no signing key is pinned. The
   `gates` job runs it again on the tag.
5. **Watch `.github/workflows/release.yml`.** It tests every runtime the
   release ships, on the guests and tarballs it ships, before it drafts
   anything. The header of release.yml lists every job. In short:
   - `gates`: `scripts/release-check.sh`, `make lint test`, the npm
     publish dry run, the install.sh rollback test, `make e2e-upgrade`
     (newest published release → this commit: data, rejoin, old bundles;
     while none is published, this tree stamped an older version,
     `scripts/upgrade-from.sh`) and `make e2e-tw-upgrade`;
   - `suites`, `test-images`, `test-pg`, `headscale`: every hermetic
     suite (the ones ci.yml and nightly.yml run, and more);
   - `vm-assets`, `vz-assets`, `build`: the microVM guests, then the three
     signed tarballs;
   - `tarballs`: each tarball on a clean runner of its platform
     (`scripts/ci-tarball-check.sh`: signature, install.sh, `tw doctor`,
     every template under `tw dev`);
   - `microvm-amd64`, `mac-firecracker`, `mac-vz`: the Firecracker tier
     (amd64 on a hosted runner's KVM, arm64 in Lima on the release
     runner) and the vz tier, each with the shipped guest: `e2e-vm`,
     `e2e-container`, `e2e-vz`, `e2e-vz-container`;
   - `tailnet`, `linux-amd64`, `mac-linux`, `mac-linuxvm`, `mac-cli`: the
     real-tailnet suites, including `e2e-linux` on every distro row (amd64
     natively on a hosted runner) and `e2e-linux-vm` from the tarballs. The
     Linux jobs run alongside `tailnet`; `mac-cli` waits for it (both write
     policy);
   - `release`: a **draft** GitHub release with the tarballs,
     `SHA256SUMS`, `SHA256SUMS.sig` (see [Signing](#signing)) and the
     notes from `release-notes.sh`.

   A failed job means no draft, and no suite may pass by skipping
   (`scripts/ci-suite.sh`). Before tagging, rehearse on master; it runs
   everything and drafts nothing:
   `gh workflow run release.yml -f tag=master -f rehearsal=true`.
   After fixing one job, rerun just that job: add
   `-f only=tailnet,mac-cli` (job names, comma-separated, no spaces;
   setup and the builds the jobs need always run). A real release always
   runs everything, and `only` without `rehearsal` is refused.
6. **Review the draft.**
   - Check that the notes read well, and that the Breaking and Upgrade
     actions sections are complete.
   - Check the asset list: `tailworks-0.2.0-{darwin-arm64,linux-arm64,linux-amd64}.tar.gz`,
     `SHA256SUMS` and `SHA256SUMS.sig`. `get.sh` refuses a release without
     the signature.
   - Check that private vulnerability reporting is still on, since
     [SECURITY.md](../SECURITY.md) sends reporters there:
     `gh api repos/rosskukulinski/tailworks/private-vulnerability-reporting`
     must print `{"enabled":true}`.
   - Install from the draft's assets (a draft's download URLs aren't
     public, so fetch them first):

     ```sh
     gh release download v0.2.0 -D /tmp/tw-v0.2.0
     TW_HOME=/tmp/tw-home TW_RELEASE_BASE=file:///tmp/tw-v0.2.0 sh scripts/get.sh v0.2.0
     ```

7. **Publish** the draft. `scripts/get.sh` and `tw upgrade` install the
   latest published release.

   ```sh
   gh release edit v0.2.0 --draft=false --latest
   gh api repos/rosskukulinski/tailworks/releases/latest --jq .tag_name   # v0.2.0
   ```

   Then smoke it as a newcomer would, on a clean Mac (Apple silicon) and a
   clean Linux machine:
   `curl -fsSLO https://raw.githubusercontent.com/rosskukulinski/tailworks/master/scripts/get.sh && sh get.sh && ~/.tailworks/bin/tw doctor`.
8. **Publish npm.** Publishing the GitHub release starts
   `.github/workflows/npm-publish.yml`. It runs `scripts/npm-publish.sh`
   from the tag, which publishes `@tailworks/tailnet`, `@tailworks/node`, `@tailworks/mcp`
   and `create-tailworks` at the release's version (a prerelease goes to the
   `next` dist-tag), with provenance. It then publishes the tw CLI (issue
   #240):
   - The workflow downloads the release's tarballs, `SHA256SUMS` and
     `SHA256SUMS.sig` from the GitHub release. `scripts/npm-cli-packages.sh`
     checks the signature against the pinned keys and each tarball against
     `SHA256SUMS`. It then makes one platform package per target:
     `@tailworks/cli-darwin-arm64`, `@tailworks/cli-linux-arm64` and
     `@tailworks/cli-linux-amd64`. Each is the extracted tarball without
     `install.sh`, the checksums and the microVM guest, with `os`/`cpu`/`libc`
     set. They go out first, and `tailworks` (`js/cli/tailworks`, the shim
     whose `optionalDependencies` name them) goes out last, so it is never
     on npm without them.
   - Each platform package is about 120 MB to download and 400 MB unpacked,
     most of it workerd. The npm registry's size limit isn't documented, so
     if a publish fails with `413`, that is the cause.
   - The same packages were already installed and run on each platform
     before the draft existed: release.yml's tarball jobs run
     `scripts/ci-npm-cli-check.sh` (npm install -g, `tw doctor`,
     `tw init`, `tw dev --once`, `tw upgrade` refusing). The `ready` gate
     runs `make npm-cli-test`.
   - Auth is npm [trusted publishing](https://docs.npmjs.com/trusted-publishers)
     (OIDC): npm trusts this repository's `npm-publish.yml` for each
     package, and the job (`id-token: write`, npm >= 11.5.1) publishes with
     no token or secret. Provenance is automatic. A package without a
     trusted publisher fails to publish; see "Trusted publishing setup"
     below.
   - Packages already on npm at that version are skipped, so after a partial
     failure re-run it: `gh workflow run npm-publish.yml -f tag=v0.2.0`.
   - By hand, from a checkout of the tag after `npm ci` and `npm login`:
     `gh release download v0.2.0 --dir /tmp/rel --pattern 'tailworks-*.tar.gz' --pattern 'SHA256SUMS*'`,
     then `sh scripts/npm-publish.sh 0.2.0 --cli-dist /tmp/rel`
     (`--dry-run` to check first; release.yml's gates already ran one,
     without `--cli-dist`).
   - Check it: `npm view tailworks version` and
     `npm view @tailworks/cli-linux-amd64 version` show the release, and on a
     clean machine with Node.js,
     `npx tailworks@<VERSION> init hello && cd hello && npx tailworks@<VERSION> dev --once`
     answers. `npx tailworks@<VERSION> doctor` says
     `installed by (npm)`.
   - Before the first publish, check the templates against the packed
     packages: `make templates-typecheck` (every template's
     `tw init && npm install && npx tsc --noEmit`, with `@tailworks/*` from
     `npm pack` tarballs).
   - Trusted publishing setup (each package: `@tailworks/tailnet`,
     `@tailworks/node`, `@tailworks/mcp`, `create-tailworks`, `tailworks`,
     `@tailworks/cli-darwin-arm64`, `@tailworks/cli-linux-arm64`,
     `@tailworks/cli-linux-amd64`). npm adds a trusted publisher only to a
     package that exists. So the first release of `tailworks` and the three
     `@tailworks/cli-*` packages is published by hand, with `npm login` (see
     "By hand" above). Then set up trusted publishing for them like the
     others:
     1. Configure it just before a release: a new trusted publisher must be
        used by a publish within 2 days or it expires (then delete it and
        add it again).
     2. On npmjs.com, the package's Settings → Trusted Publisher → GitHub
        Actions: user `rosskukulinski`, repository `tailworks`, workflow
        `npm-publish.yml`, no environment. Or with npm >= 11.15, logged in
        as a maintainer:
        `npm trust github <pkg> --file npm-publish.yml --repo rosskukulinski/tailworks --allow-publish`.
     3. Publish the release (or `gh workflow run npm-publish.yml -f tag=v<VERSION>`
        for one already published), then check
        `npm view @tailworks/tailnet version`.
     4. After the first OIDC publish succeeds, delete the `NPM_TOKEN`
        repository secret (`gh secret delete NPM_TOKEN`), and set each
        package's Settings → Publishing access to "Require two-factor
        authentication and disallow tokens". That blocks classic and
        granular tokens only; trusted publishing and `npm login` keep
        working.
     - Optional, staged publishing: allow only staged publishes
       (`npm trust github … --allow-stage-publish`, or the "Allowed
       actions" setting on npmjs.com) and switch `scripts/npm-publish.sh`
       to `npm stage publish`. Each staged package is public only after a
       maintainer approves it with 2FA: `npm stage list`, then
       `npm stage approve <stage-id>` (or on npmjs.com).
   - A maintainer can still publish by hand with `npm login` (above).

## Release runner

The vz tier and nested KVM need real Apple silicon, which hosted macOS
runners don't offer, so the `mac-*` jobs run on a self-hosted runner with
the labels `self-hosted, macOS, ARM64, tw-release`. The machine needs Go
and Node (the setup actions install them), Docker (OrbStack or Docker
Desktop, for the sandbox images), `mkfs.erofs` (`brew install
erofs-utils`), `jq`, Lima with a running-capable dev VM, and the
nested-virt VM from `e2e/linuxvm/twl-kvm-u24.yaml`. The Linux distro rows
of `e2e-linux` are created on demand. Plan for tens of GiB free: the guests,
sandbox images (cached in `~/tw-ci-cache`) and Lima VMs.

Five `mac-*` jobs run per release, so register two or three runner
instances on a big machine (each its own directory and `./config.sh
--name <host>-<n>`). The jobs use different Lima VMs and share the image
cache under a lock. Budget memory for the dev VM (16 GiB), the nested-virt VM
(10 GiB) and three distro VMs (6 GiB each) at once.

- Install: download `actions-runner-osx-arm64` from
  github.com/actions/runner (check its SHA-256 against the release notes),
  `./config.sh --url https://github.com/rosskukulinski/tailworks --token
  <registration token> --labels tw-release`, then `./svc.sh install &&
  ./svc.sh start` (a LaunchAgent, so it needs the user logged in).
- The runner's `.env` sets `PATH` (Homebrew first) and, when the Lima VMs
  are named differently, `TW_DEV_VM`, `TW_E2E_VM_PREFIX` and `LINUX_VM`.
- Security: the repository is public, so the runner must only ever run
  release.yml. Fork pull requests need approval for every outside
  contributor (Settings → Actions), and the `release` environment, which
  holds the signing key and the tailnet API keys, deploys only from `v*`
  tags and master.
- Tailnet secrets (`release` environment): `TW_E2E_TAILNET1` (the tailnet
  name), `TW_E2E_TAILNET1_API_KEY`, `TW_E2E_TAILNET2_API_KEY`. Tailscale
  API keys expire: rotate them with
  `gh secret set TW_E2E_TAILNET1_API_KEY --env release` before they do.

## Signing

Every release is signed (issue #71). A checksum only proves a download is
intact; the signature proves the release came from this repository's
release workflow.

- **What is signed.** `release.yml` signs the release's `SHA256SUMS` and
  each tarball's inner `SHA256SUMS`. Each gets a `SHA256SUMS.sig` next to
  it. The signature is an SSH signature (`ssh-keygen -Y sign -n
  tw-release`) by an Ed25519 key.
- **Who checks it.**
  - `scripts/get.sh` checks the release's `SHA256SUMS.sig` before it trusts
    the hashes in `SHA256SUMS`. It refuses an unsigned release whatever its
    version: an exception for "old" releases would let whoever can publish
    a release pick a tag that skips the check. (No unsigned release was
    ever published.)
  - `install.sh` checks the tarball's `SHA256SUMS.sig` before it installs.
    It is only a second check: a tampered tarball ships its own
    `install.sh`, which is why `get.sh` and hostd check first.
    A build whose version has a `-` suffix (what `make release` stamps on a
    local build) may be unsigned.
  - tw-hostd's self-upgrade checks the tarball's signature in Go, against
    the keys built into the running binary, before it runs `install.sh`.
    The keys are never taken from tw-mgmt or the release.
  - `tw upgrade` on a `tw up` machine checks the tarball's signature with
    the same Go verifier and the keys built into the running `tw`, after
    extracting it and before it runs the release's `install.sh`, for a
    download and for `--from` alike. `--allow-unsigned` (or
    `TW_ALLOW_UNSIGNED=1`) installs a build you made yourself, with a
    warning in the plan.
- **Where the keys are pinned.** `internal/releasesig/allowed_signers` is
  the source of truth. `scripts/get.sh` and `scripts/install.sh` carry
  copies, and `make test` fails if they differ.
- **Opting out.** `install.sh --allow-unsigned`, or `TW_ALLOW_UNSIGNED=1`
  for `get.sh` and `install.sh`. Use it for builds you made yourself.

### The key ceremony

Do this once, and again to rotate the key.

1. Make the key outside the repository, on an encrypted volume:

   ```sh
   sh scripts/release-keygen.sh /Volumes/keys/tw-release
   ```

   This writes the key pair, adds the public key to
   `internal/releasesig/allowed_signers`, and copies it into `get.sh` and
   `install.sh` (`--sync`).
2. Store the private key as the `release` environment's secret:
   `gh secret set TW_RELEASE_SIGNING_KEY --env release <key-file>`.
   Keep an offline backup.
3. Commit the three changed files and merge them before you tag.
   `release.yml` refuses to draft a release without the secret, or with a
   key that isn't pinned.

**Rotating.** Add the new key first and keep the old line, so binaries
already installed still trust the next release. Remove the old line a
release later. **If the key leaks:** remove its line, add a new key and
ship a patch. Hosts on older releases still trust the leaked key until they
upgrade, so tell operators to upgrade with `get.sh` from master.

## Licenses and notices

Each tarball redistributes third-party software (issue #102). `release.sh`
runs `scripts/third-party-notices.mjs` on every staged tarball, which writes:

- `LICENSE`: the project's BSD-3-Clause license. Clause 2 requires it in
  binary distributions.
- `THIRD_PARTY_NOTICES` covers:
  - workerd (Apache-2.0);
  - every Go module compiled into `bin/`, and into tw-guestd, with the
    module's own license files (found with `go version -m` and the module
    cache);
  - the Go standard library;
  - the vendored `share/tailworks/node_modules` (paths to each package's
    license file);
  - with a microVM guest, Firecracker + jailer (Apache-2.0, with
    Firecracker's NOTICE), the kernel and the rootfs's Ubuntu packages;
  - the Apache-2.0, GPL-2.0 and LGPL-2.1 texts.
- With a microVM guest, `share/tailworks/vm/KERNEL-SOURCE.md` and
  `ROOTFS-SOURCE.md`: the exact corresponding source (the kernel.org tarball
  and its sha256, the config inputs and build script at the release commit,
  the Ubuntu source packages and versions) and a written offer for it.

Texts that the upstream artifacts don't ship are vendored in
`third_party/licenses/`. The workerd npm package has no LICENSE, and
Firecracker's release tarball has no NOTICE. When you bump Firecracker or
Go, check those copies against upstream.

`guest/build-image.sh` records the Ubuntu packages behind every file it
copies from ubuntu-base in `manifest.json` `"packages"`
(`guest/rootfs-packages.sh`), and copies each package's
`/usr/share/doc/<pkg>/copyright` into the image. A file that no package
owns fails the build.

**The build fails when** a Go module has no license file, or a guest's
`manifest.json` has no package list. **It warns when** a vendored npm
package has no license file. `scripts/check-release-notices.sh` fails a
tarball without the files above. `release.sh` runs it on each tarball, and
`release.yml` runs it again before the upload.

**Review before a release.** This tooling is a compliance aid, not legal
advice. On each release, check:

- Warnings about npm packages without license files.
- workerd's notices. Its binary links V8, ICU, BoringSSL, SQLite and
  others, and the npm package carries none of their notices. Upstream
  publishes no NOTICE file for them.
- That the written offer's contact (an issue on this repository) is still
  how you want source requests to arrive.
- Whether to attach the kernel source tarball to the GitHub release, which
  is more robust than relying on the offer.

## If a release is bad

- **Before publishing:** delete the draft and the tag, fix the problem on
  master, then release again. If more changes landed meanwhile, re-finalize
  the notes (below) so they are in this release.
- **After publishing:** ship a patch. Never move a published tag.
- **For users:** `tw upgrade` rolls back on a failed health check. To roll
  back later, `tw upgrade rollback` puts the previous release back, and the
  backup that `tw upgrade` took when the previous tw-mgmt can't open the
  store. Contract migrations can't be undone without that backup, which is
  why they ship a release after their expand step.

## Re-finalizing an unpublished release

Finalized notes are for one commit. If changes land after you finalize a
version but before it is published (or its draft is deleted), those
fragments are pending again: `release-check.sh` fails, and if you tagged
anyway they would be missing from this release's notes and catalog entry
and turn up in the next release's. Fold them in, for a version that was
never published only:

```sh
git mv changes/released/v0.2.0/*.md changes/ && rmdir changes/released/v0.2.0
git rm internal/catalog/releases/v0.2.0.json
scripts/release-notes.sh --finalize v0.1.0 v0.2.0
git commit -m "Release v0.2.0 (re-finalized)"
```

`--finalize` writes the entry again with today's date and re-renders
CHANGELOG.md. A published release's notes never change: ship the
changes in the next version.
