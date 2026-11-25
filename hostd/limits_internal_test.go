package hostd

import (
	"slices"
	"testing"
)

// TestHeapCapDefaults (#115): 0 takes the default, a negative value turns
// the cap off, anything else is used as given.
func TestHeapCapDefaults(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ cfg, want int }{{0, DefaultWorkerdHeapMiB}, {-1, 0}, {256, 256}} {
		h := &Host{cfg: Config{WorkerdHeapMiB: c.cfg}}
		if got := h.workerdHeapMiB(); got != c.want {
			t.Errorf("WorkerdHeapMiB %d: %d, want %d", c.cfg, got, c.want)
		}
	}
	if got := heapMiB(0, DefaultNodeHeapMiB); got != 2048 {
		t.Errorf("node default %d", got)
	}
}

// TestNodeHeapArgs (#115): a Node-tier app gets --max-old-space-size ahead
// of its own arguments, unless it already sets one in node.args or
// NODE_OPTIONS (the app's own choice wins) or the cap is off.
func TestNodeHeapArgs(t *testing.T) {
	t.Parallel()
	main := []string{"--import", "/i/secrets.mjs", "server.js"}
	if got := nodeHeapArgs(main, nil, 2048); !slices.Equal(got, append([]string{"--max-old-space-size=2048"}, main...)) {
		t.Fatalf("default: %q", got)
	}
	own := []string{"--max-old-space-size=8192", "server.js"}
	if got := nodeHeapArgs(own, nil, 2048); !slices.Equal(got, own) {
		t.Fatalf("node.args sets its own: %q", got)
	}
	if got := nodeHeapArgs([]string{"--max_old_space_size=64", "s.js"}, nil, 2048); len(got) != 2 {
		t.Fatalf("underscore spelling: %q", got)
	}
	env := map[string]string{"NODE_OPTIONS": "--enable-source-maps --max-old-space-size=4096"}
	if got := nodeHeapArgs(main, env, 2048); !slices.Equal(got, main) {
		t.Fatalf("NODE_OPTIONS sets its own: %q", got)
	}
	if got := nodeHeapArgs(main, nil, 0); !slices.Equal(got, main) {
		t.Fatalf("cap off: %q", got)
	}
}
