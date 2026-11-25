---
kind: security
upgrade: []
---
`tw dev --inspector-port` no longer lets a web page attach to the
debugger (red-team H-dev-inspector, #335). The inspector proxy now
accepts only a `Host` of `127.0.0.1`, `localhost` or `[::1]` with its
port (else `421`), and only a debugger's `Origin` (`devtools://`,
`chrome-devtools://`, `vscode-webview://`, `vscode-file://`) or its own
loopback origin (else `403`). Chrome DevTools and VS Code attach as
before. Only your app's isolate (`workerd: worker tenant`) is listed and
reachable; tailworks' own `platform` and `deny` isolates are not.

workerd's own inspector port, which the proxy forwards to, still checks
neither header. docs/dev-debugging.md ("Who may connect") describes what
remains.
