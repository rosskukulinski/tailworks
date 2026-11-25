---
kind: fix
upgrade: []
---
A container exec on a busy microVM could report exit 0 with its output
missing: when the guest was slow to read a short-lived process's stdout
or stderr, the post-exit read deadline dropped what the process had
already written. The guest now sends everything buffered before it stops
reading, so only output written after the exit (by a daemon the exec
left behind) is cut off, as before.
