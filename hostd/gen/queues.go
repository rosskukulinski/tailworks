package gen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/queuecfg"
)

// Queues (ADR-039): every producer binding becomes workerd's own
// `queue` binding in the tenant worker, targeting the platform's
// QueueProducer entrypoint (js/queue.js) with props {queue}; facets get a
// QueueFacetProxy that forwards to a platform-side native binding of the
// same queue (`__TW_QUEUE_<Binding>`). Push consumers are run by hostd
// through the platform's deliverQueue (cf.tw kind "queue"), which needs
// workerd's service_binding_extra_handlers (TENANT.queue()).

//go:embed js/queue.js
var queueJS string

// queueCfg is tw-config.json "queues".
type queueCfg struct {
	// Consumers are the queues whose batches the platform delivers.
	Consumers []string `json:"consumers"`
	// Producers are the producer bindings a facet's env gets.
	Producers []queueProducerCfg `json:"producers"`
}

type queueProducerCfg struct {
	Binding string `json:"binding"`
	Queue   string `json:"queue"`
}

func queueConfig(app *bundle.App) queueCfg {
	c := queueCfg{Consumers: []string{}, Producers: []queueProducerCfg{}}
	if app.Queues == nil {
		return c
	}
	for _, k := range app.Queues.PushConsumers() {
		c.Consumers = append(c.Consumers, k.Queue)
	}
	for _, p := range app.Queues.Producers {
		c.Producers = append(c.Producers, queueProducerCfg{p.Binding, p.Queue})
	}
	return c
}

// hasQueueConsumers: the app's default export runs queue() batches.
func hasQueueConsumers(app *bundle.App) bool {
	return app.Queues != nil && len(app.Queues.PushConsumers()) > 0
}

// checkQueues refuses a secret with a producer binding's name.
func checkQueues(app *bundle.App) error {
	if app.Queues == nil {
		return nil
	}
	for _, p := range app.Queues.Producers {
		if _, ok := app.Secrets[p.Binding]; ok {
			return fmt.Errorf("gen: secret %q has the name of a queue binding; rename one (tw secret rm %s)", p.Binding, p.Binding)
		}
	}
	return nil
}

func queueBinding(name string, p queuecfg.Producer) string {
	props, _ := json.Marshal(map[string]string{"queue": p.Queue, "binding": p.Binding})
	return fmt.Sprintf("    (name = %s, queue = (name = \"platform\", entrypoint = \"QueueProducer\", props = (json = %s))),\n",
		cq(name), cq(string(props)))
}

// queueTenantBindings are the tenant worker's env.<Binding> entries.
func queueTenantBindings(app *bundle.App) string {
	if app.Queues == nil {
		return ""
	}
	var s strings.Builder
	for _, p := range app.Queues.Producers {
		s.WriteString(queueBinding(p.Binding, p))
	}
	return s.String()
}

// queuePlatformBindings are the platform's native bindings that facet
// proxies forward to.
func queuePlatformBindings(app *bundle.App) string {
	if app.Queues == nil {
		return ""
	}
	var s strings.Builder
	for _, p := range app.Queues.Producers {
		s.WriteString(queueBinding("__TW_QUEUE_"+p.Binding, p))
	}
	return s.String()
}
