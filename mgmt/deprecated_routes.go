package mgmt

import (
	"net/http"
	"net/url"
)

// objectCertDeprecatedAt is when GET /v1/objects/{id}/certificate was
// deprecated (2026-11-25T00:00:00Z), sent as the RFC 9745 Deprecation
// header. ADR-041: removed no earlier than two releases later.
const objectCertDeprecatedAt = "@1795564800"

// setObjectCertDeprecation marks a GET /v1/objects/{id}/certificate response
// deprecated and, when the object is known, links the replacement route
// GET /v1/certificates/{stableNodeID}?tenant=.
func setObjectCertDeprecation(w http.ResponseWriter, o Object, err error) {
	w.Header().Set("Deprecation", objectCertDeprecatedAt)
	if err != nil || o.StableNodeID == "" {
		return
	}
	succ := "/v1/certificates/" + url.PathEscape(o.StableNodeID) + "?tenant=" + url.QueryEscape(o.TenantID)
	w.Header().Set("Link", "<"+succ+`>; rel="successor-version"`)
}
