---
kind: feature
upgrade: []
---
Platform stores: a SQLite Durable Object per store, backed by a fenced, encrypted journal in tw-mgmt, so a store survives restarts and moves between hosts. KV, D1, R2 and Queues build on it.
