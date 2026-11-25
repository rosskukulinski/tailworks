---
kind: feature
upgrade: []
---
`tw deploy`, `tw dev` and `tw policy` run a wrangler config's custom build
(`"build": {"command": "…"}`) before the built-in esbuild bundles, as
wrangler does: in a shell, in `build.cwd`, with the project's
`node_modules/.bin` on `PATH`. They print `› build      <command>` and
stream its output on stderr; a failing build stops the deploy with
`build_command_failed`. `tw dev` runs it at start and again only when a
file under `build.watch_dir` (default `./src`) changes, so the build's own
output can't trigger a reload loop. Projects such as Cloudflare's
durable-chat and multiplayer-globe templates no longer need `--wrangler`.
A Pages project, a pre-built `no_bundle` Worker and a `runtime: "node"` app
don't run it; other `rules` / `no_bundle` configs still need `--wrangler`.
