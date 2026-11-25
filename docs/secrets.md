# Worker secrets

`wrangler.jsonc` `vars` are plaintext and live in git. For API keys and
other credentials, tailworks has **secrets**, which work like Cloudflare Worker
secrets:

- You set a secret with `tw secret put <NAME>`. The value is read from
  stdin, never from the command line.
- Your code reads it as `env.NAME`, a string, in the Worker and in every
  Durable Object class. For `"runtime": "node"` it is
  `process.env.NAME`.
- A secret overrides a var of the same name.

The design and the threat model are in
[ADR-034](design/adr/034-worker-secrets.md).

```sh
cd my-agent                         # the app: the workload is its name, as for tw deploy
tw secret put LLM_API_KEY          # hidden prompt on a terminal
printf %s "$KEY" | tw secret put LLM_API_KEY   # or from a pipe (one trailing newline is dropped)
tw secret bulk .dev.vars           # several at once, from a dotenv or JSON file
tw secret ls                       # names, last update and who set them; never values
tw secret rm LLM_API_KEY
tw deploy                          # secrets take effect on the next deploy
```

```ts
export default {
  async fetch(req: Request, env: { LLM_API_KEY: string }) {
    const r = await fetch("https://api.example.com/v1/chat", {
      headers: { authorization: `Bearer ${env.LLM_API_KEY}` },
    });
    // …
  },
};
```

## Commands

