Last update: 2026-11-17 PT. npm-publish.yml publishes with npm trusted publishing (OIDC) instead of the NPM_TOKEN secret.

# npm-trusted-publishing

- `.github/workflows/npm-publish.yml`: removed the "Check for NPM_TOKEN"
  step, every `if: steps.token.outputs.skip != '1'`, `NODE_AUTH_TOKEN` and
  `NPM_CONFIG_PROVENANCE` (provenance is automatic with trusted publishing
  from a public repo). Added a step after setup-node that installs
  `npm@11.21.2` (trusted publishing needs npm >= 11.5.1; Node 22 bundles
  npm 10). Kept `permissions: id-token: write` and setup-node's
  `registry-url` (npm's documented trusted-publishing workflow keeps it).
  Actions stay pinned by SHA. Header comment rewritten.
- `scripts/npm-publish.sh`: auth comment describes OIDC in CI, `npm login`
  by hand.
- `docs/RELEASING.md` step 8: trusted publishing replaces the NPM_TOKEN
  setup. Per package: npmjs.com Trusted Publisher (user `rosskukulinski`,
  repo `tailworks`, workflow `npm-publish.yml`, no environment) or
  `npm trust github <pkg> --file npm-publish.yml --repo rosskukulinski/tailworks --allow-publish`
  (npm >= 11.15); configure just before a release (2-day expiry); after the
  first OIDC publish delete `NPM_TOKEN` and set Publishing access to
  "Require two-factor authentication and disallow tokens". Staged
  publishing (`--allow-stage-publish`, `npm stage approve`) as an option.
- release.yml: no `NPM_TOKEN` reference; its dry-run gate needs no auth and
  is unchanged.

Not done here (maintainer, on npmjs.com): configuring the trusted
publishers, deleting the `NPM_TOKEN` secret, changing publishing access.

## Tests

- `actionlint` v1.7.7 (`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7`)
  on npm-publish.yml and release.yml: no findings.
- `sh scripts/npm-publish.sh 0.2.0 --dry-run`: versions check and pack of
  `@tailworks/tailnet` pass, then npm 11 refuses "You cannot publish over
  the previously published versions: 0.2.0" — expected now that 0.2.0 is
  on npm (a dry run doesn't skip published packages); unrelated to this
  change, and release.yml runs the dry run before the tag is published.
- The workflow itself can't run without a new release.
- `make lint test`, `make changes-check`: see the PR.
