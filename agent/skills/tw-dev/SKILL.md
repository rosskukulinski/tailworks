---
name: tw-dev
description: Use when running a tailworks app locally with `tw dev` (no tailnet, no credentials), smoke-testing it before a deploy, testing cron triggers locally, or giving it local secrets with .dev.vars.
---

# Run a tailworks app locally

`tw dev` runs the app on this machine with an embedded control plane: no
tailnet, no Tailscale credentials, no `tw up`. Every object still gets its
own (local) node and `getCaller` still works; the caller is a local test
user. There is no policy evaluator in dev: every dev node can reach every
other.

## Smoke test (use this from an agent)

`tw dev --once` starts the app, makes one request and exits. For an MCP
app it sends an MCP `initialize` to the MCP route; otherwise `GET /`.

<!-- tw-eval: dev -->
```sh
tw dev --once --port 0 --json    # {"url", "serves", "workload", "node", "smoke": {"status", ...}}
```

`smoke.status` is the HTTP status the app answered with. A build error
fails with `build_failed` (the compiler output is in the error), a crash
at start with `deploy_failed`.

`--port 0` picks a free port. Without it tw dev listens on
127.0.0.1:8787 and fails if that port is taken.

## Long-running dev server

```sh
tw dev --json                    # NDJSON on stdout: {"event": "ready", "url", ...}, then "reload" / "error" / "request"
```

It reloads on every file change and runs until it's stopped (Ctrl-C).
Each request the app serves is printed with its `console.*` lines and
any uncaught exception's stack (a `request` event with a `record` in
`--json`), so read that output when a request returns 500.
Run it in the background, wait for the `ready` event, then call the `url`
it printed. A failed rebuild prints an `error` event and keeps the last
good version running.

## Local secrets: `.dev.vars`

Put local values in `.dev.vars` in the app directory, dotenv syntax
(`NAME=value`). It is gitignored by the templates; keep it that way.
Ask the user to fill in real values; don't write real keys yourself.

## Cron triggers

`tw dev --test-scheduled` serves `/__scheduled?cron=<expr>` on the local
port, which runs the app's `scheduled()` handler now for that cron
expression (see `tw-cron`).

## Next

When it works locally, deploy it: `tw-deploy`.
