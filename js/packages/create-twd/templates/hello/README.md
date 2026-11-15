# hello

The smallest twd app: a Worker that answers with its **verified tailnet
caller** (`getCaller`) and, optionally, fetches a private service on your
tailnet from its own node.

## Run it

```sh
twd dev                                # http://127.0.0.1:8787, no tailnet needed
curl http://127.0.0.1:8787/            # {"hello":"<you>","caller":{…},"upstream":null}
twd deploy                             # then curl the URL it prints, from any device on your tailnet
```

## What to change

- `src/index.ts`: your handler. `getCaller(req)` tells you who is calling
  (node, user, tags, capabilities); there are no tokens to check.
- `wrangler.jsonc` `vars.UPSTREAM`: a service on your tailnet to fetch, e.g.
  `http://inventory-db/items`. Outbound `fetch()` reaches tailnet peers only;
  a missing grant answers `peer_not_visible` (in the `twd-error` header).
- `twd.jsonc` `tailnet.connects`: the tags you call (the template says
  `tag:db`). `twd deploy` checks that your tailnet policy grants them.
- `twd.jsonc` `tailnet.tags`: the workload node's tag, named after your app
  by `twd init`.

More: the [recipes](https://github.com/rosskukulinski/twd/blob/master/docs/recipes.md)
and [twd.jsonc reference](https://github.com/rosskukulinski/twd/blob/master/docs/twd-jsonc.md).
