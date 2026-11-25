# arm64-sandbox-source (issue #494, part)

Code side of #494: `registry.Alias.Source` (digest-pinned), `pinOne` fills a
missing `tw.local` alias target into `public/` from it on NotFound when mgmt
mirrors, `imagebuild.aliasSource` returns it as `docker-image://`, reworded
`-no-mirror` error, `-image-aliases` validation and help, docs (errors.md
`image_unavailable`/`mirror_disabled`, containers.md Platforms + Public bases,
INSTALL.md, guest/images/README.md), the publish workflow
(`.github/workflows/publish-sandbox-images.yml`), and the `test-images`
digest check (`TestDefaultAliasSourcesResolve`).

The digests in `internal/registry/aliassource.go` (`sandboxSourceDigests`)
are PLACEHOLDERS (empty): DefaultAliases have no Source, so behaviour is
today's until the maintainer runs the workflow and bumps them in a follow-up PR.

## Tests

After merging origin/master (2026-11-25):

- `make lint`: pass (includes api-check, changes-check, docs lint).
- `make test` (offloaded to mbp with `mbp-test.sh test`): pass.
- `go test ./internal/registry/ ./internal/imagebuild/ ./internal/mgmtd/`: pass
  (new: TestPinAliasFillsFromSource, TestPinAliasSourceDigestVerified,
  TestDefaultAliasSources, TestAliasContextFromSource).
- `TW_TEST_IMAGE_SOURCES=1 go test -run TestDefaultAliasSources ./internal/registry/`:
  TestDefaultAliasSourcesResolve skips (every digest is a placeholder).
- Skipped: full `make test-images` (needs crane + docker buildx for both
  arches; the new check in it skips while the digests are placeholders).
  The publish workflow was not run (maintainer's call), and actionlint is
  not installed here.

## Left for the maintainer

1. Run `.github/workflows/publish-sandbox-images.yml` (`gh workflow run
   publish-sandbox-images.yml --ref master`).
2. Check the ghcr.io package `cloudflare-sandbox` is public.
3. Copy the three digests from the run summary into `sandboxSourceDigests`
   (internal/registry/aliassource.go) in a follow-up PR; run `make test-images`.
   Then drop the "not published yet" prerequisite text from docs/containers.md
   (Platforms, Public bases), docs/INSTALL.md and docs/errors.md#image_unavailable.
