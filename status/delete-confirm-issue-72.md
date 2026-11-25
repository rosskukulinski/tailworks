Last update: 2026-11-16 PT. One confirmation rule for every command that deletes data (#72).

# Delete confirmations (#72)

## What's built

- `cmd/twd/confirm.go` holds the rule:
  - `yesFlag` registers `--yes`.
  - `confirmDestroy` decides: `--yes` goes ahead; a terminal without
    `--json` asks for the name to be typed back, and any other answer gives
    `cancelled`; anything else refuses with `confirmation_required`. The
    refusal's fix lines are any backup step, then the same command with
    `--yes`.
  - `destructiveCommands` lists the commands it guards.
- The refusal comes before the command opens the state or contacts mgmt.
- The commands it guards:
  - `twd d1 delete`: moved onto the shared rule. It was `--yes`-only with
    `bad_request`; it now prompts on a terminal.
  - `twd kv namespace delete`.
  - `twd queues rm`.
  - `twd queues purge`: its fix also suggests `queues messages` first.
  - `twd r2 bucket rm`.
  - `twd tenant rm`: the `tenant_has_live_objects` fix now includes
    `--yes`.
  - `twd host rm`: the `host_holds_leases` fix now includes `--yes`.
- Left unprompted, as before: `twd kill`, `undeploy`, `down`, `secret rm`
  and `kv key delete` (they stop things, or remove one key or secret). The
  issue's open question about `kill --cascade` is also left as is.
- Docs and skills updated: `docs/errors.md` (`confirmation_required`,
  `cancelled`), the `twd` and `twd-teardown` skills, `docs/agents.md`,
  `docs/d1.md`, and `docs/cli.md` (regenerated with `make cli-docs`).
- Changes fragment: kind `breaking`, because scripts need `--yes`.

## Tests

- `TestDestructiveCommandsConfirm` runs every entry in `destructiveCommands`
  and checks:
  - no terminal: `confirmation_required`, and the fix names `--yes`;
  - `--json` on a terminal: `confirmation_required`;
  - a terminal with a wrong answer: `cancelled`;
  - the name typed back, or `--yes`: the command goes on.
- Existing tests now pass `--yes`: d1, kv, r2, queues, cli_json, agent_eval.
- For the commands run: see the PR.
