# static-site

A static site served from a tailworks workload, with a Worker next to it.
The files in `public/` are served as they are (wrangler's
[`assets`](https://github.com/rosskukulinski/tailworks/blob/master/docs/wrangler.md#assets));
the Worker (`src/index.ts`) answers an API and checks the caller before it
serves the pages under `/team/`.

## Run it

```sh
tw dev                                     # http://127.0.0.1:8787, no tailnet needed
open http://127.0.0.1:8787/                # the page greets you by your (dev) tailnet login
curl -i http://127.0.0.1:8787/style.css    # a file: Content-Type, ETag
curl http://127.0.0.1:8787/api/whoami      # the Worker: {"login":"…","node":"…","tags":[]}
curl -i http://127.0.0.1:8787/missing      # no file: 404 with public/404.html
tw deploy                                  # then open the URL it prints, from any device on your tailnet
```

Edit a file in `public/` while `tw dev` runs and reload the page: the change
is there. `npm test` runs `test/app.test.js` against `tw dev` as a person and
as a tagged node.

## How requests are routed

`wrangler.jsonc` `assets` sets it up:

- A request for a file in `public/` gets the file. `/team` and `/about.html`
  style paths follow `html_handling` (default `auto-trailing-slash`:
  `/team` redirects to `/team/`, which serves `team/index.html`).
- Paths in `run_worker_first` (`/api/*`, `/team/*`) run the Worker first.
  It can answer itself, or serve a file with `env.ASSETS.fetch(req)` once it
  has checked `getCaller(req)`.
- Any other path with no file runs the Worker. Here it calls
  `env.ASSETS.fetch(req)`, which applies `not_found_handling`
  (`404-page`: the nearest `404.html`, with status 404).

`public/_redirects` and `public/_headers` work as on Cloudflare:
`/home` redirects to `/`, and every file gets `X-Content-Type-Options`
and `Referrer-Policy`. Neither file is served itself, and `_headers`
doesn't touch the Worker's own responses.

For a single-page app, set `"not_found_handling": "single-page-application"`:
a browser navigation to a path with no file gets `index.html`.

## What to change

- `public/`: your site. Point `assets.directory` at a build output (for
  example `./dist` from Vite) to serve a built client; build it before
  `tw deploy`.
- `src/index.ts`: your API and access checks. `getCaller(req)` tells you who
  is calling (node, user, tags, capabilities); there are no tokens to check.
- `tailworks.jsonc` `tailnet.tags`: the workload node's tag, named after your
  app by `tw init`.

Everyone who can reach the workload can read the files that don't go through
the Worker first. Put pages that need an identity check under a
`run_worker_first` path, as `/team/` is here.
