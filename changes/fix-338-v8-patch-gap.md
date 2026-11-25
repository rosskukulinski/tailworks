---
kind: feature
upgrade: [cli-json]
---
tailworks now watches V8, not only workerd (#338). V8 security fixes arrive
as V8 rolls inside ordinary workerd releases, and the public signal is
Chrome's stable-channel release notes. A nightly job (`make v8-check`)
compares the V8 in the pinned workerd with the V8 in Chrome's stable channel
and opens a `security` issue when the pin is behind; `docs/RELEASING.md`
names that feed, the 7-day target for a High V8 CVE and which V8 a release
carries, and `docs/security-model.md` records that the shipped workerd
builds have no V8 sandbox.

For operators: `tw doctor` has a new `workerd_age` check that shows how old
the pinned workerd is and warns when it is more than 30 days old. The
once-a-day update notice and `tw version --check` now say "security
release, upgrade soon" when the latest release's notes have a Security
section; `tw version --check --json` has `securityRelease` for it.
