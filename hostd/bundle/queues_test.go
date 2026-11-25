package bundle_test

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestParseQueues: wrangler.jsonc "queues" becomes App.Queues (validated
// by internal/queuecfg); a producer binding may not take the name of a
// Durable Object binding or a var, and the node runtime refuses queues.
func TestParseQueues(t *testing.T) {
	parse := func(queues string) (*bundle.App, error) {
		return bundle.Parse(map[string][]byte{
			"index.js": []byte("export default {}"),
			"wrangler.jsonc": []byte(`{ "name": "w", "main": "index.js", "vars": { "V": 1 },
				"durable_objects": { "bindings": [{ "name": "BOX", "class_name": "Box" }] },
				"queues": ` + queues + ` }`),
		})
	}
	a, err := parse(`{ "producers": [{ "binding": "JOBS", "queue": "jobs" }], "consumers": [{ "queue": "jobs", "max_batch_size": 5 }] }`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Queues.ProducerBinding("JOBS").Queue != "jobs" || a.Queues.ConsumerOf("jobs").BatchSize() != 5 {
		t.Fatalf("queues: %+v", a.Queues)
	}
	for queues, want := range map[string]string{
		`{ "producers": [{ "binding": "BOX", "queue": "q" }] }`:       "durable_objects binding",
		`{ "producers": [{ "binding": "V", "queue": "q" }] }`:         "a var",
		`{ "producers": [{ "binding": "Q", "queue": "Not_Valid" }] }`: "bad queue name",
		`{ "consumers": [{ "queue": "q", "max_batch_size": 1000 }] }`: "max_batch_size",
	} {
		if _, err := parse(queues); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", queues, err, want)
		}
	}
	if a, err := parse(`{}`); err != nil || a.Queues != nil {
		t.Fatalf("empty queues: %v %+v", err, a)
	}
	_, err = bundle.Parse(map[string][]byte{
		"tailworks.jsonc": []byte(`{ "runtime": "node" }`),
		"wrangler.jsonc":  []byte(`{ "name": "n", "queues": { "producers": [{ "binding": "Q", "queue": "q" }] } }`),
	})
	if err == nil || !strings.Contains(err.Error(), "workerd runtime") {
		t.Fatalf("node runtime with queues: %v", err)
	}
}
