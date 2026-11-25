Last update: 2026-11-24 PT. #464 (`tw host rm --force`) and #477 (`tw host ls` OFFERS/ARCH, "Node runtime").

# host-rm-force-offers

Two issues from the interface-simplification audit (#434).

## #464: `tw host rm --drain` is now `--force`

- `cmd/tw/host.go`: `--force` kills the host's objects and releases its
  leases (the API's `?drain=true`, unchanged). `--drain` is still accepted
  and listed, with the usage "deprecated: same as --force". The
  `host_holds_leases` fix now offers `tw host drain <id>` first, then
  `tw host rm <id> --force --yes`.
- mgmt `lease_held` (pinned move away from a live holder) now says to
  redeploy with `--replace` (or `tw host drain <id>`), and gives
  `tw host rm <id> --force` only for a host that is gone for good.
  `takeover_unconfirmed`, `host_identity_mismatch` (auditPubKey rotation),
  the `exists` hints (cli `host add`, `tw up`) say `--force` as well.
- docs: errors.md (`exists`, `host_holds_leases`, `lease_held`), INSTALL.md,
  locks.md; api/openapi.yaml's `takeover_unconfirmed` row (then `make api`).
- Admin UI wording was already done by #505 ("Remove host", Force
  checkbox, still `?drain=true`); not touched here.
- Left as is: ADR-033, ADR-041 and e2e/multimgmt/LEDGER.md mention
  `host rm --drain` as history.
- An older CLI that reads a new mgmt's hint doesn't know `--force`; it
  must use `--drain`. Noted in the release-note fragment.

## #477: what a host offers

- `tw host ls`: column `TIERS` is `OFFERS`, 26 wide (fits
  `process,microvm,containers`), new `ARCH` column (`-` when the host never
  reported one). `--json` rows gain `arch` (omitted when empty) and
  `features` (always a list); `schema/cli.schema.json` "host ls" lists them.
- `tw status` host line says `offers`; the admin UI overview and host page
  say "Offers" instead of "Tiers".
- docs/containers.md: "hosts that offer containers"; the "Host tiers"
  section is "Hosts that offer containers" (old `#host-tiers` anchor kept).
- "Node runtime" instead of "Node tier" in node-tier.md (title "The Node
  runtime"; filename kept), COMPAT.md (section renamed, `#node-process-tier-m9`
  anchor kept), errors.md, testing.md, security-model.md, limits.md,
  INSTALL.md, ADMIN-UI.md, cron-triggers.md, tailworks-jsonc.md, and the
  user-visible CLI strings (`tw logs --console` help, `tw status`'s sandbox
  note, `tw doctor` node_egress and side_channels). Code comments, tw-hostd's
  flag help (a hotspot file) and the ledgers are unchanged.

## Tests

- `go test ./cmd/tw -run 'TestHostLsRmCLI|TestCLIJSONContract|TestDoctorSideChannels'`:
  ok. New `cmd/tw/host_rm_test.go` (`TestHostLsRmCLI`): `host ls` against a
  real mgmt prints OFFERS/ARCH and `--json` has `arch`/`features`;
  `host rm` without `--force` is `host_holds_leases` with the drain-first
  fix; `--force` and `--drain` both remove a host holding a lease.
- `mgmt.TestTakeover...` pinned-live case asserts the new `lease_held` text.
- `make api cli-docs`: regenerated docs/cli.md, the API generated files and
  docs/contracts/mgmt-api.md.
- `make lint` (after merging origin/master `6726276c`): ok.
- `make test`: everything passed except two timeouts on a heavily loaded
  machine, both unrelated to this branch: `netd TestService/KillWithdraws`
  (a localapi context deadline) and `hostd TestTelemetry/EntryTraces/ObjAlarm`.
  Rerun on their own, `go test ./netd -run 'TestService$'` and
  `go test ./hostd -run 'TestTelemetry$'`: both ok.
- `make e2e-placement`: ok (36.5 s).
- Merged origin/master again (`8cbbbd40`, #504/#506/#510; one conflict in
  docs/tailworks-jsonc.md's `name`/`runtime` rows: took master's `name`
  row, kept "Node runtime"). `make lint`: ok. `go test ./cmd/tw/...
  ./mgmt/... ./schema/...`: ok. `make test`: ok except two load timeouts
  (`mgmt TestRT3_H23_PlacementPoison`, a heartbeat that missed its 10 s
  window; `hostd TestR2Binding/fs/worker/Multipart`, hostd's run took
  1060 s). Rerun on their own: both ok.
