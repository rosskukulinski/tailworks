Last update: 2026-11-23 12:00 PT. microVM-tier e2e coverage for D1, Queues, Workflows, cron, alarms and WebSockets/hibernation: all six pass on vz.

# vm-feature-coverage

The microVM tier's e2e suite (`e2e/vm`, tag `e2evm`) covered only DO
storage (KV + SQLite), the KV binding, R2, and isolation/egress. This
branch adds a test for each Durable Object or binding feature that was
tested only in the process tier. Each test runs on its own hermetic host
with the tenant's tier set to `microvm`, and checks that the workload
really runs in tier `microvm`.

## What's built

- `e2e/vm/suite_test.go`: shared helpers (`startVMSuite`, `deploy`, `do`,
  `must`, `eventually`, `killVM` (SIGKILL the VMM, wait for a new VM that
  serves), `redeploy`, `vmApp`). `vm_test.go` is unchanged.
- `e2e/vm/d1_test.go` `TestE2EVMD1` (fixture `hostd/testdata/apps/d1app`):
  env.DB in the Worker and in a facet; migrations through hostd's store
  admin op (`d1mig.Apply`); run, a batch, raw reads; a write acknowledged
  just before a VM kill, and every row, survive the kill and a redeploy;
  migrations aren't re-applied.
- `e2e/vm/queues_test.go` `TestE2EVMQueues` (fixture
  `hostd/testdata/apps/queues`): send, a sendBatch delivered as one batch of
  3, a send from a facet, retry() and its redelivery, and a delayed message
  sent before a VM kill delivered by the new VM.
- `e2e/vm/workflows_test.go` `TestE2EVMWorkflows` (`e2e/vm/testdata/vmflow`):
  instances created from the Worker and from a facet run steps (one
  retried) and a sleep to `complete` with the right output; an instance
  asleep (8 s) when its VM is killed completes on the new VM.
- `e2e/vm/cron_test.go` `TestE2EVMCron` (`e2e/vm/testdata/vmcron`):
  `Host.RunCron` runs scheduled() in the guest (outcome ok, scheduledTime,
  ctx.tailnet present). The host's own scheduler loop, on the real clock,
  then fires `* * * * *` at the next minute boundary (up to 60 s).
- `e2e/vm/alarms_test.go` `TestE2EVMAlarms` (`e2e/vm/testdata/vmalarm`):
  setAlarm → alarm() (with getAlarm pending, then cleared) for a facet (the
  Host-DO shim) and a plain-DO object. Alarms set before a VM kill and due
  after it fire on the new VM, in both modes.
- `e2e/vm/websockets_test.go` `TestE2EVMWebSockets` (`e2e/vm/testdata/vmws`),
  facet and plain-DO subtests: ctx.acceptWebSocket, two clients, replies and
  a broadcast. Both clients then idle 14 s. The test asserts that the object
  was evicted (new boot ID), the sockets stayed open, and each socket's
  serializeAttachment state is intact.
- `docs/COMPAT.md`: the new tests are listed in the WebSockets, Alarms,
  Hibernation, D1, Queues, Workflows and Cron rows.

## Results (mini, Apple Silicon, macOS, vz backend)

- `go test -tags e2evm -run 'TestE2EVM(D1|Queues|Cron|Workflows|Alarms|WebSockets)$' ./e2e/vm`
  with the VZ env: first run 5/6 pass. D1 failed on a test bug: a POST
  without a body, and the fixture calls `req.json()`. After the fix,
  `TestE2EVMD1` passes.
- `TW_DEV_VM=twd-dev make e2e-vz` (vz assets symlinked from the main
  checkout's `dist/vz-assets-arm64` cache): PASS. `hostd/vm` 7.1 s; `e2e/vm`
  108.7 s: Alarms 9.9 s, Cron 7.3 s, D1 5.2 s, KV 4.5 s, Queues 13.0 s,
  R2 7.0 s, E2EVM 18.6 s, WebSockets 30.8 s, Workflows 12.0 s; Snapshot
  skipped (Firecracker only).
- `make lint test` (after merging origin/master, already up to date): PASS (exit 0, no FAIL lines).

No feature failed or needed a skip: all six work in the microVM tier.

## Skipped

- `make e2e-vm` (Firecracker, Linux + KVM): not run. Mini can't run it
  without nested virtualization in Lima. The new tests are not
  darwin-specific (`go vet -tags e2evm` passes with GOOS=linux).

## Notes

- With vz, every VM cold-boots (no pool), so the six tests together add
  about 80 s to `make e2e-vz`. That is well inside its 15 m timeout.
- The cron test waits for a real minute boundary (0 to 60 s).
