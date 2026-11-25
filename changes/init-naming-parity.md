---
kind: fix
upgrade: []
---
`tw init` renames a template's tags in one pass, so a tag the rename itself
produced is never renamed again. Before, with some workload names, a class
could end up with the workload's tag or the reverse, depending on map order.
`npm create tailworks` without `tw` on PATH now lowercases `İ` the way `tw
init` does, so both give a directory such as `İstanbul_App` the workload
name `istanbul-app`.
