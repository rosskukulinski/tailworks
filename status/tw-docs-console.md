Last update: 2026-11-24 PT. #478 (console URL in tw link / tw whoami) and #479 (tw docs embeds docs/*.md only).

## #479: tw docs embeds the guides only

- `docs/docs.go`: `//go:embed *.md` (was `*.md contracts/*.md design/RFC.md
  design/adr/*.md`). `docWeight` and its call are gone; the carve-outs in
  `docs/docs_test.go` (`TestLLMsTxt`'s ADR skip, `TestSearchRanksGuides`'s
  "not contracts/ or design/ first") are gone with it.
- `tw docs show design/…|contracts/…` answers `not_found` with the file's
  GitHub URL as the fix (`docs/github.go: OnGitHubOnly`, `GitHubBlob`).
- `docs/README.md` and `llms.txt` say the API contract and the design
  records aren't in the CLI.
- No skill, error hint, test or doc runs `tw docs show` on a design/ or
  contracts/ path (grepped every `tw docs show <ref>`; `TestSkills` checks
  the skills' refs against the embedded docs).
- `TestSearchCLIOnce` (`tw docs search delay`: at most one cli.md hit in the
  top 10) still passes without docWeight.
- `tw docs ls`: 101 docs → 44.
- Binary size (`go build -trimpath -ldflags='-s -w' ./cmd/tw`, darwin/arm64):
  53,975,554 → 53,216,002 bytes (−759,552, about −0.72 MiB, −1.4%).

## #478: console URL in tw link / tw whoami

Verified before implementing (the issue's comment records all three
verifiers accepting the gated form; re-checked here against the code):
mgmt's `mountUI` (mgmt/v1.go) admits an admin, a tenant admin, and an
approver (the approvals page only), never a host; GET / from a browser
redirects to /ui/. `tw link` already refuses anyone who is neither admin nor
tenant admin, so its console line is always openable. Under `--tsnet` the
CLI's own node is the only tailnet path, so this machine's browser can't
reach mgmt: the line is omitted.

- `cmd/tw/console.go: consoleURL`: `<linked endpoint>/ui/` for admin or
  tenant admin, `<endpoint>/ui/approvals` for an approver who is neither,
  "" otherwise, under `--tsnet`, or when not linked. Built from the linked
  endpoint, not mgmt's DNS name.
- `tw link`: `✓ console   <url>` line, `console` (omitempty) in `--json`.
- `tw whoami`: `console   <url>` line (approver-only: noted as the
  approvals page), `console` (omitempty) in `--json`; only through
  `tw link` (`tw up`/`tw status` already print it on the host).
- `schema/cli.schema.json`: `console` on `$defs/whoami` and `$defs/link`.
- README: the bare mgmt URL redirects a browser to /ui/; link and whoami
  print it.
- Tests: `TestConsoleURL` (unit); `TestWhoami` checks the console URL for
  the admin and the approver and GETs it through mgmt's real handler (200
  for both).

## Commands run

- `go test ./docs`: ok.
- `go test ./cmd/tw -run 'TestWhoami|TestConsoleURL|TestSkills|Schema|JSON'`: ok.
- `make cli-docs`: no change to docs/cli.md (no flag or help change).
- `make changes-check`: ok (needs cli-json; `changes/tw-docs-console.md`).
- Merged origin/master (b8501254), then `make cli-docs` (no change) and
  `make lint`: ok.
- `make test`: test-go failed in cmd/tw, mgmt, hostd and internal/mgmtd,
  every failure `no space left on device` / `database or disk is full`
  (the shared machine's disk filled during the run). Reruns after space
  freed up: `go test ./cmd/tw ./docs ./schema`: docs and schema ok, cmd/tw
  ok except TestDevPython (workerd not healthy in time) and TestCLIOAuth
  (slow kill), both of which pass alone (`go test -run
  '^(TestDevPython|TestCLIOAuth)$' ./cmd/tw`: ok); `go test ./internal/mgmtd`:
  ok; `make test-docs`: ok. mgmt and hostd (untouched) not rerun; CI is the
  authority. test-js not reached (no JS changed).
