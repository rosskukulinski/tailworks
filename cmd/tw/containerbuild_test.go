package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd"
)

// TestContainerKeyWarnings: one line per dropped containers key, however
// many entries set it; name and scheduling_policy "durable_object" (what
// Cloudflare's examples write) get none.
func TestContainerKeyWarnings(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"name": "a", "main": "m.ts", "containers": []any{
		map[string]any{"class_name": "A", "image": "./Dockerfile", "name": "a-app", "scheduling_policy": "durable_object",
			"rollout_step_percentage": 25, "image_build_context": ".", "image_vars": map[string]any{"X": "1"}, "max_instances": 2},
		map[string]any{"class_name": "B", "image": "docker.io/x/y:1", "rollout_step_percentage": 50, "scheduling_policy": "default"},
	}}
	got := wranglerWarnings("wrangler.jsonc", cfg, "esbuild")
	want := []string{"wrangler.jsonc: containers[].rollout_step_percentage: ignored by tailworks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	cfg["containers"] = []any{map[string]any{"class_name": "A", "image": "x:1", "scheduling_policy": "regional", "constraints": map[string]any{}}}
	got = wranglerWarnings("wrangler.jsonc", cfg, "esbuild")
	if len(got) != 2 || !strings.Contains(got[0], "containers[].constraints") || !strings.Contains(got[1], "scheduling_policy") {
		t.Fatalf("got %q", got)
	}
}

// TestRebaseContainerBuildPaths: a redirected config's build contexts and
// named-image Dockerfiles are rebased like its image.
func TestRebaseContainerBuildPaths(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"containers": []any{map[string]any{"class_name": "A", "image": "./Dockerfile", "image_build_context": "../..",
		"images": map[string]any{"py": map[string]any{"dockerfile": "py/Dockerfile", "build_context": "py"}, "ref": map[string]any{"image": "docker.io/x/y:1"}}}}}
	rebaseWranglerPaths(cfg, "dist/w")
	m := cfg["containers"].([]any)[0].(map[string]any)
	ims := m["images"].(map[string]any)
	got := []any{m["image"], m["image_build_context"], ims["py"].(map[string]any)["dockerfile"], ims["py"].(map[string]any)["build_context"], ims["ref"].(map[string]any)["image"]}
	want := []any{"./dist/w/Dockerfile", ".", "dist/w/py/Dockerfile", "dist/w/py", "docker.io/x/y:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

// TestContainersNotes: tw up's tier-line note and doctor's fix for the
// host-side reasons.
func TestContainersNotes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tiers []string
		want  string
	}{
		{[]string{"process", "microvm", "containers"}, ", containers"},
		{[]string{"process", "microvm"}, "; containers off: see tw doctor"},
		{[]string{"process"}, ""},
	} {
		if got := containersUpNote(hostd.StatusView{Tiers: c.tiers}); got != c.want {
			t.Errorf("%v: %q", c.tiers, got)
		}
	}
	if f := containersFix("mkfs.erofs not found (install erofs-utils)"); !strings.Contains(f, "brew install erofs-utils") {
		t.Errorf("erofs fix %q", f)
	}
	if f := containersFix("guest kernel has no erofs"); f != "" {
		t.Errorf("unknown reason fix %q", f)
	}
	if st, d, _ := doctorContainers(nil); st != "skip" || !strings.Contains(d, "not running") {
		t.Errorf("no state: %s %s", st, d)
	}
	hosts := []containersHost{{HostID: "h1", Status: "live", Tiers: []string{"process"}}, {HostID: "h2", Status: "dead", Tiers: []string{"process"}}}
	if f := containersNoHostFix(nil, hosts); f != "live hosts: h1 (process); tw doctor on a host says why it has no containers" {
		t.Errorf("no_host fix %q", f)
	}
}
