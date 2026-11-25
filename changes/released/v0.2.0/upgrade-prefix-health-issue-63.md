---
kind: fix
upgrade: [cli-json, config-default]
---
`tw upgrade` on a `tw up` machine now replaces the install that runs the state dir (the binary the service or the running supervisor was started from, or the state dir itself), not whichever directory the `tw` binary happens to live in. Running it from an extracted release, as the first upgrade from 0.1.0 needs, used to plan against the extracted directory; it now refuses with `prefix_mismatch` unless `--prefix` names the install. docs/upgrading.md covers the first upgrade.

The health check after the restart now only waits for deployments that were active before the upgrade. One that had already failed no longer rolls a good upgrade back; `--plan` and the start of the run warn about it, and the result lists it (`notActiveBefore` in `--json`) (#63).
