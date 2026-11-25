package hostd

import (
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestInspectorArgsNeedBothDevFlags (security ledger C-inspector): the
// workerd command line hostd builds (workerdCommand, the one start() runs)
// carries --inspector-addr only when the host is tw dev
// (Config.ForceProcessTier) and was asked for it (Config.WorkerdInspector).
// tw-hostd (internal/hostdrun) sets neither; a host with WorkerdInspector
// alone still runs workerd without an inspector. Neither flag can come from
// a deployment or from mgmt: they are hostd's own Config.
func TestInspectorArgsNeedBothDevFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		inspector, dev bool
		want           bool
	}{
		{"tw-hostd (neither set)", false, false, false},
		{"WorkerdInspector without ForceProcessTier", true, false, false},
		{"tw dev without --inspector-port", false, true, false},
		{"tw dev --inspector-port", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Host{log: slog.New(slog.DiscardHandler), cfg: Config{WorkerdBin: "/bin/true", WorkerdInspector: c.inspector, ForceProcessTier: c.dev}}
			inst := &instance{h: h, dir: t.TempDir(), res: &gen.Result{Config: "/nonexistent/config.capnp"}}
			cmd, err := inst.workerdCommand()
			if err != nil {
				t.Fatal(err)
			}
			has := slices.ContainsFunc(cmd.Args, func(a string) bool { return strings.HasPrefix(a, "--inspector-addr") })
			if has != c.want {
				t.Fatalf("workerd args %q: --inspector-addr present %v, want %v", cmd.Args, has, c.want)
			}
			if got := inst.inspector != ""; got != c.want {
				t.Fatalf("instance inspector address %q, want one: %v", inst.inspector, c.want)
			}
		})
	}
}
