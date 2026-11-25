---
kind: security
upgrade: []
---
A sandboxed Node app can now be reached only through tailworks' ingress.
Before, an app that ignored `HOST` and called `listen(PORT)` (which binds
every interface), or that opened its own listener on `0.0.0.0`, could be
reached on its port at the host's LAN address, without WireGuard identity
or tailnet ACLs. That was possible on both macOS and Linux.

hostd now owns the app's listener. On Linux it passes `127.0.0.1:<PORT>`
to the app as fd 3, and a seccomp rule makes any other `listen()` a no-op.
On macOS the app listens on a unix socket in its instance dir, and hostd
forwards `127.0.0.1:<PORT>` to that socket for self-calls. A preload points
`server.listen(PORT)` at hostd's listener, so apps need no change.

Behaviour change: under a sandbox, a listen on any port other than `PORT`
(or a native module's own TCP listener) now fails. On Linux kernels with
Landlock ABI < 4 it returns success instead, but the socket never accepts
connections.