| Command | What it does |
|---|---|
| `tw secret put <NAME>` | Creates or replaces a secret. It reads the value from stdin: a hidden prompt on a terminal, or everything piped in, less one trailing newline. A value given on the command line is refused and never echoed. |
| `tw secret bulk [<file>]` | Creates or replaces several secrets, like `wrangler secret bulk`. The file is dotenv (`NAME=value` lines, as in `.dev.vars`) or a JSON object of strings (`{"NAME": "value"}`); a first character of `{` means JSON. Without a file (or with `-`) it reads piped stdin. See [below](#tw-secret-bulk). |
| `tw secret ls` | Lists the secret names with their last update time and actor. No command or API ever returns a value. |
| `tw secret rm <NAME>` | Deletes a secret. |

All four take these flags:

- `--workload <w>`: defaults to the app in the current directory, named the
  way `tw deploy` names it.
- `--tenant <t>`: defaults to the `tw up` tenant.
- `--json`

The commands work on the host and from a `tw link`ed laptop: both go to
mgmt's `/v1` API, and over the tailnet they need the admin capability
(`github.com/rosskukulinski/tailworks/cap/admin`), like every other write.

The workload doesn't have to be deployed yet, so you can put its secrets
before the first deploy. With `"replicas": N`, every replica gets the
workload's secrets. Each replica is released them under its own lease.

**Names** are JS identifiers that are also environment variable names
(`[A-Za-z_][A-Za-z0-9_]*`, up to 128 characters). Names starting with
`TW_` or `__TW` are reserved. For `"runtime": "node"`, these are also
refused because tailworks sets them: `PATH`, `HOME`, `TMPDIR`, `NODE_ENV`,
`PORT`, `HOST` and the proxy variables. A secret can't have the name of a
Durable Object binding.

**Limits** are Cloudflare's: 128 secrets per workload and 5 KiB per value
(UTF-8 text).

### tw secret bulk

```sh
tw secret bulk .dev.vars                 # dotenv: NAME=value lines
tw secret bulk secrets.json              # JSON: {"NAME": "value", …}
op inject -i secrets.env.tpl | tw secret bulk   # piped stdin (no file, or -)
```

- **Values never go on the command line.** An argument containing `=` is
  refused (and not echoed), and so is a terminal on stdin with no file:
  give a file, or pipe the secrets in.
- **Everything is checked before anything is set:** the syntax, every
  name, each value's size, a name given twice, and the workload's total
  after the run (at most 128). A refusal names the line and the secret
  name, never a value, and nothing is set.
- The secrets are then set one at a time, in the input's order, each
  through the same API call as `tw secret put` (audited as `secret_put`
  with the name and the actor). If one fails, the run stops and the error
  lists the names **set**, the one that **failed**, and those **not
  attempted**, and `tw` exits non-zero. Run it again after fixing the
  cause: setting a secret to the same value is harmless.
- **It only adds and replaces.** A secret that isn't in the input is kept;
  remove it with `tw secret rm <NAME>`. (`tw dev` is different: it makes
  the dev workload's secrets exactly `.dev.vars`.)
- `--json` prints `{"workload", "tenantID", "set", "failed",
  "notAttempted"}`, names only.
- Dotenv syntax is the one `tw dev` reads: `#` comments, an optional
  `export `, `"double quoted"` values with `\n` escapes that may span
  lines, `'single quoted'` literal values.

## When changes take effect

A running workload keeps the secrets it started with. A new or changed
value reaches it on its **next deploy**: run `tw deploy` again, the same
as for a changed var. A restart of tw-hostd also refetches secrets, since
it redeploys from its assignments. A crash restart of the same instance
(workerd or node exiting) reuses the same values.

Secrets are not versioned with deployments. `tw rollback` redeploys an
older bundle with the secrets the workload has **now**, so a secret you
rotated stays rotated and a secret you deleted stays deleted.

A preview (`tw deploy --preview`) runs with the workload's secrets, as a
replica does: they are released to the host holding the preview's own
lease, and the release is audited with `previewOf`. Set them on the
workload, not on `<workload>--preview-<alias>`. A preview runs code that
hasn't been promoted yet, with the same secrets as the workload; keep
credentials that must not reach unreviewed code out of the workload's
secrets.

## Local development: `.dev.vars`

`tw dev` reads `.dev.vars` from the app directory, if there is one, the
same way `wrangler dev` does. It's a dotenv file:

```sh
# .dev.vars — never commit this
LLM_API_KEY=sk-local-…
GREETING="multi\nline"
```

Every entry becomes a secret of the dev workload, so it overrides a var of
the same name. It reaches your code through the same path as
`tw secret put` in production. If you edit the file, the app reloads.

The format:

- Blank lines and `#` comments are skipped.
- `export ` is allowed before a name.
- `"double quotes"` allow `\n` escapes and may span lines.
- `'single quotes'` are literal.
- Unquoted values end at ` #`.

The templates' `.gitignore` lists `.dev.vars`.

To have [`tw types`](wrangler.md#types-tw-types) declare a secret's name
on every machine, commit a `.dev.vars.example` with the same names and
placeholder values only, never real secrets, or list the names under the
wrangler config's `secrets.required`. `tw types` reads names only from
these local files, not from mgmt; `tw secret ls --json` lists the secrets
a deployed workload has.

## Where values live

- **At rest:** mgmt's custody, one sealed entry per workload
  (`tw/<tenant>/workloads/<workload>/secrets`). The homelab file custody
  is AES-256-GCM with the entry name bound as additional data; setec
  custody keeps its own encryption. mgmt's database never holds a value.
- **In flight:** a value leaves mgmt only in a secret grant
  (`POST /v1/tenants/{id}/workloads/{w}/secret-grants?epoch=N`). mgmt
  answers it only to the host that holds the workload's lease at that
  epoch, the same fence that protects microVM volume keys. The answer
  carries `Cache-Control: no-store`.
- **On the host:** the values go into the instance directory only, which
  is `0700` and is deleted when the instance stops:
  - workerd: text bindings in `config.capnp`, plus `tw-config.json`,
    where facet Durable Objects get their `env`;
  - runtime `node`: a `0600` file there, which a `--require` preload
    copies into `process.env` at start-up and then deletes (every start
    writes it again). The values are deliberately **not** in the node
    process's exec-time environment: on macOS any process of the same user
    can read another process's environment (`ps -E`, `KERN_PROCARGS2`),
    and Seatbelt can't refuse that read.

  The process sandbox keeps every other tenant's workloads out of that
  directory and, on Linux, out of other processes' `/proc` entries.
- **Never logged or audited.** The audit records `secret_put`,
  `secret_delete` and `secret_release` carry names only.

## Tests

The guarantees above are checked under faults by the data-plane chaos
loop's deploys scenarios (`make e2e-dataplane-deploys`, nightly;
`e2e/dataplane/LOOP.md` "Deploys"): concurrent deploys, rollbacks, previews
and `tw secret put`/`rm` through three mgmt replicas on Postgres, while
hosts, replicas and their database links fail. Its invariant V4 checks
that:

- a deployment runs with the secrets the workload had when it was
  deployed, or newer, never a value overwritten or deleted before that;
- a rollback gets the workload's current secrets, not the old bundle's;
- a preview gets the workload's secrets, at its own lease, and never one
  set on its own name;
- values are released only to the host holding that epoch's lease;
- no value appears in any process log, audit record, or mgmt's database.

Concurrent writes of one workload's secrets through several mgmt replicas
are serialised (#578): `TestMM_H24_SecretWritesAcrossReplicas`.

## Not supported yet

- **The microVM tier.** A `"tier": "microvm"` deployment of a workload that
  has secrets fails with `secrets_unsupported_tier`. On a cold boot, the
  microVM's config drive (a tar of the instance directory) is written to
  the VM's run directory, outside the instance directory. Deploy such
  workloads with `"tier": "process"` for now, or keep the secret out of
  the workload.
- Secret versions and gradual deployments, and the
  Secrets Store binding (`secrets_store_secrets`).
- Restarting a running workload when a secret changes. Redeploy instead.
