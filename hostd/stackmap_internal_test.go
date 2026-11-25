package hostd

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestInstanceMapStack: an instance maps exception stacks with its bundle's
// source map (parsed once); a broken map leaves the frames alone.
func TestInstanceMapStack(t *testing.T) {
	t.Parallel()
	h := &Host{log: slog.New(slog.DiscardHandler)}
	// dist/index.js line 1 col 0 -> src/index.ts 1:1; col 10 -> 3:5.
	app := &bundle.App{SourceMaps: map[string][]byte{
		"dist/index.js.map":  []byte(`{"version":3,"sources":["src/index.ts"],"names":[],"mappings":"AAAA,UAEI"}`),
		"dist/broken.js.map": []byte(`{"version":3,"sources":[],"mappings":"!"}`),
	}}
	inst := &instance{h: h, w: &workload{name: "w"}, app: app}
	stack := "Error: x\n    at f (dist/index.js:1:12)\n    at g (dist/broken.js:1:1)\n    at __tw_rt.js:5:6"
	got := inst.mapStack(stack)
	want := "Error: x\n    at f (src/index.ts:3:5)\n    at g (dist/broken.js:1:1)\n    at __tw_rt.js:5:6"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if m, ok := inst.stackMaps.maps["dist/broken.js"]; !ok || m != nil {
		t.Fatal("the broken map wasn't remembered as nil")
	}
	// No maps: unchanged.
	plain := &instance{h: h, w: &workload{name: "w"}, app: &bundle.App{}}
	if got := plain.mapStack(stack); got != stack || !strings.Contains(got, "dist/index.js:1:12") {
		t.Fatalf("changed without a map: %s", got)
	}
}
