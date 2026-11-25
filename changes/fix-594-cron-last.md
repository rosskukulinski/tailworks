---
kind: fix
upgrade: []
---
A cron trigger no longer runs a minute twice when the host's wall clock
was stepped back and hostd restarted (#594). hostd remembered the last
minute it had run only in memory, so after a restart, a clock that had
been fast (and was corrected) or stepped back ran minutes that had already
run. hostd now keeps that minute per workload in `<data-dir>/cron-last.json`.
It ignores a stored minute more than 15 minutes ahead of its clock, so a
clock that was badly wrong and has been fixed doesn't hold triggers back.
