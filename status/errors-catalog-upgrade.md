Last update: 2026-11-25 PT. Issues #529 and #532: every code tw emits is in docs/errors.md (with a test), and tw up records installPrefix in tw.json.

# errors-catalog-upgrade

## What changed

- **docs/errors.md** (#529): new entries `network`, `no_cgroup2`,
  `prefix_mismatch`, `upgrade_failed` (Fix by outcome: rolled back, or
  not rolled back / rollback failed → `tw down`, `tw restore <backup> --yes`,
  `<prefix>/install.sh --rollback`, `tw up`), and the dynamic
  `upgrade_paused` / `upgrade_aborted` (`fail("upgrade_"+run.State)`;
  the emitted code is unchanged).
- **TestDocRefCheckErrorCodes** (`cmd/tw/errors_catalog_test.go`): every
  `fail("<code>",` literal in cmd/tw's non-test sources, plus the dynamic
  list, has a `### <code>` heading. The name matches `make test-docs`'s
  `-run` filter, so docs-only branches run it too.
- `e2e/agenteval`'s `codeHeading` accepts digits (`d1_error`, `r2_*`,
  `no_cgroup2`).
- **tw.json `installPrefix`** (#532): `tw up` records the install.sh prefix
  of the tw it runs (`""` for npm, dev builds, checkouts; always
  overwritten). `tw upgrade` uses it after the live service-unit and
  supervisor candidates and before "the state dir is the install prefix",
  and only while `isInstallPrefix` still holds. Additive field, no `v` bump.
- **docs/upgrading.md** (#532): one paragraph on which install is
  upgraded (now naming tw.json and `prefix_mismatch`); the 0.1.0 → 0.2.0
  recipe is a link to the v0.2.0 guide; "Roll back by hand" is its own
  section and uses `tw restore`.

## Follow-ups (not in this change)

- Daemon-emitted codes (e.g. `image_unavailable` in the openapi ErrorCode
  enum) aren't checked against errors.md; a later test could check the enum.

## Tests

- `make lint`: ok.
- `make test-docs`: ok (includes TestDocRefCheckErrorCodes).
- `go test ./cmd/tw -run 'TestResolvePrefix|TestPrefixOfBinary|TestServiceBinary|TestDocRefCheckErrorCodes'`: ok.
- `make test` (mbp runner): cmd/tw and everything else ok except two
  packages this branch doesn't touch: mgmt `TestRegistryAccess` (no
  heartbeat within 10s under load; passes on rerun) and hostd (a
  `closeOnce` panic in objects.go, then on a local rerun
  `TestContainerPolyfill`), both unrelated to these files.
- `make e2e-tw-upgrade`: ok (TestTwUpgradeSingleMachine, TestUpDownBudget).
- `make changes-check`: ok, no fragment needed.
