Last update: 2026-11-21 PT. Issue #150 (ledger H13, the output half of F9): `tw` escapes control characters in tenant- and internet-controlled strings.

## What changed

- New package `internal/termsafe` is the one sanitiser for human-readable
  CLI output:
  - `String` escapes C0 (`\n`, `\r`, `\t`, `\x1b`, `\x00`, …), DEL
    (`\x7f`), C1 (`\u009b`), bidi controls (U+202A–202E, U+2066–2069,
    LRM/RLM/ALM as `‮` and so on), U+2028/2029, and raw invalid UTF-8
    bytes (`\xff`; 0x9b is CSI on 8-bit terminals). Every other rune
    (accents, CJK, emoji, RTL letters) is left alone. Backslash is not
    escaped, because the output is for people.
  - `Line` is the same but keeps TAB (stack traces). `Text` keeps LF and
    TAB (multi-line error messages). `Writer` is a line-buffered
    `io.Writer` for log streams.
  - `JSON` rewrites encoded JSON so DEL, C1 and bidi runes, which
    encoding/json leaves raw, become `\uXXXX`. The decoded value is
    unchanged.
- Used by (`cmd/tw/termsafe.go` aliases `tsafe` / `tsafeAll`):
  - `tw ls` (deployments, objects, tags, parent, deploy errors), `tw audit`
    (node, text, chain, chain errors), `tw kill`, `tw undeploy`.
  - `tw logs`: request records (`obs.Record.Text` escapes the summary,
    console lines, exceptions and stack); the raw `--console` and daemon
    streams, local and remote (`logsOut` returns a `termsafe.Writer`); a
    host's non-record lines and `[host]` prefixes (`copyRecords`).
  - `tw deployments`, `tw previews`, `tw rollback` and `tw promote`
    (placement and preflight lines), `tw approvals` and `tw approve`
    (instance, step, params), `tw kv namespace list` and `tw kv key list`
    (metadata), `tw r2` listings, `tw queues ls`, `tw queues info` and
    `tw queues messages` (bodies), `tw d1 list` and `tw d1 execute` (column
    names, cells), `tw secret ls`, `tw status` (workload and deployment
    errors, containers and reservations), `tw host ls`, `tw host drain`,
    `tw notify test`, and the deploy wait for a public URL.
  - `printErr`: message and lines go through `Text` (mgmt and host error
    text can quote tenant output).
- `--json`: `emit`, NDJSON events, `printRecord` and the record
  pass-through in `copyRecords` go through `termsafe.JSON`. Checked, not
  assumed: encoding/json escapes ESC, CR, LF and NUL as `\u00XX` and
  replaces invalid UTF-8, but sends DEL, C1 (U+009B) and U+202E raw.
  `TestJSONEscapesWhatEncodingJSONLeavesRaw` pins that premise, and the
  decoded value is unchanged.
- Admin UI (`mgmt/ui`): html/template escapes every page, and nothing builds
  `template.HTML` from API strings (`since` formats integers only). The
  live refresh copies `innerHTML` from the server's own escaped page. New
  `ui.TestHostileStringsEscaped` puts `<script>`/`<img onerror>` in an
  object name, a deployment error and a public request path, and checks
  every page. Raw control characters are inert in HTML. Bidi overrides
  can still reorder text visually in the browser; that is not covered here.
- Not changed, on purpose:
  - `tw kv key get` writes the value raw. It is data, like `cat`, often
    redirected to a file.
  - `tw d1 export` writes the dump raw.
  - `tw dev`'s passthrough of the developer's own workerd output (their own
    code on their own machine; escaping would break its colours). `tw dev`'s
    request records do go through `obs.Record.Text`.
  - Names that mgmt validates (workload, store, secret names) are escaped
    anyway where they share a line with untrusted text.
- Raw `--console` streams: LF is the stream's framing, so tenant output
  still owns its own lines there. Everything else is escaped. In request
  records, a multi-line console message stays indented under its record,
  so it can't start a row.

## Split out

Refusing control bytes at the input is the other half of F9 and is now
#185: in `publicspec.CanonicalPath` (a NUL in a public path reaches the
app, per `netd.TestPublicPathTricksNeverReachPlatformPaths`' log) and in
object names at `ensure`/`admit`. That changes what public ingress and the
object API accept, so it isn't folded into this renderer fix.

## Ledger

`security/LEDGER.md`: H13 is **fixed**, pinned by
`cmd/tw.TestRT3_H13_TerminalInjection` and `ui.TestHostileStringsEscaped`.
F9 is **partial**: renderers done, input refusal in #185. A run-log row was
added.

## Tests

- `go test ./internal/termsafe/ ./internal/obs/ ./internal/auditfmt/ ./mgmt/ui/ ./cmd/tw/ -count=1`: ok.
- `TestRT3_H13_TerminalInjection` (`tw ls`, `tw audit --all`, `tw audit
  <object>`, human and `--json`, against a fake linked mgmt; plus
  `copyRecords` and `logsOut`): passes here, and fails on origin/master
  (b43256f) with `tw ls: raw control byte 0x1b`.
- `make lint`: ok (staticcheck ST1018 caught raw U+202E in a test
  literal; fixed).
- `make changes-check`: ok, no upgrade-relevant paths. A `security`
  fragment is in `changes/fix-150-terminal-escapes.md`.
- `make test`: all packages ok except `cmd/tw` `TestCLISingleProcess`,
  which failed on its 1 s kill budget (1.08 s on a loaded shared machine).
  Rerun alone: ok.
- No extra suites apply (no store, placement, queues runtime, templates or
  wrangler changes).
