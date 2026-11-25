Last update: 2026-11-17 PT. Issue #117: internet egress from a Worker gets its own error code; docs say Workers have no internet egress.

# fix-117-worker-egress

## What changed

- **New refusal code `internet_egress_unavailable` (403, netd).** In
  `resolveEgress` (netd/egress.go), a name that misses the netmap and is not
  a Tailscale Service name is checked by `tailnetShaped` (netd/internet.go).
  A tailnet-shaped name (no dot, under this node's or any visible peer's
  MagicDNS suffix, or any `*.ts.net` / `*.beta.tailscale.net` name) keeps
  `peer_not_visible` and its grants hint. Anything else gets
  `internet_egress_unavailable` with one line that links
  docs/recipes.md#call-an-internet-api. No reachability changed: every
  destination refused before is still refused; only the code and detail differ.
- IP literals are unchanged: a public IP is still `forbidden_destination`.
- Registered the code in `hostd/http.go` `errStatus`, the SDK catalog
  (`js/packages/tailnet/src/errors.ts`), `docs/errors.md`, and the
  `tw-debug` agent skill.
- Docs: README compat row no longer says "no internet, by default"; it says
  Workers have no internet egress and links the gateway recipe. COMPAT.md,
  recipes.md, wrangler.md, security-model.md, troubleshooting.md and the hello
  template README say the same. The `tailnet.egress.internet` schema
  description now says it is the containers' allowlist.

## No opt-in switch (left for a follow-up)

There's no Worker internet egress switch. Building one (exit node or
gateway, RFC §4.6) needs a dial path that leaves netd's netmap-only egress
guard: today every Worker dial goes through `tsnet.Server.Dial` to a
tailnet address, and the only internet path is the containers' sbxnet
allowlist. That is a design change (an ADR) rather than a fix, so this
branch documents that there is no switch. Follow-up: #161.

## Tests

- `go test ./netd/ -run 'TestTailnetShaped|TestChain'`: PASS. New
  `TestTailnetShaped` (unit) and `TestChain/InternetEgressUnavailable`
  (api.github.com and example.com: 403, `tw-error` header, detail links the
  recipe and doesn't mention grants, doc_url anchor). `TestChain/PeerNotVisible`
  gained an unknown `*.ttest.ts.net` FQDN case that stays `peer_not_visible`.
  `SelfSSRFRefused/public-name-no-host-dns` and `RawProxyNumericHost`
  (`127.1`, `metadata.google.internal`) now expect the new code.
- `e2e/local/netd_binary_test.go` and `e2e/real/real_test.go` expectations
  for example.com updated (not run: e2e-real needs credentials).
- `go test -tags e2e ./e2e/local/ -run Netd` (TestNetdBinary, example.com now
  `internet_egress_unavailable` through the real binary): PASS.
- `make lint`: PASS.
- `make test`: PASS apart from three tests. `hostd/gen TestRuntimeEmbedFresh`
  needed `make gen-js` after the SDK catalog change (committed; then PASS).
  `netd TestEgressWaitsForNewPeerPath` (#139) and `mgmt TestWarmKeyExpiry`
  (#137) are known flakes and passed on rerun.
- `make templates-typecheck test-sdk`: PASS.
- Not run: `e2e-real` (needs credentials and a real tailnet).
