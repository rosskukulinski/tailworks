---
kind: feature
upgrade: []
---
`tw d1 execute <db>` reads the SQL from stdin when neither `--command` nor
`--file` is given and stdin is not a terminal, as `tw kv key put`,
`tw queues send` and `tw secret put` do: `tw d1 execute notes-db < backup.sql`,
or `tw d1 export a | tw d1 execute b` to copy a database. Stdin holds at
most 8 MiB, the limit of one execute. `--command` and `--file` work as
before; giving both is still refused, and on a terminal with neither the
error now names the stdin form.
