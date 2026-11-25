---
kind: feature
upgrade: []
---
An MCP server on tailworks can be used from hosted assistants (claude.ai,
ChatGPT and other connectors) while the rest of the app stays
tailnet-only. `@tailworks/mcp`'s `withTailnet` takes a `publicOAuth`
option (`resource`, `authorizationServers`, `verify`): it serves the OAuth
protected resource metadata, answers public requests without a valid
bearer token with a `401` challenge, and refuses tokens whose audience
isn't the configured resource. Tools see who called through the new
`requester()`, which returns `{kind: "public", subject, login, …}` for such
a request; `caller()` and `authorizeTool()` still throw `public_request`
on it, so tools written for tailnet grants stay closed to the internet.
`introspectionVerifier({ endpoint })` checks tokens with RFC 7662 token
introspection (tsidp's `/introspect`). The recipe, with tsidp as the
authorization server, is docs/mcp-public.md.
