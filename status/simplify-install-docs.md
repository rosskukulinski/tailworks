Last update: 2026-11-24 PT. INSTALL.md back to installing, one troubleshooting page, docs/hosts.md, policy snippet drift test (#482, #484).

# simplify-install-docs

Issues #482 and #484 from the interface-simplification audit (#434).

## What changed

**#484, docs split.**

- `docs/INSTALL.md` (1207 → 922 lines) keeps prerequisites, npm vs the
  tarball, verify a release, PATH, the OAuth client's operator paragraphs
  (apply semantics, what `tw up` checks, automation, rotation), `tw link`,
  transport, Headscale, the service, upgrade, `~/.tailworks`, backup,
  restore, uninstall, Linux and the microVM tier. The OAuth section links
  the README quick start for the policy snippet instead of carrying a copy,
  and its per-scope bullets became a 4-row scope table.
- New `docs/hosts.md` ("More than one machine"): one process vs
  `tw host add`, placement, moving a workload, sizing. `--local-dev` stays
  in INSTALL.md.
- Tuning (`--warm-keys`, `--pool-nodes`, `--port-mapping`) moved to
  `docs/concepts.md`, next to Pool node and Warm keys.
- Isolation: a short summary in INSTALL.md (keeps "dev-grade isolation"
  and "don't run code you don't trust") that links
  `security-model.md#isolation-tiers`; the Landlock/doctor sentences and
  `host_sockets` moved into security-model.md.
- Storage quotas and retention: dropped from INSTALL.md;
  `limits.md#tenant-storage` and its logs/audit rows already had all of it.
- `docs/troubleshooting.md` is now "Troubleshooting" with four sections:
  `tw up` fails, my first deploy doesn't answer (the old page, headings one
  level down so anchors are unchanged), `tw link` and linked machines,
  `tw doctor` warns. Coded bullets are a symptom plus an errors.md link;
  the INSTALL-only detail went into errors.md first (`bootstrap_failed`:
  remove the half-joined node; `device_delete_unavailable`: revoke the old
  client; `tailnet_required`: the cause; `host_unavailable`: the linked
  machine case). Uncoded bullets moved verbatim.
- "Try it without a tailnet" moved to `docs/dev-debugging.md` (same
  anchor).
- Stubs kept in INSTALL.md at `#the-oauth-client` (still a real section),
  `#isolation`, `#try-it-without-a-tailnet` and `#troubleshooting`; they
  name `tw docs show …` since the tarball ships INSTALL.md alone.
- Links updated: docs/README.md, llms.txt, concepts.md, footprint.md,
  install-testing.md, security-model.md, tailworks-jsonc.md,
  alternatives.md. A local anchor check over every tracked .md found no
  new broken links (the pre-existing false positives are unchanged).

**#482, policy snippets and skills.**

- `cmd/tw/docs_policy_test.go` is a table: README (operatorSnippet, plus
  the `tag:tw` line), INSTALL.md (no copy; a copy would be checked),
  docs/headscale.md (`policy.Headscale(baseSnippet())`, a `hujson` block)
  and docs/public-ingress.md (`policy.PublicSnippet()`). Blocks are
  matched by their tagOwners key set and compared on tagOwners, grants and
  nodeAttrs; each doc's count is pinned. Checked that a changed port in
  headscale.md and a changed attr in public-ingress.md fail it.
- Skills `tw`, `tw-policy` and `tw-deploy`, and docs/agents.md, no longer
  say tailworks never writes the policy: it writes it only when the user
  passes `--apply-policy` or runs `tw policy --apply` and confirms; agents
  mustn't pass those flags, or `--yes` with them, for the user.

## Not done

- #482's last item, letting `internal/policy/headscale.go` keep
  `autogroup:member` grant sources on the Headscale profile, is left out.
  It changes what `tw deploy --apply-policy` writes on a Headscale tailnet
  and needs `make e2e-headscale` (Docker), which this machine doesn't
  have. Filed as #552.
- #481 (the policy-write default on a terminal) and #483 (README quick
  start) belong to other branches; README.md is untouched.

## Tests

After merging origin/master (e879b14e), Go 1.27.4:

- `make test-docs`: ok.
- `go test -run 'DocPolicySnippets|OperatorSnippet' ./cmd/tw/`: ok; with a
  port changed in headscale.md and an attr in public-ingress.md it fails
  on both, as it should.
- `make changes-check`: ok (no upgrade-relevant paths, no fragment).
- `make lint`: ok.
- `make test`: everything passed except five timing tests under heavy load
  (`TestStartCloseBudget` start 4.2 s vs a 2 s budget, `TestTenantAdminScope`
  `no_host` heartbeat, `TestDevPython`, `TestPublicIngress`,
  `TestContainerPolyfill`); none touches code this branch changed, and each
  passes rerun alone with `-race`.
- A local Markdown anchor check over every tracked .md: no new broken
  links.
- Not run: `make e2e-headscale` (no Docker here); nothing else in the
  AGENTS.md table applies (docs, skills and one cmd/tw test file).
