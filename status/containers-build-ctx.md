# containers-build-ctx (#496)

Containers: wrangler build keys honoured, dropped keys warned, and
doctor/up/deploy say whether containers can run.

- `internal/container`: `Class.Build` (`json:"-"`, developer-machine only)
  from `image_build_context`/`image_vars` and
  `images.<name>.build_context`/`build_vars`; tailworks.jsonc may not set
  them. `HonouredKeys` lists the entry keys tailworks reads.
- `internal/imagebuild`: `Plan` uses the build context (resolved against
  the app dir, must be a directory); `Build` appends sorted `--build-arg`s,
  echoed as `k=…`.
- `cmd/tw/containerbuild.go`: rebase of the build paths (and of named
  images' `dockerfile`, which were objects the rebase skipped),
  `containerKeyWarnings` (one line per dropped key; none for `name` or
  `scheduling_policy` default/durable_object), the `docker buildx version`
  check right after `Plan` returns jobs.
- hostd: `Config.ContainersOff` set once by hostdrun (the `containersUsable`
  error, or `-no-containers`); `StatusView.ContainersOff`
  (`hostd/containersoff.go`).
- `cmd/tw/doctor_containers.go`: doctor `containers` row (ok/skip, never
  fail; local `mkfs.erofs` probe only when hostd is down), tw up tier-line
  note, `no_host` fix listing live hosts with tiers (plus the local hostd's
  reason when this machine is one of them).

Not done: remote hosts' reasons in `no_host`. mgmt's host record has no
field for it; carrying it needs a HostRegistration field and a store
column (API + migration), out of scope here. `tw doctor` on the host says
why.

## Tests

See the PR body.

- `make lint`: ok (after merging origin/master).
- `make test` (mbp): all ok except hostd, which panicked in a test helper
  (`hostd/connect_test.go:42` serveWhoisTCP: `who.Node` nil, untouched
  here); `go test ./hostd -count=1` locally: ok.
- `make e2e-wrangler`: ok.
- Not run: `make e2e-container` (needs Linux + KVM), `make e2e-compat`
  (no container project runs on this Mac's host; the change is in the
  build and diagnostics, not the runtime).
