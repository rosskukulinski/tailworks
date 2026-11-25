---
name: tw-deploy
description: Use when deploying a tailworks app to the tailnet with `tw deploy`, redeploying after a change, or checking what is deployed and where with `tw ls` and `tw status`.
---

# Deploy a tailworks app

`tw deploy [dir]` builds the app, checks the tailnet policy it needs
(preflight), uploads it, places it on a host and waits until its node is
ready. Run it from the app directory (or pass the directory).

Check tailworks is up first: `tw status --json` must say `"healthy": true`. If
it fails with `not_up`, ask the user to start or link tailworks (see the `tw`
skill).

Before an app's first deploy, run `tw policy --json`. It checks the
tailnet policy for everything the app needs and deploys nothing.
- On success, `callerGrant` is the grant that lets people call the app.
  tailworks can't check it, so ask the user whether they have one like it.
- If something is missing, it fails with `acl_denied`, and the error's
  `hint` is one snippet with every line to add. Handle it as below.

<!-- tw-eval: deploy -->
```sh
tw deploy --json
```

On success, stdout is one JSON document:

- `deploymentID`, `host`, `status` (`active`);
- `endpoint.magicDNS`: the workload's address on the tailnet; it answers at
  `http://<endpoint.magicDNS>/`;
- `preflight`: what the policy check found;
- `placement`: which host was picked and why;
- `public`: the public URL, if the app has public routes (`tw-public`);
- `timingsMs`.

While it runs, `tw deploy --json` writes progress events to stderr, one
JSON object per line: `start`, `preflight`, `build`, `placed`, `status`,
`ready` (and `log` lines from container image builds). A failure ends
with the usual `{"error": ...}` line.

## When the preflight fails

The most common first-deploy failure is `acl_denied`: the user's tailnet
policy doesn't yet have a `tagOwners` line for the app's new tags, or a
grant that a `tailnet.connects` entry needs. The error's `hint` has the
exact policy lines to add.

1. Show the user the lines from `hint` and ask them to add them in the
   Tailscale admin console (Access controls). Don't edit the policy
   yourself (see `tw-policy`). Run `tw deploy` with `--json`: then it
   never asks and never writes the policy, it fails with the snippet. (On
   a terminal, plain `tw deploy` asks the user y/N itself.) If the user
   wants tailworks to write the lines, run `tw deploy --apply-policy
   --yes --json` only after they agreed to the lines you showed them.
2. When they say it's done, run `tw policy --json` (or `tw deploy --json`) again. Tailworks re-reads
   the policy every few seconds, so if the same `acl_denied` comes back
   right after they saved it, wait about 10 seconds and retry once more
   before asking them to check it.

`--allow-missing-grants` deploys anyway, but the app can't use what's
missing. Use it only if the user asks for it.

Other deploy failures (`build_failed`, `deploy_failed`,
`identity_pending`, ...): see `tw-debug`.

## Look at what's running

<!-- tw-eval: inspect -->
```sh
tw ls --json        # {"deployments": [...], "objects": [...], "truncated": false}: running deployments and live objects (--all adds killed and stopped)tw status --json    # health of mgmt, the hosts and deployments
```

Each object in `tw ls` is a Durable Object instance (or a workload)
with its own node: `objectID`, `workload`, `namespace`, `objectName`,
`status`, `tags`, `magicDNS` and `parent` (the object that spawned it).
Give `tw ls` a workload name to list only that workload, its previews
and its replicas. `"truncated": true` means more objects matched than
were listed (`--limit 0` lists them all).

## Redeploy

Run `tw deploy` again after a change. Durable Object storage survives a
redeploy. A redeploy stays on the same host; `--replace` asks tailworks to move
it to a better-placed host.

Secrets (`tw-secrets`) only take effect on the next deploy.

## Useful flags

- `--name <workload>`: deploy under another name than `tailworks.jsonc`'s.
- `--var KEY=VALUE`: override a `vars` value (repeatable). Not for secrets.
- `--tenant <tailnet>`: when tailworks serves several tailnets.
- `--no-wait`: return once the deploy is accepted.
- `--wait-public 5m`: also wait until the public URL answers.

`tw deploy --help` lists every flag.
