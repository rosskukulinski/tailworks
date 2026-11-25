Last update: 2026-11-24 16:30 PT. Python Workers: cron triggers, Queues and static assets work (follow-up to status/python-support-292.md).

# python-cron-queues-assets

Continues `status/python-support-292.md` (PR #405, ADR-055). ADR-055 is
amended with decision 8.

## What changed

- `hostd/gen/python.go`: the generated JS front of a Python Worker gets
  `scheduled()` (calls tenant-py's through `Fetcher.scheduled()`, carries
  back `noRetry`, throws on failure) and `queue()` (calls `Fetcher.queue()`
  with the batch's messages, then replays explicit acks, per-message
  retries, `ackAll` and `retryAll` onto the real batch, and throws if the
  Python handler did). The front gets `service_binding_extra_handlers` when
  the app has crons or queue consumers. tenant-py gets the queue producer
  bindings and `env.<assets.binding>`.
- `hostd/bundle/python.go`: crons, queues and assets are no longer refused
  (Workflows and containers still are).
- `cmd/tw/python.go`: an `assets.directory` under main's directory isn't
  also packed as modules.
- Compat suite: `py-cron` and `py-assets` flipped from expected refusals to
  passes (Cloudflare's examples, unmodified).

## Tests run

- `go test ./hostd/gen -run Python` (new: TestPythonCron: ok, exception,
  noRetry; TestPythonAssets: router + env.ASSETS): pass.
- `go test ./hostd -run TestPython` (new: TestPythonQueues on real hostd:
  send, consume, per-message retry, a throw retrying the batch, no
  redelivery after ack): pass.
- `go test ./hostd/bundle -run Python`, `go test ./cmd/tw -run Python`: pass.
- `TW_COMPAT_ONLY='^(py-|c3-python$)' make e2e-compat-dev`: 8 PASS, 2 XFAIL.
- `TW_COMPAT_ONLY='^(py-|c3-python$)' make e2e-compat` (tailnet 1): 9 PASS, 2 XFAIL.

## Gaps

Python Workflows; Python Durable Objects with their own node; the microVM
tier; memory snapshots.
