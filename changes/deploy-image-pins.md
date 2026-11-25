---
kind: feature
upgrade: [cli-json]
---
`tw deploy` prints one `✓ image` line per container image it did not build, showing what mgmt pinned the ref to (`<Class> <ref> linux/<arch> → <repository>@<digest>`, plus the alias, public base or upstream digest it resolved through). `tw deploy --json` lists every pin in the result's `images` and on the `ready` event.
