---
kind: feature
upgrade: [feature-string]
---
wrangler `assets` (static files) now work. `tw deploy` and `tw dev` read
`assets.directory` and ship its files with the Worker; the host serves them
with Cloudflare's semantics: `Content-Type`, `ETag` and `304 Not Modified`,
`html_handling`, `not_found_handling` (`single-page-application`,
`404-page`, `none`), `run_worker_first` (so the Worker can check
`getCaller(req)` before a file is served) and `env.ASSETS.fetch()`. `tw dev`
reloads when a file changes. `.assetsignore` is honoured, and `_redirects`
and `_headers` work with Cloudflare's syntax (`tw` rejects a bad line
before uploading). A config with `assets` and no `main` deploys as a site
with no Worker. The files
count against the 64 MiB bundle. `tw types` types the binding as `Fetcher`.
The `assets: not supported` warning is gone. Try it with
`tw init my-site --template static-site` (docs/wrangler.md#assets).

Operator action: upgrade hosts before deploying an app with `assets`. A
bundle with `assets` needs the new `assets` host feature, so mgmt places it
only on upgraded hosts.
