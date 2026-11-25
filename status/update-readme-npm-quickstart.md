# noble-pug: README quick start uses `npm create tailworks`

`create-tailworks` and the `@tailworks/*` packages are on npm (0.3.0), so
the README quick start now starts from npm:

- "Only want to see the code first?" is `npm create tailworks@latest hello`
  then `npm install && npx tsc --noEmit`. It needs only Node.js ≥ 20, with
  no tailworks install. Running it (`npm run dev`, i.e. `tw dev`) still
  needs step 1.
- Step 4 keeps `tw init` (the release needs no Node.js, per the
  prerequisites table from #74) and adds an optional `npm install` for
  editor types. It mentions that `npm create tailworks … --template agent`
  does the same, since the bin runs `tw init` when `tw` is on PATH.
- The prerequisites table gets a row: scaffolding with `npm create
  tailworks` needs only Node.js ≥ 20.
- Step 1 (get.sh tarball) is unchanged: the `tw` CLI itself is not on npm.

## Tests

- Fresh temp dir, no `tw` on PATH: `npm create tailworks@latest hello`
  then `npm install && npx tsc --noEmit`: pass.
- `make changes-check`: ok (no fragment needed).
- `make lint test`: pass (before and after merging origin/master).

Follow-up: publishing the `tw` CLI itself to npm is #240.
