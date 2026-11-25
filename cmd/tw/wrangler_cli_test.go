package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestCLIDeployWranglerTOML (issue #19): an existing Workers project with
// only a wrangler.toml (no tailworks.jsonc, no wrangler.jsonc) deploys with
// `tw deploy`, unchanged, on a real single-process stack (tw up against
// testcontrol and the fake Tailscale API): preflight names its default
// tag, one warning per ignored key, then it serves over the tailnet,
// top level and `--env staging` side by side.
func TestCLIDeployWranglerTOML(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	fts := newFakeTS(t)
	api := httptest.NewServer(fts)
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-wr-")
	work := t.TempDir()
	keyFile := filepath.Join(work, "api.env")
	writeFakeOAuthFile(t, keyFile)
	var client *nettest.Node
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: "ttest.ts.net", OAuthClientFile: keyFile,
		ControlURL: ctl.URL, TSAPIURL: api.URL, Prefix: "tw-w-", Res: cliflow.NewResults("hermetic-wrangler", "ttest.ts.net"), SingleProcess: true}
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-5000):]), 5000))
			}
		}
		e.Tw("down", "--purge")
		if client != nil {
			client.Close()
		}
		os.RemoveAll(state)
	})
	owners := []string{"autogroup:admin", "tag:tw"}
	fts.mu.Lock() // the fake API is already serving
	fts.policy, err = policy.MergeWith(fts.policy, policy.Snippet{TagOwners: map[string][]string{
		"tag:tw-wl-wtoml":         owners,
		"tag:tw-wl-wtoml-staging": owners,
	}}, policy.Options{})
	fts.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	e.MustJSON(nil, e.UpCommand()...)

	app := filepath.Join(work, "toml-app")
	if err := os.CopyFS(app, os.DirFS("../../e2e/wrangler/testdata/toml-app")); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, app)

	// The default tag follows the workload name (tag:tw-wl-<name>); one
	// with no tagOwners stops at preflight with the snippet. The warnings
	// come first, one per key.
	so, se, _, err := e.Tw("deploy", app, "--name", "wtoml-noowner")
	if err == nil || !strings.Contains(se, "acl_denied") || !strings.Contains(so, `"tag:tw-wl-wtoml-noowner": ["autogroup:admin", "tag:tw"],`) {
		t.Fatalf("deploy without tagOwners should fail preflight naming the default tag: %v\n%s\n%s", err, so, se)
	}
	for _, w := range []string{
		"› wrangler.toml: vectorize: no such binding on tailworks: env.CACHE will be undefined",
		"› wrangler.toml: routes: ignored",
		"› wrangler.toml: workers_dev: ignored",
		"› no tailworks.jsonc: using defaults (workload node tag:tw-wl-wtoml-noowner;",
	} {
		if !strings.Contains(so, w) {
			t.Errorf("deploy output lacks %q:\n%s", w, so)
		}
	}

	type dep struct {
		Workload string `json:"workload"`
		Endpoint struct {
			MagicDNS string `json:"magicDNS"`
		} `json:"endpoint"`
		Warnings []string `json:"warnings"`
	}
	var top, stg dep
	e.MustJSON(&top, "deploy", app)
	e.MustJSON(&stg, "deploy", app, "--env", "staging")
	if top.Workload != "wtoml" || stg.Workload != "wtoml-staging" || len(top.Warnings) != 4 {
		t.Fatalf("deploys: %+v %+v", top, stg)
	}
	if after := snapshotDir(t, app); after != before {
		t.Fatalf("tw deploy changed the project:\nbefore %s\nafter  %s", before, after)
	}

	client, err = nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-wclient", filepath.Join(work, "client"))
	if err != nil {
		t.Fatal(err)
	}
	hc := client.HTTPClient(45 * time.Second)
	get := func(magicDNS string) map[string]any {
		ip := cliflow.PeerIP(t, client, magicDNS, 60*time.Second)
		resp, err := hc.Get("http://" + ip + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &m) != nil {
			t.Fatalf("%s: %d %s", magicDNS, resp.StatusCode, b)
		}
		return m
	}
	r := get(top.Endpoint.MagicDNS)
	if r["greeting"] != "hello from wrangler.toml" || r["build"] != "top" || r["shout"] != "ALIAS" || r["wasm"] != true ||
		!strings.Contains(r["notes"].(string), "Text module") || r["hello"] != "plain text module\n" || r["count"] != float64(1) || r["cache"] != "undefined" {
		t.Fatalf("top level: %v", r)
	}
	if r = get(top.Endpoint.MagicDNS); r["count"] != float64(2) {
		t.Fatalf("the internal Durable Object keeps state: %v", r)
	}
	if r = get(stg.Endpoint.MagicDNS); r["greeting"] != "hello from staging" || r["build"] != "staging" || r["count"] != float64(1) {
		t.Fatalf("--env staging: %v", r)
	}
}

// snapshotDir lists dir's files, sizes and mtimes (tw deploy must not write into
// the project).
func snapshotDir(t *testing.T, dir string) string {
	var b strings.Builder
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			fmt.Fprintf(&b, "%s:%v:%d:%d ", rel, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}
