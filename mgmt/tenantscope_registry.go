package mgmt

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rosskukulinski/tailworks/internal/registry"
)

// The registry's rule for tenant admins (ADR-043 D3). Handler
// marks a tenant admin's /v2 request with registryTenantScopeKey;
// registryAuthorize then defers here.

type registryTenantScopeKey struct{}

func withRegistryTenantScope(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), registryTenantScopeKey{}, true))
}

func registryTenantAuthorize(p Principal, ns string, push bool) error {
	switch {
	case ns == "":
		// The /v2/ version check names no repository.
		return nil
	case ns == registry.PublicNamespace:
		if push {
			return &registry.Denied{Reason: "only operators push operator base images (public/…)"}
		}
		if len(p.tenantsWith(ActionRead)) == 0 {
			return &registry.Denied{Reason: fmt.Sprintf("no %s value grants %q", TenantAdminCap, ActionRead)}
		}
		return nil
	}
	action := ActionRead
	if push {
		action = ActionDeploy
	}
	if !p.tenantAllows(ns, action) {
		return &registry.Denied{Reason: fmt.Sprintf("no single %s value names tenant %q with %q", TenantAdminCap, ns, action)}
	}
	return nil
}
