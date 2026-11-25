---
kind: feature
upgrade: [protocol]
---
An image alias can name a `source`: a digest-pinned published image that
fills the alias target when it isn't in tailworks' registry yet. On an arm64
deploy of `cloudflare/sandbox:<v>`, tw-mgmt then copies the rebuild into
`public/` itself (verified against the digest, once for every tenant), and
`tw deploy` builds a Dockerfile that names it from the same image.
`GET /v1/images/aliases` returns the new optional `source` field, and
`tw-mgmt -image-aliases` files accept it (a `source` must be pinned by
digest). The default aliases have no `source` yet: until tailworks' arm64
rebuilds are published, operators still build them and push them with
`tw image push`, and the `image_unavailable` and `mirror_disabled` errors
and docs/containers.md now say so.
