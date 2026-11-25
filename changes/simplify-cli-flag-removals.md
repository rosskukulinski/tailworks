---
kind: breaking
upgrade: []
---
Five flags that did nothing useful are gone:

- `tw notify set --events`: it only ever accepted its own default,
  `approval_requested`. Drop the flag. The API's `events` field and the
  `events` line in `tw notify` are unchanged; the flag comes back when a
  second event exists.
- `tw types --remote` and `tw types --tenant`: `tw types` now declares only
  the secret names in `.dev.vars`, `.dev.vars.example` and the wrangler
  config's `secrets.required`, so its output is the same on every machine
  and `--check` is reliable in CI. The secrets comment in
  `worker-configuration.d.ts` no longer mentions `--remote`, so run
  `tw types` once if you commit that file and it declares secrets.
- `tw init --keep-tags`: it only ever kept the Durable Object class tags
  (the workload tag was always renamed after the app). Use
  `--object-tags [<Class>=]<tag>`, or edit `tailnet.objects` in
  tailworks.jsonc.
- `tw docs ls --limit` and `tw docs show --limit`: `--limit` is a
  `tw docs search` flag only.
- `tw-hostd -default-tier`: it was not an isolation floor (a deployment's
  or tenant's tier overrode it, and mgmt placement never saw it). It is
  hidden from `-help` for this release: `-default-tier process` is ignored
  with a warning, and `-default-tier microvm` stops tw-hostd at startup
  rather than quietly running unlabelled deployments in the process tier.

Operator action: if a host's service unit passes `-default-tier microvm`,
run `tw tenant set <tenant> --tier microvm` (or set `"tier": "microvm"` in
each app's tailworks.jsonc) and remove the flag before upgrading. If you
used `tw types --remote`, run `tw types` and add any missing secret names
to `.dev.vars.example` (placeholder values only) or `secrets.required`;
`tw secret ls --json` lists a workload's secrets in mgmt.
