# Release-note fragments

Release notes are assembled from one file per branch, not from a shared
CHANGELOG. That way parallel branches never edit the same lines, which is
the same reason `status/` is one file per branch (see AGENTS.md).

## Writing one

Add `changes/<your-branch>.md`, and don't touch any other fragment.

```markdown
---
kind: feature
upgrade: [schema-expand, protocol]
---
What changed, written for the people who run or use tw.

Operator action: what an operator must do when upgrading (omit if nothing).
```

- `kind` is one of `feature`, `fix`, `breaking` or `security`.
- `upgrade` lists what the change does to an upgrade. It may be empty
  (`[]`). The kinds:

| Kind | Use it when the branch… |
|---|---|
| `schema-expand` | adds a mgmt store migration that only adds things (`mgmt/migrations/`) |
| `schema-contract` | adds a migration marked `-- tw:contract` (drops, renames, tightened constraints) |
| `format` | adds or changes a persisted format version (`testdata/formats/`) |
| `feature-string` | adds a host feature string that placement gates on |
| `protocol` | changes a wire contract: `api/openapi.yaml`, or the push, guest or helper protocol |
| `workerd` | bumps the workerd pin, or changes the guest inputs (`guest/inputs.sh`) |
| `compat-date` | moves a platform-owned `compatibility_date` or an auto-added flag |
| `config-default` | changes a default: a flag, a port, CLI output |
| `cli-json` | changes `schema/cli.schema.json` |

`make lint` runs `scripts/check-changes.sh`. If a branch touches one of the
paths above, it needs a fragment that names the matching kind, or the check
fails.

## At release time

`scripts/release-notes.sh <prev-tag> <tag>` prints the assembled notes.
With `--finalize` it also moves the fragments to `changes/released/<tag>/`
and writes the release catalog entry `internal/catalog/releases/<tag>.json`,
which `tw-mgmt` embeds. See `docs/RELEASING.md`.
