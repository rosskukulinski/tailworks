Last update: 2026-11-24 08:20 PT. `scripts/demo.sh` (issue #323, the scriptable part), a throwaway Headscale tailnet to run it on, and a recorded GIF (not in the README yet: it is from the Headscale run).

# demo-323-script: the reproducible launch demo

Part of #323. The screenshots, the MP4 and the second ("Claude Code builds
an MCP server") recording are left for the owner; see "Not done".

## What's built

- `scripts/demo.sh`: ten steps, the same every run. `tw status` (or `tw up`
  with `--up`), `tw init` of the `mcp` and `agent` templates, `tw policy
  --apply`, two grants, `tw deploy` of both, a tool call from a tailnet
  device through the agent (allowed), `tw ls` (the agent object's own
  node), the grant narrowed (the call answers `cap_missing`), the grant
  restored, `tw kill` (the call answers `killed`), `tw audit <object>`.
  `--tailnet` is required and checked against `tw status`; it prints what
  it will change and asks, unless `--yes`; an EXIT/INT/TERM trap restores
  the grant; `--cleanup` undeploys and removes the two grants.
  `DEMO_PAUSE` / Enter between steps, `DEMO_CLEAR=1` for recording.
- `internal/demogrant` (Go, `go test ./internal/demogrant`): the policy writer the
  demo needs, because `tw` only ever adds to a policy. It touches two
  grants, recognised by src and dst, with read (ETag) → edit (JSON Patch on
  the HuJSON, comments kept) → validate → `If-Match` write. Tailscale
  (OAuth client or API key file) and Headscale.
- `scripts/demo-headscale.sh up|down`: Headscale v0.29.7 in Docker, a
  Tailscale client container (`laptop`) as the calling device, and `tw up`
  with its own state directory. Nothing in `~/.tailworks`,
  `~/.config/tailworks` or a real tailnet is touched.
- `scripts/demo_test.sh` / `make demo-test`: the demo for real on that
  tailnet. Refusals, first run, rerun, interrupted run, `--cleanup`.
- `docs/demo.md` (linked from `docs/README.md` and `llms.txt`): how to run
  it, what it changes in the policy, how to record (asciinema → agg GIF →
  ffmpeg MP4), the scrub check, and the asset paths.
- `docs/assets/demo/demo.cast` and `demo.gif` (56.7 s, 811 KB, 1028×804),
  and the GIF under the README's first paragraph.

## Tests run (this machine: macOS arm64, Docker Desktop)

All real runs were against the throwaway Headscale tailnet
(`demo.tw.test`), with `TW_STATE_DIR=/tmp/tw-demo-hs/state`.

| Command | Result |
| --- | --- |
| `make demo-test` (after merging origin/master at v0.5.0) | PASS: refusals, first run, rerun, interrupt restores the grant, cleanup |
| `scripts/demo.sh --tailnet demo.tw.test` through a pty, answering `n` | exit 1, "nothing was changed", policy has no demo entries |
| the same, answering `y` and Enter at each of the 9 pauses | exit 0, all ten steps, on a fresh tenant |
| `DEMO_NAME=demo2 scripts/demo.sh … --yes --no-pause`, then `--cleanup` | exit 0 both |
| `go test -count=1 ./internal/demogrant` | ok |
| `go test -count=1 ./docs/...` | ok |
| `go test -count=1 ./cmd/tw -run 'Doc\|CLIReference\|Design\|Help\|Policy'` | ok |
| `make lint`, `make changes-check`, `make test` (after the merge) | all exit 0; 57 packages ok |

## Not tested

- **Nothing ran against a Tailscale tailnet.** The Tailscale code path of
  `internal/demogrant` (OAuth token, `tsapi` policy read/validate/write) is
  the same client `tw policy --apply` uses, but this branch never called
  it. `--up` (the script running `tw up` itself) was not run either. The
  first run on a Tailscale tailnet should be on the test tailnet, with
  `--dry-run` of the grant editor first (the script prints that plan
  before it asks).
- `DEMO_CALL` with its default (`curl` on this machine) and over `ssh`.

## Findings: rough edges the demo ran into

1. **No `tw` command can narrow or remove a grant**, or add the grant that
   lets someone call an app. `tw policy --apply` adds tagOwners only. The
   demo's central step needed its own policy writer
   (`internal/demogrant`), and that edit is not in `tw audit`.
2. **`tw undeploy X` then `tw deploy X` fails for good** on the same host:
   `bootstrap_failed: the workload node for X is killed`, until `tw down
   --purge`, which kills every app's objects. `--cleanup` therefore uses up
   the names `demo` and `demo-agent`; the script detects this before it
   writes anything and points at `DEMO_NAME`.
3. **A killed agent name stays killed**, so each run needs a new instance
   name (`demo-<HHMMSS>`). Expected by design, but a rerun of a hand-typed
   demo would trip on it.
4. `tw ls` can list a new object as `assigned`, with no node name, for a
   moment after it has already answered a request (it took a pool node).
   The script waits for the node before it shows `tw ls`.
5. The `killed` answer's `doc_url` points at `blob/main/docs/errors.md`;
   the default branch is `master`, as every other `doc_url` says.
6. `tw audit` has no way to show only the last N records. The demo shows
   `tw audit <object>` instead of a tail of `--all`.
7. `tw audit` names the operator as `$USER@<hostname>`: a recording shows
   the login and machine name unless `USER` is set and the cast edited.
8. Docker Desktop on macOS: a Headscale container started right after its
   bind-mounted directory was deleted and recreated exits at once.
   `demo-headscale.sh` uses a new directory per `up`.

## Not done (for the owner)

- The two admin-console screenshots (paths in `docs/demo.md`).
- The 90 s MP4 for the blog and social (commands in `docs/demo.md`; the
  ffmpeg line was run once on the committed GIF and works).
- The second recording, "Claude Code builds an MCP server" (issue item 4).
- The committed GIF is from the Headscale tailnet: it says `mint mode
  headscale` and calls from `docker exec laptop curl`. So the README
  line was taken out before merge; re-record on a Tailscale tailnet as
  `docs/demo.md` says, replace the two files and add the line.
