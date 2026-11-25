Last update: 2026-11-17 PT. Issue #102: LICENSE, THIRD_PARTY_NOTICES and guest source offers in every release tarball.

# prelaunch-102-license-notices

## What changed

- `scripts/third-party-notices.mjs <stage>` (new; one line in `scripts/release.sh`) writes into each staged tarball:
  - `LICENSE`
  - `THIRD_PARTY_NOTICES`: workerd (Apache-2.0); the Go standard library and every Go module in `bin/*` (from `go version -m`) and in tw-guestd (`go list -deps`), each with its license files from the module cache; the vendored `share/tailworks/node_modules` (each package's license-file paths); with a guest, Firecracker + jailer (NOTICE and THIRD-PARTY), the kernel and the rootfs packages; and the Apache-2.0 / GPL-2.0 / LGPL-2.1 texts.
  - With a guest, `share/tailworks/vm/KERNEL-SOURCE.md` (the kernel.org tarball and its sha256, the Firecracker CI config and `guest/kernel/{tw,vz}.config` at the release commit, the build script, the compiler, a written offer) and `ROOTFS-SOURCE.md` (each Ubuntu package: the files, its copyright file in the image, the Launchpad source package, a written offer).
  - Fails when a Go module has no license file or a guest's `manifest.json` has no `packages`. Warns when a vendored npm package has no license file.
- `scripts/check-release-notices.sh <tarball>...` (new) fails when a tarball is missing these files. It runs in `release.sh` after each tarball and again in `release.yml` before the upload (a new step).
- `guest/rootfs-packages.sh` (new) is called from `guest/build-image.sh`, which now also extracts ubuntu-base's dpkg database and `usr/share/doc`. It maps every file copied from ubuntu-base to its Debian package and version, copies each package's `copyright` into the image (`/usr/share/doc/<pkg>/copyright`), and adds `manifest.json` `"packages"`, plus ca-certificates from the pinned .deb. A file that no package owns fails the build. The rootfs hash changes because of the new copyright files.
- `third_party/licenses/` (new) holds texts that the upstream artifacts don't ship: workerd's LICENSE (the npm platform package has none), Firecracker v1.17.0's LICENSE, NOTICE and THIRD-PARTY, Go's LICENSE (Homebrew's GOROOT has none), GPL-2.0 and LGPL-2.1.
- `internal/release/notices_test.go` (new, part of `make test`): runs the generator and the check on a fake stage, with and without a guest, including the failure cases.
- Docs: `docs/RELEASING.md` has a new section, "Licenses and notices". The RFC §15 sentence now names the real files.

## Tests

- `go test -count=1 -run TestThirdPartyNotices ./internal/release/`: ok.
- `make release RELEASE_VM_ASSETS=0 RELEASE_TARGETS="darwin-arm64 linux-amd64"` (local, unsigned): both tarballs built. The notices covered 62 and 64 Go modules and 175 npm packages, and `check-release-notices` passed for both. Two warnings: `@ai-sdk/provider-utils` (Apache-2.0) and `@cfworker/json-schema` (MIT) ship no license file.
- The generator on that linux-amd64 stage plus a fake `share/tailworks/vm` produced KERNEL-SOURCE.md and ROOTFS-SOURCE.md. I read both.
- `guest/build-image.sh` (TARGETARCH=amd64) in an `ubuntu:24.04` Docker container, using the real pinned ubuntu-base and ca-certificates downloads and a prebuilt tw-guestd: the build succeeded. `manifest.json` is valid JSON with 7 packages (e2fsprogs, libblkid1, libc6, libcom-err2, libext2fs2t64, libuuid1, ca-certificates). `debugfs` shows `/usr/share/doc/<pkg>/copyright` in the image.
- `make lint`: ok.
- `make test`: every Go package passed except `netd` `TestEgressWaitsForNewPeerPath`, which also fails alone on this Mac ("connection was refused" to 100.64.0.3:80). This branch doesn't touch `netd`, and the likely cause is the host's own Tailscale on 100.64/10; CI on Linux is the check. `make test-js`: 32/32 passed.
- Not run: `make vm-assets`, `make vz-assets` and a full release with guests, which need Lima or Linux with KVM. The kernel builds are unchanged.

## For the maintainer (not legal advice)

- **workerd's bundled libraries.** workerd statically links V8, ICU, BoringSSL, SQLite, Cap'n Proto and others. The npm package has no notices for them, and the notices file only points at the upstream source. Decide whether to collect their notices.
- **npm packages without license files:** `@ai-sdk/provider-utils` and `@cfworker/json-schema`.
- **Written offer.** The contact is a GitHub issue on this repository. Confirm that is acceptable for three years. Consider attaching `linux-<ver>.tar.xz` and `kernel.config` to each GitHub release, which satisfies GPL-2.0 §3(a) directly. The issue's optional `syft` SBOM is not done.
- **Firecracker's NOTICE** mentions libseccomp (LGPL-2.1) being used at build time. Check whether shipping the firecracker binary needs more than reproducing the NOTICE.
- **Vendored texts.** workerd's LICENSE was taken from upstream `main`. Re-check the `third_party/licenses` copies on Firecracker, Go or workerd bumps.
