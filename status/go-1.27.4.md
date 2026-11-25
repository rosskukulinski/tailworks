# go-1.27.4 (issue #146)

Moves the Go toolchain from go1.27.3 to go1.27.4 for GO-2026-6859 (the
`net/http` `MaxBytesReader` header race), and makes `make vulncheck`
blocking in `make lint`.

## Changes

- `go.mod` and `tools/go.mod`: `go 1.27.4`. CI and release workflows read
  the version from `go.mod` (`go-version-file`), so they follow.
- `Makefile`: the header comment names 1.27.4; `make lint` runs
  `make vulncheck` without the leading `-`, and the TODO(#146) is gone.
- `docs/RELEASING.md`: govulncheck is now described as blocking.
- `go.mod` tailscale.com comment: dropped the "v1.104.6+ need go1.27.4"
  note. The pin stays: the un-pin condition (the first tag cut from main
  after 5030108, v1.106.0) is not met; the newest tag is v1.104.7.
- `changes/go-1.27.4.md`: `security` fragment.

No Dockerfiles, guest builds, install scripts or install docs pin a Go
patch version, so nothing else changed. `install.sh` / `get.sh` are
untouched.

## Operator note

With `GOTOOLCHAIN=local` (the Makefile default) a machine still on
go1.27.3 now fails with `go.mod requires go >= 1.27.4`. Upgrade the
installed Go (for example `brew upgrade go`).

## Tests (darwin/arm64, go1.27.4)

- `make vulncheck`: no vulnerabilities reachable (0 symbol results).
- `make lint`: pass (now including the blocking vulncheck).
- `make test`: pass except `TestWarmKeyExpiry` (known flaky, #137);
  reran alone, pass.
- `make e2e-placement`: pass.
- `make e2e-upgrade`: pass.
