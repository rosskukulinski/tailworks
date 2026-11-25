---
kind: feature
upgrade: [schema-expand, protocol]
---
mgmt now knows when a platform store is degraded (its object on a host stopped its replay at a journal entry it can't apply, `store_degraded`). The host reports it when it happens and when it heals; mgmt records it on the store (`degraded` on the stores API: the entry's seq, the error, the host and when), audits it (`store_replay_failed`), and shows it in `tw d1 list`, `tw kv namespace ls`, `tw r2 bucket ls`, `tw status` (a new `GET /v1/stores/degraded`) and the console's overview. New: `tw d1 repair <db> --skip <seq> --yes` (`POST …/stores/{kind}/{name}/repair`) deletes the one entry the database is degraded at from its journal, so every host's replay goes on to the next one and the writes after it are kept; it is audited (`store_repair`) and refused with the new error `store_not_degraded` for any other entry. docs/d1.md "Recovery" covers both ways out (skip, or rebuild from an export).

Operator action: none. The migration adds five columns to `stores`. A new tw-hostd against an older tw-mgmt logs a warning when a store is degraded (the report has nowhere to go) and otherwise works as before.
