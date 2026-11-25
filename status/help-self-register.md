Last update: 2026-11-15 PT. Help groups set per command; definition of done in AGENTS.md.

# Help groups set per command; definition of done

## What's built

- Each entry in `commands` (`cmd/twd/main.go`) now carries its `twd help`
  group and short line (`group:`, `short:`).
- `cmd/twd/helpgroups.go` keeps only the group titles and their order.
  The `helpGroups` name lists and the `shortHelp` map are gone, so a new
  command edits one line (its own) instead of three shared lists. Those
  lists are what broke master after #54, #55 and #57 (fixed in #65).
- `TestHelpGroups` checks the same things as before: every visible command
  is in a group, has a short line of at most 60 characters, and is listed
  once.
- `twd help`: within a group, commands now follow `commands` order. Four
  groups changed order:
  - Set up: doctor before host.
  - Develop: docs, mcp, agent.
  - Observe: audit before logs.
  - Operate: completion before upgrade.
  Every short line is unchanged.
- `AGENTS.md`: a new "Definition of done" section covers CI and the
  up-to-date rule, which extra suite to run for which change, the generated
  and registered artifacts, status files, and the PR test section.

## Tests

- `go vet ./cmd/twd`, then `go test -count=1 -run 'TestHelp|TestCompletion|TestCLI' ./cmd/twd`: ok.
- `make lint test`: see the PR.
