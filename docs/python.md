# Python Workers

tailworks runs Cloudflare Python Workers: a wrangler project whose `main` is a
`.py` module, with `"python_workers"` in `compatibility_flags`, deploys with
`tw deploy` and runs under `tw dev` as on Cloudflare. The code runs in Pyodide
inside workerd, with the same `workers` SDK (`WorkerEntrypoint`,
`DurableObject`, `Response`, `fetch`) and the same `js` interop. Design:
[ADR-055](design/adr/055-python-workers.md). What matches Cloudflare
and what doesn't: [COMPAT.md](COMPAT.md#python).

```sh
tw init my-py --template python
tw dev my-py                     # http://127.0.0.1:8787
tw deploy my-py
```

## A Worker

```jsonc
// wrangler.jsonc
{
  "name": "hello-py",
  "main": "src/entry.py",
  "compatibility_date": "2026-04-01",
  "compatibility_flags": ["python_workers"]
}
```

```python
# src/entry.py
from workers import Response, WorkerEntrypoint


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        return Response("Hello from Python")
```

Bindings work as in a JavaScript Worker, through `self.env`: `vars`,
[secrets](secrets.md), [KV](kv.md), [R2](r2.md), [D1](d1.md), Durable
Object namespaces and [service bindings](wrangler.md#service-bindings-services).
Outbound `fetch()` leaves from the workload's node, so tailnet policy decides
what it can reach, as for any Worker ([COMPAT.md](COMPAT.md#fetch)).

## Who is calling

The caller's tailnet identity is in `request.cf`, as for a JavaScript Worker.
tailworks checks it before your code runs: `tailscale.caller` is present only
when hostd's seal over it verified, so it can't be forged by a client. The
`python` template ships a small `tailworks.py` with the helper:

```python
from tailworks import get_caller


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        who = get_caller(request)  # None for a public request
        login = who["user"]["login"] if who and who["user"] else "anonymous"
        return Response(f"hello {login}")
```

`get_caller` returns a dict with `getCaller`'s fields from
`@tailworks/tailnet`: `nodeId`, `tags`, `hostname`, `node`, `addrs`, `user`
(`{"login": …}` for a person, `None` for a tagged node), `capMap`,
`objectId` and `traceId`. It reads `request.js_object.cf.tailscale.caller`
(in an RPC method, `get_caller(self.ctx)` reads `self.ctx.cf`, below);
copy the file into your own project to use it there.

## RPC from other Workers

Another workload can call a Python Worker's methods over a
[service binding](wrangler.md#service-bindings), as it calls a
JavaScript `WorkerEntrypoint`'s:

```python
class Default(WorkerEntrypoint):
    async def highlight(self, code, language=None):
        who = get_caller(self.ctx)  # the calling workload's node
        return {"html": render(code, language), "for": who["hostname"]}
```

```js
// the caller's wrangler.jsonc: "services": [{ "binding": "PY", "service": "my-py" }]
const r = await env.PY.highlight("print(1)", "python");
```

A public method of the `Default` class, or of a named `WorkerEntrypoint`
class (a binding with `"entrypoint": "Admin"`), can be called. Handler
names (`fetch`, `scheduled`, `queue`, …) and names starting with `_` can't.
Arguments arrive as Python values and the return value goes back as the
`workers` SDK converts them, as on Cloudflare: a returned `dict` reaches
JavaScript as a plain object with the vendored `workers-runtime-sdk`, and
as a `Map` with the SDK built in before `compatibility_date` 2026-04-21. An exception reaches the caller as an `Error` with the
Python exception in its message. `get_caller(self.ctx)` names the caller,
as `getCaller(this.ctx)` does in JavaScript: tailworks puts the verified
caller in `self.ctx.cf` for the call.

## Cron triggers, Queues and static assets

They work as for a JavaScript Worker, declared the same way in
`wrangler.jsonc`:

- **Cron triggers** (`triggers.crons`, [cron-triggers.md](cron-triggers.md))
  run the default export's `scheduled(self, controller, env, ctx)`.
  `controller.cron`, `controller.scheduledTime` and `controller.noRetry()`
  work; an exception marks the run failed.
- **Queues** ([queues.md](queues.md)): a producer binding's `send()` and
  `sendBatch()`, and a consumer runs the default export's
  `queue(self, batch, env, ctx)`. Per-message `ack()` and `retry()`, and
  `ackAll()` / `retryAll()` on the batch, are applied as on Cloudflare; an
  exception retries the messages it didn't ack.
- **Static assets** (`assets`, [wrangler.md](wrangler.md#assets)) are served
  in front of the Worker, with `env.ASSETS.fetch()` in Python code.

```python
class Default(WorkerEntrypoint):
    async def scheduled(self, controller, env, ctx):
        print("cron", controller.cron)

    async def queue(self, batch, env, ctx):
        for m in batch.messages:
            print(m.body)
            m.ack()
```

## Durable Objects

A Python `DurableObject` class works with its storage (`self.ctx.storage`,
KV and SQL), RPC methods, `fetch` and the WebSocket hibernation API.

```python
from workers import DurableObject, Response, WorkerEntrypoint


class Counter(DurableObject):
    async def increment(self):
        n = (await self.ctx.storage.get("n") or 0) + 1
        await self.ctx.storage.put("n", n)
        return n


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        n = await self.env.COUNTER.getByName("global").increment()
        return Response(str(n))
```

Python Durable Objects are **internal objects**: they run in plain-DO mode
and have no tailnet node of their own, like a class with `"node": false` in
[tailworks.jsonc](tailworks-jsonc.md). They are reached through the Worker's
`env`, not over the tailnet, and their egress leaves as the workload. So
`tailnet.objects.<Class>` can't give a Python class tags, `ttl` or
`public`, and `ctx.tailnet` (spawn, kill) isn't available to Python code.
[Egress modes](egress-modes.md) explains the difference.

## Modules and files

There is no bundler. `tw deploy` ships, as pywrangler does:

- every file under the directory of `main` (`src/` for `src/entry.py`):
  `.py` files as Python modules named relative to it (`import helper` for
  `src/helper.py`), and the rest as files your code can read
  (`Path(__file__).parent / "page.html"`). `__pycache__`, `.pyc` files,
  dot-directories, `node_modules` and `venv` are left out.
- the vendored packages in `python_modules/` (next to `main`, else at the
  project root), under `python_modules/`.

## Packages

Packages are vendored at build time and shipped with the Worker, never
installed on a host. Vendor them with Cloudflare's tooling, then deploy:

```sh
uv run pywrangler sync     # writes python_modules/ from pyproject.toml
tw deploy
```

What pywrangler vendors works, compiled packages included: it resolves
packages for Pyodide, so a package with native code ships as Pyodide's
WebAssembly build of it (`pydantic-core` under FastAPI, and Pillow, are
tested; [Cloudflare's examples](COMPAT.md#upstream-examples-python-workers)
also run Django, Flask, httpx/requests and the Python MCP SDK). Nothing is
fetched at run time, so a package that isn't vendored isn't there.
`tw deploy` warns when `pyproject.toml` or
`requirements.txt` declares packages and there is no `python_modules/`.

From `compatibility_date` 2026-04-21 the `workers` SDK itself is a package
(`workers-runtime-sdk`, which `pywrangler sync` vendors). Without it,
`tw deploy` refuses a Worker that imports `workers` and says how to fix it:
vendor it, add `"disable_python_external_sdk"` to `compatibility_flags`, or
use an earlier date. The template uses 2026-04-01, which has the SDK built in.

## Pyodide on the host

workerd needs a Pyodide bundle to start a Python Worker. tailworks pins the
bundles the pinned workerd uses, by SHA-256 (`internal/pyodide`). The first
Python deployment on a host fetches them once into the host's bundle
directory (`tw-hostd -pyodide-dir`, default `<data-dir>/pyodide`; `tw dev`
uses the per-user cache, `~/Library/Caches/tailworks/pyodide` on macOS and
`~/.cache/tailworks/pyodide` on Linux) and checks each digest. workerd then
reads them read-only: its sandbox gains read access to that one directory and
nothing else, and it never downloads anything itself.

On the **microVM tier** the guest image carries the same pinned bundles, in
`/usr/share/tailworks/pyodide` on its read-only root filesystem
(`guest/build-image.sh`), so a microVM host fetches nothing and the guest,
which has no network, has them at boot. A Python Worker takes more memory
than a JavaScript one: about 250 MiB for workerd with one Python Worker
and one Durable Object, against about 30 MiB without Python. That fits the
default 512 MiB guest (`tw-hostd -vm-mem-mib`), with less room left for the
objects' own data than a JavaScript workload has.

### Offline hosts

On a host with no internet access, copy the bundles into the directory before
the first Python deployment. The file names and digests are in
[`internal/pyodide/bundles.txt`](../internal/pyodide/bundles.txt); each file is
at `https://pyodide-capnp-bin.edgeworker.net/<file name>`. A missing or
corrupt bundle fails the deployment with `python_unavailable`
([errors.md](errors.md#python_unavailable)).

### Updating the pins

The bundles follow the workerd pin. When a workerd bump changes them,
`internal/pyodide:TestBundlesMatchWorkerd` fails; add the new bundle's name,
SHA-256 and size to `internal/pyodide/bundles.txt`. The process tier and the
guest image build both read that file, so a change to it is a guest input
change and needs a new guest image.

## Limits

Not supported for Python Workers yet; `tw deploy` and `tw dev` refuse each
one with a message naming it:

- Workflows and containers ([#424](https://github.com/rosskukulinski/tailworks/issues/424));
- `--wrangler` and `--from-wrangler-out`: they bundle JavaScript.

A named entrypoint of a Python Worker (a binding with `entrypoint`) answers
RPC calls but not `fetch()`, which gets `service_not_found`; bind the
default entrypoint for `fetch()`. The generated main module is named
`_tw_main.py`, so the app can't have a module of that name, and its main
module can't define a `TailworksRpc`.

Also different from Cloudflare: no memory snapshots, so every cold start
loads Pyodide (about a second on a laptop;
[#426](https://github.com/rosskukulinski/tailworks/issues/426)); packages
come only from `python_modules/`, never downloaded at run time.
