---
kind: security
upgrade: []
---
Releases are signed. Each release's `SHA256SUMS`, and each tarball's, gets a `SHA256SUMS.sig`: an SSH signature (`ssh-keygen -Y`) by a tailworks release key that is pinned in `scripts/get.sh`, `install.sh` and the tailworks binaries. `get.sh` checks the signature before it trusts the checksums, and `install.sh` checks it before it installs. tw-hostd's self-upgrade checks it in Go against the keys built into the running binary, so a compromised tw-mgmt can no longer push a release of its own. An unsigned or badly signed release is refused. `install.sh --allow-unsigned` (or `TW_ALLOW_UNSIGNED=1`) installs a build you made yourself, and local builds (`x.y.z-<commit>`) install with a warning. `get.sh` refuses an unsigned release whatever its tag (no unsigned release was ever published). tw-hostd also checks that a release it is ordered to install is that version (`VERSION`), and extracts tarballs so that no entry can write or link outside the extract directory. To check a release by hand, see INSTALL.md, "Verify a release" (#71).

Operator action: a host that self-upgrades now refuses unsigned releases. Until a release key is pinned (docs/RELEASING.md, "Signing"), upgrade hosts by hand.
