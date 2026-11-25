package bundle

import (
	"errors"
	"fmt"

	"github.com/rosskukulinski/tailworks/internal/queuecfg"
)

// Queues (ADR-039): wrangler.jsonc "queues". Producers become env
// bindings (workerd's queue binding, backed by the platform); push
// consumers are run by hostd against the default export's queue()
// handler. The queues themselves live in mgmt.

// parseQueues reads and validates wrangler "queues". A producer binding
// may not take the name of a Durable Object binding, a var or a storage
// binding.
func parseQueues(wranglerJSON []byte, a *App) (*queuecfg.Config, error) {
	c, err := queuecfg.FromWrangler(wranglerJSON)
	if err != nil {
		return nil, fmt.Errorf("bundle: wrangler.jsonc %w", err)
	}
	if c == nil {
		return nil, nil
	}
	taken := map[string]string{}
	for _, b := range a.Bindings {
		taken[b.Name] = "a durable_objects binding"
	}
	for v := range a.Vars {
		taken[v] = "a var"
	}
	for _, s := range a.Stores {
		taken[s.Binding] = "a storage binding"
	}
	for _, p := range c.Producers {
		if what, dup := taken[p.Binding]; dup {
			return nil, fmt.Errorf("bundle: wrangler.jsonc queues.producers: binding %s has the name of %s", p.Binding, what)
		}
	}
	return c, nil
}

// refuseNodeQueues: the node runtime has no queue bindings or queue()
// handler.
func refuseNodeQueues(files map[string][]byte) error {
	wb, ok := files["wrangler.jsonc"]
	if !ok {
		wb, ok = files["wrangler.json"]
	}
	if !ok {
		return nil
	}
	c, err := queuecfg.FromWrangler(StripJSONC(wb))
	if err != nil {
		return fmt.Errorf("bundle: wrangler.jsonc %w", err)
	}
	if !c.Empty() {
		return errors.New("bundle: queues: Queues need the workerd runtime (queue bindings and a queue() handler); runtime \"node\" does not support them")
	}
	return nil
}
