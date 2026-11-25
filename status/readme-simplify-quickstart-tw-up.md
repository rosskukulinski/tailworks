Last update: 2026-11-23. README rewrite and a flag-free first `tw up`.

## What changed

- **README** rewritten as a short quick start: install → policy → OAuth
  client (Settings › Trust credentials, tag `tag:tw`) → `tw up` → `tw init
  hello` / `tw policy` / `tw deploy` → `curl` + `tw logs`. The compatibility
  table is kept (compat-check needs it) but collapsed in `<details>`. The
  prerequisites table moved to `docs/INSTALL.md#prerequisites`; links to the
  old `#quick-start-deploy-your-first-durable-object` anchor now use
  `#quick-start`.
- **`tw up` defaults** (`cmd/tw/up_defaults.go`), on the default state dir
  only (no `--state-dir`, no `$TW_STATE_DIR`, so tests never pick up the
  operator's credential or install a service):
  - OAuth client file defaults to `~/.config/tailworks/oauth.env`
    (`$XDG_CONFIG_HOME` honoured); if missing and stdin is a terminal, it
    asks for the Client Secret (hidden) and saves it 0600. The client ID is
    derived from the secret, so the secret alone is enough.
  - `--service` defaults on for a new state dir where a user service
    manager exists; an existing state dir keeps whether it has a service, so
    `tw upgrade`'s `tw up --yes` restart doesn't change it.
- **No confirmation prompt** in `tw up`; `--yes` is accepted and does
  nothing.
- **Loose OAuth client file** (`tw up` only): tightened to 0600 if this user
  owns it, instead of `credential_file_insecure`. `tw tenant add`,
  `--api-key-file` and `--db-file` still refuse.
- **`hello` template**: `tailnet.connects` is now `[]` (was `["tag:db"]`,
  which most tailnets don't define; pasting the grant `tw policy` printed
  failed with `tag not found`). The `tw policy` snippet now says the dst tag
  must already be in tagOwners; `docs/errors.md#acl_denied` explains the
  `tag not found` error. `e2e/cliflow` patches `tag:db` into hello itself
  to keep testing the preflight refusal.
- **`tw deploy`** ends with `try it  curl <url>`, `watch  tw logs <app> -f`
  and a hint that callers need a grant.
- Console wording "Settings › OAuth clients" → "Settings › Trust
  credentials" in CLI messages and docs.

## Tests

- `make lint`: ok (api-check, compat-check, changes-check, vet, secrets).
- `go test ./cmd/tw ./docs ./internal/tsapi ./schema`: ok.
- `make test`: ok (exit 0, no FAIL lines).
- Not run: real-tailnet suites (`e2e-real` etc.); the prompt path of
  `tw up` was not exercised against a real tailnet.
