---
kind: feature
upgrade: []
---
On Headscale, `tw deploy --apply-policy` and `tw policy --apply` now write
the caller grant for a new app (`autogroup:member` to the app's own tag on
port 80), the same as on Tailscale, instead of printing it for you to add.
Headscale v0.27 and later accept `autogroup:member` as a grant source, and
tailworks requires v0.29. `autogroup:admin` and the other autogroups are
still left out of the Headscale profile.
