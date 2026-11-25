---
kind: fix
upgrade: [protocol]
---
A platform store (D1 in particular) no longer skips a journal entry it can't apply when it catches up or rebuilds from the journal, and no longer reports itself synced past it. It stops before the entry and is degraded: reads answer from what it applied, writes fail with the new error code `store_degraded` (`409` from the mgmt API, `D1_ERROR: [store_degraded] …` in a Worker), and it tries the entry again on later syncs. `tw d1 export` of a degraded database exports what applied, headed by a comment naming the failing entry; docs/d1.md "Recovery" has the steps to rebuild it.
