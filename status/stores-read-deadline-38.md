Last update: 2026-11-24 PT. Platform-store read deadline + serve-stale breaker, and outage docs (#38 "needed either way").

# Store read deadline (#38)

## What changed

- `hostd/gen/js/store.js`:
  - `fresh()` waits at most `READ_WAIT_MS` (2 s) for its catch-up, then
    serves the local copy (if the object has synced on this host before).
  - Breaker: once a catch-up fails as unreachable, or outlasts that wait,
    reads serve local data without waiting, and one catch-up is retried in
    the background at most every `STALE_RETRY_MS` (5 s). The first
    successful catch-up closes it. `lease_lost` is never served around;
    a never-synced object still waits and fails.
  - `sync()` coalesces: a caller joins a catch-up that is queued but not
    started, and `fresh(maxAge)` joins one in progress that started within
    `maxAge`. At most one catch-up runs and one waits behind it.
- `hostd/stores.go`: `/v1/store/read` bounds the journal read at
  `storeReadTimeout` (10 s; was the mgmt client's 30 s), answering
  `store_unavailable`. Appends are unchanged (RPO 0).
- `hostd/fakemgmt/storeshold.go`: `HoldStoreReads` / `StoreReadStats`
  (hosts' journal reads hang until released or abandoned).
- Docs: RFC SLO table (tw-mgmt down / store down), ADR-044 §8, ADR-028 §3,
  ADR-036, ADR-037, `docs/kv.md`, `docs/d1.md`, `docs/r2.md`,
  `docs/COMPAT.md`, `docs/errors.md` (`store_unavailable`), and
  `docs/limits.md` (new "Platform store reads while mgmt is slow" table).
  They now say reads serve the host's copy only while the workload runs:
  a workload with a fenceable DO class stops after `FenceAfter` (30 s).

## Tests

- `hostd:TestStoreReadDeadline` (new): with hosts' journal reads hung,
  five concurrent reads each return the local copy in about 2 s; the next
  read returns at once; mgmt saw one journal read (the rest coalesced);
  hostd abandons the hung read (10 s); with the journal down a write is
  refused (`store_unavailable`) and reads don't wait; after mgmt is back
  an admin's append shows up and writes work again.

Commands run are listed in the PR body.

## Not done / for later

- Explicit-consistency reads (D1 `first-primary`, a bookmark ahead of the
  local copy, `tw d1 execute`) still wait for the journal (now up to 10 s
  per journal read) and fail. That is deliberate: they asked for it.
- A write queued behind a hung catch-up waits for it (up to 10 s) before
  its own append. Only matters when reads hang but appends work.
- Taking mgmt off the read/write path entirely is #297 / PR #226.
