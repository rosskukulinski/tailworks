---
kind: feature
upgrade: []
---
The tw CLI is on npm (issue #240): `npx tailworks init hello && cd hello && npx tailworks dev`, or `npm install -g tailworks` for `tw` (and `tailworks`) on your PATH. The `tailworks` package is a small shim. npm installs it with one platform package, `@tailworks/cli-darwin-arm64`, `@tailworks/cli-linux-arm64` or `@tailworks/cli-linux-amd64`. Each is made from the signed release tarball and published with provenance: the same `tw`, daemons, workerd and bundler, without the microVM guest. State stays in `~/.tailworks`.

On an npm install, `tw upgrade` refuses with the new `npm_install` error and says to upgrade with npm (`tw down && npm install -g tailworks@latest && tw up`). `tw version --check` prints that command, and `tw-hostd -self-upgrade` refuses. `tw up` from `npx` runs without a login service, because npm can clear npx's cache, and `tw up --service` from there refuses. `tw doctor` has a new "installed by" check (install.sh, npm or npx), and on Linux its microVM check says the npm package has no guest. The README quick start now starts with npm. `scripts/get.sh` remains the way to install without Node.js, and the only one with the microVM tier.
