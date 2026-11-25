Last update: 2026-11-24 PT. One install-channel classifier (#498) and tw upgrade refreshing agent skills (#490).

# simplify-install-channel

Issues #498 and #490 from the interface-simplification audit (#434).

## What's built

- **#498, one `installChannel()`** (`cmd/tw/install_channel.go`): install.sh
  prefix | npm global | npx cache | source checkout | other. Each has one
  upgrade command: `tw upgrade`; `tw backup && tw down && npm install -g
  tailworks@<v> && tw up`; `npx tailworks@latest …`; `cd <root> && git pull
  && make build`; the get.sh line. `tw version --check` (and the daily notice),
  `tw doctor`'s install row (`; upgrade: <cmd>`) and `tw upgrade --plan` all
  use it. `tw upgrade --plan` on an npm install prints the npm command as the
  plan (same `localResult` JSON shape) instead of failing. The run still
  refuses with `npm_install`, and so does `tw-hostd -self-upgrade` (not
  touched). The plan on a source checkout or a copied binary carries a
  warning with the right command. The `bad_request` fix uses it too.
  `scripts/ci-npm-cli-check.sh` step 5 now checks both: the plan prints the
  npm command, and the run refuses.
- **#490, refresh the skills** (`cmd/tw/agent_refresh.go`, `agent/refresh.go`):
  `tw agent setup --refresh` is a hidden flag (stripped before flag parsing,
  so it's not in help or docs/cli.md). It covers every agent and only
  updates or removes tw-marked files and blocks. It never creates anything
  and never reports Skip. After a healthy upgrade, `tw upgrade` execs
  `<prefix>/bin/tw agent setup --scope user --yes --refresh --json`, so the
  new binary renders its own skills and version. The plan lists the step
  only when a tw-marked user-scope skill exists. The result reports it as
  `agentSkills` (schema/cli.schema.json). A refresh failure prints a hint
  and doesn't fail the upgrade. The project-scope hint stays.

## Decisions

- Refresh never creates (as the issue says). So a skill that a new release
  adds isn't installed by the refresh: `tw agent setup` does that.
- Source checkout command is `cd <root> && git pull && make build` (the issue
  said `make build`; without the pull it doesn't upgrade anything).
- npx keeps `npx tailworks@latest …` (npx always runs what you name).
- e2e/twupgrade now runs with its own HOME, seeded with an old tw skill. It
  checks the plan's 6th step and that the good upgrade refreshed the skill,
  and it no longer touches the developer's real home.

## Tests

- `go test ./agent ./cmd/tw` (targeted, incl. new `TestRefreshOnlyUpdatesStamped`,
  `TestAgentSetupRefresh`, `TestInstallChannel*`): pass.
- `make lint`: pass. `make changes-check`: ok (fragment `changes/simplify-install-channel.md`, `cli-json`).
- `make test`: cmd/tw and mgmt failed with "no space left on device" from
  `go build` (the disk was briefly full while other agents built). Rerun on
  their own, `go test -race -count=1 ./cmd/tw/ ./mgmt/`: both pass. Every
  other package passed.
- `make e2e-tw-upgrade`: pass. The plan has the refresh step, and the good
  N+1 upgrade refreshed the seeded old skill with the new binary.
- `make npm-cli-test`: pass.
- `make cli-docs`: no change (the flag is hidden).
- `scripts/ci-npm-cli-check.sh` (step 5 changed) runs only in release.yml on
  real tarballs, so it wasn't run locally. The npm `--plan`/refusal is
  covered by `TestInstallChannelAgrees`.
- `make e2e-upgrade`: not needed (changes-check: not upgrade-relevant).
  `make install-test`: install.sh/get.sh weren't touched.
