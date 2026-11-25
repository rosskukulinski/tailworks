# Routing loop ledger

Issue #422. Process: `LOOP.md` ("Routing" for this loop's workloads,
faults and invariants N0–N6). Findings are filed as GitHub issues labelled
`dataplane-chaos`.

## Status

| Scenario | Clean seeds | Notes |
|---|---|---|
| public | 1, 2, 3 | Seed 1 counts its rerun (the first run hit a harness bug) |
| bindings | 1, 2, 3 | Seed 2 counts its rerun (#593 before `revoke-binding` redeployed on the same host) |
| websockets | 1, 2, 3 | Seed 1 after the #572 fix; seed 2 counts its rerun (N5 crash case, now a note) |
| routing | 2, 3, 4 | Seed 1 found #572; seed 2 counts its rerun (N4 checker fix) |

All 4-minute runs on macOS (18 cores, shared). Linux (lima twd-dev, arm64):
`public` 11, `bindings` 11, `websockets` 11 and 12 (`hibernate` and `move`
only), and `routing` 11 (rerun, after #593's workaround) are clean.
Nightly runs `make e2e-dataplane-routing` (`routing`, today's date as the
seed).

## Findings

| ID | Verdict | Invariant | Issue | Summary |
|---|---|---|---|---|
| R1 | FIXED (#579) | N1, N5 | #572 | A fenced stateful workload kept answering WebSockets that were already open for up to 3 s after its lease was revoked. `undeploy` stopped workerd first (SIGTERM, then up to 3 s of graceful drain) and reset the nodes' netd flows only after that; #501's stall fence gates new requests, not frames on an upgraded connection. After a takeover of a stalled host the old copy answered 105 (routing 1) and 159 (websockets 1) messages sent after the new copy was serving. Fix: close a fencing workload's nodes before stopping workerd. Regression: `hostd:TestFenceClosesOpenWebSockets` (closed 3.0 s after the revoke with 122 late answers → at the revoke, none) |
| R2 | CONFIRMED, open | N0 | #593 | Redeploying a workload after an undeploy onto a host that once held it never gets ready: the host rejoins the entry node from saved state, but mgmt purged that object at the undeploy and told only the lease holder, so the ready report is `not_found` and nothing re-creates it. Seen on Linux (routing 11) and macOS (bindings 2); deterministic in `TestRedeployAfterUndeploy` (skipped until fixed; `TW_DP_KNOWN_FAILURES=1`). The fix changes hostd's object-identity path (re-confirm a non-live object with mgmt, keep fail-static restarts), so it waits for a decision. `revoke-binding` now redeploys onto the host it undeployed from |

## Hypotheses

| ID | Status | Hypothesis | Result |
|---|---|---|---|
| HR1 | OPEN | A WebSocket that is open when its host's hostd crashes (SIGKILL) is ended by a reset from the restarted node | Not always: in websockets 2 the host was stopped again 5 s after the restart and the workload moved away, so its node never answered again and the 3 sockets stayed open (black-holed) for 4.5 min, until the run ended. Nothing on the server remembers a connection across a crash; only the client's own TCP timeout or a WebSocket ping ends it. N5 reports these as notes ("left open across a host crash") and still fails a socket that is black-holed without a crash. Clients that must notice should ping |

## Harness notes (not product bugs)

- **testcontrol keeps duplicate names.** mgmt deletes a moved object's
  previous device through the API, which the fake API ignores, and a node
  rejoining from saved state can come back as a new testcontrol node while
  the old one stays. Several nodes then share one MagicDNS name, and
  netd's name lookup (`resolveEgress`) takes the first match from an
  unordered map, so after a move back a binding call could go to a dead
  node and hang until its TCP connect timed out (a binding call took 132 s
  and failed `dial_failed`). Real control keeps names unique. The harness
  now keeps them unique in testcontrol (`dedupeNames`): the node that
  started last owns the name. The first version renamed nodes for good,
  which broke a public node that rejoined as the same node (`public` 1:
  `public_name_taken`, N0); it is a rerun.
- **Reordered WebSocket replies** in the first `routing` run were the
  workload's doing: `webSocketMessage` awaited an RPC (`tailnet.self()`),
  which lets the next message in. The Echo object now reads its identity
  once, in `blockConcurrencyWhile`.
- **N4 and the deployment watcher.** The watcher polls mgmt every 300 ms,
  so a `/gate` request answered `not_public` right after a route change
  could be judged against the previous listing (routing 2). Requests in a
  route change's window (+10 s) are no longer judged; routing 2 is clean
  on replay.
- **The `hibernate` fault didn't evict anything at first:** the per-second
  probe of the WebSocket object kept it active. The probes now go quiet
  with the clients; Linux websockets 12 saw evictions (5 object boots, no
  message lost).
- **Binding latency after B moves** is 0–9 s in clean runs (cache TTL 5 s
  plus a first path to the new node, `PeerPathWait` 5 s); N1 bounds it at
  20 s.

## Run log

| Run (results dir) | Scenario / seed | Verdict |
|---|---|---|
| 20261125T062207Z-routing-1 | routing 1, 30 s, `move` only (smoke) | pass. bind-rpc took 33 s to recover after B moved off A's host: found to be duplicate names in testcontrol (harness note) |
| 20261125T063252Z-bindings-3 | bindings 3, 90 s, `move` of B only, 300 s client timeout | HARNESS: one binding call took 132 s and failed `dial_failed` (a dead node with B's name) |
| 20261125T064831Z-bindings-3 | same, with `dedupeNames` | pass. Binding calls during a move take 9–15 s and succeed |
| 20261125T065209Z-routing-1 | routing 1 | **N1 fail: R1 (#572)**, 105 stale WebSocket answers after a takeover of a stalled host; N5 reorders were the workload's (fixed) |
| 20261125T065756Z-websockets-1 | websockets 1, `pause-takeover` of rt-ws, 1 min | **N1 fail: R1**, 159 stale answers, on master after #501's fix |
| 20261125T070807Z-websockets-1 | same, with fix-572 | pass, 0 stale |
| 20261125T071014Z-public-1 | public 1 | HARNESS: N0, the first `dedupeNames` renamed a public node that rejoined as the same node (`public_name_taken`) |
| 20261125T071628Z-bindings-1 | bindings 1 | pass |
| 20261125T072120Z-websockets-1 | websockets 1 | pass |
| 20261125T072712Z-routing-2 | routing 2 | N4 checker false positive (watcher lag); pass on replay |
| 20261125T073229Z-public-2 | public 2 | pass |
| 20261125T073720Z-bindings-2 | bindings 2 | **N0 fail: R2 (#593)**, B redeployed onto a host that once held it never got ready |
| 20261125T074423Z-websockets-2 | websockets 2 | N5: 3 sockets open across a `kill-host` black-holed (HR1); pass on replay with the crash case as a note |
| 20261125T074938Z-routing-3 | routing 3 | pass |
| 20261125T075453Z-public-3 | public 3 | pass |
| 20261125T080031Z-bindings-3 | bindings 3 | pass |
| 20261125T080606Z-websockets-3 | websockets 3 | pass |
| 20261125T081109Z-routing-4 | routing 4 | pass |
| 20261125T081631Z-public-1 | public 1 (rerun) | pass |
| 20261125T082144Z-bindings-2 | bindings 2 (rerun) | pass |
| 20261125T082635Z-websockets-2 | websockets 2 (rerun) | pass |
| 20261125T083149Z-routing-2 | routing 2 (rerun) | pass |
| Linux 20261125T071111Z-websockets-11 | websockets 11 | pass |
| Linux 20261125T071702Z-public-11 | public 11 | pass |
| Linux 20261125T072318Z-bindings-11 | bindings 11 | pass |
| Linux 20261125T072813Z-routing-11 | routing 11 | **N0 fail: R2 (#593)**, found here first |
| Linux 20261125T074048Z-routing-11 | routing 11 (rerun, `revoke-binding` on the same host) | pass, 3 revocations: no binding answer from a revoked copy at all |
| Linux websockets-12 | websockets 12, `hibernate` and `move`, 2–3 min | pass; after the probe fix 5 object boots (evictions), 0 lost |
