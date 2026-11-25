package gen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// Worker secrets (ADR-034): app.Secrets become text bindings of
// the tenant worker, so `env.NAME` is a string exactly as with Cloudflare
// secrets, and enter the facets' env (tw-config.json "vars") the same
// way. A secret overrides a var of the same name. Both files live only in
// the 0700 instance directory, which the process sandbox keeps from every
// other tenant.

var secretBindingRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkSecrets refuses secret names workerd can't bind or that collide
// with a DO binding or tailworks' own names.
func checkSecrets(app *bundle.App) error {
	for k := range app.Secrets {
		if !secretBindingRE.MatchString(k) || strings.HasPrefix(k, "__TW") || strings.HasPrefix(k, "TW_") {
			return fmt.Errorf("gen: bad secret name %q", k)
		}
		for _, b := range app.Bindings {
			if b.Name == k {
				return fmt.Errorf("gen: secret %q has the name of a Durable Object binding (wrangler.jsonc durable_objects.bindings); rename one (tw secret rm %s)", k, k)
			}
		}
	}
	return nil
}

// varsWithSecrets is vars with every secret added as a JSON string
// (overriding a var of the same name). vars is not modified.
func varsWithSecrets(vars map[string]json.RawMessage, secrets map[string]string) map[string]json.RawMessage {
	if len(secrets) == 0 {
		return vars
	}
	out := make(map[string]json.RawMessage, len(vars)+len(secrets))
	for k, v := range vars {
		out[k] = v
	}
	for k, v := range secrets {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

// secretBindings is the tenant worker's text bindings for app.Secrets, in
// name order.
func secretBindings(app *bundle.App) string {
	names := make([]string, 0, len(app.Secrets))
	for k := range app.Secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		fmt.Fprintf(&b, "    (name = %s, text = %s),\n", cq(k), cq(app.Secrets[k]))
	}
	return b.String()
}
