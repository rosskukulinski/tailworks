# Recipes

Short, complete apps for the things people reach for first. Each one is
three files you can paste into an empty directory (or over a
`twd init <dir> --template hello` app), then run with `twd dev` and ship
with `twd deploy`. Every recipe was run under `twd dev`; where `twd dev`
can't show the whole path (it has no gateways and no internet ingress),
the recipe says what you'll see instead.

- [Call an internet API](#call-an-internet-api)
- [Call an LLM through a gateway on your tailnet](#call-an-llm-through-a-gateway-on-your-tailnet)
- [A Durable Object per user, authorized by `getCaller`](#a-durable-object-per-user-authorized-by-getcaller)
- [A public webhook with signature verification](#a-public-webhook-with-signature-verification)
- [A cron job that writes to KV](#a-cron-job-that-writes-to-kv)

The templates (`twd init <dir> --template <name>`) are longer examples;
each has a README.md. Limits that apply to all of these are in
[limits.md](limits.md).

## Call an internet API

A Worker's outbound `fetch()` reaches **tailnet peers only**. A request to
`https://api.example.com` is refused with `peer_not_visible`, the same as
a peer you have no grant to ([COMPAT.md](COMPAT.md#fetch)). To call an
internet API, put a gateway for it on your tailnet and grant the workload
access to that gateway. The gateway is where internet egress happens, so
it's also the place to log, cache or rate-limit it.

(Containers are the exception: on the microVM tier, a container started
with `enableInternet: true` may reach the hosts in the workload's
`tailnet.egress.internet` allowlist; see [containers.md](containers.md).
`egress.internet` doesn't open the internet to Worker code.)

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
// twd.jsonc
{
  "name": "weather",
  "tailnet": {
    "tags": ["tag:twd-wl-weather"],
    // twd deploy checks that a grant lets this workload reach the gateway
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
import { getCaller } from "@twd/tailnet";

interface Env {
  API_GW: string; // the gateway's address on the tailnet, e.g. "http://api-gw"
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    getCaller(req); // tailnet callers only: throws on a request with no verified caller
    const r = await fetch(`${env.API_GW}/v1/forecast?latitude=52.52&longitude=13.41&current=temperature_2m`);
    const refused = r.headers.get("twd-error"); // set when twd refused the egress, e.g. peer_not_visible
    if (refused) {
      return Response.json({ error: refused, detail: await r.json() }, { status: 502 });
    }
    return Response.json({ forecast: await r.json() });
  },
};
```

**The policy.** `twd deploy` prints the grant if it's missing:

```jsonc
"grants": [
  {"src": ["tag:twd-wl-weather"], "dst": ["tag:api-gw"], "ip": ["tcp:80"]},
]
```

**Run it.** `twd dev --once` has no gateway to reach, so it shows the
refusal your code handles: `{"error": "peer_not_visible", …}` with status
502. After `twd deploy`, a request to the workload returns the forecast.
`api-gw` is the gateway's MagicDNS name; a bare name matches a peer's
first label, and names are never resolved through the host's DNS.

## Call an LLM through a gateway on your tailnet

The same pattern, for a model: run an OpenAI-compatible gateway on a
tailnet machine (an LLM proxy, or a model server with an OpenAI-compatible
`/v1` API), tag it `tag:llm-gw`, and call it from the Worker. The
provider's API key can stay on the gateway; if the gateway wants a key
from each app, keep it in a [secret](secrets.md), not in `vars`.

```jsonc
// twd.jsonc
{
  "name": "ask",
  "tailnet": {
    "tags": ["tag:twd-wl-ask"],
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
import { getCaller } from "@twd/tailnet";

interface Env {
  LLM_URL: string; // an OpenAI-compatible gateway on the tailnet, e.g. "http://llm-gw/v1"
  LLM_MODEL: string;
  LLM_API_KEY?: string; // a secret: `twd secret put LLM_API_KEY`, or .dev.vars for twd dev
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
    if (r.headers.get("twd-error")) {
      return Response.json({ error: r.headers.get("twd-error"), detail: await r.json() }, { status: 502 });
    }
    const out: any = await r.json();
    return Response.json({ answer: out.choices?.[0]?.message?.content ?? null, keySet: !!env.LLM_API_KEY });
  },
};
```

```sh
printf 'LLM_API_KEY=%s\n' "$KEY" > .dev.vars    # twd dev (gitignored by the templates)
twd secret put LLM_API_KEY                       # twd deploy; applies on the next deploy
```

The grant is `{"src": ["tag:twd-wl-ask"], "dst": ["tag:llm-gw"], "ip": ["tcp:80"]}`;
if the gateway listens on another port, put it in `LLM_URL` and the
grant. Under `twd dev` the call ends in `peer_not_visible`, as above.

With the Vercel AI SDK, pass the object's tailnet fetch to the provider
instead: the `ai-agent` template's `src/model.ts` shows the swap from its
mock model to `createOpenAICompatible({ baseURL: "http://llm-gw/v1", fetch: ctx.tailnet.fetch })`.
Inside a Durable Object in facet mode, the request then leaves from that
object's own node, so the gateway (and its logs) sees which agent called.

## A Durable Object per user, authorized by `getCaller`

Route each caller to their own object by their tailnet identity, with no
login or tokens. `principalKey(who)` is the person's login (or the
node's ID for a tagged device). A capability from a tailnet grant lets
some people read everyone's notes.

```jsonc
// twd.jsonc
{
  "name": "notes",
  "tailnet": {
    "tags": ["tag:twd-wl-notes"],
    "objects": {
      // each person's Notes object is its own tailnet node
      "Notes": { "tags": ["tag:twd-notes-notes"] }
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
import { getCaller, principalKey, type Caller } from "@twd/tailnet";

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
  {"src": ["autogroup:member"], "dst": ["tag:twd-wl-notes", "tag:twd-notes-notes"], "ip": ["tcp:80"]},
  {"src": ["group:support"], "dst": ["tag:twd-wl-notes", "tag:twd-notes-notes"], "app": {"example.com/cap/notes": [{"readAll": true}]}},
]
```

**Run it.** Under `twd dev` you are the dev user:

```sh
curl -X POST -d 'buy milk' http://127.0.0.1:8787/   # {"owner":"user-1@…","notes":["buy milk"]}
curl http://127.0.0.1:8787/u/someone-else           # 403 {"error":"forbidden"}
```

`twd dev` has no policy evaluator yet, so it can't show the `readAll`
grant; on a tailnet, `twd ls` lists one object (and node) per person who
has written a note.

## A public webhook with signature verification

Serve one path to the internet over Funnel and check the sender's
signature before you trust the body. This checks GitHub's
`X-Hub-Signature-256` (an HMAC-SHA256 of the raw body). Stripe and Slack
sign a timestamp with the body; check the timestamp's age too.

```jsonc
// twd.jsonc
{
  "name": "hooks",
  // only this path is served to the internet; everything else stays tailnet-only
  "public": { "routes": ["/webhooks/*"] },
  "tailnet": { "tags": ["tag:twd-wl-hooks"] }
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
import { getRequester } from "@twd/tailnet";

interface Env {
  WEBHOOK_SECRET: string; // `twd secret put WEBHOOK_SECRET`; .dev.vars for twd dev
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

**Run it.** `twd dev` has no Funnel, so test the handler from your own
machine (`from` says `tailnet`; deployed, GitHub's requests say `public`):

```sh
printf 'WEBHOOK_SECRET=s3cret\n' > .dev.vars && twd dev
body='{"action":"opened"}'
sig=$(printf %s "$body" | openssl dgst -sha256 -hmac s3cret | sed 's/^.* //')
curl -X POST http://127.0.0.1:8787/webhooks/github \
  -H 'X-GitHub-Event: issues' -H "X-Hub-Signature-256: sha256=$sig" -d "$body"
# {"ok":true,"event":"issues","action":"opened","from":"tailnet"}; a wrong signature gets 401
```

**Deploy it.** `twd secret put WEBHOOK_SECRET`, then `twd deploy`. It
prints the tagOwners and `funnel` nodeAttrs entries to add if they're
missing, and the public URL, `https://hooks.<tailnet>.ts.net/`. Point the
GitHub webhook at `https://hooks.<tailnet>.ts.net/webhooks/github` with
content type `application/json` and the same secret. Unlisted paths get
`404 not_public` and never reach your code, and `getCaller` throws on a
public request. Rate and size limits, the tenant gate and the first
certificate's delay: [public-ingress.md](public-ingress.md).

## A cron job that writes to KV

```jsonc
// twd.jsonc
{
  "name": "tick",
  "tailnet": { "tags": ["tag:twd-wl-tick"] }
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
import { getCaller } from "@twd/tailnet";

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
twd dev --test-scheduled
curl 'http://127.0.0.1:8787/__scheduled?cron=*/15+*+*+*+*'   # {"outcome":"ok","noRetry":false}
curl http://127.0.0.1:8787/                                  # {"last":{"cron":"*/15 * * * *","at":"…"},"runs":["run:…"]}
```

Deployed, the host runs it every 15 minutes; `twd kv key list --binding RUNS`
shows the keys. The namespace (named `RUNS`, after the binding) is created
by the first write. The cron syntax, missed runs and retries are in
[cron-triggers.md](cron-triggers.md); KV's consistency and limits in
[kv.md](kv.md).
