Last update: 2026-11-16 PT. twd → tailworks rename (issue #86): scripted pass, hand fixes, regenerated outputs; test results below.

# Rename twd → tailworks (issue #86)

## What was done

- `scripts/rename-tailworks.sh` does the mechanical part: it `git mv`s every
  path containing `twd`, then rewrites contents in three passes (long-name
  forms → `tailworks`, prose → `tailworks`, everything else → `tw`), then
  fixes user-facing strings in code ("not supported on tw" → "… on
  tailworks"). Read the script, not the 1,000-file diff. The commit
  "scripted pass" is its unedited output; later commits are hand fixes.
- Name map as in the issue. Choices the issue left open:
  - Peer capabilities: `github.com/rosskukulinski/tailworks/cap/*` (no
    domain bought; `tailworks.dev` is someone else's).
  - `https://twd.dev/errors/<code>` doc URLs → `docs/errors.md#<code>` on
    GitHub; the OpenAPI contact URL → the repo.
  - The state file `~/.twd/twd.json` → `~/.tailworks/tw.json` (internal:
    short name). The per-app config is `tailworks.jsonc`.
  - System dirs take the long name like the install root: `/etc/tailworks`,
    `/usr/local/libexec/tailworks`, `/run/tailworks`, `/var/lib/tailworks-vm`,
    `share/tailworks`, `~/.config/tailworks`. Systemd units are short
    (`tw.service`, `tw-vmhelper.service`); the launchd label is
    `dev.tailworks.up`.
  - Not in the issue but renamed: `guest/tw-guestd`, `hostd/gen/js/tw-rt.js`,
    `schema/tailworks.schema.json`, the root Go package (`package tailworks`).
- Hand fixes: `install.sh` adds `bin/tailworks → tw` (and `install_test.sh`
  checks it); the supervisor's pid check accepts argv0 `tailworks`;
  tarball-name globs in `upgrade.go`, `hostd/upgrade.go`, `get.sh`,
  `release.sh`; README not-affiliated-with-Tailscale note; one line at the
  top of the frozen `STATUS.md`.
- Header stripping keeps the dash: `x-tw-` / `tw-` everywhere (netd,
  hostd node ingress, container intercept, platform.js). New test
  `netd/identity_headers_test.go`: `x-tw-caller`, `Tw-Actor`, … are
  stripped; `twilio-signature`, `x-twilio-signature`, `x-twitter-token` are
  not.
- Regenerated: `make api` (no diff), `make gen-js`, `make cli-docs`,
  `cmd/tw/testdata/agent-setup.golden`, `CHANGELOG.md` header.

## Bugs the rename caused, found by tests and fixed

- `req.cf?.twd` became `req.cf?.tailworks` (the install-dir rule matched
  `?.twd`): workerd refused every request. Fixed, and the script now skips `?`.
- `queuecfg`: `Binding[:5] == "__TW"` (was `"__TWD"`) never matched, so
  reserved `__TW*` binding names were accepted. Now `strings.HasPrefix`.
- `os.Getwd` → `os.Getw`: the script now leaves it.
- RT-06's homoglyph replaced the `d` of `tag:twd`; it now replaces the `w`.

## Left as history (not renamed)

`changes/*.md` and `changes/released/`, `status/*.md` (other branches),
`STATUS.md`, the release bodies in `CHANGELOG.md` and
`internal/catalog/releases/*.json`, and the applied migration
`mgmt/migrations/202611161200_*.sql` (comments only). Plus `os.Getwd` and
base64 integrity hashes in the lock files.

## Tests

Run on macOS arm64, branch up to date with origin/master (b2bfcc8):

- `make lint test`: pass (final run). An earlier run failed once in
  `hostd` (`TestWorkersKVMove`: a data race on `mgmtapi.Client.HostID`;
  `TestQueues/FencedHostConsumesNothing`: a 30 s delivery timeout). Both
  passed on rerun under `-race`; the race is in code the rename only touched
  for import paths and a header name, so it looks pre-existing.
- `make templates-typecheck test-sdk e2e-wrangler e2e-placement e2e-locks
  e2e-queues e2e-observability test-pg install-test`: all pass.
- `npm pack --dry-run`: `@tailworks/tailnet`, `@tailworks/node`,
  `@tailworks/mcp`, `create-tailworks` (all 0.2.0).
- Not run (need a real tailnet, credentials, KVM or a release build):
  `e2e-real`, `e2e-public`, `e2e-vm`, `e2e-vz`, `e2e-headscale`, and the
  fresh-install-from-tarball check. `e2e-upgrade` is N/A: there is no
  earlier tailworks release to upgrade from.
- Leftovers: `git grep -I -i twd` finds 1966 occurrences in 90 files, all
  intended (history files listed above, `os.Getwd`, lock-file hashes, this
  file and the rename script).

## Not done here (needs the owner)

- Rename the GitHub repo to `rosskukulinski/tailworks` just before merging.
- Create the `@tailworks` npm org, check `tailworks`/`create-tailworks` are
  still free, and update the `npm-publish.yml` token.
- Tear down existing installs (issue #86 "Teardown"), then a fresh install
  from a release tarball on macOS and Linux with a real deploy.
- The pending `changes/*.md` fragments from other branches still say `twd`;
  they'll appear in the first tailworks release notes unless edited.
