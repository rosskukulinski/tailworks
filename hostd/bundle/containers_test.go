package bundle

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/container"
)

// Container classes come from wrangler "containers" (Cloudflare's shape)
// with tailworks.jsonc "containers" overriding by class; each must be a DO class.
func TestParseContainers(t *testing.T) {
	wrangler := `{
	  "name": "sb", "main": "index.js",
	  // Cloudflare's examples/minimal
	  "containers": [{ "class_name": "Sandbox", "image": "./Dockerfile", "instance_type": "lite", "max_instances": 1 }],
	  "durable_objects": { "bindings": [{ "name": "Sandbox", "class_name": "Sandbox" }] },
	}`
	files := map[string][]byte{"wrangler.jsonc": []byte(wrangler), "index.js": []byte("export default {}"),
		"tailworks.jsonc": []byte(`{ "containers": [{ "class": "Sandbox", "instance_type": "basic" }] }`)}
	a, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := a.Containers["Sandbox"]
	if !ok || c.Source != "./Dockerfile" || c.Image != "" || c.MaxInstances != 1 || c.Resources() != container.InstanceTypes["basic"] {
		t.Fatalf("%+v", a.Containers)
	}
	files["tailworks.jsonc"] = []byte(`{ "containers": [{ "class": "Other", "image": "x" }] }`)
	if _, err := Parse(files); err == nil || !strings.Contains(err.Error(), "no durable_objects binding") {
		t.Fatalf("container without a DO class: %v", err)
	}
	node := map[string][]byte{"tailworks.jsonc": []byte(`{"runtime":"node","main":"s.mjs","containers":[{"class":"S","image":"x"}]}`), "s.mjs": nil}
	if _, err := Parse(node); err == nil || !strings.Contains(err.Error(), "no containers") {
		t.Fatalf("node runtime with containers: %v", err)
	}
}

func TestParseContainerPins(t *testing.T) {
	base := func(wrangler, tw string) map[string][]byte {
		f := map[string][]byte{"index.js": []byte("export default {}"), "wrangler.jsonc": []byte(wrangler)}
		if tw != "" {
			f["tailworks.jsonc"] = []byte(tw)
		}
		return f
	}
	// Sandbox SDK 0.x: a Dockerfile image, pinned by tailworks.jsonc.
	a, err := Parse(base(`{"name":"m","main":"index.js",
	  "containers":[{"class_name":"Sandbox","image":"./Dockerfile","instance_type":"lite","max_instances":1}],
	  "durable_objects":{"bindings":[{"class_name":"Sandbox","name":"Sandbox"}]}}`,
		`{"containers":{"Sandbox":{"image":"tw.local/e2e/sandbox-minimal-v0:1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := a.Containers["Sandbox"]
	if !ok || c.Image != "tw.local/e2e/sandbox-minimal-v0:1" || c.Source != "./Dockerfile" || c.InstanceType != "lite" || c.MaxInstances != 1 {
		t.Fatalf("%+v", c)
	}
	// Unpinned Dockerfile: no ref (tw deploy must build it).
	a, _ = Parse(base(`{"name":"m","main":"index.js","containers":[{"class_name":"S","image":"./Dockerfile"}],
	  "durable_objects":{"bindings":[{"class_name":"S","name":"S"}]}}`, ""))
	if a.Containers["S"].Image != "" || len(a.Containers["S"].Refs()) != 0 {
		t.Fatalf("%+v", a.Containers["S"])
	}
	// Sandbox SDK 1.0: named images, a custom instance.
	a, err = Parse(base(`{"name":"m","main":"index.js",
	  "containers":[{"class_name":"Sandbox","images":{"sandbox":{"dockerfile":"./Dockerfile"},"py":{"image":"docker.io/library/python:3"}},
	     "instance_type":{"vcpu":0.5,"memory_mib":1024,"disk_mb":4000}}],
	  "durable_objects":{"bindings":[{"class_name":"Sandbox","name":"SANDBOX"}]}}`,
		`{"containers":{"Sandbox":{"images":{"sandbox":"tw.local/e2e/sandbox-minimal-v1:1"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	c = a.Containers["Sandbox"]
	if c.Images["sandbox"] != "tw.local/e2e/sandbox-minimal-v1:1" || c.Images["py"] != "docker.io/library/python:3" ||
		c.Custom == nil || c.Custom.VCPU != 0.5 || c.Custom.MemMiB != 1024 || len(c.Refs()) != 2 {
		t.Fatalf("%+v %+v", c, c.Custom)
	}
	for _, bad := range []struct{ w, t, want string }{
		{`{"name":"m","main":"index.js","containers":[{"class_name":"Nope","image":"x:1"}],"durable_objects":{"bindings":[{"class_name":"S","name":"S"}]}}`, "", "no durable_objects binding"},
		{`{"name":"m","main":"index.js","containers":[{"class_name":"S","image":"x:1"}],"durable_objects":{"bindings":[{"class_name":"S","name":"S"}]}}`, `{"containers":{"T":{"image":"y:1"}}}`, "no durable_objects binding"},
		{`{"name":"m","main":"index.js","containers":[{"class_name":"S","image":"x:1"}],"durable_objects":{"bindings":[{"class_name":"S","name":"S"}]}}`, `{"containers":{"S":{"image":"Bad Ref;rm"}}}`, "bad image reference"},
		{`{"name":"m","main":"index.js","containers":[{"class_name":"S","image":"x:1"}],"durable_objects":{"bindings":[{"class_name":"S","name":"S"}]}}`, `{"containers":{"S":{"images":{"zz":"y:1"}}}}`, "not in wrangler's images"},
	} {
		if _, err := Parse(base(bad.w, bad.t)); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("want %q, got %v", bad.want, err)
		}
	}
}
