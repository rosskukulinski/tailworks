package mgmt

import (
	"context"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/compatflags"
	"github.com/rosskukulinski/tailworks/internal/features"
)

// Experimental compatibility flags (#337, ledger H-compat-flags). A host
// refuses a deployment whose compatibility_flags include a flag workerd
// marks experimental unless its operator started it with
// -allow-experimental-flags (hostd/gen/compatflags.go), in which case it
// advertises the experimental-flags feature and features.Required asks
// for it, so placement pins such a deployment to one of those hosts. This
// is the early refusal: the developer sees the typed error at the deploy
// and at its dry run (tw deploy's preflight), before any host is picked.

// checkCompatFlags refuses r when its bundle lists experimental flags and
// no registered host allows them.
func (m *Mgmt) checkCompatFlags(ctx context.Context, r DeployRequest) error {
	if len(r.Bundle) == 0 {
		return nil
	}
	exp := compatflags.InBundle(r.Bundle)
	if len(exp) == 0 {
		return nil
	}
	hosts, err := m.store.ListHosts(ctx, Page{})
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if slices.Contains(h.Features, features.ExperimentalFlags) {
			return nil // placement needs the feature, so it lands on such a host
		}
	}
	return errf(403, "experimental_flag", "compatibility_flags %s: workerd marks %s experimental (it refuses them without --experimental) and no host allows them for deployments; remove them, or have the host's operator start it with tw up --allow-experimental-flags (tw-hostd -allow-experimental-flags)",
		strings.Join(exp, ", "), plural(len(exp), "this flag", "these flags"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
