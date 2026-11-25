---
kind: fix
upgrade: []
---
A Durable Object can now reach another object with
`env.X.get(env.X.idFromName(name))`, as well as with
`env.X.getByName(name)` (#391). Before, inside an object that runs as a
facet (the default), `idFromName` returned a pending RPC result and
`get()` failed with `Could not serialize object of type "RpcPromise"`.
The Agents SDK's `getAgentByName` takes that path, so one agent calling
another (the Agents playground's multi-agent demo) failed. Such an id's
`toString()` is the object's name; `idFromString` and `newUniqueId`
inside an object now fail with `bad_request`, since tailworks objects are
addressed by name.
