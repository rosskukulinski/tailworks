---
kind: security
upgrade: []
---
Cron triggers: a `scheduled()` run now lasts until the promises it passes to
`ctx.waitUntil()` settle, as on Cloudflare, and is cancelled at the 15-minute
limit. Before, work left in `waitUntil` kept running after the run was
reported done, so the trigger's next run could start beside it. A timed-out
run is logged as `cron trigger timed out` (`outcome=exceededTimeout`).
