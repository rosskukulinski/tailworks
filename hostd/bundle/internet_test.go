package bundle

import (
	"slices"
	"strings"
	"testing"
)

func TestInternetEgress(t *testing.T) {
	for _, c := range []struct {
		name, jsonc string
		workerOff   bool
		workerAllow []string
		containers  []string
	}{
		{"absent", `{"tailnet": {}}`, false, nil, nil},
		{"true", `{"tailnet": {"egress": {"internet": true}}}`, false, nil, []string{"*", "0.0.0.0/0", "::/0"}},
		{"false", `{"tailnet": {"egress": {"internet": false}}}`, true, nil, nil},
		{"empty", `{"tailnet": {"egress": {"internet": []}}}`, true, []string{}, []string{}},
		{"list", `{"tailnet": {"egress": {"internet": ["api.example.com", "*.github.com"]}}}`, false,
			[]string{"api.example.com", "*.github.com"}, []string{"api.example.com", "*.github.com"}},
	} {
		var tc twConfig
		if err := UnmarshalJSONC([]byte(c.jsonc), &tc); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ie := tc.Tailnet.Egress.Internet
		if ie.WorkerOff() != c.workerOff || !slices.Equal(ie.WorkerAllow(), c.workerAllow) || !slices.Equal(ie.Containers(), c.containers) {
			t.Errorf("%s: off=%v allow=%v containers=%v", c.name, ie.WorkerOff(), ie.WorkerAllow(), ie.Containers())
		}
	}
	var tc twConfig
	if err := UnmarshalJSONC([]byte(`{"tailnet": {"egress": {"internet": "x"}}}`), &tc); err == nil {
		t.Error("a string must be refused")
	}
}

// TestEgressVia: tailnet.egress.via is "" or "exit-node" (ADR-054).
func TestEgressVia(t *testing.T) {
	parse := func(tw string) (*App, error) {
		return Parse(map[string][]byte{
			"wrangler.jsonc":  []byte(`{"name":"app","main":"index.js"}`),
			"index.js":        []byte(`export default {}`),
			"tailworks.jsonc": []byte(tw),
		})
	}
	a, err := parse(`{"tailnet": {"egress": {"via": "exit-node", "internet": ["api.example.com"]}}}`)
	if err != nil || a.EgressVia != ViaExitNode || !slices.Equal(a.Internet.WorkerAllow(), []string{"api.example.com"}) {
		t.Fatalf("via: %+v %v", a, err)
	}
	if a, err := parse(`{"tailnet": {}}`); err != nil || a.EgressVia != "" {
		t.Fatalf("absent: %q %v", a.EgressVia, err)
	}
	if _, err := parse(`{"tailnet": {"egress": {"via": "host"}}}`); err == nil || !strings.Contains(err.Error(), `tailnet.egress.via "host"`) {
		t.Fatalf("bad via: %v", err)
	}
}
