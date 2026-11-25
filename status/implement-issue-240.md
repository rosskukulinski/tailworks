Last update: 2026-11-23 20:30 PT. Issue #240: the tw CLI on npm (`npx tailworks`, `npm i -g tailworks`).

# The tw CLI on npm (issue #240)

## What's built

- **Packages.** `tailworks` (`js/cli/tailworks`) is a Node.js shim with bins
  `tw` and `tailworks`. It has no install script and downloads nothing. It
  resolves `@tailworks/cli-<os>-<arch>` (an optionalDependency with `os`,
  `cpu` and, on Linux, `libc: glibc`) and spawns its `bin/tw`, passing the
  arguments, stdio and exit status (or signal) through. SIGTERM and SIGHUP
  are forwarded; SIGINT and SIGQUIT are left to the terminal's process
  group. When the platform package is missing (`--omit=optional`, musl, or
  an unsupported platform), it says why.
- **Platform packages** come from the signed release tarballs
  (`scripts/npm-cli-packages.sh`). The script verifies `SHA256SUMS.sig`
  against the pinned keys and each tarball against `SHA256SUMS`, then
  extracts the tarball and drops `install.sh`, the checksums and
  `share/tailworks/vm` (the microVM guest). It sets release.json to
  `"microvm":false,"install":"npm"` and writes the package.json. It also
  fails if `npm pack` would leave out a payload file: npm drops nested
  `.npmignore` files and honours their rules.
- **Where state lives:** where it always did, `~/.tailworks` or
  `--state-dir`. Only the binaries are in npm's directory. tw finds its
  daemons, workerd and the bundler next to itself, as in a tarball install.
- **Detecting an npm install** (`internal/npminstall`): a `package.json`
  named `@tailworks/cli-*` in the directory above `bin/`. npx means the
  path contains `/_npx/`.
  - `tw upgrade` refuses with the new `npm_install` error and gives the
    npm command. `isInstallPrefix` is false for a platform package, so
    `--prefix <pkg>` is refused too.
  - `tw version --check` / the update notice print `npm install -g tailworks@latest` (or `npx tailworks@latest …`).
  - `tw-hostd -self-upgrade` refuses on an npm install.
  - A login service never points into npx's cache: `installService`
    refuses (`npm_install`), and `tw up`'s default `--service` is off from
    npx. A global npm install keeps the service, because its path is
    stable across `npm i -g` upgrades.
  - `tw doctor` has a new `install` check ("installed by": install.sh,
    npm or npx). On Linux, `microvm_tier` says the npm package has no
    guest.
- **Publishing.** `scripts/npm-publish.sh <v> --cli-dist <dir>` publishes
  the three platform packages after the existing four, then `tailworks`
  last. A real publish requires `--cli-dist`. `npm-publish.yml`
  downloads the release's tarballs, `SHA256SUMS` and `.sig` with
  `gh release download` and passes them in. Provenance comes from trusted
  publishing, as for the other packages. `scripts/npm-versions.mjs` also
  checks `js/cli/tailworks` (its version and every `@tailworks/cli-*`
  version) and `--set`s it. It is deliberately not a workspace, because
  `npm ci` would try to fetch its platform packages.
- **Tests.**
  - `make npm-cli-test` (`scripts/npm_cli_test.sh`) is hermetic. It uses
    fake signed tarballs and checks the refusals (unpinned key, unsigned,
    tampered), the contents of each package, and an offline `npm install -g`
    with the shim running tw.
  - `make test-js` also runs `js/cli/tailworks/test`.
  - release.yml's `ready` gate runs `npm-cli-test`. Each `tarball <platform>`
    job then runs `scripts/ci-npm-cli-check.sh` against the real tarball:
    npm install -g, `tw version`, `tw doctor` (install = npm), `tw init` +
    `tw dev --once` smoke 200, and `tw upgrade` refusing with `npm_install`.
- **Docs.**
  - README quick start: `npx tailworks init/dev` to try it, then
    `npm install -g tailworks`, with get.sh as the no-Node.js alternative.
  - INSTALL.md: a new "Install with npm" section and a prerequisites row.
  - upgrading.md: a new "Installed with npm" section.
  - RELEASING.md: the npm CLI publish, how to check it, and the first
    publish being by hand (trusted publishing needs the package to exist).
  - errors.md: `npm_install`. AGENTS.md: a suite-table row.
  - create-tailworks: the no-tw hint now points at `npx tailworks dev`.

## Sizes (v0.4.0 payload)

| | download | unpacked | files |
|---|---|---|---|
| `@tailworks/cli-darwin-arm64` | 117.6 MB | 385 MB | 12,766 |
| `tailworks` | 2.8 kB | 5.5 kB | 4 |

workerd is 152 MB of the darwin package; the vendored node_modules are
108 MB. The microVM guest (211 MB on darwin) is left out. The Linux
packages should be about the same size: their tarballs are 181 MB and
192 MB with the guest.

## Test results (commands run)

- `go test ./internal/npminstall ./cmd/tw -run 'NPM|Prefix|Doctor|ServiceBinary'`: ok.
- `node --test js/cli/tailworks/test/*.test.js`: 3/3 on macOS arm64 and
  Linux arm64 (Lima twd-dev).
- `sh scripts/npm_cli_test.sh`: ok on macOS arm64 and Linux arm64.
- `scripts/ci-npm-cli-check.sh` against a local `0.4.0-npmtest` tarball
  (the v0.4.0 payload with this branch's Go binaries; unsigned local
  version): ok on **macOS arm64** (this Mac) and **Linux arm64** (Lima
  twd-dev, Ubuntu 24.04, glibc 2.39). That covers npm install -g, doctor,
  `tw init`, `tw dev --once` smoke 200 and the `tw upgrade` refusal.
  Linux amd64 wasn't run by hand. release.yml's `tarball linux-amd64`
  job runs it.
- By hand on macOS:
  - `npx -p <shim.tgz> -p <cli.tgz> tw doctor` says npx, and `tw dev --once`
    serves 200.
  - A SIGTERM to the shim stopped `tw dev` cleanly.
  - `npm publish --dry-run` of the shim and of a platform package both
    succeed.
- `make lint`: ok (on mini, after merging origin/master).
- `make test npm-cli-test` on the mbp runner: ok (116 Go packages, every JS suite including the shim's). The first run caught `TestDoctor` (it needed the new `install` check, and on Linux glibc must stay `Checks[1]`); fixed.
- 2026-11-23, after merging origin/master again (one conflict, `TestDoctor`'s expected checks: kept master's `worker_internet` and this branch's `install`): `make lint` ok, `make test npm-cli-test` ok, on macOS arm64 with Go 1.27.4.
- Not run: `install-test` (install.sh/get.sh unchanged), and the credentialed and VM suites.

## Gaps / next steps

- **First publish by hand.** `tailworks` and `@tailworks/cli-*` don't
  exist on npm yet. Trusted publishing can only be set up for a package
  that exists, so a maintainer publishes the first release by hand
  (RELEASING.md "By hand") and then configures trusted publishers.
- **Registry size limit** for a ~120 MB package: unverified until the
  first publish.
- **The issue's "done when" checks** (`npx tailworks@latest dev` on a fresh
  machine) can only run after a release is published. Before that, the
  release.yml tarball jobs run the same flow on all three platforms.
