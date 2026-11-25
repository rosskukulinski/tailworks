# Recipes

Short, complete apps for the things people reach for first. Each one is
three files you can paste into an empty directory (or over a
`tw init <dir> --template hello` app), then run with `tw dev` and ship
with `tw deploy`. Every recipe was run under `tw dev`; where `tw dev`
can't show the whole path (it has no gateways and no internet ingress),
the recipe says what you'll see instead.

- [Call an internet API](#call-an-internet-api)
- [Call an LLM through a gateway on your tailnet](#call-an-llm-through-a-gateway-on-your-tailnet)
- [Connect to Postgres on your tailnet](#connect-to-postgres-on-your-tailnet)
- [A Durable Object per user, authorized by `getCaller`](#a-durable-object-per-user-authorized-by-getcaller)
- [Choose which classes get their own node](#choose-which-classes-get-their-own-node)
- [A public webhook with signature verification](#a-public-webhook-with-signature-verification)
- [Use your MCP server from claude.ai or ChatGPT](mcp-public.md): one public `/mcp` with OAuth from tsidp (its own page)
- [A cron job that writes to KV](#a-cron-job-that-writes-to-kv)

The templates (`tw init <dir> --template <name>`) are longer examples;
each has a README.md. Limits that apply to all of these are in
[limits.md](limits.md).

## Call an internet API

**Directly.** Workers have internet egress by default, as on Cloudflare:
`fetch("https://api.example.com/...")` just works. The request leaves from
the host's own network, not from a tailnet node, so the API sees the
host's source address (a different one on each host), never a tailnet
identity. A host can instead send all of it through a tailnet exit node
([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressvia)), which gives
one fixed source address per exit node. Private, link-local (cloud metadata), loopback, tailnet and host
addresses are always refused, including through a DNS name that resolves
to one ([security-model.md](security-model.md#egress)).

To turn it off, or limit it to a few hosts, set `tailnet.egress.internet`
([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressinternet)):

```jsonc
// tailworks.jsonc
{
  "tailnet": {
    "tags": ["tag:tw-wl-weather"],
    "egress": { "internet": ["api.open-meteo.com"] } // or false: no internet at all
  }
}
```

A destination that the setting refuses gets 403
[`internet_egress_unavailable`](errors.md#internet_egress_unavailable),
whose detail links here. The same setting caps a container's
`enableInternet` ([containers.md](containers.md)).

**Through a gateway.** When internet egress is off, or the API allowlists
a fixed source address, or you want one place to log, cache or rate-limit
calls, put a gateway for the API on your tailnet and grant the workload
access to it.

**The gateway.** Any HTTP reverse proxy on a tailnet machine works. Tag
the machine `tag:api-gw`. With [Caddy](https://caddyserver.com/), for
example:

```
:80 {
	reverse_proxy https://api.open-meteo.com {
		header_up Host {upstream_hostport}
	}
}
```

**The app.**

```jsonc
// tailworks.jsonc
{
  "name": "weather",
  "tailnet": {
    "tags": ["tag:tw-wl-weather"],
    // tw deploy checks that a grant lets this workload reach the gateway
    "connects": ["tag:api-gw"]
  }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "weather",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01",
  "vars": { "API_GW": "http://api-gw" }
}
```

```ts
// src/index.ts
import { getCaller } from "@tailworks/tailnet";

interface Env {
  API_GW: string; // the gateway's address on the tailnet, e.g. "http://api-gw"
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    getCaller(req); // tailnet callers only: throws on a request with no verified caller
    const r = await fetch(`${env.API_GW}/v1/forecast?latitude=52.52&longitude=13.41&current=temperature_2m`);
    const refused = r.headers.get("tw-error"); // set when tw refused the egress, e.g. peer_not_visible
    if (refused) {
      return Response.json({ error: refused, detail: await r.json() }, { status: 502 });
    }
    return Response.json({ forecast: await r.json() });
  },
};
```

**The policy.** `tw deploy` prints the grant if it's missing:

```jsonc
"grants": [
  {"src": ["tag:tw-wl-weather"], "dst": ["tag:api-gw"], "ip": ["tcp:80"]},
]
```

**Run it.** `tw dev --once` has no gateway to reach, so it shows the
refusal your code handles: `{"error": "peer_not_visible", …}` with status
502. After `tw deploy`, a request to the workload returns the forecast.
`api-gw` is the gateway's MagicDNS name; a bare name matches a peer's
first label, and names are never resolved through the host's DNS.

## Call an LLM through a gateway on your tailnet

The same pattern, for a model: run an OpenAI-compatible gateway on a
tailnet machine (an LLM proxy, or a model server with an OpenAI-compatible
`/v1` API), tag it `tag:llm-gw`, and call it from the Worker. The
provider's API key can stay on the gateway; if the gateway wants a key
from each app, keep it in a [secret](secrets.md), not in `vars`.

```jsonc
// tailworks.jsonc
{
  "name": "ask",
  "tailnet": {
    "tags": ["tag:tw-wl-ask"],
    "connects": ["tag:llm-gw"]
  }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "ask",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01",
  "vars": { "LLM_URL": "http://llm-gw/v1", "LLM_MODEL": "my-model" }
}
```

```ts
// src/index.ts
import { getCaller } from "@tailworks/tailnet";

interface Env {
  LLM_URL: string; // an OpenAI-compatible gateway on the tailnet, e.g. "http://llm-gw/v1"
  LLM_MODEL: string;
  LLM_API_KEY?: string; // a secret: `tw secret put LLM_API_KEY`, or .dev.vars for tw dev
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const who = getCaller(req);
    const prompt = new URL(req.url).searchParams.get("q") ?? "Say hello.";
    const r = await fetch(`${env.LLM_URL}/chat/completions`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        ...(env.LLM_API_KEY ? { authorization: `Bearer ${env.LLM_API_KEY}` } : {}),
      },
      body: JSON.stringify({
        model: env.LLM_MODEL,
        messages: [{ role: "user", content: prompt }],
        user: who.user?.login ?? who.nodeId, // lets the gateway attribute usage per caller
      }),
    });
    if (r.headers.get("tw-error")) {
      return Response.json({ error: r.headers.get("tw-error"), detail: await r.json() }, { status: 502 });
    }
    const out: any = await r.json();
    return Response.json({ answer: out.choices?.[0]?.message?.content ?? null, keySet: !!env.LLM_API_KEY });
  },
};
```

```sh
printf 'LLM_API_KEY=%s\n' "$KEY" > .dev.vars    # tw dev (gitignored by the templates)
tw secret put LLM_API_KEY                       # tw deploy; applies on the next deploy
```

The grant is `{"src": ["tag:tw-wl-ask"], "dst": ["tag:llm-gw"], "ip": ["tcp:80"]}`;
if the gateway listens on another port, put it in `LLM_URL` and the
grant. Under `tw dev` the call ends in `peer_not_visible`, as above.

With the Vercel AI SDK, pass the object's tailnet fetch to the provider
instead: the `ai-agent` template's `src/model.ts` shows the swap from its
mock model to `createOpenAICompatible({ baseURL: "http://llm-gw/v1", fetch: ctx.tailnet.fetch })`.
Inside a Durable Object in facet mode, the request then leaves from that
object's own node, so the gateway (and its logs) sees which agent called.

## Connect to Postgres on your tailnet

Database drivers that run on Workers use `connect()` from
`cloudflare:sockets`, and so does tailworks (ADR-052): the socket leaves
from the workload's node (from the Worker) or the object's own node (from a
facet-mode Durable Object), under the same grants as `fetch()`. Here
[postgres.js](https://github.com/porsager/postgres) talks to a Postgres on
a tailnet machine tagged `tag:db`. Put the port in `tailnet.connects`, so
`tw deploy` checks and prints the grant for `tcp:5432` instead of `tcp:80`.

```jsonc
// tailworks.jsonc
{
  "name": "notes",
  "tailnet": {
    "tags": ["tag:tw-wl-notes"],
    "connects": ["tag:db:5432"]
  }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "notes",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01",
  "compatibility_flags": ["nodejs_compat"],
  "vars": { "PGHOST": "db", "PGDATABASE": "notes", "PGUSER": "notes" }
}
```

```ts
// src/index.ts
import postgres from "postgres";

interface Env {
  PGHOST: string; // the database's tailnet name
  PGDATABASE: string;
  PGUSER: string;
  PGPASSWORD: string; // a secret: `tw secret put PGPASSWORD`, or .dev.vars for tw dev
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    // ssl: false. WireGuard already encrypts the flow, and workerd can't
    // hand a TLS socket to tailworks' egress (COMPAT.md#connect).
    const sql = postgres({ host: env.PGHOST, port: 5432, database: env.PGDATABASE, username: env.PGUSER, password: env.PGPASSWORD, ssl: false, max: 1, fetch_types: false });
    try {
      const rows = await sql`SELECT now() AS at, current_user AS "user"`;
      return Response.json(rows[0]);
    } finally {
      await sql.end();
    }
  },
};
```

```sh
npm install postgres
tw secret put PGPASSWORD
tw deploy
```

The grant is `{"src": ["tag:tw-wl-notes"], "dst": ["tag:db"], "ip": ["tcp:5432"]}`.
On the database machine, allow the workload's tailnet addresses in
`pg_hba.conf` (`100.64.0.0/10` and `fd7a:115c:a1e0::/48`, or tighter).
The database sees each connection from the workload's node, so
`tailscale whois <client address>` on it names the workload. From a
Durable Object in facet mode it names that object.

Things to know:

- A refused socket (no grant, a name not in the netmap) reaches the driver
  as "Network connection lost". `tw audit <object>` shows it as a `deny`
  with the code. `ctx.tailnet.connect()` gives the code directly,
  as a `TailnetError`.
- A database on the internet needs TLS, which stock `connect()` can't do
  here (`secureTransport` fails). `ctx.tailnet.connect({ hostname, port },
  { tls: "netd" })` returns a socket whose TLS the host does. Use it with a
  driver that lets you supply the socket.
- Each flow is an audit record and a trace span. Keep connections few:
  `max: 1` per request as above, or hold one in a Durable Object.
- `make e2e-pg-socket` runs the same driver code against Postgres 17, from
  a Worker and from a facet object (`hostd/testdata/apps/pgprobe`).

## A Durable Object per user, authorized by `getCaller`

Route each caller to their own object by their tailnet identity, with no
login or tokens. `principalKey(who)` is the person's login (or the
node's ID for a tagged device). A capability from a tailnet grant lets
some people read everyone's notes.

```jsonc
// tailworks.jsonc
{
  "name": "notes",
  "tailnet": {
    "tags": ["tag:tw-wl-notes"],
    "objects": {
      // each person's Notes object is its own tailnet node
      "Notes": { "tags": ["tag:tw-notes-notes"] }
    }
  }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "notes",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01",
  "durable_objects": { "bindings": [{ "name": "NOTES", "class_name": "Notes" }] },
  "migrations": [{ "tag": "v1", "new_sqlite_classes": ["Notes"] }]
}
```

```ts
// src/index.ts
import { DurableObject } from "cloudflare:workers";
import { getCaller, principalKey, type Caller } from "@tailworks/tailnet";

interface Env {
  NOTES: DurableObjectNamespace<Notes>;
}

// May `who` read the notes named `owner`? Their own, or anyone's with the
// readAll capability from a tailnet grant.
function allowed(who: Caller, owner: string): boolean {
  return principalKey(who) === owner || who.caps<{ readAll?: boolean }>("example.com/cap/notes").some((c) => c.readAll);
}

// One Notes object per person: its storage holds only that person's notes.
export class Notes extends DurableObject<Env> {
  async fetch(req: Request): Promise<Response> {
    // Check here too: each object is its own tailnet node, so a caller can
    // reach it directly, without going through the Worker below.
    const who = getCaller(req); // request.cf travels with the request, so the object sees the caller
    const owner = this.ctx.id.name!; // the name the Worker routed by
    if (!allowed(who, owner)) {
      return Response.json({ error: "forbidden", detail: `these are ${owner}'s notes` }, { status: 403 });
    }
    if (req.method === "POST") {
      const notes = (await this.ctx.storage.get<string[]>("notes")) ?? [];
      notes.push(await req.text());
      await this.ctx.storage.put("notes", notes);
    }
    return Response.json({ owner, notes: (await this.ctx.storage.get<string[]>("notes")) ?? [] });
  }
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const who = getCaller(req);
    // GET|POST /         your own notes
    // GET /u/<login>     someone else's: only with the readAll capability
    const m = new URL(req.url).pathname.match(/^\/u\/([^/]+)$/);
    const name = m ? decodeURIComponent(m[1]) : principalKey(who);
    if (!allowed(who, name)) {
      // refuse before getByName: don't create an object (and its node) for a refused request
      return Response.json({ error: "forbidden" }, { status: 403 });
    }
    return env.NOTES.getByName(name).fetch(req); // pass the original request on: it carries the caller
  },
};
```

**The policy.** Members may call the app; a support group may read
everyone's notes:

```jsonc
"grants": [
  {"src": ["autogroup:member"], "dst": ["tag:tw-wl-notes", "tag:tw-notes-notes"], "ip": ["tcp:80"]},
  {"src": ["group:support"], "dst": ["tag:tw-wl-notes", "tag:tw-notes-notes"], "app": {"example.com/cap/notes": [{"readAll": true}]}},
]
```

**Run it.** Under `tw dev` you are the dev user:

```sh
curl -X POST -d 'buy milk' http://127.0.0.1:8787/   # {"owner":"user-1@…","notes":["buy milk"]}
curl http://127.0.0.1:8787/u/someone-else           # 403 {"error":"forbidden"}
```

`tw dev` has no policy evaluator yet, so it can't show the `readAll`
grant; on a tailnet, `tw ls` lists one object (and node) per person who
has written a note.

## Choose which classes get their own node

This one is a decision, not an app. Each object of a class with `tags` in
`tailnet.objects` is a node, which is a device on your tailnet: your
Tailscale plan's device limit applies, and so does the tenant's
`maxObjects` ([limits.md](limits.md#tailnet-node-budget)). So give a class
its own identity when the tailnet has to tell its objects apart, and not
otherwise.

**Give the class tags** (a node per object) when:

- it is an agent, or anything that acts on its own: you want grants,
  `getCaller()` on the far side, flow logs and the audit trail to name
  the object, not the app;
- it calls out to other tailnet services, and what it may reach should be
  a grant on its tag rather than on the whole workload's;
- you want to kill or grant one object: `tw kill` takes that object's
  node off the tailnet and leaves the rest of the app running;
- it spawns sub-agents, which can hold at most their parent's grants.

**Set `"node": false`** (no node; objects are reached through the
workload) when:

- there is one object per user, session or key, and they are only state:
  counters, caches, rate limiters, per-user settings;
- the number of objects grows with your users or your data rather than
  with what you deploy: chat rooms, documents, game lobbies;
- nothing outside the workload needs to call an object directly, and the
  objects don't need different grants from the workload's.

```jsonc
// tailworks.jsonc
{
  "name": "support",
  "tailnet": {
    "tags": ["tag:tw-wl-support"],
    "objects": {
      // a few, long-lived, each acting for one customer: a node each
      "SupportAgent": { "tags": ["tag:tw-agent-support"] },
      // one per chat room and one per user: state only, no nodes
      "Room": { "node": false },
      "RateLimit": { "node": false }
    }
  }
}
```

The Worker in front of a `"node": false` class is where you authorize:
callers reach the workload node, so check `getCaller(req)` there before
`env.ROOM.getByName(...)`, as the recipe above does.

`"egress": "workload"` (plain-DO mode) is a different choice. The class
still has a node per object for calls in; only its outbound `fetch()`
leaves from the workload node. It doesn't save nodes
([egress-modes.md](egress-modes.md)). `"node": false` implies plain-DO
mode, and moving a class between facet mode and plain-DO mode starts its
objects with empty storage, so decide before you store data.

`tw policy` prints what an app will use before you deploy it, and `tw ls`
what the tenant uses now:

```
  nodes      this app is 1 workload node + 1 node per SupportAgent object (RateLimit, Room objects share the workload node); tenant example.ts.net has 3 live nodes: 1 workload, 2 objects (maxObjects 1000)
```

## A public webhook with signature verification

Serve one path to the internet over Funnel and check the sender's
signature before you trust the body. This checks GitHub's
`X-Hub-Signature-256` (an HMAC-SHA256 of the raw body). Stripe and Slack
sign a timestamp with the body; check the timestamp's age too.

```jsonc
// tailworks.jsonc
{
  "name": "hooks",
  // only this path is served to the internet; everything else stays tailnet-only
  "public": { "routes": ["/webhooks/*"] },
  "tailnet": { "tags": ["tag:tw-wl-hooks"] }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "hooks",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01"
}
```

```ts
// src/index.ts
import { getRequester } from "@tailworks/tailnet";

interface Env {
  WEBHOOK_SECRET: string; // `tw secret put WEBHOOK_SECRET`; .dev.vars for tw dev
}

// GitHub-style signature: X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of the raw body>.
async function verified(secret: string, body: ArrayBuffer, header: string | null): Promise<boolean> {
  if (!secret || !header?.startsWith("sha256=")) return false;
  const hex = header.slice("sha256=".length);
  if (!/^[0-9a-f]{64}$/.test(hex)) return false;
  const key = await crypto.subtle.importKey("raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  const sig = new Uint8Array(hex.match(/../g)!.map((b) => parseInt(b, 16)));
  return crypto.subtle.verify("HMAC", key, sig, body); // constant-time comparison
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    if (req.method === "POST" && url.pathname === "/webhooks/github") {
      const body = await req.arrayBuffer(); // verify the raw bytes, before parsing
      if (!(await verified(env.WEBHOOK_SECRET, body, req.headers.get("x-hub-signature-256")))) {
        return Response.json({ error: "bad_signature" }, { status: 401 });
      }
      const event = JSON.parse(new TextDecoder().decode(body));
      return Response.json({ ok: true, event: req.headers.get("x-github-event"), action: event.action ?? null, from: getRequester(req).kind });
    }
    return Response.json({ error: "not_found" }, { status: 404 });
  },
};
```

**Run it.** `tw dev` has no Funnel, so test the handler from your own
machine (`from` says `tailnet`; deployed, GitHub's requests say `public`):

```sh
printf 'WEBHOOK_SECRET=s3cret\n' > .dev.vars && tw dev
body='{"action":"opened"}'
sig=$(printf %s "$body" | openssl dgst -sha256 -hmac s3cret | sed 's/^.* //')
curl -X POST http://127.0.0.1:8787/webhooks/github \
  -H 'X-GitHub-Event: issues' -H "X-Hub-Signature-256: sha256=$sig" -d "$body"
# {"ok":true,"event":"issues","action":"opened","from":"tailnet"}; a wrong signature gets 401
```

**Deploy it.** `tw secret put WEBHOOK_SECRET`, then `tw deploy`. It
prints the tagOwners and `funnel` nodeAttrs entries to add if they're
missing, and the public URL, `https://hooks.<tailnet>.ts.net/`. Point the
GitHub webhook at `https://hooks.<tailnet>.ts.net/webhooks/github` with
content type `application/json` and the same secret. Unlisted paths get
`404 not_public` and never reach your code, and `getCaller` throws on a
public request. Rate and size limits, the tenant gate and the first
certificate's delay: [public-ingress.md](public-ingress.md).

## A cron job that writes to KV

```jsonc
// tailworks.jsonc
{
  "name": "tick",
  "tailnet": { "tags": ["tag:tw-wl-tick"] }
}
```

```jsonc
// wrangler.jsonc
{
  "name": "tick",
  "main": "src/index.ts",
  "compatibility_date": "2026-04-01",
  "triggers": { "crons": ["*/15 * * * *"] },
  "kv_namespaces": [{ "binding": "RUNS" }]
}
```

```ts
// src/index.ts
import { getCaller } from "@tailworks/tailnet";

interface Env {
  RUNS: KVNamespace;
}

export default {
  // Runs on every cron in wrangler.jsonc, on the host, in UTC.
  async scheduled(controller: ScheduledController, env: Env): Promise<void> {
    const at = new Date(controller.scheduledTime).toISOString();
    await env.RUNS.put("last", JSON.stringify({ cron: controller.cron, at }));
    await env.RUNS.put(`run:${at}`, controller.cron, { expirationTtl: 7 * 24 * 3600 }); // keep a week of history
  },

  async fetch(req: Request, env: Env): Promise<Response> {
    getCaller(req);
    const last = await env.RUNS.get("last", "json");
    const { keys } = await env.RUNS.list({ prefix: "run:" });
    return Response.json({ last, runs: keys.map((k) => k.name) });
  },
};
```

**Run it.** `--test-scheduled` adds wrangler's `/__scheduled` route, which
runs a trigger now:

```sh
tw dev --test-scheduled
curl 'http://127.0.0.1:8787/__scheduled?cron=*/15+*+*+*+*'   # {"outcome":"ok","noRetry":false}
curl http://127.0.0.1:8787/                                  # {"last":{"cron":"*/15 * * * *","at":"…"},"runs":["run:…"]}
```

Deployed, the host runs it every 15 minutes; `tw kv key ls` in the app's
directory shows the keys (of its only namespace). The namespace (named
`RUNS`, after the binding) is created by the first write. The cron syntax, missed runs and retries are in
[cron-triggers.md](cron-triggers.md); KV's consistency and limits in
[kv.md](kv.md).
