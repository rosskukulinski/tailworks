---
kind: fix
upgrade: []
---
`tw d1`, `tw kv`, `tw r2` and `tw secret` without `--workload` take the
workload from the app in the current directory. When that app failed to
load (a `tailworks.jsonc` schema problem, a bad wrangler config), they said
"no app in the current directory"; they now print the app's own error, with
a `--workload` fix line to skip the app. With no app config at all the
message is unchanged.
