---
kind: breaking
upgrade: [config-default]
---
Every command that deletes data or a tenant's resources now asks first:
`tw d1 delete`, `tw kv namespace delete`, `tw queues rm`,
`tw queues purge`, `tw r2 bucket rm`, `tw tenant rm` and `tw host rm`.
On a terminal it says what will be lost and asks you to type the name.
Without a terminal (a script, a coding agent, `--json`) it refuses with
`confirmation_required` and does nothing; add `--yes` to go ahead.
`tw d1 delete` without `--yes` now prompts on a terminal, and its refusal
code is `confirmation_required` (it was `bad_request`).

Operator action: scripts that run these commands need `--yes`.
