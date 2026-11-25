package gen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/internal/queuecfg"
)

// TestGenerateQueues: a producer binding becomes workerd's queue binding
// to the platform's QueueProducer with props naming the queue and binding
// (and a platform-side twin for facets); a push consumer turns on
// service_binding_extra_handlers (TENANT.queue()) and is listed in
// tw-config.json; producers alone don't.
func TestGenerateQueues(t *testing.T) {
	gen1 := func(q *queuecfg.Config, secrets map[string]string) (string, []byte, error) {
		app := &bundle.App{
			Name: "w", Main: "index.js", CompatDate: "2026-04-01",
			Modules: map[string][]byte{"index.js": []byte("export default { fetch() { return new Response('ok'); } };")},
			Classes: map[string]*bundle.Class{}, Queues: q, Secrets: secrets,
		}
		dir := filepath.Join(t.TempDir(), "i")
		if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
			return "", nil, err
		}
		c, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
		cfg, _ := os.ReadFile(filepath.Join(dir, "tw-config.json"))
		return string(c), cfg, nil
	}
	q, err := queuecfg.Parse([]byte(`{"producers":[{"binding":"JOBS","queue":"jobs"}],"consumers":[{"queue":"jobs"},{"queue":"pulled","type":"http_pull"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	c, cfgb, err := gen1(q, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`(name = "JOBS", queue = (name = "platform", entrypoint = "QueueProducer", props = (json = "{\"binding\":\"JOBS\",\"queue\":\"jobs\"}")))`,
		`(name = "__TW_QUEUE_JOBS", queue = (name = "platform", entrypoint = "QueueProducer"`,
		`compatibilityFlags = ["service_binding_extra_handlers"]`,
		`(name = "tw-queue.js", esModule = embed "tw-queue.js")`,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("config.capnp lacks %s", want)
		}
	}
	var tc struct {
		Queues struct {
			Consumers []string
			Producers []struct{ Binding, Queue string }
		}
	}
	if err := json.Unmarshal(cfgb, &tc); err != nil || len(tc.Queues.Consumers) != 1 || tc.Queues.Consumers[0] != "jobs" || tc.Queues.Producers[0].Binding != "JOBS" {
		t.Fatalf("tw-config.json queues: %+v %v", tc.Queues, err)
	}
	// The tenant never gets the platform-side twin.
	tenant := c[strings.Index(c, "const tenant"):]
	if strings.Contains(tenant, "__TW_QUEUE_") {
		t.Error("the tenant worker has a platform queue binding")
	}

	// Producers only: no extra handlers.
	po, _ := queuecfg.Parse([]byte(`{"producers":[{"binding":"JOBS","queue":"jobs"}]}`))
	if c, _, err := gen1(po, nil); err != nil || strings.Contains(c, "service_binding_extra_handlers") {
		t.Fatalf("producers only: %v", err)
	}
	// A secret may not take a queue binding's name.
	if _, _, err := gen1(q, map[string]string{"JOBS": "x"}); err == nil {
		t.Error("a secret named like a queue binding was accepted")
	}
	// No queues: an empty config, still a valid platform.
	if _, cfgb, err := gen1(nil, nil); err != nil || !strings.Contains(string(cfgb), `"consumers": []`) {
		t.Fatalf("no queues: %v %s", err, cfgb)
	}
}
