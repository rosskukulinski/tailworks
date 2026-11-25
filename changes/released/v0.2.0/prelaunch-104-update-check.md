---
kind: fix
upgrade: [cli-json, config-default]
---
`tw init` and `tw dev` no longer run the once-a-day update check, so they make no outbound request (before, they could ask `api.github.com` for the latest release after they finished). `tw version --json` adds `updateCheck`: whether this tw runs the check (a release build without `CI` or `TW_NO_UPDATE_CHECK` set). The README ("What tw sends") and `docs/security-model.md` ("Telemetry and the update check") now say what the check sends, when it runs and how to turn it off; tailworks has no other telemetry.
