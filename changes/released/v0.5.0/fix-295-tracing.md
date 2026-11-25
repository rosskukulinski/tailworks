---
kind: feature
upgrade: []
---
Traces now cover Durable Object RPC, alarms and cron runs. An RPC call
(`stub.method(...)`) gets a CLIENT span in the caller and a SERVER span in
the object, in the caller's trace. Each alarm and each cron run starts its
own trace, so their records in `tw logs --source requests` now carry a trace
ID. Egress, `ctx.tailnet.fetch()` and DO calls made inside them join that
trace. What is still not traced is listed in `docs/observability.md`.
