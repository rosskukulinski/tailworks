package schema

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every tailworks.jsonc in the repo (templates, examples, e2e fixtures) must pass:
// the schema is the list of keys tailworks reads, so a failure here means the
// schema is missing a key, not that the fixture is wrong.
func TestRepoConfigsPass(t *testing.T) {
	n := 0
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.Name() != "tailworks.jsonc" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		probs, err := CheckTw(b)
		if err != nil {
			t.Errorf("%s: %v", p, err)
		}
		for _, pr := range probs {
			t.Errorf("%s: %s", p, pr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("found only %d tailworks.jsonc files", n)
	}
}

func TestCheckTw(t *testing.T) {
	for _, tc := range []struct {
		name, cfg string
		want      []string // substrings, one per problem, in path order
	}{
		{"ok", `{
			"$schema": "../schema/tailworks.schema.json",
			"name": "x", "tier": "microvm", "replicas": 2,
			"placement": {"mode": "smart", "targets": ["pg"], "aggregate": "sum"},
			"public": {"routes": ["/hook"], "limits": {"rps": 1.5}},
			"containers": [{"class_name": "C", "image": "./Dockerfile"}],
			"tailnet": {
				"tags": ["tag:a"], "service": true, "connects": ["tag:db"],
				"egress": {"internet": ["api.example.com"]},
				"spawn": {"maxLive": 4, "maxDepth": 2},
				"objects": {"A": {"tags": ["tag:b"], "agent": true, "ttl": "10m", "egress": "workload", "fence": "never", "public": {"create": true, "maxObjects": 9}}},
			},
		}`, nil},
		{"service name", `{"tailnet": {"service": "svc:api"}}`, nil},
		{"internet off", `{"tailnet": {"egress": {"internet": false}}}`, nil},
		{"internet on", `{"tailnet": {"egress": {"internet": true}}}`, nil},
		{"via exit-node", `{"tailnet": {"egress": {"via": "exit-node"}}}`, nil},
		{"via bad", `{"tailnet": {"egress": {"via": "exitnode"}}}`,
			[]string{`tailnet.egress.via: "exitnode" is not one of "exit-node" (did you mean "exit-node"?)`}},
		// docs/tailworks-jsonc.md#tailnetegressvia, verbatim.
		{"via doc example", `{
  "tailnet": {
    "tags": ["tag:tw-wl-billing"],
    "egress": { "internet": ["api.stripe.com"], "via": "exit-node" }
  }
}`, nil},
		{"deprecated placement hints still pass", `{"placement": {"host": "db", "hostname": "db.example", "region": "aws:us-east-1"}}`, nil},
		{"internet bad", `{"tailnet": {"egress": {"internet": "api.example.com"}}}`, []string{`tailnet.egress.internet: is string, want`}},
		{"misspelt objects", `{"tailnet": {"tags": ["tag:a"], "objcts": {}}}`,
			[]string{`tailnet.objcts: unknown key (did you mean "objects"?)`}},
		{"unknown top level", `{"tailnett": {}}`,
			[]string{`tailnett: unknown key (did you mean "tailnet"?)`}},
		{"class key typo", `{"tailnet": {"objects": {"A": {"tag": ["tag:x"]}}}}`,
			[]string{`tailnet.objects.A.tag: unknown key (did you mean "tags"?)`}},
		{"enum", `{"tier": "mircovm"}`,
			[]string{`tier: "mircovm" is not one of "process", "microvm" (did you mean "microvm"?)`}},
		{"type", `{"replicas": "3"}`, []string{`replicas: is string, want an integer`}},
		{"integer", `{"replicas": 1.5}`, []string{`replicas: is number, want an integer`}},
		{"minimum", `{"replicas": 0}`, []string{`below the minimum 1`}},
		{"tag pattern", `{"tailnet": {"tags": ["tw-wl-x"]}}`, []string{`tailnet.tags[0]: "tw-wl-x" must match ^tag:`}},
		{"required", `{"public": {"upgrade": []}}`, []string{`public: missing required "routes"`}},
		{"no suggestion when far", `{"zzzzzzzz": 1}`, []string{"zzzzzzzz: unknown key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probs, err := CheckTw([]byte(tc.cfg))
			if err != nil {
				t.Fatal(err)
			}
			if len(probs) != len(tc.want) {
				t.Fatalf("got %d problems %v, want %d", len(probs), probs, len(tc.want))
			}
			for i, w := range tc.want {
				if got := probs[i].String(); !strings.Contains(got, w) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got, w)
				}
			}
			if tc.name == "no suggestion when far" && probs[0].Suggest != "" {
				t.Errorf("suggested %q for a far key", probs[0].Suggest)
			}
		})
	}
}

func TestSyntaxError(t *testing.T) {
	if _, err := CheckTw([]byte(`{"tailnet": `)); err == nil {
		t.Fatal("want a syntax error")
	}
}

func TestUnlisted(t *testing.T) {
	type cfg struct {
		Name    string `json:"name"`
		Bogus   int    `json:"bogus"`
		Tailnet struct {
			Egress struct {
				Via   string `json:"via"`
				Proxy string `json:"proxy"`
			} `json:"egress"`
			Objects map[string]struct {
				TTL  string `json:"ttl"`
				Idle string `json:"idle"`
			} `json:"objects"`
		} `json:"tailnet"`
		Public *struct {
			Limits *struct {
				RPS float64 `json:"rps,omitempty"`
			} `json:"limits"`
		} `json:"public"`
		Raw json.RawMessage `json:"containers"`
	}
	got := strings.Join(Unlisted(reflect.TypeFor[cfg]()), " ")
	if want := "bogus tailnet.egress.proxy tailnet.objects.*.idle"; got != want {
		t.Fatalf("Unlisted = %q, want %q", got, want)
	}
}
