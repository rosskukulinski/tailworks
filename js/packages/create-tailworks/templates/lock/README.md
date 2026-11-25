# lock

A lease-based lock and leader-election service. Each lock name is one
Durable Object (and tailnet node); its holder is the caller's tailnet
identity, and every grant comes with a **fencing token** that only goes
up.

```
POST /locks/<name>/acquire  {"ttlMs": 10000}  → 200 {token, expiresAt, holder, …} | 409 lock_held
POST /locks/<name>/renew    {"token": …}      → 200 {expiresAt, …} | 409 lock_lost
POST /locks/<name>/release  {"token": …}      → 200 | 409 lock_lost
GET  /locks/<name>                            → {held, holder, expiresAt, lastToken, …}
```

## Run it

```sh
tw dev
curl -X POST -d '{"ttlMs": 5000}' http://127.0.0.1:8787/locks/nightly/acquire
curl http://127.0.0.1:8787/locks/nightly
tw deploy
```

## What to change

- Use it from your app: copy `src/client.ts` into the app that takes the
  lock (it needs only `fetch`) and wrap the job in
  `lockClient({ url: "http://<lock workload>" }).withLock(name, fn)`.
  Pass the lease's `token` to the system you write to, and have it refuse
  older tokens.
- `wrangler.jsonc` `vars`: `DEFAULT_TTL_MS` and `MAX_TTL_MS`. Keep the
  maximum at 10 s or less: a lease must end before another host can take
  the workload over.
- The policy: grant the lock's clients access to this workload's tag.

What tailworks guarantees when a host fails, and the patterns (mutex, fencing,
leader election):
[locks.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/locks.md).
