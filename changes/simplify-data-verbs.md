---
kind: feature
upgrade: [protocol, config-default]
---
The data commands (`tw d1`, `tw kv`, `tw queues`, `tw r2`, `tw secret`) are
more consistent:

- `ls` and `rm` are the documented verbs everywhere, including `tw d1 ls`,
  `tw d1 rm`, `tw d1 migrations ls`, `tw kv namespace ls|rm` and
  `tw kv key ls|rm`. wrangler's `list` and `delete` (and `remove`, `del`)
  still work, for these and for `tw tenant`, `tw host`, `tw release`,
  `tw telemetry`, `tw notify`, `tw image public`, `tw docs` and `tw agent`
  (so `tw docs list` and `tw agent rm` work now).
- `tw kv key …` finds the namespace by `--namespace` given its name or its
  binding in the app's `wrangler.jsonc`. In an app with one
  `kv_namespaces` entry, it needs no flag at all. `--binding` still works,
  as the same flag. With `--workload <other>`, `--namespace` is still
  required. `tw r2 <bucket>` and `tw d1 <db>` take a binding too; a store
  the app declares by that name wins over a binding of the same name.
- Each verb takes only its own flags: `tw kv key get --help` no longer
  lists `--ttl` or `--prefix`, and a flag of another verb
  (`tw kv key get k --ttl 60`) is now refused with `bad_request` instead of
  ignored, as wrangler does. `--yes` is only on the commands that ask
  first.
- Flags may come before the verb in all five commands:
  `tw secret --tenant x ls` and `tw queues --limit 5 messages q` used to
  fail.

Completion offers only the documented spelling, so `tw d1 li<TAB>` no
longer completes (`tw d1 list` still runs).

Operator action: none. The `api/openapi.yaml` change is description text
only (the commands it names).
