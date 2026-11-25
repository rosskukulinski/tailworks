---
kind: security
upgrade: []
---
`tw upgrade` on a `tw up` machine now checks the release signature. It extracts the tarball with the hardened extractor tw-hostd uses, then checks its `SHA256SUMS.sig` against the release keys built into `tw` (or `TW_RELEASE_SIGNERS`) with the same verifier as tw-hostd's self-upgrade, before anything in the release runs. Before, it only compared the tarball with a `SHA256SUMS` downloaded from the same place, and `--from <dir>` checked nothing, so a tampered tarball's own `install.sh` would have run. An unsigned release is refused with `release_unsigned` and a badly signed one with `release_bad_signature`, on `--plan` too and for `--from`. `tw upgrade --allow-unsigned` (or `TW_ALLOW_UNSIGNED=1`) installs a build you made yourself, with a warning in the plan. `tw upgrade <version>` also refuses a download whose release is a different version (#98).
