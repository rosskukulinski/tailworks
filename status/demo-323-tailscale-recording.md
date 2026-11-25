Last update: 2026-11-24 PT. The launch demo re-recorded on a Tailscale tailnet; the GIF is at the top of the README (#323).

# Launch demo: the Tailscale recording

Continues [demo-323-script](demo-323-script.md), whose recording was from
the throwaway Headscale tailnet and so was kept out of the README.

## What was done

- `scripts/demo.sh --tailnet <test tailnet 1> --yes` ran all ten steps on a
  Tailscale tailnet, first try, with `DEMO_PAUSE=4 DEMO_CLEAR=1` under
  `asciinema rec`. This is the first run of `internal/demogrant`'s Tailscale
  path (OAuth token, policy read, validate, write with `If-Match`): ensure,
  narrow, restore and remove all worked.
- tailworks was `bin/tw` from master with its own state directory
  (`TW_STATE_DIR` under `/tmp`), `tw up --service=false --ephemeral
  --mgmt-hostname tw-demo-mgmt`, and a new OAuth client (auth_keys,
  devices:core, policy_file; `tag:tw`). The caller was a
  `tailscale/tailscale` container named `laptop`, joined with an ephemeral,
  untagged auth key. The machine's own tailworks install and its tailnet
  were not used.
- `docs/assets/demo/demo.cast` and `demo.gif` replaced (57.1 s, 885 KB).
  Replaced in the cast before rendering: the tailnet name, the caller's
  login, the host name in the audit line, a user ID and a key ID. The scrub
  grep in `docs/demo.md` now finds only `example.ts.net`, `you@example.com`
  and `demo@…` names.
- README: the GIF line, directly under the description. `docs/demo.md`
  says how this recording was made.
- `scripts/demo-headscale.sh` still passed `--policy-apply` to `tw up`,
  which #357 removed, so `make demo-test` was broken on master (it is not
  in CI). It now passes `--apply-policy --yes`.

## Test tailnet 1: what changed (install-testing.md safety rules)

- Policy backup before the first write:
  `tailnet1_acl.pre-323-demo-20261124-093320.hujson` (and `.etag`) next to
  the credentials.
- Added and kept: `tagOwners` for `tag:tw-wl-demo`, `tag:tw-wl-demo-agent`,
  `tag:tw-demo-agent-support-agent`, `tag:tw-demo-agent-lookup-agent`.
- Added, then removed by `scripts/demo.sh --cleanup`: the demo's caller
  grant and tool grant. `tw policy --apply` wrote no caller grant of its
  own (that policy already lets admins reach every node).
- Created and deleted by ID: the OAuth client, the caller's auth key, the
  `laptop` and `tw-demo-mgmt` devices. `tw down --purge` deleted the three
  ephemeral workload and pool devices.

## Tests

- The recorded run itself: all ten steps, then `--cleanup`.
- `make demo-test` (Headscale), `make lint`, `go test -count=1 ./docs/...`,
  `make changes-check`: see the PR.

## Still open on #323

- The two admin-console screenshots (they need a browser signed in to the
  tailnet).
- The 90 s MP4, and the second recording (Claude Code builds an MCP server).

## Seen in the recording

- `tw policy <app> --apply` prints "? callers not checked: tailworks
  doesn't know who you want to allow" and "tailworks didn't write it" on a
  policy where a rule from people already reaches every node. That is the
  quiet case of #357's caller-grant rule; the wording predates it.
- The `killed` response's `doc_url` still says `blob/main` (#365).
