# deflake-e2e-public-slow-dial

The v0.5.0 release run (45346852696) failed its `tailnet` job in
`make e2e-public`. Every check passed, then the `/slow` stream request
failed with `dial tcp [<IPv6>]:443: connect: network is unreachable`: the
lookup returned only the Funnel edge's IPv6 address, and GitHub's hosted
runners have no IPv6. That request was the only public one not made through
`fetch`, which retries a failure to resolve or connect for a minute. It now
retries the same way.

A rerun of the job failed the same way, so the test's dialer changed too:

- It reports every address's error. It reported only the last one's (the
  IPv6 address), which hid what happened to the IPv4 attempt.
- It keeps the addresses that connected and tries them after a lookup's
  own, so a lookup that answers with only the AAAA still reaches the edge
  over IPv4.

A third run failed earlier, at `public_existing_object: 0 map[]`: a request
that does go through `fetch` got no response for a minute, and the detail
didn't say why. A request that fails now carries its error in the detail
(`fetch_error`).

## Tests

- `make lint`: ok (macOS arm64, Go 1.27.4).
- `go vet -tags e2epublic ./e2e/public/`: ok.
- The dialer, by hand in a scratch test (not committed): three GETs of a public site through `publicClient` answer 200 and keep the address; an unreachable address reports its error.
- Not run: `make e2e-public` (needs the test tailnet's credentials and
  Funnel), and `make test` (the only change is in a file behind the
  `e2epublic` build tag, which `make test` doesn't build).
