# ADR-055: Python Workers run in a Python worker of their own behind a generated JavaScript front; Pyodide bundles are pinned, fetched once per host and read-only to the sandbox

**Date:** 2026-11-24
**Status:** Accepted (2026-11-24; built on branch `python-support-292`; drafted as `ADR-python-workers`;
issue #292). Amended 2026-11-24 (branch `python-cron-queues-assets`):
cron triggers, Queues and static assets work for Python (decision 8).
Amended 2026-11-24 (branch `python-microvm`): Python Workers run on the
microVM tier (decision 9).

## Context

- A Cloudflare Python Worker is a wrangler project whose `main` is a `.py`
  module, with `"python_workers"` in `compatibility_flags`. workerd runs it
  in Pyodide (CPython compiled to WebAssembly): modules of type
  `pythonModule`, the `workers` SDK (`WorkerEntrypoint`, `DurableObject`,
  `Response`, `fetch`), and the `js` module for JavaScript interop.
  Packages are vendored at build time by pywrangler (`uv run pywrangler
  sync` writes `python_modules/`) and uploaded as modules.
- tailworks never runs the tenant's main module as the worker's main
  module. hostd generates `__tw_main.js` (and `__tw_facet.js` for facets),
  which `import * as T from "./<main>"` and wrap the default export and the
  Durable Object classes (`wrapHandler`, `wrapPlainClass`,
  `wrapFacetClass`). The wrapper is where the caller seal is checked
  (RT3-08), where public and tailnet requests are told apart, where
  traces start, and where `ctx.tailnet`, the guarded `env` (create
  admission), the storage and workflow facet shims, `ctx.container` and
  the kill check come from.
- workerd can't import a Python module from JavaScript. On the pinned
  workerd 1.20261121.1, a worker whose main module is an ES module that
  imports a `pythonModule` (with `python_workers` set) fails to start
  ("Module … already added to bundle"): a Python worker's main module must
  be the `.py` module. So wrap-by-import can't wrap Python.
- workerd fetches a Pyodide bundle (`pyodide_<version>_<date>_<n>.capnp.bin`,
  14 to 23 MB) from `pyodide-capnp-bin.edgeworker.net` the first time it
  starts a Python Worker, unless it is in `--pyodide-bundle-disk-cache-dir`.
  Which bundle follows the compatibility date and flags: the pinned
  workerd uses three (0.26.0a2, 0.28.2, and 314.0.6 for Python 3.14).
  Process-tier workerd runs under Seatbelt or Landlock with no network and
  writes only to its instance and DO storage directories.
- From compatibility date 2026-04-21 the runtime no longer has the
  `workers` SDK built in: it is the `workers-runtime-sdk` package, which
  pywrangler vendors, unless `disable_python_external_sdk` is set.
  (Measured on the pinned workerd: 2026-04-20 works without it, 2026-04-21
  fails at startup with "No module named 'workers'".)

## Decision

1. **A Python Worker runs in a second tenant worker, `tenant-py`, behind
   a generated JavaScript front.** For a Python app, hostd/gen gives the
   usual `tenant` worker a generated main module (`__tw_py.js`) whose
   default export forwards each request to `tenant-py` over a service
   binding (`__TW_PY`). `__tw_main.js` wraps that front exactly as it wraps
   a JavaScript Worker, so the caller seal, the public/tailnet split and
   tracing are the same code at the same enforcement point.
   `request.cf` (with the verified `tailscale.caller`, or none) crosses the
   service binding intact. Only the front names `tenant-py`: no socket, no
   platform binding and no other service can reach it, so a forged caller
   can't be delivered to Python code. (Option (a) of #292.)
2. **`tenant-py` has the app's own bindings directly**: vars, secrets,
   KV/R2/D1 (workerd's `kvNamespace`, `r2Bucket` and wrapped D1 bindings
   to the platform's store backends, the same lines the JavaScript tenant
   gets), service bindings (the platform's `ServiceBinding` entrypoint),
   its Durable Object namespaces, the tenant's `globalOutbound`
   (`WorkloadEgress`) and the workload's tails. The front's env doesn't
   carry them to Python: a Python env can't hold JavaScript-wrapped
   objects, and these bindings are already safe to hand to tenant code,
   which is what the JavaScript tenant does.
3. **Python Durable Objects are internal objects.** Every class of a
   Python app runs in plain-DO mode with no node of its own (as
   `"node": false`): bundle.Parse forces it and refuses tags, `agent`,
   `ttl` and `public` on such a class. The per-object wrapper (identity,
   `ctx.tailnet`, egress MAC, kill gate, `ensure`) is JavaScript, and a
   facet's Worker Loader main module would have to import Python. Create
   admission still holds: it is enforced by hostd, and a raw namespace
   binding reaching a new name mints nothing (docs/create-admission.md:
   "bypassing the guard loses the ability to mint nodes"). There is no
   node to kill per object.
4. **No `ctx.tailnet` and no Python SDK of security-relevant code.** The
   caller's identity is `request.js_object.cf.tailscale.caller`, already
   verified by the front. The `python` template ships `tailworks.py` with
   `get_caller(request)` returning `getCaller`'s fields as a dict. A second
   implementation of the wrapper in Python (option (b) of #292) is
   rejected: two copies of the seal check and the guards to keep in step.
5. **Pinned Pyodide bundles, fetched once per host, read-only to
   workerd.** `internal/pyodide` lists every bundle the pinned workerd may
   ask for, with its size and SHA-256. Before the first Python instance,
   hostd fetches the missing ones into its bundle directory
   (`-pyodide-dir`, default `<data-dir>/pyodide`; `tw dev` uses the
   per-user cache), checks each digest before renaming it into place, and
   starts workerd with `--pyodide-bundle-disk-cache-dir` pointing at it. The
   sandbox profile gains that one directory read-only (Seatbelt
   `file-read*` subpath; Landlock `ReadOnly`), nothing else. workerd never
   downloads anything. An offline host is seeded by copying the files in.
   A fetch or digest failure is `python_unavailable`.
   `TestBundlesMatchWorkerd` fails when a workerd bump drops a pinned
   bundle's version or date.
6. **No bundler: `tw deploy` packs the project as pywrangler does.** The
   files under `main`'s directory become modules named relative to it
   (`.py` as Python modules, the rest as data the code reads from its
   filesystem, `Path(__file__).parent / "page.html"`), and
   `python_modules/` (next to `main`, else at the project root) ships under
   `python_modules/`. `tw` doesn't run uv or pywrangler and never installs a
   package; it warns when `pyproject.toml` or `requirements.txt` declares
   packages and nothing is vendored, and refuses a Worker that imports
   `workers` at a compatibility date that needs the vendored SDK, with the
   three fixes.
7. **Unsupported cases are refused by name**, at parse (so by `tw dev`,
   `tw deploy` and hostd alike): cron triggers, Queues, Workflows, static
   assets and containers for a Python app, and `"tier": "microvm"`. A
   Python app that resolves to the microVM tier from its tenant's default
   is refused by hostd with `tier_unavailable`, never run in the process
   tier instead (ADR-032 D5b). `--wrangler` / `--from-wrangler-out` are
   refused for Python. esbuild's "no loader" message no longer appears.
   (Amended by decision 8: crons, Queues and assets are no longer refused;
   by decision 9: the microVM tier is no longer refused.)
8. **Cron triggers, Queues and static assets go through the front.**
   hostd delivers a cron run and a queue batch to the tenant's default
   export, which for a Python app is the front, so the wrapper still
   handles the run's controller and the batch's create ticket. The front's
   `scheduled()` calls `tenant-py`'s through workerd's `Fetcher.scheduled()`
   and carries back `noRetry`; its `queue()` calls `Fetcher.queue()` with
   the batch's messages and replays the result (explicit acks, per-message
   retries, `ackAll`, `retryAll`) onto the real batch, then throws if the
   Python handler did. Both need `service_binding_extra_handlers` on the
   front: generated code, so the platform sets the flag, as it does on its
   own worker. `tenant-py` gets the queue producer bindings. The assets
   router (ADR-050) wraps the front's default export unchanged, and
   `tenant-py` gets `env.<assets.binding>`, the router without props, as a
   JavaScript tenant does.
9. **The microVM guest image carries the pinned bundles.** The pins move
   to `internal/pyodide/bundles.txt`, read by the Go package and by
   `guest/build-image.sh`, which fetches each one (sha256-checked, like
   every guest input) into `/usr/share/tailworks/pyodide` on the image's
   read-only root, and lists them in `manifest.json`. On the microVM tier
   hostd fetches nothing and passes the guest workerd
   `--pyodide-bundle-disk-cache-dir /usr/share/tailworks/pyodide` with the
   app's load (`WorkerdArgs`), so warm-pool VMs and snapshot restores, which
   start workerd before an app is assigned, are unchanged. The image grows
   by the bundles (about 53 MB) for every microVM host, Python or not: a
   second, optional disk would save that, at the cost of a new drive on
   both VM backends; not worth it at this size. Measured on the vz backend:
   a Python Worker's VM is active about 4 s after the deploy, and its first
   request takes about 50 ms; workerd with one Python Worker and one object
   is about 250 MiB, inside the default 512 MiB guest.
10. **RPC into a Python Worker goes through the front too** (#430). A
    service binding's RPC call reaches the front's `__tw_binding`
    dispatcher, which unseals the caller and refuses handler names as for
    a JavaScript Worker, then calls `TailworksRpc.call()` in `tenant-py`
    over a second front-only binding (`__TW_PY_RPC`) with the entrypoint,
    the method, the arguments and the call's verified `cf` (no create
    ticket). `tenant-py`'s main module is generated (`_tw_main.py`): it
    re-exports the app's main module, so workerd finds the same classes
    and handlers, and adds `TailworksRpc`, which finds the entrypoint class
    and a public method on it, sets `ctx.cf` so `get_caller(self.ctx)`
    names the caller, and runs the method. A caller can't name
    `TailworksRpc` as an entrypoint. workerd's own RPC into a Python
    `WorkerEntrypoint` would need a binding per entrypoint, and no
    per-call value reaches the callee's `ctx` across it (props are fixed
    in the config), so the caller couldn't be named.

## Consequences

- Cloudflare's Python examples that need no package run unmodified under
  `tw dev` and deployed (docs/COMPAT.md "Upstream examples: Python
  Workers"): a fetch handler, KV, D1, Durable Objects with RPC and storage,
  a hibernatable WebSocket chatroom, HTMLRewriter over an internet fetch.
- Each Python request takes one extra in-process service-binding hop
  (front → `tenant-py`); cold start is Pyodide's (about a second on a
  laptop). No memory snapshots are written: the sandbox gives workerd no
  writable place outside its instance for them.
- A host's first Python deployment downloads up to three bundles (about
  53 MB together) once.
- Python Durable Objects can't have their own tailnet identity, egress or
  public routes until the per-object wrapper can wrap them; that is the
  main follow-up, with Workflows.

## Alternatives considered

- **A Python runtime shim** reimplementing the wrapper in Python
  (#292 option (b)): two implementations of the seal check, the guards
  and the kill gate. Rejected.
- **Python Workers with no wrapper at all** (#292 option (c)): the caller
  seal would not be checked, so a Python Worker could not tell a verified
  caller from a forged `cf` blob. Rejected; (a) costs one hop.
- **Let workerd download Pyodide**: needs network (and a writable cache)
  in the sandbox. Rejected.
- **Ship the bundles in the release tarball**: about 53 MB more for every
  install, most of which never run Python. Fetch-on-first-use with a
  pinned digest gives the same integrity; the offline path is a copy.
