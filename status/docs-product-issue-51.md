# docs-product-issue-51: docs as a product (issue #51)

Items 1, 2, 4, 5 and 6 of #51. Item 3 (generated CLI reference) is #46's.
#48 (caller grants, troubleshooting, glossary, splitting INSTALL) is PR #53.

## Done

- **Recipes** (`docs/recipes.md`): internet API through a tailnet gateway,
  an LLM through an OpenAI-compatible gateway, a Durable Object per user
  authorized by `getCaller`/`principalKey` and a grant capability, a public
  GitHub webhook with HMAC verification, cron writing to KV. Each app's
  three files were run under `twd dev` (hermetic, `--port 0`/fixed ports,
  temp state dirs): the gateway recipes end in `peer_not_visible` there
  (no gateway node in dev; a direct internet `fetch()` is refused the same
  way), the others ran end to end (403 for another user's notes, 401 for a
  bad signature, `/__scheduled` then the KV keys).
- **Template READMEs**, one per template (what it shows, how to run, what
  to change). `twd init` copies them (`os.CopyFS`); schema and esbundle
  tests are unaffected. The agent, lock and ai-agent commands were run
  under `twd dev`.
- **Limits** (`docs/limits.md`): every size, count, rate and timeout with
  its `file:line`, and a "Limits that don't exist" table (no CPU time,
  tailnet request duration, body size or rate limit).
- **Changelog**: `catalog.Changelog` renders `CHANGELOG.md` from the
  embedded release catalog (`go run ./internal/catalog/changes changelog`);
  `release-notes.sh --finalize` re-renders it; `TestChangelogUpToDate`
  fails when it is stale. Versions and compatibility dates: a short
  section at the end of `docs/upgrading.md` linking RELEASING.md.
- **Security**: `SECURITY.md` (GitHub private vulnerability reporting) and
  `docs/security-model.md`. THREAT-MODEL.md no longer cites a local path.
- **README in `twd docs`**: a root package (`rootdocs.go`, package `twd`)
  embeds README.md, CHANGELOG.md and SECURITY.md; `docs` lists them as
  `../README.md` etc. `twd docs search quick start` now ranks the README's
  quick start first; search URLs are built from the repo path.

## Found, not fixed

- Private vulnerability reporting is **off** on the GitHub repo
  (`gh api repos/rosskukulinski/twd/private-vulnerability-reporting` →
  `enabled: false`). SECURITY.md relies on it: the owner must enable it
  (Settings › Code security).
- `cmd/twd/wranglercfg.go` warns that `queues` is "no such binding on twd",
  and docs/wrangler.md lists it so, though queues are supported.
- `docs/queues.md` omits the message, batch, delay and queue-size limits;
  `docs/kv.md` omits the 30 s minimum `cacheTtl` and calls the 1 GiB
  namespace cap "live data" (the code caps the journal). limits.md has
  the code's values.
- `twd-jsonc.md` described `egress.internet` as opening the internet to the
  workload; it applies to containers only (row fixed).
- The refund-approval template's `/refund` reply hard-codes
  `twd approve refunds <id>`, which is wrong after `twd init` renames the
  workload (its README says so).
- README.md doesn't link CHANGELOG.md yet: README is being edited by PR
  #53, so the link is in docs/README.md and llms.txt; add a README line
  after #53 merges.
