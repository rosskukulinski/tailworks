package gen_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// expFlagFiles is a built bundle whose wrangler config lists three
// experimental flags and whose Worker and facet object try to import
// workerd:unsafe (behind unsafe_module).
func expFlagFiles() map[string][]byte {
	const js = `import { DurableObject } from "cloudflare:workers";
async function probe() {
  try {
    const m = await import("workerd:unsafe");
    return Response.json({ unsafe: Object.keys(m).length > 0 || m.default !== undefined });
  } catch (e) {
    return Response.json({ unsafe: false, error: String(e) });
  }
}
export class Facet extends DurableObject { fetch() { return probe(); } }
export default { fetch() { return probe(); } };
`
	return map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "expflags", "main": "index.js", "compatibility_date": "2026-04-01",
  "compatibility_flags": ["nodejs_compat", "unsafe_module", "rtti_api", "precise_timers"],
  "durable_objects": {"bindings": [{"name": "FACET", "class_name": "Facet"}]}}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"tags": ["tag:expflags"]}}`),
		"index.js":        []byte(js),
	}
}

// TestExperimentalFlagsRefused (#337, ledger H-compat-flags): tenant code
// that lists unsafe_module, rtti_api and precise_timers is refused at
// deploy with the typed experimental_flag error under the default policy
// (the known list, and the pinned workerd asked directly), so it never
// imports workerd:unsafe. On master Generate accepted the flags and the
// Worker imported it. With the operator switch the old behaviour is back
// in the Worker; a facet's Dynamic Worker refuses the flags on its own
// (the worker loader doesn't inherit --experimental), as it did on master.
func TestExperimentalFlagsRefused(t *testing.T) {
	wdBin, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	app, err := bundle.Parse(expFlagFiles())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"precise_timers", "rtti_api", "unsafe_module"}
	for _, c := range []struct {
		name string
		bin  string
	}{{"KnownList", ""}, {"AskWorkerd", wdBin}} {
		t.Run("RefusedByDefault/"+c.name, func(t *testing.T) {
			dir, err := hostdtest.ShortDir("tw-gen-")
			if err != nil {
				t.Fatal(err)
			}
			_, err = gen.Generate(app, gen.Options{Dir: filepath.Join(dir, "i1"), DOStorage: filepath.Join(dir, "do"), DeploymentID: "d1", Workload: app.Name, WorkerdBin: c.bin})
			var ee *gen.ExperimentalFlagError
			if !errors.As(err, &ee) {
				t.Fatalf("Generate with compatibility_flags %v: err = %v, want *gen.ExperimentalFlagError", app.CompatFlags, err)
			}
			if !slices.Equal(ee.Flags, want) || !strings.HasPrefix(err.Error(), gen.ErrExperimentalFlag+": ") || !strings.Contains(err.Error(), "-allow-experimental-flags") {
				t.Fatalf("refusal = %v (flags %v)", err, ee.Flags)
			}
		})
	}

	t.Run("StableFlagsPass", func(t *testing.T) {
		in := start(t, hostdtest.Fixture("compat"), nil) // nodejs_compat
		if code, _, r := in.get("/node", wl); code != 200 || r["base64"] != "dHc=" {
			t.Fatalf("%d %v", code, r)
		}
	})

	t.Run("AllowedWithOperatorSwitch", func(t *testing.T) {
		in := startFilesOpts(t, expFlagFiles(), func(o *gen.Options) { o.AllowExperimentalFlags, o.WorkerdBin = true, wdBin })
		if code, _, r := in.get("/", wl); code != 200 || r["unsafe"] != true {
			t.Fatalf("Worker: %d %v", code, r)
		}
	})
}
