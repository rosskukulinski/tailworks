Last update: 2026-11-25 PT. Issue #523: tw d1 execute reads SQL from stdin.

## What changed

- `cmd/tw/d1stdin.go`: `d1ExecuteSQL` (--command, --file, or stdin) and
  `d1Input` (stdin + is-a-terminal, replaceable in tests). With neither
  flag and stdin not a terminal, the SQL is read from stdin, at most
  `mgmt.D1BodyLimit` (8 MiB; the constant is exported from mgmt/d1.go,
  not copied). Both flags together are still refused. On a terminal the
  error is "tw d1 execute needs --command <sql>, --file <file>, or SQL on
  stdin"; an empty pipe gets "no SQL statement to run: pass …".
- The SQL is read before mgmt is contacted (the old flag check's place).
- Usage `d1 execute <db> [--command <sql> | --file <file>]`; --file help
  names the stdin form; docs/cli.md regenerated.
- Docs: d1.md operator table row (`< schema.sql`, `export | execute`
  copy, 2 MiB per write), COMPAT.md d1-cli row, ADR-038 §5, limits.md
  citations renamed to `D1BodyLimit`. The Recovery recipe keeps `--file`.

## Tests

- `go test ./cmd/tw -run 'TestCLID1|TestSubcommandHelp'`: ok (TestCLID1
  now covers the TTY error, both flags, stdin rows, an empty pipe, and
  over-limit stdin)
- `go test ./mgmt -run D1`, `go test ./docs/... -run TestLimitsDocCitations`: ok
- `make lint`: ok
- `make test` (mbp-test.sh, both slots busy with other runs): found the
  stale subcommand-help expectation and limits.md citations (fixed); also
  TestCLISingleProcess, TestCLIOAuth and hostd TestFenceClosesOpenWebSockets
  failed with no assertion message / a timing bound under load; they don't
  touch d1 execute. CI's `make test` is the rerun.
