# Contributing to tailworks

Thanks for helping. This page is the short version. The full checklist
for a pull request is "Definition of done" in [AGENTS.md](AGENTS.md),
which applies to people and coding agents alike.

Security problems go through [SECURITY.md](SECURITY.md), not a public
issue or pull request.

## Set up

You need Go ≥ 1.27 and Node.js ≥ 20 ([prerequisites](docs/INSTALL.md#prerequisites)),
on macOS or Linux.

```sh
git clone https://github.com/rosskukulinski/tailworks && cd tailworks
make build        # every binary into bin/
make test         # hermetic unit and integration tests: no tailnet, no credentials
```

The design is in [docs/design/](docs/design/): the RFC and one file per
ADR. How the pieces fit together: [docs/concepts.md](docs/concepts.md).

## Before you open a pull request

1. Merge `origin/master` into your branch, then run `make lint test`.
   CI runs the same two on every pull request, and master only accepts a
   branch that is up to date with it.
2. Run the extra suites for what you touched. The table in
   [AGENTS.md › Definition of done](AGENTS.md#definition-of-done-before-you-open-or-merge-a-pr)
   lists them (`make test-pg` for the mgmt store, `make e2e-queues` for
   Queues, `make templates-typecheck test-sdk` for templates and SDKs,
   and so on). Suites that need credentials, a real tailnet, Docker or
   KVM are fine to skip; say so in the pull request.
3. Regenerate what is generated, and commit the output:
   - `make api` after editing `api/openapi.yaml`. Never edit the
     generated `*.gen.go` files or `docs/contracts/mgmt-api.md` by hand.
   - `make cli-docs` after changing a command, flag or help text
     (`docs/cli.md`).
   - `tw types` in a template after changing its bindings
     (`worker-configuration.d.ts`).
4. Add what is registered by hand:
   - a new error code goes in [docs/errors.md](docs/errors.md), with
     "When" and "Fix";
   - a new CLI command is one entry in `commands` in `cmd/tw/main.go`.

The `## <target>:` comment above each target in the Makefile says what
it runs and what it needs. Tests that check the docs against the
code run in `make test`: every `tw <command> --flag` named in README.md
and `docs/*.md` must exist (`TestDocsNameRealCommands`), and
`docs/cli.md` must match `--help` (`TestCLIReferenceUpToDate`). Its
Environment section comes from `cmd/tw/envvars.go`: a new `TW_*` variable
goes there (`TestCLIReferenceEnvVars`).

## Release notes: `changes/<branch>.md`

Release notes are built from one fragment per branch, not from a shared
file. When `make changes-check` (part of `make lint`) asks for one, add
`changes/<your-branch>.md`. The format and the upgrade kinds are in
[changes/README.md](changes/README.md). At release time
`scripts/release-notes.sh --finalize` moves the fragments to
`changes/released/<tag>/` and regenerates [CHANGELOG.md](CHANGELOG.md);
don't edit CHANGELOG.md by hand ([docs/RELEASING.md](docs/RELEASING.md)).

## Status notes: `status/<branch>.md`

Write what your branch built, the commands you ran with their results,
and the gaps you know of in `status/<your-branch>.md`. Touch no other
file in `status/`, and don't edit `STATUS.md`, which is frozen. Details:
[status/README.md](status/README.md).

## Keeping parallel branches mergeable

Many branches are open at once. [AGENTS.md](AGENTS.md) has the rules that
keep them from conflicting; the main ones:

- put new feature code in new files, and keep the edit to a shared file
  to one line where you can;
- one item per line in lists, enums and composite literals, inserted in
  sorted position rather than at the end;
- name a new ADR `ADR-<slug>` until it merges; it gets its number then.

## Pull requests

Say what changed and why, and include a **Tests** section listing the
commands you ran and their results, and anything you skipped. Link the
issue (`Fixes #N`).

By contributing you agree that your contribution is licensed under the
repository's [LICENSE](LICENSE) (BSD 3-Clause).
