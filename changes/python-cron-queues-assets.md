---
kind: feature
upgrade: []
---
Python Workers can now use cron triggers (`scheduled()`), Queues (producer
bindings and a `queue()` consumer, with per-message `ack()`/`retry()`) and
static assets (`assets`, `env.ASSETS`), as JavaScript Workers do. Python
Workflows are still refused. See docs/python.md.
