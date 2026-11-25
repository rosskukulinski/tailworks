---
kind: fix
upgrade: []
---
The built-in bundler now handles a CommonJS dependency that `require()`s a
Node builtin, such as `dotenv`'s `require("fs")` (#356). Before, the Worker
bundled and then failed to start with `Dynamic require of "node:fs" is not
supported`. With `nodejs_compat` (or `nodejs_compat_v2`), each such
`require()` now imports the builtin, as wrangler does, so `dotenv` and
similar packages work without `--wrangler`. Without the flag, the
`require()` throws, when it runs, an error naming the package and telling
you to add `nodejs_compat`. A guarded `require()` that never runs still
does no harm.
