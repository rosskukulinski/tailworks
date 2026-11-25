Last update: 2026-11-21 PT. macOS Seatbelt reads are an allow list and TCP is IPv4-loopback only (#175, ledger H26 fixed).

# Seatbelt: read allow list and pinned TCP (#175)

Follows `status/fix-121-seatbelt.md`.

## What changed

- `hostd/seatbelt_strict.go` (`seatbeltProfile`):
  - `(deny file-read*)`, then reads allowed back for `/` (literal), the OS
    dirs in `seatbeltReadDirs` (`/System`, `/bin`, `/dev`, `/opt`,
    `/private/etc`, `/private/var/db/timezone`, `/private/var/select`,
    `/sbin`, `/usr`), the runtime binary, the Node prefix and own dirs.
    The deny list (home, hostd data/run dirs, credential files) still
    follows the OS allows, so it wins inside an allowed dir (a data dir
    under `/opt`), and own dirs are allowed after it. Only metadata
    (stat) of the root links `/etc`, `/tmp`, `/var` and of every allowed
    path's parents.
  - TCP rules use `tcp4`: `(remote tcp4 "localhost:<port>")`,
    `(local tcp4 "localhost:<port>")`.
- Tests: `hostd.TestSeatbeltStrictProbes` gains `read_tmp`,
  `read_user_temp_dir`, `list_user_temp_dir` and
  `tcp_connect_given_port_ipv6` (denied) and `read_etc`,
  `read_dev_urandom` (allowed); the node probe also loads ICU/time zones,
  TLS roots, crypto, zlib and reads `/etc/hosts`; probes run with the
  instance dir as cwd, as hostd does. `hostd.TestSeatbeltStrictProfileShape`
  checks the new ordering and that no `tcp "` (any-family) rule or
  `/Users`/`/private/tmp` read allow appears. `seatbeltLiveEscapes`
  (`hostd.TestMultiTenant`, live profile) gains `read_tmp`.
- Docs: `docs/security-model.md` (macOS column), `docs/node-tier.md`,
  `security/THREAT-MODEL.md` §1, §3, §7; ledger H26 fixed, C21 updated,
  run-log row.

## Findings

- **Literal address:** Seatbelt on macOS 27.2 refuses one in every network
  filter: `sandbox-exec: host must be * or localhost in network address`
  for `127.0.0.1:<port>`, `[::1]:<port>`, `::1:<port>` in `tcp` and `ip`
  and `ip4`. `tcp4`/`tcp6`/`ip4` with `localhost` are accepted. Measured
  with listeners on both `127.0.0.1:P` and `[::1]:P`: `tcp` allows both;
  `tcp4` allows only `127.0.0.1`. `lo0` has only `127.0.0.1` for IPv4
  (`127.0.0.2` is unreachable even unsandboxed), so `tcp4` pins to the
  one address hostd listens on unless an admin adds lo0 aliases (not
  tested: needs root).
- **Node with `tcp4`:** `net.connect`, `fetch` and `http.get` to
  `localhost:<port>` still work (Node's family fallback reaches
  127.0.0.1). `listen(P, "localhost")` and `listen(P, "::1")` get `EPERM`
  (they were unreachable by hostd anyway, which dials 127.0.0.1);
  `listen(P)` falls back to `0.0.0.0`.
- **What workerd and node read at start-up** (`(allow file-read* (with
  report))` + `log stream`): `/`, the dyld shared cache under
  `/System/Volumes/Preboot/Cryptexes`, `/etc/localtime` and
  `/private/var/db/timezone`, `/dev/urandom`, `/dev/dtracehelper`,
  `/System/Library/OpenSSL/openssl.cnf`, their binary/prefix and own
  files; stat of the root symlinks and path ancestors. Without the literal
  `/` read every process aborts at exec (dyld).
- **Denials seen under the new profile** across the runs below (from
  `log stream`): `~/.CFUserTextEncoding` (home, denied before too),
  `/Library/Preferences/Logging/com.apple.diagnosticd.filter.plist`
  (os_log filter; harmless), workerd stat of `$TMPDIR` and `/var/tmp`
  (it couldn't write there before either). Nothing a test depends on.
- **Not fixed, needs an issue (side finding):** Seatbelt's bind rule
  ignores the host part: under `(local tcp[4] "localhost:P")` a sandboxed
  node binds `0.0.0.0:P` / `[::]:P` and accepted a connection on the
  Mac's LAN address. Seatbelt can't narrow it (no literal addresses;
  `network-inbound` is checked at listen, not per peer). Landlock's bind
  rule is port-only too. So an app that ignores `HOST` is reachable from
  the LAN on its port, bypassing tailworks ingress. Recorded in the docs'
  TCP row; issue draft in the PR description.

## Tests run (macOS 27.2, arm64)

- `go test ./hostd/ -run TestSeatbeltStrict` against the old profile:
  FAIL (shape; `read_tmp`, `read_user_temp_dir`, `list_user_temp_dir`,
  `tcp_connect_given_port_ipv6` allowed). With the change: PASS.
- `TW_TEST_SANDBOX=auto go test ./hostd/... -count=1`: PASS (hostd 222 s).
- `make test-node` (Node tier + Vercel Workflow under Seatbelt): PASS.
- `make test-sdk`: PASS (seatbelt).
- `TW_TEST_SANDBOX=auto make e2e-observability e2e-queues e2e-locks
  e2e-wrangler`: all PASS.
- `make lint`: PASS. `make test`: see the PR.
