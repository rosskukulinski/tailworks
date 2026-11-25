# adr-index-drafts

`TestADRIndex` no longer fails CI on an un-numbered ADR draft
(`docs/design/adr/<slug>.md`). A draft needs no index row, but its first line
must start `# ADR-<slug>`. Design PRs (#226, #396) were red only because of
the old CI-only check. AGENTS.md rule 5 says so.

Tests: `go test ./docs/` and `CI=true go test ./docs/ -run TestADRIndex`
pass; checked by hand that a draft with a correct header passes and one with
a wrong header fails. `make lint` run before pushing.
