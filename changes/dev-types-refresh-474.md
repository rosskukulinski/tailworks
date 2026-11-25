---
kind: feature
upgrade: []
---
`tw dev` keeps a `tw types`-generated `worker-configuration.d.ts` current
(#474): on start and on each reload it rewrites the file when the config
has changed, so a new binding shows up in the editor without rerunning
`tw types`. It never creates the file and leaves hand-written files
alone, as well as files typed for an `--env`.
`tw types --check` stays the CI step.
