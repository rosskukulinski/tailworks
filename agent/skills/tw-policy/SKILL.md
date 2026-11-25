---
name: tw-policy
description: Use when a tailworks workload needs tailnet policy changes - tagOwners for a new app's tags, a grant so people or other workloads can call it, a grant so it can call something else (tailnet.connects), or MCP tool permissions via an app capability.
---

# Tailnet policy for a tailworks workload

On tailworks, access control is the user's Tailscale tailnet policy, not code.
Each workload node carries the tags from `tailworks.jsonc` `tailnet.tags`
(default `tag:tw-wl-<workload>`); each
Durable Object class with its own nodes carries its class's tags. Grants
between tags decide who can call whom.

**You draft policy; the user applies it.** Show the user the exact lines
and ask them to add them in the Tailscale admin console (Access
controls). Tailworks writes the policy only after the user answers y
to the lines it shows (plain `tw up`, `tw deploy` and `tw tenant add`
ask on a terminal; so do `--apply-policy` and `tw policy --apply`), or
with `--apply-policy --yes`. With `--json`, which you use, nothing asks
and nothing is written; a bare `--yes` never writes the policy. Pass
`--apply-policy --yes` only after the user agreed to the lines you
showed them, and don't edit the policy yourself.

## 1. Let tailworks own the app's tags

Every tag the app uses needs a `tagOwners` entry that includes `tag:tw`,
or tailworks can't create nodes with it. `tw deploy` checks this first and, if
a line is missing, fails with `acl_denied` and puts the lines in the
error's `hint`. They look like this:

```jsonc
"tagOwners": {
  "tag:tw-wl-hello-mcp": ["autogroup:admin", "tag:tw"],
}
```

A class whose objects spawn children (`ctx.tailnet.spawn`) needs the
child's tag owned by the parent's tag too:

```jsonc
"tag:tw-myapp-child": ["autogroup:admin", "tag:tw", "tag:tw-myapp-parent"],
```

## 2. Let callers reach it

Nothing on the tailnet can call a new workload until a grant allows it.
Workloads listen on port 80:

```jsonc
"grants": [
  // people in the tailnet may call the app
  {"src": ["autogroup:member"], "dst": ["tag:tw-wl-hello-mcp"], "ip": ["tcp:80"]},
]
```

Use the narrowest `src` that works: a specific user, a group, or another
workload's tag.

## 3. Let it call something

List what the app calls in `tailworks.jsonc` `tailnet.connects` (tags or
hosts). `tw deploy` preflights a grant for each one and prints the grant
to add if it's missing:

```jsonc
{"src": ["tag:tw-wl-hello-mcp"], "dst": ["tag:db"], "ip": ["tcp:5432"]},
```

## 4. MCP tool permissions

The `mcp` template authorizes each tool call from the caller's grant: the
tools a caller may use are an app capability on the grant to the server
(`example.com/cap/mcp` unless `MCP_CAP` in `wrangler.jsonc` says
otherwise). A caller without the tool gets `cap_missing`.

```jsonc
{
  "src": ["tag:tw-wl-my-agent"],
  "dst": ["tag:tw-wl-hello-mcp"],
  "ip": ["tcp:80"],
  "app": {
    "example.com/cap/mcp": [{
      "tools": ["lookup_order", "refund"],
      "limits": {"refund": {"maxUsd": 50, "perDayUsd": 200}}
    }]
  }
}
```

## Checking it

- `tw deploy --json` again: the `preflight` in its result lists what it
  checked, and `acl_denied` lists what's still missing.
- A grant applies within seconds of saving the policy; nodes don't need
  to restart.
- `tw docs show tailworks-jsonc.md#tags-and-tw-init` explains how
  `tw init` names tags and how to reuse tags across apps.
