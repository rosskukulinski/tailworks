Last update: 2026-11-21 22:30 PT. Issue #200: a node control rejects after Ready is re-minted.

## What was wrong

A node that rejoins from saved state with tailscale's netmap cache
(`profile-data/<profile>/netmap-cache`) goes Running from the cached
netmap before control answers. `tsnet.Up` returns, netd marks the object
Ready, and control's NeedsLogin arrives afterwards. `watchNeedsLogin` only
ran inside `Object.up`, so nothing re-minted, and the object stayed
"ready" on a dead node.

The cache is written only when control sends the `cache-network-maps`
nodeAttr. Production control asks clients for it (capability version 149).
testcontrol doesn't send it, so the new tests turn it on with
`SetNodeCapMap`. With the cache on, the bug reproduces every time, not
the 7/80 rate the issue saw.

## Decision: keep watching after Ready

There were two options. One was to hold Ready on a rejoin until a live
(not cached) map response arrives. The other was to keep watching for
NeedsLogin after Ready and re-mint then. I chose the second:

- It also covers control rejecting a node later (key expired, device
  removed), which waiting at join time can't.
- It keeps what the cache is for: a node that serves its peers while
  control is unreachable after a host restart. Waiting for a live map
  would block Ready until control answers.

## What changed

- `netd/rejected.go` (new): every ready node runs `watchRejected` on its
  IPN bus. The watch asks only for state and health, not netmaps or peers.
  If NeedsLogin or a NodeKeyExpired login-state error lasts
  `needsLoginGrace` (2 s), `rejectReady` does the following:
  - moves the object Ready→Failed under `o.mu`;
  - resets its flows and tears the node down;
  - removes `tailscaled.state`;
  - writes a `remint` mark in the object dir;
  - calls `Config.OnRejected(id)`.
  ErrMessage doesn't count after Ready, because it can carry unrelated
  control errors.
- `netd/registry.go`: there is a new `Config.OnRejected` field and a new
  `Object.rejectedCancel` field. `start()` launches the watcher and sends
  `KeyRequest.Remint` when the mark is present, then clears the mark once
  the node is up, so a failed attempt asks again. `teardown()` cancels the
  watcher before `srv.Close`, because tsnet's LocalAPI `Shutdown` would
  otherwise wait for the open bus stream.
- `hostd/noderejected.go` (new) and a one-line hook in `hostd/tenant.go`:
  `onNodeRejected` restarts the node of a ready, live, non-killed object
  through `startNodeLocked`. The keySource calls mgmt Remint, and
  awaitNode reports the new node ID.
- `cmd/tw-netd`: `OnRejected` re-creates the object.

## Interaction with kill/close and #217

- Kill and Close set their own state first, under `o.mu`. `rejectReady`
  acts only if the state is still Ready and `o.lc` is the same client. A
  tombstoned kill, which keeps the node up while mgmt deletes the device,
  stays killed (`TestRejectedAfterKillStaysKilled`).
- hostd ignores the callback unless the object is `statusReady && live`.
  Workload stop and public restart set `live=false` and Kill first.
- The restart bumps `nodeGen`. A pending `retryReady` (lease_lost,
  `hostd/readyretry.go`) checks `Status == statusReady && nodeGen == gen`
  and stops, so it never reports the dead node after the re-mint.
  `TestReadyRetriesAfterLeaseLost` passes 30/30 under load alongside.
- mgmt marks an object killed before it deletes the device, so a re-mint
  that races a kill gets 410 killed and can't bring the node back.

## Tests

New tests:

- `TestRejoinFromNetmapCacheRejectedRemints` (netd): rejoin with the cache
  present while control rejects the node. It covers the persistent case
  and the ephemeral case where the logout was lost.
- `TestReadyNodeRejectedLater` (netd): a node that is already up has its
  key expire.
- `TestRejectedAfterKillStaysKilled` (netd).
- `TestReadyNodeRejectedRemints` (hostd): end to end. mgmt gets a second
  ready report with a new node ID, and the probe answers on it.

Stress runs (local, Apple Silicon). The load was 6 busy loops plus 2
loops of other netd `-race` tests:

- Before the fix, with the same test minus the `OnRejected` hook (a
  prebuilt binary):
  `-race -test.count=30 -run '^TestRejoinFromNetmapCacheRejectedRemints$'`
  failed **30/30** (60/60 subtests): `Rejoined:true Reminted:false`,
  still ready on the old node.
- After the fix, the same command: **30/30 pass**. At that point the test
  also required `OnRejected` to be called, so all 60 subtests went
  through the after-Ready path.
- On the mbp runner, one `make test` run had the ephemeral subtest
  rejected *before* Ready: control's answer beat the cache, and up()
  re-minted the node itself. The test now accepts either path, logs which
  one ran, and still requires the re-mint.
- `go test -race -count=30 -run '^(TestRejectedAfterKillStaysKilled|TestReadyNodeRejectedLater|TestRejoinExpiredStillLoggedInRemints|TestRejoinRejectedRemints)$' ./netd/`,
  run alongside the hostd stress: ok.
- `go test -race -count=30 -run '^(TestReadyNodeRejectedRemints|TestReadyRetriesAfterLeaseLost)$' ./hostd/`,
  under load: 30/30 pass. The first version of the test failed 7/30
  because it compared against mgmt's node ID before the first ready report
  had landed. That was a test bug, now fixed.
- The hostd test without the `tenant.go` hook (prebuilt binary) fails,
  because mgmt keeps the old node.

Other suites:

- `make lint`: ok. `make changes-check`: ok (nothing upgrade-relevant). A
  `changes/` fix fragment is included anyway.
- `make test` (mbp runner): the first run caught a `-race` report in
  `TestEgressWaitsForNewPeerPath`. Netd tests were writing `OmitAuth`
  again on a registry node's LocalClient, which netd had already set,
  while the new watcher reads it. I removed those redundant writes in
  `netd/peerpath_test.go` and `netd/service_test.go`. The rerun was ok.
- No AGENTS.md table suite applies. This doesn't touch the mgmt store,
  placement, locks, queues, observability, templates, wrangler, upgrade or
  install.

## Gaps

- The re-mint uses mgmt's Remint with reason `rejoin_rejected`, the same
  as the rejection path at join time. There is no separate reason for
  "rejected later".
- A device an admin deletes on purpose while the workload is still
  deployed gets re-minted, the same as a restart already does with
  rejoin-rejected state. Stopping that needs mgmt to tell the host, which
  is out of scope.
