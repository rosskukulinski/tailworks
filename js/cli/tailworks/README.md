# tailworks

The [tailworks](https://github.com/rosskukulinski/tailworks) CLI, `tw`: run
Cloudflare-style Workers and Durable Objects on your own machines, on your
Tailscale tailnet.

```sh
npx tailworks init hello && cd hello && npx tailworks dev
# or
npm install -g tailworks && tw init hello && cd hello && tw dev
```

macOS 14+ on Apple silicon, or Linux arm64/amd64 with glibc ≥ 2.35. npm
installs one platform package (`@tailworks/cli-darwin-arm64`,
`@tailworks/cli-linux-arm64` or `@tailworks/cli-linux-amd64`) with `tw`,
its daemons, workerd and the bundler. They're the release tarball's
binaries, without the Linux microVM guest. This package only finds that
platform package and runs its `tw`: it has no install script and downloads
nothing.

- Upgrade: `tw down && npm install -g tailworks@latest && tw up`.
  `tw upgrade` is for tarball installs and refuses here.
- `tw up` from `npx` runs in the background without a login service,
  because npm can clear npx's cache at any time. Install it globally for
  the service.
- The microVM tier, and installs without Node.js, need the release tarball:
  [docs/INSTALL.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/INSTALL.md).
