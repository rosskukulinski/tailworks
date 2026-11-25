---
kind: feature
upgrade: [cli-json]
---
`tw expose names` lists the public names a tenant owns, and
`tw expose release <name>` gives one up once nothing serves it, so any
tenant on the tailnet can deploy public routes under it next. They call
the existing `GET`/`DELETE /v1/tenants/{id}/public-names` routes, which
until now only a raw API call reached; the release is audited with the
operator as the actor. A release of a team tenant's (prefixed) public name
is now refused while its workload still serves public routes, as an
unprefixed one already was.
