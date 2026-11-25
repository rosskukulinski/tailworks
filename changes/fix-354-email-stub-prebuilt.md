---
kind: fix
upgrade: []
---
A pre-built Worker (a Cloudflare Vite plugin build, or any `no_bundle`
config) that imports `cloudflare:email` now starts (#354). Before, workerd
failed with `No such module "cloudflare-internal:email"`, because only the
built-in bundler stubbed the import. The generated workerd config now
provides the stub for every Worker, so importing `cloudflare:email` works
however the Worker was bundled, and constructing an `EmailMessage` still
throws "cloudflare:email is not supported on tailworks". Hosts pick this
up when a workload is next deployed or restarted.
