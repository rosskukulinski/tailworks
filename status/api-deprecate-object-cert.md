Last update: 2026-11-25 PT. #520 step 1: GET /v1/objects/{id}/certificate deprecated; the admin UI uses GET /v1/certificates/{stableNodeID}.

## What (this release)

- Spec: `deprecated: true` on `GetObjectCertificate`; its description
  points at the replacement. The netd-verification paragraph and the
  "Replaces `GET /v0/certs/{nodeID}`" lineage moved to `GetCertificate`.
- Handler (`mgmt/deprecated_routes.go`): every response carries
  `Deprecation: @1795564800` (RFC 9745, 2026-11-25) and, when the object
  has a node, `Link: </v1/certificates/<stableNodeID>?tenant=<tenantID>>;
  rel="successor-version"`.
- Admin UI object page calls `/v1/certificates/{stableNodeID}?tenant=`.
- `api/render`: the route table marks deprecated operations.
- `docs/errors.md` `not_ready`: reworded (hostd never called the route).
- `changes/api-deprecate-object-cert.md` (kind fix, upgrade protocol).

## Left for later (ADR-041 §9: removal no earlier than N+2)

Delete the path, `GetObjectCertificate`, `mgmt/deprecated_routes.go`, the
`hostd/fakemgmt/drift_test.go` allowlist entry and `not_ready` (its only
emitter) from the spec enum and docs/errors.md; `make api`; a Breaking
release note. Keep #520 open for that.

## Tests

- `go test ./mgmt/ -run 'TestObjectCertificateDeprecated|TestNodeCertificates'`: ok
- `go test ./mgmt/ui/ ./hostd/fakemgmt/`: ok
- `make lint`: ok (after merging origin/master)
- `make test` (mbp): ok except `mgmt TestRedeployAfterUndeploy` (heartbeat
  timing under load, 503 no_host); passes locally `-count=2`.
