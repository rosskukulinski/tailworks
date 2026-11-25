# hello

The smallest tailworks app: a Worker that answers with its **verified tailnet
caller** (`getCaller`) and, optionally, fetches a private service on your
tailnet from its own node.

## Run it

```sh
tw dev                                # http://127.0.0.1:8787, no tailnet needed
curl http://127.0.0.1:8787/            # {"hello":"<you>","caller":{…},"upstream":null}
tw deploy                             # then curl the URL it prints, from any device on your tailnet
```

## What to change

- `src/index.ts`: your handler. `getCaller(req)` tells you who is calling
  (node, user, tags, capabilities); there are no tokens to check.
- `wrangler.jsonc` `vars.UPSTREAM`: a service on your tailnet to fetch, e.g.
  `http://inventory-db/items`. A missing grant answers `peer_not_visible`
  (in the `tw-error` header). An internet URL works too: it leaves from the
  host's network, unless `tailnet.egress.internet` in tailworks.jsonc turns
  it off ([call an internet API](https://github.com/rosskukulinski/tailworks/blob/master/docs/recipes.md#call-an-internet-api)).
- `tailworks.jsonc` `tailnet.connects`: the tags you call, e.g. `["tag:db"]`
  (the tag must already exist in your policy's `tagOwners`). `tw deploy`
  checks that your tailnet policy grants them.
- `tailworks.jsonc` `tailnet.tags`: the workload node's tag, named after your app
  by `tw init`.

More: the [recipes](https://github.com/rosskukulinski/tailworks/blob/master/docs/recipes.md)
and [tailworks.jsonc reference](https://github.com/rosskukulinski/tailworks/blob/master/docs/tailworks-jsonc.md).
