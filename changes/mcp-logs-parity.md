---
kind: feature
upgrade: [config-default]
---
The MCP server's `logs` tool takes `outcome` and `caller`, so it has every
`tw logs` record filter, and `audit` takes `human` like `tw audit --human`.
The MCP `kill` tool and the admin UI's kill form now cascade by default, like
`tw kill`: the object's sub-agents are killed with it, attributed to the
caller and the reason. Pass `cascade: false` (or untick the checkbox) to kill
only the object; the reaper then kills its sub-agents within seconds, as
before.
