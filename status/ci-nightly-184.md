Last update: 2026-11-21 PT. #184 was a one-off npm propagation 404. The nightly now closes its tracker when green, and test-pg containers no longer collide.

# ci-nightly-184

## #184: why the 2026-11-18 nightly failed

- Only `templates-typecheck` failed (run 44267016229). `agent`, `mcp` and
  `refund-approval` got `npm error 404 … js-base64-3.9.7.tgz - Not found` at
  08:27:53–08:28:41 UTC.
- `npm view js-base64 time`: 3.9.7 was published at 2026-11-18T08:25:23Z,
  about 2.5 minutes earlier. The packument already listed it, but the tarball
  wasn't on the registry CDN yet. Template apps have no lockfile, so the
  transitive `js-base64@^3.7.7` picked the new version (the repo lock pins
  3.9.6).
- The tarball serves 200 now. The next three nightlies passed at the same
  commit, b43256f. It wasn't our code and wasn't the @tailworks 0.2.0
  publish. I left templates-typecheck alone: it resolves fresh on purpose, to
  check what users get. Evidence is on the issue; I closed #184.

## nightly.yml report job

Before, it only commented on or opened the "nightly suites failing" tracker,
and never touched it after a green night, so #184 stayed open after three
passing runs. Now a green scheduled run closes the open tracker with
"Passing again on <date> at <sha>: <run>". The next failure opens a new one.
Known failures (`allow_failure`, continue-on-error) count as success, the
same as before.

## scripts/test-pg.sh: no more collisions between worktrees

- The container is `tw-test-pg-<pid>` (or `TW_TEST_PG_NAME`), not the fixed
  `tw-test-pg`. Before, a second run's `docker rm -f tw-test-pg` killed the
  first run's database.
- With no port, docker publishes on a free port and the script reads it with
  `docker port`. `make test-pg PG_PORT=55433` (the old default) still pins
  it. The Makefile's `PG_PORT` now defaults to empty.
- The default log is `/tmp/<name>.log`, not one shared
  `/tmp/tw-test-pg.log`, and the script prints its path. `TW_TEST_PG_LOG`
  and `TW_TEST_POSTGRES_DSN` (CI) work as before.

## Tests

- The report step's script, pulled out of the YAML and run with a stub `gh`:
  green with an open tracker closes it with the comment; green with no
  tracker does nothing; red with a tracker comments; red with none creates
  one.
- `make test-pg` with no DSN: the container came up as
  `tw-test-pg-88787 127.0.0.1:32768->5432/tcp`, next to another test
  Postgres on 55491. It finished ok, ok, printed
  `full log: /tmp/tw-test-pg-88787.log`, and removed the container.
- `make lint`: ok. `make test`: ok.
