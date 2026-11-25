---
name: tw-secrets
description: Use when a tailworks app needs an API key, token or other credential (env.NAME, like `wrangler secret`) - setting, listing or removing secrets with `tw secret`, and local secrets for `tw dev` in .dev.vars.
---

# Secrets for a tailworks app

`wrangler.jsonc` `vars` are plaintext and committed to git. Credentials go
in **secrets** instead. The app reads a secret as `env.NAME` (a string)
in the Worker and in every Durable Object class, or as
`process.env.NAME` with `"runtime": "node"`. A secret overrides a var of
the same name.

## Never handle the value yourself

- Don't put a secret's value in a command line, a file, `wrangler.jsonc`,
  `tailworks.jsonc`, a commit or your output.
- `tw secret put` reads the value from stdin. Ask the user to run it
  themselves, so they type the value at the hidden prompt:

```sh
tw secret put LLM_API_KEY                        # the user runs this and types the value
```

If the value is already in the user's environment, they can pipe it
(one trailing newline is dropped):

```sh
printf %s "$LLM_API_KEY" | tw secret put LLM_API_KEY
```

If the user already has the values in a file (`.dev.vars`, or a JSON
object of strings), ask them to set them all at once. It only adds and
replaces; it never removes a secret that isn't in the file:

```sh
tw secret bulk .dev.vars                         # the user runs this
```

Don't create or edit that file with real values yourself.

## Commands

Run them from the app directory, or pass `--workload <workload>`:

```sh
tw secret ls --json              # names, last update and who set them; never values
tw secret rm LLM_API_KEY         # destructive: ask the user first
```

- **Secrets take effect on the next `tw deploy`.** After `put` or `rm`,
  redeploy (`tw-deploy`).
- The workload doesn't need to be deployed yet: put its secrets before the
  first deploy.
- Names are JS identifiers that are also environment variable names, e.g.
  `LLM_API_KEY`.

## Local development

`tw dev` reads secrets from `.dev.vars` in the app directory (dotenv
syntax, `NAME=value`). The templates gitignore it. Create the file with
placeholder values if needed and ask the user to fill in the real ones.

More: `tw docs show secrets.md`.
